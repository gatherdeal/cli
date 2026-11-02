package cli

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func loggedIn(t *testing.T) (*fakeServer, *testEnv) {
	t.Helper()
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	return fake, te
}

const contactsPage = `{"status":"success","contacts":{"data":[{"id":"55555555-5555-5555-5555-555555555555","name":"Ali"}],"page":2,"page_size":1,"total_items":3,"total_pages":3,"has_next_page":true,"has_prev_page":true,"next_cursor":"cur-3","prev_cursor":"cur-1"}}`

func TestContactsList(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = contactsPage
	res := te.run("crm", "contacts", "list", "--page", "2", "--page-size", "1", "--search", "ali", "--with-phones", "--with-tags",
		"--tag", "VIP", "--tag-id", "66666666-6666-6666-6666-666666666666", "--outlet", "33333333-3333-3333-3333-333333333333",
		"--sort", "name", "--order", "asc")
	if res.exit != 0 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	q := fake.contactsQuery
	for k, want := range map[string]string{"p": "2", "count": "1", "q": "ali", "with_phones": "true", "with_tags": "true",
		"outlet_id": "33333333-3333-3333-3333-333333333333", "s": "name", "o": "asc"} {
		if q.Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), want)
		}
	}
	for _, k := range []string{"with_emails", "with_attributes", "with_referrer", "with_relationships", "with_addresses", "with_sales_persons"} {
		if q.Has(k) {
			t.Errorf("unexpected query %s", k)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Get("filter"))
	if err != nil {
		t.Fatalf("filter not base64url: %v", err)
	}
	var group map[string]any
	_ = json.Unmarshal(raw, &group)
	edge := group["groups"].([]any)[0].(map[string]any)
	if edge["edge"] != "tags" || edge["quantifier"] != "SOME" || len(edge["rules"].([]any)) != 2 {
		t.Fatalf("filter = %s", raw)
	}
	if fake.contactsAuth != "Bearer "+fake.lastToken() {
		t.Fatal("credential not attached to the CRM call")
	}
	golden(t, "contacts_list.golden.json", res.stdout)
}

func TestContactsListValidation(t *testing.T) {
	_, te := loggedIn(t)
	for _, args := range [][]string{
		{"crm", "contacts", "list", "--page", "0"},
		{"crm", "contacts", "list", "--page-size", "101"},
		{"crm", "contacts", "list", "--page-size", "0"},
		{"crm", "contacts", "list", "--page", "2", "--after", "c"},
		{"crm", "contacts", "list", "--after", ""},
		{"crm", "contacts", "list", "--total", "some"},
		{"crm", "contacts", "list", "--all", "--page", "2"},
		{"crm", "contacts", "list", "--all", "--total", "exact"},
		{"crm", "contacts", "list", "--limit", "0"},
		{"crm", "contacts", "list", "--limit", "10001"},
		{"crm", "contacts", "list", "--limit", "5", "--page", "2"},
		{"crm", "contacts", "list", "--limit", "5", "--total", "exact"},
		{"crm", "contacts", "list", "--outlet", "hq"},
		{"crm", "contacts", "list", "--tag-id", "x"},
		{"crm", "contacts", "list", "--order", "up"},
		{"crm", "contacts", "list", "--with-referrer"},
		{"crm", "contacts", "get"},
		{"crm", "contacts", "get", "not-a-uuid"},
	} {
		if res := te.run(args...); res.exit != 2 {
			t.Errorf("%v: exit %d", args, res.exit)
		}
	}
}

