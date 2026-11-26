package cli

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/auth"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	wa "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/paging"
)

// RequiresPublishedOperationExclusion applies the CLI release policy to external
// writes. The exceptions are the commands already present when the policy was
// adopted on 2026-11-21.
func RequiresPublishedOperationExclusion(op contracts.Operation) bool {
	if op.ReadOnly() || op.Effect.Interaction != "external" {
		return false
	}
	switch op.PublicID {
	case "whatsapp.broadcasts.resume",
		"whatsapp.broadcasts.start",
		"whatsapp.message_templates.create",
		"whatsapp.message_templates.delete",
		"whatsapp.message_templates.replace",
		"whatsapp.messages.create":
		return false
	default:
		return true
	}
}

// Generated bindings own request encoding and dispatch. Existing presentations
// win by public ID so the same operation never acquires a second command.
func generatedCommands(existing []*Command) []*Command {
	bound := make(map[string]*Command)
	for _, c := range existing {
		bound[c.Operation] = c
	}
	var commands []*Command
	for _, b := range crmv1.CLIBindings() {
		if c := bound[b.PublicID]; c != nil {
			c.FilterFields = b.FilterFields
			continue
		}
		op := contracts.MustOperation(b.PublicID)
		dispatch := b
		if op.Deprecation != nil {
			// A deprecated command keeps its flags and input and sends
			// them through its successor.
			dispatch = crmSuccessor(b, op.Deprecation.Successor)
		}
		var batch *batchSpec
		if b.Batch != nil {
			batch = &batchSpec{MaxItems: b.Batch.MaxItems}
		}
		c := generatedCommand(op, b.Summary, b.HasBody, b.Arguments, batch, dispatch.Bind, (*crmv1.Client)(nil),
			func(inv *Invocation) (invokeInput, error) {
				client, err := inv.contactsClient()
				if err != nil {
					return nil, err
				}
				outlet := selectedOutlet(inv)
				var opts []crmv1.RequestOption
				if requiresIfMatch(op.PublicID) {
					opts = append(opts, crmv1.WithIfMatch(inv.str("if-match")))
				}
				return func(ctx context.Context, input any, key string) (jsontext.Value, string, error) {
					return client.Invoke(ctx, dispatch, input, outlet, key, !op.ReadOnly(), opts...)
				}, nil
			})
		if c != nil {
			c.FilterFields = b.FilterFields
			commands = append(commands, c)
		}
	}
	for _, b := range wa.CLIBindings() {
		if bound[b.PublicID] != nil {
			continue
		}
		op := contracts.MustOperation(b.PublicID)
		c := generatedCommand(op, b.Summary, b.HasBody, b.Arguments, nil, b.Bind, (*wa.Client)(nil),
			func(inv *Invocation) (invokeInput, error) {
				client, err := inv.whatsappClient()
				if err != nil {
					return nil, err
				}
				outlet := selectedOutlet(inv)
				var opts []wa.RequestOption
				if requiresIfMatch(op.PublicID) {
					opts = append(opts, wa.WithIfMatch(inv.str("if-match")))
				}
				return func(ctx context.Context, input any, key string) (jsontext.Value, string, error) {
					return client.Invoke(ctx, b, input, outlet, key, !op.ReadOnly(), opts...)
				}, nil
			})
		if c != nil {
			commands = append(commands, c)
		}
	}
	return commands
}

// crmSuccessor returns the binding of a deprecated operation's successor.
// The successor must take the same arguments, the same preconditions and a
// body that accepts every body of the deprecated operation.
func crmSuccessor(deprecated crmv1.CLIBinding, successor string) crmv1.CLIBinding {
	for _, b := range crmv1.CLIBindings() {
		if b.PublicID != successor {
			continue
		}
		if !slices.Equal(b.Arguments, deprecated.Arguments) || b.HasBody != deprecated.HasBody || b.Keyed != deprecated.Keyed ||
			requiresIfMatch(b.PublicID) != requiresIfMatch(deprecated.PublicID) {
			panic(deprecated.PublicID + ": successor " + successor + " takes different arguments")
		}
		return b
	}
	panic(deprecated.PublicID + ": missing successor binding " + successor)
}

func (inv *Invocation) generatedWriteKey(op contracts.Operation) (string, error) {
	if op.ReadOnly() {
		return "", nil
	}
	if err := inv.recordWriteKey(); err != nil {
		return "", err
	}
	return inv.Meta.Write.IdempotencyKey, nil
}

type bindingArgument struct {
	Name, WireName, In string
	Required           bool
}
type bindInput func(map[string]jsontext.Value, jsontext.Value) (any, error)

// invokeInput sends one bound request with the given idempotency key ("" for
// an unkeyed operation).
type invokeInput func(ctx context.Context, input any, key string) (jsontext.Value, string, error)

