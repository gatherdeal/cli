package crm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/conformance"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
)

const (
	contactID = "55555555-5555-5555-5555-555555555555"
	outletID  = "33333333-3333-3333-3333-333333333333"
)

// crmFake answers the CRM routes under /crm/v1 with a fixed status and body
// and records the last request.
type crmFake struct {
	status int
	body   string
	header http.Header
	path   string
	query  url.Values
}

func newClient(t *testing.T, f *crmFake) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path, f.query = r.URL.Path, r.URL.Query()
		for k, v := range f.header {
			w.Header()[k] = v
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.Header().Set("X-Correlation-Id", "corr-header")
		status := f.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(httpx.New(httpx.Options{Origin: srv.URL, Token: "gd_dat_test"}), srv.URL+"/crm/v1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func cliError(t *testing.T, err error) *output.Error {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %T %v is not *output.Error", err, err)
	}
	return e
}

func TestListContactsPage(t *testing.T) {
	f := &crmFake{body: `{"status":"success","correlation_id":"corr-body","contacts":{"data":[` +
		`{"id":"` + contactID + `","kind":"personal","name":"Ali","is_verified":true,` +
		`"phones":[{"id":"77777777-7777-7777-7777-777777777777","phone":"+60123456789"}],` +
		`"tags":[{"id":"66666666-6666-6666-6666-666666666666","name":"VIP","assigned_at":"2026-01-02T03:04:05Z"}]}],` +
		`"page":2,"page_size":1,"total_items":3,"total_pages":3,"has_next_page":true}}`}
	c := newClient(t, f)
	res, err := c.ListContacts(t.Context(), ListContactsParams{
		Page: 2, PageSize: 1, Search: "ali", SortBy: "name", SortOrder: "asc", OutletID: outletID,
		Expand: Expansions{"with_phones", "with_tags"}, TagNames: []string{"VIP"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.path != "/crm/v1/contacts" {
		t.Fatalf("path %s", f.path)
	}
	for k, want := range map[string]string{"p": "2", "count": "1", "q": "ali", "s": "name", "o": "asc",
		"outlet_id": outletID, "with_phones": "true", "with_tags": "true"} {
		if got := f.query.Get(k); got != want {
			t.Errorf("query %s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"with_emails", "with_attributes", "with_referrer"} {
		if f.query.Has(k) {
			t.Errorf("unexpected query %s", k)
		}
	}
	if !f.query.Has("filter") {
		t.Error("no tag filter sent")
	}
	pi := res.PageInfo
	if pi.Page != 2 || pi.PageSize != 1 || *pi.TotalItems != 3 || *pi.TotalPages != 3 || !pi.HasNextPage ||
		len(res.Contacts) != 1 || res.CorrelationID != "corr-header" {
		t.Fatalf("result %+v", res)
	}
	got := res.Contacts[0]
	if got.Name != "Ali" || !got.IsVerified || len(got.Phones) != 1 || got.Tags[0].Name != "VIP" {
		t.Fatalf("contact %+v", got)
	}
}

func TestListContactsCursor(t *testing.T) {
	f := &crmFake{body: `{"status":"success","contacts":{"data":[],"page_size":20,"has_next_page":true,"has_prev_page":true,"next_cursor":"n2","prev_cursor":"p2"}}`}
	res, err := newClient(t, f).ListContacts(t.Context(), ListContactsParams{After: "c1", Total: crmv1.TotalModeExact})
	if err != nil {
		t.Fatal(err)
	}
	if f.query.Get("after") != "c1" || f.query.Get("total") != "exact" || f.query.Has("p") {
		t.Fatalf("query %s", f.query.Encode())
	}
	pi := res.PageInfo
	if pi.NextCursor != "n2" || pi.PrevCursor != "p2" || !pi.HasPrevPage || pi.Page != 0 || pi.TotalItems != nil {
		t.Fatalf("page info %+v", pi)
	}
}

// pagedFake serves pages keyed by the after cursor and records the queries.
func pagedFake(t *testing.T, pages map[string]string) (*Client, *[]url.Values) {
	t.Helper()
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-Id", "corr-"+strconv.Itoa(len(queries)))
		_, _ = w.Write([]byte(pages[r.URL.Query().Get("after")]))
	}))
	t.Cleanup(srv.Close)
	c, err := New(httpx.New(httpx.Options{Origin: srv.URL}), srv.URL+"/crm/v1")
	if err != nil {
		t.Fatal(err)
	}
	return c, &queries
}

func page(ids []string, next string) string {
	var items []string
	for _, id := range ids {
		items = append(items, `{"id":"`+id+`"}`)
	}
	more := next != ""
	body := `{"status":"success","contacts":{"data":[` + strings.Join(items, ",") + `],"page_size":2,"has_next_page":` + strconv.FormatBool(more)
	if more {
		body += `,"next_cursor":"` + next + `"`
	}
	return body + "}}"
}

func TestListAllContacts(t *testing.T) {
	pages := map[string]string{"": page([]string{"a", "b"}, "c1"), "c1": page([]string{"c", "d"}, "c2"), "c2": page([]string{"e"}, "")}
	c, queries := pagedFake(t, pages)
	res, err := c.ListAllContacts(t.Context(), ListContactsParams{Page: 3, Total: crmv1.TotalModeExact, Search: "x"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contacts) != 5 || res.Truncated || res.CorrelationID != "corr-3" {
		t.Fatalf("result %+v", res)
	}
	q := (*queries)[0]
	if q.Has("p") || q.Get("total") != "none" || q.Get("count") != "100" || q.Get("q") != "x" {
		t.Fatalf("first query %s", q.Encode())
	}
	if (*queries)[2].Get("after") != "c2" || (*queries)[2].Get("q") != "x" {
		t.Fatalf("third query %s", (*queries)[2].Encode())
	}

	// --limit: exactly at the end is not truncated; below it is.
	c, _ = pagedFake(t, pages)
	if res, err := c.ListAllContacts(t.Context(), ListContactsParams{}, 5); err != nil || len(res.Contacts) != 5 || res.Truncated {
		t.Fatalf("cap 5: %+v %v", res, err)
	}
	c, queries = pagedFake(t, pages)
	res, err = c.ListAllContacts(t.Context(), ListContactsParams{After: "c1"}, 1)
	if err != nil || len(res.Contacts) != 1 || res.Contacts[0].ID != "c" || !res.Truncated || len(*queries) != 1 {
		t.Fatalf("resume capped: %+v %v (%d requests)", res, err, len(*queries))
	}
}

func TestListAllContactsStops(t *testing.T) {
	cases := map[string]map[string]string{
		"repeated cursor":  {"": page([]string{"a"}, "c1"), "c1": page([]string{"b"}, "c1")},
		"no next cursor":   {"": `{"status":"success","contacts":{"data":[{"id":"a"}],"page_size":1,"has_next_page":true}}`},
		"missing contacts": {"": `{"status":"success"}`},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := pagedFake(t, pages)
			_, err := c.ListAllContacts(t.Context(), ListContactsParams{}, 0)
			if e := cliError(t, err); e.Code != output.CodeProtocol || e.Exit != output.ExitUnclassified || e.Retryable {
				t.Fatalf("got %+v", e)
			}
		})
	}
	// A problem in the middle of the walk is classified like any call.
	fake := &crmFake{status: 400, header: problemHeader, body: problemBody(400, crmv1.CodePaginationCursorInvalid, `{"reason":"query_mismatch"}`)}
	_, err := newClient(t, fake).ListAllContacts(t.Context(), ListContactsParams{After: "x"}, 0)
	if e := cliError(t, err); e.Code != crmv1.CodePaginationCursorInvalid || e.Exit != output.ExitUsage {
		t.Fatalf("got %+v", e)
	}
}

func TestListContactsEmptyPage(t *testing.T) {
	c := newClient(t, &crmFake{body: `{"status":"success","contacts":{"data":null,"page":1,"page_size":20,"total_items":0,"total_pages":0,"has_next_page":false}}`})
	res, err := c.ListContacts(t.Context(), ListContactsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Contacts == nil || len(res.Contacts) != 0 {
		t.Fatalf("contacts %#v", res.Contacts)
	}
}

func TestGetContact(t *testing.T) {
	f := &crmFake{body: `{"status":"success","contact":{"id":"` + contactID + `","name":"Ali","emails":[{"email":"ali@example.com"}]}}`}
	c := newClient(t, f)
	res, err := c.GetContact(t.Context(), GetContactParams{ID: contactID, Expand: Expansions{"with_emails"}})
	if err != nil {
		t.Fatal(err)
	}
	if f.path != "/crm/v1/contacts/"+contactID || f.query.Get("with_emails") != "true" || f.query.Has("outlet_id") {
		t.Fatalf("request %s?%s", f.path, f.query.Encode())
	}
	if res.Contact.ID != contactID || len(res.Contact.Emails) != 1 {
		t.Fatalf("contact %+v", res.Contact)
	}
}

var problemHeader = http.Header{"Content-Type": {crmv1.ProblemMediaType}}

func problemBody(status int, code, details string) string {
	body := `{"type":"https://docs.gatherdeal.ai/errors/` + code + `","title":"t","status":` + strconv.Itoa(status) +
		`,"code":"` + code + `","retryable":` + strconv.FormatBool(status == 429 || status == 503)
	if details != "" {
		body += `,"details":` + details
	}
	return body + "}"
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		header    http.Header
		code      string
		exit      output.ExitCode
		retryable bool
	}{
		{"out of scope", 404, problemBody(404, crmv1.CodeCRMContactNotFound, `{"contact_id":"x"}`), problemHeader, crmv1.CodeCRMContactNotFound, output.ExitNotFound, false},
		{"expansion denied", 403, problemBody(403, crmv1.CodeDelegatedAccessExpansionNotAllowed, `{"reason":"expansion_not_allowed"}`), problemHeader, crmv1.CodeDelegatedAccessExpansionNotAllowed, output.ExitDenied, false},
		{"permission denied", 403, problemBody(403, crmv1.CodeDelegatedAccessPermissionDenied, ""), problemHeader, crmv1.CodeDelegatedAccessPermissionDenied, output.ExitDenied, false},
		{"access inactive", 401, problemBody(401, crmv1.CodeDelegatedAccessInactive, `{"reason":"revoked"}`), problemHeader, crmv1.CodeDelegatedAccessInactive, output.ExitAuth, false},
		{"plain 401", 401, problemBody(401, crmv1.CodeUnauthorized, ""), problemHeader, crmv1.CodeUnauthorized, output.ExitAuth, false},
		{"bad cursor", 400, problemBody(400, crmv1.CodePaginationCursorInvalid, `{"reason":"query_mismatch"}`), problemHeader, crmv1.CodePaginationCursorInvalid, output.ExitUsage, false},
		{"validation failed", 422, problemBody(422, crmv1.CodeValidationFailed, ""), problemHeader, crmv1.CodeValidationFailed, output.ExitConflict, false},
		{"rate limited", 429, problemBody(429, "RATE_LIMITED", ""), problemHeader, "RATE_LIMITED", output.ExitRateLimited, true},
		{"internal error is not retryable", 500, problemBody(500, crmv1.CodeInternalError, ""), problemHeader, crmv1.CodeInternalError, output.ExitUnavailable, false},
		{"client too old", 426, problemBody(426, crmv1.CodeClientUpgradeRequired, `{"client":"gatherdeal-cli","min_version":"0.2.0","current_version":"0.1.0"}`), problemHeader, crmv1.CodeClientUpgradeRequired, output.ExitActionRequired, false},
		{"upstream 503 without problem", 503, `upstream connect error`, http.Header{"Content-Type": {"text/plain"}}, output.CodeUpstream, output.ExitUnavailable, true},
		{"401 without problem", 401, ``, nil, output.CodeUpstream, output.ExitAuth, false},
		{"problem with 2xx", 200, problemBody(200, "X", ""), problemHeader, output.CodeProtocol, output.ExitUnclassified, false},
		{"HTML on 200", 200, `<html></html>`, http.Header{"Content-Type": {"text/html"}}, output.CodeProtocol, output.ExitUnclassified, false},
		{"malformed success", 200, `{"status":"success","contact":null}`, nil, output.CodeProtocol, output.ExitUnclassified, false},
		{"wrong payload shape", 200, `{"status":"success","contact":{"id":7}}`, nil, output.CodeProtocol, output.ExitUnclassified, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, &crmFake{status: tc.status, body: tc.body, header: tc.header})
			_, err := c.GetContact(t.Context(), GetContactParams{ID: contactID})
			e := cliError(t, err)
			if e.Code != tc.code || e.Exit != tc.exit || e.Retryable != tc.retryable || e.HTTPStatus != tc.status {
				t.Fatalf("got code %s exit %d retryable %v status %d", e.Code, e.Exit, e.Retryable, e.HTTPStatus)
			}
			if e.CorrelationID == "" {
				t.Error("correlation id lost")
			}
		})
	}
}

