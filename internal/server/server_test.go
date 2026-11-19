package server

import (
	"strings"
	"testing"
)

func TestTokenPrefixPinned(t *testing.T) {
	// The server's credential registry issues delegated-access tokens with
	// this prefix; changing it breaks every stored credential.
	if TokenPrefix != "gd_dat_" {
		t.Fatalf("TokenPrefix = %q", TokenPrefix)
	}
}

func TestValidToken(t *testing.T) {
	good := TokenPrefix + strings.Repeat("a1", 25)
	cases := map[string]bool{
		good:                                  true,
		strings.ToUpper(good):                 false,
		TokenPrefix + strings.Repeat("a", 49): false,
		TokenPrefix + strings.Repeat("a", 51): false,
		TokenPrefix + strings.Repeat("A", 50): false,
		TokenPrefix + strings.Repeat("_", 50): false,
		"gd_oidc_" + strings.Repeat("a", 50):  false,
		"Bearer " + good:                      false,
		"":                                    false,
	}
	for tok, want := range cases {
		if got := ValidToken(tok); got != want {
			t.Errorf("ValidToken(%q) = %v, want %v", tok, got, want)
		}
	}
	if h := TokenHint(good); h != "gd_dat_…"+good[len(good)-4:] {
		t.Errorf("TokenHint = %q", h)
	}
}

func TestNormalizeOrigin(t *testing.T) {
	ok := map[string]string{
		"https://api.example.com":      "https://api.example.com",
		"https://API.Example.com/":     "https://api.example.com",
		"https://api.example.com:443":  "https://api.example.com",
		"https://api.example.com:8443": "https://api.example.com:8443",
		"http://localhost:8080":        "http://localhost:8080",
		"http://127.0.0.1:5000":        "http://127.0.0.1:5000",
		"http://[::1]:5000":            "http://[::1]:5000",
		"  https://api.example.com  ":  "https://api.example.com",
	}
	for in, want := range ok {
		got, err := NormalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "api.example.com", "http://api.example.com", "ftp://api.example.com",
		"https://user:pw@api.example.com", "https://api.example.com/path",
		"https://api.example.com?x=1", "https://api.example.com?", "https://api.example.com#f",
		"http://localhost.evil.com",
	}
	for _, in := range bad {
		if got, err := NormalizeOrigin(in); err == nil {
			t.Errorf("NormalizeOrigin(%q) = %q, want error", in, got)
		}
	}
}

func custom(origin string) Discovery {
	return Discovery{
		SchemaVersion:               1,
		Server:                      origin,
		Issuer:                      origin + "/oidc",
		ClientID:                    "gatherdeal-cli",
		DeviceAuthorizationEndpoint: origin + "/oidc/device/authorize",
		TokenEndpoint:               origin + "/oidc/device/token",
		RevocationEndpoint:          origin + "/oidc/revoke",
		StatusEndpoint:              origin + "/identity/v1/authenticate",
		VerificationURI:             "https://passport.example.com/device",
		Services:                    map[string]Service{"crm": {BasePath: "/crm/v1"}},
		MinCLIVersion:               "0.1.0",
	}
}

func TestProductionDiscoveryValid(t *testing.T) {
	d := Production()
	if err := d.Validate(ProductionOrigin, true); err != nil {
		t.Fatal(err)
	}
	if d.ClientID != ClientID || d.Server != "https://api.gatherdeal.ai" || d.Issuer != ProductionIssuer {
		t.Fatalf("unexpected production defaults: %+v", d)
	}
	// Every endpoint is on the public origin; only the issuer is not.
	for _, ep := range []string{d.DeviceAuthorizationEndpoint, d.TokenEndpoint, d.RevocationEndpoint, d.StatusEndpoint} {
		if o, err := OriginOfURL(ep); err != nil || o != ProductionOrigin {
			t.Errorf("%s is not on %s", ep, ProductionOrigin)
		}
	}
	if o, _ := OriginOfURL(ProductionIssuer); o == ProductionOrigin {
		t.Fatal("the pinned issuer is expected on its own host")
	}
}

