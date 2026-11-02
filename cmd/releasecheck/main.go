// Command releasecheck is the release workflow's tool. It is built from the
// release tag and uses the same internal/update verifier as `gatherdeal
// update`, so a release is checked exactly as clients will check it.
//
//	releasecheck trusted-root [-write] [-out PATH]
//	    Fetch the Sigstore trusted root through TUF. Without -write, exit 1
//	    when the embedded copy is stale; with -write, refresh it.
//	releasecheck manifest -dist DIR -version V [-min-upgrade-from V] [-published-at RFC3339]
//	    Write DIR/release.json from the GoReleaser archives in DIR.
//	    -min-upgrade-from defaults to the lower of releasemeta.MinUpgradeFrom
//	    and V.
//	releasecheck extract -dir DIR -version V -out DIST
//	    Verify like verify, then extract every platform's binary to
//	    DIST/gatherdeal_<os>_<arch>/ for the npm packages, so npm ships
//	    exactly the signed bytes.
//	releasecheck verify -dir DIR -version V [-exec]
//	    Verify a downloaded (draft) release: release.json and checksums.txt
//	    against their Sigstore bundles, pinned identity and version binding
//	    (with the embedded and, when reachable, the TUF trusted root); this
//	    runner's archive size and SHA-256; the extracted binary's SHA-256.
//	    With -exec, run the candidate: `version` must report V, and
//	    `commands` and `agent-context` must succeed.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gatherdeal/cli/internal/releasemeta"
	"github.com/gatherdeal/cli/internal/semver"
	"github.com/gatherdeal/cli/internal/update"
)

// errBadOutput marks a candidate command that exited 0 with wrong output.
var errBadOutput = errors.New("unexpected output")

// trustedMaterial is update.TrustedMaterial; tests replace it to stay
// offline.
var trustedMaterial = update.TrustedMaterial

func main() {
	ctx := context.Background()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "releasecheck:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: releasecheck trusted-root|manifest|verify [flags]")
	}
	switch args[0] {
	case "trusted-root":
		return trustedRoot(ctx, args[1:], stdout)
	case "manifest":
		return manifest(args[1:], stdout)
	case "verify":
		return verifyRelease(ctx, args[1:], stdout)
	case "extract":
		return extractRelease(ctx, args[1:], stdout)
	}
	return fmt.Errorf("unknown subcommand %q", args[0])
}

func trustedRoot(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("trusted-root", flag.ContinueOnError)
	write := fs.Bool("write", false, "refresh the embedded trusted root")
	out := fs.String("out", filepath.Join("internal", "update", "trusted_root.json"), "embedded trusted root path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := update.FetchTrustedRootJSON(ctx, nil)
	if err != nil {
		return err
	}
	if *write {
		if err := os.WriteFile(*out, data, 0o644); err != nil { //nolint:gosec // a public, tracked source file
			return err
		}
		fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", *out, len(data))
		return nil
	}
	if !bytes.Equal(data, update.EmbeddedTrustedRootJSON()) {
		return errors.New("the embedded Sigstore trusted root is stale; run `go run ./cmd/releasecheck trusted-root -write` and commit it")
	}
	fmt.Fprintln(stdout, "embedded Sigstore trusted root is current")
	return nil
}

func manifest(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "GoReleaser dist directory")
	version := fs.String("version", "", "release version (no leading v)")
	minFrom := fs.String("min-upgrade-from", "", "oldest version that may update in place")
	publishedAt := fs.String("published-at", "", "RFC 3339 publication time (default: now)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return errors.New("manifest: -version is required")
	}
	if *minFrom == "" {
		*minFrom = releasemeta.MinUpgradeFrom
		if v, err := semver.Parse(*version); err == nil && semver.Less(v, semver.MustParse(*minFrom)) {
			*minFrom = v.String()
		}
	}
	at := time.Now()
	if *publishedAt != "" {
		t, err := time.Parse(time.RFC3339, *publishedAt)
		if err != nil {
			return fmt.Errorf("-published-at: %w", err)
		}
		at = t
	}
	m, err := releasemeta.Generate(*dist, *version, at, *minFrom)
	if err != nil {
		return err
	}
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	path := filepath.Join(*dist, releasemeta.ManifestName)
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a public release asset
		return err
	}
	fmt.Fprintf(stdout, "wrote %s for %s (%d assets)\n", path, m.Version, len(m.Assets))
	return nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// loadVerified reads release.json, checksums.txt and their bundles from dir
