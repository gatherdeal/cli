package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

func loadProfiles(t *testing.T, te *testEnv) *store.Profiles {
	t.Helper()
	p, err := store.LoadProfiles(te.root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// credentialKey is the memKeyring key of the committed credential of
// profile, derived the way the CLI derives it.
func credentialKey(t *testing.T, te *testEnv, profile string) string {
	t.Helper()
	p := loadProfiles(t, te)
	prof := p.Get(profile)
	if prof == nil || prof.Credential == nil {
		t.Fatalf("profile %s has no credential", profile)
	}
	slot, err := p.Slot(prof.Issuer, profile)
	if err != nil {
		t.Fatal(err)
	}
	return store.KeyringService + "|" + slot.CredentialAccount(prof.Credential.Generation)
}

func TestLoginProductionKeyring(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	fake.pendingPolls = 2
	te := newTestEnv(t, fake)
	res := te.run("auth", "login", "--lifetime", "30d", "--device-name", "my laptop")
	if res.exit != 0 {
		t.Fatalf("exit %d: %s\n%s", res.exit, res.stdout, res.stderr)
	}
	// Device authorization carried the client id and scope hints.
	if got := fake.authorizeForm.Get("client_id"); got != "gatherdeal-cli" {
		t.Fatalf("client_id = %q", got)
	}
	if got := fake.authorizeForm.Get("scope"); got != "gd.preset:read_only gd.lifetime:30d gd.device:my-laptop" {
		t.Fatalf("scope = %q", got)
	}
	// Polled at the server interval until approved.
	if fake.tokenCalls != 3 || len(te.sleeps) != 3 || te.sleeps[0] != 5*time.Second {
		t.Fatalf("token calls %d, sleeps %v", fake.tokenCalls, te.sleeps)
	}
	for _, want := range []string{"PASSPORT.GATHERDEAL.AI", "https://passport.gatherdeal.ai/device", "BCDF-GHJK"} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, res.stderr)
		}
	}
	if strings.Contains(res.stdout, "BCDF-GHJK") {
		t.Error("an interactive-style login put the code on stdout")
	}
	d := res.data()
	if d["credential_store"] != "keyring" || d["server"] != server.ProductionOrigin || d["profile"] != "default" {
		t.Fatalf("data = %v", d)
	}
	tok := fake.lastToken()
	if d["token_hint"] != "gd_dat_…"+tok[len(tok)-4:] {
		t.Fatalf("token_hint = %v", d["token_hint"])
	}
	org := d["delegated_access"].(map[string]any)["organization"].(map[string]any)
	if org["name"] != "Acme Sdn Bhd" {
		t.Fatalf("organization = %v", org)
	}
	// Stored in the keyring under service gatherdeal-cli, account
	// <store id>#<issuer>#<profile>#credential#<generation>.
	key := credentialKey(t, te, "default")
	if !strings.HasPrefix(key, "gatherdeal-cli|") || !strings.Contains(key, "#https://api.gatherdeal.com/oidc#default#credential#") {
		t.Fatalf("credential key %q", key)
	}
	if te.keyring.items[key] != tok {
		t.Fatalf("keyring items: %v", keys(te.keyring.items))
	}
	if len(te.keyring.items) != 1 {
		t.Fatalf("preflight probe left behind: %v", keys(te.keyring.items))
	}
	prof := loadProfiles(t, te).Get("default")
	if prof == nil || prof.Server != server.ProductionOrigin || prof.Issuer != server.ProductionIssuer || prof.Discovery != nil {
		t.Fatalf("profile = %+v", prof)
	}
	raw, _ := os.ReadFile(filepath.Join(te.root, "profiles.json"))
	if strings.Contains(string(raw), tok) {
		t.Fatal("profiles.json holds the token")
	}
	if _, err := os.Stat(filepath.Join(te.root, "credentials")); !os.IsNotExist(err) {
		t.Fatal("keyring login created a credentials directory")
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLoginSlowDownIncreasesInterval(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	fake.slowDownOnce = true
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatalf("exit %d: %s", res.exit, res.stdout)
	}
	if len(te.sleeps) != 2 || te.sleeps[0] != 5*time.Second || te.sleeps[1] != 10*time.Second {
		t.Fatalf("sleeps = %v", te.sleeps)
	}
}

