// Package whatsapp binds the CLI to the published WhatsApp API.
package whatsapp

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	wa "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/paging"
)

type Client struct {
	http *http.Client
	root string
}

func New(h *http.Client, base string) (*Client, error) {
	root, ok := strings.CutSuffix(base, "/"+wa.Version)
	if !ok || root == "" {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "unsupported WhatsApp base URL %s", base)
	}
	if _, err := wa.NewClient(root, h); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "invalid WhatsApp base URL").WithCause(err)
	}
	return &Client{http: h, root: root}, nil
}

type call struct {
	api *wa.Client
	d   *httpx.ResponseDoer
}

func (c *Client) call(outlet string) call {
	d := &httpx.ResponseDoer{Next: c.http, OutletID: outlet}
	a, err := wa.NewClient(c.root, d)
	if err != nil {
		panic(err)
	}
	return call{a, d}
}
func (c call) apiError(ctx context.Context, err error, write bool) error {
	var p *wa.Problem
	if errors.As(err, &p) {
		return httpx.ProblemError(&httpx.Problem{Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: p.Instance, Code: p.Code, Retryable: p.Retryable, RetryAfterMS: p.RetryAfterMS, CorrelationID: p.CorrelationID, Details: p.Details, Errors: p.Errors, ContentLanguage: p.ContentLanguage}, c.d.Problem).WithCause(err)
	}
	var s *wa.StatusError
	if errors.As(err, &s) && (!write || s.HTTPStatus >= 400 && s.HTTPStatus < 500) {
		return httpx.StatusError(s.HTTPStatus, s.CorrelationID).WithCause(err)
	}
	if write {
		return output.UnknownWriteOutcome(err)
	}
	if s != nil {
		return httpx.StatusError(s.HTTPStatus, s.CorrelationID).WithCause(err)
	}
	var pErr *wa.ProtocolError
	if errors.As(err, &pErr) {
		return httpx.ProtocolError(pErr.HTTPStatus, pErr.CorrelationID, pErr.Reason).WithCause(err)
	}
	return httpx.TransportError(ctx, err)
}
func (c *Client) Invoke(ctx context.Context, binding wa.CLIBinding, input any, outlet, key string, write bool, opts ...wa.RequestOption) (jsontext.Value, string, error) {
	call := c.call(outlet)
	raw, err := binding.Invoke(ctx, call.api, input, key, opts...)
	if err != nil {
		err = call.apiError(ctx, err, write)
	}
	return raw, call.d.CorrelationID, err
}

func readBinding(id string) wa.CLIBinding {
	for _, b := range wa.CLIBindings() {
		if b.PublicID == id {
			return b
		}
	}
	panic("missing generated binding: " + id)
}
func missing() error {
	return output.Errorf(output.ExitUnclassified, output.CodeProtocol, "WhatsApp response is missing its result")
}

var getAgent = readBinding("whatsapp.agents.get")

func (c *Client) FetchAgent(ctx context.Context, in *wa.FetchAgentInput, outlet string) (json.RawMessage, string, error) {
	raw, corr, err := c.Invoke(ctx, getAgent, in, outlet, "", false)
	if err != nil {
		return nil, corr, err
	}
	record, err := httpx.ResultMember(raw, "agent", corr)
	return record, corr, err
}
func (c *Client) FetchAgentPairing(ctx context.Context, in *wa.FetchAgentPairingInput, outlet string) (*wa.AgentPairing, string, error) {
	call := c.call(outlet)
	out, err := call.api.FetchAgentPairing(ctx, in)
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, false)
	}
	if out == nil || out.Pairing == nil {
		return nil, call.d.CorrelationID, missing()
	}
	return out.Pairing, call.d.CorrelationID, nil
}
func (c *Client) CreateAgent(ctx context.Context, in *wa.CreateAgentInput, outlet string, key string) (*wa.Agent, string, error) {
	call := c.call(outlet)
	out, err := call.api.CreateAgent(ctx, in, wa.WithIdempotencyKey(key))
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, true)
	}
	if out == nil || out.Agent == nil || out.Agent.ID == "" {
		return nil, call.d.CorrelationID, output.UnknownWriteOutcome(missing())
	}
	return out.Agent, call.d.CorrelationID, nil
}

