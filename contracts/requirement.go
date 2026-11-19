package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// KeyRequirement is an operation's permission requirement: every key (all-of)
// or at least one of two or more distinct keys (any-of). Keys are kept sorted
// and deduplicated. The zero value is all-of without keys.
//
// The wire form (manifest format_version 2) is a closed object with exactly
// one member, {"allOf":[...]} or {"anyOf":[...]}. Decoding mirrors the
// producer's (gopkg.gatherdeal.net/utilities/core/permission.KeyRequirement):
// it refuses null, any other member, both members, neither, a malformed key
// and an anyOf with fewer than two distinct keys.
type KeyRequirement struct {
	keys  []string
	anyOf bool
}

// keyPattern is the permission key grammar
// (<service>.<resource>.<action>, lowercase snake case).
var keyPattern = regexp.MustCompile(`^[a-z]+(_[a-z]+)*\.[a-z]+(_[a-z]+)*\.[a-z]+(_[a-z]+)*$`)

// AllOf returns the requirement that every key is held.
func AllOf(keys ...string) (KeyRequirement, error) {
	if err := checkKeys(keys); err != nil {
		return KeyRequirement{}, err
	}
	return KeyRequirement{keys: normalizeKeys(keys)}, nil
}

// AnyOf returns the requirement that at least one of two or more distinct
// keys is held.
func AnyOf(keys ...string) (KeyRequirement, error) {
	normalized := normalizeKeys(keys)
	if len(normalized) < 2 {
		return KeyRequirement{}, errors.New("contracts: anyOf needs at least two distinct keys")
	}
	if err := checkKeys(normalized); err != nil {
		return KeyRequirement{}, err
	}
	return KeyRequirement{keys: normalized, anyOf: true}, nil
}

func checkKeys(keys []string) error {
	for _, key := range keys {
		if !keyPattern.MatchString(key) {
			return fmt.Errorf("contracts: malformed permission key %q", key)
		}
	}
	return nil
}

func normalizeKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := slices.Clone(keys)
	slices.Sort(out)
	return slices.Compact(out)
}

// Keys returns every key the requirement names, sorted; for an any-of
// requirement these are the alternatives.
func (r KeyRequirement) Keys() []string { return slices.Clone(r.keys) }

// IsAnyOf reports whether holding one of the keys suffices.
func (r KeyRequirement) IsAnyOf() bool { return r.anyOf }

type keyRequirementWire struct {
	AllOf *[]string `json:"allOf,omitempty"`
	AnyOf *[]string `json:"anyOf,omitempty"`
}

// MarshalJSON encodes the closed object.
func (r KeyRequirement) MarshalJSON() ([]byte, error) {
	keys := r.Keys()
	if keys == nil {
		keys = []string{}
	}
	var wire keyRequirementWire
	if r.anyOf {
		wire.AnyOf = &keys
	} else {
		wire.AllOf = &keys
	}
	return json.Marshal(wire)
}

// UnmarshalJSON decodes and validates the closed object.
func (r *KeyRequirement) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return errors.New("contracts: permission requirement must be an object")
	}
	member, err := dec.Token()
	if err != nil || (member != "anyOf" && member != "allOf") {
		return errors.New("contracts: permission requirement needs allOf or anyOf")
	}
	var keys []string
	if err := dec.Decode(&keys); err != nil {
		return fmt.Errorf("contracts: permission requirement: %w", err)
	}
	if keys == nil || dec.More() {
		return errors.New("contracts: permission requirement must contain exactly one key array")
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	var req KeyRequirement
	if member == "anyOf" {
		req, err = AnyOf(keys...)
	} else {
		req, err = AllOf(keys...)
	}
	if err == nil {
		*r = req
	}
	return err
}
