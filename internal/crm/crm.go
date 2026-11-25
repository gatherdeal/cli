// Package crm is the seam between the `crm contacts` commands and the CRM API.
//
// The commands depend only on the Contacts interface below. Client
// implements it over the generated, dependency-free client in
// internal/generated/crm/v1. Request parameters are CLI-shaped; payloads are
// the record JSON returned through the generated bindings.
package crm

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"

	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	"github.com/gatherdeal/cli/internal/output"
)

// Contacts is the CRM surface the CLI uses. Implementations must:
//
//   - send every request through the *http.Client they are given (it is
//     bound to the profile's server origin, carries the credential and
//     refuses redirects), never a client of their own;
//   - return API failures as *output.Error built from the problem document
//     (httpx.ProblemError), so the exit code follows the problem's status
//     and its retryable member decides retryability.
type Contacts interface {
	// Invoke dispatches a generated binding; opts carry request preconditions
	// such as If-Match unchanged.
	Invoke(ctx context.Context, binding crmv1.CLIBinding, input any, outlet, key string, write bool, opts ...crmv1.RequestOption) (jsontext.Value, string, error)
	ListContacts(ctx context.Context, p ListContactsParams) (*ListContactsResult, error)
	// ListAllContacts walks forward through every page with cursors (the
	// shared bounded walker: total=none, 100 per page unless p.PageSize is
	// set), starting after p.After when set. It ignores p.Page and p.Total.
	// maxItems > 0 caps the result. It is a live traversal, not a snapshot:
	// rows created, changed or deleted during the walk may be missed or
	// seen out of order.
	ListAllContacts(ctx context.Context, p ListContactsParams, maxItems int) (*ListAllContactsResult, error)
	GetContact(ctx context.Context, p GetContactParams) (*GetContactResult, error)
}

// Expansions are the with_* query parameters to set, by wire name (for
// example "with_phones"). The commands offer exactly the operation's
// delegated_expansions from contracts/manifest.json; the server refuses any
// other expansion for delegated access.
type Expansions []string

// ListContactsParams selects one page of contacts.
type ListContactsParams struct {
	// Page is 1-based; 0 means the server default (page 1, or cursor mode
	// with After).
	Page int `json:"page,omitempty"`
	// PageSize is 1..100; 0 means the server default.
	PageSize int `json:"page_size,omitempty"`
	// After is an opaque cursor (a previous page's next_cursor): the page
	// after it. Exclusive with Page.
	After string `json:"after,omitempty"`
	// Total selects whether the server counts the rows ("exact" or "none");
	// empty means the server default (exact in page mode, none with After).
	Total crmv1.TotalMode `json:"total,omitempty"`
	// Search is free text matched by the server.
	Search string `json:"search,omitempty"`
	// Sort lists comma-separated fields; a "-" prefix sorts that field
	// descending, a bare field ascending.
	Sort string `json:"sort,omitempty"`
	// OutletID narrows the request to one outlet of the access.
	OutletID string     `json:"outlet_id,omitempty"`
	Expand   Expansions `json:"expansions,omitempty"`
	// TagIDs and TagNames filter by tag through the TagsEdge edge filter
	// (any match).
	TagIDs   []string `json:"tag_ids,omitempty"`
	TagNames []string `json:"tag_names,omitempty"`
}

// GetContactParams selects one contact.
type GetContactParams struct {
	ID       string
	OutletID string
	Expand   Expansions
}

// Contact is the generated contact DTO.
type Contact = crmv1.ContactView

// ListContactsResult is one page of contacts.
type ListContactsResult struct {
	Contacts []json.RawMessage
	// PageInfo is the page's ListPage fields other than its items.
	PageInfo      output.PageInfo
	CorrelationID string
}

// ListAllContactsResult is every contact a forward walk returned.
type ListAllContactsResult struct {
	Contacts []json.RawMessage
	// Truncated is true when maxItems stopped the walk before the end.
	Truncated bool
	// NextCursor is absent if the server overfilled a page and the cap cut it.
	NextCursor    string
	CorrelationID string
}

// GetContactResult is one contact.
type GetContactResult struct {
	Contact       json.RawMessage
	CorrelationID string
}
