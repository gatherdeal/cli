// Package paging implements bounded adaptive cursor traversal for any typed service.
package paging

import (
	"context"
	"net/http"

	"github.com/gatherdeal/cli/internal/output"
)

// MaxItems is the hard cap of one walk: a walk without a smaller cap stops
// after this many items and reports a resumable truncation, so --all can
// never loop or grow without bound.
const MaxItems = 10000

type Page[T any] struct {
	Items         []T
	HasNextPage   bool
	NextCursor    string
	CorrelationID string
}
type Result[T any] struct {
	Items         []T
	Truncated     bool
	NextCursor    string
	CorrelationID string
}

// Walk requests smaller pages as a cap approaches. maxItems outside
// 1..MaxItems means MaxItems. An overfilled page cannot supply a safe
// continuation. Failure discards the entire accumulated result.
func Walk[T any](ctx context.Context, resource, after string, pageSize, maxItems int, fetch func(context.Context, string, int) (Page[T], error)) (*Result[T], error) {
	if maxItems <= 0 || maxItems > MaxItems {
		maxItems = MaxItems
	}
	res := &Result[T]{Items: []T{}}
	// The walk allows one request per item (short pages are valid), plus
	// eight empty pages.
	budget := maxItems + 8
	seen := map[string]bool{after: true}
	for range budget {
		size := min(pageSize, maxItems-len(res.Items))
		page, err := fetch(ctx, after, size)
		if err != nil {
			return nil, err
		}
		res.CorrelationID = page.CorrelationID
		if len(page.Items) > maxItems-len(res.Items) {
			// The server ignored the requested size. Its cursor skips rows
			// we have not returned, so this truncated result has no cursor.
			res.Items = append(res.Items, page.Items[:maxItems-len(res.Items)]...)
			res.Truncated = true
			return res, nil
		}
		res.Items = append(res.Items, page.Items...)
		if !page.HasNextPage {
			return res, nil
		}
		if page.NextCursor == "" || seen[page.NextCursor] {
			return nil, protocolError("the "+resource+" walk has a missing or repeated next cursor", res.CorrelationID)
		}
		if len(res.Items) == maxItems {
			res.Truncated, res.NextCursor = true, page.NextCursor
			return res, nil
		}
		seen[page.NextCursor] = true
		after = page.NextCursor
	}
	return nil, protocolError("the "+resource+" walk exceeded its page request budget (too many empty or short pages); use a smaller --limit and resume with --after", res.CorrelationID)
}
func protocolError(message, correlation string) error {
	return &output.Error{Code: output.CodeProtocol, Message: message, Exit: output.ExitUnclassified, HTTPStatus: http.StatusOK, CorrelationID: correlation}
}