func TestLoginTokenErrors(t *testing.T) {
	cases := map[string]struct {
		exit int
		code string
	}{
		"access_denied":             {4, "CLI_LOGIN_DENIED"},
		"expired_token":             {3, "CLI_LOGIN_EXPIRED"},
		"invalid_grant":             {3, "CLI_LOGIN_RESTART_REQUIRED"},
		"unauthorized_client":       {1, "CLI_LOGIN_REJECTED"},
		"too_many_pending_requests": {7, "CLI_RATE_LIMITED"},
		"server_error":              {8, "CLI_UPSTREAM_ERROR"},
	}
	for oauthErr, want := range cases {
		t.Run(oauthErr, func(t *testing.T) {
			fake := newFake(t, server.ProductionOrigin)
			fake.tokenError = oauthErr
			te := newTestEnv(t, fake)
			res := te.run("auth", "login")
			if res.exit != want.exit || res.errorCode() != want.code {
				t.Fatalf("exit %d code %s, want %d %s", res.exit, res.errorCode(), want.exit, want.code)
			}
			if loadProfiles(t, te).Get("default") != nil {
				t.Fatal("a failed login saved a profile")
			}
		})
	}
}

func TestLoginCodeExpiresWhilePending(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	fake.pendingPolls = 1000
	te := newTestEnv(t, fake)
	res := te.run("auth", "login")
	if res.exit != 3 || res.errorCode() != "CLI_LOGIN_EXPIRED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if fake.tokenCalls > 120 {
		t.Fatalf("polled %d times in 600s at a 5s interval", fake.tokenCalls)
	}
}

func TestLoginKeyringUnavailableNonInteractive(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	te.keyring.fail = errNoSecretService
	res := te.run("auth", "login")
	if res.exit != 9 || res.errorCode() != "CLI_CREDENTIAL_STORE_UNAVAILABLE" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if !strings.Contains(res.env["hint"].(string), "--credential-store file") {
		t.Fatalf("hint = %v", res.env["hint"])
	}
	// The preflight runs before the device flow: no code was requested,
	// and nothing fell back to the file store.
	if fake.authorizeCalls != 0 {
		t.Fatal("device authorization started before the storage preflight")
	}
	if _, err := os.Stat(filepath.Join(te.root, "credentials")); !os.IsNotExist(err) {
		t.Fatal("silently fell back to the file store")
	}
}

func TestLoginKeyringUnavailableInteractiveChoosesFile(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	te.keyring.fail = errNoSecretService
	te.interactive = true
	te.stdin = "y\n"
	res := te.run("auth", "login")
	if res.exit != 0 || res.data()["credential_store"] != "file" {
		t.Fatalf("exit %d: %s", res.exit, res.stdout)
	}
	if !strings.Contains(res.stderr, "Same-user programs can read it") {
		t.Fatalf("prompt missing: %s", res.stderr)
	}
}

