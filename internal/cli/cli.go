// Package cli implements the gatherdeal command tree.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/lang"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/store"
	"github.com/gatherdeal/cli/internal/update"
)

// Env is everything a command may touch outside its arguments. Tests build
// their own; main builds the real one.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads the environment. Only GATHERDEAL_TOKEN,
	// GATHERDEAL_LAUNCHER (set by the npm launcher) and the standard
	// XDG_CONFIG_HOME / APPDATA / LOCALAPPDATA and LC_ALL / LC_MESSAGES /
	// LANG are read by the CLI itself.
	Getenv func(string) string
	// Interactive is true when a human can answer prompts (stdin and stderr
	// are terminals).
	Interactive bool
	// Keyring is the OS keyring (a fake in tests).
	Keyring store.Keyring
	// HTTPBase is the base transport; nil means the default transport.
	HTTPBase http.RoundTripper
	// Now and Sleep are clocks; nil means the real ones.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Hostname names this machine for the default device label.
	Hostname func() (string, error)
	// UpdateVerifier returns the release verifier of `update` and its
	// trust-root source; nil uses Sigstore with the TUF-refreshed or
	// embedded trusted root.
	UpdateVerifier func(context.Context) (update.ManifestVerifier, string, error)
	// UpdateHost describes the running executable for install detection;
	// nil inspects this process.
	UpdateHost *update.Host

	stdinReader *bufio.Reader
	redactor    *output.Redactor
}

// redact returns the run's redactor; every credential the CLI handles is
// registered with it before any output.
func (e *Env) redact() *output.Redactor {
	if e.redactor == nil {
		e.redactor = &output.Redactor{}
	}
	return e.redactor
}

// addSecret registers a credential so no output can contain it.
func (e *Env) addSecret(s string) { e.redact().Add(s) }

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// stderrf writes a human diagnostic. The format is a trusted constant;
// every string-like argument is redacted and stripped of terminal control
// characters, because it may come from the server or the command line.
func (e *Env) stderrf(format string, args ...any) {
	clean := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			clean[i] = e.redact().Terminal(v)
		case error:
			clean[i] = e.redact().Terminal(v.Error())
		case fmt.Stringer:
			clean[i] = e.redact().Terminal(v.String())
		default:
			clean[i] = a
		}
	}
	fmt.Fprintf(e.Stderr, format, clean...)
}

// readLine reads one answer from stdin.
func (e *Env) readLine() string {
	if e.stdinReader == nil {
		e.stdinReader = bufio.NewReader(e.Stdin)
	}
	line, _ := e.stdinReader.ReadString('\n')
	return strings.TrimSpace(line)
}

// confirm asks a yes/no question on stderr; only "y" or "yes" is yes.
func (e *Env) confirm(format string, args ...any) bool {
	e.stderrf(format, args...)
	ans := strings.ToLower(e.readLine())
	return ans == "y" || ans == "yes"
}

// flagKind is the type of a flag value.
type flagKind string

const (
	kindString  flagKind = "string"
	kindBool    flagKind = "bool"
	kindInt     flagKind = "int"
	kindStrings flagKind = "string[]"
)

// FlagSpec declares one flag; it drives parsing, help and `commands`.
type FlagSpec struct {
	Name    string   `json:"name"`
	Kind    flagKind `json:"type"`
	Default string   `json:"default,omitempty"`
	Usage   string   `json:"usage"`
	Values  []string `json:"values,omitempty"`
}

// ArgSpec declares one positional argument. Only the last argument may be
// optional (zero or one value) or variadic (zero or more values).
type ArgSpec struct {
	Name     string `json:"name"`
	Usage    string `json:"usage"`
	Optional bool   `json:"optional,omitempty"`
	Variadic bool   `json:"variadic,omitempty"`
}

// argUsage renders an argument for usage lines: <name>, [<name>] or
// [<name>...].
func (a ArgSpec) argUsage() string {
	switch {
	case a.Variadic:
		return "[<" + a.Name + ">...]"
	case a.Optional:
		return "[<" + a.Name + ">]"
	}
	return "<" + a.Name + ">"
}

// Command is one leaf of the command tree.
type Command struct {
	// App is the owning contract service; empty for cross-cutting commands.
	App     string
	Path    []string
	Summary string
	Args    []ArgSpec
	Flags   []FlagSpec
	// Output is "json" (the envelope) or "text".
	Output string
	// Network is true when the command may contact the server.
	Network bool
	// ReadOnly is false for commands that change local or server state.
	ReadOnly bool
	// Operation is the public id (contracts/manifest.json) of the API
	// operation the command calls, if any.
	Operation string
	// Record is a zero value of the record type in data (one object, or an
	// array of them) of a list or get command. It enables --fields, whose
	// names are its JSON members.
	Record any
	// Presentation optionally overrides the generated command path, never service ownership.
	Presentation *CommandPresentation
	Write        *WritePolicy
	Run          func(ctx context.Context, inv *Invocation) (any, error)
}