func TestContactsListCursor(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = `{"status":"success","contacts":{"data":[],"page_size":5,"has_next_page":false,"has_prev_page":true,"prev_cursor":"p"}}`
	res := te.run("crm", "contacts", "list", "--after", "cur-3", "--page-size", "5", "--total", "none")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	q := fake.contactsQuery
	if q.Get("after") != "cur-3" || q.Get("count") != "5" || q.Get("total") != "none" || q.Has("p") {
		t.Fatalf("query %s", q.Encode())
	}
	pi := res.env["meta"].(map[string]any)["page_info"].(map[string]any)
	want := map[string]any{"page_size": float64(5), "has_next_page": false, "has_prev_page": true, "prev_cursor": "p"}
	if len(pi) != len(want) {
		t.Fatalf("page_info %v", pi)
	}
	for k, v := range want {
		if pi[k] != v {
			t.Fatalf("page_info %v", pi)
		}
	}
	// No --page and no --page-size: the server defaults apply.
	if res := te.run("crm", "contacts", "list"); res.exit != 0 || fake.contactsQuery.Has("p") || fake.contactsQuery.Has("count") {
		t.Fatalf("defaults: %s %s", fake.contactsQuery.Encode(), res.stdout)
	}
}

func TestContactsListAll(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsPages = map[string]string{
		"":   `{"status":"success","contacts":{"data":[{"id":"a"},{"id":"b"}],"page_size":100,"has_next_page":true,"has_prev_page":false,"next_cursor":"c1"}}`,
		"c1": `{"status":"success","contacts":{"data":[{"id":"c"}],"page_size":100,"has_next_page":false,"has_prev_page":true}}`,
	}
	res := te.run("crm", "contacts", "list", "--all", "--search", "x")
	if res.exit != 0 || len(res.env["data"].([]any)) != 3 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	if q := fake.contactsQuery; q.Get("after") != "c1" || q.Get("total") != "none" || q.Get("count") != "100" || q.Get("q") != "x" {
		t.Fatalf("last query %s", q.Encode())
	}
	pi := res.env["meta"].(map[string]any)["page_info"].(map[string]any)
	if pi["has_next_page"] != false || len(pi) != 2 {
		t.Fatalf("page_info %v", pi)
	}
	res = te.run("crm", "contacts", "list", "--all", "--limit", "2")
	pi = res.env["meta"].(map[string]any)["page_info"].(map[string]any)
	if res.exit != 0 || len(res.env["data"].([]any)) != 2 || pi["has_next_page"] != true || pi["next_cursor"] != "c1" {
		t.Fatalf("capped: %s", res.stdout)
	}
	golden(t, "contacts_all_truncated.golden.json", res.stdout)
	res = te.run("crm", "contacts", "list", "--limit", "2", "--after", pi["next_cursor"].(string))
	if res.exit != 0 || len(res.env["data"].([]any)) != 1 || res.env["data"].([]any)[0].(map[string]any)["id"] != "c" {
		t.Fatalf("resumed: %s", res.stdout)
	}
	// --limit alone walks too, across pages, shrinking the last request.
	res = te.run("crm", "contacts", "list", "--limit", "3")
	pi = res.env["meta"].(map[string]any)["page_info"].(map[string]any)
	if res.exit != 0 || len(res.env["data"].([]any)) != 3 || pi["has_next_page"] != false || fake.contactsQuery.Get("count") != "1" || fake.contactsQuery.Get("after") != "c1" {
		t.Fatalf("limit 3: %s (last query %s)", res.stdout, fake.contactsQuery.Encode())
	}
}

