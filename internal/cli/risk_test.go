package cli

import (
	"strings"
	"testing"
)

func TestAcceptServerRiskIsExact(t *testing.T) {
	fake := newFake(t, "")
	te := newTestEnv(t, fake)
	o := fake.origin // e.g. http://127.0.0.1:41234
	for _, v := range []string{o + "/", " " + o, strings.ToUpper(o), strings.Replace(o, "127.0.0.1", "127.0.0.1", 1) + "#"} {
		if res := te.run("auth", "login", "--server", o, "--accept-server-risk", v); res.exit != 2 {
			t.Errorf("accepted %q: exit %d", v, res.exit)
		}
	}
	if fake.authorizeCalls != 0 {
		t.Fatal("login proceeded")
	}
	if res := te.run("auth", "login", "--server", o, "--accept-server-risk", o); res.exit != 0 {
		t.Fatalf("exact origin refused: %s", res.stdout)
	}
}

func TestAcceptServerRiskCommonFlag(t *testing.T) {
	const origin = "https://chosen.example.com"
	for _, cmd := range Commands() {
		hasTarget := false
		for _, flag := range cmd.Flags {
			hasTarget = hasTarget || flag.Name == "server" || flag.Name == "profile"
		}
		if !hasTarget {
			continue
		}
		t.Run(cmd.Name(), func(t *testing.T) {
			for _, target := range [][]string{{"--server", origin}, {"--profile", "chosen"}} {
				args := append(target, "--accept-server-risk", origin)
				for range cmd.Args {
					args = append(args, "id")
				}
				inv, err := parse(&Env{}, cmd, args)
				if err != nil {
					t.Fatal(err)
				}
				if !inv.isSet("accept-server-risk") || inv.str("accept-server-risk") != origin {
					t.Fatal("risk flag value was not preserved")
				}
			}
		})
	}
}
