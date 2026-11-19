package cli

import (
	"encoding/base64"
	"encoding/json"
	"github.com/gatherdeal/cli/internal/crm"
	"net/http"
	"reflect"
	"testing"
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
	res := te.run("crm", "contact_tags", "list", "--search", "VIP", "--filter", `{"conjunction":"AND"}`, "--limit", "2", "--fields", "name", "--jq", ".[].name")
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
		if r.Method != http.MethodPost || r.URL.Path != "/whatsapp/v1/broadcasts/"+waAgentID+"/recipients" || r.Header.Get("Idempotency-Key") != "retry-1" {
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
	if r := te.run(append(args, "--yes", "--idempotency-key", "retry-1")...); r.exit != 0 || r.data()["added"] != float64(1) || calls != 1 {
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