// The problem's details, its typed accessors and its raw document reach
// the CLI error.
func TestErrorDetailsKept(t *testing.T) {
	body := `{"type":"https://docs.gatherdeal.ai/errors/DELEGATED_ACCESS_INACTIVE","title":"Akses tidak aktif.","status":401,` +
		`"code":"DELEGATED_ACCESS_INACTIVE","retryable":false,"correlation_id":"corr-body","details":{"reason":"idle"}}`
	c := newClient(t, &crmFake{status: 401, body: body, header: problemHeader})
	_, err := c.ListContacts(t.Context(), ListContactsParams{})
	e := cliError(t, err)
	raw, ok := e.Details.(json.RawMessage)
	if !ok || string(raw) != `{"reason":"idle"}` || e.CorrelationID != "corr-body" || e.Message != "Akses tidak aktif." {
		t.Fatalf("details %v correlation %s message %q", e.Details, e.CorrelationID, e.Message)
	}
	if string(e.Problem) != body {
		t.Fatalf("problem %s", e.Problem)
	}
	if d, ok := crmv1.AsDelegatedAccessInactiveDetails(err); !ok || d.Reason != crmv1.DelegatedAccessInactiveReasonIdle {
		t.Fatalf("typed details %+v %v", d, ok)
	}
}