// and verifies them for version with the embedded trusted root and, when
// reachable, the TUF one: offline clients use the former, online ones the
// latter, so the release must verify with both.
func loadVerified(ctx context.Context, dir, version string, stdout io.Writer) (*releasemeta.Manifest, error) {
	read := func(name string) ([]byte, error) {
		return readBounded(filepath.Join(dir, name), releasemeta.MaxManifestBytes)
	}
	var files [4][]byte
	for i, name := range []string{
		releasemeta.ManifestName, releasemeta.ManifestName + releasemeta.BundleSuffix,
		releasemeta.ChecksumsName, releasemeta.ChecksumsName + releasemeta.BundleSuffix,
	} {
		data, err := read(name)
		if err != nil {
			return nil, err
		}
		files[i] = data
	}
	embedded, err := update.EmbeddedTrustedRoot()
	if err != nil {
		return nil, fmt.Errorf("embedded trusted root: %w", err)
	}
	verifiers := map[string]*update.Verifier{update.TrustSourceEmbedded: update.NewVerifier(embedded)}
	if tufRoot, src, err := trustedMaterial(ctx, nil); err == nil && src == update.TrustSourceTUF {
		verifiers[update.TrustSourceTUF] = update.NewVerifier(tufRoot)
	} else {
		fmt.Fprintln(stdout, "warning: Sigstore TUF repository unreachable; verified with the embedded trusted root only")
	}
	var m *releasemeta.Manifest
	for src, v := range verifiers {
		got, err := v.VerifyRelease(files[0], files[1], version)
		if err != nil {
			return nil, fmt.Errorf("release.json (%s trusted root): %w", src, err)
		}
		if err := v.VerifyBlob(files[2], files[3], version); err != nil {
			return nil, fmt.Errorf("checksums.txt (%s trusted root): %w", src, err)
		}
		m = got
	}
	if err := checkChecksumsList(files[2], m); err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "verified release.json and checksums.txt of %s\n", m.Version)
	return m, nil
}

func verifyRelease(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory holding the downloaded release assets")
	version := fs.String("version", "", "expected release version (no leading v)")
	runIt := fs.Bool("exec", false, "execute the extracted candidate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *version == "" {
		return errors.New("verify: -dir and -version are required")
	}
	m, err := loadVerified(ctx, *dir, *version, stdout)
	if err != nil {
		return err
	}
	asset, ok := m.Asset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("release.json has no asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	tmp, err := os.MkdirTemp("", "releasecheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, releasemeta.BinaryName(runtime.GOOS))
	if err := extractAsset(*dir, asset, bin); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "verified %s and its binary\n", asset.Name)
	if !*runIt {
		return nil
	}
	return execCandidate(ctx, bin, m.Version, stdout)
}

func extractRelease(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory holding the downloaded release assets")
	version := fs.String("version", "", "expected release version (no leading v)")
	out := fs.String("out", "dist", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *version == "" {
		return errors.New("extract: -dir and -version are required")
	}
	m, err := loadVerified(ctx, *dir, *version, stdout)
	if err != nil {
		return err
	}
	for _, a := range m.Assets {
		target := filepath.Join(*out, "gatherdeal_"+a.OS+"_"+a.Arch)
		if err := os.MkdirAll(target, 0o750); err != nil {
			return err
		}
		if err := extractAsset(*dir, a, filepath.Join(target, releasemeta.BinaryName(a.OS))); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "extracted %d verified binaries to %s\n", len(m.Assets), *out)
	return nil
}

// extractAsset checks an archive against the manifest and extracts its
// verified binary to dest.
func extractAsset(dir string, a releasemeta.Asset, dest string) error {
	archive := filepath.Join(dir, a.Name)
	if err := update.CheckFile(archive, a.Size, a.SHA256); err != nil {
		return err
	}
	return update.ExtractVerified(archive, a.OS, dest, a.BinarySHA256)
}

// checkChecksumsList requires checksums.txt to list every archive with the
// manifest's hash, so the human-facing file agrees with release.json.
func checkChecksumsList(checksums []byte, m *releasemeta.Manifest) error {
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			listed[f[1]] = f[0]
		}
	}
	for _, a := range m.Assets {
		if listed[a.Name] != a.SHA256 {
			return fmt.Errorf("checksums.txt disagrees with release.json on %s", a.Name)
		}
	}
	return nil
}

// execCandidate runs the candidate with an empty environment except PATH.
func execCandidate(ctx context.Context, bin, version string, stdout io.Writer) error {
	runCmd := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // the verified candidate
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		cmd.Dir = filepath.Dir(bin)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		err := cmd.Run()
		return out.Bytes(), err
	}
	out, err := runCmd("version")
	if err != nil {
		return fmt.Errorf("candidate `version`: %w", err)
	}
	var env struct {
		Status string `json:"status"`
		Data   struct {
			Version string `json:"version"`
			Source  string `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("candidate `version` output: %w", err)
	}
	if env.Status != "success" || env.Data.Version != version || env.Data.Source != "release" {
		return fmt.Errorf("candidate reports version %q (source %q), want %q from a release build", env.Data.Version, env.Data.Source, version)
	}
	out, err = runCmd("commands")
	if err != nil || !json.Valid(out) || !bytes.Contains(out, []byte(`"status":"success"`)) {
		return fmt.Errorf("candidate `commands` failed: %w", errors.Join(err, errBadOutput))
	}
	out, err = runCmd("agent-context")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("candidate `agent-context` failed: %w", errors.Join(err, errBadOutput))
	}
	fmt.Fprintf(stdout, "candidate %s runs: version, commands, agent-context\n", version)
	return nil
}