func generatedCommand[A ~struct {
	Name, WireName, In string
	Required           bool
}](op contracts.Operation, summary string, hasBody bool, args []A, batch *batchSpec, bind bindInput, client any, connect func(*Invocation) (invokeInput, error)) *Command {
	if RequiresPublishedOperationExclusion(op) {
		return nil
	}
	c := &Command{Operation: op.PublicID, Summary: strings.TrimSuffix(summary, ".") + ".", Network: true, Output: "json", Flags: append([]FlagSpec{}, targetFlags...)}
	if op.OutletScope == "filtered" {
		outlet := flagOutlet
		if !op.ReadOnly() {
			outlet.Usage = "Outlet id (UUID) within this access's approved write scope."
		}
		c.Flags = append(c.Flags, outlet)
	}
	if op.Pagination != nil {
		c.Flags = append(c.Flags, pageFlags(op.PublicID, op.Pagination.ResultField)...)
	}
	params := make([]commandParameter, 0, len(args))
	for _, item := range args {
		a := bindingArgument(item)
		p := parameterFor(op, a)
		params = append(params, p)
		if a.In == "path" {
			c.Args = append(c.Args, ArgSpec{Name: a.Name, Usage: p.flag.Usage})
		} else if !p.shared {
			c.Flags = append(c.Flags, p.flag)
		}
	}
	if batch != nil {
		return batchCommand(c, op, *batch, params, bind, connect)
	}
	result := generatedResult(op, client, c)
	run := func(ctx context.Context, inv *Invocation) (any, error) {
		values, err := inv.bindingArguments(params)
		if err != nil {
			return nil, err
		}
		if inv.isSet("outlet") {
			if err := validateOutlet(inv); err != nil {
				return nil, err
			}
		}
		if op.Pagination != nil {
			if err := validatePaging(inv); err != nil {
				return nil, err
			}
			walk, _ := inv.walk()
			if inv.isSet("before") && (walk || inv.isSet("after") || inv.isSet("page")) {
				return nil, usageError("--before requires a single cursor page and cannot be combined with --after or --page")
			}
		}
		var body jsontext.Value
		if hasBody {
			v, err := ReadBody[json.RawMessage](inv.str("input"), inv.Env.Stdin)
			if err != nil {
				return nil, err
			}
			body = *v
			if !op.ReadOnly() {
				if err := validateJSON(body, writeContracts()[op.PublicID].request); err != nil {
					return nil, usageError("invalid --input: %v", err)
				}
			}
		}
		var input any
		if inv.Meta.Write != nil {
			input, err = bind(values, body)
			if err != nil {
				return nil, usageError("%v", err)
			}
			if hasBody {
				body, err = json.Marshal(input)
				if err != nil {
					return nil, usageError("cannot encode --input: %v", err)
				}
				if err := validateJSON(body, writeContracts()[op.PublicID].request); err != nil {
					return nil, usageError("invalid encoded --input: %v", err)
				}
			}
		}
		if inv.Meta.Write != nil && inv.Meta.Write.DryRun {
			if _, err := inv.resolveTarget(false); err != nil {
				return nil, err
			}
			return struct {
				Arguments map[string]jsontext.Value `json:"arguments,omitempty"`
				Body      jsontext.Value            `json:"body,omitempty"`
				Outlet    string                    `json:"outlet,omitempty"`
				IfMatch   string                    `json:"if_match,omitempty"`
			}{values, body, selectedOutlet(inv), inv.writeOptions(*c.Write).IfMatch}, nil
		}
		var invoke invokeInput
		call := func() (jsontext.Value, error) {
			bound := input
			if bound == nil {
				bound, err = bind(values, body)
				if err != nil {
					return nil, usageError("%v", err)
				}
			}
			if invoke == nil {
				invoke, err = connect(inv)
				if err != nil {
					return nil, err
				}
			}
			key, err := inv.generatedWriteKey(op)
			if err != nil {
				return nil, err
			}
			raw, corr, err := invoke(ctx, bound, key)
			inv.Meta.CorrelationID = corr
			return raw, auth.Explain(err)
		}
		if op.Pagination != nil {
			return generatedList(ctx, inv, op.Pagination, values, call)
		}
		raw, err := call()
		if err != nil {
			return nil, err
		}
		if !op.ReadOnly() {
			if receipt := writeContracts()[op.PublicID].receipt; receipt != nil {
				if err := validateJSON(raw, receipt); err != nil {
					return nil, output.UnknownWriteOutcome(err)
				}
			}
		}
		if len(raw) == 0 {
			return nil, nil
		}
		if result != "" {
			return httpx.ResultMember(raw, result, inv.Meta.CorrelationID)
		}
		return raw, nil
	}
	c.Run = run
	if !op.ReadOnly() {
		confirmation := ConfirmConfig
		if op.Effect.Interaction == "external" {
			confirmation = ConfirmSend
		}
		c.Write = &WritePolicy{Confirmation: confirmation, Preview: run}
		c.Flags = append(c.Flags, c.Write.flags(op)...)
	}
	if hasBody {
		input := flagInput
		input.Required = true
		c.Flags = append(c.Flags, input)
	}
	return c
}

