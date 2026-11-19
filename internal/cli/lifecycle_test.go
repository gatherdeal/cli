package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// process is another gatherdeal process on the same machine and config
// directory as te (same root, keyring, server and clock).
func process(te *testEnv) *testEnv {
	sub := *te
	sub.sleeps = nil
	return &sub
}

// Two config directories of one OS user share the keyring but never a
// credential: each sends its own token, and logging out of one leaves the
// other working.
func TestConfigDirsSharingKeyringStayIsolated(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	a := newTestEnv(t, fake)
	b := newTestEnv(t, fake)
	b.keyring = a.keyring
	if res := a.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	tokA := fake.lastToken()
	if res := b.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	tokB := fake.lastToken()
	if len(a.keyring.items) != 2 || credentialKey(t, a, "default") == credentialKey(t, b, "default") {
		t.Fatalf("config directories share a keyring account: %v", keys(a.keyring.items))
	}
	fake.contactsBody = contactsPage
	for env, want := range map[*testEnv]string{a: tokA, b: tokB} {
		if res := env.run("crm", "contacts", "list"); res.exit != 0 || fake.contactsAuth != "Bearer "+want {
			t.Fatalf("a config directory sent another directory's token (exit %d)", res.exit)
		}
	}
	if res := b.run("auth", "logout"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	if fake.revoked[tokA] || !fake.revoked[tokB] {
		t.Fatal("logout revoked the wrong access")
	}
	if res := a.run("crm", "contacts", "list"); res.exit != 0 || fake.contactsAuth != "Bearer "+tokA {
		t.Fatalf("the other directory's logout broke this login: %s", res.stdout)
	}
}

// A logout that completes between the token response and the commit wins:
// the late token is rejected and revoked, and no profile comes back.
func TestLogoutBeforeCommitRejectsLateToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		renewal bool
		args    []string
	}{
		{"first login", false, []string{"auth", "login"}},
		{"renewal", true, []string{"auth", "login"}},
		{"continue", true, []string{"auth", "login", "--continue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake(t, server.ProductionOrigin)
			te := newTestEnv(t, fake)
			var first string
			if tc.renewal {
				if res := te.run("auth", "login"); res.exit != 0 {
					t.Fatal(res.stdout)
				}
				first = fake.lastToken()
			}
			if tc.args[len(tc.args)-1] == "--continue" {
				if res := te.run("auth", "login", "--no-wait"); res.exit != 9 {
					t.Fatal(res.stdout)
				}
			}
			var logout result
			fake.onStatus = func() { logout = process(te).run("auth", "logout") }
			res := te.run(tc.args...)
			late := fake.lastToken()
			if logout.exit != 0 {
				t.Fatalf("logout: %s", logout.stdout)
			}
			if res.exit != 3 || res.errorCode() != "CLI_LOGIN_RESTART_REQUIRED" {
				t.Fatalf("late login: exit %d %s", res.exit, res.stdout)
			}
			if late == first || !fake.revoked[late] {
				t.Fatal("the late token was not revoked")
			}
			if tc.renewal && !fake.revoked[first] {
				t.Fatal("logout did not revoke the committed access")
			}
			if loadProfiles(t, te).Get("default") != nil || len(te.keyring.items) != 0 {
				t.Fatalf("the login came back after logout: %v", keys(te.keyring.items))
			}
			if st := te.run("auth", "status"); st.errorCode() != "CLI_NOT_LOGGED_IN" {
				t.Fatalf("status after logout: %s", st.stdout)
			}
		})
	}
}

// An older login that receives its token after a newer login started
// cannot overwrite it; the newer login then completes normally.
func TestOlderClaimCannotOverwriteNewerLogin(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--no-wait"); res.exit != 9 {
		t.Fatal(res.stdout)
	}
	var newer result
	fake.onStatus = func() { newer = process(te).run("auth", "login", "--no-wait") }
	old := te.run("auth", "login", "--continue")
	oldTok := fake.lastToken()
	if newer.exit != 9 {
		t.Fatalf("newer login: %s", newer.stdout)
	}
	if old.exit != 3 || old.errorCode() != "CLI_LOGIN_RESTART_REQUIRED" || !fake.revoked[oldTok] {
		t.Fatalf("older claim accepted or not revoked: %s", old.stdout)
	}
	if prof := loadProfiles(t, te).Get("default"); prof == nil || prof.Credential != nil || prof.Login == nil || prof.Login.Device == nil {
		t.Fatalf("the newer pending login was disturbed: %+v", prof)
	}
	if res := te.run("auth", "login", "--continue"); res.exit != 0 {
		t.Fatalf("newer login: %s", res.stdout)
	}
	newTok := fake.lastToken()
	if te.keyring.items[credentialKey(t, te, "default")] != newTok || fake.revoked[newTok] || len(te.keyring.items) != 1 {
		t.Fatalf("keyring after the newer login: %v", keys(te.keyring.items))
	}
}

