package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGeneratedTagList(t *testing.T) {
	f, te := loggedIn(t)
	token := f.lastToken()
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
	if res.exit != 0 || res.stdout != "VIP\nVIP2\n" || calls != 2 {
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
