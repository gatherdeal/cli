package crm

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/paging"

	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
)

// Public ids of the operations Client calls (contracts/manifest.json).
const (
	ListOperation = crmv1.Service + ".contacts.list"
	GetOperation  = crmv1.Service + ".contacts.get"
)

// TagsEdge is the edge filter --tag and --tag-id go through. It must be one
// of the list operation's delegated_edge_filters.
const TagsEdge = "tags"

// expansionPrefix starts every expansion query parameter.
const expansionPrefix = "with_"

// Client implements Contacts over the generated crmv1 client.
type Client struct {
	http *http.Client
	root string
}

var _ Contacts = (*Client)(nil)

// New returns a Client that sends every request through httpClient (the
// origin-bound, credentialed client). serviceURL is the profile's CRM base
// URL from discovery (server origin plus services.crm.base_path, e.g.
// https://api.gatherdeal.ai/crm/v1). Discovery advertises the versioned
// path while the generated client appends "/v1/..." to the service root, so
// the version segment must be the one this CLI was generated for.
func New(httpClient *http.Client, serviceURL string) (*Client, error) {
	root, ok := strings.CutSuffix(serviceURL, "/"+crmv1.Version)
	if !ok || root == "" {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid,
			"the server advertises the CRM API at %s; this CLI speaks CRM %s", serviceURL, crmv1.Version)
	}
	if _, err := crmv1.NewClient(root, httpClient); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "invalid CRM base URL").WithCause(err)
	}
	return &Client{http: httpClient, root: root}, nil
}

// call is one request's generated client. Its doer adds the outlet
// selector, when set, and records the response's correlation id, which the
// generated outputs do not carry.
type call struct {
	api *crmv1.Client
	d   *httpx.ResponseDoer
}

func (c *Client) call(outletID string) call {
	d := &httpx.ResponseDoer{Next: c.http, OutletID: outletID}
	api, err := crmv1.NewClient(c.root, d)
	if err != nil {
		// New validated root, and d is never nil.
		panic(err)
	}
	return call{api: api, d: d}
}

func (c *Client) InvokeRead(ctx context.Context, binding crmv1.CLIBinding, input any) (jsontext.Value, string, error) {
	call := c.call("")
	raw, err := binding.Invoke(ctx, call.api, input, "")
	if err != nil {
		err = call.apiError(ctx, err)
	}
	return raw, call.d.CorrelationID, err
}

// listInput builds the generated list input from p.
func listInput(p ListContactsParams) (*crmv1.FetchContactsInput, error) {
	in := &crmv1.FetchContactsInput{
		Page: p.Page, PageSize: p.PageSize, After: p.After, Total: p.Total,
		SearchTerm: p.Search, SortBy: p.SortBy, SortOrder: p.SortOrder,
	}
	if err := SetExpansions(in, p.Expand); err != nil {
		return nil, err
	}
	f, err := TagsFilter(p.TagIDs, p.TagNames)
	if err != nil {
		return nil, err
	}
	in.Filter = f
	return in, nil
}

func (c *Client) ListContacts(ctx context.Context, p ListContactsParams) (*ListContactsResult, error) {
	in, err := listInput(p)
	if err != nil {
		return nil, err
	}
	call := c.call(p.OutletID)
	var response struct {
		Contacts *struct {
			output.PageInfo
			Data []json.RawMessage `json:"data"`
		} `json:"contacts"`
	}
	if err := call.read(ctx, listBinding, in, &response); err != nil {
		return nil, err
	}
	page := response.Contacts
	if page == nil {
		return nil, call.protocolError("the contacts response has no contacts page")
	}
	if page.Data == nil {
		page.Data = []json.RawMessage{}
	}
	return &ListContactsResult{Contacts: page.Data, PageInfo: page.PageInfo, CorrelationID: call.d.CorrelationID}, nil
}

