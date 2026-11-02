package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gatherdeal/cli/internal/output"
)

const outletsPage = `{"status":"success","outlets":{"data":[{"id":"33333333-3333-3333-3333-333333333333","code":"HQ","name":"Head office","currency":"MYR","address":{"city":"Kuala Lumpur"}}],"page_size":1,"page":1,"has_next_page":false,"has_prev_page":false}}`

func TestOutletsList(t *testing.T) {
	f, te := loggedIn(t)
	f.outlets = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for k, want := range map[string]string{"id": waOutletID, "code": "HQ", "name": "Head", "q": "office", "p": "1", "count": "1", "with_owner": "true", "with_organization": "true", "is_master": "false", "s": "name", "o": "asc"} {
			if q.Get(k) != want {
				t.Errorf("%s = %q want %q", k, q.Get(k), want)
			}
		}
		if r.Method != http.MethodGet || q.Has("outlet_id") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, outletsPage)
	}
	res := te.run("outlets", "list", "--id", waOutletID, "--code", "HQ", "--name", "Head", "--search", "office", "--page", "1", "--page-size", "1", "--with-owner", "--with-organization", "--is-master=false", "--sort", "name", "--order", "asc")
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
				key := "code"
				if value == waOutletID {
					key = "id"
				}
				if q.Get(key) != value || q.Get("count") != "1" || q.Get("total") != "none" || q.Has("outlet_id") {
					t.Errorf("query %s", q)
				}
				writeJSON(w, 200, outletsPage)
			}
			res := te.run("outlets", "get", value)
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
	f.outlets = func(w http.ResponseWriter, _ *http.Request) {
		lookups++
		writeJSON(w, 200, `{"status":"success","outlets":{"data":[],"has_next_page":false,"has_prev_page":false}}`)
	}
	f.whatsapp = func(w http.ResponseWriter, _ *http.Request) { writes++; writeJSON(w, 200, `{}`) }
	for _, args := range [][]string{{"outlets", "get", "hidden"}, {"outlets", "get", waOutletID}, {"whatsapp", "agents", "create", "--outlet", "hidden", "--yes", "--no-wait"}} {
		res := te.run(args...)
		if res.exit != 5 || res.errorCode() != output.CodeOutletNotFound {
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
				writeJSON(w, 200, `{"status":"success","agent":{"id":"`+waAgentID+`"},"pairing":`+waPending+`}`)
			}
			args := []string{"whatsapp", "agents", "create", "--outlet", "HQ", "--idempotency-key", "code-create"}
			if dryRun {
				args = append(args, "--dry-run")
			} else {
				args = append(args, "--yes")
			}
			res := te.run(args...)
			if res.exit != 0 || lookups != 1 {
				t.Fatal(res.stdout)
			}
			if dryRun {
				if writes != 0 || polls != 0 || res.data()["outlet_id"] != waOutletID {
					t.Fatal(res.stdout)
				}
			} else if writes != 1 || polls != 1 {
				t.Fatalf("writes=%d polls=%d", writes, polls)
			}
			context := res.env["meta"].(map[string]any)["context"].(map[string]any)
			ids := context["request"].(map[string]any)["outlet_ids"].([]any)
			if len(ids) != 1 || ids[0] != waOutletID {
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
			f.whatsapp = func(w http.ResponseWriter, _ *http.Request) {
				writes++
				writeJSON(w, 200, `{"status":"success","agent":{"id":"`+waAgentID+`"},"pairing":`+waPending+`}`)
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
