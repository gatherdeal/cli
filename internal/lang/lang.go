// Package lang chooses the language the CLI asks GatherDeal servers to
// write problem text (title, detail) in. The server localizes; the CLI only
// picks one of the served locales and sends it as Accept-Language.
package lang

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// Default is the language served when nothing else matches. English is the
// only locale that is always complete.
const Default = "en"

// Supported are the locales GatherDeal servers write problem text in.
var Supported = []string{"en", "ms", "zh-Hans", "zh-Hant"}

// EnvVars are the standard POSIX locale variables the CLI reads when --lang
// is not given, first set wins.
var EnvVars = []string{"LC_ALL", "LC_MESSAGES", "LANG"}

// ErrInvalid reports a value that is neither a language tag nor an
// Accept-Language list.
var ErrInvalid = errors.New("not a language tag or Accept-Language list")

type weighted struct {
	tag string
	q   float64
}

// Lookup returns the supported locale a server serves for an
// Accept-Language value (a single tag such as "ms-MY", or a weighted list
// such as "fr, ms;q=0.9"), with the server's matching rules: an RFC 4647
// §3.4 lookup over the ranges by descending q, the zh aliases first, then
// progressive truncation, skipping locales excluded with q=0; "*" counts
// only as the last range; English when nothing matches. An unparsable value
// returns ErrInvalid (a server serves English for it).
func Lookup(accept string) (string, error) {
	ranges, err := parse(accept)
	if err != nil {
		return Default, err
	}
	excluded := map[string]bool{}
	var kept []weighted
	for _, r := range ranges {
		if r.q == 0 {
			if loc, ok := match(r.tag); ok {
				excluded[loc] = true
			}
			continue
		}
		kept = append(kept, r)
	}
	slices.SortStableFunc(kept, func(a, b weighted) int {
		switch {
		case a.q > b.q:
			return -1
		case a.q < b.q:
			return 1
		}
		return 0
	})
	for i, r := range kept {
		if r.tag == "*" {
			if i == len(kept)-1 {
				return Default, nil
			}
			continue
		}
		if loc, ok := match(r.tag); ok && !excluded[loc] {
			return loc, nil
		}
	}
	return Default, nil
}

// match maps one language range to a supported locale: the zh aliases
// first, then truncation from the end ("ms-MY" → "ms").
func match(tag string) (string, bool) {
	lower := strings.ToLower(tag)
	switch {
	case lower == "zh" || lower == "zh-cn" || lower == "zh-sg" || lower == "zh-hans" || strings.HasPrefix(lower, "zh-hans-"):
		return "zh-Hans", true
	case lower == "zh-tw" || lower == "zh-hk" || lower == "zh-mo" || lower == "zh-hant" || strings.HasPrefix(lower, "zh-hant-"):
		return "zh-Hant", true
	}
	for lower != "" {
		for _, s := range Supported {
			if strings.ToLower(s) == lower {
				return s, true
			}
		}
		i := strings.LastIndexByte(lower, '-')
		if i < 0 {
			break
		}
		lower = lower[:i]
	}
	return "", false
}

// parse splits an Accept-Language value into ranges with their weights.
func parse(accept string) ([]weighted, error) {
	accept = strings.TrimSpace(accept)
	if accept == "" {
		return nil, ErrInvalid
	}
	var out []weighted
	for part := range strings.SplitSeq(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, params, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)
		if !validRange(tag) {
			return nil, ErrInvalid
		}
		q := 1.0
		if params = strings.TrimSpace(params); params != "" {
			name, value, ok := strings.Cut(params, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
				return nil, ErrInvalid
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || v < 0 || v > 1 {
				return nil, ErrInvalid
			}
			q = v
		}
		out = append(out, weighted{tag: tag, q: q})
	}
	if len(out) == 0 {
		return nil, ErrInvalid
	}
	return out, nil
}

// validRange accepts "*" or 1-8 letter primary subtag followed by 1-8
// alphanumeric subtags separated by "-".
func validRange(tag string) bool {
	if tag == "*" {
		return true
	}
	for i, sub := range strings.Split(tag, "-") {
		if len(sub) < 1 || len(sub) > 8 {
			return false
		}
		for _, c := range sub {
			letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			digit := c >= '0' && c <= '9'
			if !letter && (i == 0 || !digit) {
				return false
			}
		}
	}
	return true
}

// FromPOSIX converts a POSIX locale value ("ms_MY.UTF-8", "zh_TW@euro") to
// a language tag ("ms-MY", "zh-TW"); "C" and "POSIX" are English.
func FromPOSIX(v string) string {
	v = strings.TrimSpace(v)
	v, _, _ = strings.Cut(v, "@")
	v, _, _ = strings.Cut(v, ".")
	if v == "C" || v == "POSIX" {
		return Default
	}
	return strings.ReplaceAll(v, "_", "-")
}

// Resolve returns the Accept-Language value to send: the locale for the
// --lang value when flag is non-empty (ErrInvalid if it does not parse),
// otherwise for the first set POSIX locale variable, otherwise English.
func Resolve(flag string, getenv func(string) string) (string, error) {
	if flag != "" {
		return Lookup(flag)
	}
	if getenv != nil {
		for _, name := range EnvVars {
			if v := getenv(name); v != "" {
				loc, _ := Lookup(FromPOSIX(v))
				return loc, nil
			}
		}
	}
	return Default, nil
}
