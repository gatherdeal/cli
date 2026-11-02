package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/releasemeta"
)

func hexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// testManifest is a valid manifest whose binaries hash to hexOf("bin-<version>").
func testManifest(t *testing.T, version string) []byte {
	t.Helper()
	m := &releasemeta.Manifest{
		SchemaVersion: releasemeta.SchemaVersion, Version: version, Prerelease: strings.Contains(version, "-"),
		PublishedAt: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), MinUpgradeFrom: "0.0.1",
		NPM: releasemeta.NPM{Package: releasemeta.NPMPackage, Version: version},
	}
	for _, p := range releasemeta.Platforms {
		m.Assets = append(m.Assets, releasemeta.Asset{OS: p[0], Arch: p[1], Name: releasemeta.ArchiveName(version, p[0], p[1]),
			Size: 1000, SHA256: hexOf("archive-" + version + p[0] + p[1]), BinarySHA256: hexOf("bin-" + version)})
	}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakeVerifier accepts a bundle "sig:<sha256 of the manifest>".
type fakeVerifier struct{}

func (fakeVerifier) VerifyRelease(data, bundle []byte, version string) (*releasemeta.Manifest, error) {
	if string(bundle) != "sig:"+hexOf(string(data)) {
		return nil, fmt.Errorf("%w: bad signature", ErrVerify)
	}
	m, err := releasemeta.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerify, err)
	}
	if m.Version != version {
		return nil, fmt.Errorf("%w: version binding", ErrVerify)
	}
	return m, nil
}

// fakeGitHub serves release assets by URL.
type fakeGitHub struct {
	t      *testing.T
	files  map[string]string // URL → body
	status map[string]int
	urls   []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	return &fakeGitHub{t: t, files: map[string]string{}, status: map[string]int{}}
}

// publish adds a release; latest also serves it under /latest/download.
func (g *fakeGitHub) publish(version string, latest bool) {
	data := string(testManifest(g.t, version))
	g.files[DownloadURL(version, "release.json")] = data
	g.files[DownloadURL(version, "release.json.sigstore.json")] = "sig:" + hexOf(data)
	if latest {
		g.files[ReleasesURL+"/latest/download/release.json"] = data
	}
}

func (g *fakeGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		g.t.Errorf("credential sent to %s", r.URL)
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "gatherdeal-cli/") {
		g.t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
	}
	u := r.URL.String()
	g.urls = append(g.urls, u)
	resp := &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}
	if st, ok := g.status[u]; ok {
		resp.StatusCode = st
		if loc, ok := g.files[u]; ok && st >= 300 && st < 400 {
			resp.Header.Set("Location", loc)
		}
		return resp, nil
	}
	if body, ok := g.files[u]; ok {
		resp.StatusCode = http.StatusOK
		resp.Body = io.NopCloser(strings.NewReader(body))
	}
	return resp, nil
}

// testHost is a Linux host running a stamped release binary.
func testHost(version, exe string) Host {
	return Host{
		GOOS: "linux", GOARCH: "amd64", Version: version, Source: buildinfo.SourceRelease,
		Executable:   func() (string, error) { return exe, nil },
		EvalSymlinks: func(p string) (string, error) { return p, nil },
		Getenv:       func(string) string { return "" },
		HomeDir:      func() (string, error) { return "/home/u", nil },
		OwnedByUser:  func(string) bool { return true },
		Writable:     func(string) bool { return true },
		FileSHA256:   func(string) (string, error) { return hexOf("bin-" + version), nil },
	}
}

func withLauncher(h Host) Host {
	h.Getenv = func(k string) string {
		if k == EnvLauncher {
			return LauncherNPM
		}
		return ""
	}
	return h
}

