package paging

import (
	"context"
	"encoding/json"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
)

// Records returns a single page or walks the original record JSON. The
// adapter supplies the generated request, including its non-paging filters.
func Records(ctx context.Context, pg *contracts.Pagination, after string, size int, all bool, limit int, fetch func(string, int) ([]byte, string, error)) ([]json.RawMessage, output.PageInfo, string, error) {
	read := func(cursor string, count int) ([]json.RawMessage, output.PageInfo, string, error) {
		var page struct {
			output.PageInfo
			Data []json.RawMessage `json:"data"`
		}
		raw, corr, err := fetch(cursor, count)
		if err != nil {
			return nil, page.PageInfo, corr, err
		}
		result, err := httpx.ResultMember(raw, pg.ResultField, corr)
		if err != nil {
			return nil, page.PageInfo, corr, err
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, output.PageInfo{}, corr, protocolError("invalid "+pg.ResultField+" page", corr)
		}
		if page.Data == nil {
			page.Data = []json.RawMessage{}
		}
		return page.Data, page.PageInfo, corr, nil
	}
	if !all {
		return read(after, size)
	}
	if size == 0 {
		size = pg.MaxPageSize
	}
	res, err := Walk(ctx, pg.ResultField, after, size, limit, func(_ context.Context, cursor string, count int) (Page[json.RawMessage], error) {
		data, info, corr, err := read(cursor, count)
		return Page[json.RawMessage]{Items: data, HasNextPage: info.HasNextPage, NextCursor: info.NextCursor, CorrelationID: corr}, err
	})
	if err != nil {
		return nil, output.PageInfo{}, "", err
	}
	return res.Items, output.PageInfo{HasNextPage: res.Truncated, NextCursor: res.NextCursor, HasPrevPage: after != ""}, res.CorrelationID, nil
}