func TestLoginFileStore(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	res := te.run("auth", "login", "--credential-store", "file")
	if res.exit != 0 {
		t.Fatalf("exit %d: %s", res.exit, res.stdout)
	}
	if len(te.keyring.items) != 0 {
		t.Fatal("file login touched the keyring")
	}
	entries, err := os.ReadDir(filepath.Join(te.root, "credentials"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("credentials dir: %v %v", entries, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(te.root, "credentials", entries[0].Name()))
		di, _ := os.Stat(filepath.Join(te.root, "credentials"))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes file %04o dir %04o", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	if st := te.run("auth", "status"); st.exit != 0 || st.data()["credential_source"] != "file" {
		t.Fatalf("status: %s", st.stdout)
	}
}

func TestLoginFileStoreRefusesGitWorkTree(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	te.vars["XDG_CONFIG_HOME"], te.vars["APPDATA"] = repo, repo
	te.root = filepath.Join(repo, "gatherdeal")
	res := te.run("auth", "login", "--credential-store", "file")
	if res.exit != 9 || res.errorCode() != "CLI_CREDENTIAL_STORE_INSECURE" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if fake.authorizeCalls != 0 {
		t.Fatal("device flow started")
	}
}

func TestLoginCustomServerRisk(t *testing.T) {
	fake := newFake(t, "")
	origin := fake.origin
	warning := fmt.Sprintf(ServerRiskWarning, origin)

	te := newTestEnv(t, fake)
	res := te.run("auth", "login", "--server", origin)
	if res.exit != 9 || res.errorCode() != "CLI_SERVER_RISK_NOT_ACCEPTED" {
		t.Fatalf("no TTY, no accept: exit %d code %s", res.exit, res.errorCode())
	}
	if !strings.Contains(res.stderr, warning) {
		t.Fatalf("warning not verbatim:\n%s", res.stderr)
	}
	if fake.authorizeCalls != 0 {
		t.Fatal("login proceeded without confirmation")
	}

	res = te.run("auth", "login", "--server", origin, "--accept-server-risk", "https://other.example.com")
	if res.exit != 2 {
		t.Fatalf("mismatched accept: exit %d", res.exit)
	}

	te.interactive, te.stdin = true, "n\n"
	res = te.run("auth", "login", "--server", origin)
	if res.exit != 9 || res.errorCode() != "CLI_SERVER_RISK_DECLINED" || !strings.Contains(res.stderr, "Continue? [y/N]") {
		t.Fatalf("declined: exit %d code %s", res.exit, res.errorCode())
	}

	te.interactive, te.stdin = false, ""
	res = te.run("auth", "login", "--server", origin, "--accept-server-risk", origin)
	if res.exit != 0 {
		t.Fatalf("accepted: exit %d %s", res.exit, res.stdout)
	}
	name := defaultProfileName(origin)
	if res.data()["profile"] != name || !strings.HasPrefix(name, "127.0.0.1-") {
		t.Fatalf("profile = %v", res.data()["profile"])
	}
	prof := loadProfiles(t, te).Get(name)
	if prof == nil || prof.Discovery == nil || prof.Server != origin {
		t.Fatalf("profile = %+v", prof)
	}
	// The built-in profile is untouched and still not logged in.
	if st := te.run("auth", "status"); st.exit != 3 {
		t.Fatalf("default profile status exit %d", st.exit)
	}
	if st := te.run("auth", "status", "--server", origin); st.exit != 0 || st.env["meta"].(map[string]any)["server"] != origin {
		t.Fatalf("custom status: %s", st.stdout)
	}
	if st := te.run("auth", "status", "--profile", name); st.exit != 0 {
		t.Fatalf("custom status by profile: %s", st.stdout)
	}
	// Interactive "y" also works.
	te.interactive, te.stdin = true, "y\n"
	if res := te.run("auth", "login", "--server", origin); res.exit != 0 {
		t.Fatalf("interactive yes: exit %d", res.exit)
	}
}

func TestLoginCustomServerDiscoveryRejected(t *testing.T) {
	cases := map[string]func(d *server.Discovery){
		"schema version": func(d *server.Discovery) { d.SchemaVersion = 2 },
		"issuer":         func(d *server.Discovery) { d.Issuer = "https://evil.example.com/oidc" },
		"token endpoint": func(d *server.Discovery) { d.TokenEndpoint = "https://evil.example.com/oidc/device/token" },
		"base path": func(d *server.Discovery) {
			d.Services["crm"] = server.Service{BasePath: "https://evil.example.com/crm/v1"}
		},
		"verification": func(d *server.Discovery) { d.VerificationURI = "ftp://passport.example.com/device" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFake(t, "")
			d := fake.discovery()
			mutate(&d)
			b, _ := json.Marshal(d)
			fake.discoveryJSON = string(b)
			te := newTestEnv(t, fake)
			res := te.run("auth", "login", "--server", fake.origin, "--accept-server-risk", fake.origin)
			if res.exit != 1 || res.errorCode() != "CLI_DISCOVERY_INVALID" {
				t.Fatalf("exit %d code %s", res.exit, res.errorCode())
			}
			if fake.authorizeCalls != 0 {
				t.Fatal("device flow started on a rejected discovery document")
			}
		})
	}
}

