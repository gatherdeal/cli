package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Redacted replaces every credential the CLI finds in its output.
const Redacted = "[REDACTED]"

// credentialBodyLength is the length of the [a-z0-9] body of a
// delegated-access token (gd_dat_) and of a device code (gd_dcd_).
const credentialBodyLength = 50

// credentialCandidate finds prefix+body runs; String keeps only runs whose
// body is exactly credentialBodyLength long and that are not glued to
// other [a-z0-9] characters, so ordinary text such as "gd_dat_team" is
// left alone.
var credentialCandidate = regexp.MustCompile(`gd_(?:dat|dcd)_[a-z0-9]+`)

// Redactor removes credentials from everything the CLI prints. A server
// can echo the bearer it received in a message, in error details or in a
// success payload; the redactor runs on every string before output.
type Redactor struct {
	mu     sync.Mutex
	secret []string
}

// Add registers an exact secret value (a token or device code).
func (r *Redactor) Add(secret string) {
	if r == nil || len(secret) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secret {
		if s == secret {
			return
		}
	}
	r.secret = append(r.secret, secret)
}

// String redacts s: every registered secret (matched without regard to
// ASCII case) and every string in the exact credential formats.
func (r *Redactor) String(s string) string {
	if r != nil {
		r.mu.Lock()
		for _, sec := range r.secret {
			s = replaceFold(s, sec)
		}
		r.mu.Unlock()
	}
	return replaceFormats(s)
}

// asciiLower lowers A-Z only, so byte offsets stay valid for any input.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func replaceFold(s, secret string) string {
	lowerSecret := asciiLower(secret)
	var b strings.Builder
	for {
		i := strings.Index(asciiLower(s), lowerSecret)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(Redacted)
		s = s[i+len(secret):]
	}
}

