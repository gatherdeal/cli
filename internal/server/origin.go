// Package server holds the compiled production defaults, origin rules and
// the CLI discovery document.
package server

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Compiled production defaults. Other servers are reachable only through an
// explicit --server and the risk confirmation.
const (
	// ProductionOrigin is the built-in production API origin. The profile's
	// server and every endpoint of its discovery document share it.
	ProductionOrigin = "https://api.gatherdeal.ai"
	// ProductionIssuer is the OIDC issuer the built-in production profile
	// pins. It is the one name not on ProductionOrigin. Every other profile
	// requires issuer == server + "/oidc".
	ProductionIssuer = "https://api.gatherdeal.com/oidc"
	// ProductionVerificationHost is the only approval-page host accepted
	// for the built-in production profile.
	ProductionVerificationHost = "passport.gatherdeal.ai"
	// ClientID is the public client id of this CLI.
	ClientID = "gatherdeal-cli"
	// TokenPrefix is the prefix of every delegated-access bearer token.
	TokenPrefix = "gd_dat_"
	// tokenBodyLength is the number of [a-z0-9] characters after the prefix.
	tokenBodyLength = 50
	// DiscoveryPath is where a server publishes its CLI discovery document.
	DiscoveryPath = "/oidc/.well-known/gatherdeal-cli.json"
)

// ValidToken reports whether token has the delegated-access format:
// TokenPrefix followed by 50 characters of [a-z0-9].
func ValidToken(token string) bool {
	body, ok := strings.CutPrefix(token, TokenPrefix)
	if !ok || len(body) != tokenBodyLength {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// TokenHint is the only form of a token the CLI ever displays: the prefix
// and the last four characters.
func TokenHint(token string) string {
	if len(token) < len(TokenPrefix)+4 {
		return TokenPrefix + "…"
	}
	return TokenPrefix + "…" + token[len(token)-4:]
}

// IsLoopbackHost reports whether host (without port) is a loopback name or
// address.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkScheme enforces HTTPS, allowing plain HTTP only for loopback hosts.
func checkScheme(u *url.URL) error {
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if IsLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%q must use https (http is allowed only for loopback hosts)", u.Redacted())
	default:
		return fmt.Errorf("%q must use https", u.Redacted())
	}
}

// originOf returns scheme://host[:port] in canonical form (lower case,
// default port removed).
func originOf(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

// OriginOfURL returns the canonical origin of an absolute URL.
func OriginOfURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("not an absolute URL")
	}
	return originOf(u), nil
}

// NormalizeOrigin parses a --server or --accept-server-risk value. It must
// be a bare origin (an optional trailing slash is tolerated): no user
// info, path, query or fragment.
func NormalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("server URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("server URL %q must be absolute, e.g. https://api.example.com", raw)
	}
	if u.User != nil {
		return "", errors.New("server URL must not contain user info")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("server URL %q must be an origin only (no path, query or fragment)", u.Redacted())
	}
	if err := checkScheme(u); err != nil {
		return "", err
	}
	return originOf(u), nil
}