func TestDetect(t *testing.T) {
	const pkg = "/node_modules/@gatherdeal/cli/node_modules/@gatherdeal/cli-linux-x64/bin/gatherdeal"
	win := func(h Host) Host {
		h.GOOS = "windows"
		return h
	}
	cases := []struct {
		name    string
		host    Host
		want    Method
		project string
		raw     bool
	}{
		{"npm global", withLauncher(testHost("0.1.0", "/usr/local/lib"+pkg)), MethodNPMGlobal, "", false},
		{"npm global under nvm", withLauncher(testHost("0.1.0", "/home/u/.nvm/versions/node/v22.0.0/lib"+pkg)), MethodNPMGlobal, "", false},
		{"npm path without launcher", testHost("0.1.0", "/usr/local/lib"+pkg), MethodUnknown, "", false},
		{"npm local", withLauncher(testHost("0.1.0", "/work/shop"+pkg)), MethodNPMLocal, filepath.FromSlash("/work/shop"), false},
		{"npx", withLauncher(testHost("0.1.0", "/home/u/.npm/_npx/1a2b"+pkg)), MethodNPX, "", false},
		{"pnpm", withLauncher(testHost("0.1.0", "/home/u/.local/share/pnpm/global/5/node_modules/.pnpm/x/node_modules/@gatherdeal/cli-linux-x64/bin/gatherdeal")), MethodUnknown, "", false},
		{"npm global windows", win(withLauncher(testHost("0.1.0", `C:\Users\u\AppData\Roaming\npm\node_modules\@gatherdeal\cli\node_modules\@gatherdeal\cli-win32-x64\bin\gatherdeal.exe`))), MethodNPMGlobal, "", false},
		{"documented raw location", testHost("0.1.0", "/home/u/.local/bin/gatherdeal"), MethodUnknown, "", true},
		{"other location", testHost("0.1.0", "/opt/homebrew/bin/gatherdeal"), MethodUnknown, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.host.GOOS == "windows" && filepath.Separator != '\\' {
				t.Skip("Windows paths need the Windows path package")
			}
			got := Detect(c.host)
			if got.Method != c.want || got.ProjectDir != c.project || got.RawCandidate() != c.raw {
				t.Fatalf("got %+v raw=%v, want %s project %q raw=%v", got, got.RawCandidate(), c.want, c.project, c.raw)
			}
		})
	}

	h := testHost("0.1.0", "/usr/local/bin/gatherdeal")
	h.Writable = func(string) bool { return false }
	if got := Detect(h); got.Method != MethodUnwritable {
		t.Errorf("unwritable: %s", got.Method)
	}
	h = testHost("0.1.0", "/home/u/.local/bin/gatherdeal")
	h.OwnedByUser = func(p string) bool { return p != "/home/u/.local/bin/gatherdeal" }
	if got := Detect(h); got.RawCandidate() {
		t.Error("a binary owned by another user is a raw candidate")
	}
	h = testHost("dev", "/src/cli/gatherdeal")
	h.Source = buildinfo.SourceDev
	if got := Detect(h); got.Method != MethodDev {
		t.Errorf("dev: %s", got.Method)
	}
	h.Source = buildinfo.SourceModule
	if got := Detect(h); got.Method != MethodGoInstall {
		t.Errorf("go install: %s", got.Method)
	}
	if filepath.Separator == '\\' {
		return
	}
	// The home directory behind a symlink (/home → /var/home).
	h = testHost("0.1.0", "/var/home/u/.local/bin/gatherdeal")
	h.EvalSymlinks = func(p string) (string, error) { return strings.Replace(p, "/home/u", "/var/home/u", 1), nil }
	h.Executable = func() (string, error) { return "/home/u/.local/bin/gatherdeal", nil }
	if got := Detect(h); !got.RawCandidate() {
		t.Error("symlinked home: not a raw candidate")
	}
}