// The production profile pins its issuer; every other profile requires
// issuer == server + "/oidc", so the pinned issuer is refused there.
func TestDiscoveryIssuerRule(t *testing.T) {
	prod := Production()
	prod.Issuer = ProductionOrigin + "/oidc"
	if err := prod.Validate(ProductionOrigin, true); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("production with server+/oidc issuer: %v", err)
	}
	// The same document fetched for a --server profile on another origin.
	const origin = "https://api.example.com"
	d := custom(origin)
	d.Issuer = ProductionIssuer
	if err := d.Validate(origin, false); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("custom server naming the production issuer: %v", err)
	}
	// production applies to the production origin only.
	if err := custom(origin).Validate(origin, true); err == nil || !strings.Contains(err.Error(), "bound to") {
		t.Fatalf("production flag on another origin: %v", err)
	}
	// The production document pins the approval-page host.
	prod = Production()
	prod.VerificationURI = "https://passport.example.com/device"
	if err := prod.Validate(ProductionOrigin, true); err == nil || !strings.Contains(err.Error(), "production approval host") {
		t.Fatalf("production approval host: %v", err)
	}
	// An endpoint on another origin is refused for production too.
	prod = Production()
	prod.TokenEndpoint = "https://api.example.com/oidc/device/token"
	if err := prod.Validate(ProductionOrigin, true); err == nil || !strings.Contains(err.Error(), "token_endpoint") {
		t.Fatalf("production endpoint off origin: %v", err)
	}
	if ExpectedIssuer(origin, false) != origin+"/oidc" || ExpectedIssuer(ProductionOrigin, true) != ProductionIssuer {
		t.Fatal("ExpectedIssuer")
	}
}

