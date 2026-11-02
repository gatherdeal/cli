package cli

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gatherdeal/cli/internal/auth"
	wa "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/whatsapp"
)

// whatsappSummaries are the command summaries, keyed by operation.
var whatsappSummaries = map[string]string{
	"agents.list":     "List WhatsApp agents in the outlets this access may read.",
	"agents.get":      "Show one WhatsApp agent.",
	"agents.pairing":  "Check a WhatsApp agent's pairing once, without requesting a new code.",
	"agents.create":   "Create a WhatsApp agent and return its pairing instructions.",
	"agents.update":   "Change a WhatsApp agent's mode or outlet.",
	"agents.delete":   "Delete a WhatsApp agent and unlink its device.",
	"messages.list":   "List WhatsApp messages in the outlets this access may read.",
	"messages.get":    "Show one WhatsApp message and its status.",
	"messages.create": "Send a WhatsApp message to one recipient.",
}

func whatsappCommands() []*Command {
	var cmds []*Command
	for _, r := range []string{"agents", "messages"} {
		var record any = wa.Agent{}
		idUsage := "Agent id (UUID)."
		if r == "messages" {
			record = wa.Message{}
			idUsage = "Message id (UUID)."
		}
		flags := append([]FlagSpec{}, targetFlags...)
		flags = append(flags, pageFlags("whatsapp."+r+".list", r)...)
		flags = append(flags, flagOutlet, FlagSpec{Name: "sort", Kind: kindString, Usage: "Field to sort by."}, FlagSpec{Name: "order", Kind: kindString, Values: []string{"asc", "desc"}, Usage: "Sort direction."})
		if r == "messages" {
			flags = append(flags, FlagSpec{Name: "agent", Kind: kindString, Usage: "Filter by agent id."}, FlagSpec{Name: "status", Kind: kindString, Values: wa.MessageStatus("").Values(), Usage: "Runtime acceptance status, not recipient delivery."}, FlagSpec{Name: "to", Kind: kindString, Usage: "Recipient filter."}, FlagSpec{Name: "from", Kind: kindString, Usage: "Sender filter."})
		}
		cmds = append(cmds, &Command{Operation: "whatsapp." + r + ".list", Summary: whatsappSummaries[r+".list"], Flags: flags, Record: record, Output: "json", Network: true, Run: runWhatsAppList})
		cmds = append(cmds, &Command{Operation: "whatsapp." + r + ".get", Summary: whatsappSummaries[r+".get"], Args: []ArgSpec{{Name: "id", Usage: idUsage}}, Flags: append(append([]FlagSpec{}, targetFlags...), flagOutlet), Record: record, Output: "json", Network: true, Run: runWhatsAppGet})
	}
	cmds = append(cmds, &Command{Operation: "whatsapp.agents.pairing", Summary: whatsappSummaries["agents.pairing"], Args: []ArgSpec{{Name: "agent_id", Usage: "Agent id (UUID)."}}, Flags: append(append([]FlagSpec{}, targetFlags...), flagOutlet), Output: "json", Network: true, Run: runWhatsAppPairing})
	for _, op := range []string{"agents.create", "agents.update", "agents.delete", "messages.create"} {
		c := &Command{Operation: "whatsapp." + op, Summary: whatsappSummaries[op], Flags: append(append([]FlagSpec{}, targetFlags...), flagOutlet), Output: "json", Network: true, Run: runWhatsAppWrite}
		confirmation := ConfirmConfig
		if op == "messages.create" {
			confirmation = ConfirmSend
		}
		c.Write = &WritePolicy{Confirmation: confirmation, Preview: previewWhatsAppWrite}
		if op == "agents.create" {
			c.Flags[len(c.Flags)-1].Usage = "Outlet UUID or code; codes are resolved through the scoped outlets list."
			c.Flags = append(c.Flags, FlagSpec{Name: "phone", Kind: kindString, Usage: "Phone in E.164, including leading +, for phone-number pairing."}, FlagSpec{Name: "no-wait", Kind: kindBool, Usage: "Return pairing instructions immediately (exit 9); check later with agents pairing."})
		}
		if op == "agents.update" || op == "agents.delete" {
			c.Args = []ArgSpec{{Name: "agent_id", Usage: "Agent id (UUID)."}}
		}
		cmds = append(cmds, c)
	}
	return cmds
}
func (inv *Invocation) whatsappClient() (*whatsapp.Client, error) {
	t, err := inv.resolveTarget(false)
	if err != nil {
		return nil, err
	}
	cred, err := inv.loadCredential(t)
	if err != nil {
		return nil, err
	}
	base, ok := t.disc.ServiceURL("whatsapp")
	if !ok {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeServiceMissing, "%s does not advertise the WhatsApp API", t.origin)
	}
	return whatsapp.New(inv.httpClient(t, cred.token), base)
}
func whatsappID(inv *Invocation) (string, error) {
	id := inv.Args[0]
	if !uuidRE.MatchString(id) {
		return "", usageError("id must be a UUID")
	}
	return id, validateOutlet(inv)
}
func runWhatsAppList(ctx context.Context, inv *Invocation) (any, error) {
	if err := validatePaging(inv); err != nil {
		return nil, err
	}
	if err := validateOutlet(inv); err != nil {
		return nil, err
	}
	if inv.Command.Operation == "whatsapp.messages.list" {
		if id := inv.str("agent"); id != "" && !uuidRE.MatchString(id) {
			return nil, usageError("--agent must be a UUID")
		}
	}
	c, err := inv.whatsappClient()
	if err != nil {
		return nil, err
	}
	var data any
	var page output.PageInfo
	var correlation string
	walk, limit := inv.walk()
	if inv.Command.Operation == "whatsapp.agents.list" {
		data, page, correlation, err = c.FetchAgents(ctx, wa.FetchAgentsInput{Page: inv.integer("page"), PageSize: inv.integer("page-size"), After: inv.str("after"), Total: wa.TotalMode(inv.str("total")), SortBy: inv.str("sort"), SortOrder: inv.str("order")}, inv.str("outlet"), walk, limit)
	} else {
		data, page, correlation, err = c.FetchMessages(ctx, wa.FetchMessagesInput{Page: inv.integer("page"), PageSize: inv.integer("page-size"), After: inv.str("after"), Total: wa.TotalMode(inv.str("total")), SortBy: inv.str("sort"), SortOrder: inv.str("order"), AgentID: inv.str("agent"), Status: wa.MessageStatus(inv.str("status")), To: inv.str("to"), From: inv.str("from")}, inv.str("outlet"), walk, limit)
	}
	inv.Meta.CorrelationID = correlation
	if err != nil {
		return nil, auth.Explain(err)
	}
	inv.Meta.PageInfo = &page
	return data, nil
}
func runWhatsAppGet(ctx context.Context, inv *Invocation) (any, error) {
	id, err := whatsappID(inv)
	if err != nil {
		return nil, err
	}
	c, err := inv.whatsappClient()
	if err != nil {
		return nil, err
	}
	var data any
	if inv.Command.Operation == "whatsapp.agents.get" {
		data, inv.Meta.CorrelationID, err = c.FetchAgent(ctx, &wa.FetchAgentInput{AgentID: id}, inv.str("outlet"))
	} else {
		data, inv.Meta.CorrelationID, err = c.FetchMessage(ctx, &wa.FetchMessageInput{MessageID: id}, inv.str("outlet"))
	}
	return data, auth.Explain(err)
}

