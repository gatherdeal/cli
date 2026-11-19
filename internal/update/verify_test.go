package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/bundle"
)

func embeddedVerifier(t *testing.T) *Verifier {
	t.Helper()
	tr, err := EmbeddedTrustedRoot()
	if err != nil {
		t.Fatalf("embedded trusted root: %v", err)
	}
	return NewVerifier(tr)
}

func TestEmbeddedTrustedRootIsPublicGood(t *testing.T) {
	tr, err := EmbeddedTrustedRoot()
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.FulcioCertificateAuthorities()) == 0 || len(tr.RekorLogs()) == 0 || len(tr.CTLogs()) == 0 {
		t.Fatal("embedded trusted root lacks Fulcio, Rekor or CT log material")
	}
}

func TestExpectedIdentityIsAValidPolicy(t *testing.T) {
	if _, err := ExpectedIdentity("0.1.2").certificateIdentity(); err != nil {
		t.Fatalf("certificate identity: %v", err)
	}
}

func TestExpectedIdentityBindsTheTag(t *testing.T) {
	id := ExpectedIdentity("0.1.2-rc.1")
	if id.SAN != "https://github.com/gatherdeal/cli/.github/workflows/release.yml@refs/tags/v0.1.2-rc.1" {
		t.Errorf("SAN = %q", id.SAN)
	}
	if id.Ref != "refs/tags/v0.1.2-rc.1" || id.Issuer != "https://token.actions.githubusercontent.com" {
		t.Errorf("identity = %+v", id)
	}
}

func TestVerifyBlobRefuses(t *testing.T) {
	v := embeddedVerifier(t)
	foreign, err := os.ReadFile(filepath.Join("testdata", "foreign-workflow.sigstore.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		bundle  []byte
		version string
	}{
		"no bundle":             {nil, "0.1.0"},
		"malformed bundle":      {[]byte(`{"mediaType":"nope"}`), "0.1.0"},
		"non-canonical version": {foreign, "v0.1.0"},
		"build metadata":        {foreign, "0.1.0+x"},
		"oversized bundle":      {bytes.Repeat([]byte(" "), 65<<10), "0.1.0"},
		// A genuine Sigstore bundle from another repository's release
		// workflow: valid signature, wrong identity and artifact.
		"foreign workflow": {foreign, "2.0.0"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := v.VerifyBlob([]byte("artifact"), c.bundle, c.version)
			if !errors.Is(err, ErrVerify) {
				t.Fatalf("err = %v, want ErrVerify", err)
			}
		})
	}
}

func loadBundle(t *testing.T, name string) *bundle.Bundle {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	return &b
}

func TestCheckIdentity(t *testing.T) {
	b := loadBundle(t, "foreign-workflow.sigstore.json")
	// The bundle's own identity matches field for field...
	own := identityAt("https://github.com/sigstore/sigstore-js", ".github/workflows/release.yml", "refs/heads/main")
	if err := checkIdentity(b, own); err != nil {
		t.Fatalf("own identity: %v", err)
	}
	// ...and every deviation is refused.
	for name, id := range map[string]Identity{
		"release identity": ExpectedIdentity("2.0.0"),
		"other ref":        identityAt("https://github.com/sigstore/sigstore-js", ".github/workflows/release.yml", "refs/tags/v2.0.0"),
		"other workflow":   identityAt("https://github.com/sigstore/sigstore-js", ".github/workflows/ci.yml", "refs/heads/main"),
	} {
		if err := checkIdentity(b, id); !errors.Is(err, ErrVerify) || !strings.Contains(err.Error(), "signed by") {
			t.Errorf("%s: err = %v, want an identity mismatch", name, err)
		}
	}
	other := own
	other.Repository = "https://github.com/gatherdeal/cli"
	if err := checkIdentity(b, other); !errors.Is(err, ErrVerify) || !strings.Contains(err.Error(), "source repository") {
		t.Errorf("other repository: err = %v", err)
	}
}

func TestVerifyManifestRejectsInvalidManifestFirst(t *testing.T) {
	v := embeddedVerifier(t)
	if _, err := v.VerifyManifest([]byte(`{"schema_version":2}`), []byte(`{}`)); !errors.Is(err, ErrVerify) {
		t.Fatalf("err = %v, want ErrVerify", err)
	}
}

func writeArchive(t *testing.T, dir, body string) (string, int64, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "gatherdeal", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gatherdeal_0.1.0_linux_amd64.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return path, int64(buf.Len()), hex.EncodeToString(sum[:])
}

func TestCheckFileAndExtractVerified(t *testing.T) {
	dir := t.TempDir()
	archive, size, sum := writeArchive(t, dir, "binary")
	if err := CheckFile(archive, size, sum); err != nil {
		t.Fatalf("CheckFile: %v", err)
	}
	if err := CheckFile(archive, size+1, sum); !errors.Is(err, ErrVerify) {
		t.Fatalf("size mismatch: %v", err)
	}
	if err := CheckFile(archive, size, hex.EncodeToString(make([]byte, 32))); !errors.Is(err, ErrVerify) {
		t.Fatalf("hash mismatch: %v", err)
	}
	binSum := sha256.Sum256([]byte("binary"))
	dest := filepath.Join(dir, "out")
	if err := ExtractVerified(archive, "linux", dest, hex.EncodeToString(binSum[:])); err != nil {
		t.Fatalf("ExtractVerified: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "binary" {
		t.Fatalf("extracted %q", got)
	}
	bad := filepath.Join(dir, "bad")
	if err := ExtractVerified(archive, "linux", bad, hex.EncodeToString(make([]byte, 32))); !errors.Is(err, ErrVerify) {
		t.Fatalf("binary mismatch: %v", err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("a rejected binary was left behind: %v", err)
	}
}
