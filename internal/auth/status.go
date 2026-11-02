package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
)

// DelegatedAccessContractVersion is sent to the status endpoint. The
// server returns a delegated-access session only to callers that send
// version 1 or later. It is not part of the discovery document, so the CLI
// hard-codes it.
const DelegatedAccessContractVersion = 1

// StatusUser is the authenticated principal.
type StatusUser struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
}

// StatusOrganization is the access's organization.
type StatusOrganization struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
}

// StatusResult is the part of the status response the CLI uses.
type StatusResult struct {
	User                           *StatusUser         `json:"user"`
	Organization                   *StatusOrganization `json:"organization"`
	DelegatedAccessContractVersion int                 `json:"delegated_access_contract_version"`
	DelegatedAccess                *DelegatedAccess    `json:"delegated_access"`
	CorrelationID                  string              `json:"-"`
}

// Status calls the status endpoint with the bearer the client carries.
func Status(ctx context.Context, client *http.Client, endpoint, clientID string) (*StatusResult, error) {
	body, _ := json.Marshal(map[string]int{"delegated_access_contract_version": DelegatedAccessContractVersion})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot build request").WithCause(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := httpx.DoAPI(ctx, client, req)
	if err != nil {
		return nil, Explain(err)
	}
	var out StatusResult
	if err := res.DecodeInto(&out); err != nil {
		return nil, err
	}
	out.CorrelationID = res.CorrelationID
	if out.DelegatedAccess == nil || out.DelegatedAccessContractVersion < DelegatedAccessContractVersion {
		e := output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the server did not return a delegated-access session")
		e.CorrelationID = res.CorrelationID
		return nil, e
	}
	if err := out.DelegatedAccess.check(clientID); err != nil {
		return nil, err
	}
	return &out, nil
}

// Revoke revokes token at an RFC 7009 endpoint as a public client.
func Revoke(ctx context.Context, client *http.Client, endpoint, clientID, token string) error {
	form := url.Values{"token": {token}, "client_id": {clientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot build request").WithCause(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return httpx.TransportError(ctx, err)
	}
	body, err := httpx.ReadBody(resp)
	if err != nil {
		return httpx.TransportError(ctx, err)
	}
	if resp.StatusCode == http.StatusOK {
		return revokeOK(body)
	}
	e := deviceError(resp.StatusCode, body)
	e.Code = output.CodeRevokeFailed
	e.Message = "the server did not revoke the access: " + e.Message
	return e
}

// revokeOK interprets an RFC 7009 HTTP 200 body. Empty, null and plain
// JSON values are success; an OAuth or platform error object, or a body
// that is not JSON, is a failure.
func revokeOK(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	fail := func(msg string) error {
		return &output.Error{Code: output.CodeRevokeFailed, Message: "the server did not revoke the access: " + msg,
			HTTPStatus: http.StatusOK, Exit: output.ExitUnclassified}
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return fail("the response is not valid JSON")
	}
	if obj, ok := v.(map[string]any); ok {
		if e, hasErr := obj["error"]; hasErr && e != nil {
			return fail("the response reports an error")
		}
		if st, _ := obj["status"].(string); st != "" && st != "success" {
			return fail("the response reports status " + st)
		}
	}
	return nil
}

// Rejection says why the server refused a credential.
type Rejection string

const (
	// RejectedUnknown: the server refused the credential without a reason
	// the CLI knows.
	RejectedUnknown Rejection = "unknown"
	// RejectedExpired: the access reached its absolute expiry.
	RejectedExpired Rejection = "expired"
	// RejectedIdle: the access expired after 30 days without use.
	RejectedIdle Rejection = "idle"
	// RejectedRevoked: the access was revoked.
	RejectedRevoked Rejection = "revoked"
)

// CredentialRejectedError is an authentication failure of a request that
// carried a credential. It has no recovery hint: the frontend renders one,
// because only it knows which profile, server or environment variable
// supplied the credential.
type CredentialRejectedError struct {
	Reason Rejection
	Err    *output.Error
}

func (e *CredentialRejectedError) Error() string { return e.Err.Error() }
func (e *CredentialRejectedError) Unwrap() error { return e.Err }

// Explain turns an authentication failure into a CredentialRejectedError,
// with the reason of DELEGATED_ACCESS_INACTIVE when the server gave one. A
// problem keeps the server's title and detail; only CLI-worded status
// failures are rephrased. Other errors pass through.
func Explain(err error) error {
	var e *output.Error
	if !errors.As(err, &e) || e.Exit != output.ExitAuth {
		return err
	}
	reason := RejectedUnknown
	var d struct {
		Reason string `json:"reason"`
	}
	raw, _ := json.Marshal(e.Details)
	if e.Code == "DELEGATED_ACCESS_INACTIVE" && json.Unmarshal(raw, &d) == nil {
		switch d.Reason {
		case "expired":
			reason = RejectedExpired
		case "idle":
			reason = RejectedIdle
		case "revoked":
			reason = RejectedRevoked
		}
	}
	if len(e.Problem) == 0 && (e.Message == "" || strings.HasPrefix(e.Message, "the server answered HTTP")) {
		e.Message = "the server did not accept this credential"
	}
	return &CredentialRejectedError{Reason: reason, Err: e}
}

// EffectiveExpiry returns the access's absolute expiry (nil = never).
func (d *DelegatedAccess) EffectiveExpiry() *time.Time { return d.ExpireAt }
