package whatsapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/conformance"
	wa "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestConformance(t *testing.T) {
	for _, fc := range conformance.Load(t) {
		t.Run(fc.Name, func(t *testing.T) {
			base := "https://api.example.com"
			c, err := New(httpx.New(httpx.Options{Origin: base, Base: roundTripper(func(r *http.Request) (*http.Response, error) { return fc.HTTPResponse(r), nil })}), base+"/whatsapp/v1")
			if err != nil {
				t.Fatal(err)
			}
			call := c.call("")
			_, err = call.api.FetchAgent(t.Context(), &wa.FetchAgentInput{AgentID: "agent"})
			if fc.Expect.Kind == conformance.KindSuccess {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			got := output.AsError(call.apiError(t.Context(), err, false))
			_, wantErr := httpx.DecodeAPI(fc.Response.Status, fc.Header(), []byte(fc.Response.Body))
			want := output.AsError(wantErr)
			if got.Exit != fc.WantExit() || got.HTTPStatus != fc.Expect.Status || got.Code != want.Code || got.Retryable != want.Retryable || got.Message != want.Message || got.CorrelationID != want.CorrelationID || string(got.Problem) != string(want.Problem) {
				t.Fatalf("adapter %+v; want %+v", got, want)
			}
		})
	}
}
func TestWriteUnknownOutcomeAndKey(t *testing.T) {
	for _, body := range []string{"transport", "not JSON", `{"status":"success"}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			cause := errors.New("connection lost")
			base := "https://api.example.com"
			c, err := New(httpx.New(httpx.Options{Origin: base, Base: roundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Idempotency-Key") != "stable-key" {
					t.Errorf("key %q", r.Header.Get("Idempotency-Key"))
				}
				if body == "transport" {
					return nil, cause
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}), base+"/whatsapp/v1")
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = c.CreateAgent(t.Context(), &wa.CreateAgentInput{OutletID: "outlet"}, "outlet", "stable-key")
			e := output.AsError(err)
			if calls != 1 || e.Code != output.CodeWriteOutcomeUnknown || e.Exit != 8 || e.Retryable {
				t.Fatalf("calls=%d error=%+v", calls, e)
			}
			if body == "transport" && !errors.Is(err, cause) {
				t.Fatal("lost cause")
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
		if q.Get("outlet_id") != "outlet" || q.Get("total") != "none" || q.Has("p") {
			t.Errorf("query %s", q)
		}
		body := `{"status":"success","agents":{"data":[{"id":"a"},{"id":"b"}],"has_next_page":true,"next_cursor":"next"}}`
		if calls == 1 && q.Get("count") != "3" {
			t.Errorf("size %s", q.Get("count"))
		}
		if calls == 2 {
			if q.Get("count") != "1" || q.Get("after") != "next" {
				t.Errorf("query %s", q)
			}
			body = `{"status":"success","agents":{"data":[{"id":"c"}],"has_next_page":true,"next_cursor":"resume"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}), base+"/whatsapp/v1")
	if err != nil {
		t.Fatal(err)
	}
	items, page, _, err := c.FetchAgents(context.Background(), wa.FetchAgentsInput{}, "outlet", true, 3)
	if err != nil || calls != 2 || len(items) != 3 || !page.HasNextPage || page.HasPrevPage || page.NextCursor != "resume" {
		t.Fatalf("items=%v page=%+v err=%v", items, page, err)
	}
}
