package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	mod := func(path, version string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: path, Version: version}}
	}
	cases := []struct {
		name    string
		ldflags string
		bi      *debug.BuildInfo
		want    string
		src     Source
	}{
		{"release ldflags win", "0.1.2", mod(ModulePath, "v0.1.0"), "0.1.2", SourceRelease},
		{"tagged go install", "", mod(ModulePath, "v0.1.2"), "0.1.2", SourceModule},
		{"tagged pre-release", "", mod(ModulePath, "v0.2.0-rc.1"), "0.2.0-rc.1", SourceModule},
		{"local checkout", "", mod(ModulePath, "(devel)"), DevVersion, SourceDev},
		{"pseudo-version", "", mod(ModulePath, "v0.1.3-0.20261024055047-c9d1a9258d92"), DevVersion, SourceDev},
		{"pseudo-version no tag", "", mod(ModulePath, "v0.0.0-20261024055047-c9d1a9258d92"), DevVersion, SourceDev},
		{"dirty VCS build", "", mod(ModulePath, "v0.1.2+dirty"), DevVersion, SourceDev},
		{"other module", "", mod("example.com/other", "v9.9.9"), DevVersion, SourceDev},
		{"no build info", "", nil, DevVersion, SourceDev},
	}
	for _, c := range cases {
		v, src := resolve(c.ldflags, c.bi, c.bi != nil)
		if v != c.want || src != c.src {
			t.Errorf("%s: resolve = (%q, %q), want (%q, %q)", c.name, v, src, c.want, c.src)
		}
	}
}