// The same race between two synchronous logins: the one that started first
// but finished last loses.
func TestOlderSyncLoginLosesToNewerLogin(t *testing.T) {
	fake, te := loggedIn(t)
	var newer result
	fake.onStatus = func() { newer = process(te).run("auth", "login") }
	old := te.run("auth", "login")
	if newer.exit != 0 {
		t.Fatalf("newer login: %s", newer.stdout)
	}
	// Tokens: initial, older (issued before its status call), newer
	// (issued inside the hook, before the older one commits).
	fake.mu.Lock()
	initial, oldTok, newTok := fake.tokens[0], fake.tokens[1], fake.tokens[2]
	fake.mu.Unlock()
	if old.errorCode() != "CLI_LOGIN_RESTART_REQUIRED" || !fake.revoked[oldTok] {
		t.Fatalf("older login accepted: %s", old.stdout)
	}
	if te.keyring.items[credentialKey(t, te, "default")] != newTok || !fake.revoked[initial] || fake.revoked[newTok] {
		t.Fatal("the newer login was overwritten")
	}
}

// crashOnCommit kills the command at the metadata save that would commit a
// credential (the second save of a login: the first records the attempt),
// after the new secret was written: deferred functions run as in a dying
// process (the lock is released), rollbacks do not.
func crashOnCommit(t *testing.T, te *testEnv, args ...string) {
	t.Helper()
	orig := saveProfiles
	saves := 0
	saveProfiles = func(p *store.Profiles) error {
		saves++
		if saves == 2 {
			runtime.Goexit()
		}
		return p.Save()
	}
	defer func() { saveProfiles = orig }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		te.run(args...)
	}()
	<-done
}

// A crash after the new secret is written but before metadata names it
// leaves every reader on the old, consistent credential; the next login
// removes and revokes the orphan.
func TestCrashBeforeCommitKeepsOldCredential(t *testing.T) {
	for _, kind := range []store.Kind{store.KindKeyring, store.KindFile} {
		t.Run(string(kind), func(t *testing.T) {
			fake := newFake(t, server.ProductionOrigin)
			te := newTestEnv(t, fake)
			if res := te.run("auth", "login", "--credential-store", string(kind)); res.exit != 0 {
				t.Fatal(res.stdout)
			}
			first := fake.lastToken()
			before := loadProfiles(t, te).Get("default")

			crashOnCommit(t, te, "auth", "login", "--credential-store", string(kind))
			orphan := fake.lastToken()
			if orphan == first {
				t.Fatal("no new token was issued before the crash")
			}
			if secretCount(t, te) != 2 {
				t.Fatalf("expected the old credential and the orphan, got %d secrets", secretCount(t, te))
			}
			after := loadProfiles(t, te).Get("default")
			if *after.Credential != *before.Credential || after.Access.TokenHint != before.Access.TokenHint {
				t.Fatal("metadata changed without a commit")
			}
			fake.contactsBody = contactsPage
			if res := te.run("crm", "contacts", "list"); res.exit != 0 || fake.contactsAuth != "Bearer "+first {
				t.Fatalf("a reader used the uncommitted credential: %s", res.stdout)
			}
			if st := te.run("auth", "status"); st.exit != 0 || st.data()["token_hint"] != server.TokenHint(first) {
				t.Fatalf("status: %s", st.stdout)
			}

			if res := te.run("auth", "login", "--credential-store", string(kind)); res.exit != 0 {
				t.Fatal(res.stdout)
			}
			if secretCount(t, te) != 1 {
				t.Fatalf("orphan not cleaned up: %d secrets", secretCount(t, te))
			}
			if !fake.revoked[orphan] || !fake.revoked[first] {
				t.Fatal("orphan or replaced access not revoked")
			}
		})
	}
}

// A crash-left orphan is also cleaned up by logout.
func TestLogoutCleansCrashOrphan(t *testing.T) {
	fake, te := loggedIn(t)
	crashOnCommit(t, te, "auth", "login")
	orphan := fake.lastToken()
	if res := te.run("auth", "logout"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	if len(te.keyring.items) != 0 || !fake.revoked[orphan] {
		t.Fatalf("orphan left behind: %v", keys(te.keyring.items))
	}
}

// secretCount counts stored secrets in the keyring and the file store.
func secretCount(t *testing.T, te *testEnv) int {
	t.Helper()
	n := len(te.keyring.items)
	entries, _ := os.ReadDir(filepath.Join(te.root, "credentials"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n
}

// A renewal commits before it deletes the previous generation, so a reader
// that loaded the old metadata just before the commit reloads and reads
// the new generation instead of failing.
func TestReaderFollowsCommittedGeneration(t *testing.T) {
	fake, te := loggedIn(t)
	stale := process(te)
	tgt := &target{name: "default", origin: server.ProductionOrigin, production: true, builtin: true,
		disc: server.Production(), root: te.root}
	tgt.profiles = loadProfiles(t, te)
	tgt.profile = tgt.profiles.Get("default")
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	inv := &Invocation{Env: &Env{Getenv: func(k string) string { return stale.vars[k] }, Keyring: stale.keyring,
		Now: stale.now, Stderr: &strings.Builder{}}}
	cred, err := inv.loadCredential(tgt)
	if err != nil || cred.token != fake.lastToken() {
		t.Fatalf("stale reader: %v", err)
	}
}
