package cli

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/gatherdeal/cli/contracts"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/crm"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	wa "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/update"
)

// Commands returns the command tree. The same declarations drive parsing,
// help and `gatherdeal commands`.
func Commands() []*Command {
	cmds, err := commands()
	if err != nil {
		panic(err)
	}
	return cmds
}

func commands() ([]*Command, error) {
	loginFlags := append([]FlagSpec{}, targetFlags...)
	loginFlags = append(loginFlags,
		FlagSpec{Name: "credential-store", Kind: kindString, Default: "keyring", Values: []string{"keyring", "file"}, Usage: "Where to keep the credential: the OS keyring, or an owner-only file under the config directory."},
		FlagSpec{Name: "device-name", Kind: kindString, Usage: "Label shown on the approval page (default: this machine's host name)."},
		FlagSpec{Name: "lifetime", Kind: kindString, Values: []string{"30d", "90d", "180d", "365d", "never"}, Usage: "Suggested lifetime; the approver chooses the final one."},
		FlagSpec{Name: "no-wait", Kind: kindBool, Usage: "Start the login, print the approval code in the JSON envelope (exit 9) and return without waiting."},
		FlagSpec{Name: "continue", Kind: kindBool, Usage: "Finish a login started with --no-wait: wait for the approval and store the credential."},
	)
	listFlags := append([]FlagSpec{}, targetFlags...)
	listFlags = append(listFlags, pageFlags(crm.ListOperation, "contacts")...)
	listFlags = append(listFlags,
		FlagSpec{Name: "dry-run", Kind: kindBool, Usage: "Preview the contact selection and output fields without reading contacts."},
		flagSearch,
		sortFlag(crm.ListOperation),
		FlagSpec{Name: "tag", Kind: kindStrings, Usage: "Only contacts with a tag of this name (repeatable; any match)."},
		FlagSpec{Name: "tag-id", Kind: kindStrings, Usage: "Only contacts with this tag id (repeatable; any match)."},
		flagOutlet,
	)
	listFlags = append(listFlags, expansionFlags(crm.ListOperation)...)
	getFlags := append([]FlagSpec{}, targetFlags...)
	getFlags = append(getFlags, flagOutlet)
	getFlags = append(getFlags, expansionFlags(crm.GetOperation)...)

	cmds := []*Command{
		{Path: []string{"auth", "profiles", "list"}, Summary: "List local profiles, pinned identities and cached grant summaries without network requests.", Output: "json", ReadOnly: true, Run: runProfilesList},
		{
			Path: []string{"auth", "login"}, Summary: "Log in through the browser approval page and store the access the approver grants.",
			Flags: loginFlags, Output: "json", Network: true, ReadOnly: false, Run: runLogin,
		},
		{
			Path: []string{"auth", "status"}, Summary: "Show the organization, outlets, permissions and expiry of the current access.",
			Flags: append([]FlagSpec{}, targetFlags...), Output: "json", Network: true, ReadOnly: true, Run: runStatus,
		},
		{
			Path: []string{"auth", "logout"}, Summary: "Revoke the current access on the server and delete it locally.",
			Flags: append([]FlagSpec{}, targetFlags...), Output: "json", Network: true, ReadOnly: false, Run: runLogout,
		},
		{
			Summary:   "List CRM contacts in the outlets this access may read.",
			Operation: crm.ListOperation, Flags: listFlags, Record: crm.Contact{}, Output: "json", Network: true, Run: runContactsList,
		},
		{
			Summary: "Show one CRM contact.", Operation: crm.GetOperation,
			Args:  []ArgSpec{{Name: "id", Usage: "Contact id (UUID)."}},
			Flags: getFlags, Record: crm.Contact{}, Output: "json", Network: true, Run: runContactsGet,
		},
		{
			Path: []string{"commands"}, Summary: "Machine-readable list of commands, flags, exit codes and environment variables.",
			Args: []ArgSpec{{Name: "path", Usage: "Command path prefix of whole segments, such as crm or crm contacts; lists only the commands under it.", Variadic: true}},
			Flags: []FlagSpec{
				{Name: "search", Kind: kindString, Usage: "Only commands whose path, summary or resource summary contains this text (case-insensitive)."},
			},
			Output: "json", ReadOnly: true, Run: runCommands,
		},
		{
			Path: []string{"agent-context"}, Summary: "Instructions for an AI agent using this CLI, or for one of its apps (plain text).",
			Args:   []ArgSpec{{Name: "app", Usage: "App whose notes and resource index to print, such as crm; without it, the general guidance and the app index.", Optional: true}},
			Output: "text", ReadOnly: true, Run: runAgentContext,
		},
		skillsInstallCommand(),
		{
			Path: []string{"skills", "show"}, Summary: "Print the GatherDeal agent skill (plain text).",
			Output: "text", ReadOnly: true, Run: runSkillsShow,
		},
		updateCommand(),
		uninstallCommand(),
		{
			Path: []string{"version"}, Summary: "Show the CLI version (no network).",
			Output: "json", ReadOnly: true, Run: runVersion,
		},
	}
	cmds = append(cmds, whatsappCommands()...)
	cmds = append(cmds, outletCommands()...)
	cmds = append(cmds, generatedCommands(cmds)...)
	// API paths and app ownership come from the published contracts.
	m, err := contracts.Load()
	if err != nil {
		return nil, err
	}
	for _, c := range cmds {
		if c.Operation == "" {
			continue
		}
		op := contracts.MustOperation(c.Operation)
		c.Path = strings.Fields(op.Command)
		c.ReadOnly = op.ReadOnly()
		if c.Presentation != nil {
			c.Path = append([]string(nil), c.Presentation.Path...)
		}
		for _, b := range m.Bundles {
			for _, published := range b.Operations {
				if published.PublicID == c.Operation {
					c.App = b.Service
				}
			}
		}
		if len(c.Path) == 0 || (c.Presentation == nil && (len(c.Path) < 3 || c.Path[0] != c.App)) {
			panic("contract command path must start with its app: " + c.Operation)
		}
		// Only CRM and WhatsApp bindings pass --if-match to their generated client.
		if c.App != crmv1.Service && c.App != wa.Service && requiresIfMatch(c.Operation) {
			panic("bind the If-Match option of the generated client for " + c.Operation)
		}
	}
	for _, c := range cmds {
		addShapingFlags(c)
	}
	return registerFormerCommands(cmds, m)
}

