package cli

import (
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
)

// Recovery hints of a rejected credential name the profile and server the
// failed command used.
func TestRejectedCredentialHintKeepsTarget(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--profile", "finance"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	fake.statusError = problemJSON(401, "DELEGATED_ACCESS_INACTIVE", "x", map[string]any{"reason": "idle"})
	fake.statusErrorCode = 401
	fake.contactsStatus, fake.contactsBody = 401, fake.statusError
	for _, args := range [][]string{
		{"crm", "contacts", "list", "--profile", "finance"},
		{"crm", "contacts", "get", "55555555-5555-5555-5555-555555555555", "--profile", "finance"},
		{"auth", "status", "--profile", "finance"},
	} {
		res := te.run(args...)
		hint, _ := res.env["hint"].(string)
		if res.exit != 3 || !strings.Contains(hint, "unused for 30 days") || !strings.Contains(hint, "`gatherdeal auth login --profile finance`") {
			t.Fatalf("%v: %s", args, res.stdout)
		}
	}
	// A plain 401 without a reason gets the same targeted command.
	fake.contactsBody = problemJSON(401, "UNAUTHORIZED", "x", nil)
	res := te.run("crm", "contacts", "list", "--profile", "finance")
	if hint, _ := res.env["hint"].(string); hint != "Run `gatherdeal auth login --profile finance` to create a new access." {
		t.Fatalf("plain 401: %s", res.stdout)
	}
}

// A custom-server profile's hint keeps --server.
func TestRejectedCredentialHintKeepsServer(t *testing.T) {
	fake := newFake(t, "")
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--server", fake.origin, "--accept-server-risk", fake.origin); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	fake.statusError = problemJSON(401, "DELEGATED_ACCESS_INACTIVE", "x", map[string]any{"reason": "revoked"})
	fake.statusErrorCode = 401
	res := te.run("auth", "status", "--server", fake.origin)
	if hint, _ := res.env["hint"].(string); !strings.Contains(hint, "`gatherdeal auth login --server "+fake.origin+"`") {
		t.Fatalf("hint: %s", res.stdout)
	}
}

// When GATHERDEAL_TOKEN supplied the rejected credential, the hint says so
// instead of suggesting a login the variable would still override.
func TestRejectedEnvironmentTokenHint(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	te.vars[EnvToken] = server.TokenPrefix + strings.Repeat("e", 50)
	fake.statusError = problemJSON(401, "DELEGATED_ACCESS_INACTIVE", "x", map[string]any{"reason": "expired"})
	fake.statusErrorCode = 401
	res := te.run("auth", "status")
	hint, _ := res.env["hint"].(string)
	if res.exit != 3 || !strings.Contains(hint, "has expired") || !strings.Contains(hint, EnvToken) || !strings.Contains(hint, "unset it") {
		t.Fatalf("hint: %s", res.stdout)
	}
}

// Device-flow failures suggest retrying the same profile.
func TestLoginFailureHintKeepsTarget(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	fake.tokenError = "expired_token"
	te := newTestEnv(t, fake)
	res := te.run("auth", "login", "--profile", "finance")
	if hint, _ := res.env["hint"].(string); res.errorCode() != "CLI_LOGIN_EXPIRED" || !strings.Contains(hint, "`gatherdeal auth login --profile finance`") {
		t.Fatalf("sync: %s", res.stdout)
	}
	fake.tokenError = ""
	if res := te.run("auth", "login", "--no-wait", "--profile", "finance"); res.exit != 9 {
		t.Fatal(res.stdout)
	}
	fake.tokenError = "access_denied"
	res = te.run("auth", "login", "--continue", "--profile", "finance")
	if hint, _ := res.env["hint"].(string); res.errorCode() != "CLI_LOGIN_DENIED" || !strings.Contains(hint, "`gatherdeal auth login --no-wait --profile finance`") {
		t.Fatalf("continue: %s", res.stdout)
	}
	if loadProfiles(t, te).Get("finance") != nil {
		t.Fatal("a denied login left its profile behind")
	}
}
