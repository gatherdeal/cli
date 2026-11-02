package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const tok = "gd_dat_abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmn"

func TestRedactorPatternsAndExact(t *testing.T) {
	r := &Redactor{}
	r.Add("custom-secret-value")
	device := "gd_dcd_" + strings.Repeat("x", 50)
	for in, want := range map[string]string{
		"Bearer " + tok:           "Bearer " + Redacted,
		"(" + tok + ")":           "(" + Redacted + ")",
		"code " + device + ".":    "code " + Redacted + ".",
		"x custom-secret-value y": "x " + Redacted + " y",
		"x CUSTOM-Secret-Value y": "x " + Redacted + " y",
		"nothing here":            "nothing here",
		// Ordinary text in the credential shape but not its format stays.
		"team gd_dat_team":                  "team gd_dat_team",
		tok + "z":                           tok + "z",
		"a" + tok:                           "a" + tok,
		"gd_dcd_" + strings.Repeat("x", 49): "gd_dcd_" + strings.Repeat("x", 49),
		strings.ToUpper(tok):                strings.ToUpper(tok),
	} {
		if got := r.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
	// A registered secret is matched regardless of ASCII case.
	r.Add(tok)
	if got := r.String(strings.ToUpper(tok)); got != Redacted {
		t.Errorf("upper-case registered secret: %q", got)
	}
}

func TestCredentialFormatPinned(t *testing.T) {
	// Tokens and device codes are the prefix plus 50 characters of [a-z0-9].
	if credentialBodyLength != 50 || len(tok) != len("gd_dat_")+50 {
		t.Fatal("credential format changed")
	}
}

func TestRedactionKeepsKeys(t *testing.T) {
	r := &Redactor{}
	r.Add(tok)
	got, err := r.value(map[string]any{
		"gd_dat_team": "ok", Redacted: "existing", tok: "secret-key", strings.ToUpper(tok): "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"gd_dat_team": "ok", Redacted: "existing", Redacted + "#2": "second", Redacted + "#3": "secret-key"}
	m := got.(map[string]any)
	if len(m) != len(want) {
		t.Fatalf("got %v", m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("got %v, want %v", m, want)
		}
	}
}

func TestEnvelopeRedactsEverywhere(t *testing.T) {
	r := &Redactor{}
	escaped := `"gd_dat_` + tok[len("gd_dat_"):] + `"`
	env := Failure(&Error{
		Code: "X_" + tok, Message: "you sent " + tok, CorrelationID: tok, Exit: ExitAuth, Hint: tok,
		Details: json.RawMessage(`{"echo":` + escaped + `,"nested":[{"` + tok + `":"Bearer ` + tok + `"}]}`),
	}, Meta{CorrelationID: tok})
	var buf bytes.Buffer
	if err := Write(&buf, env, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(buf.String()), "gd_dat_") || strings.Contains(buf.String(), tok[10:]) {
		t.Fatalf("token leaked: %s", buf.String())
	}
	buf.Reset()
	if err := Write(&buf, Success(map[string]any{"contact": json.RawMessage(`{"note":` + escaped + `}`)}, Meta{}), r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), tok[10:]) {
		t.Fatalf("token leaked from success data: %s", buf.String())
	}
}

func TestTerminalEscapes(t *testing.T) {
	r := &Redactor{}
	in := "a\x1b]52;c;ZXZpbA==\x07b\x1b[2Jc\rd\x7fe\u009bf\u202eg\xffh\ni"
	got := r.Terminal(in)
	for _, bad := range []string{"\x1b", "\x07", "\r", "\x7f", "\u009b", "\u202e", "\xff", "\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("Terminal left %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, `\x1b]52`) || !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "i") {
		t.Fatalf("got %q", got)
	}
	if r.Terminal("Acme Sdn Bhd — café 日本") != "Acme Sdn Bhd — café 日本" {
		t.Fatal("printable text altered")
	}
}

func TestJSONOutputHasNoRawControls(t *testing.T) {
	var buf bytes.Buffer
	msg := "x\x1b[31m\x07\r\x7f\u0085\u009b\u202ey"
	if err := Write(&buf, Failure(&Error{Code: "C", Message: msg, Exit: ExitConflict}, Meta{}), nil); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	for _, c := range out {
		if c < 0x20 || isTerminalControl(c) {
			t.Fatalf("raw control %U in %q", c, out)
		}
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back["error"].(map[string]any)["message"] != msg {
		t.Fatalf("JSON escaping changed the value: %q", back["error"].(map[string]any)["message"])
	}
}

func TestStripControls(t *testing.T) {
	for in, want := range map[string]string{
		"Kenalan tidak dijumpai.":             "Kenalan tidak dijumpai.",
		"联系人不存在。":                             "联系人不存在。",
		"red \x1b[31mtext\x1b[0m":             "red text",
		"title\x1b]0;pwned\x07 rest":          "title rest",
		"osc st \x1b]52;c;ZXZpbA==\x1b\\done": "osc st done",
		"c1 csi \u009b2Jx":                    "c1 csi x",
		"c1 osc \u009d0;t\u009cy":             "c1 osc y",
		"line\nbreak\ttab\rcr":                "line break tab cr",
		"bell\x07 nul\x00 del\x7f":            "bell nul del",
		"bidi \u202eevil\u202c":               "bidi evil",
		"two-char \x1bcreset":                 "two-char reset",
		"trailing \x1b":                       "trailing ",
		"bad utf8 \xff":                       "bad utf8 \uFFFD",
	} {
		if got := StripControls(in); got != want {
			t.Errorf("StripControls(%q) = %q, want %q", in, got, want)
		}
	}
}