func registerFormerCommands(cmds []*Command, m *contracts.Manifest) ([]*Command, error) {
	occupied := []string{"help"}
	for _, c := range cmds {
		occupied = append(occupied, c.Name())
		if c.Presentation != nil {
			occupied = append(occupied, strings.Join(strings.Fields(c.Contract().Command), " "))
		}
	}
	var aliases []*Command
	for _, c := range cmds {
		op, _ := m.Operation(c.Operation)
		for _, former := range op.FormerIDs {
			path := strings.Fields(former.Command)
			name := strings.Join(path, " ")
			if name == "" {
				return nil, fmt.Errorf("former operation %s has an empty command path", former.PublicID)
			}
			for _, other := range occupied {
				if name == other || strings.HasPrefix(name, other+" ") || strings.HasPrefix(other, name+" ") {
					return nil, fmt.Errorf("former command %q (%s) conflicts with command %q", name, former.PublicID, other)
				}
			}
			alias := *c
			alias.Path = path
			alias.Presentation = nil
			alias.aliasDeprecation = &output.Deprecation{Since: former.Since, Successor: c.Name()}
			aliases = append(aliases, &alias)
			occupied = append(occupied, name)
		}
	}
	return append(cmds, aliases...), nil
}

// deprecation reports the command's deprecation, or nil.
func (c *Command) deprecation() *output.Deprecation {
	if c.aliasDeprecation != nil {
		return c.aliasDeprecation
	}
	if c.Operation == "" {
		return nil
	}
	d := c.Contract().Deprecation
	if d == nil {
		return nil
	}
	return &output.Deprecation{Since: d.Since, Successor: contracts.MustOperation(d.Successor).Command}
}

