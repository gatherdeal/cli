package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/paging"
)

// pageFlags are the paging flags of a list command. Limits and the --total
// values come from the operation's pagination in contracts/manifest.json
// and the generated TotalMode.
func pageFlags(publicID string, resource string) []FlagSpec {
	pg := pagination(publicID)
	resource = strings.ReplaceAll(resource, "_", " ")
	return []FlagSpec{
		{Name: "page", Kind: kindInt, Usage: fmt.Sprintf("Page number (1-based; default 1). Page numbers reach at most %d rows deep; continue with --after beyond that.", pg.MaxOffset)},
		{Name: "page-size", Kind: kindInt, Usage: fmt.Sprintf(strings.ToUpper(resource[:1])+resource[1:]+" per page (1-%d; default %d, or %d with --all or --limit).", pg.MaxPageSize, pg.DefaultPageSize, pg.MaxPageSize)},
		{Name: "after", Kind: kindString, Usage: "Opaque cursor: the page after it (meta.page_info.next_cursor of the previous page). Not with --page; the other flags must stay the same."},
		{Name: "total", Kind: kindString, Values: []string{"exact", "none"}, Usage: "Whether the server counts matching " + resource + " (default: exact with --page, none with --after)."},
		{Name: "all", Kind: kindBool, Usage: fmt.Sprintf("Walk forward through every page with cursors (starting after --after when given) and return all %s in one data array, at most %d (the hard cap; then meta.page_info.has_next_page is true). A live traversal, not a snapshot: %s created, changed or deleted during the walk may be missed or seen out of order.", resource, paging.MaxItems, resource)},
		{Name: "limit", Kind: kindInt, Usage: fmt.Sprintf("Return at most this many %s (1-%d), following cursors across pages as needed (a walk like --all; not with --page or --total). meta.page_info.has_next_page is true if more exist; resume with --after <next_cursor> when present.", resource, paging.MaxItems)},
	}
}

// sortFlag is the --sort flag of a list operation that declares sortable
// fields. parse checks each field against the operation's contract.
func sortFlag(publicID string) FlagSpec {
	fields := contracts.MustOperation(publicID).SortableFields
	if len(fields) == 0 {
		panic(fmt.Sprintf("contracts: %s declares no sortable fields", publicID))
	}
	example := fields[0]
	if slices.Contains(fields, "create_at") {
		example = "create_at"
	}
	return FlagSpec{Name: "sort", Kind: kindString, Usage: fmt.Sprintf("Comma-separated fields to sort by, from: %s. A field sorts ascending; prefix it with - to sort descending (for example -%s).", strings.Join(fields, ", "), example)}
}

// validateSort checks a --sort value against the operation's sortable
// fields: one or more comma-separated names, each optionally prefixed with -.
func validateSort(value string, fields []string) error {
	for item := range strings.SplitSeq(value, ",") {
		if !slices.Contains(fields, strings.TrimPrefix(item, "-")) {
			return usageError("--sort %q: each field must be one of %s, optionally prefixed with - for descending", item, strings.Join(fields, ", "))
		}
	}
	return nil
}

// flagSearch is the server-side free-text search of the list operations
// whose API takes a search term (q).
var flagSearch = FlagSpec{Name: "search", Kind: kindString, Usage: "Server-side free-text search."}

// pagination returns the operation's adaptive pagination; its absence is
// a build defect the contract check catches.
func pagination(publicID string) contracts.Pagination {
	op := contracts.MustOperation(publicID)
	if op.Pagination == nil || op.Pagination.Style != contracts.PaginationAdaptive {
		panic(fmt.Sprintf("contracts: %s has no adaptive pagination", publicID))
	}
	return *op.Pagination
}

// validatePaging checks the paging flags against each other and the
// operation's limits.
func validatePaging(inv *Invocation) error {
	pg := pagination(inv.Command.Operation)
	walk, _ := inv.walk()
	switch {
	case inv.isSet("page") && inv.integer("page") < 1:
		return usageError("--page must be 1 or more")
	case inv.isSet("page-size") && (inv.integer("page-size") < 1 || inv.integer("page-size") > pg.MaxPageSize):
		return usageError("--page-size must be between 1 and %d", pg.MaxPageSize)
	case inv.isSet("page") && inv.isSet("after"):
		return usageError("--page and --after cannot be combined")
	case inv.isSet("after") && inv.str("after") == "":
		return usageError("--after must be a cursor")
	case inv.isSet("limit") && (inv.integer("limit") < 1 || inv.integer("limit") > paging.MaxItems):
		return usageError("--limit must be between 1 and %d", paging.MaxItems)
	case walk && inv.isSet("page"):
		return usageError("--all and --limit walk with cursors; they cannot be combined with --page (use --after to resume)")
	case walk && inv.isSet("total"):
		return usageError("--all and --limit never count; they cannot be combined with --total")
	}
	return nil
}

// walk reports whether a list follows cursors across pages (--all or
// --limit) and the most records it may return.
func (inv *Invocation) walk() (bool, int) {
	if !inv.boolean("all") && !inv.isSet("limit") {
		return false, 0
	}
	if inv.isSet("limit") {
		return true, inv.integer("limit")
	}
	return true, paging.MaxItems
}