func TestContactsGet(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = `{"status":"success","contact":{"id":"55555555-5555-5555-5555-555555555555","name":"Ali"}}`
	res := te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555", "--with-emails")
	if res.exit != 0 || res.data()["name"] != "Ali" || fake.contactsQuery.Get("with_emails") != "true" {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	// Flags may also come before the positional argument.
	if res := te.run("crm", "contacts", "get", "--with-tags", "55555555-5555-5555-5555-555555555555"); res.exit != 0 {
		t.Fatalf("flag-first: exit %d", res.exit)
	}
}

func TestContactsErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		exit   int
		code   string
	}{
		{"out of scope", 404, problemJSON(404, "CRM_CONTACT_NOT_FOUND", "Contact not found.", map[string]any{"contact_id": "55555555-5555-5555-5555-555555555555"}), 5, "CRM_CONTACT_NOT_FOUND"},
		{"expansion denied", 403, problemJSON(403, "DELEGATED_ACCESS_EXPANSION_NOT_ALLOWED", "x", nil), 4, "DELEGATED_ACCESS_EXPANSION_NOT_ALLOWED"},
		{"outlet denied", 403, problemJSON(403, "DELEGATED_ACCESS_OUTLET_NOT_ALLOWED", "x", nil), 4, "DELEGATED_ACCESS_OUTLET_NOT_ALLOWED"},
		{"permission denied", 403, problemJSON(403, "DELEGATED_ACCESS_PERMISSION_DENIED", "x", nil), 4, "DELEGATED_ACCESS_PERMISSION_DENIED"},
		{"access revoked", 401, problemJSON(401, "DELEGATED_ACCESS_INACTIVE", "x", map[string]any{"reason": "revoked"}), 3, "DELEGATED_ACCESS_INACTIVE"},
		{"filter denied", 403, problemJSON(403, "DELEGATED_ACCESS_FILTER_NOT_ALLOWED", "x", nil), 4, "DELEGATED_ACCESS_FILTER_NOT_ALLOWED"},
		{"plain 401", 401, problemJSON(401, "UNAUTHORIZED", "x", nil), 3, "UNAUTHORIZED"},
		{"cursor invalid", 400, problemJSON(400, "PAGINATION_CURSOR_INVALID", "x", map[string]any{"reason": "query_mismatch"}), 2, "PAGINATION_CURSOR_INVALID"},
		{"validation failed", 422, problemJSON(422, "VALIDATION_FAILED", "x", nil), 6, "VALIDATION_FAILED"},
		{"client too old", 426, problemJSON(426, "CLIENT_UPGRADE_REQUIRED", "x", map[string]any{"client": "gatherdeal-cli", "min_version": "9.0.0", "current_version": "0.1.0"}), 9, "CLIENT_UPGRADE_REQUIRED"},
		{"upstream 503", 503, `upstream connect error`, 8, "CLI_UPSTREAM_ERROR"},
		{"malformed success", 200, `{"status":"success","contact":null}`, 1, "CLI_PROTOCOL_ERROR"},
		{"payload of the wrong shape", 200, `{"status":"success","contact":{"id":7}}`, 1, "CLI_PROTOCOL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, te := loggedIn(t)
			fake.contactsStatus, fake.contactsBody = tc.status, tc.body
			res := te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555")
			if res.exit != tc.exit || res.errorCode() != tc.code {
				t.Fatalf("exit %d code %s, want %d %s", res.exit, res.errorCode(), tc.exit, tc.code)
			}
			e := res.env["error"].(map[string]any)
			if int(e["http_status"].(float64)) != tc.status {
				t.Fatalf("http_status = %v", e["http_status"])
			}
			switch tc.code {
			case "CRM_CONTACT_NOT_FOUND":
				golden(t, "contacts_get_not_found.golden.json", res.stdout)
			case "DELEGATED_ACCESS_INACTIVE":
				if e["message"] != "x" || !strings.Contains(res.env["hint"].(string), "was revoked") || !strings.Contains(res.env["hint"].(string), "auth login") {
					t.Fatalf("inactive not explained: %s", res.stdout)
				}
			case "CLIENT_UPGRADE_REQUIRED":
				if res.env["hint"] != "Run `gatherdeal update`." {
					t.Fatalf("hint: %s", res.stdout)
				}
			}
			if strings.HasPrefix(tc.body, "{") && tc.status >= 400 {
				// The JSON output carries the problem as received.
				if p, ok := e["problem"].(map[string]any); !ok || p["code"] != tc.code {
					t.Fatalf("problem not carried: %s", res.stdout)
				}
			}
		})
	}
}

