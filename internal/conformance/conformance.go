// Package conformance loads the shared problem decode fixtures the public
// bundle ships under contracts/testdata/problem-conformance, so every
// decoder of the CLI runs the same cases as the server-side clients. Only
// tests import it.
package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/output"
)

// Dir is the fixture directory relative to the repository root.
const Dir = "contracts/testdata/problem-conformance"

// Expected kinds of a case.
const (
	KindSuccess  = "success"
	KindProblem  = "problem"
	KindStatus   = "status"
	KindProtocol = "protocol"
)

// Case is one fixture: a raw HTTP response and the decode it must produce.
type Case struct {
	Name        string `json:"-"`
	Description string `json:"description"`
	Response    struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"response"`
	Expect struct {
		Kind         string `json:"kind"`
		Status       int    `json:"status"`
		Code         string `json:"code"`
		Retryable    bool   `json:"retryable"`
		RetryAfterMS int64  `json:"retry_after_ms"`
	} `json:"expect"`
}

// Header returns the response headers.
func (c Case) Header() http.Header {
	h := http.Header{}
	for k, v := range c.Response.Headers {
		h.Set(k, v)
	}
	return h
}

// HTTPResponse builds the *http.Response of the case.
func (c Case) HTTPResponse(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: c.Response.Status,
		Header:     c.Header(),
		Body:       io.NopCloser(strings.NewReader(c.Response.Body)),
		Request:    req,
	}
}

// WantExit is the CLI exit code the case must map to.
func (c Case) WantExit() output.ExitCode {
	switch c.Expect.Kind {
	case KindSuccess:
		return output.ExitOK
	case KindProtocol:
		return output.ExitUnclassified
	}
	return output.ExitForStatus(c.Expect.Status)
}

// Load reads every fixture, sorted by file name.
func Load(t testing.TB) []Case {
	t.Helper()
	dir := filepath.Join(repoRoot(t), filepath.FromSlash(Dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var cases []Case
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var c Case
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		c.Name = strings.TrimSuffix(e.Name(), ".json")
		switch c.Expect.Kind {
		case KindSuccess, KindProblem, KindStatus, KindProtocol:
		default:
			t.Fatalf("%s: unknown expect.kind %q", e.Name(), c.Expect.Kind)
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	if len(cases) < 10 {
		t.Fatalf("only %d conformance fixtures in %s", len(cases), Dir)
	}
	return cases
}

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