func TestConfirm(t *testing.T) {
	h := testHost("0.1.0", "/home/u/.local/bin/gatherdeal")
	inst := Detect(h)
	own, err := releasemeta.Parse(testManifest(t, "0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Confirm(h, inst, own); got.Method != MethodReleaseBinary || got.RawCandidate() {
		t.Fatalf("matching hash: %+v", got)
	}
	h.FileSHA256 = func(string) (string, error) { return hexOf("something else"), nil }
	if got := Confirm(h, inst, own); got.Method != MethodUnknown {
		t.Fatalf("other hash: %s", got.Method)
	}
	if got := Confirm(h, inst, nil); got.Method != MethodUnknown {
		t.Fatalf("no manifest: %s", got.Method)
	}
}

func newChecker(g *fakeGitHub, h Host) *Checker {
	return &Checker{Host: h, Releases: NewReleases(g), Verifier: fakeVerifier{}, TrustedRoot: TrustSourceEmbedded}
}

func TestCheckLatest(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish("0.1.2", true)
	p, err := newChecker(g, withLauncher(testHost("0.1.0", "/usr/local/lib/node_modules/@gatherdeal/cli-linux-x64/bin/gatherdeal"))).Check(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateUpdateAvailable || !p.UpdateAvailable || p.LatestVersion != "0.1.2" || p.TargetVersion != "0.1.2" || !p.Verified {
		t.Fatalf("plan %+v", p)
	}
	if p.InstallMethod != MethodNPMGlobal || p.Action.Kind != ActionDelegate ||
		strings.Join(p.Action.Argv, " ") != "npm install -g @gatherdeal/cli@0.1.2 --ignore-scripts" {
		t.Fatalf("action %+v", p.Action)
	}
	if p.ReleaseURL != "https://github.com/gatherdeal/cli/releases/tag/v0.1.2" {
		t.Fatalf("release url %s", p.ReleaseURL)
	}
	// The bundle comes from the exact version the latest manifest names.
	want := []string{ReleasesURL + "/latest/download/release.json", DownloadURL("0.1.2", "release.json.sigstore.json")}
	if strings.Join(g.urls, " ") != strings.Join(want, " ") {
		t.Fatalf("requests %v", g.urls)
	}
}

func TestCheckRawBinaryConfirmedByItsOwnRelease(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish("0.1.0", false)
	g.publish("0.1.2", true)
	p, err := newChecker(g, testHost("0.1.0", "/home/u/.local/bin/gatherdeal")).Check(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.InstallMethod != MethodReleaseBinary || p.Action.Kind != ActionManual ||
		p.Action.DownloadURL != DownloadURL("0.1.2", "gatherdeal_0.1.2_linux_amd64.tar.gz") || p.Action.SHA256 == "" {
		t.Fatalf("plan %+v", p)
	}
	// Without its own release, the same binary is unknown (manual).
	delete(g.files, DownloadURL("0.1.0", "release.json"))
	p, err = newChecker(g, testHost("0.1.0", "/home/u/.local/bin/gatherdeal")).Check(context.Background(), "")
	if err != nil || p.InstallMethod != MethodUnknown || p.Action.Kind != ActionManual {
		t.Fatalf("plan %+v, %v", p, err)
	}
}

func TestCheckStates(t *testing.T) {
	g := newFakeGitHub(t)
	g.publish("0.1.0", true)
	g.publish("0.0.9", false)
	g.publish("0.2.0-rc.1", false)
	h := withLauncher(testHost("0.1.0", "/usr/local/lib/node_modules/@gatherdeal/cli-linux-x64/bin/gatherdeal"))
	ctx := context.Background()
	p, err := newChecker(g, h).Check(ctx, "")
	if err != nil || p.State != StateUpToDate || p.Action.Kind != ActionNone || p.UpdateAvailable {
		t.Fatalf("up to date: %+v %v", p, err)
	}
	p, err = newChecker(g, h).Check(ctx, "0.0.9")
	if err != nil || p.State != StateDowngrade || p.LatestVersion != "" || p.Action.Kind != ActionDelegate {
		t.Fatalf("downgrade: %+v %v", p, err)
	}
	p, err = newChecker(g, h).Check(ctx, "0.2.0-rc.1")
	if err != nil || p.State != StateUpdateAvailable || !p.Prerelease {
		t.Fatalf("explicit pre-release: %+v %v", p, err)
	}
	h.Version = "0.3.0"
	p, err = newChecker(g, h).Check(ctx, "")
	if err != nil || p.State != StateAheadOfLatest || p.Action.Kind != ActionNone {
		t.Fatalf("ahead: %+v %v", p, err)
	}
	h.Version, h.Source = "dev", buildinfo.SourceDev
	h.Executable = func() (string, error) { return "/src/gatherdeal", nil }
	p, err = newChecker(g, h).Check(ctx, "")
	if err != nil || p.State != StateDevBuild || p.InstallMethod != MethodDev || p.Action.Kind != ActionManual {
		t.Fatalf("dev: %+v %v", p, err)
	}
}

func TestCheckFailures(t *testing.T) {
	ctx := context.Background()
	h := testHost("0.1.0", "/opt/bin/gatherdeal")

	g := newFakeGitHub(t)
	if _, err := newChecker(g, h).Check(ctx, "9.9.9"); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("missing version: %v", err)
	}

	g = newFakeGitHub(t)
	g.publish("0.1.2", true)
	g.files[DownloadURL("0.1.2", "release.json.sigstore.json")] = "sig:forged"
	if _, err := newChecker(g, h).Check(ctx, ""); !errors.Is(err, ErrVerify) {
		t.Errorf("bad signature: %v", err)
	}

	// A latest manifest claiming another version than its signature's tag.
	g = newFakeGitHub(t)
	g.publish("0.1.2", false)
	g.publish("0.1.3", false)
	g.files[ReleasesURL+"/latest/download/release.json"] = g.files[DownloadURL("0.1.2", "release.json")]
	g.files[DownloadURL("0.1.2", "release.json.sigstore.json")] = g.files[DownloadURL("0.1.3", "release.json.sigstore.json")]
	if _, err := newChecker(g, h).Check(ctx, ""); !errors.Is(err, ErrVerify) {
		t.Errorf("swapped signature: %v", err)
	}

	g = newFakeGitHub(t)
	g.publish("0.2.0-rc.1", true)
	if _, err := newChecker(g, h).Check(ctx, ""); !errors.Is(err, ErrLatestIsPrerelease) {
		t.Errorf("pre-release latest: %v", err)
	}

	g = newFakeGitHub(t)
	g.status[ReleasesURL+"/latest/download/release.json"] = http.StatusInternalServerError
	var netErr *NetworkError
	if _, err := newChecker(g, h).Check(ctx, ""); !errors.As(err, &netErr) {
		t.Errorf("5xx: %v", err)
	}
}

func TestReleaseRedirects(t *testing.T) {
	ctx := context.Background()
	h := testHost("0.1.0", "/opt/bin/gatherdeal")
	latest := ReleasesURL + "/latest/download/release.json"

	// GitHub redirects downloads to its asset host: followed.
	g := newFakeGitHub(t)
	g.publish("0.1.2", false)
	asset := "https://release-assets.githubusercontent.com/x/release.json"
	g.status[latest] = http.StatusFound
	g.files[latest] = asset
	g.files[asset] = g.files[DownloadURL("0.1.2", "release.json")]
	if _, err := newChecker(g, h).Check(ctx, ""); err != nil {
		t.Fatalf("asset redirect: %v", err)
	}

	// Anywhere else, or plain HTTP: refused.
	for _, target := range []string{"https://evil.example/release.json", "http://release-assets.githubusercontent.com/x/release.json"} {
		g = newFakeGitHub(t)
		g.status[latest] = http.StatusFound
		g.files[latest] = target
		var netErr *NetworkError
		if _, err := newChecker(g, h).Check(ctx, ""); !errors.As(err, &netErr) {
			t.Errorf("redirect to %s: %v", target, err)
		}
		for _, u := range g.urls {
			if u == target {
				t.Errorf("request sent to %s", target)
			}
		}
	}

	// More than three redirects: refused.
	g = newFakeGitHub(t)
	hops := []string{latest, "https://github.com/a", "https://github.com/b", "https://github.com/c", "https://github.com/d"}
	for i := 0; i+1 < len(hops); i++ {
		g.status[hops[i]] = http.StatusFound
		g.files[hops[i]] = hops[i+1]
	}
	var netErr *NetworkError
	if _, err := newChecker(g, h).Check(ctx, ""); !errors.As(err, &netErr) {
		t.Errorf("redirect chain: %v", err)
	}
}
