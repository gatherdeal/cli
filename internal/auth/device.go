// Package auth implements the delegated-access login (RFC 8628 device
// authorization), the status call and RFC 7009 revocation.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
)

// Lifetimes are the absolute lifetimes a login may request as a hint; the
// approver makes the final choice.
var Lifetimes = []string{"30d", "90d", "180d", "365d", "never"}

// ValidLifetime reports whether s is one of Lifetimes.
func ValidLifetime(s string) bool {
	for _, l := range Lifetimes {
		if s == l {
			return true
		}
	}
	return false
}

// PresetReadOnly is the access preset the login suggests. It is a hint: the
// approver chooses the final preset (read only, or read and write) and
// whether the access may send messages.
const PresetReadOnly = "read_only"

// Bounds on server-chosen polling values. A response outside them is a
// protocol error, so a hostile server cannot stall the CLI.
const (
	// MaxInterval is the longest accepted polling interval, in seconds.
	MaxInterval = 60
	// MaxExpiresIn is the longest accepted device-code lifetime, in seconds.
	MaxExpiresIn = 1800
)

const (
	deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	defaultInterval     = 5 * time.Second
	slowDownIncrement   = 5 * time.Second
	maxDeviceLabel      = 200
)

// ScopeHints builds the device authorization scope: the preset, and the
// optional lifetime and device label hints.
func ScopeHints(lifetime, deviceName string) string {
	scopes := []string{"gd.preset:" + PresetReadOnly}
	if lifetime != "" {
		scopes = append(scopes, "gd.lifetime:"+lifetime)
	}
	if deviceName != "" {
		scopes = append(scopes, "gd.device:"+deviceName)
	}
	return strings.Join(scopes, " ")
}

// SanitizeDeviceName keeps printable ASCII without spaces (a scope token
// cannot contain one), truncated to 200 characters.
func SanitizeDeviceName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t':
			b.WriteByte('-')
		case r > ' ' && r < 0x7f:
			b.WriteRune(r)
		}
		if b.Len() >= maxDeviceLabel {
			break
		}
	}
	out := b.String()
	if len(out) > maxDeviceLabel {
		out = out[:maxDeviceLabel]
	}
	return out
}

// DeviceAuthorization is the device authorization response.
type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// EffectCeiling bounds what an access may do.
type EffectCeiling struct {
	MaxMutation   string   `json:"max_mutation"`
	AllowExternal bool     `json:"allow_external"`
	AllowedRisks  []string `json:"allowed_risks"`
}

// DelegatedAccess is the restriction of an access as the server reports it.
type DelegatedAccess struct {
	ID                 string        `json:"id"`
	CredentialKind     string        `json:"credential_kind"`
	RestrictionVersion int           `json:"restriction_version"`
	ClientID           string        `json:"client_id"`
	OrganizationID     string        `json:"organization_id"`
	OutletIDs          []string      `json:"outlet_ids"`
	Permissions        []string      `json:"permissions"`
	EffectCeiling      EffectCeiling `json:"effect_ceiling"`
	Lifetime           string        `json:"lifetime"`
	ExpireAt           *time.Time    `json:"expire_at,omitempty"`
	IdleExpireAt       time.Time     `json:"idle_expire_at"`
}

// SupportedRestrictionVersion is the only restriction version this CLI
// understands; it refuses others.
const SupportedRestrictionVersion = 1

// check validates fields the CLI relies on.
func (d *DelegatedAccess) check(clientID string) error {
	if d.RestrictionVersion != SupportedRestrictionVersion {
		return &output.Error{
			Code:    output.CodeUnsupportedRestriction,
			Message: fmt.Sprintf("the server reports restriction version %d; this CLI understands only %d. Upgrade the CLI.", d.RestrictionVersion, SupportedRestrictionVersion),
			Exit:    output.ExitUnclassified,
		}
	}
	if d.ID == "" || d.OrganizationID == "" || d.CredentialKind != "bearer" || (clientID != "" && d.ClientID != clientID) {
		return output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the server's delegated_access summary is incomplete or for another client")
	}
	return nil
}

// TokenResponse is a successful device token response.
type TokenResponse struct {
	AccessToken     string          `json:"access_token"`
	TokenType       string          `json:"token_type"`
	ExpiresIn       *int64          `json:"expires_in,omitempty"`
	DelegatedAccess DelegatedAccess `json:"delegated_access"`
}

// Device runs the device flow against one validated discovery document.
type Device struct {
	// HTTP must be bound to the discovery server origin, without a token.
	HTTP       *http.Client
	Discovery  server.Discovery
	Production bool
	// Sleep waits between polls; nil uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
}

func (d *Device) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Device) sleep(ctx context.Context, dur time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, dur)
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (d *Device) postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot build request").WithCause(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return 0, nil, httpx.TransportError(ctx, err)
	}
	body, err := httpx.ReadBody(resp)
	if err != nil {
		return 0, nil, httpx.TransportError(ctx, err)
	}
	return resp.StatusCode, body, nil
}

