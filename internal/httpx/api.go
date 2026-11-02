package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/gatherdeal/cli/internal/output"
)

// Problem is the service-independent platform problem document.
type Problem struct {
	// Type is https://docs.gatherdeal.ai/errors/<CODE>, one-to-one with Code.
	Type string `json:"type"`
	// Title is the code's constant summary, localized.
	Title string `json:"title"`
	// Status equals the HTTP status line.
	Status int `json:"status"`
	// Detail is occurrence-specific text, localized; empty when absent.
	Detail string `json:"detail,omitempty"`
	// Instance identifies the occurrence (urn:gatherdeal:request:<id>).
	Instance string `json:"instance,omitempty"`
	// Code is the discriminant.
	Code string `json:"code"`
	// Retryable reports a transient condition; it does not authorize replay.
	Retryable bool `json:"retryable"`
	// RetryAfterMS is the server's retry hint in milliseconds, 0 when none.
	RetryAfterMS int64 `json:"retry_after_ms,omitempty"`
	// CorrelationID is the support lookup key of the occurrence (the
	// response's X-Correlation-Id header when the body carries none).
	CorrelationID string `json:"correlation_id,omitempty"`
	// Details is the code's raw details object, nil when absent.
	Details json.RawMessage `json:"details,omitempty"`
	// Errors is the raw "errors" member, nil when absent: violations for
	// VALIDATION_FAILED, item problems for batch codes.
	Errors json.RawMessage `json:"errors,omitempty"`
	// ContentLanguage is the response's Content-Language header: the
	// languages Title and Detail are written in.
	ContentLanguage string `json:"-"`
}

func (p *Problem) Error() string {
	return "api: " + p.Code + " (HTTP " + fmt.Sprint(p.Status) + "): " + p.Title
}

const ProblemMediaType = "application/problem+json"
const CodeClientUpgradeRequired = "CLIENT_UPGRADE_REQUIRED"

// upgradeHint is the hint of CLIENT_UPGRADE_REQUIRED.
const upgradeHint = "Run `gatherdeal update`."

// problemExits overrides the status-derived exit code for the platform
// codes whose client action differs from their status class.
var problemExits = map[string]output.ExitCode{
	CodeClientUpgradeRequired: output.ExitActionRequired,
}

// Result is a decoded successful API response.
type Result struct {
	// Body is the full success document (payload fields plus "status").
	Body []byte
	// CorrelationID is the server's correlation id, if any.
	CorrelationID string
	// HTTPStatus is the response status code.
	HTTPStatus int
}

// DoAPI sends req with client and decodes the response with DecodeAPI.
func DoAPI(ctx context.Context, client *http.Client, req *http.Request) (*Result, error) {
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, TransportError(ctx, err)
	}
	body, err := ReadBody(resp)
	if errors.Is(err, ErrBodyTooLarge) {
		return nil, ProtocolError(resp.StatusCode, resp.Header.Get(CorrelationHeader), "response body exceeds the size limit")
	}
	if err != nil {
		return nil, TransportError(ctx, err)
	}
	return DecodeAPI(resp.StatusCode, resp.Header, body)
}

// DecodeAPI applies the header-aware decode contract every GatherDeal
// client shares (the generated runtime's decodeResponse; the conformance
// fixtures under contracts/testdata pin both):
//
//  1. Content-Type is parsed structurally (case-insensitive, parameters
//     ignored).
//  2. A 2xx must be an application/json success envelope; a 2xx problem
//     document or any other 2xx is a protocol error.
//  3. A non-2xx application/problem+json document is a problem: its status
//     derives the exit code and its retryable member the retryability.
//     Malformed JSON, a missing code or a status that differs from the
//     status line is a protocol error.
//  4. Any other non-2xx is classified by its HTTP status alone.
func DecodeAPI(status int, header http.Header, body []byte) (*Result, error) {
	correlationID := header.Get(CorrelationHeader)
	media := MediaType(header.Get("Content-Type"))
	if status >= 200 && status < 300 {
		switch {
		case media == ProblemMediaType:
			return nil, ProtocolError(status, correlationID, "problem document with a success status")
		case media != "application/json":
			return nil, ProtocolError(status, correlationID, "success response is not application/json")
		}
		var env struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, ProtocolError(status, correlationID, "malformed success envelope")
		}
		if env.Status != "success" {
			return nil, ProtocolError(status, correlationID, "2xx response is not a success envelope")
		}
		return &Result{Body: body, CorrelationID: correlationID, HTTPStatus: status}, nil
	}
	if media != ProblemMediaType {
		return nil, StatusError(status, correlationID)
	}
	var p Problem
	switch {
	case json.Unmarshal(body, &p) != nil:
		return nil, ProtocolError(status, correlationID, "malformed problem document")
	case p.Code == "":
		return nil, ProtocolError(status, correlationID, "problem document without a code")
	case p.Status != status:
		return nil, ProtocolError(status, correlationID, "problem status differs from the status line")
	}
	if p.CorrelationID == "" {
		p.CorrelationID = correlationID
	}
	p.ContentLanguage = header.Get("Content-Language")
	return nil, ProblemError(&p, body)
}

