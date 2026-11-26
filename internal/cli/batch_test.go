package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	batchOutlet  = "33333333-3333-3333-3333-333333333333"
	batchTag     = "55555555-5555-4555-8555-555555555555"
	batchAddPath = "/crm/v1/contact-tags/batch-add"
)

// batchContact is the contact ID of input item i.
func batchContact(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }

// membershipLines renders n batch-add items as JSON Lines.
func membershipLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "{\"contact_id\":%q,\"tag_id\":%q}\n", batchContact(i), batchTag)
	}
	return b.String()
}

type batchSend struct {
	key   string
	body  string
	items []map[string]string
}

// batchServer records every batch-add send; answer decides the response.
func batchServer(t *testing.T, f *fakeServer, answer func(n int, s batchSend, w http.ResponseWriter)) *[]batchSend {
	var sends []batchSend
	f.crm = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != batchAddPath || r.URL.Query().Get("outlet_id") != batchOutlet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		raw, _ := io.ReadAll(r.Body)
		s := batchSend{key: r.Header.Get("Idempotency-Key"), body: string(raw)}
		var body struct {
			Items []map[string]string `json:"items"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body: %v", err)
		}
		s.items = body.Items
		sends = append(sends, s)
		answer(len(sends), s, w)
	}
	return &sends
}

func addedReceipt(n int) string {
	results := make([]string, n)
	for i := range results {
		results[i] = `{"outcome":"added"}`
	}
	return `{"status":"success","results":[` + strings.Join(results, ",") + `]}`
}

func rejection(complete bool, indices ...int) string {
	items := make([]map[string]any, len(indices))
	for i, index := range indices {
		items[i] = map[string]any{"index": index, "type": "https://docs.gatherdeal.ai/errors/CRM_CONTACT_NOT_FOUND",
			"title": "Contact not found.", "status": 404, "code": "CRM_CONTACT_NOT_FOUND", "retryable": false}
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(problemJSON(422, "BATCH_REJECTED", "The batch was rejected.", map[string]any{"complete": complete})), &p)
	p["errors"] = items
	b, _ := json.Marshal(p)
	return string(b)
}

func batchRows(t *testing.T, res result) []map[string]any {
	t.Helper()
	raw, ok := res.env["data"].([]any)
	if !ok {
		t.Fatalf("data is not a list: %s", res.stdout)
	}
	rows := make([]map[string]any, len(raw))
	for i, r := range raw {
		rows[i] = r.(map[string]any)
		if int(rows[i]["index"].(float64)) != i {
			t.Fatalf("row %d has index %v", i, rows[i]["index"])
		}
	}
	return rows
}

func states(rows []map[string]any) map[string][]int {
	out := map[string][]int{}
	for i, r := range rows {
		out[r["state"].(string)] = append(out[r["state"].(string)], i)
	}
	return out
}

func span(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func batchArgs(extra ...string) []string {
	return append([]string{"crm", "contact-tags", "batch-add", "--outlet", batchOutlet, "--file", "-", "--yes"}, extra...)
}

func TestBatchChunksInOrderWithDerivedKeys(t *testing.T) {
	f, te := loggedIn(t)
	sends := batchServer(t, f, func(_ int, s batchSend, w http.ResponseWriter) {
		writeJSON(w, 200, addedReceipt(len(s.items)))
	})
	te.stdin = membershipLines(205)
	res := te.run(batchArgs("--idempotency-key", "import-2026-11")...)
	if res.exit != 0 || len(*sends) != 3 {
		t.Fatal(res.stdout, res.stderr)
	}
	next := 0
	for n, s := range *sends {
		if want := fmt.Sprintf("import-2026-11.%d", n); s.key != want {
			t.Errorf("chunk %d key %q, want %q", n, s.key, want)
		}
		if want := []int{100, 100, 5}[n]; len(s.items) != want {
			t.Errorf("chunk %d has %d items, want %d", n, len(s.items), want)
		}
		for _, item := range s.items {
			if item["contact_id"] != batchContact(next) || item["tag_id"] != batchTag {
				t.Fatalf("chunk %d out of order at input %d: %v", n, next, item)
			}
			next++
		}
	}
	rows := batchRows(t, res)
	if len(rows) != 205 || !slices.Equal(states(rows)["succeeded"], span(0, 204)) || rows[204]["data"].(map[string]any)["outcome"] != "added" {
		t.Fatal(res.stdout)
	}
	if meta := res.env["meta"].(map[string]any)["write"].(map[string]any); meta["idempotency_key"] != "import-2026-11" {
		t.Fatal(meta)
	}
	if !strings.Contains(res.stderr, "idempotency key import-2026-11 (chunk n is sent as import-2026-11.<n>)") ||
		!strings.Contains(res.stderr, "chunk 2 (items 200-204, key import-2026-11.2): committed") {
		t.Fatal(res.stderr)
	}
	// One line per item with --jq.
	te.stdin = membershipLines(3)
	res = te.run(batchArgs("--jq", ".[] | {index, state}")...)
	if res.exit != 0 || res.stdout != "{\"index\":0,\"state\":\"succeeded\"}\n{\"index\":1,\"state\":\"succeeded\"}\n{\"index\":2,\"state\":\"succeeded\"}\n" {
		t.Fatal(res.stdout)
	}
	if s := (*sends)[3]; len(s.key) != 34 || !strings.HasSuffix(s.key, ".0") {
		t.Fatalf("generated chunk key %q", s.key)
	}
}

func TestBatchInputForms(t *testing.T) {
	f, te := loggedIn(t)
	sends := batchServer(t, f, func(_ int, s batchSend, w http.ResponseWriter) {
		writeJSON(w, 200, addedReceipt(len(s.items)))
	})
	dir := t.TempDir()
	array := filepath.Join(dir, "items.json")
	items := fmt.Sprintf("[\n  {\"contact_id\":%q,\"tag_id\":%q},\n  {\"contact_id\":%q,\"tag_id\":%q}\n]\n", batchContact(0), batchTag, batchContact(1), batchTag)
	lines := filepath.Join(dir, "items.jsonl")
	if os.WriteFile(array, []byte(items), 0o600) != nil || os.WriteFile(lines, []byte("\r\n"+strings.ReplaceAll(membershipLines(2), "\n", "\r\n\n")), 0o600) != nil {
		t.Fatal("write fixtures")
	}
	for _, path := range []string{array, lines} {
		args := []string{"crm", "contact-tags", "batch-add", "--outlet", batchOutlet, "--file", path, "--yes"}
		if res := te.run(args...); res.exit != 0 || len(batchRows(t, res)) != 2 {
			t.Fatal(path, res.stdout)
		}
	}
	if len(*sends) != 2 || (*sends)[0].body != (*sends)[1].body {
		t.Fatalf("array and JSON Lines input differ: %v", *sends)
	}
	for _, tc := range []struct{ stdin, want string }{
		{"", "--file holds no items"},
		{"\n \n", "--file holds no items"},
		{"[]", "--file holds no items"},
		{`{"contact_id":"` + batchContact(0) + `","tag_id":"` + batchTag + `"} {}`, "line 1 (item 0) must be one JSON object"},
		{"[1]", "item 0 must be a JSON object"},
		{"[{}] []", "one JSON array or one JSON value per line"},
		{membershipLines(1) + "[]\n", "line 2 (item 1) must be one JSON object"},
		{membershipLines(2) + `{"contact_id":"` + batchContact(2) + `"}` + "\n", "invalid --file item 2"},
		{`{"contact_id":"` + batchContact(0) + `","tag_id":"` + batchTag + `","note":"x"}`, "invalid --file item 0"},
		{`{"contact_id":"not-a-uuid","tag_id":"` + batchTag + `"}`, "invalid --file item 0"},
	} {
		te.stdin = tc.stdin
		res := te.run(batchArgs()...)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" || !strings.Contains(res.stderr, tc.want) {
			t.Errorf("%q: exit %d: %s", tc.stdin, res.exit, res.stderr)
		}
	}
	if res := te.run("crm", "contact-tags", "batch-add", "--outlet", batchOutlet, "--yes"); res.exit != 2 || !strings.Contains(res.stderr, "--file is required") {
		t.Fatal(res.stderr)
	}
	if len(*sends) != 2 {
		t.Fatalf("invalid input was sent: %d sends", len(*sends))
	}
}

func TestBatchRejectionMapsIndices(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint("complete=", complete), func(t *testing.T) {
			f, te := loggedIn(t)
			sends := batchServer(t, f, func(n int, s batchSend, w http.ResponseWriter) {
				if n == 2 {
					writeJSON(w, 422, rejection(complete, 3, 7))
					return
				}
				writeJSON(w, 200, addedReceipt(len(s.items)))
			})
			te.stdin = membershipLines(250)
			res := te.run(batchArgs()...)
			if res.exit != 10 || res.errorCode() != "CLI_BATCH_INCOMPLETE" || len(*sends) != 3 {
				t.Fatal(res.exit, res.stderr)
			}
			rows := batchRows(t, res)
			got := states(rows)
			uncommitted := append(append(span(100, 102), span(104, 106)...), span(108, 199)...)
			if !slices.Equal(got["succeeded"], append(span(0, 99), span(200, 249)...)) || !slices.Equal(got["errored"], []int{103, 107}) || !slices.Equal(got["uncommitted"], uncommitted) {
				t.Fatal(got)
			}
			if p := rows[103]["problem"].(map[string]any); p["code"] != "CRM_CONTACT_NOT_FOUND" || p["index"].(float64) != 3 {
				t.Fatal(p)
			}
			details := res.env["error"].(map[string]any)["details"].(map[string]any)
			chunks := details["chunks"].([]any)
			second, third := chunks[1].(map[string]any), chunks[2].(map[string]any)
			if second["state"] != "refused" || second["complete"] != complete || second["error"].(map[string]any)["code"] != "BATCH_REJECTED" ||
				third["state"] != "committed" || third["first_index"].(float64) != 200 || third["idempotency_key"] == "" {
				t.Fatal(chunks)
			}
			if details["succeeded"].(float64) != 150 || details["errored"].(float64) != 2 || details["uncommitted"].(float64) != 98 {
				t.Fatal(details)
			}

			// --stop-on-error leaves the later chunks unsent.
			*sends = nil
			res = te.run(batchArgs("--stop-on-error")...)
			got = states(batchRows(t, res))
			chunks = res.env["error"].(map[string]any)["details"].(map[string]any)["chunks"].([]any)
			if res.exit != 10 || len(*sends) != 2 || !slices.Equal(got["succeeded"], span(0, 99)) || !slices.Contains(got["uncommitted"], 249) ||
				chunks[2].(map[string]any)["state"] != "not_sent" || !strings.Contains(res.stderr, "Stopped after chunk 1 (--stop-on-error); chunks 2-2 were not sent") {
				t.Fatal(res.exit, got, res.stderr)
			}
		})
	}
}

func TestBatchFirstChunkRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		exit   int
		code   string
		sends  int
	}{
		{"rejected", 422, rejection(true, 1), 6, "BATCH_REJECTED", 2},
		{"too large", 413, "Request Entity Too Large", 6, "CLI_UPSTREAM_ERROR", 2},
		{"rate limited", 429, problemJSON(429, "RATE_LIMITED", "Too many requests.", nil), 7, "RATE_LIMITED", 6},
		{"denied", 403, problemJSON(403, "FORBIDDEN", "Forbidden.", nil), 4, "FORBIDDEN", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			sends := batchServer(t, f, func(_ int, _ batchSend, w http.ResponseWriter) {
				if tc.status == 413 {
					w.Header().Set("Content-Type", "text/plain")
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				writeJSON(w, tc.status, tc.body)
			})
			te.stdin, te.sleeps = membershipLines(150), nil
			res := te.run(batchArgs()...)
			// Every chunk is attempted; only a 429 is resent, unchanged.
			if res.exit != tc.exit || len(*sends) != tc.sends || len(te.sleeps) != tc.sends-2 {
				t.Fatal(res.exit, len(*sends), te.sleeps, res.stdout)
			}
			for i, s := range *sends {
				if first := (*sends)[i/(tc.sends/2)*(tc.sends/2)]; s.key != first.key || s.body != first.body {
					t.Fatalf("send %d changed its chunk's request", i)
				}
			}
			if tc.code != "CLI_UPSTREAM_ERROR" && res.errorCode() != tc.code {
				t.Fatal(res.stdout)
			}
			got := states(batchRows(t, res))
			want := span(0, 149)
			if tc.name == "rejected" {
				want = slices.Delete(slices.Delete(want, 101, 102), 1, 2)
				if !slices.Equal(got["errored"], []int{1, 101}) {
					t.Fatal(got)
				}
			}
			if !slices.Equal(got["uncommitted"], want) {
				t.Fatal(got)
			}
		})
	}
}

func TestBatchUnknownOutcomeResendsIdenticalRequest(t *testing.T) {
	f, te := loggedIn(t)
	sends := batchServer(t, f, func(n int, s batchSend, w http.ResponseWriter) {
		switch n {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			writeJSON(w, 503, problemJSON(503, "SERVICE_UNAVAILABLE", "Unavailable.", nil))
		default:
			writeJSON(w, 200, addedReceipt(len(s.items)))
		}
	})
	te.stdin, te.sleeps = membershipLines(120), nil
	res := te.run(batchArgs("--idempotency-key", "retry-key-1")...)
	if res.exit != 0 || len(*sends) != 4 {
		t.Fatal(res.exit, len(*sends), res.stdout)
	}
	first := (*sends)[0]
	for i, s := range (*sends)[:3] {
		if s.key != "retry-key-1.0" || s.body != first.body {
			t.Fatalf("resend %d changed the request: %q", i, s.key)
		}
	}
	if (*sends)[3].key != "retry-key-1.1" || !slices.Equal(te.sleeps, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Fatal((*sends)[3].key, te.sleeps)
	}
}

func TestBatchRateLimitResendsAfterRetryDelay(t *testing.T) {
	f, te := loggedIn(t)
	limited := func(ms int) string {
		var p map[string]any
		_ = json.Unmarshal([]byte(problemJSON(429, "RATE_LIMITED", "Too many requests.", nil)), &p)
		if ms > 0 {
			p["retry_after_ms"] = ms
		}
		b, _ := json.Marshal(p)
		return string(b)
	}
	sends := batchServer(t, f, func(n int, s batchSend, w http.ResponseWriter) {
		switch n {
		case 1:
			w.Header().Set("Retry-After", "3")
			writeJSON(w, 429, limited(0))
		case 2:
			writeJSON(w, 429, limited(2000))
		default:
			writeJSON(w, 200, addedReceipt(len(s.items)))
		}
	})
	te.stdin, te.sleeps = membershipLines(2), nil
	res := te.run(batchArgs("--idempotency-key", "limited-key")...)
	if res.exit != 0 || len(*sends) != 3 || !slices.Equal(te.sleeps, []time.Duration{3 * time.Second, 2 * time.Second}) {
		t.Fatal(res.exit, len(*sends), te.sleeps, res.stdout)
	}
	for _, s := range *sends {
		if s.key != "limited-key.0" || s.body != (*sends)[0].body {
			t.Fatal("a rate-limited chunk was changed")
		}
	}
}

func TestBatchUnknownOutcomeStops(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []func(w http.ResponseWriter)
		sends   int
	}{
		{"server errors", []func(w http.ResponseWriter){
			func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
		}, 3},
		{"key conflict after an unknown outcome", []func(w http.ResponseWriter){
			func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
			func(w http.ResponseWriter) {
				writeJSON(w, 409, problemJSON(409, "IDEMPOTENCY_KEY_CONFLICT", "Conflict.", nil))
			},
		}, 2},
		{"refusal after an unknown outcome", []func(w http.ResponseWriter){
			func(w http.ResponseWriter) { w.WriteHeader(http.StatusGatewayTimeout) },
			func(w http.ResponseWriter) { writeJSON(w, 422, rejection(true, 0)) },
		}, 2},
		{"receipt with missing results", []func(w http.ResponseWriter){
			func(w http.ResponseWriter) { writeJSON(w, 200, addedReceipt(1)) },
		}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			attempts := 0
			sends := batchServer(t, f, func(_ int, s batchSend, w http.ResponseWriter) {
				if strings.HasSuffix(s.key, ".1") {
					writeJSON(w, 200, addedReceipt(len(s.items)))
					return
				}
				attempts++
				tc.answers[min(attempts, len(tc.answers))-1](w)
			})
			te.stdin = membershipLines(150)
			res := te.run(batchArgs()...)
			if res.exit != 8 || res.errorCode() != "CLI_WRITE_OUTCOME_UNKNOWN" || len(*sends) != tc.sends+1 {
				t.Fatal(res.exit, len(*sends), res.stdout)
			}
			for _, s := range (*sends)[:tc.sends] {
				if s.key != (*sends)[0].key || s.body != (*sends)[0].body {
					t.Fatal("an unresolved chunk was changed")
				}
			}
			got := states(batchRows(t, res))
			if !slices.Equal(got["unknown"], span(0, 99)) || !slices.Equal(got["succeeded"], span(100, 149)) {
				t.Fatal(got)
			}
			chunk := res.env["error"].(map[string]any)["details"].(map[string]any)["chunks"].([]any)[0].(map[string]any)
			if chunk["state"] != "unknown" || int(chunk["attempts"].(float64)) != tc.sends {
				t.Fatal(chunk)
			}
		})
	}
}

func TestBatchKeyConflict(t *testing.T) {
	conflict := problemJSON(409, "IDEMPOTENCY_KEY_CONFLICT", "Idempotency key conflict.", nil)
	for _, tc := range []struct {
		name      string
		committed bool // the second chunk commits
		exit      int
		code      string
	}{
		{"every chunk conflicts", false, 6, "IDEMPOTENCY_KEY_CONFLICT"},
		{"conflict beside a committed chunk", true, 6, "IDEMPOTENCY_KEY_CONFLICT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			sends := batchServer(t, f, func(_ int, s batchSend, w http.ResponseWriter) {
				if tc.committed && strings.HasSuffix(s.key, ".1") {
					writeJSON(w, 200, addedReceipt(len(s.items)))
					return
				}
				writeJSON(w, 409, conflict)
			})
			te.stdin, te.sleeps = membershipLines(150), nil
			res := te.run(batchArgs("--idempotency-key", "reused-key")...)
			if res.exit != tc.exit || res.errorCode() != tc.code || len(*sends) != 2 || len(te.sleeps) != 0 {
				t.Fatal(res.exit, len(*sends), res.stdout)
			}
			e := res.env["error"].(map[string]any)
			if e["http_status"].(float64) != 409 || !strings.Contains(res.env["hint"].(string), "already used with a different input") ||
				!strings.Contains(res.env["hint"].(string), "use a new --idempotency-key") || strings.Contains(res.env["hint"].(string), "Rerun with the same --file") {
				t.Fatal(res.stdout)
			}
			if d := e["details"].(map[string]any); d["unknown"].(float64) == 0 || d["chunks"].([]any)[0].(map[string]any)["state"] != "unknown" {
				t.Fatal(d)
			}
			rows := batchRows(t, res)
			got := states(rows)
			unknown := span(0, 149)
			if tc.committed {
				unknown = span(0, 99)
			}
			if !slices.Equal(got["unknown"], unknown) || rows[0]["problem"].(map[string]any)["code"] != "IDEMPOTENCY_KEY_CONFLICT" {
				t.Fatal(got)
			}
			if tc.committed && (!slices.Equal(got["succeeded"], span(100, 149)) || rows[100]["problem"] != nil) {
				t.Fatal(got)
			}
		})
	}

	// A conflict beside an unknown transport outcome keeps the general unknown error.
	f, te := loggedIn(t)
	batchServer(t, f, func(_ int, s batchSend, w http.ResponseWriter) {
		if strings.HasSuffix(s.key, ".1") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, 409, conflict)
	})
	te.stdin = membershipLines(150)
	if res := te.run(batchArgs()...); res.exit != 8 || res.errorCode() != "CLI_WRITE_OUTCOME_UNKNOWN" {
		t.Fatal(res.stdout)
	}
}

func TestBatchPreviewAndKeyLimits(t *testing.T) {
	f, te := loggedIn(t)
	sends := batchServer(t, f, func(int, batchSend, http.ResponseWriter) {})
	te.stdin = membershipLines(101)
	res := te.run("crm", "contact-tags", "batch-add", "--outlet", batchOutlet, "--file", "-", "--dry-run", "--idempotency-key", "preview-key")
	if res.exit != 0 || len(*sends) != 0 {
		t.Fatal(res.stdout)
	}
	chunks := res.data()["chunks"].([]any)
	last := chunks[1].(map[string]any)
	if len(chunks) != 2 || last["idempotency_key"] != "preview-key.1" || last["first_index"].(float64) != 100 ||
		len(last["body"].(map[string]any)["items"].([]any)) != 1 || res.data()["outlet"] != batchOutlet {
		t.Fatal(res.stdout)
	}
	te.stdin = membershipLines(1)
	if res := te.run("crm", "contact-tags", "batch-add", "--outlet", batchOutlet, "--file", "-"); res.exit != 9 || len(*sends) != 0 {
		t.Fatal(res.stdout)
	}
	te.stdin = membershipLines(1)
	if res := te.run(batchArgs("--idempotency-key", strings.Repeat("k", 127))...); res.exit != 2 || !strings.Contains(res.stderr, "--idempotency-key is too long") || len(*sends) != 0 {
		t.Fatal(res.stderr)
	}
}

func TestBatchCommandsUseTheirRoutes(t *testing.T) {
	for _, tc := range []struct {
		command, path, item, result string
	}{
		{"contacts batch-create", "/crm/v1/contacts/batch-create", `{"kind":"personal","name":"Amina"}`, `{"id":"` + batchTag + `"}`},
		{"contact-logs batch-create", "/crm/v1/contact-logs/batch-create", `{"contact_id":"` + batchContact(1) + `","content":"Called"}`, `{"id":"` + batchTag + `"}`},
		{"contact-tags batch-create", "/crm/v1/contact-tags/batch-create", `{"name":"VIP"}`, `{"id":"` + batchTag + `"}`},
		{"contact-tags batch-remove", "/crm/v1/contact-tags/batch-remove", `{"contact_id":"` + batchContact(1) + `","tag_id":"` + batchTag + `"}`, `{"outcome":"removed"}`},
	} {
		t.Run(tc.command, func(t *testing.T) {
			f, te := loggedIn(t)
			var paths []string
			f.crm = func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("missing key")
				}
				writeJSON(w, 200, `{"status":"success","results":[`+tc.result+`]}`)
			}
			te.stdin = tc.item + "\n"
			args := append(append([]string{"crm"}, strings.Fields(tc.command)...), "--file", "-", "--yes")
			if tc.command != "contact-tags batch-create" {
				args = append(args, "--outlet", batchOutlet)
			}
			res := te.run(args...)
			if res.exit != 0 || !slices.Equal(paths, []string{tc.path}) || batchRows(t, res)[0]["state"] != "succeeded" {
				t.Fatal(res.stdout, paths)
			}
		})
	}
}

func TestBatchHelp(t *testing.T) {
	te := newTestEnv(t, nil)
	for _, name := range []string{"contacts batch-create", "contact-tags batch-add"} {
		res := te.run(append([]string{"crm"}, append(strings.Fields(name), "--help")...)...)
		if res.exit != 0 {
			t.Fatal(res.stdout)
		}
		compareGolden(t, "help_crm_"+strings.ReplaceAll(name, " ", "_")+".golden.txt", res.stdout)
	}
}

func TestContactTagCreate(t *testing.T) {
	f, te := loggedIn(t)
	var keys []string
	f.crm = func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/crm/v1/contact-tags" || string(raw) != `{"name":"VIP"}` {
			t.Errorf("unexpected request: %s %s %s", r.Method, r.URL, raw)
		}
		writeJSON(w, http.StatusCreated, `{"status":"success","id":"`+batchTag+`"}`)
	}
	res := te.run("crm", "contact-tags", "create", "--input", `{"name":"VIP"}`, "--yes", "--idempotency-key", "tag-key-1")
	if res.exit != 0 || res.data()["id"] != batchTag || !slices.Equal(keys, []string{"tag-key-1"}) {
		t.Fatal(res.stdout, keys)
	}
	if res := te.run("crm", "contact-tags", "create", "--input", `{"name":""}`, "--yes"); res.exit != 2 || len(keys) != 1 {
		t.Fatal(res.stdout)
	}
}