// The CLI asks for problem text in --lang, else the POSIX locale, else
// English, and prints the server's localized title as received.
func TestContactsLanguage(t *testing.T) {
	cases := []struct {
		args []string
		vars map[string]string
		want string
	}{
		{nil, nil, "en"},
		{nil, map[string]string{"LANG": "ms_MY.UTF-8"}, "ms"},
		{nil, map[string]string{"LC_ALL": "zh_TW.UTF-8", "LANG": "ms_MY.UTF-8"}, "zh-Hant"},
		{[]string{"--lang", "zh-CN"}, map[string]string{"LANG": "ms_MY.UTF-8"}, "zh-Hans"},
		{[]string{"--lang", "fr, ms;q=0.9"}, nil, "ms"},
		{[]string{"--lang", "fr"}, nil, "en"},
	}
	for _, tc := range cases {
		fake, te := loggedIn(t)
		for k, v := range tc.vars {
			te.vars[k] = v
		}
		fake.contactsStatus, fake.contactsBody = 404, problemJSON(404, "CRM_CONTACT_NOT_FOUND", "Kenalan tidak dijumpai.", nil)
		args := append([]string{"crm", "contacts", "get", "55555555-5555-5555-5555-555555555555"}, tc.args...)
		res := te.run(args...)
		if fake.contactsLang != tc.want {
			t.Errorf("%v %v: Accept-Language %q, want %q", tc.args, tc.vars, fake.contactsLang, tc.want)
		}
		e := res.env["error"].(map[string]any)
		if res.exit != 5 || e["message"] != "Kenalan tidak dijumpai." || !strings.Contains(res.stderr, "error: Kenalan tidak dijumpai.") {
			t.Fatalf("%v: %s %s", tc.args, res.stdout, res.stderr)
		}
	}
	_, te := loggedIn(t)
	if res := te.run("crm", "contacts", "list", "--lang", "not a tag!"); res.exit != 2 || res.errorCode() != "CLI_USAGE" {
		t.Fatalf("invalid --lang: exit %d %s", res.exit, res.stdout)
	}
}

func TestContactsNeedLogin(t *testing.T) {
	te := newTestEnv(t, newFake(t, server.ProductionOrigin))
	res := te.run("crm", "contacts", "list")
	if res.exit != 3 || res.errorCode() != "CLI_NOT_LOGGED_IN" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	golden(t, "not_logged_in.golden.json", res.stdout)
}

func TestCommandsListing(t *testing.T) {
	te := newTestEnv(t, nil)
	res := te.run("commands")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	golden(t, "commands.golden.json", res.stdout)
	names := map[string]bool{}
	for _, c := range res.data()["commands"].([]any) {
		names[c.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"auth login", "auth status", "auth logout", "crm contacts list", "crm contacts get", "commands", "agent-context", "skills install", "skills show", "update", "version"} {
		if !names[want] {
			t.Errorf("commands lacks %q", want)
		}
	}
}

func TestAgentContext(t *testing.T) {
	te := newTestEnv(t, nil)
	res := te.run("agent-context")
	for _, want := range []string{"Never pass --server or --accept-server-risk", "Never print, log or paste a token", "read-only"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("agent-context lacks %q", want)
		}
	}
}

func TestVersionOffline(t *testing.T) {
	te := newTestEnv(t, nil) // no transport: any network call would fail
	res := te.run("version")
	if res.exit != 0 || res.data()["version"] == "" {
		t.Fatal(res.stdout)
	}
}

func TestUnknownCommand(t *testing.T) {
	te := newTestEnv(t, nil)
	if res := te.run("crm", "contacts", "delete"); res.exit != 2 || res.errorCode() != "CLI_USAGE" {
		t.Fatalf("exit %d", res.exit)
	}
}

// golden compares got with testdata/name; -update rewrites it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	var v any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(v, "", "  ")
	pretty = append(pretty, '\n')
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != string(pretty) {
		t.Fatalf("%s differs from golden:\n%s", name, pretty)
	}
}