var e164RE = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)

// whatsappInput is shared by preview and execution so both validate the same typed request.
func whatsappInput(inv *Invocation) (any, error) {
	switch inv.Command.Operation {
	case "whatsapp.agents.create":
		in := &wa.CreateAgentInput{}
		if inv.str("input") != "" {
			var err error
			in, err = ReadBody[wa.CreateAgentInput](inv.str("input"), inv.Env.Stdin)
			if err != nil {
				return nil, err
			}
		}
		if in.OutletID != "" && !uuidRE.MatchString(in.OutletID) {
			return nil, usageError("input outlet_id must be a UUID")
		}
		if inv.isSet("outlet") {
			outlet := inv.str("outlet")
			if strings.TrimSpace(outlet) == "" {
				return nil, usageError("--outlet requires an outlet id or code")
			}
			if uuidRE.MatchString(outlet) {
				if in.OutletID != "" && in.OutletID != outlet {
					return nil, usageError("--outlet differs from input outlet_id")
				}
				in.OutletID = outlet
			}
		}
		if inv.isSet("phone") {
			if in.Phone != "" && in.Phone != inv.str("phone") {
				return nil, usageError("--phone differs from input phone")
			}
			in.Phone = inv.str("phone")
		}
		if in.OutletID == "" && !inv.isSet("outlet") {
			return nil, usageError("--outlet or input outlet_id is required")
		}
		if in.Phone != "" && !e164RE.MatchString(in.Phone) {
			return nil, usageError("--phone must be E.164 with a leading +")
		}
		return in, nil
	case "whatsapp.agents.update":
		id, err := whatsappID(inv)
		if err != nil {
			return nil, err
		}
		in, err := ReadBody[wa.UpdateAgentInput](inv.str("input"), inv.Env.Stdin)
		if err != nil {
			return nil, err
		}
		in.AgentID = id
		if in.Revision.IsZero() {
			return nil, usageError("input revision is required")
		}
		if in.Mode.Present {
			if err := in.Mode.Value.Validate(); err != nil {
				return nil, usageError("%v", err)
			}
		}
		if in.OutletID.Present && in.OutletID.Value != nil && !uuidRE.MatchString(*in.OutletID.Value) {
			return nil, usageError("input outlet_id must be a UUID or null")
		}
		return in, nil
	case "whatsapp.agents.delete":
		id, err := whatsappID(inv)
		if err != nil {
			return nil, err
		}
		if inv.str("input") != "" {
			return nil, usageError("delete takes no input body")
		}
		return &wa.DeleteAgentInput{AgentID: id}, nil
	case "whatsapp.messages.create":
		if err := validateOutlet(inv); err != nil {
			return nil, err
		}
		in, err := ReadBody[wa.CreateMessageInput](inv.str("input"), inv.Env.Stdin)
		if err != nil {
			return nil, err
		}
		if !uuidRE.MatchString(in.AgentID) {
			return nil, usageError("input agent_id must be a UUID")
		}
		if !e164RE.MatchString(in.To) {
			return nil, usageError("input to must be E.164 with a leading +")
		}
		if err := in.Type.Validate(); err != nil {
			return nil, usageError("%v", err)
		}
		if in.Text.Body == "" {
			return nil, usageError("input text.body is required")
		}
		if in.Strategy != "" {
			if err := in.Strategy.Validate(); err != nil {
				return nil, usageError("%v", err)
			}
		}
		return in, nil
	}
	panic("unknown WhatsApp write")
}
func previewWhatsAppWrite(ctx context.Context, inv *Invocation) (any, error) {
	in, err := whatsappInput(inv)
	if err != nil {
		return nil, err
	}
	if _, err = inv.whatsappClient(); err != nil {
		return nil, err
	}
	if create, ok := in.(*wa.CreateAgentInput); ok {
		if _, err = pairingPage(inv, "preview"); err != nil {
			return nil, err
		}
		if err = inv.resolveCreateOutlet(ctx, create); err != nil {
			return nil, err
		}
	}
	return in, nil
}
func runWhatsAppWrite(ctx context.Context, inv *Invocation) (any, error) {
	in, err := whatsappInput(inv)
	if err != nil {
		return nil, err
	}
	c, err := inv.whatsappClient()
	if err != nil {
		return nil, err
	}
	var data any
	switch in := in.(type) {
	case *wa.CreateAgentInput:
		if _, err = pairingPage(inv, "preview"); err != nil {
			return nil, err
		}
		if err = inv.resolveCreateOutlet(ctx, in); err != nil {
			return nil, err
		}
		var out *wa.CreateAgentOutput
		out, inv.Meta.CorrelationID, err = c.CreateAgent(ctx, in, in.OutletID, inv.Meta.Write.IdempotencyKey)
		if err != nil {
			return nil, auth.Explain(err)
		}
		return awaitPairing(ctx, inv, c, out.Agent.ID, out.Pairing)
	case *wa.UpdateAgentInput:
		data, inv.Meta.CorrelationID, err = c.UpdateAgent(ctx, in, inv.str("outlet"))
	case *wa.DeleteAgentInput:
		data, inv.Meta.CorrelationID, err = c.DeleteAgent(ctx, in, inv.str("outlet"))
	case *wa.CreateMessageInput:
		data, inv.Meta.CorrelationID, err = c.CreateMessage(ctx, in, inv.str("outlet"), inv.Meta.Write.IdempotencyKey)
	}
	return data, auth.Explain(err)
}

