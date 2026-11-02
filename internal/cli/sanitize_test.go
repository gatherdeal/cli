package cli

import (
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
)

func TestServerEchoedCredentialRedacted(t *testing.T) {
	fake, te := loggedIn(t)
	tok := fake.lastToken()
	fake.echoAuth = true
	for _, args := range [][]string{
		{"auth", "status"},
		{"crm", "contacts", "get", "55555555-5555-5555-5555-555555555555"},
	} {
		res := te.run(args...) // run() fails the test if the token appears
		out := res.stdout + res.stderr
		if !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("%v: expected a redaction marker:\n%s", args, out)
		}
		if strings.Contains(out, tok[len(server.TokenPrefix):]) {
			t.Fatalf("%v: token body leaked", args)
		}
	}
	// The same holds for GATHERDEAL_TOKEN.
	te2 := newTestEnv(t, fake)
	te2.vars[EnvToken] = tok
	if res := te2.run("auth", "status"); strings.Contains(res.stdout+res.stderr, tok[len(server.TokenPrefix):]) {
		t.Fatal("environment token leaked")
	}
}

func TestTerminalControlsEscapedOnStderr(t *testing.T) {
	fake := newFake(t, server.ProductionOrigin)
	fake.orgName = "Acme\x1b]52;c;ZXZpbA==\x07\r\x1b[2J"
	te := newTestEnv(t, fake)
	res := te.run("auth", "login")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	assertNoControls(t, res.stderr)
	if !strings.Contains(res.stderr, `Acme\x1b]52`) {
		t.Fatalf("org name not shown escaped: %q", res.stderr)
	}
	fake.echoAuth = true
	res = te.run("auth", "status")
	assertNoControls(t, res.stderr)
	assertNoControls(t, strings.TrimSuffix(res.stdout, "\n"))

	// User-supplied arguments are escaped too.
	res = te.run("auth", "\x1b]0;x\x07")
	assertNoControls(t, res.stderr)
}

func assertNoControls(t *testing.T, s string) {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		for _, c := range line {
			if c < 0x20 && c != '\t' || c == 0x7f || (c >= 0x80 && c <= 0x9f) {
				t.Fatalf("raw control %U in %q", c, line)
			}
		}
	}
}
