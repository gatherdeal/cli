package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveOne(t *testing.T, root string) {
	t.Helper()
	p, err := LoadProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	p.Put("default", &Profile{Server: "https://api.example.com", Issuer: "https://api.example.com/oidc"})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestProfilesMetadataProtected(t *testing.T) {
	skipOnWindows(t)
	root := newRoot(t)
	saveOne(t, root)
	path := filepath.Join(root, "profiles.json")
	var insecure *InsecureError

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(root); !errors.As(err, &insecure) {
		t.Fatalf("group-readable profiles.json: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(root); !errors.As(err, &insecure) {
		t.Fatalf("broad config dir: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// A symlinked profiles.json (e.g. planted by another tool) is refused.
	elsewhere := filepath.Join(t.TempDir(), "p.json")
	if err := os.Rename(path, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(root); !errors.As(err, &insecure) {
		t.Fatalf("symlinked profiles.json: %v", err)
	}
}

func TestMetadataHasNoAccountNames(t *testing.T) {
	root := newRoot(t)
	p, _ := LoadProfiles(root)
	_ = p.EnsureStoreID()
	p.Put("x", &Profile{Server: "https://a.example", Issuer: "https://a.example/oidc",
		Credential: &CredentialRef{Store: KindKeyring, Generation: Generation(strings.Repeat("b", 32))},
		Login:      &LoginAttempt{Store: KindFile, Generation: Generation(strings.Repeat("a", 32))}})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "profiles.json"))
	if strings.Contains(string(raw), `"account"`) {
		t.Fatalf("metadata names a store account: %s", raw)
	}
}

func TestLockIsExclusive(t *testing.T) {
	root := newRoot(t)
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	old := LockTimeout
	LockTimeout = 150 * time.Millisecond
	defer func() { LockTimeout = old }()
	if _, err := Lock(root); err == nil || !strings.Contains(err.Error(), "another gatherdeal process") {
		t.Fatalf("second lock = %v", err)
	}
	unlock()
	unlock2, err := Lock(root)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func TestGenerations(t *testing.T) {
	a, _ := NewGeneration()
	b, _ := NewGeneration()
	if a == b {
		t.Fatal("generations collide")
	}
	var g Generation
	for _, bad := range []string{"", "../x", strings.Repeat("A", 32), strings.Repeat("a", 31), strings.Repeat("a", 32) + "#"} {
		if err := g.UnmarshalText([]byte(bad)); err == nil {
			t.Fatalf("generation %q accepted", bad)
		}
	}
	if err := g.UnmarshalText([]byte(a)); err != nil || g != a {
		t.Fatalf("generation %q rejected: %v", a, err)
	}
	root := newRoot(t)
	p, _ := LoadProfiles(root)
	if _, err := p.Slot("https://i/oidc", "p"); err == nil {
		t.Fatal("slot without a store id")
	}
	_ = p.EnsureStoreID()
	s, err := p.Slot("https://i/oidc", "p")
	if err != nil {
		t.Fatal(err)
	}
	if s.DeviceCodeAccount(a) == s.DeviceCodeAccount(b) || s.CredentialAccount(a) == s.CredentialAccount(b) || s.CredentialAccount(a) == s.DeviceCodeAccount(a) {
		t.Fatal("accounts collide")
	}
	for _, bad := range [][2]string{{"", "p"}, {"https://i/oidc#x", "p"}, {"https://i/oidc", "../p"}} {
		if _, err := p.Slot(bad[0], bad[1]); err == nil {
			t.Fatalf("slot %q accepted", bad)
		}
	}
}

// Two config directories never derive the same account, and a directory
// keeps its store id across saves and moves.
func TestStoreIDNamespacesAccounts(t *testing.T) {
	g, _ := NewGeneration()
	account := func(root string) string {
		t.Helper()
		p, err := LoadProfiles(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.EnsureStoreID(); err != nil {
			t.Fatal(err)
		}
		p.Put("default", &Profile{Server: "https://a.example", Issuer: "https://a.example/oidc",
			Credential: &CredentialRef{Store: KindKeyring, Generation: g}})
		if err := p.Save(); err != nil {
			t.Fatal(err)
		}
		s, err := p.Slot("https://a.example/oidc", "default")
		if err != nil {
			t.Fatal(err)
		}
		return s.CredentialAccount(g)
	}
	a, b := newRoot(t), newRoot(t)
	accA := account(a)
	if accA == account(b) {
		t.Fatal("two config directories share a credential account")
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(a, moved); err != nil {
		t.Fatal(err)
	}
	if account(moved) != accA {
		t.Fatal("moving the config directory changed its accounts")
	}
}

// Metadata naming an invalid generation, store or store id is corrupt,
// never used.
func TestProfilesRejectInvalidReferences(t *testing.T) {
	gen := strings.Repeat("a", 32)
	sid := strings.Repeat("c", 32)
	cases := map[string]string{
		"bad generation": `{"schema_version":1,"store_id":"` + sid + `","profiles":{"p":{"server":"s","issuer":"i","credential":{"store":"keyring","generation":"../x"}}}}`,
		"no generation":  `{"schema_version":1,"store_id":"` + sid + `","profiles":{"p":{"server":"s","issuer":"i","credential":{"store":"keyring"}}}}`,
		"bad store":      `{"schema_version":1,"store_id":"` + sid + `","profiles":{"p":{"server":"s","issuer":"i","credential":{"store":"vault","generation":"` + gen + `"}}}}`,
		"no store id":    `{"schema_version":1,"profiles":{"p":{"server":"s","issuer":"i","credential":{"store":"keyring","generation":"` + gen + `"}}}}`,
		"bad store id":   `{"schema_version":1,"store_id":"x","profiles":{}}`,
		"bad name":       `{"schema_version":1,"store_id":"` + sid + `","profiles":{"../p":{"server":"s","issuer":"i"}}}`,
		"unknown schema": `{"schema_version":0,"profiles":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := newRoot(t)
			if err := ensurePrivateDir(root); err != nil {
				t.Fatal(err)
			}
			if err := writeFileAtomic(root, profilesFileName, []byte(body)); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(root); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