// UpdateAgent sends ifMatch, the agent's revision, as If-Match.
func (c *Client) UpdateAgent(ctx context.Context, in *wa.UpdateAgentInput, outlet, ifMatch string) (*wa.Agent, string, error) {
	call := c.call(outlet)
	out, err := call.api.UpdateAgent(ctx, in, wa.WithIfMatch(ifMatch))
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, true)
	}
	if out == nil || out.Agent == nil {
		return nil, call.d.CorrelationID, output.UnknownWriteOutcome(missing())
	}
	return out.Agent, call.d.CorrelationID, nil
}
func (c *Client) DeleteAgent(ctx context.Context, in *wa.DeleteAgentInput, outlet string) (*wa.AgentDeletion, string, error) {
	call := c.call(outlet)
	out, err := call.api.DeleteAgent(ctx, in)
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, true)
	}
	if out == nil || out.Deletion == nil {
		return nil, call.d.CorrelationID, output.UnknownWriteOutcome(missing())
	}
	return out.Deletion, call.d.CorrelationID, nil
}

var getMessage = readBinding("whatsapp.messages.get")

func (c *Client) FetchMessage(ctx context.Context, in *wa.FetchMessageInput, outlet string) (json.RawMessage, string, error) {
	raw, corr, err := c.Invoke(ctx, getMessage, in, outlet, "", false)
	if err != nil {
		return nil, corr, err
	}
	record, err := httpx.ResultMember(raw, "message", corr)
	return record, corr, err
}
func (c *Client) CreateMessage(ctx context.Context, in *wa.CreateMessageInput, outlet string, key string) (*wa.Message, string, error) {
	call := c.call(outlet)
	out, err := call.api.CreateMessage(ctx, in, wa.WithIdempotencyKey(key))
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, true)
	}
	if out == nil || out.Message == nil {
		return nil, call.d.CorrelationID, output.UnknownWriteOutcome(missing())
	}
	return out.Message, call.d.CorrelationID, nil
}

var listAgents = readBinding("whatsapp.agents.list")

func (c *Client) FetchAgents(ctx context.Context, in wa.FetchAgentsInput, outlet string, all bool, maxItems int) ([]json.RawMessage, output.PageInfo, string, error) {
	call := c.call(outlet)
	if all {
		in.Page, in.Total = 0, wa.TotalModeNone
	}
	return paging.Records(ctx, contracts.MustOperation(listAgents.PublicID).Pagination, in.After, in.PageSize, all, maxItems, func(after string, size int) ([]byte, string, error) {
		in.After, in.PageSize = after, size
		raw, err := listAgents.Invoke(ctx, call.api, &in, "")
		if err != nil {
			err = call.apiError(ctx, err, false)
		}
		return raw, call.d.CorrelationID, err
	})
}

var listMessages = readBinding("whatsapp.messages.list")

func (c *Client) FetchMessages(ctx context.Context, in wa.FetchMessagesInput, outlet string, all bool, maxItems int) ([]json.RawMessage, output.PageInfo, string, error) {
	call := c.call(outlet)
	if all {
		in.Page, in.Total = 0, wa.TotalModeNone
	}
	return paging.Records(ctx, contracts.MustOperation(listMessages.PublicID).Pagination, in.After, in.PageSize, all, maxItems, func(after string, size int) ([]byte, string, error) {
		in.After, in.PageSize = after, size
		raw, err := listMessages.Invoke(ctx, call.api, &in, "")
		if err != nil {
			err = call.apiError(ctx, err, false)
		}
		return raw, call.d.CorrelationID, err
	})
}
