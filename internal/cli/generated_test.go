package cli

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/crm"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
)

func TestGeneratedCommandSkipsNewExternalWrite(t *testing.T) {
	op := contracts.Operation{
		PublicID: "whatsapp.messages.forward",
		Method:   http.MethodPost,
		Effect:   contracts.Effect{Mutation: "additive", Interaction: "external"},
	}
	if got := generatedCommand(op, "Forward a message.", true, []bindingArgument(nil), nil, nil, nil); got != nil {
		t.Errorf("generated a command for new external write %s", op.PublicID)
	}
}

func TestGeneratedTagList(t *testing.T) {
	f, te := loggedIn(t)
	token := f.lastToken()
	previous := NewContacts
	t.Cleanup(func() { NewContacts = previous })
	clients := 0
	NewContacts = func(d ContactsDeps) (crm.Contacts, error) { clients++; return previous(d) }
	var calls int
	f.contactTags = func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token || q.Get("q") != "VIP" || q.Get("total") != "none" || q.Has("outlet_id") {
			t.Errorf("unexpected tag request %s %s", r.Method, r.URL)
		}
		filter, err := base64.RawURLEncoding.DecodeString(q.Get("filter"))
		if err != nil || !json.Valid(filter) {
			t.Errorf("invalid filter %s", filter)
		}
		w.Header().Set("X-Correlation-ID", "tags")
		if calls == 1 {
			// A logged-out profile loses its stored bearer between pages.
			// The in-flight walk must continue with its original client.
			te.keyring.mu.Lock()
			clear(te.keyring.items)
			te.keyring.mu.Unlock()
			if q.Has("after") || q.Get("limit") != "2" {
				t.Errorf("first page: %v", q)
			}
			writeJSON(w, 200, `{"status":"success","tags":{"data":[{"id":"1","name":"VIP"}],"has_next_page":true,"next_cursor":"c2"}}`)
			return
		}
		if q.Get("after") != "c2" || q.Get("limit") != "1" {
			t.Errorf("second page: %v", q)
		}
		writeJSON(w, 200, `{"status":"success","tags":{"data":[{"id":"2","name":"VIP2"}],"has_next_page":false}}`)
	}
	res := te.run("crm", "contact-tags", "list", "--search", "VIP", "--filter", `{"conjunction":"AND"}`, "--limit", "2", "--fields", "name", "--jq", ".[].name")
	if res.exit != 0 || res.stdout != "VIP\nVIP2\n" || calls != 2 || clients != 1 {
		t.Fatal(res.stdout, calls)
	}
}

func TestGeneratedBroadcastRecipientWrite(t *testing.T) {
	f, te := loggedIn(t)
	const body = `{"recipients":[{"to":"+60123456789"}]}`
	args := []string{"whatsapp", "broadcasts", "recipients", "add", waAgentID, "--input", body}
	calls := 0
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/whatsapp/v1/broadcasts/"+waAgentID+"/recipients" || r.Header.Get("Idempotency-Key") != "retry-01" {
			t.Errorf("unexpected recipient request: %s %s", r.Method, r.URL)
		}
		var in struct{ Recipients []struct{ To string } }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Recipients) != 1 || in.Recipients[0].To != "+60123456789" {
			t.Errorf("body: %+v %v", in, err)
		}
		writeJSON(w, 200, `{"status":"success","added":1,"duplicates":0}`)
	}
	if r := te.run(append(args, "--dry-run")...); r.exit != 0 || calls != 0 {
		t.Fatal(r.stdout)
	}
	if r := te.run(args...); r.exit != 9 || calls != 0 {
		t.Fatal(r.stdout)
	}
	if r := te.run("whatsapp", "broadcasts", "recipients", "add", waAgentID, "--input", `{"unexpected":true}`, "--yes"); r.exit != 2 || calls != 0 {
		t.Fatal(r.stdout)
	}
	if r := te.run(append(args, "--yes", "--idempotency-key", "retry-01")...); r.exit != 0 || r.data()["added"] != float64(1) || calls != 1 {
		t.Fatal(r.stdout)
	}
	if r := te.run("whatsapp", "broadcasts", "start", waAgentID, "--yes"); r.exit != 2 || calls != 1 {
		t.Fatal(r.stdout)
	}
	f.whatsapp = func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusBadGateway) }
	if r := te.run("whatsapp", "broadcasts", "start", waAgentID, "--send"); r.exit != 8 || r.errorCode() != "CLI_WRITE_OUTCOME_UNKNOWN" || calls != 2 {
		t.Fatal(r.stdout)
	}
}

