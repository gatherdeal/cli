package cli

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/crm"
)

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
			if q.Has("after") || q.Get("count") != "2" {
				t.Errorf("first page: %v", q)
			}
			writeJSON(w, 200, `{"status":"success","tags":{"data":[{"id":"1","name":"VIP"}],"has_next_page":true,"next_cursor":"c2"}}`)
			return
		}
		if q.Get("after") != "c2" || q.Get("count") != "1" {
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
	for _, name := range []string{"contacts update", "contact-tags assign", "contact-tags remove", "contact-logs list"} {
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
		{"assign tag", http.MethodPut, membership, "", []string{"contact-tags", "assign", id, tag}, true},
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
					writeJSON(w, 200, `{"status":"success","id":"`+id+`"}`)
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
