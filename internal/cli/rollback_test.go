package cli

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// A renewal that cannot take the lock changes nothing: the existing
// credential stays and no new access is waited for.
func TestRenewalLockTimeoutKeepsCredential(t *testing.T) {
	fake, te := loggedIn(t)
	first := fake.lastToken()
	keyringAccount := credentialKey(t, te, "default")
	unlock, err := store.Lock(te.root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	old := store.LockTimeout
	store.LockTimeout = 100 * time.Millisecond
	defer func() { store.LockTimeout = old }()

	if res := te.run("auth", "login"); res.exit == 0 {
		t.Fatal("login succeeded without the lock")
	}
	if te.keyring.items[keyringAccount] != first {
		t.Fatal("the existing credential was changed or deleted")
	}
	// The attempt could not be recorded, so no approval was awaited.
	if fake.revoked[first] || fake.lastToken() != first {
		t.Fatal("a new access was issued or the existing one revoked")
	}
}

func failSave(t *testing.T) { failSaveAfter(t, 0) }

// failSaveAfter lets ok metadata saves succeed (a login's first save
// records its attempt), then fails every later one.
func failSaveAfter(t *testing.T, ok int) {
	t.Helper()
	orig := saveProfiles
	var mu sync.Mutex
	saveProfiles = func(p *store.Profiles) error {
		mu.Lock()
		defer mu.Unlock()
		if ok > 0 {
			ok--
			return p.Save()
		}
		return errors.New("disk full")
	}
	t.Cleanup(func() { saveProfiles = orig })
}

// A failed metadata save keeps the committed credential and removes the
// new generation before the lock is released.
func TestRenewalSaveFailureRestoresCredential(t *testing.T) {
	fake, te := loggedIn(t)
	first := fake.lastToken()
	keyringAccount := credentialKey(t, te, "default")
	failSaveAfter(t, 1)
	if res := te.run("auth", "login"); res.exit == 0 {
		t.Fatal("login succeeded although the save failed")
	}
	if te.keyring.items[keyringAccount] != first || len(te.keyring.items) != 1 {
		t.Fatalf("previous credential not kept alone: %v", keys(te.keyring.items))
	}
	if fake.revoked[first] || !fake.revoked[fake.lastToken()] {
		t.Fatal("wrong access revoked")
	}
}

// Switching stores: the old store's credential is deleted only after the
// metadata commit, and a failed commit leaves the new store empty.
func TestStoreSwitchSaveFailureKeepsOldStore(t *testing.T) {
	fake, te := loggedIn(t) // keyring
	first := fake.lastToken()
	keyringAccount := credentialKey(t, te, "default")
	failSaveAfter(t, 1)
	if res := te.run("auth", "login", "--credential-store", "file"); res.exit == 0 {
		t.Fatal("login succeeded although the save failed")
	}
	if te.keyring.items[keyringAccount] != first {
		t.Fatal("old-store credential deleted before commit")
	}
	entries, _ := os.ReadDir(filepath.Join(te.root, "credentials"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			t.Fatalf("new-store credential left behind: %s", e.Name())
		}
	}
	if prof := loadProfiles(t, te).Get("default"); prof == nil || prof.Credential.Store != store.KindKeyring {
		t.Fatal("metadata changed")
	}
	if st := te.run("auth", "status"); st.exit != 0 {
		t.Fatalf("existing login broken: %s", st.stdout)
	}
}

// A first login whose save fails leaves nothing behind.
func TestFirstLoginSaveFailureLeavesNothing(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	failSaveAfter(t, 1)
	if res := te.run("auth", "login"); res.exit == 0 {
		t.Fatal("login succeeded")
	}
	if len(te.keyring.items) != 0 || !fake.revoked[fake.lastToken()] {
		t.Fatal("credential left behind or not revoked")
	}
}

// A failed pending save leaves no device code behind and keeps the older
// pending login usable.
func TestPendingSaveFailureKeepsPrevious(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--no-wait"); res.exit != 9 {
		t.Fatal(res.stdout)
	}
	before := len(te.keyring.items)
	failSave(t)
	if res := te.run("auth", "login", "--no-wait"); res.errorCode() == "CLI_LOGIN_PENDING" {
		t.Fatal("pending login reported although the save failed")
	}
	if len(te.keyring.items) != before {
		t.Fatalf("device codes: %d, want %d", len(te.keyring.items), before)
	}
	saveProfiles = func(p *store.Profiles) error { return p.Save() }
	if res := te.run("auth", "login", "--continue"); res.exit != 0 {
		t.Fatalf("earlier pending login lost: %s", res.stdout)
	}
}
