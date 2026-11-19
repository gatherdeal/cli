// Package httpx is the only HTTP stack of the CLI. Every client it builds
// refuses redirects, talks to exactly one origin and attaches the bearer
// credential only to that origin.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"

	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
)

const (
	// DefaultTimeout bounds one HTTP exchange.
	DefaultTimeout = 30 * time.Second
	// MaxBodyBytes bounds every response body the CLI reads.
	MaxBodyBytes = 32 << 20
	// CorrelationHeader is the response header carrying the server's
	// correlation id.
	CorrelationHeader = "X-Correlation-Id"
)

// ErrRedirect is returned for any 3xx response. Redirects are never
// followed: a request body may carry a device code or token.
var ErrRedirect = errors.New("redirect refused")

// ErrBodyTooLarge is returned by ReadBody for a body over MaxBodyBytes.
var ErrBodyTooLarge = fmt.Errorf("response body exceeds %d bytes", MaxBodyBytes)

// ErrOriginMismatch is returned when a request targets another origin than
// the one the client is bound to.
var ErrOriginMismatch = errors.New("request origin differs from the profile's server origin")

// Options configures a bound client.
type Options struct {
	// Origin is the only origin the client may contact (canonical form).
	Origin string
	// Token, when non-empty, is attached as a bearer credential.
	Token string
	// UserAgent is sent on every request.
	UserAgent string
	// AcceptLanguage, when non-empty, is sent on every request: the
	// language the server writes problem text in.
	AcceptLanguage string
	// Base is the underlying transport; nil uses a clone of
	// http.DefaultTransport (which honours the standard proxy variables).
	Base http.RoundTripper
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
}

// New returns an *http.Client bound to opts.Origin. It is safe to hand to
// generated API clients: the binding and redirect refusal live in the
// transport, not in the caller.
func New(opts Options) *http.Client {
	base := opts.Base
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{
		Transport: &boundTransport{base: base, origin: opts.Origin, token: opts.Token, userAgent: opts.UserAgent, acceptLanguage: opts.AcceptLanguage},
		// The transport already turns 3xx into ErrRedirect; this is a
		// second line in case a response slips through.
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
		Timeout:       timeout,
	}
}

type boundTransport struct {
	base           http.RoundTripper
	origin         string
	token          string
	userAgent      string
	acceptLanguage string
}

func (t *boundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, ErrOriginMismatch
	}
	got, err := server.OriginOfURL(req.URL.String())
	if err != nil || got != t.origin || req.URL.User != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrOriginMismatch
	}
	r := req.Clone(req.Context())
	r.Header.Del("Authorization")
	r.Header.Del("Cookie")
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	if t.userAgent != "" {
		r.Header.Set("User-Agent", t.userAgent)
	}
	if t.acceptLanguage != "" {
		r.Header.Set("Accept-Language", t.acceptLanguage)
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		return nil, ErrRedirect
	}
	return resp, nil
}

// TransportError converts an error from http.Client.Do into a CLI error.
func TransportError(ctx context.Context, err error) *output.Error {
	if errors.Is(err, ErrRedirect) {
		return output.Errorf(output.ExitUnclassified, output.CodeRedirect,
			"the server answered with a redirect; the CLI never follows redirects").WithCause(err)
	}
	if errors.Is(err, ErrOriginMismatch) {
		return output.Errorf(output.ExitUnclassified, output.CodeOriginMismatch,
			"refused to send a request outside the profile's server origin").WithCause(err)
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return output.Errorf(output.ExitInterrupted, output.CodeInterrupted, "interrupted").WithCause(err)
	}
	msg := "could not reach the server"
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var urlErr *url.Error
	switch {
	case errors.As(err, &dnsErr):
		msg = "could not resolve the server host"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &urlErr) && urlErr.Timeout()):
		msg = "the request timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		msg = "the server refused the connection"
	case errors.As(err, &opErr):
		msg = "network error talking to the server"
	}
	e := output.Errorf(output.ExitUnavailable, output.CodeNetwork, "%s", msg).WithCause(err)
	e.Retryable = true
	return e
}

// ReadBody reads at most MaxBodyBytes of resp.Body and closes it.
func ReadBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	return b, nil
}
