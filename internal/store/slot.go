package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// randomIDRE is the format of every random identifier the store persists.
var randomIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func newRandomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Generation names one login attempt and the credential it commits. A
// Generation value is always valid: it comes from NewGeneration or from
// decoding metadata, which rejects anything else.
type Generation string

// NewGeneration returns a fresh random generation.
func NewGeneration() (Generation, error) {
	id, err := newRandomID()
	return Generation(id), err
}

// UnmarshalText accepts only a generation NewGeneration could have made.
func (g *Generation) UnmarshalText(b []byte) error {
	if !randomIDRE.Match(b) {
		return fmt.Errorf("invalid generation %q", b)
	}
	*g = Generation(b)
	return nil
}

// StoreID namespaces the credentials of one config directory. It is random,
// created once and kept in profiles.json, so it survives moving the
// directory and never derives from user input.
type StoreID string

// UnmarshalText accepts only an id EnsureStoreID could have made.
func (s *StoreID) UnmarshalText(b []byte) error {
	if !randomIDRE.Match(b) {
		return fmt.Errorf("invalid store_id %q", b)
	}
	*s = StoreID(b)
	return nil
}

// Slot is where the secrets of one profile of one config directory live.
// Every account is derived from validated components (StoreID, issuer,
// profile name, Generation); none is ever read from metadata as a string.
type Slot struct {
	storeID StoreID
	issuer  string
	profile string
}

// Slot returns the secrets slot of profile under issuer. The config
// directory must have a StoreID (EnsureStoreID under Lock; a loaded file
// with profiles always has one).
func (p *Profiles) Slot(issuer, profile string) (Slot, error) {
	if p.data.StoreID == "" {
		return Slot{}, fmt.Errorf("%s has no store id", p.Root)
	}
	if !ValidProfileName(profile) {
		return Slot{}, fmt.Errorf("invalid profile name %q", profile)
	}
	if issuer == "" || strings.ContainsAny(issuer, "#\x00") {
		return Slot{}, fmt.Errorf("invalid issuer %q", issuer)
	}
	return Slot{storeID: p.data.StoreID, issuer: issuer, profile: profile}, nil
}

func (s Slot) account(kind string, g Generation) string {
	return string(s.storeID) + "#" + s.issuer + "#" + s.profile + "#" + kind + "#" + string(g)
}

// CredentialAccount is the store account of the credential generation g:
// "<store id>#<issuer>#<profile>#credential#<generation>". A credential is
// never overwritten; a renewal writes a new generation.
func (s Slot) CredentialAccount(g Generation) string { return s.account("credential", g) }

// DeviceCodeAccount is where the device code of the login attempt g is
// kept: "<store id>#<issuer>#<profile>#device#<generation>".
func (s Slot) DeviceCodeAccount(g Generation) string { return s.account("device", g) }
