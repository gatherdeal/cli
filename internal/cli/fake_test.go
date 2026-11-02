package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// fakeServer imitates the identity and CRM endpoints the CLI uses.
type fakeServer struct {
	t      *testing.T
	srv    *httptest.Server
	origin string // the origin the CLI believes it talks to

	mu              sync.Mutex
	tokens          []string // issued tokens, in order
	pendingPolls    int      // authorization_pending answers before approval
	slowDownOnce    bool
	tokenError      string // RFC 8628 error to answer instead of approving
	authorizeCalls  int
	authorizeForm   url.Values
	tokenCalls      int
	revokeForms     []url.Values
	revokeStatus    int
	revokeFailToken string // this token's revoke answers HTTP 503
	revoked         map[string]bool
	statusCalls     int
	statusError     string // raw response body for the status endpoint
	statusErrorCode int
	lifetimeNever   bool
	verificationURI string
	discoveryJSON   string // overrides the discovery document
	redirectAll     bool
	contactsQuery   url.Values
	contactsAuth    string
	contactsLang    string // Accept-Language of the last CRM request
	contactsBody    string
	contactsPages   map[string]string // list bodies by the after cursor; overrides contactsBody
	contactsStatus  int
	echoAuth        bool   // status and contacts echo the Authorization header
	authInterval    *int   // overrides the device interval
	authExpiresIn   *int   // overrides the device expires_in
	revokeBody      string // overrides the HTTP 200 revoke body
	onToken         func() // runs at the start of every token poll
	onStatus        func() // runs once, unlocked, at the start of the next status call
	tokenDelay      time.Duration
	orgID           string
	principalID     string
	orgName         string // overrides the organization name
	whatsapp        http.HandlerFunc
	outlets         http.HandlerFunc
}

func newFake(t *testing.T, origin string) *fakeServer {
	f := &fakeServer{t: t, origin: origin, revoked: map[string]bool{}, revokeStatus: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	if origin == "" {
		f.origin = f.srv.URL
	}
	f.verificationURI = "https://passport.example.com/device"
	if f.origin == server.ProductionOrigin {
		f.verificationURI = "https://" + server.ProductionVerificationHost + "/device"
	}
	return f
}

// transport rewrites every request to the test server, whatever origin the
// CLI addressed (the CLI checks the origin before this runs).
func (f *fakeServer) transport() http.RoundTripper {
	target, _ := url.Parse(f.srv.URL)
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = target.Scheme, target.Host
		r2.Host = r.URL.Host
		return http.DefaultTransport.RoundTrip(r2)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (f *fakeServer) newToken() string {
	n := len(f.tokens) + 1
	tok := server.TokenPrefix + strings.Repeat("k", 46) + fmt.Sprintf("%04d", n)
	f.tokens = append(f.tokens, tok)
	return tok
}

func (f *fakeServer) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokens) == 0 {
		return ""
	}
	return f.tokens[len(f.tokens)-1]
}