func TestWhatsAppBodylessWriteFlags(t *testing.T) {
	_, te := loggedIn(t)
	for _, command := range []string{"agents delete", "broadcasts start"} {
		t.Run(command, func(t *testing.T) {
			args := append([]string{"whatsapp"}, strings.Fields(command)...)
			args = append(args, waAgentID, "--dry-run")
			if r := te.run(args...); r.exit != 0 {
				t.Fatal(r.stdout)
			}
			for name, value := range map[string]string{"input": "{}", "idempotency-key": "caller-key"} {
				r := te.run(append(args, "--"+name, value)...)
				if r.exit != 2 || !strings.Contains(r.stderr, "flag provided but not defined: -"+name) {
					t.Fatal(r.stdout)
				}
			}
		})
	}
}

func TestWhatsAppRemoveCommands(t *testing.T) {
	const recipientID = "55555555-5555-4555-8555-555555555555"
	for _, tc := range []struct {
		resource string
		path     string
		ids      []string
	}{
		{"broadcasts recipients", "/whatsapp/v1/broadcasts/" + waAgentID + "/recipients/" + recipientID, []string{waAgentID, recipientID}},
		{"broadcast-suppressions", "/whatsapp/v1/broadcast-suppressions/" + recipientID, []string{recipientID}},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			f, te := loggedIn(t)
			calls := 0
			f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodDelete || r.URL.Path != tc.path || r.ContentLength != 0 {
					t.Errorf("unexpected request: %s %s (body length %d)", r.Method, r.URL, r.ContentLength)
				}
				w.WriteHeader(http.StatusNoContent)
			}

			canonical := "whatsapp " + tc.resource + " remove"
			former := "whatsapp " + tc.resource + " delete"
			for _, name := range []string{canonical, former} {
				args := append(strings.Fields(name), tc.ids...)
				r := te.run(append(args, "--yes")...)
				if r.exit != 0 {
					t.Fatalf("%s: %s", name, r.stdout)
				}
				deprecation := r.env["meta"].(map[string]any)["deprecation"]
				if name == former {
					want := map[string]any{"successor": canonical, "since": "2026-11-26"}
					if !reflect.DeepEqual(deprecation, want) || !strings.Contains(r.stderr, "is deprecated") {
						t.Fatalf("%s: %s %s", name, r.stderr, r.stdout)
					}
				} else if deprecation != nil || r.stderr != "" {
					t.Fatalf("%s: %s %s", name, r.stderr, r.stdout)
				}
			}
			if calls != 2 {
				t.Fatalf("got %d route calls, want 2", calls)
			}
		})
	}
}

