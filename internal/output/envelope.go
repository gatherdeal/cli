package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// SchemaVersion is the version of the CLI output envelope. It is
// independent of any server API version.
const SchemaVersion = 1

// Status discriminates the envelope.
type Status string

const (
	StatusSuccess Status = "success"
	StatusError   Status = "error"
)

// Envelope is the single JSON document every command writes to stdout.
type Envelope struct {
	SchemaVersion int        `json:"schema_version"`
	Status        Status     `json:"status"`
	Data          any        `json:"data"`
	Meta          Meta       `json:"meta"`
	Error         *ErrorBody `json:"error,omitempty"`
	ExitCode      ExitCode   `json:"exit_code"`
	Hint          string     `json:"hint,omitempty"`
}

// Meta carries context about the call, never business data.
type Meta struct {
	Context *Context   `json:"context,omitempty"`
	Write   *WriteMeta `json:"write,omitempty"`
	// Server is the origin the command talked to (or would have).
	Server string `json:"server,omitempty"`
	// Profile is the local profile name used.
	Profile string `json:"profile,omitempty"`
	// CorrelationID is the server's correlation id of the last API call.
	CorrelationID string `json:"correlation_id,omitempty"`
	// PageInfo describes the returned page of a list: the fields of the
	// API's ListPage other than its items.
	PageInfo *PageInfo `json:"page_info,omitempty"`
	// CredentialExpiresAt is absent when unknown, null for an access that
	// never expires, and the effective expiry otherwise.
	CredentialExpiresAt OptionalTime `json:"credential_expires_at,omitzero"`
	// CredentialExpiryWarning is true within 7 days of expiry.
	CredentialExpiryWarning bool `json:"credential_expiry_warning,omitempty"`
}

// PageInfo mirrors the API's ListPage keys. Absent members were not in the
// response: page only in page mode, total_items and total_pages only when
// counted, cursors only when the sort supports them.
type PageInfo struct {
	PageSize    int    `json:"page_size,omitempty"`
	HasNextPage bool   `json:"has_next_page"`
	HasPrevPage bool   `json:"has_prev_page"`
	NextCursor  string `json:"next_cursor,omitempty"`
	PrevCursor  string `json:"prev_cursor,omitempty"`
	Page        int    `json:"page,omitempty"`
	TotalItems  *int   `json:"total_items,omitempty"`
	TotalPages  *int   `json:"total_pages,omitempty"`
}

// OptionalTime is a tri-state time: unset (omitted), null, or a value.
type OptionalTime struct {
	Set  bool
	Time *time.Time
}

// KnownTime returns a set OptionalTime; t == nil means "never".
func KnownTime(t *time.Time) OptionalTime { return OptionalTime{Set: true, Time: t} }

// IsZero reports whether the field is unset (omitted by omitzero).
func (o OptionalTime) IsZero() bool { return !o.Set }

// MarshalJSON writes null or an RFC 3339 time.
func (o OptionalTime) MarshalJSON() ([]byte, error) {
	if o.Time == nil {
		return []byte("null"), nil
	}
	return json.Marshal(o.Time.UTC().Format(time.RFC3339))
}

