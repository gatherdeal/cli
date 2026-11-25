// Package identity binds the CLI to the published identity API.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	identityv1 "github.com/gatherdeal/cli/internal/generated/identity/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/paging"
)

const (
	ListOperation = identityv1.Service + ".outlets.list"
	GetOperation  = identityv1.Service + ".outlets.get"
)

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
func readBinding(id string) identityv1.CLIBinding {
	for _, b := range identityv1.CLIBindings() {
		if b.PublicID == id {
			return b
		}
	}
	panic("missing generated binding: " + id)
}

var (
	listOutlets = readBinding(ListOperation)
	getOutlet   = readBinding(GetOperation)
)

func (c *Client) FetchOutlets(ctx context.Context, in identityv1.FetchOutletsInput, all bool, maxItems int) ([]json.RawMessage, output.PageInfo, string, error) {
	call := c.call()
	if all {
		in.Page, in.Total = 0, identityv1.TotalModeNone
	}
	return paging.Records(ctx, contracts.MustOperation(listOutlets.PublicID).Pagination, in.After, in.PageSize, all, maxItems, func(after string, size int) ([]byte, string, error) {
		in.After, in.PageSize = after, size
		raw, err := listOutlets.Invoke(ctx, call.api, &in, "")
		if err != nil {
			err = call.apiError(ctx, err)
		}
		return raw, call.d.CorrelationID, err
	})
}

// GetOutlet reads one outlet by its UUID.
func (c *Client) GetOutlet(ctx context.Context, in identityv1.FetchOutletByIDInput) (json.RawMessage, string, error) {
	call := c.call()
	raw, err := getOutlet.Invoke(ctx, call.api, &in, "")
	if err != nil {
		return nil, call.d.CorrelationID, call.apiError(ctx, err)
	}
	record, err := httpx.ResultMember(raw, "outlet", call.d.CorrelationID)
	return record, call.d.CorrelationID, err
}

// FindOutlet returns the first outlet the list filter in selects, such as
// an exact code; none is OUTLET_NOT_FOUND.
func (c *Client) FindOutlet(ctx context.Context, in identityv1.FetchOutletsInput) (json.RawMessage, string, error) {
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
