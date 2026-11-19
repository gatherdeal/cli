package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Hosts the release-update clients may reach. They never carry a
// credential: not GATHERDEAL_TOKEN, not a GitHub token.
var (
	// ReleaseHost serves the fixed release URLs the CLI requests.
	ReleaseHost = "github.com"
	// ReleaseAssetHosts are GitHub's release-asset hosts that release
	// downloads redirect to.
	ReleaseAssetHosts = []string{"release-assets.githubusercontent.com", "objects.githubusercontent.com"}
	// SigstoreTUFHost serves the Sigstore public-good TUF repository.
	SigstoreTUFHost = "tuf-repo-cdn.sigstore.dev"
)

const (
	// MaxReleaseRedirects bounds the redirects of one release download.
	MaxReleaseRedirects = 3
	// ReleaseTimeout bounds one release-metadata exchange.
	ReleaseTimeout = 30 * time.Second
)

// ErrHostNotAllowed is returned for a request or redirect to a host outside
// a client's allowlist, or over plain HTTP.
var ErrHostNotAllowed = errors.New("host is not on the update allowlist")

// AllowlistOptions configures a client bound to fixed hosts.
type AllowlistOptions struct {
	// Hosts may be requested directly.
	Hosts []string
	// RedirectHosts may be reached through a redirect (in addition to
	// Hosts). Empty with MaxRedirects 0 means redirects are refused.
	RedirectHosts []string
	MaxRedirects  int
	UserAgent     string
	// Base is the underlying transport; nil uses a clone of
	// http.DefaultTransport (standard proxy variables apply).
	Base    http.RoundTripper
	Timeout time.Duration
}

// NewAllowlisted returns a client for release metadata and downloads: HTTPS
// only, only to the allowlisted hosts, at most MaxRedirects redirects and
// only to RedirectHosts, without cookies or credentials.
func NewAllowlisted(opts AllowlistOptions) *http.Client {
	base := opts.Base
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = ReleaseTimeout
	}
	all := append(slices.Clone(opts.Hosts), opts.RedirectHosts...)
	return &http.Client{
		Transport: &allowlistTransport{base: base, hosts: all, userAgent: opts.UserAgent},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > opts.MaxRedirects {
				return fmt.Errorf("%w: more than %d redirects", ErrRedirect, opts.MaxRedirects)
			}
			if !allowedURL(req, opts.RedirectHosts) && !allowedURL(req, opts.Hosts) {
				return fmt.Errorf("%w: redirect to %s", ErrHostNotAllowed, req.URL.Host)
			}
			return nil
		},
		Timeout: timeout,
	}
}

func allowedURL(req *http.Request, hosts []string) bool {
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil {
		return false
	}
	if p := req.URL.Port(); p != "" && p != "443" {
		return false
	}
	return slices.Contains(hosts, strings.ToLower(req.URL.Hostname()))
}

type allowlistTransport struct {
	base      http.RoundTripper
	hosts     []string
	userAgent string
}

func (t *allowlistTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !allowedURL(req, t.hosts) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		host := ""
		if req.URL != nil {
			host = req.URL.Host
		}
		return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
	}
	r := req.Clone(req.Context())
	r.Header.Del("Authorization")
	r.Header.Del("Cookie")
	r.Header.Del("Proxy-Authorization")
	if t.userAgent != "" {
		r.Header.Set("User-Agent", t.userAgent)
	}
	return t.base.RoundTrip(r)
}

// ReadLimited reads at most limit bytes of resp.Body and closes it.
func ReadLimited(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return b, nil
}