func TestGeneratedWriteBodyAndReceipt(t *testing.T) {
	f, te := loggedIn(t)
	calls := 0
	receipt := `{"status":"success"}`
	var wire json.RawMessage
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		writeJSON(w, 200, receipt)
	}
	for _, mode := range []string{"--dry-run", "--yes"} {
		for _, args := range [][]string{
			{"create", "--input", "{}"},
			{"update", waAgentID, "--input", `{"priority_offset":null}`},
			{"recipients", "add", waAgentID, "--input", `{"recipients":[{}]}`},
		} {
			r := te.run(append(append([]string{"whatsapp", "broadcasts"}, args...), mode)...)
			if r.exit != 2 || calls != 0 {
				t.Fatal(r.stdout, calls)
			}
		}
	}
	args := []string{"whatsapp", "broadcasts", "recipients", "add", waAgentID, "--input", `{"recipients":[{"to":"+60123456789","params":{}}]}`, "--idempotency-key", "original-key"}
	preview := te.run(append(args, "--dry-run")...)
	if preview.exit != 0 || calls != 0 {
		t.Fatal(preview.stdout)
	}
	for _, response := range []string{`{"status":"success"}`, `{"status":"success","added":0}`, `{"status":"success","added":null,"duplicates":0}`} {
		receipt = response
		r := te.run(append(args, "--yes")...)
		if r.exit != 8 || r.errorCode() != "CLI_WRITE_OUTCOME_UNKNOWN" || r.env["meta"].(map[string]any)["write"].(map[string]any)["idempotency_key"] != "original-key" {
			t.Fatal(r.stdout)
		}
	}
	receipt = `{"status":"success","added":0,"duplicates":0}`
	r := te.run(append(args, "--yes")...)
	var sent any
	if err := json.Unmarshal(wire, &sent); err != nil || !reflect.DeepEqual(sent, preview.data()["body"]) || r.exit != 0 || calls != 4 {
		t.Fatal(string(wire), preview.stdout, r.stdout)
	}
}

func TestGeneratedPreviewUsesWireBody(t *testing.T) {
	f, te := loggedIn(t)
	args := []string{"whatsapp", "broadcasts", "create", "--input", `{"name":"hello","priority_offset":0,"content":{"type":"text","text":{"body":"hello"},"template":null}}`}
	preview := te.run(append(args, "--dry-run")...)
	if preview.exit != 0 {
		t.Fatal(preview.stdout)
	}
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		var sent map[string]any
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil || !reflect.DeepEqual(sent, preview.data()["body"]) {
			t.Errorf("sent %v, preview %s", sent, preview.stdout)
		}
		if _, ok := sent["priority_offset"]; ok {
			t.Error("expected omitzero on the wire")
		}
		w.WriteHeader(http.StatusBadGateway)
	}
	if r := te.run(append(args, "--yes")...); r.exit != 8 {
		t.Fatal(r.stdout)
	}
}

func TestChatMessagesRevisionPoll(t *testing.T) {
	f, te := loggedIn(t)
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("agent_id") != waAgentID || q.Get("after_rev") != "7" || q.Get("limit") != "10" {
			t.Error(q)
		}
		writeJSON(w, 200, `{"status":"success","messages":[],"after_rev":9,"has_more":false}`)
	}
	args := []string{"whatsapp", "chat-messages", "list", "--agent-id", waAgentID}
	r := te.run(append(args, "--count", "10", "--after-rev", "7")...)
	if r.exit != 0 || r.data()["after_rev"] != float64(9) || r.data()["has_more"] != false || !reflect.DeepEqual(r.data()["messages"], []any{}) {
		t.Fatal(r.stdout)
	}
	for _, flag := range []string{"--limit", "--after", "--fields"} {
		if r := te.run(append(args, flag, "1")...); r.exit != 2 {
			t.Fatal(r.stdout)
		}
	}
}

func TestGeneratedCRMHelp(t *testing.T) {
	te := newTestEnv(t, nil)
	for _, name := range []string{"contacts update", "contact-tags add", "contact-tags assign", "contact-tags remove", "contact-logs list", "sales-pipelines list", "deals list", "deals get", "deals create", "deals move-stage", "deals update"} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"crm"}, strings.Fields(name)...)
			res := te.run(append(args, "--help")...)
			if res.exit != 0 {
				t.Fatal(res.stdout)
			}
			compareGolden(t, "help_crm_"+strings.ReplaceAll(name, " ", "_")+".golden.txt", res.stdout)
		})
	}
}

