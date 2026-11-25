package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/output"
)

const outletRecord = `{"status":"success","outlet":{"id":"33333333-3333-3333-3333-333333333333","code":"HQ","name":"Head office","currency":"MYR","address":{"city":"Kuala Lumpur"}}}`

const outletsPage = `{"status":"success","outlets":{"data":[{"id":"33333333-3333-3333-3333-333333333333","code":"HQ","name":"Head office","currency":"MYR","address":{"city":"Kuala Lumpur"}}],"page_size":1,"page":1,"has_next_page":false,"has_prev_page":false}}`

func TestOutletsList(t *testing.T) {
	f, te := loggedIn(t)
	f.outlets = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for k, want := range map[string]string{"id": waOutletID, "code": "HQ", "name": "Head", "q": "office", "page": "1", "limit": "1", "with_owner": "true", "with_organization": "true", "is_master": "false", "sort": "-name"} {
			if q.Get(k) != want {
				t.Errorf("%s = %q want %q", k, q.Get(k), want)
			}
		}
		if r.Method != http.MethodGet || q.Has("outlet_id") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, outletsPage)
	}
	res := te.run("outlets", "list", "--id", waOutletID, "--code", "HQ", "--name", "Head", "--search", "office", "--page", "1", "--page-size", "1", "--with-owner", "--with-organization", "--is-master=false", "--sort", "-name")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	golden(t, "outlets_list.golden.json", res.stdout)
}
func TestOutletsGetByIDOrCode(t *testing.T) {
	for _, value := range []string{waOutletID, "HQ"} {
		t.Run(value, func(t *testing.T) {
			f, te := loggedIn(t)
			calls := 0
			f.outlets = func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if value == waOutletID {
					// A UUID is read with outlets.get.
					if r.URL.Path != "/identity/v1/outlets/"+waOutletID || q.Get("with_owner") != "true" || q.Has("outlet_id") {
						t.Errorf("request %s", r.URL)
					}
					writeJSON(w, 200, outletRecord)
					return
				}
				// A code is looked up with the list's exact code selector.
				if r.URL.Path != "/identity/v1/outlets" || q.Get("code") != value || q.Get("limit") != "1" || q.Get("total") != "none" || q.Get("with_owner") != "true" || q.Has("outlet_id") {
					t.Errorf("request %s", r.URL)
				}
				writeJSON(w, 200, outletsPage)
			}
			res := te.run("outlets", "get", value, "--with-owner")
			if res.exit != 0 || calls != 1 || res.data()["id"] != waOutletID {
				t.Fatal(res.stdout)
			}
			if value == "HQ" {
				golden(t, "outlets_get.golden.json", res.stdout)
			}
		})
	}
}
func TestOutletsNotFound(t *testing.T) {
	f, te := loggedIn(t)
	lookups, writes := 0, 0
	f.outlets = func(w http.ResponseWriter, r *http.Request) {
		lookups++
		if r.URL.Path != "/identity/v1/outlets" {
			writeJSON(w, 404, problemJSON(404, "IDENTITY_OUTLET_NOT_FOUND", "Outlet not found.", nil))
			return
		}
		writeJSON(w, 200, `{"status":"success","outlets":{"data":[],"has_next_page":false,"has_prev_page":false}}`)
	}
	f.whatsapp = func(w http.ResponseWriter, _ *http.Request) { writes++; writeJSON(w, 200, `{}`) }
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"outlets", "get", "hidden"}, output.CodeOutletNotFound},
		{[]string{"outlets", "get", waOutletID}, "IDENTITY_OUTLET_NOT_FOUND"},
		{[]string{"whatsapp", "agents", "create", "--outlet", "hidden", "--yes", "--no-wait"}, output.CodeOutletNotFound},
	} {
		res := te.run(tc.args...)
		if res.exit != 5 || res.errorCode() != tc.code {
			t.Fatal(res.stdout)
		}
	}
	if lookups != 3 || writes != 0 {
		t.Fatalf("lookups=%d writes=%d", lookups, writes)
	}
}
func TestWhatsAppCreateOutletCode(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "preview"}[dryRun], func(t *testing.T) {
			f, te := loggedIn(t)
			lookups, writes, polls := 0, 0, 0
			token := f.lastToken()
			f.outlets = func(w http.ResponseWriter, r *http.Request) {
				lookups++
				if r.URL.Query().Get("code") != "HQ" || r.URL.Query().Has("outlet_id") || r.Header.Get("Authorization") != "Bearer "+token {
					t.Errorf("lookup request %s", r.URL)
				}
				writeJSON(w, 200, outletsPage)
			}
			f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("outlet_id") != waOutletID || r.Header.Get("Authorization") != "Bearer "+token {
					t.Errorf("request %s", r.URL)
				}
				if r.Method == http.MethodGet {
					polls++
					writeJSON(w, 200, `{"status":"success","pairing":{"status":"connected"}}`)
					return
				}
				writes++
				var body struct {
					OutletID string `json:"outlet_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.OutletID != waOutletID || r.Header.Get("Idempotency-Key") != "code-create" {
					t.Errorf("body %+v", body)
				}
				writeJSON(w, 201, `{"status":"success","agent":{"id":"`+waAgentID+`"}}`)
			}
			args := []string{"whatsapp", "agents", "create", "--outlet", "HQ", "--idempotency-key", "code-create"}
			if dryRun {
				args = append(args, "--dry-run")
			} else {
				args = append(args, "--yes")
			}
			before := f.requests.Load()
			res := te.run(args...)
			if res.exit != 0 {
				t.Fatal(res.stdout)
			}
			context := res.env["meta"].(map[string]any)["context"].(map[string]any)
			request := context["request"].(map[string]any)
			if dryRun {
				if _, hasIDs := request["outlet_ids"]; hasIDs || request["outlet_code"] != "HQ" {
					t.Fatal(res.stdout)
				}
				if f.requests.Load() != before || res.data()["outlet_code"] != "HQ" || res.data()["outlet_id"] != nil {
					t.Fatal(res.stdout)
				}
				return
			} else if lookups != 1 || writes != 1 || polls != 1 {
				t.Fatalf("writes=%d polls=%d", writes, polls)
			}
			ids := request["outlet_ids"].([]any)
			if len(ids) != 1 || ids[0] != waOutletID || request["outlet_code"] != nil {
				t.Fatal(res.stdout)
			}
		})
	}
}

func TestWhatsAppOutletCodeAndInputMustAgree(t *testing.T) {
	for _, id := range []string{waOutletID, "66666666-6666-6666-6666-666666666666"} {
		t.Run(id, func(t *testing.T) {
			f, te := loggedIn(t)
			writes := 0
			f.outlets = func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, outletsPage) }
			f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeJSON(w, 200, `{"status":"success","pairing":`+waPending+`}`)
					return
				}
				writes++
				writeJSON(w, 201, `{"status":"success","agent":{"id":"`+waAgentID+`"}}`)
			}
			te.stdin = `{"outlet_id":"` + id + `"}`
			res := te.run("whatsapp", "agents", "create", "--outlet", "HQ", "--input", "-", "--yes", "--no-wait")
			wantExit, wantWrites := 9, 1
			if id != waOutletID {
				wantExit, wantWrites = 2, 0
			}
			if res.exit != wantExit || writes != wantWrites {
				t.Fatalf("writes=%d %s", writes, res.stdout)
			}
		})
	}
}

func TestRecordReadsPreserveNull(t *testing.T) {
	f, te := loggedIn(t)
	f.outlets = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/identity/v1/outlets" {
			writeJSON(w, 200, `{"status":"success","outlet":{"business_hours":null}}`)
			return
		}
		writeJSON(w, 200, `{"status":"success","outlets":{"data":[{"business_hours":null}]}}`)
	}
	for _, tc := range []struct {
		resource, field string
		prefix          []string
	}{
		{"outlets", "business_hours", []string{"outlets"}},
		{"agents", "outlet_id", []string{"whatsapp", "agents"}},
		{"messages", "text", []string{"whatsapp", "messages"}},
	} {
		f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
			record := `{"` + tc.field + `":null}`
			if strings.HasSuffix(r.URL.Path, "/"+waAgentID) {
				writeJSON(w, 200, `{"status":"success","`+strings.TrimSuffix(tc.resource, "s")+`":`+record+`}`)
				return
			}
			writeJSON(w, 200, `{"status":"success","`+tc.resource+`":{"data":[`+record+`]}}`)
		}
		for _, cmd := range [][]string{{"list"}, {"list", "--all"}, {"get", waAgentID}} {
			query := `has("` + tc.field + `")`
			if cmd[0] == "list" {
				query = ".[0] | " + query
			}
			args := append(append([]string{}, tc.prefix...), cmd...)
			r := te.run(append(args, "--fields", tc.field, "--jq", query)...)
			if r.exit != 0 || r.stdout != "true\n" {
				t.Fatal(args, r.stdout)
			}
		}
	}
}
