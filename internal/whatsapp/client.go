// Package whatsapp binds the CLI to the published WhatsApp API.
package whatsapp

import (
	"context"
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
func missing() error {
	return output.Errorf(output.ExitUnclassified, output.CodeProtocol, "WhatsApp response is missing its result")
}
func (c *Client) FetchAgent(ctx context.Context, in *wa.FetchAgentInput, outlet string) (*wa.Agent, string, error) {
	call := c.call(outlet)
	out, err := call.api.FetchAgent(ctx, in)
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, false)
	}
	if out == nil || out.Agent == nil {
		return nil, call.d.CorrelationID, missing()
	}
	return out.Agent, call.d.CorrelationID, nil
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
func (c *Client) CreateAgent(ctx context.Context, in *wa.CreateAgentInput, outlet string, key string) (*wa.CreateAgentOutput, string, error) {
	call := c.call(outlet)
	out, err := call.api.CreateAgent(ctx, in, wa.WithIdempotencyKey(key))
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, true)
	}
	if out == nil || out.Agent == nil || out.Pairing == nil {
		return nil, call.d.CorrelationID, output.UnknownWriteOutcome(missing())
	}
	return out, call.d.CorrelationID, nil
}
func (c *Client) UpdateAgent(ctx context.Context, in *wa.UpdateAgentInput, outlet string) (*wa.Agent, string, error) {
	call := c.call(outlet)
	out, err := call.api.UpdateAgent(ctx, in)
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
func (c *Client) FetchMessage(ctx context.Context, in *wa.FetchMessageInput, outlet string) (*wa.Message, string, error) {
	call := c.call(outlet)
	out, err := call.api.FetchMessage(ctx, in)
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err, false)
	}
	if out == nil || out.Message == nil {
		return nil, call.d.CorrelationID, missing()
	}
	return out.Message, call.d.CorrelationID, nil
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
func (c *Client) FetchAgents(ctx context.Context, in wa.FetchAgentsInput, outlet string, all bool, maxItems int) ([]*wa.Agent, output.PageInfo, string, error) {
	call := c.call(outlet)
	if all {
		hadAfter := in.After != ""
		in.Page, in.Total = 0, wa.TotalModeNone
		if in.PageSize == 0 {
			in.PageSize = contracts.MustOperation("whatsapp.agents.list").Pagination.MaxPageSize
		}
		res, err := paging.Walk(ctx, "agents", in.After, in.PageSize, maxItems, func(ctx context.Context, after string, size int) (paging.Page[*wa.Agent], error) {
			in.After, in.PageSize = after, size
			out, err := call.api.FetchAgents(ctx, &in)
			if err != nil {
				return paging.Page[*wa.Agent]{}, call.apiError(ctx, err, false)
			}
			if out.Agents == nil {
				return paging.Page[*wa.Agent]{}, missing()
			}
			p := out.Agents
			return paging.Page[*wa.Agent]{Items: p.Data, HasNextPage: p.HasNextPage, NextCursor: p.NextCursor, CorrelationID: call.d.CorrelationID}, nil
		})
		if err != nil {
			return nil, output.PageInfo{}, "", err
		}
		return res.Items, output.PageInfo{HasNextPage: res.Truncated, NextCursor: res.NextCursor, HasPrevPage: hadAfter}, res.CorrelationID, nil
	}
	out, err := call.api.FetchAgents(ctx, &in)
	if err != nil {
		return nil, output.PageInfo{}, call.d.CorrelationID, call.apiError(ctx, err, false)
	}
	if out.Agents == nil {
		return nil, output.PageInfo{}, call.d.CorrelationID, missing()
	}
	p := out.Agents
	if p.Data == nil {
		p.Data = []*wa.Agent{}
	}
	return p.Data, output.PageInfo{PageSize: p.PageSize, HasNextPage: p.HasNextPage, HasPrevPage: p.HasPrevPage, NextCursor: p.NextCursor, PrevCursor: p.PrevCursor, Page: p.Page, TotalItems: p.TotalItems, TotalPages: p.TotalPages}, call.d.CorrelationID, nil
}
func (c *Client) FetchMessages(ctx context.Context, in wa.FetchMessagesInput, outlet string, all bool, maxItems int) ([]*wa.Message, output.PageInfo, string, error) {
	call := c.call(outlet)
	if all {
		hadAfter := in.After != ""
		in.Page, in.Total = 0, wa.TotalModeNone
		if in.PageSize == 0 {
			in.PageSize = contracts.MustOperation("whatsapp.messages.list").Pagination.MaxPageSize
		}
		res, err := paging.Walk(ctx, "messages", in.After, in.PageSize, maxItems, func(ctx context.Context, after string, size int) (paging.Page[*wa.Message], error) {
			in.After, in.PageSize = after, size
			out, err := call.api.FetchMessages(ctx, &in)
			if err != nil {
				return paging.Page[*wa.Message]{}, call.apiError(ctx, err, false)
			}
			if out.Messages == nil {
				return paging.Page[*wa.Message]{}, missing()
			}
			p := out.Messages
			return paging.Page[*wa.Message]{Items: p.Data, HasNextPage: p.HasNextPage, NextCursor: p.NextCursor, CorrelationID: call.d.CorrelationID}, nil
		})
		if err != nil {
			return nil, output.PageInfo{}, "", err
		}
		return res.Items, output.PageInfo{HasNextPage: res.Truncated, NextCursor: res.NextCursor, HasPrevPage: hadAfter}, res.CorrelationID, nil
	}
	out, err := call.api.FetchMessages(ctx, &in)
	if err != nil {
		return nil, output.PageInfo{}, call.d.CorrelationID, call.apiError(ctx, err, false)
	}
	if out.Messages == nil {
		return nil, output.PageInfo{}, call.d.CorrelationID, missing()
	}
	p := out.Messages
	if p.Data == nil {
		p.Data = []*wa.Message{}
	}
	return p.Data, output.PageInfo{PageSize: p.PageSize, HasNextPage: p.HasNextPage, HasPrevPage: p.HasPrevPage, NextCursor: p.NextCursor, PrevCursor: p.PrevCursor, Page: p.Page, TotalItems: p.TotalItems, TotalPages: p.TotalPages}, call.d.CorrelationID, nil
}