func TestGeneratedCRMBoardReads(t *testing.T) {
	const pipeline = "01234567-89ab-4cde-8fab-0123456789ab"
	const stage = "55555555-5555-4555-8555-555555555555"
	const deal = "44444444-4444-4444-8444-444444444444"
	const outlet = "33333333-3333-3333-3333-333333333333"
	for _, tc := range []struct {
		name     string
		args     []string
		path     string
		query    string
		response string
		want     string
	}{
		{
			"pipeline stages", []string{"sales-pipelines", "list", "--with-stages", "--search", "retail", "--jq", ".[].stages[].name"},
			"/crm/v1/sales-pipelines", "q=retail&with_stages=true",
			`{"status":"success","sales_pipelines":{"data":[{"name":"Retail","stages":[{"name":"Lead"}]}],"page_size":20}}`, "Lead\n",
		},
		{
			"no pipeline expansion", []string{"sales-pipelines", "list", "--with-stages=false", "--jq", ".[].name"},
			"/crm/v1/sales-pipelines", "",
			`{"status":"success","sales_pipelines":{"data":[{"name":"Retail"}],"page_size":20}}`, "Retail\n",
		},
		{
			"deal list", []string{"deals", "list", "--pipeline-id", pipeline, "--stage-id", stage, "--search", "retail", "--outlet", outlet, "--jq", ".[].name"},
			"/crm/v1/deals", "outlet_id=" + outlet + "&pipeline_id=" + pipeline + "&q=retail&stage_id=" + stage,
			`{"status":"success","deals":{"data":[{"name":"Renewal"}],"page_size":20}}`, "Renewal\n",
		},
		{
			"deal detail", []string{"deals", "get", deal, "--jq", ".name"},
			"/crm/v1/deals/" + deal, "", `{"status":"success","deal":{"name":"Renewal"}}`, "Renewal\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			f.crm = func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("unexpected CRM request: %s %s", r.Method, r.URL)
				}
				writeJSON(w, http.StatusOK, tc.response)
			}
			if r := te.run(append([]string{"crm"}, tc.args...)...); r.exit != 0 || r.stdout != tc.want {
				t.Fatal(r.stdout, r.stderr)
			}
		})
	}
}

func TestGeneratedCRMPipelineStageLimit(t *testing.T) {
	f, te := loggedIn(t)
	const code = "CRM_SALES_PIPELINE_STAGE_LIMIT"
	f.crm = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("with_stages") != "true" {
			t.Errorf("unexpected CRM request: %s %s", r.Method, r.URL)
		}
		writeJSON(w, http.StatusUnprocessableEntity, problemJSON(http.StatusUnprocessableEntity, code,
			"Pipeline stage expansion exceeds its limit.", map[string]any{"expansion": "with_stages", "limit": 50}))
	}
	r := te.run("crm", "sales-pipelines", "list", "--with-stages")
	if r.exit != 6 || r.errorCode() != code || !strings.Contains(r.stdout, `"details":{"expansion":"with_stages","limit":50}`) {
		t.Fatal(r.stdout, r.stderr)
	}
}

func TestGeneratedCRMContactLogs(t *testing.T) {
	f, te := loggedIn(t)
	const id = "01234567-89ab-4cde-8fab-0123456789ab"
	const outlet = "33333333-3333-3333-3333-333333333333"
	calls := 0
	f.crm = func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/crm/v1/contact-logs" ||
			q.Get("contact_id") != id || q.Get("outlet_id") != outlet {
			t.Errorf("unexpected log request: %s %s", r.Method, r.URL)
		}
		writeJSON(w, 200, `{"status":"success","contact_logs":{"data":[{"content":"Follow-up"}],"has_next_page":false}}`)
	}
	args := []string{"crm", "contact-logs", "list"}
	missing := te.run(args...)
	if missing.exit != 2 || missing.errorCode() != "CLI_USAGE" || !strings.Contains(missing.stderr, "flag --contact-id is required") || calls != 0 {
		t.Fatal(missing.stdout, missing.stderr, calls)
	}
	golden(t, "contact_logs_missing_contact_id.golden.json", missing.stdout)
	if r := te.run(append(args, "--contact-id", "invalid")...); r.exit != 2 || calls != 0 {
		t.Fatal(r.stdout, calls)
	}
	r := te.run(append(args, "--contact-id", id, "--outlet", outlet, "--jq", ".[].content")...)
	if r.exit != 0 || calls != 1 || r.stdout != "Follow-up\n" {
		t.Fatal(r.stdout, calls)
	}
}