// ErrorBody is the error member of a failure envelope.
type ErrorBody struct {
	Code string `json:"code"`
	// Message is the problem's title for a server failure (in the language
	// the CLI asked for), or the CLI's own text.
	Message string `json:"message"`
	// Detail is the problem's occurrence-specific detail, when present.
	Detail        string `json:"detail,omitempty"`
	Details       any    `json:"details,omitempty"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Retryable     bool   `json:"retryable"`
	// Problem is the server's RFC 9457 problem document as received
	// (credentials redacted), for failures the server answered with one.
	Problem any `json:"problem,omitempty"`
}

// Error is the typed failure every command returns. It never contains a
// credential.
type Error struct {
	Code    string
	Message string
	// Detail is a problem's detail text, stripped of control characters.
	Detail        string
	Details       any
	HTTPStatus    int
	CorrelationID string
	Retryable     bool
	Exit          ExitCode
	Hint          string
	// Problem is the raw problem document of a server failure.
	Problem json.RawMessage
	// Cause is kept for errors.Is/As; it is never written to the envelope.
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// Errorf builds a CLI-originated error.
func Errorf(exit ExitCode, code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Exit: exit}
}

// WithHint sets the hint and returns e.
func (e *Error) WithHint(hint string) *Error { e.Hint = hint; return e }

// WithDetails sets the details and returns e.
func (e *Error) WithDetails(details any) *Error { e.Details = details; return e }

// WithCause sets the cause and returns e.
func (e *Error) WithCause(err error) *Error { e.Cause = err; return e }

// AsError converts any error into an *Error, classifying unknown errors as
// internal failures.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error(), Exit: ExitUnclassified, Cause: err}
}

// problemValue keeps an absent problem out of the envelope (a nil
// json.RawMessage inside an interface is not omitted by omitempty).
func problemValue(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return raw
}

// Success builds a success envelope.
func Success(data any, meta Meta) Envelope {
	return Envelope{SchemaVersion: SchemaVersion, Status: StatusSuccess, Data: data, Meta: meta, ExitCode: ExitOK}
}

// Failure builds a failure envelope from err.
func Failure(err error, meta Meta) Envelope {
	e := AsError(err)
	if e.CorrelationID != "" && meta.CorrelationID == "" {
		meta.CorrelationID = e.CorrelationID
	}
	exit := e.Exit
	if exit == ExitOK {
		exit = ExitUnclassified
	}
	return Envelope{
		SchemaVersion: SchemaVersion,
		Status:        StatusError,
		Data:          nil,
		Meta:          meta,
		Error: &ErrorBody{
			Code:          e.Code,
			Message:       e.Message,
			Detail:        e.Detail,
			Details:       e.Details,
			HTTPStatus:    e.HTTPStatus,
			CorrelationID: e.CorrelationID,
			Retryable:     e.Retryable,
			Problem:       problemValue(e.Problem),
		},
		ExitCode: exit,
		Hint:     e.Hint,
	}
}

// Write encodes env as one line of JSON after redacting every credential
// (red may be nil: pattern-based redaction still applies) and escaping
// characters a terminal could interpret.
func Write(w io.Writer, env Envelope, red *Redactor) error {
	clean, err := red.Envelope(env)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(clean); err != nil {
		return err
	}
	_, err = w.Write(EscapeJSONControls(buf.Bytes()))
	return err
}

// Context separates cached identity from requested selection, never effective scope.
type Context struct {
	Profile string          `json:"profile"`
	Server  string          `json:"server"`
	Issuer  string          `json:"issuer"`
	Binding *ContextBinding `json:"binding,omitempty"`
	Request ContextRequest  `json:"request"`
}
type ContextBinding struct {
	Organization ContextOrganization `json:"organization"`
	PrincipalID  string              `json:"principal_id"`
}
type ContextOrganization struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
}
type ContextRequest struct {
	// Empty means the authorized set; absence of context means no auth context.
	OutletIDs []string `json:"outlet_ids"`
}

// WriteMeta is reported on previews, successes and failures once a write is
// prepared. Preserve the key when reconciling an uncertain server outcome.
type WriteMeta struct {
	DryRun                  bool   `json:"dry_run"`
	IdempotencyKey          string `json:"idempotency_key,omitempty"`
	IdempotencyKeyGenerated bool   `json:"idempotency_key_generated,omitempty"`
}

// UnknownWriteOutcome reports uncertainty, independently of transient failure.
// No retry is authorized by this class; even a keyed request needs a server
// replay guarantee before repetition. Call only after dispatch may have begun.
func UnknownWriteOutcome(cause error) *Error {
	return Errorf(ExitUnavailable, CodeWriteOutcomeUnknown, "the write may have been accepted; reconcile its outcome before repeating it").WithCause(cause)
}
