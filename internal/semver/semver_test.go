package semver

import "testing"

func TestParse(t *testing.T) {
	good := map[string]string{
		"0.1.0":               "0.1.0",
		"v1.2.3":              "1.2.3",
		"1.0.0-rc.1":          "1.0.0-rc.1",
		"1.0.0-alpha-1.0+b.7": "1.0.0-alpha-1.0+b.7",
		"10.20.30":            "10.20.30",
	}
	for in, want := range good {
		v, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if v.String() != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, v.String(), want)
		}
	}
	for _, in := range []string{"", "dev", "(devel)", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "1.2.3+", "1.2.3-a_b", "vv1.2.3", " 1.2.3"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}

// The SemVer 2.0.0 §11 example ordering.
func TestPrecedence(t *testing.T) {
	order := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
	}
	for i := range order {
		for j := range order {
			a, b := MustParse(order[i]), MustParse(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	if Compare(MustParse("1.0.0+a"), MustParse("1.0.0+b")) != 0 {
		t.Error("build metadata must not affect precedence")
	}
	if !Less(MustParse("0.2.0-rc.1"), MustParse("0.2.0")) {
		t.Error("a pre-release must precede its release")
	}
}