// MediaType returns the lower-cased type/subtype of a Content-Type value.
func MediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt, _, _ = strings.Cut(contentType, ";")
		mt = strings.ToLower(strings.TrimSpace(mt))
	}
	return mt
}

// ProblemError is the CLI error of a server problem document. raw is the
// document as received; nil re-encodes p. Exit code: the platform override
// table first, the problem's status otherwise (output.ExitForStatus).
// Retryability is the problem's own. Title and detail keep the server's
// wording and language, stripped of control characters.
func ProblemError(p *Problem, raw []byte) *output.Error {
	exit, ok := problemExits[p.Code]
	if !ok {
		exit = output.ExitForStatus(p.Status)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !json.Valid(raw) {
		raw, _ = json.Marshal(p)
	}
	e := &output.Error{
		Code:          p.Code,
		Message:       output.StripControls(p.Title),
		Detail:        output.StripControls(p.Detail),
		HTTPStatus:    p.Status,
		CorrelationID: p.CorrelationID,
		Retryable:     p.Retryable,
		Exit:          exit,
		Problem:       json.RawMessage(raw),
		Cause:         p,
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("the server answered HTTP %d with problem %s", p.Status, p.Code)
	}
	if d := bytes.TrimSpace(p.Details); len(d) > 0 && !bytes.Equal(d, []byte("null")) {
		e.Details = json.RawMessage(d)
	}
	if p.Code == CodeClientUpgradeRequired {
		e.Hint = upgradeHint
	}
	return e
}

// StatusError is the CLI error of a response without a problem document
// (a gateway page, text, an empty body). On a 2xx it is a protocol error;
// otherwise the HTTP status classifies it, and only the gateway statuses
// 502, 503 and 504 are retryable (the shared retry table).
func StatusError(httpStatus int, correlationID string) *output.Error {
	if httpStatus >= 200 && httpStatus < 300 {
		return ProtocolError(httpStatus, correlationID, "success response without a success envelope")
	}
	return &output.Error{
		Code:          output.CodeUpstream,
		Message:       fmt.Sprintf("the server answered HTTP %d without a problem document", httpStatus),
		HTTPStatus:    httpStatus,
		CorrelationID: correlationID,
		Retryable:     GatewayStatus(httpStatus),
		Exit:          output.ExitForStatus(httpStatus),
	}
}

// GatewayStatus reports the statuses a gateway answers for a transient
// failure (502, 503, 504): retryable when they carry no problem document.
func GatewayStatus(httpStatus int) bool {
	switch httpStatus {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// ProtocolError is the CLI error of a response that violates the
// success/problem contract. It is never retryable.
func ProtocolError(httpStatus int, correlationID, reason string) *output.Error {
	return &output.Error{
		Code:          output.CodeProtocol,
		Message:       "the server response violates the API contract: " + reason,
		HTTPStatus:    httpStatus,
		CorrelationID: correlationID,
		Exit:          output.ExitUnclassified,
	}
}

// DecodeInto unmarshals the success body into out.
func (r *Result) DecodeInto(out any) error {
	if err := json.Unmarshal(r.Body, out); err != nil {
		return &output.Error{
			Code:          output.CodeProtocol,
			Message:       "the server response does not match the expected shape",
			HTTPStatus:    r.HTTPStatus,
			CorrelationID: r.CorrelationID,
			Exit:          output.ExitUnclassified,
			Cause:         err,
		}
	}
	return nil
}