// problemJSON renders a problem document; details may be nil.
func problemJSON(status int, code, title string, details any) string {
	p := map[string]any{
		"type": "https://docs.gatherdeal.ai/errors/" + code, "title": title, "status": status,
		"code": code, "retryable": status == 429 || status == 503, "correlation_id": "corr-test",
	}
	if details != nil {
		p["details"] = details
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// writeJSON writes v; a string starting with a problem's "type" member is
// sent as application/problem+json.
func writeJSON(w http.ResponseWriter, status int, v any) {
	media := "application/json"
	if b, ok := v.(string); ok && strings.HasPrefix(b, `{"`) && strings.Contains(b, `"type":"https://docs.gatherdeal.ai/errors/`) && status >= 300 {
		media = "application/problem+json"
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Correlation-Id", "corr-test")
	w.WriteHeader(status)
	switch b := v.(type) {
	case string:
		_, _ = io.WriteString(w, b)
	default:
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (f *fakeServer) discovery() server.Discovery {
	o := f.origin
	return server.Discovery{
		SchemaVersion: 1, Server: o, Issuer: o + "/oidc", ClientID: "gatherdeal-cli",
		DeviceAuthorizationEndpoint: o + "/oidc/device/authorize", TokenEndpoint: o + "/oidc/device/token",
		RevocationEndpoint: o + "/oidc/revoke", StatusEndpoint: o + "/identity/v1/authenticate",
		VerificationURI: f.verificationURI, Services: map[string]server.Service{"crm": {BasePath: "/crm/v1"}},
		MinCLIVersion: "0.1.0",
	}
}

func (f *fakeServer) access() map[string]any {
	a := map[string]any{
		"id": "11111111-1111-1111-1111-111111111111", "credential_kind": "bearer", "restriction_version": 1,
		"client_id": "gatherdeal-cli", "organization_id": f.organizationID(),
		"outlet_ids": []string{"33333333-3333-3333-3333-333333333333"}, "permissions": []string{"crm.contact.read"},
		"effect_ceiling": map[string]any{"max_mutation": "none", "allow_external": false, "allowed_risks": []string{}},
		"lifetime":       "30d", "expire_at": "2030-01-01T00:00:00Z", "idle_expire_at": "2029-12-01T00:00:00Z",
	}
	if f.lifetimeNever {
		a["lifetime"] = "never"
		delete(a, "expire_at")
	}
	return a
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirectAll {
		http.Redirect(w, r, "https://evil.example.com/", http.StatusFound)
		return
	}
	_ = r.ParseForm()
	switch {
	case r.URL.Path == "/identity/v1/outlets" && f.outlets != nil:
		f.outlets(w, r)
	case strings.HasPrefix(r.URL.Path, "/whatsapp/") && f.whatsapp != nil:
		f.whatsapp(w, r)
	case r.Method == http.MethodGet && r.URL.Path == server.DiscoveryPath:
		if f.discoveryJSON != "" {
			writeJSON(w, 200, f.discoveryJSON)
			return
		}
		writeJSON(w, 200, f.discovery())
	case r.Method == http.MethodPost && r.URL.Path == "/oidc/device/authorize":
		f.authorizeCalls++
		f.authorizeForm = r.PostForm
		interval, expiresIn := 5, 600
		if f.authInterval != nil {
			interval = *f.authInterval
		}
		if f.authExpiresIn != nil {
			expiresIn = *f.authExpiresIn
		}
		writeJSON(w, 200, map[string]any{
			"device_code": "gd_dcd_" + strings.Repeat("d", 50), "user_code": "BCDF-GHJK",
			"verification_uri": f.verificationURI, "verification_uri_complete": f.verificationURI + "?user_code=BCDF-GHJK",
			"expires_in": expiresIn, "interval": interval,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/oidc/device/token":
		f.tokenCalls++
		if f.onToken != nil {
			f.onToken()
		}
		if f.tokenDelay > 0 {
			f.mu.Unlock()
			select {
			case <-time.After(f.tokenDelay):
			case <-r.Context().Done():
			}
			f.mu.Lock()
			if r.Context().Err() != nil {
				return
			}
		}
		if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.PostForm.Get("client_id") != "gatherdeal-cli" || r.PostForm.Get("device_code") == "" {
			writeJSON(w, 400, `{"error":"invalid_request"}`)
			return
		}
		if f.tokenError != "" {
			writeJSON(w, 400, map[string]string{"error": f.tokenError})
			return
		}
		if f.slowDownOnce {
			f.slowDownOnce = false
			writeJSON(w, 400, `{"error":"slow_down"}`)
			return
		}
		if f.pendingPolls > 0 {
			f.pendingPolls--
			writeJSON(w, 400, `{"error":"authorization_pending"}`)
			return
		}
		tok := f.newToken()
		resp := map[string]any{"access_token": tok, "token_type": "Bearer", "delegated_access": f.access()}
		if !f.lifetimeNever {
			resp["expires_in"] = 2592000
		}
		writeJSON(w, 200, resp)
	case r.Method == http.MethodPost && r.URL.Path == "/oidc/revoke":
		f.revokeForms = append(f.revokeForms, r.PostForm)
		if r.Header.Get("Authorization") != "" {
			f.t.Errorf("revoke sent an Authorization header")
		}
		if f.revokeFailToken != "" && r.PostForm.Get("token") == f.revokeFailToken {
			writeJSON(w, 503, `{"error":"temporarily_unavailable"}`)
			return
		}
		if f.revokeStatus != 200 {
			writeJSON(w, f.revokeStatus, `{"error":"server_error"}`)
			return
		}
		if f.revokeBody != "" {
			writeJSON(w, 200, f.revokeBody)
			return
		}
		f.revoked[r.PostForm.Get("token")] = true
		writeJSON(w, 200, "null")
	case r.Method == http.MethodPost && r.URL.Path == "/identity/v1/authenticate":
		f.statusCalls++
		if hook := f.onStatus; hook != nil {
			// A login calls status after the token response and before it
			// commits, so the hook interleaves another process there.
			f.onStatus = nil
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, _ := body["delegated_access_contract_version"].(float64); v != 1 {
			writeJSON(w, 401, problemJSON(401, "DELEGATED_ACCESS_UNSUPPORTED", "The caller does not support delegated access.", nil))
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if f.echoAuth {
			auth := r.Header.Get("Authorization")
			writeJSON(w, 401, problemJSON(401, "UNAUTHORIZED", "bad credential "+auth+"\x1b]0;pwned\x07",
				map[string]any{"echo": auth, "list": []string{tok}}))
			return
		}
		if f.statusError != "" {
			writeJSON(w, f.statusErrorCode, f.statusError)
			return
		}
		if f.revoked[tok] {
			writeJSON(w, 401, problemJSON(401, "DELEGATED_ACCESS_INACTIVE", "The delegated access is no longer active.", map[string]any{"reason": "revoked"}))
			return
		}
		if !f.known(tok) {
			writeJSON(w, 401, problemJSON(401, "UNAUTHORIZED", "Authentication is required.", nil))
			return
		}
		writeJSON(w, 200, map[string]any{
			"status":                            "success",
			"user":                              map[string]any{"id": f.userID(), "username": "staff1", "name": "Staff One"},
			"organization":                      map[string]any{"id": f.organizationID(), "code": "acme", "name": f.organizationName()},
			"delegated_access_contract_version": 1, "delegated_access": f.access(),
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/crm/v1/contacts"):
		f.contactsQuery = r.URL.Query()
		f.contactsAuth = r.Header.Get("Authorization")
		f.contactsLang = r.Header.Get("Accept-Language")
		if f.echoAuth {
			auth := r.Header.Get("Authorization")
			writeJSON(w, 200, `{"status":"success","contact":{"id":"x","remark":"`+auth+`","name":"\u0067d_dat_`+strings.TrimPrefix(auth, "Bearer gd_dat_")+`"}}`)
			return
		}
		status := f.contactsStatus
		if status == 0 {
			status = 200
		}
		if body, ok := f.contactsPages[r.URL.Query().Get("after")]; ok {
			writeJSON(w, status, body)
			return
		}
		writeJSON(w, status, f.contactsBody)
	default:
		writeJSON(w, 404, problemJSON(404, "NOT_FOUND", "Not found.", nil))
	}
}

func (f *fakeServer) organizationName() string {
	if f.orgName != "" {
		return f.orgName
	}
	return "Acme Sdn Bhd"
}

func (f *fakeServer) known(tok string) bool {
	for _, t := range f.tokens {
		if t == tok {
			return true
		}
	}
	return false
}

// testEnv is one isolated machine: config dir, keyring, clock.
type testEnv struct {
	t           *testing.T
	root        string
	vars        map[string]string
	keyring     *memKeyring
	fake        *fakeServer
	clockMu     *sync.Mutex
	clock       time.Time
	sleeps      []time.Duration
	stdin       string
	interactive bool
}

func newTestEnv(t *testing.T, fake *fakeServer) *testEnv {
	dir := t.TempDir()
	if repo := store.FindGitWorkTree(dir); repo != "" {
		t.Skipf("temp dir inside git work tree %s", repo)
	}
	vars := map[string]string{"XDG_CONFIG_HOME": dir, "APPDATA": dir}
	return &testEnv{t: t, root: filepath.Join(dir, "gatherdeal"), vars: vars, keyring: newMemKeyring(), fake: fake,
		clockMu: &sync.Mutex{}, clock: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
}

func (te *testEnv) now() time.Time {
	te.clockMu.Lock()
	defer te.clockMu.Unlock()
	return te.clock
}

func (te *testEnv) advance(d time.Duration) {
	te.clockMu.Lock()
	defer te.clockMu.Unlock()
	te.clock = te.clock.Add(d)
}

type result struct {
	exit   int
	stdout string
	stderr string
	env    map[string]any
}

func (r result) errorCode() string {
	if e, ok := r.env["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func (r result) data() map[string]any {
	d, _ := r.env["data"].(map[string]any)
	return d
}

func (te *testEnv) run(args ...string) result {
	te.t.Helper()
	var stdout, stderr bytes.Buffer
	env := &Env{
		Stdin: strings.NewReader(te.stdin), Stdout: &stdout, Stderr: &stderr,
		Getenv:      func(k string) string { return te.vars[k] },
		Interactive: te.interactive,
		Keyring:     te.keyring,
		Now:         te.now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			te.sleeps = append(te.sleeps, d)
			te.advance(d)
			return ctx.Err()
		},
		Hostname: func() (string, error) { return "test host", nil },
	}
	if te.fake != nil {
		env.HTTPBase = te.fake.transport()
	}
	code := Run(context.Background(), env, args)
	res := result{exit: code, stdout: stdout.String(), stderr: stderr.String()}
	node, rest, resolveErr := commandTree(Commands()).resolve(args)
	// Text commands print an envelope only on failure.
	textOutput := resolveErr == nil && (node.command == nil || (node.command.Output == "text" && code == 0))
	for _, arg := range rest {
		if arg == "--" {
			break
		}
		if arg == "--help" || arg == "-h" || arg == "-help" {
			textOutput = true
		}
		// A successful --jq prints its results, not an envelope.
		if (arg == "--jq" || strings.HasPrefix(arg, "--jq=")) && code == 0 {
			textOutput = true
		}
	}
	if !textOutput {
		if strings.Count(res.stdout, "\n") != 1 {
			te.t.Fatalf("stdout must hold exactly one envelope line, got %q", res.stdout)
		}
		if err := json.Unmarshal([]byte(res.stdout), &res.env); err != nil {
			te.t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
		}
		if int(res.env["exit_code"].(float64)) != code {
			te.t.Fatalf("envelope exit_code %v != process exit %d", res.env["exit_code"], code)
		}
	}
	// No issued token may ever appear in any output.
	if te.fake != nil {
		te.fake.mu.Lock()
		issued := append([]string(nil), te.fake.tokens...)
		te.fake.mu.Unlock()
		for _, tok := range issued {
			if strings.Contains(res.stdout, tok) || strings.Contains(res.stderr, tok) {
				te.t.Fatalf("a token was printed")
			}
		}
	}
	if v := te.vars[EnvToken]; v != "" && (strings.Contains(res.stdout, v) || strings.Contains(res.stderr, v)) {
		te.t.Fatalf("%s was printed", EnvToken)
	}
	return res
}

// memKeyring is an in-memory OS keyring; fail makes every call fail.
type memKeyring struct {
	mu    sync.Mutex
	items map[string]string
	fail  error
}

func newMemKeyring() *memKeyring { return &memKeyring{items: map[string]string{}} }

func (k *memKeyring) Set(s, u, p string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return k.fail
	}
	k.items[s+"|"+u] = p
	return nil
}

func (k *memKeyring) Get(s, u string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return "", k.fail
	}
	v, ok := k.items[s+"|"+u]
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}

func (k *memKeyring) Delete(s, u string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return k.fail
	}
	if _, ok := k.items[s+"|"+u]; !ok {
		return store.ErrNotFound
	}
	delete(k.items, s+"|"+u)
	return nil
}

var errNoSecretService = errors.New("the name org.freedesktop.secrets was not provided by any .service files")

func (f *fakeServer) organizationID() string {
	if f.orgID != "" {
		return f.orgID
	}
	return "22222222-2222-2222-2222-222222222222"
}
func (f *fakeServer) userID() string {
	if f.principalID != "" {
		return f.principalID
	}
	return "44444444-4444-4444-4444-444444444444"
}
