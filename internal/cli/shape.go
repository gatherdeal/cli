package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/itchyny/gojq"

	"github.com/gatherdeal/cli/internal/output"
)

// Output shaping. --fields projects the records in data and keeps the
// envelope; --jq replaces the envelope with the results of a jq expression
// over data. Both apply only to a successful result: a failure always
// prints its normal error envelope and exit code.

var (
	flagFields = FlagSpec{Name: "fields", Kind: kindString, Usage: "Comma-separated top-level fields to keep in each record (the data object, or each element of the data array); the envelope stays. Fields a record lacks stay absent. Names must come from record_fields in `gatherdeal commands`. Applied before --jq."}
	flagJQ     = FlagSpec{Name: "jq", Kind: kindString, Usage: "jq expression over data (not the envelope) of a successful result. Prints each result on its own line instead of the envelope: strings raw, other values as compact JSON. A failure prints the normal error envelope unfiltered. An invalid expression exits 2 before any request."}
)

// maxJQOutput bounds what one --jq run may print (a runaway generator such
// as repeat(.) fails instead of exhausting memory).
const maxJQOutput = 64 << 20

// addShapingFlags gives every JSON command --jq and every record command
// --fields.
func addShapingFlags(c *Command) {
	if c.Output != "json" {
		return
	}
	flags := append([]FlagSpec{}, c.Flags...)
	if c.Record != nil {
		flags = append(flags, flagFields)
	}
	c.Flags = append(flags, flagJQ)
}

// recordFields are the JSON member names of a record type, in declaration
// order (embedded structs inline).
func recordFields(record any) []string {
	var names []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				ft := f.Type
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					walk(ft)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			names = append(names, name)
		}
	}
	if record != nil {
		walk(reflect.TypeOf(record))
	}
	return names
}

// shaping is the parsed --fields and --jq of an invocation.
type shaping struct {
	fields []string
	jq     *gojq.Code
}

// parseShaping validates --fields and compiles --jq; both fail as usage
// errors before the command runs.
func parseShaping(inv *Invocation) (shaping, error) {
	var s shaping
	if inv.isSet(flagFields.Name) {
		known := recordFields(inv.Command.Record)
		var unknown []string
		for _, name := range strings.Split(inv.str(flagFields.Name), ",") {
			name = strings.TrimSpace(name)
			switch {
			case name == "":
				return s, usageError("--fields must be a comma-separated list of field names")
			case !slices.Contains(known, name):
				unknown = append(unknown, name)
			case !slices.Contains(s.fields, name):
				s.fields = append(s.fields, name)
			}
		}
		if len(unknown) > 0 {
			return s, usageError("--fields: unknown field %s; available: %s", strings.Join(unknown, ", "), strings.Join(known, ", "))
		}
	}
	if inv.isSet(flagJQ.Name) {
		expr := inv.str(flagJQ.Name)
		if strings.TrimSpace(expr) == "" {
			return s, usageError("--jq must be a jq expression")
		}
		q, err := gojq.Parse(expr)
		if err != nil {
			return s, usageError("--jq: %v", err)
		}
		// No environment ($ENV is empty) and no input/inputs: the
		// expression sees only data.
		if s.jq, err = gojq.Compile(q); err != nil {
			return s, usageError("--jq: %v", err)
		}
	}
	return s, nil
}

func (s shaping) active() bool { return len(s.fields) > 0 || s.jq != nil }

// project keeps the named members of the data object, or of each object in
// the data array. data is the redacted generic form of the result.
func (s shaping) project(data any) any {
	pick := func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(s.fields))
		for _, f := range s.fields {
			if val, ok := m[f]; ok {
				out[f] = val
			}
		}
		return out
	}
	if arr, ok := data.([]any); ok {
		out := make([]any, len(arr))
		for i, v := range arr {
			out[i] = pick(v)
		}
		return out
	}
	return pick(data)
}

// writeShaped prints a successful result with --fields and --jq applied
// and returns the exit code. A --jq runtime error prints an error envelope
// (exit 2, CLI_JQ_FAILED) whose data is the unshaped result, because the
// command itself, including any write, has already succeeded.
func (inv *Invocation) writeShaped(ctx context.Context, data any) int {
	env := inv.Env
	red := env.redact()
	clean, err := red.Envelope(output.Success(data, inv.Meta))
	if err != nil {
		return int(output.ExitUnclassified)
	}
	shaped := clean.Data
	if len(inv.shaping.fields) > 0 {
		shaped = inv.shaping.project(shaped)
	}
	if inv.shaping.jq == nil {
		clean.Data = shaped
		if err := output.Write(env.Stdout, clean, red); err != nil {
			return int(output.ExitUnclassified)
		}
		return int(output.ExitOK)
	}
	out, err := runJQ(ctx, inv.shaping.jq, shaped, red)
	if err != nil {
		e := output.Errorf(output.ExitUsage, output.CodeJQFailed, "--jq: %v", err).
			WithHint("The command succeeded and data holds its unfiltered result; fix the --jq expression. Do not repeat a write to re-filter it.")
		return emitError(env, inv.Meta, e, clean.Data)
	}
	if _, err := env.Stdout.Write(out); err != nil {
		return int(output.ExitUnclassified)
	}
	return int(output.ExitOK)
}

// runJQ evaluates code over data and renders each result on its own line:
// strings raw, everything else as compact JSON. Output is redacted and
// terminal controls are escaped (line feeds and tabs in raw strings stay).
func runJQ(ctx context.Context, code *gojq.Code, data any, red *output.Redactor) ([]byte, error) {
	var buf bytes.Buffer
	iter := code.RunWithContext(ctx, data)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			var halt *gojq.HaltError
			if errors.As(err, &halt) && halt.ExitCode() == 0 {
				break
			}
			return nil, err
		}
		if s, ok := v.(string); ok {
			buf.WriteString(red.Text(s))
		} else {
			b, err := gojq.Marshal(v)
			if err != nil {
				return nil, err
			}
			buf.Write(output.EscapeJSONControls([]byte(red.String(string(b)))))
		}
		buf.WriteByte('\n')
		if buf.Len() > maxJQOutput {
			return nil, errors.New("output exceeds 64 MiB")
		}
	}
	return buf.Bytes(), nil
}