// The adapter over the generated client passes the shared decode-contract
// fixtures of the bundle, with the same classification as httpx.DecodeAPI.
func TestConformance(t *testing.T) {
	for _, fc := range conformance.Load(t) {
		t.Run(fc.Name, func(t *testing.T) {
			base := "https://api.example.com"
			hc := httpx.New(httpx.Options{Origin: base, Base: roundTripper(func(r *http.Request) (*http.Response, error) {
				return fc.HTTPResponse(r), nil
			})})
			c, err := New(hc, base+"/crm/v1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.GetContact(t.Context(), GetContactParams{ID: contactID})
			if fc.Expect.Kind == conformance.KindSuccess {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := cliError(t, err)
			if e.Exit != fc.WantExit() || e.HTTPStatus != fc.Expect.Status {
				t.Fatalf("exit %d status %d, want %d %d (%s)", e.Exit, e.HTTPStatus, fc.WantExit(), fc.Expect.Status, fc.Description)
			}
			_, wantErr := httpx.DecodeAPI(fc.Response.Status, fc.Header(), []byte(fc.Response.Body))
			want := cliError(t, wantErr)
			if e.Code != want.Code || e.Retryable != want.Retryable || e.Message != want.Message ||
				e.CorrelationID != want.CorrelationID || string(e.Problem) != string(want.Problem) {
				t.Fatalf("adapter %+v\nhttpx   %+v", e, want)
			}
		})
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedirectRefused(t *testing.T) {
	c := newClient(t, &crmFake{status: http.StatusFound, header: http.Header{"Location": {"https://evil.example.com/"}}})
	_, err := c.GetContact(t.Context(), GetContactParams{ID: contactID})
	if e := cliError(t, err); e.Code != output.CodeRedirect || e.Exit != output.ExitUnclassified {
		t.Fatalf("got %s exit %d", e.Code, e.Exit)
	}
}

func TestNetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c, err := New(httpx.New(httpx.Options{Origin: base}), base+"/crm/v1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListContacts(context.Background(), ListContactsParams{})
	if e := cliError(t, err); e.Code != output.CodeNetwork || e.Exit != output.ExitUnavailable || !e.Retryable {
		t.Fatalf("got %s exit %d", e.Code, e.Exit)
	}
}

func TestNewChecksAdvertisedVersion(t *testing.T) {
	hc := httpx.New(httpx.Options{Origin: "https://api.example.com"})
	if _, err := New(hc, "https://api.example.com/crm/v1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"https://api.example.com/crm/v2", "https://api.example.com/crm", "/v1"} {
		_, err := New(hc, bad)
		if e := cliError(t, err); e.Code != output.CodeDiscoveryInvalid {
			t.Errorf("%s: got %s", bad, e.Code)
		}
	}
}

func TestTagsFilter(t *testing.T) {
	if f, err := TagsFilter(nil, nil); err != nil || f != nil {
		t.Fatalf("empty: %v %v", f, err)
	}
	f, err := TagsFilter([]string{"66666666-6666-6666-6666-666666666666"}, []string{"VIP"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"groups":[{"rules":[{"field":"id","op":"IN","value":["66666666-6666-6666-6666-666666666666"]},{"field":"name","op":"IN","value":["VIP"]}],"conjunction":"OR","edge":"tags","quantifier":"SOME"}],"conjunction":"AND"}`
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
	// The generated client sends it as unpadded base64url JSON.
	fake := &crmFake{body: `{"status":"success","contacts":{"data":[]}}`}
	if _, err := newClient(t, fake).ListContacts(t.Context(), ListContactsParams{TagIDs: []string{"66666666-6666-6666-6666-666666666666"}, TagNames: []string{"VIP"}}); err != nil {
		t.Fatal(err)
	}
	sent, err := base64.RawURLEncoding.DecodeString(fake.query.Get("filter"))
	if err != nil || string(sent) != want {
		t.Fatalf("sent %s (%v)", sent, err)
	}
}

func TestExpansionField(t *testing.T) {
	for param, want := range map[string]string{"with_phones": "WithPhones", "with_tags": "WithTags"} {
		if got := ExpansionField(param); got != want {
			t.Errorf("%s: %s", param, got)
		}
	}
}

// Every expansion the manifest allows resolves to a bool field of the
// operation's generated input; anything else is refused.
func TestManifestExpansionsResolve(t *testing.T) {
	inputs := map[string]func() any{
		ListOperation: func() any { return &crmv1.FetchContactsInput{} },
		GetOperation:  func() any { return &crmv1.FetchContactByIDInput{} },
	}
	for id, newInput := range inputs {
		op := contracts.MustOperation(id)
		if len(op.DelegatedExpansions) == 0 {
			t.Fatalf("%s declares no expansions", id)
		}
		for _, param := range op.DelegatedExpansions {
			if err := SetExpansions(newInput(), Expansions{param}); err != nil {
				t.Errorf("%s: %v", id, err)
			}
		}
		for _, bad := range []string{"with_nothing", "page", "Phones"} {
			if err := SetExpansions(newInput(), Expansions{bad}); err == nil || !strings.Contains(err.Error(), "no expansion") {
				t.Errorf("%s: %q accepted (%v)", id, bad, err)
			}
		}
	}
	in := &crmv1.FetchContactsInput{}
	if err := SetExpansions(in, Expansions{"with_phones", "with_tags"}); err != nil || !in.WithPhones || !in.WithTags || in.WithEmails {
		t.Fatalf("%+v %v", in, err)
	}
}

// The generated client and httpx must agree on the correlation header and
// the response size bound.
func TestGeneratedRuntimeMatchesHTTPX(t *testing.T) {
	if crmv1.CorrelationHeader != httpx.CorrelationHeader || crmv1.MaxResponseBytes != httpx.MaxBodyBytes {
		t.Fatalf("generated %q/%d, httpx %q/%d", crmv1.CorrelationHeader, crmv1.MaxResponseBytes, httpx.CorrelationHeader, httpx.MaxBodyBytes)
	}
}

func TestListAllContactsResume(t *testing.T) {
	// The server honors count, including a smaller final request.
	var counts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("after"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		counts = append(counts, count)
		end := min(start+count, 7)
		var ids []string
		for i := start; i < end; i++ {
			ids = append(ids, strconv.Itoa(i))
		}
		next := ""
		if end < 7 {
			next = strconv.Itoa(end)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page(ids, next)))
	}))
	defer srv.Close()
	c, err := New(srv.Client(), srv.URL+"/crm/v1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.ListAllContacts(t.Context(), ListContactsParams{PageSize: 3}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.NextCursor != "4" || len(counts) != 2 || counts[0] != 3 || counts[1] != 1 {
		t.Fatalf("first %+v, counts %v", first, counts)
	}
	second, err := c.ListAllContacts(t.Context(), ListContactsParams{PageSize: 3, After: first.NextCursor}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if second.Truncated || second.NextCursor != "" {
		t.Fatalf("second %+v", second)
	}
	all := append(first.Contacts, second.Contacts...)
	if len(all) != 7 {
		t.Fatalf("contacts %v", all)
	}
	for i, contact := range all {
		if contact.ID != strconv.Itoa(i) {
			t.Fatalf("item %d: %s", i, contact.ID)
		}
	}
}

func TestListAllContactsOverfilledPage(t *testing.T) {
	for _, next := range []string{"", "next"} {
		c, queries := pagedFake(t, map[string]string{"": page([]string{"a", "b"}, next)})
		res, err := c.ListAllContacts(t.Context(), ListContactsParams{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Truncated || res.NextCursor != "" || len(res.Contacts) != 1 || res.Contacts[0].ID != "a" || (*queries)[0].Get("count") != "1" {
			t.Fatalf("overfilled: %+v queries %v", res, queries)
		}
	}
}

func TestListAllContactsEmptyPageBudget(t *testing.T) {
	pages := map[string]string{}
	cursor := ""
	for i := range 10 {
		next := strconv.Itoa(i + 1)
		pages[cursor] = page(nil, next)
		cursor = next
	}
	c, queries := pagedFake(t, pages)
	res, err := c.ListAllContacts(t.Context(), ListContactsParams{}, 1)
	if e := cliError(t, err); e.Code != output.CodeProtocol || !strings.Contains(e.Message, "page request budget") || e.Retryable {
		t.Fatalf("error %+v", e)
	}
	if res != nil || len(*queries) != 9 {
		t.Fatalf("result %+v, requests %d", res, len(*queries))
	}
}