func TestGeneratedCRMWrites(t *testing.T) {
	const id = "01234567-89ab-4cde-8fab-0123456789ab"
	const tag = "55555555-5555-4555-8555-555555555555"
	const outlet = "33333333-3333-3333-3333-333333333333"
	const contacts = "/crm/v1/contacts"
	const logs = "/crm/v1/contact-logs"
	const membership = contacts + "/" + id + "/tags/" + tag
	for _, tc := range []struct {
		name, method, path, body string
		args                     []string
		keyed                    bool
	}{
		{"create contact", http.MethodPost, contacts, `{"kind":"personal","name":"Amina"}`, []string{"contacts", "create"}, true},
		{"update contact", http.MethodPatch, contacts + "/" + id, `{"remark":null}`, []string{"contacts", "update", id}, false},
		{"add tag", http.MethodPut, membership, "", []string{"contact-tags", "add", id, tag}, false},
		{"assign tag alias", http.MethodPut, membership, "", []string{"contact-tags", "assign", id, tag}, false},
		{"remove tag", http.MethodDelete, membership, "", []string{"contact-tags", "remove", id, tag}, false},
		{"create log", http.MethodPost, logs, `{"contact_id":"` + id + `","content":" Follow-up "}`, []string{"contact-logs", "create"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			var keys []string
			f.crm = func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.Query().Get("outlet_id") != outlet {
					t.Errorf("unexpected write request: %s %s", r.Method, r.URL)
				}
				var sent, want map[string]any
				err := json.NewDecoder(r.Body).Decode(&sent)
				if tc.body == "" {
					if err != io.EOF || r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
						t.Errorf("addressed request has a body: %v, %v", sent, err)
					}
				} else if wantErr := json.Unmarshal([]byte(tc.body), &want); wantErr != nil || err != nil || !reflect.DeepEqual(sent, want) {
					t.Errorf("body: %v, want %v, decode errors: %v, %v", sent, want, err, wantErr)
				}
				switch {
				case !tc.keyed:
					w.WriteHeader(http.StatusNoContent)
				case len(keys) == 1:
					w.WriteHeader(http.StatusBadGateway)
				default:
					writeJSON(w, http.StatusCreated, `{"status":"success","id":"`+id+`"}`)
				}
			}
			args := append([]string{"crm"}, tc.args...)
			args = append(args, "--outlet", outlet)
			if tc.body != "" {
				args = append(args, "--input", tc.body)
			}
			if r := te.run(args...); r.exit != 9 || len(keys) != 0 {
				t.Fatal(r.stdout, keys)
			}
			if r := te.run(append(args, "--dry-run")...); r.exit != 0 || len(keys) != 0 {
				t.Fatal(r.stdout, keys)
			}
			if tc.body == "" {
				if r := te.run(append(args, "--dry-run", "--input", "{}")...); r.exit != 2 || !strings.Contains(r.stderr, "flag provided but not defined: -input") || len(keys) != 0 {
					t.Fatal(r.stdout, keys)
				}
			}
			if !tc.keyed {
				if r := te.run(append(args, "--yes", "--idempotency-key", "caller-key")...); r.exit != 2 || !strings.Contains(r.stderr, "flag provided but not defined: -idempotency-key") || len(keys) != 0 {
					t.Fatal(r.stdout, keys)
				}
				if r := te.run(append(args, "--yes")...); r.exit != 0 || !reflect.DeepEqual(keys, []string{""}) {
					t.Fatal(r.stdout, keys)
				}
				return
			}
			first := te.run(append(args, "--yes")...)
			if first.exit != 8 || first.errorCode() != "CLI_WRITE_OUTCOME_UNKNOWN" || len(keys) != 1 || keys[0] == "" {
				t.Fatal(first.stdout, keys)
			}
			meta := first.env["meta"].(map[string]any)["write"].(map[string]any)
			if meta["idempotency_key"] != keys[0] || meta["idempotency_key_generated"] != true || meta["invocation_id"] == nil {
				t.Fatal(first.stdout)
			}
			res := te.run(append(args, "--yes", "--idempotency-key", keys[0])...)
			if res.exit != 0 || res.data()["id"] != id || len(keys) != 2 || keys[1] != keys[0] {
				t.Fatal(res.stdout, keys)
			}
			res = te.run(append(args, "--yes")...)
			if res.exit != 0 || len(keys) != 3 || keys[2] == "" || keys[2] == keys[0] {
				t.Fatal(res.stdout, keys)
			}
		})
	}
}

