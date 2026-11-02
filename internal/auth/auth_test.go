package auth

import (
	"strings"
	"testing"
)

func TestScopeHints(t *testing.T) {
	if got := ScopeHints("", ""); got != "gd.preset:read_only" {
		t.Fatalf("got %q", got)
	}
	if got := ScopeHints("90d", "box-1"); got != "gd.preset:read_only gd.lifetime:90d gd.device:box-1" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeDeviceName(t *testing.T) {
	if got := SanitizeDeviceName("gatherdeal CLI on my\tbox\n\x00é"); got != "gatherdeal-CLI-on-my-box" {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeDeviceName(strings.Repeat("a", 500)); len(got) != 200 {
		t.Fatalf("len %d", len(got))
	}
}

func TestValidLifetime(t *testing.T) {
	for _, l := range []string{"30d", "90d", "180d", "365d", "never"} {
		if !ValidLifetime(l) {
			t.Errorf("%s rejected", l)
		}
	}
	for _, l := range []string{"", "7d", "forever", "30D"} {
		if ValidLifetime(l) {
			t.Errorf("%s accepted", l)
		}
	}
}

func TestDelegatedAccessCheck(t *testing.T) {
	ok := DelegatedAccess{ID: "a", OrganizationID: "o", CredentialKind: "bearer", RestrictionVersion: 1, ClientID: "gatherdeal-cli"}
	if err := ok.check("gatherdeal-cli"); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]DelegatedAccess{
		"restriction version": {ID: "a", OrganizationID: "o", CredentialKind: "bearer", RestrictionVersion: 2, ClientID: "gatherdeal-cli"},
		"credential kind":     {ID: "a", OrganizationID: "o", CredentialKind: "oauth_access", RestrictionVersion: 1, ClientID: "gatherdeal-cli"},
		"client":              {ID: "a", OrganizationID: "o", CredentialKind: "bearer", RestrictionVersion: 1, ClientID: "other"},
		"organization":        {ID: "a", CredentialKind: "bearer", RestrictionVersion: 1, ClientID: "gatherdeal-cli"},
	} {
		if err := d.check("gatherdeal-cli"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
