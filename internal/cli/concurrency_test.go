package cli

import (
	"strings"
	"sync"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// Concurrent --no-wait logins never pair one code with another's record,
// and concurrent logins never lose a profile.
func TestConcurrentStateChanges(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := *te
			sub.t = t
			profile := []string{"default", "a", "b"}[i%3]
			sub.run("auth", "login", "--no-wait", "--profile", profile)
		}(i)
	}
	wg.Wait()
	p, err := store.LoadProfiles(te.root)
	if err != nil {
		t.Fatal(err)
	}
	pendingSecrets := 0
	for k := range te.keyring.items {
		if strings.Contains(k, "#device#") {
			pendingSecrets++
		}
	}
	for _, name := range []string{"default", "a", "b"} {
		prof := p.Get(name)
		if prof == nil || prof.Login == nil || prof.Login.Device == nil {
			t.Fatalf("profile %s lost", name)
		}
		slot, err := p.Slot(prof.Issuer, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := te.keyring.items["gatherdeal-cli|"+slot.DeviceCodeAccount(prof.Login.Generation)]; !ok {
			t.Fatalf("profile %s points at a device code that is not stored", name)
		}
	}
	if pendingSecrets != 3 {
		t.Fatalf("%d pending device codes stored, want 3 (superseded ones removed)", pendingSecrets)
	}
}
