package cli

import (
	"testing"
	"time"

	"github.com/gatherdeal/cli/internal/server"
)

func TestDevicePollingBounds(t *testing.T) {
	for name, tc := range map[string]struct{ interval, expires int }{
		"huge interval":   {2147483647, 600},
		"negative":        {-1, 600},
		"huge expires_in": {5, 1 << 30},
		"zero expires_in": {5, 0},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFake(t, server.ProductionOrigin)
			fake.authInterval, fake.authExpiresIn = &tc.interval, &tc.expires
			te := newTestEnv(t, fake)
			res := te.run("auth", "login")
			if res.exit != 1 || res.errorCode() != "CLI_PROTOCOL_ERROR" || fake.tokenCalls != 0 {
				t.Fatalf("exit %d code %s polls %d", res.exit, res.errorCode(), fake.tokenCalls)
			}
		})
	}
	// Sleeps never run past the expiry deadline, slow_down is capped.
	fake := newFake(t, server.ProductionOrigin)
	interval, expires := 60, 7
	fake.authInterval, fake.authExpiresIn = &interval, &expires
	fake.pendingPolls = 1000
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 3 {
		t.Fatalf("exit %d", res.exit)
	}
	var total time.Duration
	for _, s := range te.sleeps {
		total += s
	}
	if total > 7*time.Second {
		t.Fatalf("slept %v past a 7s expiry", total)
	}
}

// A token response that arrives after the code expired is not kept: the
// late credential is revoked and the login reports expiry.
func TestLateTokenAfterExpiryRevoked(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	interval, expires := 5, 6
	fake.authInterval, fake.authExpiresIn = &interval, &expires
	te := newTestEnv(t, fake)
	fake.onToken = func() { te.advance(10 * time.Second) } // the request "takes" 10s
	res := te.run("auth", "login")
	if res.exit != 3 || res.errorCode() != "CLI_LOGIN_EXPIRED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if len(fake.tokens) != 1 || !fake.revoked[fake.tokens[0]] {
		t.Fatal("late credential not revoked")
	}
	if loadProfiles(t, te).Get("default") != nil || len(te.keyring.items) != 0 {
		t.Fatal("late credential stored")
	}
}

// A poll request that hangs is cut off at the code's expiry.
func TestPollRequestBoundedByExpiry(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	interval, expires := 1, 2
	fake.authInterval, fake.authExpiresIn = &interval, &expires
	fake.tokenDelay = 30 * time.Second
	te := newTestEnv(t, fake)
	start := time.Now()
	res := te.run("auth", "login")
	if res.exit != 3 || res.errorCode() != "CLI_LOGIN_EXPIRED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("poll ran %v past a 2s code lifetime", elapsed)
	}
}
