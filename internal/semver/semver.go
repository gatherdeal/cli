// Package semver parses Semantic Versioning 2.0.0 versions and compares them
// with SemVer precedence, pre-releases included: 0.2.0-rc.1 < 0.2.0.
package semver

import (
	"errors"
	"strconv"
	"strings"
)

// Version is a parsed MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD] version.
type Version struct {
	Major, Minor, Patch uint64
	// Pre holds the dot-separated pre-release identifiers; empty for a
	// release.
	Pre []string
	// Build is the build metadata; it never affects precedence.
	Build string
}

// maxLen bounds the accepted input; real versions are far shorter.
const maxLen = 128

var errInvalid = errors.New("not a semantic version")

// Parse parses s. A single leading "v" is accepted, so Go module versions
// ("v0.1.2") parse too.
func Parse(s string) (Version, error) {
	var v Version
	if len(s) > maxLen {
		return v, errInvalid
	}
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build = s[i+1:]
		s = s[:i]
		if !validIdentifiers(v.Build, false) {
			return Version{}, errInvalid
		}
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre := s[i+1:]
		s = s[:i]
		if !validIdentifiers(pre, true) {
			return Version{}, errInvalid
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, errInvalid
	}
	nums := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if !numeric(p) || (len(p) > 1 && p[0] == '0') {
			return Version{}, errInvalid
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, errInvalid
		}
		*nums[i] = n
	}
	return v, nil
}

// MustParse is Parse for compile-time constants; it panics on bad input.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic("semver: " + s + ": " + err.Error())
	}
	return v
}

// Valid reports whether s parses.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// String formats v without a leading "v".
func (v Version) String() string {
	var b strings.Builder
	b.WriteString(strconv.FormatUint(v.Major, 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(v.Minor, 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(v.Patch, 10))
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.Pre, "."))
	}
	if v.Build != "" {
		b.WriteByte('+')
		b.WriteString(v.Build)
	}
	return b.String()
}

// Prerelease reports whether v has pre-release identifiers.
func (v Version) Prerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or +1 by SemVer precedence. Build metadata is
// ignored.
func Compare(a, b Version) int {
	for _, d := range [3][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareIdentifier(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Less reports whether a has lower precedence than b.
func Less(a, b Version) bool { return Compare(a, b) < 0 }

func compareIdentifier(x, y string) int {
	xn, yn := numeric(x), numeric(y)
	switch {
	case xn && yn:
		if len(x) != len(y) {
			if len(x) < len(y) {
				return -1
			}
			return 1
		}
		return strings.Compare(x, y)
	case xn:
		return -1
	case yn:
		return 1
	}
	return strings.Compare(x, y)
}

func numeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validIdentifiers checks dot-separated [0-9A-Za-z-] identifiers;
// pre-release numeric identifiers must not have leading zeros.
func validIdentifiers(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
				return false
			}
		}
		if pre && numeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}