// Authorize starts a device authorization and validates the response.
func (d *Device) Authorize(ctx context.Context, scope string) (*DeviceAuthorization, error) {
	form := url.Values{"client_id": {d.Discovery.ClientID}}
	if scope != "" {
		form.Set("scope", scope)
	}
	status, body, err := d.postForm(ctx, d.Discovery.DeviceAuthorizationEndpoint, form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, deviceError(status, body)
	}
	var da DeviceAuthorization
	if err := json.Unmarshal(body, &da); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the device authorization response is not valid JSON").WithCause(err)
	}
	if da.DeviceCode == "" || da.UserCode == "" {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the device authorization response is incomplete")
	}
	// Validate the integers before any duration conversion.
	if da.ExpiresIn < 1 || da.ExpiresIn > MaxExpiresIn || da.Interval < 0 || da.Interval > MaxInterval {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol,
			"the device authorization response has expires_in %d / interval %d outside 1..%d / 0..%d seconds",
			da.ExpiresIn, da.Interval, MaxExpiresIn, MaxInterval)
	}
	if err := server.CheckVerificationURIs(da.VerificationURI, da.VerificationURIComplete, d.Discovery.VerificationURI, d.Discovery.Server, d.Production); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "unsafe approval page: %v", err)
	}
	if da.Interval == 0 {
		da.Interval = int(defaultInterval / time.Second)
	}
	return &da, nil
}

// Poll polls the token endpoint until the login is approved, denied or
// expired, honouring interval and slow_down.
func (d *Device) Poll(ctx context.Context, deviceCode string, interval time.Duration, expiresAt time.Time) (*TokenResponse, error) {
	const maxInterval = MaxInterval * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	interval = min(interval, maxInterval)
	form := url.Values{
		"grant_type":  {deviceCodeGrantType},
		"device_code": {deviceCode},
		"client_id":   {d.Discovery.ClientID},
	}
	for {
		remaining := expiresAt.Sub(d.now())
		if remaining <= 0 {
			return nil, expiredLogin()
		}
		// Never sleep past the code's expiry.
		if err := d.sleep(ctx, min(interval, remaining)); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, output.Errorf(output.ExitInterrupted, output.CodeInterrupted, "interrupted while waiting for approval")
			}
			return nil, err
		}
		if !d.now().Before(expiresAt) {
			return nil, expiredLogin()
		}
		// Bound the request by the code's remaining lifetime too.
		reqCtx, cancel := context.WithTimeout(ctx, expiresAt.Sub(d.now()))
		status, body, err := d.postForm(reqCtx, d.Discovery.TokenEndpoint, form)
		cancel()
		if !d.now().Before(expiresAt) && ctx.Err() == nil {
			// The answer arrived after the code expired. A credential
			// issued that late is revoked rather than kept.
			if err == nil && status == http.StatusOK {
				if tr, derr := d.decodeToken(body); derr == nil {
					rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					_ = Revoke(rctx, d.HTTP, d.Discovery.RevocationEndpoint, d.Discovery.ClientID, tr.AccessToken)
					rcancel()
				}
			}
			return nil, expiredLogin()
		}
		if err != nil {
			var oe *output.Error
			if errors.As(err, &oe) && oe.Retryable && oe.Exit == output.ExitUnavailable {
				// Transient network failure: keep polling until expiry.
				continue
			}
			return nil, err
		}
		if status == http.StatusOK {
			return d.decodeToken(body)
		}
		var oe oauthError
		_ = json.Unmarshal(body, &oe)
		switch oe.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval = min(interval+slowDownIncrement, maxInterval)
			continue
		}
		return nil, deviceError(status, body)
	}
}

func (d *Device) decodeToken(body []byte) (*TokenResponse, error) {
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the token response is not valid JSON")
	}
	if !strings.EqualFold(tr.TokenType, "Bearer") {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the token response has token_type %q, want Bearer", tr.TokenType)
	}
	if !server.ValidToken(tr.AccessToken) {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeProtocol, "the server issued a credential that is not a delegated-access token")
	}
	if err := tr.DelegatedAccess.check(d.Discovery.ClientID); err != nil {
		return nil, err
	}
	return &tr, nil
}

func expiredLogin() *output.Error {
	return output.Errorf(output.ExitAuth, output.CodeLoginExpired, "the login code expired before it was approved").
		WithHint("Run `gatherdeal auth login` again and approve the new code within 10 minutes.")
}

// deviceError maps an RFC 8628 / RFC 6749 error response.
func deviceError(status int, body []byte) *output.Error {
	var oe oauthError
	if err := json.Unmarshal(body, &oe); err != nil || oe.Error == "" {
		e := output.Errorf(output.ExitForStatus(status), output.CodeUpstream, "the login endpoint answered HTTP %d", status)
		e.HTTPStatus, e.Retryable = status, httpx.GatewayStatus(status)
		return e
	}
	details := map[string]string{"oauth_error": oe.Error}
	var e *output.Error
	switch oe.Error {
	case "access_denied":
		e = output.Errorf(output.ExitDenied, output.CodeLoginDenied, "the login was denied on the approval page").
			WithHint("Ask the person who approves logins; run `gatherdeal auth login` to try again.")
	case "expired_token":
		e = expiredLogin()
	case "invalid_grant":
		e = output.Errorf(output.ExitAuth, output.CodeLoginRestart, "the login code is no longer valid").
			WithHint("Run `gatherdeal auth login` again.")
	case "too_many_pending_requests":
		e = output.Errorf(output.ExitRateLimited, output.CodeRateLimited, "too many pending logins from this network").
			WithHint("Approve or let the pending logins expire (10 minutes), then try again.")
		e.Retryable = true
	case "invalid_scope":
		e = output.Errorf(output.ExitUsage, output.CodeLoginRejected, "the server does not offer the requested access or lifetime")
	case "server_error", "temporarily_unavailable":
		e = output.Errorf(output.ExitUnavailable, output.CodeUpstream, "the login endpoint failed (%s)", oe.Error)
		e.Retryable = true
	default:
		e = output.Errorf(output.ExitUnclassified, output.CodeLoginRejected, "the server rejected the login request (%s)", oe.Error)
	}
	e.HTTPStatus = status
	e.Details = details
	return e
}