// Name is the space-joined path.
func (c *Command) Name() string { return strings.Join(c.Path, " ") }

var (
	flagAcceptServerRisk = FlagSpec{Name: "accept-server-risk", Kind: kindString, Usage: "Confirm a non-default --server during auth login without a terminal prompt; ignored by other commands. Must equal the server origin exactly. Only pass it if you chose that server yourself."}
	flagProfile          = FlagSpec{Name: "profile", Kind: kindString, Usage: "Local profile name (default: \"default\", the built-in production profile; with --server: the server host)."}
	flagServer           = FlagSpec{Name: "server", Kind: kindString, Usage: "Non-default GatherDeal server origin. Only pass it if you chose that server yourself."}
	flagLang             = FlagSpec{Name: "lang", Kind: kindString, Usage: "Language of server error text: a tag such as ms, zh-Hans, zh-Hant or en, or an Accept-Language list (default: from LC_ALL, LC_MESSAGES or LANG; otherwise en). Error codes never change."}
)

// targetFlags are accepted by every command that talks to a server.
var targetFlags = []FlagSpec{flagProfile, flagServer, flagLang, flagAcceptServerRisk}

// Invocation is a parsed command line.
type Invocation struct {
	Env     *Env
	Command *Command
	Args    []string
	strs    map[string]*string
	bools   map[string]*bool
	ints    map[string]*int
	lists   map[string]*listValue
	set     map[string]bool
	Meta    output.Meta

	// target is the profile and server the command resolved, if any.
	target *target
	// credentialSource is where the credential the command sent came from
	// (keyring, file or environment), if it loaded one.
	credentialSource string
	// shaping is the parsed --fields and --jq.
	shaping shaping
}

func (inv *Invocation) str(name string) string    { return *inv.strs[name] }
func (inv *Invocation) boolean(name string) bool  { return *inv.bools[name] }
func (inv *Invocation) integer(name string) int   { return *inv.ints[name] }
func (inv *Invocation) list(name string) []string { return inv.lists[name].values }
func (inv *Invocation) isSet(name string) bool    { return inv.set[name] }

type listValue struct{ values []string }

func (l *listValue) String() string     { return strings.Join(l.values, ",") }
func (l *listValue) Set(s string) error { l.values = append(l.values, s); return nil }

// Run executes args (without the program name) and returns the exit code.
func Run(ctx context.Context, env *Env, args []string) int {
	env.redactor = &output.Redactor{}
	if env.Getenv != nil {
		env.addSecret(env.Getenv("GATHERDEAL_TOKEN"))
	}
	node, rest, err := commandTree(Commands()).resolve(args)
	if err != nil {
		return emitError(env, output.Meta{}, err)
	}
	if node.command == nil {
		writeGroupHelp(env.Stdout, node)
		return int(output.ExitOK)
	}
	cmd := node.command
	for _, a := range rest {
		if a == "-h" || a == "--help" || a == "-help" {
			writeCommandHelp(env.Stdout, cmd)
			return int(output.ExitOK)
		}
		if a == "--" {
			break
		}
	}
	inv, err := parse(env, cmd, rest)
	if err != nil {
		return emitError(env, output.Meta{}, err)
	}
	data, err := inv.run(ctx)
	if err != nil {
		err = inv.authRecovery(err)
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			var e *output.Error
			if !errors.As(err, &e) || (e.Exit != output.ExitInterrupted && e.Code != output.CodeWriteOutcomeUnknown) {
				err = output.Errorf(output.ExitInterrupted, output.CodeInterrupted, "interrupted").WithCause(err)
			}
		}
		return emitError(env, inv.Meta, err, data)
	}
	if cmd.Output == "text" {
		if s, ok := data.(string); ok {
			fmt.Fprint(env.Stdout, s)
		}
		return int(output.ExitOK)
	}
	if inv.shaping.active() {
		return inv.writeShaped(ctx, data)
	}
	if err := output.Write(env.Stdout, output.Success(data, inv.Meta), env.redact()); err != nil {
		return int(output.ExitUnclassified)
	}
	return int(output.ExitOK)
}

