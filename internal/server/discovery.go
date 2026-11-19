package server

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/semver"
)

// DiscoverySchemaVersion is the only discovery document version this CLI
// understands.
const DiscoverySchemaVersion = 1

// Discovery is the CLI discovery document a server publishes at
// DiscoveryPath.
type Discovery struct {
	SchemaVersion               int                `json:"schema_version"`
	Server                      string             `json:"server"`
	Issuer                      string             `json:"issuer"`
	ClientID                    string             `json:"client_id"`
	DeviceAuthorizationEndpoint string             `json:"device_authorization_endpoint"`
	TokenEndpoint               string             `json:"token_endpoint"`
	RevocationEndpoint          string             `json:"revocation_endpoint"`
	StatusEndpoint              string             `json:"status_endpoint"`
	VerificationURI             string             `json:"verification_uri"`
	VerificationURIComplete     string             `json:"verification_uri_complete,omitempty"`
	Services                    map[string]Service `json:"services"`
	MinCLIVersion               string             `json:"min_cli_version,omitempty"`
}

// Service describes one business API advertised by the server.
type Service struct {
	BasePath string `json:"base_path"`
}

// Production returns the compiled discovery document of the built-in
// production profile.
func Production() Discovery {
	return Discovery{
		SchemaVersion:               DiscoverySchemaVersion,
		Server:                      ProductionOrigin,
		Issuer:                      ProductionIssuer,
		ClientID:                    ClientID,
		DeviceAuthorizationEndpoint: ProductionOrigin + "/oidc/device/authorize",
		TokenEndpoint:               ProductionOrigin + "/oidc/device/token",
		RevocationEndpoint:          ProductionOrigin + "/oidc/revoke",
		StatusEndpoint:              ProductionOrigin + "/identity/v1/authenticate",
		VerificationURI:             "https://" + ProductionVerificationHost + "/device",
		Services:                    compiledServices(),
		MinCLIVersion:               "0.1.0",
	}
}

var (
	basePathRE    = regexp.MustCompile(`^/[a-z][a-z0-9-]*/v[0-9]+$`)
	serviceNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	clientIDRE    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// ExpectedIssuer is the only issuer a discovery document for origin may
// name: the pinned ProductionIssuer for the built-in production profile,
// origin + "/oidc" for every other server.
func ExpectedIssuer(origin string, production bool) string {
	if production {
		return ProductionIssuer
	}
	return origin + "/oidc"
}

// Validate checks d against the server origin the user chose. The server
// and every endpoint must be on origin; the issuer must be
// ExpectedIssuer(origin, production). production applies only to
// ProductionOrigin and additionally pins the approval-page host.
func (d Discovery) Validate(origin string, production bool) error {
	if d.SchemaVersion != DiscoverySchemaVersion {
		return fmt.Errorf("unsupported discovery schema_version %d (this CLI understands %d); upgrade the CLI", d.SchemaVersion, DiscoverySchemaVersion)
	}
	if production && origin != ProductionOrigin {
		return fmt.Errorf("the production profile is bound to %q, not %q", ProductionOrigin, origin)
	}
	if d.Server != origin {
		return fmt.Errorf("discovery server %q does not match the requested server %q", d.Server, origin)
	}
	if want := ExpectedIssuer(origin, production); d.Issuer != want {
		return fmt.Errorf("discovery issuer %q must be exactly %q", d.Issuer, want)
	}
	if !clientIDRE.MatchString(d.ClientID) {
		return errors.New("discovery client_id is missing or malformed")
	}
	endpoints := []struct{ name, value string }{
		{"device_authorization_endpoint", d.DeviceAuthorizationEndpoint},
		{"token_endpoint", d.TokenEndpoint},
		{"revocation_endpoint", d.RevocationEndpoint},
		{"status_endpoint", d.StatusEndpoint},
	}
	for _, ep := range endpoints {
		if err := checkEndpoint(ep.value, origin); err != nil {
			return fmt.Errorf("discovery %s: %w", ep.name, err)
		}
	}
	if err := CheckVerificationURIs(d.VerificationURI, d.VerificationURIComplete, "", origin, production); err != nil {
		return fmt.Errorf("discovery %w", err)
	}
	names := make([]string, 0, len(d.Services))
	for name := range d.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !serviceNameRE.MatchString(name) {
			return fmt.Errorf("discovery service name %q is malformed", name)
		}
		if !basePathRE.MatchString(d.Services[name].BasePath) {
			return fmt.Errorf("discovery services.%s.base_path %q must match %s", name, d.Services[name].BasePath, basePathRE)
		}
	}
	if d.MinCLIVersion != "" {
		if !semver.Valid(d.MinCLIVersion) {
			return fmt.Errorf("discovery min_cli_version %q is not a version", d.MinCLIVersion)
		}
	}
	return nil
}

