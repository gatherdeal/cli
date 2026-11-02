package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/output"
)

func TestProfileBindingRejectsRenewal(t *testing.T) {
	for _, continued := range []bool{false, true} {
		for _, principal := range []bool{false, true} {
			t.Run(stringName(continued, principal), func(t *testing.T) {
				fake, te := loggedIn(t)
				old := fake.lastToken()
				before := loadProfiles(t, te).Get("default")
				key := credentialKey(t, te, "default")
				args := []string{"auth", "login"}
				if continued {
					if res := te.run("auth", "login", "--no-wait"); res.exit != 9 {
						t.Fatal(res.stdout)
					}
					args = append(args, "--continue")
				}
				code := output.CodeProfileOrganizationMismatch
				if principal {
					fake.principalID = "other-principal"
					code = output.CodeProfilePrincipalMismatch
				} else {
					fake.orgID = "other-organization"
				}
				res := process(te).run(args...)
				if res.exit != 2 || res.errorCode() != code {
					t.Fatal(res.stdout)
				}
				after := loadProfiles(t, te).Get("default")
				if !reflect.DeepEqual(before.Credential, after.Credential) || !reflect.DeepEqual(before.Binding, after.Binding) || !reflect.DeepEqual(before.Access, after.Access) || after.Login != nil {
					t.Fatal("rejection changed committed profile or left attempt")
				}
				if te.keyring.items[key] != old || fake.revoked[old] || !fake.revoked[fake.lastToken()] || len(te.keyring.items) != 1 {
					t.Fatal("incorrect credential cleanup")
				}
				// Retarget is explicit, and remains possible after rejecting a renewal.
				if res := te.run("auth", "logout"); res.exit != 0 {
					t.Fatal(res.stdout)
				}
				if res := te.run("auth", "login"); res.exit != 0 {
					t.Fatal(res.stdout)
				}
			})
		}
	}
}
func stringName(continued, principal bool) string {
	name := "organization"
	if principal {
		name = "principal"
	}
	if continued {
		name += "-continue"
	}
	return name
}

func TestLoginRequiresReliablePrincipal(t *testing.T) {
	for _, missing := range []bool{false, true} {
		fake, te := loggedIn(t)
		old := fake.lastToken()
		key := credentialKey(t, te, "default")
		fake.statusErrorCode = 503
		fake.statusError = problemJSON(503, "UNAVAILABLE", "unavailable", nil)
		if missing {
			fake.statusErrorCode = 200
			body, err := json.Marshal(map[string]any{"status": "success", "delegated_access_contract_version": 1, "delegated_access": fake.access()})
			if err != nil {
				t.Fatal(err)
			}
			fake.statusError = string(body)
		}
		res := te.run("auth", "login")
		if res.exit == 0 || te.keyring.items[key] != old || fake.revoked[old] || !fake.revoked[fake.lastToken()] {
			t.Fatalf("unsafe login: %s", res.stdout)
		}
	}
}

func TestProfilesListOfflineAndContext(t *testing.T) {
	fake, te := loggedIn(t)
	if res := te.run("auth", "login", "--profile", "acme"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	path := filepath.Join(te.root, "profiles.json")
	before, _ := os.ReadFile(path)
	calls := fake.statusCalls
	res := te.run("auth", "profiles", "list")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	entries := res.env["data"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["profile"] != "acme" {
		t.Fatal(res.stdout)
	}
	entry := entries[0].(map[string]any)
	if entry["binding"] == nil || entry["cached_at"] == nil || entry["cache_age_seconds"] != float64(0) || entry["cached_grant"] == nil {
		t.Fatal(res.stdout)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || calls != fake.statusCalls {
		t.Fatal("list mutated state or used network")
	}
	// An environment credential must not inherit the stored profile's binding.
	te.vars[EnvToken] = fake.lastToken()
	res = te.run("auth", "status")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	ctx := res.env["meta"].(map[string]any)["context"].(map[string]any)
	if ctx["binding"] != nil || ctx["issuer"] == "" || len(ctx["request"].(map[string]any)["outlet_ids"].([]any)) != 0 {
		t.Fatal(res.stdout)
	}
}

func TestUnsupportedSchemaCLI(t *testing.T) {
	fake, te := loggedIn(t)
	path := filepath.Join(te.root, "profiles.json")
	raw := []byte(`{"schema_version":99,"future_security_rule":true,"profiles":{}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := fake.statusCalls
	for _, args := range [][]string{{"auth", "profiles", "list"}, {"auth", "status"}, {"auth", "login"}, {"auth", "logout"}} {
		res := te.run(args...)
		if res.exit != 9 || res.errorCode() != "CLI_PROFILE_SCHEMA" {
			t.Fatal(res.stdout)
		}
		if !strings.Contains(res.env["hint"].(string), "Do not delete your credentials") {
			t.Fatal(res.stdout)
		}
		details := res.env["error"].(map[string]any)["details"].(map[string]any)
		if details["found"] != float64(99) || details["supported"] != float64(1) {
			t.Fatal(res.stdout)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) || fake.statusCalls != calls {
		t.Fatal("unsupported schema accessed or modified")
	}
}