func emitError(env *Env, meta output.Meta, err error, data ...any) int {
	envl := output.Failure(err, meta)
	if len(data) > 0 {
		envl.Data = data[0]
	}
	if envl.Error != nil {
		env.stderrf("error: %s\n", envl.Error.Message)
		if envl.Error.Detail != "" {
			env.stderrf("detail: %s\n", envl.Error.Detail)
		}
		if envl.Hint != "" {
			env.stderrf("hint: %s\n", envl.Hint)
		}
	}
	_ = output.Write(env.Stdout, envl, env.redact())
	return int(envl.ExitCode)
}

func usageError(format string, args ...any) *output.Error {
	return output.Errorf(output.ExitUsage, output.CodeUsage, format, args...).
		WithHint("Run `gatherdeal commands` or `gatherdeal <command> --help`.")
}

// parse binds flags (which may appear before or after positional args).
func parse(env *Env, cmd *Command, args []string) (*Invocation, error) {
	inv := &Invocation{
		Env: env, Command: cmd,
		strs: map[string]*string{}, bools: map[string]*bool{}, ints: map[string]*int{},
		lists: map[string]*listValue{}, set: map[string]bool{},
	}
	fs := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, f := range cmd.Flags {
		switch f.Kind {
		case kindString:
			inv.strs[f.Name] = fs.String(f.Name, f.Default, f.Usage)
		case kindBool:
			inv.bools[f.Name] = fs.Bool(f.Name, f.Default == "true", f.Usage)
		case kindInt:
			def, _ := strconv.Atoi(f.Default)
			inv.ints[f.Name] = fs.Int(f.Name, def, f.Usage)
		case kindStrings:
			lv := &listValue{}
			inv.lists[f.Name] = lv
			fs.Var(lv, f.Name, f.Usage)
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError("%s: %v", cmd.Name(), err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		// fs.Parse stops at the first non-flag or after "--".
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	fs.Visit(func(f *flag.Flag) { inv.set[f.Name] = true })
	required, most := len(cmd.Args), len(cmd.Args)
	if most > 0 && (cmd.Args[most-1].Optional || cmd.Args[most-1].Variadic) {
		required--
		if cmd.Args[most-1].Variadic {
			most = len(positional)
		}
	}
	if len(positional) < required || len(positional) > most {
		names := make([]string, len(cmd.Args))
		for i, a := range cmd.Args {
			names[i] = a.argUsage()
		}
		return nil, usageError("usage: gatherdeal %s %s", cmd.Name(), strings.Join(names, " "))
	}
	if p, ok := inv.strs[flagLang.Name]; ok && inv.set[flagLang.Name] {
		if _, err := lang.Lookup(*p); err != nil {
			return nil, usageError("--lang %q is not a language tag (for example ms, zh-Hans, zh-Hant or en)", *p)
		}
	}
	for _, f := range cmd.Flags {
		if len(f.Values) == 0 || f.Kind != kindString {
			continue
		}
		v := *inv.strs[f.Name]
		if v == "" && !inv.set[f.Name] {
			continue
		}
		ok := false
		for _, allowed := range f.Values {
			ok = ok || v == allowed
		}
		if !ok {
			return nil, usageError("--%s must be one of %s", f.Name, strings.Join(f.Values, ", "))
		}
	}
	var err error
	if inv.shaping, err = parseShaping(inv); err != nil {
		return nil, err
	}
	inv.Args = positional
	return inv, nil
}

func writeCommandHelp(w io.Writer, c *Command) {
	var b strings.Builder
	fmt.Fprintf(&b, "gatherdeal %s", c.Name())
	for _, a := range c.Args {
		fmt.Fprintf(&b, " %s", a.argUsage())
	}
	fmt.Fprintf(&b, "\n\n%s\n", c.Summary)
	if len(c.Args) > 0 {
		b.WriteString("\nArguments:\n")
		for _, a := range c.Args {
			fmt.Fprintf(&b, "  %-24s %s\n", a.Name, a.Usage)
		}
	}
	if len(c.Flags) > 0 {
		b.WriteString("\nFlags:\n")
		for _, f := range c.Flags {
			name := "--" + f.Name
			if f.Kind != kindBool {
				name += " <" + string(f.Kind) + ">"
			}
			usage := f.Usage
			if len(f.Values) > 0 {
				usage += " One of: " + strings.Join(f.Values, ", ") + "."
			}
			if f.Default != "" {
				usage += " Default: " + f.Default + "."
			}
			fmt.Fprintf(&b, "  %-28s %s\n", name, usage)
		}
	}
	fmt.Fprint(w, b.String())
}

// IsTerminal reports whether f is a character device.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Contract returns the authoritative operation metadata for help/skills/adapters.
func (c *Command) Contract() contracts.Operation { return contracts.MustOperation(c.Operation) }

type CommandPresentation struct{ Path []string }
