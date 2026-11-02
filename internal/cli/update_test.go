package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/releasemeta"
	"github.com/gatherdeal/cli/internal/update"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// releaseFixture is a fake GitHub release host plus a fake verifier that
// accepts the bundle "sig:<sha256 of release.json>".
type releaseFixture struct {
	t        *testing.T
	files    map[string]string
	requests []string
}

func newReleaseFixture(t *testing.T, latest string, versions ...string) *releaseFixture {
	f := &releaseFixture{t: t, files: map[string]string{}}
	for _, v := range append(versions, latest) {
		m := &releasemeta.Manifest{
			SchemaVersion: 1, Version: v, Prerelease: strings.Contains(v, "-"),
			PublishedAt: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), MinUpgradeFrom: "0.0.1",
			NPM: releasemeta.NPM{Package: releasemeta.NPMPackage, Version: v},
		}
		for _, p := range releasemeta.Platforms {
			m.Assets = append(m.Assets, releasemeta.Asset{OS: p[0], Arch: p[1], Name: releasemeta.ArchiveName(v, p[0], p[1]),
				Size: 10, SHA256: sha("a" + v + p[0] + p[1]), BinarySHA256: sha("b" + v)})
		}
		data, err := m.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		f.files[update.DownloadURL(v, "release.json")] = string(data)
		f.files[update.DownloadURL(v, "release.json.sigstore.json")] = "sig:" + sha(string(data))
		if v == latest {
			f.files[update.ReleasesURL+"/latest/download/release.json"] = string(data)
		}
	}
	return f
}

func (f *releaseFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "" {
		f.t.Errorf("credential sent to %s", r.URL)
	}
	f.requests = append(f.requests, r.URL.String())
	body, ok := f.files[r.URL.String()]
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
	if !ok {
		resp.StatusCode = http.StatusNotFound
	}
	return resp, nil
}

type fixtureVerifier struct{}

func (fixtureVerifier) VerifyRelease(data, bundle []byte, version string) (*releasemeta.Manifest, error) {
	if string(bundle) != "sig:"+sha(string(data)) {
		return nil, fmt.Errorf("%w: signature does not match", update.ErrVerify)
	}
	m, err := releasemeta.Parse(data)
	if err != nil || m.Version != version {
		return nil, fmt.Errorf("%w: bad manifest", update.ErrVerify)
	}
	return m, nil
}

// npmGlobalHost runs version from a global npm install.
func npmGlobalHost(version string) *update.Host {
	return &update.Host{
		GOOS: "linux", GOARCH: "amd64", Version: version, Source: buildinfo.SourceRelease,
		Executable: func() (string, error) {
			return "/usr/lib/node_modules/@gatherdeal/cli/node_modules/@gatherdeal/cli-linux-x64/bin/gatherdeal", nil
		},
		EvalSymlinks: func(p string) (string, error) { return p, nil },
		Getenv: func(k string) string {
			if k == update.EnvLauncher {
				return update.LauncherNPM
			}
			return ""
		},
		HomeDir:     func() (string, error) { return "/home/u", nil },
		OwnedByUser: func(string) bool { return true },
		Writable:    func(string) bool { return true },
		FileSHA256:  func(string) (string, error) { return "", nil },
	}
}

func runUpdateCLI(t *testing.T, fx *releaseFixture, host *update.Host, interactive bool, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := &Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Getenv:      func(string) string { return "" },
		Interactive: interactive,
		HTTPBase:    fx,
		UpdateHost:  host,
		UpdateVerifier: func(context.Context) (update.ManifestVerifier, string, error) {
			return fixtureVerifier{}, update.TrustSourceEmbedded, nil
		},
	}
	exit := Run(context.Background(), env, append([]string{"update"}, args...))
	res := result{exit: exit, stdout: stdout.String(), stderr: stderr.String()}
	if err := json.Unmarshal(stdout.Bytes(), &res.env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, stdout.String())
	}
	return res
}

func TestUpdateCheck(t *testing.T) {
	fx := newReleaseFixture(t, "0.1.2", "0.1.0")
	res := runUpdateCLI(t, fx, npmGlobalHost("0.1.0"), false, "--check")
	if res.exit != 0 {
		t.Fatalf("exit %d: %s", res.exit, res.stdout)
	}
	d := res.data()
	if d["current_version"] != "0.1.0" || d["latest_version"] != "0.1.2" || d["update_available"] != true ||
		d["state"] != "update_available" || d["verified"] != true || d["install_method"] != "npm_global" ||
		d["release_url"] != "https://github.com/gatherdeal/cli/releases/tag/v0.1.2" {
		t.Fatalf("data %v", d)
	}
	action, _ := d["action"].(map[string]any)
	argv, _ := json.Marshal(action["argv"])
	if action["kind"] != "delegate" || string(argv) != `["npm","install","-g","@gatherdeal/cli@0.1.2","--ignore-scripts"]` {
		t.Fatalf("action %v", action)
	}
}

