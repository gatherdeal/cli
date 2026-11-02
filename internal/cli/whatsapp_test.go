package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	wa "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/whatsapp"
)

const waAgentID = "55555555-5555-5555-5555-555555555555"
const waOutletID = "33333333-3333-3333-3333-333333333333"
const waPending = `{"status":"pending","pairing_code":"ABCDEFGH","expires_at":"2026-10-24T10:10:00Z"}`

func TestWhatsAppCreateNoWait(t *testing.T) {
	f, te := loggedIn(t)
	calls := 0
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/whatsapp/v1/agents" || r.Header.Get("Idempotency-Key") != "create-one" || r.URL.Query().Get("outlet_id") != waOutletID {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		var body struct {
			OutletID string `json:"outlet_id"`
			Phone    string `json:"phone"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.OutletID != waOutletID || body.Phone != "+60123456789" {
			t.Errorf("body %+v", body)
		}
		writeJSON(w, 200, `{"status":"success","agent":{"id":"`+waAgentID+`"},"pairing":`+waPending+`}`)
	}
	before := len(te.sleeps)
	res := te.run("whatsapp", "agents", "create", "--outlet", waOutletID, "--phone", "+60123456789", "--yes", "--no-wait", "--idempotency-key", "create-one")
	if res.exit != 9 || calls != 1 || len(te.sleeps) != before {
		t.Fatalf("calls=%d %s", calls, res.stdout)
	}
	data := res.data()
	if data["agent_id"] != waAgentID || data["pairing_code"] != "ABCD-EFGH" || data["pairing_url"] != "https://passport.gatherdeal.ai/whatsapp/pairing/"+waAgentID+"?org=acme" || data["expires_at"] != "2026-10-24T10:10:00Z" {
		t.Fatal(res.stdout)
	}
	action := data["action"].(map[string]any)
	if !strings.Contains(action["instruction"].(string), "Link with phone number instead") {
		t.Fatal(res.stdout)
	}
	golden(t, "whatsapp_create_no_wait.golden.json", res.stdout)
}
func TestWhatsAppPairingExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status string
		exit   int
	}{{"pending", 9}, {"connected", 0}, {"expired", 6}, {"failed", 6}} {
		t.Run(tc.status, func(t *testing.T) {
			f, te := loggedIn(t)
			calls := 0
			before := len(te.sleeps)
			f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/whatsapp/v1/agents/"+waAgentID+"/pairing" {
					t.Errorf("unexpected request %s", r.URL)
				}
				writeJSON(w, 200, `{"status":"success","pairing":`+strings.Replace(waPending, "pending", tc.status, 1)+`}`)
			}
			res := te.run("whatsapp", "agents", "pairing", waAgentID)
			if res.exit != tc.exit || calls != 1 || len(te.sleeps) != before {
				t.Fatalf("calls=%d %s", calls, res.stdout)
			}
			if tc.exit == 9 && res.data()["action"] == nil {
				t.Fatal(res.stdout)
			}
		})
	}
}
func TestWhatsAppWritePreflight(t *testing.T) {
	f, te := loggedIn(t)
	calls := 0
	f.whatsapp = func(w http.ResponseWriter, _ *http.Request) { calls++; writeJSON(w, 200, `{}`) }
	for _, tc := range []struct {
		args []string
		exit int
	}{
		{[]string{"--outlet", waOutletID, "--no-wait"}, 9},
		{[]string{"--outlet", waOutletID, "--phone", "60123456789", "--yes", "--no-wait"}, 2},
		{[]string{"--outlet", waOutletID, "--dry-run"}, 0},
	} {
		res := te.run(append([]string{"whatsapp", "agents", "create"}, tc.args...)...)
		if res.exit != tc.exit {
			t.Fatal(res.stdout)
		}
	}
	if calls != 0 {
		t.Fatalf("preflight made %d mutations", calls)
	}
	te.vars[EnvToken] = f.lastToken()
	res := te.run("whatsapp", "agents", "create", "--outlet", waOutletID, "--yes", "--no-wait")
	if res.exit != 9 || res.errorCode() != "CLI_PAIRING_CONTEXT_MISSING" || calls != 0 {
		t.Fatal(res.stdout)
	}
}

func TestWhatsAppCreateWaitIsBounded(t *testing.T) {
	f, te := loggedIn(t)
	calls := 0
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodPost {
			writeJSON(w, 200, `{"status":"success","agent":{"id":"`+waAgentID+`"},"pairing":`+waPending+`}`)
			return
		}
		writeJSON(w, 200, `{"status":"success","pairing":`+waPending+`}`)
	}
	before := te.now()
	res := te.run("whatsapp", "agents", "create", "--outlet", waOutletID, "--yes")
	if res.exit != 9 || calls != 151 || te.now().Sub(before) != 5*time.Minute {
		t.Fatalf("calls=%d elapsed=%s %s", calls, te.now().Sub(before), res.stdout)
	}
}

func TestWhatsAppReadCommands(t *testing.T) {
	for _, resource := range []string{"agents", "messages"} {
		t.Run(resource, func(t *testing.T) {
			f, te := loggedIn(t)
			f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method %s", r.Method)
				}
				if r.URL.Path == "/whatsapp/v1/"+resource {
					writeJSON(w, 200, `{"status":"success","`+resource+`":{"data":[],"has_next_page":false,"has_prev_page":false}}`)
					return
				}
				writeJSON(w, 200, `{"status":"success","`+strings.TrimSuffix(resource, "s")+`":{"id":"`+waAgentID+`"}}`)
			}
			for _, args := range [][]string{{"list"}, {"list", "--all", "--limit", "1"}, {"get", waAgentID}} {
				res := te.run(append([]string{"whatsapp", resource}, args...)...)
				if res.exit != 0 {
					t.Fatal(res.stdout)
				}
			}
		})
	}
}

func TestWhatsAppPairingRealTimer(t *testing.T) {
	now := time.Now()
	env := &Env{Stderr: io.Discard, Now: func() time.Time { return now }}
	var command *Command
	for _, c := range Commands() {
		if c.Operation == "whatsapp.agents.create" {
			command = c
		}
	}
	inv, err := parse(env, command, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv.target = &target{disc: server.Production()}
	inv.Meta.Context = &output.Context{Binding: &output.ContextBinding{Organization: output.ContextOrganization{Code: "acme"}}}
	calls := 0
	client, err := whatsapp.New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"success","pairing":{"status":"connected"}}`)), Request: r}, nil
	})}, server.ProductionOrigin+"/whatsapp/v1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := awaitPairing(t.Context(), inv, client, waAgentID, &wa.AgentPairing{Status: wa.AgentPairingStatusPending, ExpiresAt: now.Add(time.Millisecond)})
	if err != nil || calls != 1 || data.(pairingData).Status != wa.AgentPairingStatusConnected {
		t.Fatalf("calls=%d data=%+v err=%v", calls, data, err)
	}
}
