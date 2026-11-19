// Package buildinfo holds the release metadata stamped at build time with
// -ldflags "-X github.com/gatherdeal/cli/internal/buildinfo.Version=…" and
// derives the version every other package reports and sends.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"sync"

	"github.com/gatherdeal/cli/internal/semver"
)

// Set by the release build only. Never read Version directly: use
// CurrentVersion, which also covers `go install …@vX.Y.Z` builds.
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// ModulePath is the Go module path of the CLI.
const ModulePath = "github.com/gatherdeal/cli"

// DevVersion is the version of a build that is neither a release nor a
// tagged `go install`.
const DevVersion = "dev"

// Source says where the version came from.
type Source string

const (
	// SourceRelease: release ldflags stamped the version.
	SourceRelease Source = "release"
	// SourceModule: Go module build info names a tagged version
	// (`go install github.com/gatherdeal/cli/cmd/gatherdeal@vX.Y.Z`).
	SourceModule Source = "module"
	// SourceDev: a local or untagged build; the version is "dev".
	SourceDev Source = "dev"
)

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Source    Source `json:"source"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get returns the build metadata, falling back to the VCS stamp of a plain
// `go build` for the commit.
func Get(goos, goarch, goVersion string) Info {
	v, src := Current()
	info := Info{Version: v, Source: src, Commit: Commit, Date: Date, GoVersion: goVersion, OS: goos, Arch: goarch}
	if info.Commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					info.Commit = s.Value
				}
			}
		}
	}
	return info
}

var current = sync.OnceValues(func() (string, Source) {
	bi, ok := debug.ReadBuildInfo()
	return resolve(Version, bi, ok)
})

// Current returns the version of the running binary and its source. It is
// derived once: release ldflags if set; otherwise the tagged module
// version of a `go install …@vX.Y.Z` build; otherwise "dev".
func Current() (string, Source) { return current() }

// CurrentVersion is the version part of Current.
func CurrentVersion() string {
	v, _ := Current()
	return v
}

// ModuleVersion returns the main module version from Go build info
// ("(devel)" for a local build), or "" when there is none.
func ModuleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Path != ModulePath {
		return ""
	}
	return bi.Main.Version
}

// pseudoVersionRE matches the timestamp-and-commit tail of a Go
// pseudo-version (v0.1.3-0.20261024055047-c9d1a9258d92).
var pseudoVersionRE = regexp.MustCompile(`(^|[-.])\d{14}-[0-9a-f]{12}$`)

func resolve(ldflags string, bi *debug.BuildInfo, ok bool) (string, Source) {
	if ldflags != "" {
		return ldflags, SourceRelease
	}
	if !ok || bi == nil || bi.Main.Path != ModulePath {
		return DevVersion, SourceDev
	}
	v, err := semver.Parse(bi.Main.Version)
	if err != nil || v.Build != "" || pseudoVersionRE.MatchString(bi.Main.Version) {
		// "(devel)", a pseudo-version (untagged commit) or "+dirty".
		return DevVersion, SourceDev
	}
	return v.String(), SourceModule
}

// UserAgent is the User-Agent of every API request: an RFC 9110 product
// token the server's minimum-version gate reads.
func UserAgent() string { return "gatherdeal-cli/" + CurrentVersion() }