// checkEndpoint requires an absolute URL on exactly origin, with no user
// info, query or fragment.
func checkEndpoint(raw, origin string) error {
	if raw == "" {
		return errors.New("missing")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%q is not an absolute URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("%q must not carry user info, a query or a fragment", u.Redacted())
	}
	if err := checkScheme(u); err != nil {
		return err
	}
	if got := originOf(u); got != origin {
		return fmt.Errorf("%q is not on the server origin %q", u.Redacted(), origin)
	}
	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("%q has no path", u.Redacted())
	}
	return nil
}

// CheckVerificationURIs validates the approval-page URIs. Both must use
// HTTPS (plain HTTP only when serverOrigin and the approval host are both
// loopback, i.e. local development) on the same host; the complete URI must start with the base URI
// followed by "?". expected, when non-empty, is the base URI the response
// must equal (the discovery value). production pins the host.
func CheckVerificationURIs(base, complete, expected, serverOrigin string, production bool) error {
	if base == "" {
		return errors.New("verification_uri is missing")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("verification_uri %q is not an absolute URL", base)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("verification_uri %q must not carry user info, a query or a fragment", u.Redacted())
	}
	// Plain HTTP only for local development: both the API server and the
	// approval page must be loopback.
	localDev := !production && loopbackOrigin(serverOrigin) && IsLoopbackHost(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || !localDev) {
		return fmt.Errorf("verification_uri %q must use https", u.Redacted())
	}
	if production && (u.Scheme != "https" || !strings.EqualFold(u.Host, ProductionVerificationHost)) {
		return fmt.Errorf("verification_uri host %q is not the production approval host %q", u.Host, ProductionVerificationHost)
	}
	if expected != "" && base != expected {
		return fmt.Errorf("verification_uri %q differs from the discovery document %q", base, expected)
	}
	if complete == "" {
		return nil
	}
	c, err := url.Parse(complete)
	if err != nil || c.Scheme == "" || c.Host == "" {
		return fmt.Errorf("verification_uri_complete %q is not an absolute URL", complete)
	}
	if c.Scheme != u.Scheme || !strings.EqualFold(c.Host, u.Host) || c.User != nil || c.Fragment != "" {
		return fmt.Errorf("verification_uri_complete must be on the same scheme and host as verification_uri")
	}
	if !strings.HasPrefix(complete, base+"?") {
		return fmt.Errorf("verification_uri_complete must start with verification_uri followed by \"?\"")
	}
	return nil
}

// loopbackOrigin reports whether origin is a loopback development server.
func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && IsLoopbackHost(u.Hostname())
}

// ServiceURL returns the absolute base URL of a business API.
func (d Discovery) ServiceURL(name string) (string, bool) {
	svc, ok := d.Services[name]
	if !ok || !basePathRE.MatchString(svc.BasePath) {
		return "", false
	}
	return d.Server + svc.BasePath, true
}

// RequiresNewerCLI reports whether the running version is older than
// min_cli_version by SemVer precedence, pre-releases included. Development
// builds (non-release versions) never do.
func (d Discovery) RequiresNewerCLI(running string) bool {
	if d.MinCLIVersion == "" {
		return false
	}
	have, err := semver.Parse(running)
	if err != nil {
		return false
	}
	want, err := semver.Parse(d.MinCLIVersion)
	if err != nil {
		return false
	}
	return semver.Less(have, want)
}

// compiledServices advertises only published bundles, using their production
// paths. Custom discovery remains authoritative for non-production profiles.
func compiledServices() map[string]Service {
	m, err := contracts.Load()
	if err != nil {
		panic(err)
	}
	services := make(map[string]Service, len(m.Bundles))
	for _, b := range m.Bundles {
		if _, exists := services[b.Service]; exists {
			panic("multiple API versions for service " + b.Service)
		}
		services[b.Service] = Service{BasePath: b.ServicePath + "/" + b.Version}
	}
	return services
}
