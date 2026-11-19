package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gatherdeal/cli/internal/conformance"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
)

const testToken = "gd_dat_abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmn"

func originOf(t *testing.T, u string) string {
	t.Helper()
	o, err := server.OriginOfURL(u)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRedirectRefused(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/steal", code)
		}))
		c := New(Options{Origin: originOf(t, srv.URL), Token: testToken})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/oidc/device/token", strings.NewReader("device_code=x"))
		_, err := c.Do(req)
		srv.Close()
		if !errors.Is(err, ErrRedirect) {
			t.Fatalf("HTTP %d: err = %v, want ErrRedirect", code, err)
		}
		e := TransportError(context.Background(), err)
		if e.Code != output.CodeRedirect || e.Exit != output.ExitUnclassified {
			t.Fatalf("HTTP %d: classified as %s/%d", code, e.Code, e.Exit)
		}
	}
	if targetHits.Load() != 0 {
		t.Fatal("a redirect was followed")
	}
}

func TestOriginBinding(t *testing.T) {
	var gotAuth atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
	}))
	defer other.Close()
	bound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("bound origin did not receive the credential")
		}
	}))
	defer bound.Close()

	c := New(Options{Origin: originOf(t, bound.URL), Token: testToken})
	resp, err := c.Get(bound.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	_, err = c.Get(other.URL + "/x")
	if !errors.Is(err, ErrOriginMismatch) {
		t.Fatalf("err = %v, want ErrOriginMismatch", err)
	}
	if gotAuth.Load() != nil {
		t.Fatal("request reached another origin")
	}
	// A caller-set Authorization header never leaks either.
	c2 := New(Options{Origin: originOf(t, bound.URL)})
	req, _ := http.NewRequest(http.MethodGet, other.URL, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if _, err := c2.Do(req); !errors.Is(err, ErrOriginMismatch) {
		t.Fatalf("err = %v", err)
	}
	if e := TransportError(context.Background(), err); e.Code != output.CodeOriginMismatch {
		t.Fatalf("code = %s", e.Code)
	}
}

func TestNoTokenMeansNoAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected Authorization header")
		}
	}))
	defer srv.Close()
	c := New(Options{Origin: originOf(t, srv.URL)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer something")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestNetworkErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := New(Options{Origin: originOf(t, url)})
	_, err := c.Get(url)
	e := TransportError(context.Background(), err)
	if e.Exit != output.ExitUnavailable || !e.Retryable || e.Code != output.CodeNetwork {
		t.Fatalf("got %+v", e)
	}
}

func problemHeader() http.Header {
	return http.Header{"Content-Type": {ProblemMediaType}, CorrelationHeader: {"corr-1"}}
}

func problem(status int, code string, retryable bool) string {
	b, _ := json.Marshal(map[string]any{
		"type": "https://docs.gatherdeal.ai/errors/" + code, "title": "t", "status": status,
		"code": code, "retryable": retryable,
	})
	return string(b)
}

// The exit code derives from the problem's status, the
// platform table overrides it, and retryability is the problem's own.
func TestDecodeAPIProblemExitCodes(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		retryable bool
		want      output.ExitCode
	}{
		{400, crmv1.CodeBadRequest, false, output.ExitUsage},
		{400, crmv1.CodePaginationCursorInvalid, false, output.ExitUsage},
		{401, crmv1.CodeDelegatedAccessInactive, false, output.ExitAuth},
		{403, crmv1.CodeDelegatedAccessPermissionDenied, false, output.ExitDenied},
		{404, crmv1.CodeCRMContactNotFound, false, output.ExitNotFound},
		{409, "CRM_CONTACT_CONFLICT", false, output.ExitConflict},
		{412, "PRECONDITION_FAILED", false, output.ExitConflict},
		{422, crmv1.CodeValidationFailed, false, output.ExitConflict},
		{429, "RATE_LIMITED", true, output.ExitRateLimited},
		{500, crmv1.CodeInternalError, false, output.ExitUnavailable},
		{503, "UNAVAILABLE", true, output.ExitUnavailable},
		{426, crmv1.CodeClientUpgradeRequired, false, output.ExitActionRequired},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			_, err := DecodeAPI(tc.status, problemHeader(), []byte(problem(tc.status, tc.code, tc.retryable)))
			var e *output.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.Exit != tc.want || e.Code != tc.code || e.Retryable != tc.retryable || e.HTTPStatus != tc.status || e.CorrelationID != "corr-1" {
				t.Fatalf("got exit=%d code=%s retryable=%v status=%d corr=%q", e.Exit, e.Code, e.Retryable, e.HTTPStatus, e.CorrelationID)
			}
			var p *Problem
			if !errors.As(err, &p) || p.Code != tc.code {
				t.Fatalf("the typed problem is not in the chain: %v", err)
			}
			if tc.code == crmv1.CodeClientUpgradeRequired && !strings.Contains(e.Hint, "gatherdeal update") {
				t.Fatalf("hint = %q", e.Hint)
			}
		})
	}
}