func TestDeviceResponseVerificationHostChecked(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	fake.verificationURI = "https://passport.evil.example.com/device"
	res := te.run("auth", "login")
	if res.exit != 1 || res.errorCode() != "CLI_PROTOCOL_ERROR" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if fake.tokenCalls != 0 {
		t.Fatal("polled after an unsafe approval page")
	}
}

func TestRedirectsAreProtocolErrors(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	fake.redirectAll = true
	res := te.run("auth", "login")
	if res.exit != 1 || res.errorCode() != "CLI_REDIRECT_REFUSED" {
		t.Fatalf("login: exit %d code %s", res.exit, res.errorCode())
	}
	fake.redirectAll = false
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	fake.redirectAll = true
	for _, args := range [][]string{{"auth", "status"}, {"crm", "contacts", "list"}} {
		res := te.run(args...)
		if res.exit != 1 || res.errorCode() != "CLI_REDIRECT_REFUSED" {
			t.Fatalf("%v: exit %d code %s", args, res.exit, res.errorCode())
		}
	}
}

func TestNoWaitThenContinue(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	res := te.run("auth", "login", "--no-wait")
	if res.exit != 9 || res.errorCode() != "CLI_LOGIN_PENDING" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	details := res.env["error"].(map[string]any)["details"].(map[string]any)
	if details["user_code"] != "BCDF-GHJK" || details["verification_uri"] != "https://passport.gatherdeal.ai/device" ||
		details["approval_host"] != "passport.gatherdeal.ai" || details["continue_command"] != "gatherdeal auth login --continue" {
		t.Fatalf("details = %v", details)
	}
	if fake.tokenCalls != 0 {
		t.Fatal("--no-wait polled")
	}
	raw, _ := os.ReadFile(filepath.Join(te.root, "profiles.json"))
	if strings.Contains(string(raw), "gd_dcd_") {
		t.Fatal("the device code was written to profiles.json")
	}
	fake.pendingPolls = 1
	res = te.run("auth", "login", "--continue")
	if res.exit != 0 {
		t.Fatalf("continue: exit %d %s", res.exit, res.stdout)
	}
	if len(te.keyring.items) != 1 {
		t.Fatalf("pending device code not cleaned up: %v", keys(te.keyring.items))
	}
	if res := te.run("auth", "login", "--continue"); res.exit != 2 {
		t.Fatalf("second continue: exit %d", res.exit)
	}
}

func TestContinueAfterExpiry(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--no-wait"); res.exit != 9 {
		t.Fatal(res.stdout)
	}
	te.clock = te.clock.Add(11 * time.Minute)
	res := te.run("auth", "login", "--continue")
	if res.exit != 3 || res.errorCode() != "CLI_LOGIN_EXPIRED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if loadProfiles(t, te).Get("default") != nil || len(te.keyring.items) != 0 {
		t.Fatal("expired pending login not cleaned up")
	}
}

func TestStatus(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	res := te.run("auth", "status")
	if res.exit != 0 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	d := res.data()
	da := d["delegated_access"].(map[string]any)
	if d["credential_source"] != "keyring" || da["lifetime"] != "30d" || da["never_expires"] != false ||
		len(da["outlet_ids"].([]any)) != 1 || d["user"].(map[string]any)["username"] != "staff1" {
		t.Fatalf("data = %v", d)
	}
	meta := res.env["meta"].(map[string]any)
	if meta["server"] != server.ProductionOrigin || meta["credential_expires_at"] != "2030-01-01T00:00:00Z" || meta["correlation_id"] != "corr-test" {
		t.Fatalf("meta = %v", meta)
	}
}

