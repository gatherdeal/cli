package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// The child exits from the HTTP handler after receiving the mutation, before
// the CLI can print an envelope or run any post-dispatch cleanup.
func TestInterruptedMessageKey(t *testing.T) {
	const input = `{"agent_id":"55555555-5555-5555-5555-555555555555","to":"+60123456789","type":"text","text":{"body":"Hello"}}`
	args := []string{"whatsapp", "messages", "create", "--input", input, "--send"}
	token := server.TokenPrefix + strings.Repeat("k", 50)
	f := newFake(t, server.ProductionOrigin)
	if params := flag.Args(); len(params) == 2 && params[0] == "interrupt-message" {
		vars := map[string]string{"XDG_CONFIG_HOME": params[1], "APPDATA": params[1], EnvToken: token}
		f.whatsapp = func(_ http.ResponseWriter, r *http.Request) {
			fmt.Fprint(os.Stdout, r.Header.Get("Idempotency-Key"))
			os.Exit(23)
		}
		env := &Env{Stdout: os.Stdout, Stderr: os.Stderr, Getenv: func(k string) string { return vars[k] }, HTTPBase: f.transport()}
		t.Fatalf("write returned %d", Run(t.Context(), env, args))
	}
	te := newTestEnv(t, f)
	te.vars[EnvToken] = token
	var diagnostics bytes.Buffer
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInterruptedMessageKey$", "--", "interrupt-message", filepath.Dir(te.root))
	cmd.Stderr = &diagnostics
	firstKey, err := cmd.Output()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 {
		t.Fatalf("child: %v %s %s", err, firstKey, diagnostics.String())
	}
	file := regexp.MustCompile(`write-[0-9a-f]{32}\.json`).FindString(diagnostics.String())
	if file == "" {
		t.Fatalf("missing recovery file in %q", diagnostics.String())
	}
	raw, err := os.ReadFile(filepath.Join(te.root, file))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil || len(record) != 2 || record["schema_version"] != float64(1) ||
		len(firstKey) != 32 || record["idempotency_key"] != string(firstKey) {
		t.Fatalf("invalid recovery record after interruption")
	}
	calls := 0
	f.whatsapp = func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != string(firstKey) {
			t.Error("retry did not reuse the recovered key")
		}
		writeJSON(w, 200, `{"status":"success","message":{"id":"55555555-5555-5555-5555-555555555555"}}`)
	}
	res := te.run(append(args, "--idempotency-key", record["idempotency_key"].(string))...)
	files, err := filepath.Glob(filepath.Join(te.root, "write-*.json"))
	if res.exit != 0 || calls != 1 || err != nil || len(files) != 1 {
		t.Fatalf("explicit retry: %s; calls=%d records=%d", res.stdout, calls, len(files))
	}
}

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

func TestMessageCreateVariants(t *testing.T) {
	f, te := loggedIn(t)
	calls := 0
	f.whatsapp = func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeJSON(w, 200, `{"status":"success","message":{"id":"`+waAgentID+`"}}`)
	}
	const address = `"agent_id":"` + waAgentID + `","to":"+60 12-345 6789",`
	const reply = `"reply_to_id":"` + waAgentID + `",`
	const target = `"target_id":"` + waAgentID + `"`
	const text = `"type":"text","text":{"body":"hello"}`
	const template = `"type":"template","template":{"id":"` + waAgentID + `","params":{}}`
	const reaction = `"type":"reaction","reaction":{` + target + `,"emoji":""}`
	for _, tc := range []struct {
		members string
		valid   bool
	}{
		{address + text, true}, {address + template, true}, {reply + text, true}, {reply + template, true}, {reaction, true},
		{`"type":"edit","edit":{` + target + `,"body":"fixed"}`, true}, {`"type":"revoke","revoke":{` + target + `}`, true},
		{address + `"type":"text"`, false}, {address + `"type":"template","template":null`, false}, {text, false},
		{reply + reaction, false}, {`"strategy":"only_phone",` + reaction, false}, {`"type":"reaction","reaction":{}`, false},
		{`"type":"edit","edit":{` + target + `}`, false}, {`"type":"revoke","revoke":{"target_id":"bad"}`, false},
		{reply + `"strategy":"only_phone",` + text, false}, {address + text + `,"revoke":{}`, false},
	} {
		for _, mode := range []string{"--dry-run", "--send"} {
			before := calls
			r := te.run("whatsapp", "messages", "create", "--input", "{"+tc.members+"}", mode)
			wantExit, wantCalls := 2, before
			if tc.valid {
				wantExit = 0
				if mode == "--send" {
					wantCalls++
				}
			}
			if r.exit != wantExit || calls != wantCalls {
				t.Fatalf("%s %s: %s calls=%d", tc.members, mode, r.stdout, calls)
			}
		}
	}
}