func TestDiscoveryValidate(t *testing.T) {
	const origin = "https://api.example.com"
	if err := custom(origin).Validate(origin, false); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	cases := []struct {
		name       string
		mutate     func(*Discovery)
		production bool
		want       string
	}{
		{"unknown schema version", func(d *Discovery) { d.SchemaVersion = 2 }, false, "schema_version"},
		{"server differs from --server", func(d *Discovery) { d.Server = "https://other.example.com" }, false, "does not match"},
		{"issuer not server+/oidc", func(d *Discovery) { d.Issuer = origin + "/oauth" }, false, "issuer"},
		{"issuer with trailing slash", func(d *Discovery) { d.Issuer = origin + "/oidc/" }, false, "issuer"},
		{"empty client id", func(d *Discovery) { d.ClientID = "" }, false, "client_id"},
		{"endpoint on another origin", func(d *Discovery) { d.TokenEndpoint = "https://evil.example.com/oidc/device/token" }, false, "token_endpoint"},
		{"endpoint on another port", func(d *Discovery) { d.StatusEndpoint = "https://api.example.com:8443/identity/v1/authenticate" }, false, "status_endpoint"},
		{"endpoint over http", func(d *Discovery) { d.RevocationEndpoint = "http://api.example.com/oidc/revoke" }, false, "revocation_endpoint"},
		{"endpoint with query", func(d *Discovery) { d.DeviceAuthorizationEndpoint = origin + "/oidc/device/authorize?x=1" }, false, "device_authorization_endpoint"},
		{"endpoint with user info", func(d *Discovery) { d.TokenEndpoint = "https://u@api.example.com/oidc/device/token" }, false, "token_endpoint"},
		{"endpoint relative", func(d *Discovery) { d.TokenEndpoint = "/oidc/device/token" }, false, "token_endpoint"},
		{"endpoint missing", func(d *Discovery) { d.RevocationEndpoint = "" }, false, "revocation_endpoint"},
		{"absolute base path", func(d *Discovery) { d.Services["crm"] = Service{BasePath: "https://evil.example.com/crm/v1"} }, false, "base_path"},
		{"base path traversal", func(d *Discovery) { d.Services["crm"] = Service{BasePath: "/crm/../v1"} }, false, "base_path"},
		{"base path without version", func(d *Discovery) { d.Services["crm"] = Service{BasePath: "/crm"} }, false, "base_path"},
		{"base path upper case", func(d *Discovery) { d.Services["crm"] = Service{BasePath: "/CRM/v1"} }, false, "base_path"},
		{"protocol-relative base path", func(d *Discovery) { d.Services["crm"] = Service{BasePath: "//evil.example.com/v1"} }, false, "base_path"},
		{"bad service name", func(d *Discovery) { d.Services["CRM!"] = Service{BasePath: "/crm/v1"} }, false, "service name"},
		{"verification over http", func(d *Discovery) { d.VerificationURI = "http://passport.example.com/device" }, false, "verification_uri"},
		{"verification with query", func(d *Discovery) { d.VerificationURI = "https://passport.example.com/device?x" }, false, "verification_uri"},
		{"complete on other host", func(d *Discovery) {
			d.VerificationURIComplete = "https://evil.example.com/device?user_code=X"
		}, false, "same scheme and host"},
		{"complete not prefixed", func(d *Discovery) {
			d.VerificationURIComplete = "https://passport.example.com/other?user_code=X"
		}, false, "start with"},
		{"production bound to its origin", func(d *Discovery) {}, true, "bound to"},
		{"bad min version", func(d *Discovery) { d.MinCLIVersion = "latest" }, false, "min_cli_version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := custom(origin)
			d.Services = map[string]Service{"crm": {BasePath: "/crm/v1"}}
			tc.mutate(&d)
			err := d.Validate(origin, tc.production)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestVerificationHTTPOnlyForLoopbackServer(t *testing.T) {
	// A loopback development server may use a plain-HTTP approval page.
	const local = "http://127.0.0.1:5000"
	d := custom(local)
	d.VerificationURI = "http://localhost:3000/device"
	if err := d.Validate(local, false); err != nil {
		t.Fatal(err)
	}
	// Any other server must use HTTPS, even for a loopback approval page.
	const remote = "https://api.example.com"
	for _, v := range []string{"http://localhost:3000/device", "http://passport.example.com/device", "ftp://passport.example.com/device"} {
		d := custom(remote)
		d.VerificationURI = v
		if err := d.Validate(remote, false); err == nil {
			t.Errorf("%s accepted for %s", v, remote)
		}
	}
	// A loopback server may not send the person to a remote HTTP page.
	for _, v := range []string{"http://evil.example/device", "http://passport.example.com/device"} {
		d := custom(local)
		d.VerificationURI = v
		if err := d.Validate(local, false); err == nil {
			t.Errorf("%s accepted for the loopback server", v)
		}
	}
	for _, v := range []string{"http://127.0.0.1:3000/device", "http://[::1]:3000/device"} {
		d := custom(local)
		d.VerificationURI = v
		if err := d.Validate(local, false); err != nil {
			t.Errorf("%s refused for the loopback server: %v", v, err)
		}
	}
	if err := CheckVerificationURIs("http://localhost:3000/device", "", "", local, true); err == nil {
		t.Error("production accepted a plain-HTTP approval page")
	}
}

func TestCheckVerificationURIsProduction(t *testing.T) {
	base := "https://" + ProductionVerificationHost + "/device"
	if err := CheckVerificationURIs(base, base+"?user_code=BCDF-GHJK", base, ProductionOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := CheckVerificationURIs("https://passport.example.com/device", "", base, ProductionOrigin, true); err == nil {
		t.Fatal("non-production host accepted for the production profile")
	}
	if err := CheckVerificationURIs(base+"x", "", base, ProductionOrigin, true); err == nil {
		t.Fatal("verification_uri differing from discovery accepted")
	}
}

func TestRequiresNewerCLI(t *testing.T) {
	d := Discovery{MinCLIVersion: "0.2.0"}
	cases := map[string]bool{"0.1.9": true, "v0.1.0": true, "0.2.0": false, "0.10.0": false, "1.0.0": false, "dev": false, "0.2.0-rc.1": true, "0.2.1-rc.1": false}
	for v, want := range cases {
		if got := d.RequiresNewerCLI(v); got != want {
			t.Errorf("RequiresNewerCLI(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestServiceURL(t *testing.T) {
	d := custom("https://api.example.com")
	if u, ok := d.ServiceURL("crm"); !ok || u != "https://api.example.com/crm/v1" {
		t.Fatalf("ServiceURL = %q, %v", u, ok)
	}
	if _, ok := d.ServiceURL("inventory"); ok {
		t.Fatal("unadvertised service resolved")
	}
}