func TestStatusInactiveReasons(t *testing.T) {
	cases := map[string]string{
		"expired": "has expired",
		"idle":    "unused for 30 days",
		"revoked": "was revoked",
	}
	const title = "Akses yang diwakilkan tidak lagi aktif."
	for reason, msg := range cases {
		t.Run(reason, func(t *testing.T) {
			fake := newFake(t, server.ProductionOrigin)
			te := newTestEnv(t, fake)
			if res := te.run("auth", "login"); res.exit != 0 {
				t.Fatal(res.stdout)
			}
			fake.statusError = problemJSON(401, "DELEGATED_ACCESS_INACTIVE", title, map[string]any{"reason": reason})
			fake.statusErrorCode = 401
			for _, args := range [][]string{{"auth", "status"}, {"crm", "contacts", "list"}} {
				if args[0] == "crm" {
					fake.contactsStatus, fake.contactsBody = 401, fake.statusError
				}
				res := te.run(args...)
				e := res.env["error"].(map[string]any)
				// The server's (localized) title is kept; the CLI explains the
				// reason in the hint.
				if res.exit != 3 || e["code"] != "DELEGATED_ACCESS_INACTIVE" || e["message"] != title ||
					!strings.Contains(res.env["hint"].(string), msg) || !strings.Contains(res.env["hint"].(string), "gatherdeal auth login") {
					t.Fatalf("%v: %s", args, res.stdout)
				}
				if e["details"].(map[string]any)["reason"] != reason {
					t.Fatalf("details not passed through: %v", e["details"])
				}
			}
		})
	}
}

func TestStatusRejectsUnknownRestrictionVersion(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	fake.statusError = `{"status":"success","delegated_access_contract_version":1,"delegated_access":{"id":"x","credential_kind":"bearer","restriction_version":2,"client_id":"gatherdeal-cli","organization_id":"y"}}`
	fake.statusErrorCode = 200
	res := te.run("auth", "status")
	if res.exit != 1 || res.errorCode() != "CLI_UNSUPPORTED_RESTRICTION_VERSION" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
}

func TestNeverExpiringAccess(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	fake.lifetimeNever = true
	te := newTestEnv(t, fake)
	res := te.run("auth", "login", "--lifetime", "never")
	if res.exit != 0 || !strings.Contains(res.stderr, "This GatherDeal access never expires.") {
		t.Fatalf("exit %d stderr %s", res.exit, res.stderr)
	}
	st := te.run("auth", "status")
	meta := st.env["meta"].(map[string]any)
	if v, ok := meta["credential_expires_at"]; !ok || v != nil {
		t.Fatalf("credential_expires_at must be null, meta = %v", meta)
	}
	if st.data()["delegated_access"].(map[string]any)["never_expires"] != true {
		t.Fatal("never_expires not set")
	}
}

func TestExpiryWarning(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	fake.contactsBody = `{"status":"success","contacts":{"data":[],"page":1,"page_size":20,"total_items":0,"total_pages":0,"has_next_page":false}}`
	res := te.run("crm", "contacts", "list")
	if res.env["meta"].(map[string]any)["credential_expiry_warning"] != nil {
		t.Fatal("warned far from expiry")
	}
	te.clock = time.Date(2029, 12, 28, 0, 0, 0, 0, time.UTC)
	res = te.run("crm", "contacts", "list")
	meta := res.env["meta"].(map[string]any)
	if meta["credential_expiry_warning"] != true || meta["credential_expires_at"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("meta = %v", meta)
	}
	if !strings.Contains(res.stderr, "GatherDeal access for Acme Sdn Bhd expires on") {
		t.Fatalf("stderr = %s", res.stderr)
	}
}