func TestGeneratedCRMDealPreconditions(t *testing.T) {
	const deal = "44444444-4444-4444-8444-444444444444"
	const stage = "55555555-5555-4555-8555-555555555555"
	const revision = `"r1:AbC-9"`
	// The deprecated commands send their input through deals update.
	for _, tc := range []struct {
		name, body string
		args       []string
		deprecated bool
	}{
		{"update stage", `{"stage_id":"` + stage + `"}`, []string{"deals", "update", deal}, false},
		{"update fields", `{"priority":"high","remark":null}`, []string{"deals", "update", deal}, false},
		{"move stage", `{"stage_id":"` + stage + `"}`, []string{"deals", "move-stage", deal}, true},
		{"deal fields", `{"priority":"high","remark":null}`, []string{"deal-fields", "update", deal}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			var tags []string
			status := http.StatusOK
			f.crm = func(w http.ResponseWriter, r *http.Request) {
				tags = append(tags, strings.Join(r.Header.Values("If-Match"), "|"))
				var sent, want map[string]any
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil || json.Unmarshal([]byte(tc.body), &want) != nil || !reflect.DeepEqual(sent, want) {
					t.Errorf("body %v, want %s: %v", sent, tc.body, err)
				}
				if r.Method != http.MethodPatch || r.URL.Path != "/crm/v1/deals/"+deal || r.Header.Get("Idempotency-Key") != "" {
					t.Errorf("unexpected deal write: %s %s", r.Method, r.URL)
				}
				if status != http.StatusOK {
					writeJSON(w, status, problemJSON(status, "PRECONDITION_FAILED", "The deal changed.", nil))
					return
				}
				writeJSON(w, http.StatusOK, `{"status":"success","revision":"\"r2\""}`)
			}
			args := append([]string{"crm"}, tc.args...)
			args = append(args, "--input", tc.body)
			for _, bad := range [][]string{
				{"--yes"}, {"--dry-run"}, {"--yes", "--if-match", ""},
				{"--yes", "--if-match", "r1"}, {"--yes", "--if-match", `W/"r1"`}, {"--yes", "--if-match", "*"},
				{"--yes", "--if-match", revision, "--idempotency-key", "caller-key-1"},
			} {
				if r := te.run(append(args, bad...)...); r.exit != 2 || r.errorCode() != "CLI_USAGE" || len(tags) != 0 {
					t.Fatal(bad, r.stdout, r.stderr)
				}
			}
			args = append(args, "--if-match", revision)
			if r := te.run(args...); r.exit != 9 || r.errorCode() != "CLI_WRITE_CONFIRMATION_REQUIRED" || len(tags) != 0 {
				t.Fatal(r.stdout)
			}
			preview := te.run(append(args, "--dry-run")...)
			if preview.exit != 0 || preview.data()["if_match"] != revision || len(tags) != 0 {
				t.Fatal(preview.stdout)
			}
			r := te.run(append(args, "--yes")...)
			if r.exit != 0 || r.data()["revision"] != `"r2"` || !reflect.DeepEqual(tags, []string{revision}) {
				t.Fatal(r.stdout, tags)
			}
			deprecation, _ := r.env["meta"].(map[string]any)["deprecation"].(map[string]any)
			warned := strings.Contains(r.stderr, "is deprecated since 2026-11-25") && strings.Contains(r.stderr, "use `gatherdeal crm deals update`")
			if tc.deprecated != (deprecation != nil) || tc.deprecated != warned ||
				(tc.deprecated && (deprecation["successor"] != "crm deals update" || deprecation["since"] != "2026-11-25")) {
				t.Fatal(r.stdout, r.stderr)
			}
			status = http.StatusPreconditionFailed
			r = te.run(append(args, "--yes")...)
			if r.exit != 6 || r.errorCode() != "PRECONDITION_FAILED" || len(tags) != 2 || tags[1] != revision {
				t.Fatal(r.stdout, tags)
			}
		})
	}
}

