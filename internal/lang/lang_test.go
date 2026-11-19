package lang

import (
	"errors"
	"testing"
)

// The servers' locale matching examples, plus aliases and truncation.
func TestLookup(t *testing.T) {
	for in, want := range map[string]string{
		"fr, ms;q=0.9":           "ms",
		"zh-TW":                  "zh-Hant",
		"zh;q=0.8, en;q=0.9":     "en",
		"*, ms;q=0.5":            "ms",
		"ms;q=0, *":              "en",
		"fr":                     "en",
		"ms":                     "ms",
		"ms-MY":                  "ms",
		"en-GB":                  "en",
		"zh-CN":                  "zh-Hans",
		"zh-SG":                  "zh-Hans",
		"zh":                     "zh-Hans",
		"zh-Hans-CN":             "zh-Hans",
		"ZH-HANT":                "zh-Hant",
		"zh-HK":                  "zh-Hant",
		"zh-MO":                  "zh-Hant",
		"*":                      "en",
		"ms;q=0, ms-MY;q=0.9":    "en",
		"fr;q=0.9, zh-TW;q=0.9":  "zh-Hant",
		"en;q=0, ms;q=0.1":       "ms",
		"en;q=0":                 "en",
		" ms ; q=1 , en ; q=0.5": "ms",
	} {
		got, err := Lookup(in)
		if err != nil || got != want {
			t.Errorf("Lookup(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ms;q=2", "ms;x=1", "m$", "toolongtag1", "ms;q=abc", "-ms"} {
		if got, err := Lookup(bad); !errors.Is(err, ErrInvalid) || got != Default {
			t.Errorf("Lookup(%q) = %q, %v; want ErrInvalid", bad, got, err)
		}
	}
}

func TestResolve(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		flag string
		vars map[string]string
		want string
	}{
		{"zh-TW", map[string]string{"LANG": "ms_MY.UTF-8"}, "zh-Hant"},
		{"", map[string]string{"LANG": "ms_MY.UTF-8"}, "ms"},
		{"", map[string]string{"LC_ALL": "zh_CN.UTF-8", "LC_MESSAGES": "ms_MY", "LANG": "en_US"}, "zh-Hans"},
		{"", map[string]string{"LC_MESSAGES": "zh_TW.Big5@x", "LANG": "ms_MY"}, "zh-Hant"},
		{"", map[string]string{"LANG": "C"}, "en"},
		{"", map[string]string{"LANG": "POSIX"}, "en"},
		{"", map[string]string{"LANG": "C.UTF-8"}, "en"},
		{"", map[string]string{"LANG": "fr_FR.UTF-8"}, "en"},
		{"", map[string]string{"LANG": "garbage!"}, "en"},
		{"", nil, "en"},
	}
	for _, tc := range cases {
		got, err := Resolve(tc.flag, env(tc.vars))
		if err != nil || got != tc.want {
			t.Errorf("Resolve(%q, %v) = %q, %v; want %q", tc.flag, tc.vars, got, err, tc.want)
		}
	}
	if _, err := Resolve("not a tag!", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid --lang accepted: %v", err)
	}
}