func TestLogout(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	tok := fake.lastToken()
	res := te.run("auth", "logout")
	if res.exit != 0 || res.data()["revoked"] != true || res.data()["credential_deleted"] != true {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	if len(fake.revokeForms) != 1 || fake.revokeForms[0].Get("token") != tok || fake.revokeForms[0].Get("client_id") != "gatherdeal-cli" {
		t.Fatalf("revoke form = %v", fake.revokeForms)
	}
	if len(te.keyring.items) != 0 || loadProfiles(t, te).Get("default") != nil {
		t.Fatal("credential not deleted")
	}
	if st := te.run("auth", "status"); st.exit != 3 {
		t.Fatalf("status after logout: exit %d", st.exit)
	}
	if again := te.run("auth", "logout"); again.exit != 0 || again.data()["logged_in"] != false {
		t.Fatalf("idempotent logout: %s", again.stdout)
	}
}

func TestLogoutDeletesLocallyWhenRevokeFails(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	fake.revokeStatus = 500
	res := te.run("auth", "logout")
	if res.exit != 8 || res.errorCode() != "CLI_REVOKE_FAILED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	details := res.env["error"].(map[string]any)["details"].(map[string]any)
	if details["credential_deleted"] != true {
		t.Fatalf("details = %v", details)
	}
	if len(te.keyring.items) != 0 || loadProfiles(t, te).Get("default") != nil {
		t.Fatal("credential kept after a failed revoke")
	}
}

func TestRenewalRevokesPreviousAccess(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	first := fake.lastToken()
	res := te.run("auth", "login", "--credential-store", "file")
	if res.exit != 0 || res.data()["previous_access_revoked"] != true {
		t.Fatalf("renewal: %s", res.stdout)
	}
	if !fake.revoked[first] || fake.revoked[fake.lastToken()] {
		t.Fatal("wrong access revoked")
	}
	if len(te.keyring.items) != 0 {
		t.Fatal("old keyring credential left behind after switching stores")
	}
}

func TestEnvToken(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	// Issue a token through a login, then forget the stored copy so only
	// the environment holds it.
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	tok := fake.lastToken()
	if res := te.run("auth", "logout"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	delete(fake.revoked, tok)
	before := snapshot(t, te.root)

	te.vars[EnvToken] = tok
	res := te.run("auth", "status")
	if res.exit != 0 || res.data()["credential_source"] != "environment" {
		t.Fatalf("status with %s: %s", EnvToken, res.stdout)
	}
	if after := snapshot(t, te.root); after != before || len(te.keyring.items) != 0 {
		t.Fatal(EnvToken + " was persisted")
	}

	for _, args := range [][]string{
		{"auth", "status", "--server", "https://api.example.com"},
		{"auth", "status", "--server", server.ProductionOrigin},
		{"auth", "status", "--profile", "work"},
		{"crm", "contacts", "list", "--profile", "work"},
		{"auth", "login"},
	} {
		res := te.run(args...)
		if res.exit != 2 || res.errorCode() != "CLI_TOKEN_WITH_CUSTOM_SERVER" {
			t.Fatalf("%v: exit %d code %s", args, res.exit, res.errorCode())
		}
	}

	// A stored credential exists, but an invalid environment token never
	// falls back to it.
	delete(te.vars, EnvToken)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	statusCalls := fake.statusCalls
	te.vars[EnvToken] = "gd_oidc_" + strings.Repeat("x", 50)
	res = te.run("auth", "status")
	if res.exit != 2 || res.errorCode() != "CLI_TOKEN_INVALID" {
		t.Fatalf("invalid token: exit %d code %s", res.exit, res.errorCode())
	}
	if fake.statusCalls != statusCalls {
		t.Fatal("fell back to the stored credential")
	}
}

// snapshot lists the config tree (names and sizes).
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			fmt.Fprintf(&b, "%s %d\n", p, fi.Size())
		}
		return nil
	})
	return b.String()
}

func TestProfileBoundToServer(t *testing.T) {
	fake := newFake(t, "")
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--server", fake.origin, "--accept-server-risk", fake.origin, "--profile", "dev"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	res := te.run("auth", "status", "--profile", "dev", "--server", "https://api.example.com")
	if res.exit != 2 || res.errorCode() != "CLI_PROFILE_SERVER_MISMATCH" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	res = te.run("auth", "login", "--server", fake.origin, "--accept-server-risk", fake.origin, "--profile", "default")
	if res.exit != 2 {
		t.Fatalf("default profile on a custom server: exit %d", res.exit)
	}
}