func (inv *Invocation) bindingArguments(params []commandParameter) (map[string]jsontext.Value, error) {
	values := make(map[string]jsontext.Value)
	pos := 0
	for _, p := range params {
		var raw jsontext.Value
		if p.In == "path" {
			v := inv.Args[pos]
			pos++
			if p.uuid && !uuidRE.MatchString(v) {
				return nil, usageError("%s must be a UUID", p.Name)
			}
			raw, _ = json.Marshal(v)
		} else {
			if !inv.isSet(p.flag.Name) {
				if p.Required {
					return nil, usageError("flag --%s is required", p.flag.Name)
				}
				continue
			}
			if p.Required && p.uuid && !uuidRE.MatchString(inv.str(p.flag.Name)) {
				return nil, usageError("--%s must be a UUID", p.flag.Name)
			}
			switch {
			case p.flag.Kind == kindBool:
				raw = jsontext.Value(strconv.FormatBool(inv.boolean(p.flag.Name)))
			case p.flag.Kind == kindInt:
				raw = jsontext.Value(strconv.Itoa(inv.integer(p.flag.Name)))
			case p.jsonValue:
				raw = jsontext.Value(inv.str(p.flag.Name))
			default:
				raw, _ = json.Marshal(inv.str(p.flag.Name))
			}
		}
		values[p.Name] = raw
	}
	return values, nil
}

func selectedOutlet(inv *Invocation) string {
	if inv.isSet("outlet") {
		return inv.str("outlet")
	}
	return ""
}

// A get with one record or a paginated list supports --fields. Multi-field
// results (such as revision polling) retain their entire response.
func generatedResult(op contracts.Operation, client any, c *Command) string {
	if op.Method != http.MethodGet {
		return ""
	}
	method, ok := reflect.TypeOf(client).MethodByName(op.GoMethod)
	if !ok {
		panic("missing generated client method: " + op.GoMethod)
	}
	out := method.Type.Out(0).Elem()
	if out.NumField() != 1 {
		return ""
	}
	field := out.Field(0)
	t := field.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return ""
	}
	if op.Pagination != nil {
		data, ok := t.FieldByName("Data")
		if !ok {
			panic("missing generated page data in " + op.GoMethod)
		}
		t = data.Type.Elem()
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
	}
	c.Record = reflect.Zero(t).Interface()
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

func generatedList(ctx context.Context, inv *Invocation, pg *contracts.Pagination, args map[string]jsontext.Value, call func() (jsontext.Value, error)) (any, error) {
	fetch := func() (items []json.RawMessage, info output.PageInfo, err error) {
		raw, err := call()
		if err != nil {
			return nil, info, err
		}
		member, err := httpx.ResultMember(raw, pg.ResultField, inv.Meta.CorrelationID)
		if err != nil {
			return nil, info, err
		}
		var page struct {
			output.PageInfo
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(member, &page); err != nil || page.Data == nil {
			return nil, info, httpx.ProtocolError(http.StatusOK, inv.Meta.CorrelationID, "invalid "+pg.ResultField+" page")
		}
		return page.Data, page.PageInfo, nil
	}
	walk, limit := inv.walk()
	if !walk {
		items, page, err := fetch()
		if err != nil {
			return nil, err
		}
		inv.Meta.PageInfo = &page
		return items, nil
	}
	size := inv.integer("page-size")
	if size == 0 {
		size = pg.MaxPageSize
	}
	args[pg.TotalParam] = jsontext.Value(`"none"`)
	r, err := paging.Walk(ctx, pg.ResultField, inv.str("after"), size, limit, func(_ context.Context, cursor string, size int) (paging.Page[json.RawMessage], error) {
		args[pg.AfterParam], _ = json.Marshal(cursor)
		args[pg.PageSizeParam] = jsontext.Value(strconv.Itoa(size))
		items, info, err := fetch()
		return paging.Page[json.RawMessage]{Items: items, HasNextPage: info.HasNextPage, NextCursor: info.NextCursor, CorrelationID: inv.Meta.CorrelationID}, err
	})
	if err != nil {
		return nil, err
	}
	inv.Meta.PageInfo = &output.PageInfo{HasNextPage: r.Truncated, NextCursor: r.NextCursor, HasPrevPage: inv.isSet("after")}
	return r.Items, nil
}
