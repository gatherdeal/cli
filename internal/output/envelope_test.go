package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func encode(t *testing.T, env Envelope) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, env, nil); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestSuccessEnvelopeShape(t *testing.T) {
	exp := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	total := 2
	got := encode(t, Success([]int{1}, Meta{
		Server: "https://api.example.com", Profile: "default", CorrelationID: "c1",
		PageInfo:            &PageInfo{PageSize: 20, HasPrevPage: true, NextCursor: "n", Page: 2, TotalItems: &total, TotalPages: &total},
		CredentialExpiresAt: KnownTime(&exp), CredentialExpiryWarning: true,
	}))
	want := `{"schema_version":1,"status":"success","data":[1],"meta":{"server":"https://api.example.com","profile":"default","correlation_id":"c1","page_info":{"page_size":20,"has_next_page":false,"has_prev_page":true,"next_cursor":"n","page":2,"total_items":2,"total_pages":2},"credential_expires_at":"2026-12-01T00:00:00Z","credential_expiry_warning":true},"exit_code":0}` + "\n"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestCredentialExpiryTriState(t *testing.T) {
	if got := encode(t, Success(nil, Meta{})); got != `{"schema_version":1,"status":"success","data":null,"meta":{},"exit_code":0}`+"\n" {
		t.Fatalf("unset: %s", got)
	}
	got := encode(t, Success(nil, Meta{CredentialExpiresAt: KnownTime(nil)}))
	if got != `{"schema_version":1,"status":"success","data":null,"meta":{"credential_expires_at":null},"exit_code":0}`+"\n" {
		t.Fatalf("never: %s", got)
	}
}

func TestFailureEnvelopeShape(t *testing.T) {
	err := &Error{
		Code: "CRM_CONTACT_NOT_FOUND", Message: "Contact not found.", Detail: "d", HTTPStatus: 404, CorrelationID: "c2",
		Exit: ExitNotFound, Hint: "h", Details: json.RawMessage(`{"id":"x"}`),
		Problem: json.RawMessage(`{"code":"CRM_CONTACT_NOT_FOUND","status":404}`),
	}
	got := encode(t, Failure(err, Meta{Server: "https://api.example.com"}))
	want := `{"schema_version":1,"status":"error","data":null,"meta":{"server":"https://api.example.com","correlation_id":"c2"},"error":{"code":"CRM_CONTACT_NOT_FOUND","message":"Contact not found.","detail":"d","details":{"id":"x"},"http_status":404,"correlation_id":"c2","retryable":false,"problem":{"code":"CRM_CONTACT_NOT_FOUND","status":404}},"exit_code":5,"hint":"h"}` + "\n"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// A CLI-originated error has no problem member.
	got = encode(t, Failure(Errorf(ExitUsage, CodeUsage, "bad"), Meta{}))
	if want := `{"schema_version":1,"status":"error","data":null,"meta":{},"error":{"code":"CLI_USAGE","message":"bad","retryable":false},"exit_code":2}` + "\n"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// The exit code of a server failure derives from its
// HTTP status.
func TestExitForStatus(t *testing.T) {
	for status, want := range map[int]ExitCode{
		400: ExitUsage, 401: ExitAuth, 403: ExitDenied, 404: ExitNotFound,
		409: ExitConflict, 412: ExitConflict, 422: ExitConflict, 410: ExitConflict, 426: ExitConflict,
		429: ExitRateLimited, 500: ExitUnavailable, 502: ExitUnavailable, 503: ExitUnavailable, 504: ExitUnavailable,
		200: ExitUnclassified, 302: ExitUnclassified,
	} {
		if got := ExitForStatus(status); got != want {
			t.Errorf("ExitForStatus(%d) = %d, want %d", status, got, want)
		}
	}
}

func TestFailureFromPlainError(t *testing.T) {
	env := Failure(errors.New("boom"), Meta{})
	if env.ExitCode != ExitUnclassified || env.Error.Code != CodeInternal {
		t.Fatalf("got %+v", env)
	}
}

func TestExitCodesStable(t *testing.T) {
	want := map[ExitCode]string{0: "ok", 1: "unclassified", 2: "usage", 3: "auth", 4: "denied", 5: "not_found", 6: "conflict", 7: "rate_limited", 8: "unavailable", 9: "action_required", 10: "partial", 130: "interrupted"}
	got := ExitCodes()
	if len(got) != len(want) {
		t.Fatalf("got %d exit codes", len(got))
	}
	for _, c := range got {
		if want[c.Code] != c.Name {
			t.Errorf("exit %d named %q, want %q", c.Code, c.Name, want[c.Code])
		}
	}
}
