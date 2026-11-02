package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
)

// A tampered profiles.json cannot make a custom-server profile use the
// production credential: accounts are derived from the store id, issuer,
// profile and generation, never read from metadata. Copying the production
// generation into another profile still names a different account.
func TestTamperedMetadataCannotRedirectCredential(t *testing.T) {
	prod := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, prod)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}

	evil := newFake(t, "")
	path := filepath.Join(te.root, "profiles.json")
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	d := evil.discovery()
	profs := doc["profiles"].(map[string]any)
	cred := profs["default"].(map[string]any)["credential"]
	profs["evil"] = map[string]any{
		"server": evil.origin, "issuer": evil.origin + "/oidc", "discovery": d, "credential": cred,
	}
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	te.fake = evil
	res := te.run("auth", "status", "--profile", "evil")
	if res.exit != 3 {
		t.Fatalf("exit %d: %s", res.exit, res.stdout)
	}
	if evil.statusCalls != 0 {
		t.Fatal("a request with a credential reached the other server")
	}
}

func TestInsecureProfilesRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics")
	}
	_, te := loggedIn(t)
	if err := os.Chmod(filepath.Join(te.root, "profiles.json"), 0o666); err != nil {
		t.Fatal(err)
	}
	if res := te.run("auth", "status"); res.exit != 9 || res.errorCode() != "CLI_CREDENTIAL_STORE_INSECURE" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
}
