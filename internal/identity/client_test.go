package identity

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/conformance"
	identityv1 "github.com/gatherdeal/cli/internal/generated/identity/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestConformance(t *testing.T) {
	for _, fc := range conformance.Load(t) {
		t.Run(fc.Name, func(t *testing.T) {
			base := "https://api.example.com"
			c, err := New(httpx.New(httpx.Options{Origin: base, Base: roundTripper(func(r *http.Request) (*http.Response, error) { return fc.HTTPResponse(r), nil })}), base+"/identity/v1")
			if err != nil {
				t.Fatal(err)
			}
			call := c.call()
			_, err = call.api.FetchOutlets(t.Context(), &identityv1.FetchOutletsInput{})
			if fc.Expect.Kind == conformance.KindSuccess {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			got := output.AsError(call.apiError(t.Context(), err))
			_, wantErr := httpx.DecodeAPI(fc.Response.Status, fc.Header(), []byte(fc.Response.Body))
			want := output.AsError(wantErr)
			if got.Exit != fc.WantExit() || got.HTTPStatus != fc.Expect.Status || got.Code != want.Code || got.Retryable != want.Retryable || got.Message != want.Message || got.CorrelationID != want.CorrelationID || string(got.Problem) != string(want.Problem) {
				t.Fatalf("adapter %+v; want %+v", got, want)
			}
		})
	}
}

func TestListAllUsesBoundedPaging(t *testing.T) {
	calls := 0
	base := "https://api.example.com"
	c, err := New(httpx.New(httpx.Options{Origin: base, Base: roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		if q.Get("code") != "HQ" || q.Get("total") != "none" || q.Has("p") || q.Has("outlet_id") {
			t.Errorf("query %s", q)
		}
		body := `{"status":"success","outlets":{"data":[{"id":"a"},{"id":"b"}],"has_next_page":true,"next_cursor":"next"}}`
		if calls == 1 && q.Get("count") != "3" {
			t.Errorf("size %s", q.Get("count"))
		}
		if calls == 2 {
			if q.Get("count") != "1" || q.Get("after") != "next" {
				t.Errorf("query %s", q)
			}
			body = `{"status":"success","outlets":{"data":[{"id":"c"}],"has_next_page":true,"next_cursor":"resume"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}), base+"/identity/v1")
	if err != nil {
		t.Fatal(err)
	}
	items, page, _, err := c.FetchOutlets(context.Background(), identityv1.FetchOutletsInput{Codes: []string{"HQ"}}, true, 3)
	if err != nil || calls != 2 || len(items) != 3 || !page.HasNextPage || page.HasPrevPage || page.NextCursor != "resume" {
		t.Fatalf("items=%v page=%+v err=%v", items, page, err)
	}
}