type pairingAction struct {
	Type        string `json:"type"`
	Instruction string `json:"instruction"`
}
type pairingData struct {
	AgentID     string                `json:"agent_id"`
	Status      wa.AgentPairingStatus `json:"status"`
	ExpiresAt   time.Time             `json:"expires_at"`
	PairingURL  string                `json:"pairing_url"`
	PairingCode string                `json:"pairing_code,omitempty"`
	Action      *pairingAction        `json:"action,omitempty"`
}

func pairingPage(inv *Invocation, id string) (string, error) {
	if inv.target == nil {
		return "", output.Errorf(output.ExitActionRequired, output.CodePairingContextMissing, "passport origin is unavailable; log in first")
	}
	u, err := url.Parse(inv.target.disc.VerificationURI)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", output.Errorf(output.ExitActionRequired, output.CodePairingContextMissing, "passport origin is unavailable from discovery")
	}
	if inv.Meta.Context == nil || inv.Meta.Context.Binding == nil || inv.Meta.Context.Binding.Organization.Code == "" {
		return "", output.Errorf(output.ExitActionRequired, output.CodePairingContextMissing, "organization code is unavailable from this profile; log in first")
	}
	u.Path = "/whatsapp/pairing/" + id
	u.RawPath = ""
	u.RawQuery = url.Values{"org": {inv.Meta.Context.Binding.Organization.Code}}.Encode()
	u.Fragment = ""
	return u.String(), nil
}
func pairingResult(inv *Invocation, id string, p *wa.AgentPairing) (any, error) {
	if p == nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "missing pairing result")
	}
	if err := p.Status.Validate(); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "invalid pairing status").WithCause(err)
	}
	page, err := pairingPage(inv, id)
	if err != nil {
		return nil, err
	}
	data := pairingData{AgentID: id, Status: p.Status, ExpiresAt: p.ExpiresAt, PairingURL: page}
	switch p.Status {
	case wa.AgentPairingStatusConnected:
		return data, nil
	case wa.AgentPairingStatusExpired, wa.AgentPairingStatusFailed:
		return data, output.Errorf(output.ExitConflict, output.CodePairingEnded, "WhatsApp pairing %s", p.Status)
	default:
		instruction := "A person must open the pairing page, sign in, and link WhatsApp under Linked devices."
		if p.PairingCode != "" {
			code := p.PairingCode
			if len(code) != 8 {
				return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "pairing code must contain 8 characters")
			}
			data.PairingCode = code[:4] + "-" + code[4:]
			instruction = "A person must open WhatsApp → Linked devices → Link with phone number instead and enter the pairing code. Never request a new code while the person is typing it."
		}
		data.Action = &pairingAction{Type: "pair_whatsapp", Instruction: instruction}
		return data, output.Errorf(output.ExitActionRequired, output.CodePairingPending, "a person must pair this WhatsApp API agent")
	}
}
func runWhatsAppPairing(ctx context.Context, inv *Invocation) (any, error) {
	id, err := whatsappID(inv)
	if err != nil {
		return nil, err
	}
	c, err := inv.whatsappClient()
	if err != nil {
		return nil, err
	}
	if _, err = pairingPage(inv, id); err != nil {
		return nil, err
	}
	p, correlation, err := c.FetchAgentPairing(ctx, &wa.FetchAgentPairingInput{AgentID: id}, inv.str("outlet"))
	inv.Meta.CorrelationID = correlation
	if err != nil {
		return nil, auth.Explain(err)
	}
	return pairingResult(inv, id, p)
}
func awaitPairing(ctx context.Context, inv *Invocation, c *whatsapp.Client, id string, p *wa.AgentPairing) (any, error) {
	if inv.boolean("no-wait") || p.Status != wa.AgentPairingStatusPending {
		return pairingResult(inv, id, p)
	}
	data, err := pairingResult(inv, id, p)
	if err == nil || output.AsError(err).Exit != output.ExitActionRequired {
		return data, err
	}
	d := data.(pairingData)
	inv.Env.stderrf("Pair WhatsApp: %s\n%s\nCode: %s\n", d.PairingURL, d.Action.Instruction, d.PairingCode)
	deadline := inv.Env.now().Add(5 * time.Minute)
	for i := 0; i < 150; i++ {
		remaining := min(deadline.Sub(inv.Env.now()), p.ExpiresAt.Sub(inv.Env.now()))
		if remaining <= 0 {
			if !p.ExpiresAt.After(inv.Env.now()) {
				expired := *p
				expired.Status = wa.AgentPairingStatusExpired
				return pairingResult(inv, id, &expired)
			}
			return pairingResult(inv, id, p)
		}
		delay := min(2*time.Second, remaining)
		err = nil
		if inv.Env.Sleep != nil {
			err = inv.Env.Sleep(ctx, delay)
		} else {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-timer.C:
			}
			timer.Stop()
		}
		if err != nil {
			return data, err
		}
		p, inv.Meta.CorrelationID, err = c.FetchAgentPairing(ctx, &wa.FetchAgentPairingInput{AgentID: id}, inv.str("outlet"))
		if err != nil {
			return data, auth.Explain(err)
		}
		if p.Status != wa.AgentPairingStatusPending {
			return pairingResult(inv, id, p)
		}
	}
	if !p.ExpiresAt.After(inv.Env.now()) && p.Status == wa.AgentPairingStatusPending {
		expired := *p
		expired.Status = wa.AgentPairingStatusExpired
		return pairingResult(inv, id, &expired)
	}
	return pairingResult(inv, id, p)
}

func (inv *Invocation) resolveCreateOutlet(ctx context.Context, in *wa.CreateAgentInput) error {
	if value := inv.str("outlet"); value != "" && !uuidRE.MatchString(value) {
		outlet, err := inv.resolveOutlet(ctx, value)
		if err != nil {
			return err
		}
		if in.OutletID != "" && in.OutletID != outlet.ID {
			return usageError("--outlet differs from input outlet_id")
		}
		in.OutletID = outlet.ID
	}
	*inv.strs["outlet"] = in.OutletID
	inv.Meta.Context.Request.OutletIDs = []string{in.OutletID}
	return nil
}