func TestGeneratedCRMDealCreate(t *testing.T) {
	const deal = "44444444-4444-4444-8444-444444444444"
	const outlet = "33333333-3333-3333-3333-333333333333"
	const body = `{"pipeline_id":"01234567-89ab-4cde-8fab-0123456789ab","stage_id":"55555555-5555-4555-8555-555555555555","priority":"low"}`
	f, te := loggedIn(t)
	var keys []string
	conflict := false
	f.crm = func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if r.Method != http.MethodPost || r.URL.Path != "/crm/v1/deals" || r.URL.Query().Get("outlet_id") != outlet || r.Header.Get("If-Match") != "" {
			t.Errorf("unexpected deal create: %s %s", r.Method, r.URL)
		}
		if conflict {
			writeJSON(w, http.StatusConflict, problemJSON(http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "The key was used for other input.", nil))
			return
		}
		writeJSON(w, http.StatusCreated, `{"status":"success","id":"`+deal+`"}`)
	}
	args := []string{"crm", "deals", "create", "--outlet", outlet, "--input", body}
	if r := te.run(args...); r.exit != 9 || len(keys) != 0 {
		t.Fatal(r.stdout)
	}
	if r := te.run(append(args, "--yes", "--if-match", `"r1"`)...); r.exit != 2 || !strings.Contains(r.stderr, "flag provided but not defined: -if-match") || len(keys) != 0 {
		t.Fatal(r.stdout, r.stderr)
	}
	if r := te.run(append(args, "--dry-run")...); r.exit != 0 || len(keys) != 0 {
		t.Fatal(r.stdout)
	}
	r := te.run(append(args, "--yes")...)
	meta := r.env["meta"].(map[string]any)["write"].(map[string]any)
	if r.exit != 0 || r.data()["id"] != deal || len(keys) != 1 || keys[0] == "" || meta["idempotency_key"] != keys[0] || meta["idempotency_key_generated"] != true {
		t.Fatal(r.stdout, keys)
	}
	conflict = true
	r = te.run(append(args, "--yes", "--idempotency-key", keys[0])...)
	meta = r.env["meta"].(map[string]any)["write"].(map[string]any)
	if r.exit != 6 || r.errorCode() != "IDEMPOTENCY_KEY_CONFLICT" || len(keys) != 2 || keys[1] != keys[0] || meta["idempotency_key"] != keys[0] || meta["idempotency_key_generated"] != nil {
		t.Fatal(r.stdout, keys)
	}
}

// A deprecated CRM command sends its body through the successor, so every
// body member of the deprecated operation must be a successor member with
// the same schema.
func TestDeprecatedCRMBodiesFitSuccessor(t *testing.T) {
	m, err := contracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	data, err := m.Bundles[0].OpenAPI()
	if err != nil || m.Bundles[0].Service != "crm" {
		t.Fatal("crm bundle", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			ID          string `json:"x-gd-public-id"`
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					}
				}
			}
		}
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	body := map[string]map[string]json.RawMessage{}
	for _, path := range doc.Paths {
		for _, op := range path {
			ref := strings.TrimPrefix(op.RequestBody.Content["application/json"].Schema.Ref, "#/components/schemas/")
			body[op.ID] = doc.Components.Schemas[ref].Properties
		}
	}
	checked := 0
	for _, b := range crmv1.CLIBindings() {
		d := contracts.MustOperation(b.PublicID).Deprecation
		if d == nil {
			continue
		}
		checked++
		crmSuccessor(b, d.Successor) // panics on different arguments
		if len(body[b.PublicID]) == 0 {
			t.Errorf("%s has no body members", b.PublicID)
		}
		for name, schema := range body[b.PublicID] {
			if got, ok := body[d.Successor][name]; !ok || string(got) != string(schema) {
				t.Errorf("%s member %s is not a %s member with the same schema", b.PublicID, name, d.Successor)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no deprecated CRM operation checked")
	}
}