// Title and detail are kept as received (control characters stripped),
// the body's correlation id wins over the header and the raw document is
// carried for the JSON output.
func TestDecodeAPIProblemText(t *testing.T) {
	body := `{"type":"https://docs.gatherdeal.ai/errors/CRM_CONTACT_NOT_FOUND","title":"Kenalan tidak dijumpai.\u001b[31m","status":404,` +
		`"detail":"Tiada\nkenalan \u001b]0;pwned\u0007x.","code":"CRM_CONTACT_NOT_FOUND","retryable":false,"correlation_id":"body-id",` +
		`"details":{"contact_id":"c1"},"x_future":1}`
	h := problemHeader()
	h.Set("Content-Language", "ms")
	_, err := DecodeAPI(404, h, []byte(body))
	var e *output.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v", err)
	}
	if e.Message != "Kenalan tidak dijumpai." || e.Detail != "Tiada kenalan x." || e.CorrelationID != "body-id" {
		t.Fatalf("message %q detail %q corr %q", e.Message, e.Detail, e.CorrelationID)
	}
	if !bytes.Equal(e.Problem, []byte(body)) {
		t.Fatalf("problem = %s", e.Problem)
	}
	if string(e.Details.(json.RawMessage)) != `{"contact_id":"c1"}` {
		t.Fatalf("details = %v", e.Details)
	}
	var p *Problem
	if !errors.As(err, &p) || p.ContentLanguage != "ms" {
		t.Fatalf("problem = %+v", p)
	}
}

func TestDecodeAPIWithoutProblem(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		media     string
		body      string
		want      output.ExitCode
		code      string
		retryable bool
	}{
		{"JSON error envelope on 404", 404, "application/json", `{"status":"error","error":{"code":"NOT_FOUND"}}`, output.ExitNotFound, output.CodeUpstream, false},
		{"401 without problem", 401, "", ``, output.ExitAuth, output.CodeUpstream, false},
		{"429 without problem", 429, "text/plain", `slow down`, output.ExitRateLimited, output.CodeUpstream, false},
		{"gateway 504", 504, "text/html", `<html>`, output.ExitUnavailable, output.CodeUpstream, true},
		{"500 without problem", 500, "", ``, output.ExitUnavailable, output.CodeUpstream, false},
		{"2xx error envelope", 200, "application/json", `{"status":"error","error":{"code":"CRM0004"}}`, output.ExitUnclassified, output.CodeProtocol, false},
		{"2xx not json", 200, "text/html", `<html>`, output.ExitUnclassified, output.CodeProtocol, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{CorrelationHeader: {"corr-1"}}
			if tc.media != "" {
				h.Set("Content-Type", tc.media)
			}
			_, err := DecodeAPI(tc.status, h, []byte(tc.body))
			var e *output.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.Exit != tc.want || e.Code != tc.code || e.Retryable != tc.retryable || e.CorrelationID != "corr-1" || len(e.Problem) != 0 {
				t.Fatalf("got exit=%d code=%s retryable=%v corr=%q problem=%s", e.Exit, e.Code, e.Retryable, e.CorrelationID, e.Problem)
			}
		})
	}
}

// DecodeAPI passes the shared decode-contract fixtures of the bundle.
func TestDecodeAPIConformance(t *testing.T) {
	for _, c := range conformance.Load(t) {
		t.Run(c.Name, func(t *testing.T) {
			res, err := DecodeAPI(c.Response.Status, c.Header(), []byte(c.Response.Body))
			if c.Expect.Kind == conformance.KindSuccess {
				if err != nil || res == nil || res.HTTPStatus != c.Expect.Status {
					t.Fatalf("res=%v err=%v", res, err)
				}
				return
			}
			var e *output.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.Exit != c.WantExit() || e.HTTPStatus != c.Expect.Status {
				t.Fatalf("exit %d status %d, want %d %d (%s)", e.Exit, e.HTTPStatus, c.WantExit(), c.Expect.Status, c.Description)
			}
			var p *Problem
			isProblem := errors.As(err, &p)
			switch c.Expect.Kind {
			case conformance.KindProblem:
				if !isProblem || p.Code != c.Expect.Code || p.Retryable != c.Expect.Retryable || p.RetryAfterMS != c.Expect.RetryAfterMS ||
					e.Code != c.Expect.Code || e.Retryable != c.Expect.Retryable || len(e.Problem) == 0 {
					t.Fatalf("problem %+v, error %+v", p, e)
				}
			case conformance.KindStatus:
				if isProblem || e.Code != output.CodeUpstream || e.Retryable != GatewayStatus(c.Expect.Status) {
					t.Fatalf("error %+v", e)
				}
			case conformance.KindProtocol:
				if isProblem || e.Code != output.CodeProtocol || e.Retryable {
					t.Fatalf("error %+v", e)
				}
			}
		})
	}
}
