package update

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/releasemeta"
)

// Method is how the running CLI was installed; it decides how `gatherdeal
// update` updates it.
type Method string

const (
	MethodNPMGlobal     Method = "npm_global"
	MethodNPMLocal      Method = "npm_local"
	MethodNPX           Method = "npx"
	MethodGoInstall     Method = "go_install"
	MethodDev           Method = "dev"
	MethodReleaseBinary Method = "release_binary"
	MethodUnwritable    Method = "unwritable"
	MethodUnknown       Method = "unknown"
)

// EnvLauncher is set to LauncherNPM by the npm launcher in the binary's
// environment, and only there. It confirms npm path evidence; it never
// selects a method on its own.
const (
	EnvLauncher = "GATHERDEAL_LAUNCHER"
	LauncherNPM = "npm"
)

// Host is what install detection reads about the running process. Tests
// build their own; DefaultHost inspects this process.
type Host struct {
	GOOS, GOARCH string
	// Version and Source describe the running binary (buildinfo.Current).
	Version string
	Source  buildinfo.Source
	// Executable returns the path of the running executable.
	Executable   func() (string, error)
	EvalSymlinks func(string) (string, error)
	Getenv       func(string) string
	HomeDir      func() (string, error)
	// OwnedByUser reports whether path is owned by the current user.
	OwnedByUser func(path string) bool
	// Writable reports whether the current user may create files in dir.
	Writable func(dir string) bool
	// FileSHA256 hashes a file (hex).
	FileSHA256 func(path string) (string, error)
}

// DefaultHost describes this process.
func DefaultHost(getenv func(string) string) Host {
	v, src := buildinfo.Current()
	return Host{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Version: v, Source: src,
		Executable:   os.Executable,
		EvalSymlinks: filepath.EvalSymlinks,
		Getenv:       getenv,
		HomeDir:      os.UserHomeDir,
		OwnedByUser:  ownedByUser,
		Writable:     writableDir,
		FileSHA256:   fileSHA256,
	}
}

// Install is the detected install method.
type Install struct {
	Method Method
	// Path is the resolved path of the running executable ("" if unknown).
	Path string
	// ProjectDir is the project of an npm_local install.
	ProjectDir string
	// rawCandidate marks a stamped release binary at a documented raw-install
	// location, owned by the user. It becomes release_binary only when its
	// SHA-256 equals binary_sha256 of its own verified release.
	rawCandidate bool
}

// RawCandidate reports whether the install needs its own release's manifest
// to be confirmed as release_binary (see Confirm).
func (i Install) RawCandidate() bool { return i.rawCandidate }

// npmBinaryRE matches the binary inside an npm platform package.
var npmBinaryRE = regexp.MustCompile(`/node_modules/@gatherdeal/cli-(linux|darwin|win32)-(x64|arm64)/bin/gatherdeal(\.exe)?$`)

// Detect inspects the running executable. It never guesses from a
// substring alone and never infers a raw install from the absence of
// package-manager evidence.
func Detect(h Host) Install {
	exe, err := h.Executable()
	if err != nil {
		return Install{Method: MethodUnknown}
	}
	resolved, err := h.EvalSymlinks(exe)
	if err != nil {
		return Install{Method: MethodUnknown, Path: exe}
	}
	inst := Install{Method: MethodUnknown, Path: resolved}
	slash := filepath.ToSlash(resolved)
	if npmBinaryRE.MatchString(slash) {
		inst.Method, inst.ProjectDir = detectNPM(h, slash)
		if inst.ProjectDir != "" {
			inst.ProjectDir = filepath.FromSlash(inst.ProjectDir)
		}
		return inst
	}
	switch h.Source {
	case buildinfo.SourceDev:
		inst.Method = MethodDev
		return inst
	case buildinfo.SourceModule:
		inst.Method = MethodGoInstall
		return inst
	}
	// A stamped release binary outside npm.
	dir := filepath.Dir(resolved)
	if !h.Writable(dir) {
		inst.Method = MethodUnwritable
		return inst
	}
	if documentedLocation(h, resolved) && h.OwnedByUser(resolved) && h.OwnedByUser(dir) {
		inst.rawCandidate = true
	}
	return inst
}

// detectNPM classifies an executable inside an npm platform package. The
// launcher marker is required: without it the binary was started directly,
// or by a package manager the CLI does not know.
func detectNPM(h Host, slash string) (Method, string) {
	if h.Getenv(EnvLauncher) != LauncherNPM {
		return MethodUnknown, ""
	}
	segs := strings.Split(slash, "/")
	first := -1
	for i, s := range segs {
		switch s {
		case ".pnpm", ".yarn", ".store":
			return MethodUnknown, ""
		case "_npx":
			if first < 0 {
				return MethodNPX, ""
			}
		case "node_modules":
			if first < 0 {
				first = i
			}
		}
	}
	if first < 1 {
		return MethodUnknown, ""
	}
	parent := segs[first-1]
	if (h.GOOS != "windows" && parent == "lib") || (h.GOOS == "windows" && strings.EqualFold(parent, "npm")) {
		return MethodNPMGlobal, ""
	}
	return MethodNPMLocal, strings.Join(segs[:first], "/")
}

// RawInstallPath is the documented raw-install location of the binary, or
// "" when it cannot be determined.
func RawInstallPath(h Host) string {
	if h.GOOS == "windows" {
		base := h.Getenv("LOCALAPPDATA")
		if base == "" {
			return ""
		}
		return filepath.Join(base, "Programs", "gatherdeal", "gatherdeal.exe")
	}
	home, err := h.HomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "bin", "gatherdeal")
}

func documentedLocation(h Host, resolved string) bool {
	want := RawInstallPath(h)
	if want == "" {
		return false
	}
	// The home directory itself may sit behind a symlink (/home → /var/home).
	if dir, err := h.EvalSymlinks(filepath.Dir(want)); err == nil {
		want = filepath.Join(dir, filepath.Base(want))
	}
	if h.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(resolved), filepath.Clean(want))
	}
	return filepath.Clean(resolved) == filepath.Clean(want)
}

// Confirm settles a raw-install candidate against its own verified
// release: release_binary when the executable's SHA-256 equals that
// release's binary_sha256 for this platform, unknown otherwise.
func Confirm(h Host, inst Install, own *releasemeta.Manifest) Install {
	if !inst.rawCandidate {
		return inst
	}
	inst.rawCandidate = false
	inst.Method = MethodUnknown
	if own == nil || own.Version != h.Version {
		return inst
	}
	asset, ok := own.Asset(h.GOOS, h.GOARCH)
	if !ok {
		return inst
	}
	sum, err := h.FileSHA256(inst.Path)
	if err == nil && equalHex(sum, asset.BinarySHA256) {
		inst.Method = MethodReleaseBinary
	}
	return inst
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, releasemeta.MaxBinaryBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