func isAlnum(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

func replaceFormats(s string) string {
	matches := credentialCandidate.FindAllStringIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		bodyLen := m[1] - m[0] - len("gd_dat_")
		if bodyLen != credentialBodyLength || (m[0] > 0 && isAlnum(s[m[0]-1])) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(Redacted)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// value redacts every string (and object key) inside v by round-tripping
// it through JSON. This also canonicalizes raw JSON from the server, so an
// escaped spelling such as "gd_dat_…" cannot slip through.
func (r *Redactor) value(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return r.walk(generic), nil
}

func (r *Redactor) walk(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case []any:
		for i := range t {
			t[i] = r.walk(t[i])
		}
		return t
	case map[string]any:
		// Keys are kept as they are unless they contain a credential. Such
		// keys are renamed deterministically (sorted order, numbered on
		// collision), so no value is ever merged away.
		out := make(map[string]any, len(t))
		var renamed []string
		for k, val := range t {
			if r.String(k) == k {
				out[k] = r.walk(val)
			} else {
				renamed = append(renamed, k)
			}
		}
		sort.Strings(renamed)
		for _, k := range renamed {
			nk := r.String(k)
			for n := 2; ; n++ {
				if _, taken := out[nk]; !taken {
					break
				}
				nk = r.String(k) + "#" + strconv.Itoa(n)
			}
			out[nk] = r.walk(t[k])
		}
		return out
	default:
		return v
	}
}

// Envelope returns a copy of env with every credential redacted.
func (r *Redactor) Envelope(env Envelope) (Envelope, error) {
	data, err := r.value(env.Data)
	if err != nil {
		return env, err
	}
	env.Data = data
	env.Hint = r.String(env.Hint)
	m := env.Meta
	m.Server, m.Profile, m.CorrelationID = r.String(m.Server), r.String(m.Profile), r.String(m.CorrelationID)
	if m.PageInfo != nil {
		pi := *m.PageInfo
		pi.NextCursor, pi.PrevCursor = r.String(pi.NextCursor), r.String(pi.PrevCursor)
		m.PageInfo = &pi
	}
	env.Meta = m
	if env.Error != nil {
		e := *env.Error
		e.Code, e.Message, e.Detail, e.CorrelationID = r.String(e.Code), r.String(e.Message), r.String(e.Detail), r.String(e.CorrelationID)
		if e.Details, err = r.value(e.Details); err != nil {
			return env, err
		}
		if e.Problem, err = r.value(e.Problem); err != nil {
			return env, err
		}
		env.Error = &e
	}
	return env, nil
}

// Terminal makes s safe to print on a terminal: credentials are redacted
// and control characters (C0, DEL, C1, bidirectional overrides, invalid
// UTF-8) are written as visible escapes, so a server-supplied string cannot
// move the cursor, set the title, write the clipboard or hide text.
func (r *Redactor) Terminal(s string) string { return r.terminal(s, false) }

// Text is Terminal for multi-line text output (a raw --jq string): line
// feeds and tabs stay as they are.
func (r *Redactor) Text(s string) string { return r.terminal(s, true) }

func (r *Redactor) terminal(s string, keepLines bool) string {
	s = r.String(s)
	var b strings.Builder
	for i := 0; i < len(s); {
		c, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case c == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case keepLines && (c == '\n' || c == '\t'):
			b.WriteRune(c)
		case isTerminalControl(c):
			if c < 0x100 {
				fmt.Fprintf(&b, `\x%02x`, c)
			} else {
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		default:
			b.WriteRune(c)
		}
		i += size
	}
	return b.String()
}

func isTerminalControl(c rune) bool {
	switch {
	case c < 0x20, c == 0x7f, c >= 0x80 && c <= 0x9f:
		return true
	case c >= 0x202a && c <= 0x202e, c >= 0x2066 && c <= 0x2069, c == 0x200e, c == 0x200f, c == 0x061c:
		return true
	case c == 0x2028, c == 0x2029:
		return true
	}
	return false
}

// EscapeJSONControls rewrites characters the JSON encoder leaves raw but
// terminals may interpret (DEL, C1 controls, bidirectional overrides).
// They can only occur inside JSON strings, so a byte-level rewrite is safe.
func EscapeJSONControls(b []byte) []byte {
	if bytes.IndexFunc(b, func(c rune) bool { return c == 0x7f || (c >= 0x80 && isTerminalControl(c)) }) < 0 {
		return b
	}
	var out bytes.Buffer
	for i := 0; i < len(b); {
		c, size := utf8.DecodeRune(b[i:])
		if c == 0x7f || (c >= 0x80 && c != utf8.RuneError && isTerminalControl(c)) {
			fmt.Fprintf(&out, `\u%04x`, c)
		} else {
			out.Write(b[i : i+size])
		}
		i += size
	}
	return out.Bytes()
}

// StripControls removes what a terminal could interpret from text the
// server wrote (a problem's title and detail): ANSI escape sequences (CSI,
// OSC and other ESC sequences, and their C1 forms) are dropped whole, other
// C0/C1 controls, DEL and bidirectional controls are dropped, and tab, CR
// and LF become spaces. Invalid UTF-8 becomes U+FFFD.
func StripControls(s string) string {
	rs := []rune(strings.ToValidUTF8(s, "\uFFFD"))
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == 0x1b || c == 0x9b || isC1String(c):
			i = escapeEnd(rs, i)
		case c == '\t' || c == '\n' || c == '\r':
			b.WriteByte(' ')
		case isTerminalControl(c):
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// isC1String reports the C1 introducers of string sequences: DCS, SOS,
// OSC, PM and APC.
func isC1String(c rune) bool {
	return c == 0x90 || c == 0x98 || c == 0x9d || c == 0x9e || c == 0x9f
}

// escapeEnd returns the index of the last rune of the escape sequence that
// starts at rs[i] (ESC, CSI or a C1 string introducer).
func escapeEnd(rs []rune, i int) int {
	switch c := rs[i]; {
	case c == 0x9b:
		return csiEnd(rs, i+1)
	case isC1String(c):
		return stringEnd(rs, i+1)
	}
	if i+1 >= len(rs) {
		return i
	}
	switch next := rs[i+1]; {
	case next == '[':
		return csiEnd(rs, i+2)
	case next == ']' || next == 'P' || next == 'X' || next == '^' || next == '_':
		return stringEnd(rs, i+2)
	case next >= 0x20 && next <= 0x7e:
		return i + 1
	}
	return i
}

// csiEnd skips CSI parameter and intermediate bytes up to the final byte.
func csiEnd(rs []rune, j int) int {
	for ; j < len(rs); j++ {
		switch c := rs[j]; {
		case c >= 0x20 && c <= 0x3f:
		case c >= 0x40 && c <= 0x7e:
			return j
		default:
			// Malformed: drop the introducer only.
			return j - 1
		}
	}
	return len(rs) - 1
}

// stringEnd skips a control string up to its terminator (BEL, ST or ESC \).
func stringEnd(rs []rune, j int) int {
	for ; j < len(rs); j++ {
		switch rs[j] {
		case 0x07, 0x9c:
			return j
		case 0x1b:
			if j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
	}
	return len(rs) - 1
}