// Preserve field presence from the producer when projecting records with --fields or --jq.
func (c call) read(ctx context.Context, binding crmv1.CLIBinding, input, dest any) error {
	raw, err := binding.Invoke(ctx, c.api, input, "")
	if err != nil {
		return c.apiError(ctx, err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return c.protocolError("invalid contact response")
	}
	return nil
}

func contactBinding(id string) crmv1.CLIBinding {
	for _, b := range crmv1.CLIBindings() {
		if b.PublicID == id {
			return b
		}
	}
	panic("missing generated binding: " + id)
}

var listBinding = contactBinding(ListOperation)
var getBinding = contactBinding(GetOperation)

func (c *Client) ListAllContacts(ctx context.Context, p ListContactsParams, maxItems int) (*ListAllContactsResult, error) {
	p.Page, p.Total = 0, crmv1.TotalModeNone
	if p.PageSize == 0 {
		p.PageSize = contracts.MustOperation(ListOperation).Pagination.MaxPageSize
	}
	res, err := paging.Walk(ctx, "contacts", p.After, p.PageSize, maxItems, func(ctx context.Context, after string, size int) (paging.Page[json.RawMessage], error) {
		p.After, p.PageSize = after, size
		page, err := c.ListContacts(ctx, p)
		if err != nil {
			return paging.Page[json.RawMessage]{}, err
		}
		return paging.Page[json.RawMessage]{Items: page.Contacts, HasNextPage: page.PageInfo.HasNextPage, NextCursor: page.PageInfo.NextCursor, CorrelationID: page.CorrelationID}, nil
	})
	if err != nil {
		return nil, err
	}
	return &ListAllContactsResult{Contacts: res.Items, Truncated: res.Truncated, NextCursor: res.NextCursor, CorrelationID: res.CorrelationID}, nil
}

func (c *Client) GetContact(ctx context.Context, p GetContactParams) (*GetContactResult, error) {
	in := &crmv1.FetchContactByIDInput{ID: p.ID}
	if err := SetExpansions(in, p.Expand); err != nil {
		return nil, err
	}
	call := c.call(p.OutletID)
	var response struct {
		Contact json.RawMessage `json:"contact"`
	}
	if err := call.read(ctx, getBinding, in, &response); err != nil {
		return nil, err
	}
	if len(response.Contact) == 0 || string(response.Contact) == "null" {
		return nil, call.protocolError("the contact response has no contact")
	}
	return &GetContactResult{Contact: response.Contact, CorrelationID: call.d.CorrelationID}, nil
}

func (c call) protocolError(msg string) *output.Error {
	return &output.Error{
		Code: output.CodeProtocol, Message: msg, Exit: output.ExitUnclassified,
		HTTPStatus: http.StatusOK, CorrelationID: c.d.CorrelationID,
	}
}

// TagsFilter returns the filter matching contacts with any of the tag ids
// or names through TagsEdge, or nil when both are empty.
func TagsFilter(ids, names []string) (*crmv1.Group, error) {
	if len(ids) == 0 && len(names) == 0 {
		return nil, nil
	}
	edge := &crmv1.Group{Conjunction: crmv1.ConjunctionOR, Edge: TagsEdge, Quantifier: crmv1.QuantifierSome}
	for _, r := range []struct {
		field  string
		values []string
	}{{"id", ids}, {"name", names}} {
		if len(r.values) == 0 {
			continue
		}
		v, err := json.Marshal(r.values)
		if err != nil {
			return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot encode the tag filter").WithCause(err)
		}
		edge.Rules = append(edge.Rules, &crmv1.Rule{Field: r.field, Op: crmv1.OperatorIN, Value: v})
	}
	return &crmv1.Group{Conjunction: crmv1.ConjunctionAND, Groups: []*crmv1.Group{edge}}, nil
}

// SetExpansions sets the generated input's bool field for each expansion
// parameter: with_tags is field WithTags. A name without a
// matching field is a build defect (the contract check tests resolve every
// manifest expansion against both inputs).
func SetExpansions(input any, names Expansions) error {
	v := reflect.ValueOf(input)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return output.Errorf(output.ExitUnclassified, output.CodeInternal, "expansion target %T is not a struct pointer", input)
	}
	for _, name := range names {
		f := v.Elem().FieldByName(ExpansionField(name))
		if !strings.HasPrefix(name, expansionPrefix) || !f.IsValid() || f.Kind() != reflect.Bool {
			return output.Errorf(output.ExitUnclassified, output.CodeInternal, "%T has no expansion %q", input, name)
		}
		f.SetBool(true)
	}
	return nil
}

// ExpansionField is the generated input field of an expansion parameter.
func ExpansionField(param string) string {
	var b strings.Builder
	for word := range strings.SplitSeq(param, "_") {
		if word != "" {
			b.WriteString(strings.ToUpper(word[:1]) + word[1:])
		}
	}
	return b.String()
}

// apiError maps a generated-client failure to the CLI's *output.Error: a
// problem document by its status and code (httpx.ProblemError), a non-2xx
// without one by its HTTP status, a contract violation as a protocol error
// and anything else as a transport failure.
func (c call) apiError(ctx context.Context, err error) error {
	var problem *crmv1.Problem
	if errors.As(err, &problem) {
		p := problem
		return httpx.ProblemError(&httpx.Problem{Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: p.Instance, Code: p.Code, Retryable: p.Retryable, RetryAfterMS: p.RetryAfterMS, CorrelationID: p.CorrelationID, Details: p.Details, Errors: p.Errors, ContentLanguage: p.ContentLanguage}, c.d.Problem).WithCause(err)
	}
	var status *crmv1.StatusError
	if errors.As(err, &status) {
		return httpx.StatusError(status.HTTPStatus, status.CorrelationID).WithCause(err)
	}
	var protocol *crmv1.ProtocolError
	if errors.As(err, &protocol) {
		return httpx.ProtocolError(protocol.HTTPStatus, protocol.CorrelationID, protocol.Reason).WithCause(err)
	}
	return httpx.TransportError(ctx, err)
}