type commandInfo struct {
	App     string `json:"app,omitempty"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	// Operation is the public id of the API operation the command calls.
	Operation string     `json:"operation,omitempty"`
	Args      []ArgSpec  `json:"args"`
	Flags     []FlagSpec `json:"flags"`
	// RecordFields are the names --fields accepts.
	RecordFields []string            `json:"record_fields,omitempty"`
	FilterFields map[string][]string `json:"filter_fields,omitzero"`
	Output       string              `json:"output"`
	Network      bool                `json:"network"`
	ReadOnly     bool                `json:"read_only"`
	// Deprecated is set on a command that will be removed in a later
	// release.
	Deprecated *output.Deprecation `json:"deprecated,omitempty"`
}

type envVarInfo struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
}

type commandsData struct {
	EnvelopeSchemaVersion int                   `json:"envelope_schema_version"`
	Commands              []commandInfo         `json:"commands"`
	ExitCodes             []output.ExitCodeInfo `json:"exit_codes"`
	Environment           []envVarInfo          `json:"environment"`
}

func runCommands(_ context.Context, inv *Invocation) (any, error) {
	cmds := inv.commands
	prefix := inv.Args
	n := commandTree(cmds)
	for i, part := range prefix {
		if n.children[part] == nil {
			if n.command != nil {
				return nil, usageError("%q is a command and has no subcommands", strings.Join(prefix[:i], " "))
			}
			return nil, usageError("unknown command path %q; valid next segments: %s", strings.Join(prefix[:i+1], " "), strings.Join(n.choices(), ", "))
		}
		n = n.children[part]
	}
	search := strings.ToLower(inv.str("search"))
	out := []commandInfo{}
	for _, c := range cmds {
		if len(c.Path) < len(prefix) || !slices.Equal(c.Path[:len(prefix)], prefix) || !c.matches(search) {
			continue
		}
		args, flags := c.Args, c.Flags
		if args == nil {
			args = []ArgSpec{}
		}
		if flags == nil {
			flags = []FlagSpec{}
		}
		out = append(out, commandInfo{App: c.App, Name: c.Name(), Summary: c.Summary, Operation: c.Operation, Args: args, Flags: flags, RecordFields: recordFields(c.Record), FilterFields: c.FilterFields, Output: c.Output, Network: c.Network, ReadOnly: c.ReadOnly, Deprecated: c.deprecation()})
	}
	return commandsData{
		EnvelopeSchemaVersion: output.SchemaVersion,
		Commands:              out,
		ExitCodes:             output.ExitCodes(),
		Environment: []envVarInfo{
			{Name: EnvToken, Purpose: "A delegated-access token (gd_dat_…) for the built-in production profile. Never persisted; not combinable with --server or --profile."},
			{Name: "XDG_CONFIG_HOME", Purpose: "Base of the config directory (Linux/macOS); default ~/.config."},
			{Name: "APPDATA", Purpose: "Base of the config directory on Windows."},
			{Name: "LC_ALL / LC_MESSAGES / LANG", Purpose: "Standard locale; the first one set chooses the language of server error text (en, ms, zh-Hans, zh-Hant) unless --lang is given."},
			{Name: update.EnvLauncher, Purpose: "Set to \"npm\" by the npm launcher only; `update` reads it to confirm an npm install. Never set it yourself."},
			{Name: "LOCALAPPDATA", Purpose: "Windows: locates the documented install directory (%LOCALAPPDATA%\\Programs\\gatherdeal) for `update`."},
			{Name: "HTTPS_PROXY / HTTP_PROXY / NO_PROXY", Purpose: "Standard proxy settings."},
		},
	}, nil
}

// matches reports whether the lowercase text occurs in the command's path,
// its summary or the summary of the group that holds it.
func (c *Command) matches(text string) bool {
	parent := groupSummaries[strings.Join(c.Path[:len(c.Path)-1], " ")]
	for _, s := range []string{c.Name(), c.Summary, parent} {
		if strings.Contains(strings.ToLower(s), text) {
			return true
		}
	}
	return false
}

func runVersion(context.Context, *Invocation) (any, error) {
	return buildinfo.Get(runtime.GOOS, runtime.GOARCH, runtime.Version()), nil
}
