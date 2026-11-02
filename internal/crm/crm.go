// Package crm is the seam between the `crm contacts` commands and the CRM API.
//
// The commands depend only on the Contacts interface below. Client
// implements it over the generated, dependency-free client in
// internal/generated/crm/v1. Request parameters are CLI-shaped; payloads are
// the generated DTOs.
package crm

import (
	"context"

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
	Page int
	// PageSize is 1..100; 0 means the server default.
	PageSize int
	// After is an opaque cursor (a previous page's next_cursor): the page
	// after it. Exclusive with Page.
	After string
	// Total selects whether the server counts the rows ("exact" or "none");
	// empty means the server default (exact in page mode, none with After).
	Total crmv1.TotalMode
	// Search is free text matched by the server.
	Search string
	// SortBy is a field name; SortOrder is "asc" or "desc".
	SortBy    string
	SortOrder string
	// OutletID narrows the request to one outlet of the access.
	OutletID string
	Expand   Expansions
	// TagIDs and TagNames filter by tag through the TagsEdge edge filter
	// (any match).
	TagIDs   []string
	TagNames []string
}

// GetContactParams selects one contact.
type GetContactParams struct {
	ID       string
	OutletID string
	Expand   Expansions
}

// Contact is the generated contact DTO.
type Contact = crmv1.Contact

// ListContactsResult is one page of contacts.
type ListContactsResult struct {
	Contacts []*Contact
	// PageInfo is the page's ListPage fields other than its items.
	PageInfo      output.PageInfo
	CorrelationID string
}

// ListAllContactsResult is every contact a forward walk returned.
type ListAllContactsResult struct {
	Contacts []*Contact
	// Truncated is true when maxItems stopped the walk before the end.
	Truncated bool
	// NextCursor is absent if the server overfilled a page and the cap cut it.
	NextCursor    string
	CorrelationID string
}

// GetContactResult is one contact.
type GetContactResult struct {
	Contact       *Contact
	CorrelationID string
}