func TestUpdateApplyGuides(t *testing.T) {
	fx := newReleaseFixture(t, "0.1.2", "0.1.0", "0.0.9")
	host := npmGlobalHost("0.1.0")

	// Package-managed: the command to run, exit 9, nothing executed.
	res := runUpdateCLI(t, fx, host, true)
	if res.exit != 9 || res.errorCode() != "CLI_UPDATE_ACTION_REQUIRED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	details, _ := res.env["error"].(map[string]any)["details"].(map[string]any)
	if details["target_version"] != "0.1.2" || details["action"].(map[string]any)["kind"] != "delegate" {
		t.Fatalf("details %v", details)
	}
	if !strings.Contains(res.env["hint"].(string), "argv") {
		t.Fatalf("hint %v", res.env["hint"])
	}

	// Already current: exit 0.
	res = runUpdateCLI(t, fx, npmGlobalHost("0.1.2"), false)
	if res.exit != 0 || res.data()["state"] != "up_to_date" {
		t.Fatalf("up to date: exit %d %v", res.exit, res.stdout)
	}

	// A downgrade needs --allow-downgrade.
	res = runUpdateCLI(t, fx, host, false, "--version", "0.0.9")
	if res.exit != 9 || !strings.Contains(res.env["hint"].(string), "--allow-downgrade") {
		t.Fatalf("downgrade: exit %d %s", res.exit, res.stdout)
	}
	res = runUpdateCLI(t, fx, host, false, "--version", "v0.0.9", "--allow-downgrade")
	if res.exit != 9 || res.errorCode() != "CLI_UPDATE_ACTION_REQUIRED" {
		t.Fatalf("allowed downgrade: exit %d %s", res.exit, res.stdout)
	}
	details, _ = res.env["error"].(map[string]any)["details"].(map[string]any)
	if details["state"] != "downgrade" || details["target_version"] != "0.0.9" {
		t.Fatalf("details %v", details)
	}
}

func TestUpdateErrors(t *testing.T) {
	fx := newReleaseFixture(t, "0.1.2", "0.1.0")
	host := npmGlobalHost("0.1.0")
	cases := []struct {
		name string
		args []string
		exit int
		code string
	}{
		{"malformed version", []string{"--version", "latest"}, 2, "CLI_USAGE"},
		{"build metadata", []string{"--version", "0.1.2+x"}, 2, "CLI_USAGE"},
		{"removed yes flag", []string{"--yes"}, 2, "CLI_USAGE"},
		{"removed yes with version", []string{"--version", "0.1.2", "--yes"}, 2, "CLI_USAGE"},
		{"downgrade flag without version", []string{"--allow-downgrade"}, 2, "CLI_USAGE"},
		{"unknown version", []string{"--check", "--version", "9.9.9"}, 5, "CLI_UPDATE_VERSION_NOT_FOUND"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := runUpdateCLI(t, fx, host, false, c.args...)
			if res.exit != c.exit || res.errorCode() != c.code {
				t.Fatalf("exit %d code %s: %s", res.exit, res.errorCode(), res.stdout)
			}
		})
	}

	// A failed verification yields no plan: no argv, no URL.
	fx.files[update.DownloadURL("0.1.2", "release.json.sigstore.json")] = "sig:forged"
	res := runUpdateCLI(t, fx, host, false, "--check")
	if res.exit != 1 || res.errorCode() != "CLI_UPDATE_VERIFY_FAILED" || res.env["data"] != nil {
		t.Fatalf("verify: exit %d %s", res.exit, res.stdout)
	}
	if strings.Contains(res.stdout, "argv") || strings.Contains(res.stdout, "download_url") {
		t.Fatalf("a plan leaked from an unverified manifest: %s", res.stdout)
	}

	// No release published at all: not found.
	res = runUpdateCLI(t, &releaseFixture{t: t, files: map[string]string{}}, host, false, "--check")
	if res.exit != 5 || res.errorCode() != "CLI_UPDATE_VERSION_NOT_FOUND" {
		t.Fatalf("no release: exit %d %s", res.exit, res.stdout)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestUpdateNetworkError(t *testing.T) {
	var stdout bytes.Buffer
	env := &Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard, Getenv: func(string) string { return "" },
		HTTPBase: failingTransport{}, UpdateHost: npmGlobalHost("0.1.0"),
		UpdateVerifier: func(context.Context) (update.ManifestVerifier, string, error) {
			return fixtureVerifier{}, update.TrustSourceEmbedded, nil
		},
	}
	if exit := Run(context.Background(), env, []string{"update", "--check"}); exit != 8 || !strings.Contains(stdout.String(), `"CLI_NETWORK_ERROR"`) {
		t.Fatalf("exit %d: %s", exit, stdout.String())
	}
}
