// Package identity binds the CLI to the published identity API.
package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	identityv1 "github.com/gatherdeal/cli/internal/generated/identity/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/paging"
)

const ListOperation = identityv1.Service + ".outlets.list"

type Client struct {
	http *http.Client
	root string
}

func New(h *http.Client, base string) (*Client, error) {
	root, ok := strings.CutSuffix(base, "/"+identityv1.Version)
	if !ok || root == "" {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "unsupported identity base URL %s", base)
	}
	if _, err := identityv1.NewClient(root, h); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "invalid identity base URL").WithCause(err)
	}
	return &Client{http: h, root: root}, nil
}

type call struct {
	api *identityv1.Client
	d   *httpx.ResponseDoer
}

func (c *Client) call() call {
	d := &httpx.ResponseDoer{Next: c.http}
	a, err := identityv1.NewClient(c.root, d)
	if err != nil {
		panic(err)
	}
	return call{a, d}
}
func (c call) apiError(ctx context.Context, err error) error {
	var p *identityv1.Problem
	if errors.As(err, &p) {
		return httpx.ProblemError(&httpx.Problem{Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: p.Instance, Code: p.Code, Retryable: p.Retryable, RetryAfterMS: p.RetryAfterMS, CorrelationID: p.CorrelationID, Details: p.Details, Errors: p.Errors, ContentLanguage: p.ContentLanguage}, c.d.Problem).WithCause(err)
	}
	var s *identityv1.StatusError
	if errors.As(err, &s) {
		return httpx.StatusError(s.HTTPStatus, s.CorrelationID).WithCause(err)
	}
	var pErr *identityv1.ProtocolError
	if errors.As(err, &pErr) {
		return httpx.ProtocolError(pErr.HTTPStatus, pErr.CorrelationID, pErr.Reason).WithCause(err)
	}
	return httpx.TransportError(ctx, err)
}
func missing() error {
	return output.Errorf(output.ExitUnclassified, output.CodeProtocol, "identity response is missing its result")
}
func (c *Client) FetchOutlets(ctx context.Context, in identityv1.FetchOutletsInput, all bool, maxItems int) ([]*identityv1.Outlet, output.PageInfo, string, error) {
	call := c.call()
	if all {
		hadAfter := in.After != ""
		in.Page, in.Total = 0, identityv1.TotalModeNone
		if in.PageSize == 0 {
			in.PageSize = contracts.MustOperation(ListOperation).Pagination.MaxPageSize
		}
		res, err := paging.Walk(ctx, "outlets", in.After, in.PageSize, maxItems, func(ctx context.Context, after string, size int) (paging.Page[*identityv1.Outlet], error) {
			in.After, in.PageSize = after, size
			out, err := call.api.FetchOutlets(ctx, &in)
			if err != nil {
				return paging.Page[*identityv1.Outlet]{}, call.apiError(ctx, err)
			}
			if out.Outlets == nil {
				return paging.Page[*identityv1.Outlet]{}, missing()
			}
			p := out.Outlets
			return paging.Page[*identityv1.Outlet]{Items: p.Data, HasNextPage: p.HasNextPage, NextCursor: p.NextCursor, CorrelationID: call.d.CorrelationID}, nil
		})
		if err != nil {
			return nil, output.PageInfo{}, "", err
		}
		return res.Items, output.PageInfo{HasNextPage: res.Truncated, NextCursor: res.NextCursor, HasPrevPage: hadAfter}, res.CorrelationID, nil
	}
	out, err := call.api.FetchOutlets(ctx, &in)
	if err != nil {
		return nil, output.PageInfo{}, call.d.CorrelationID, call.apiError(ctx, err)
	}
	if out.Outlets == nil {
		return nil, output.PageInfo{}, call.d.CorrelationID, missing()
	}
	p := out.Outlets
	if p.Data == nil {
		p.Data = []*identityv1.Outlet{}
	}
	return p.Data, output.PageInfo{PageSize: p.PageSize, HasNextPage: p.HasNextPage, HasPrevPage: p.HasPrevPage, NextCursor: p.NextCursor, PrevCursor: p.PrevCursor, Page: p.Page, TotalItems: p.TotalItems, TotalPages: p.TotalPages}, call.d.CorrelationID, nil
}

// GetOutlet resolves an exact ID/code filter through the sole published list operation.
func (c *Client) GetOutlet(ctx context.Context, in identityv1.FetchOutletsInput) (*identityv1.Outlet, string, error) {
	in.PageSize, in.Total = 1, identityv1.TotalModeNone
	items, _, correlation, err := c.FetchOutlets(ctx, in, false, 0)
	if err != nil {
		return nil, correlation, err
	}
	if len(items) == 0 {
		return nil, correlation, output.Errorf(output.ExitNotFound, output.CodeOutletNotFound, "Outlet not found.").WithHint("The outlet does not exist or is outside the outlets this access may read.")
	}
	return items[0], correlation, nil
}
