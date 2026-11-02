package releasemeta

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name string
	typ  byte
	body string
	mode int64
}

func writeTarGz(t *testing.T, path string, entries []entry) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Size: int64(len(e.body))}
		if typ != tar.TypeReg {
			hdr.Size = 0
			hdr.Linkname = "/etc/passwd"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, entries []entry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := os.FileMode(0o755)
		if e.typ == tar.TypeSymlink {
			mode |= os.ModeSymlink
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func standard(binary, body string) []entry {
	return []entry{{name: "README.md", body: "readme"}, {name: binary, body: body}, {name: "LICENSE", body: "license"}}
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "a.tar.gz")
	writeTarGz(t, tgz, standard("gatherdeal", "ELF-binary"))
	var out bytes.Buffer
	sum, n, err := ExtractBinary(tgz, "linux", &out)
	if err != nil || sum != sha("ELF-binary") || n != int64(len("ELF-binary")) || out.String() != "ELF-binary" {
		t.Fatalf("tar.gz: sum %s n %d out %q err %v", sum, n, out.String(), err)
	}
	zp := filepath.Join(dir, "a.zip")
	writeZip(t, zp, standard("gatherdeal.exe", "MZ-binary"))
	out.Reset()
	sum, _, err = ExtractBinary(zp, "windows", &out)
	if err != nil || sum != sha("MZ-binary") || out.String() != "MZ-binary" {
		t.Fatalf("zip: sum %s err %v", sum, err)
	}
}

func TestExtractBinaryRejects(t *testing.T) {
	cases := map[string][]entry{
		"traversal":   {{name: "../gatherdeal", body: "x"}},
		"absolute":    {{name: "/gatherdeal", body: "x"}},
		"backslash":   {{name: "bin\\gatherdeal", body: "x"}},
		"symlink":     {{name: "gatherdeal", typ: tar.TypeSymlink}},
		"hardlink":    {{name: "README.md", typ: tar.TypeLink}, {name: "gatherdeal", body: "x"}},
		"fifo":        {{name: "pipe", typ: tar.TypeFifo}, {name: "gatherdeal", body: "x"}},
		"duplicate":   {{name: "gatherdeal", body: "x"}, {name: "gatherdeal", body: "y"}},
		"missing":     {{name: "README.md", body: "x"}},
		"setuid":      {{name: "gatherdeal", body: "x", mode: 0o4755}},
		"empty":       {{name: "gatherdeal", body: ""}},
		"nested only": {{name: "dir/gatherdeal", body: "x"}},
	}
	for name, entries := range cases {
		p := filepath.Join(t.TempDir(), "a.tar.gz")
		writeTarGz(t, p, entries)
		if _, _, err := ExtractBinary(p, "darwin", io.Discard); !errors.Is(err, ErrArchive) {
			t.Errorf("tar %s: err = %v, want ErrArchive", name, err)
		}
	}
	zipCases := map[string][]entry{
		"traversal": {{name: "../gatherdeal.exe", body: "x"}},
		"symlink":   {{name: "gatherdeal.exe", typ: tar.TypeSymlink, body: "C:/x"}},
		"duplicate": {{name: "gatherdeal.exe", body: "x"}, {name: "gatherdeal.exe", body: "y"}},
		"missing":   {{name: "gatherdeal", body: "x"}},
	}
	for name, entries := range zipCases {
		p := filepath.Join(t.TempDir(), "a.zip")
		writeZip(t, p, entries)
		if _, _, err := ExtractBinary(p, "windows", io.Discard); !errors.Is(err, ErrArchive) {
			t.Errorf("zip %s: err = %v, want ErrArchive", name, err)
		}
	}
	p := filepath.Join(t.TempDir(), "junk.tar.gz")
	if err := os.WriteFile(p, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ExtractBinary(p, "linux", io.Discard); !errors.Is(err, ErrArchive) {
		t.Errorf("junk: err = %v", err)
	}
}

func fakeDist(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range Platforms {
		path := filepath.Join(dir, ArchiveName(version, p[0], p[1]))
		body := "binary-" + p[0] + "-" + p[1]
		if p[0] == "windows" {
			writeZip(t, path, standard("gatherdeal.exe", body))
		} else {
			writeTarGz(t, path, standard("gatherdeal", body))
		}
	}
	return dir
}

func TestGenerateAndParse(t *testing.T) {
	dist := fakeDist(t, "0.1.2")
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	m, err := Generate(dist, "0.1.2", at, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Assets) != 6 || m.Prerelease || m.NPM.Version != "0.1.2" {
		t.Fatalf("manifest %+v", m)
	}
	a, ok := m.Asset("linux", "arm64")
	if !ok || a.Name != "gatherdeal_0.1.2_linux_arm64.tar.gz" || a.BinarySHA256 != sha("binary-linux-arm64") {
		t.Fatalf("asset %+v", a)
	}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != "0.1.2" || !back.PublishedAt.Equal(at) || len(back.Assets) != 6 {
		t.Fatalf("round trip %+v", back)
	}
	// A newer release may add fields; this CLI must still read it.
	withExtra := strings.Replace(string(data), `"schema_version": 1,`, `"schema_version": 1, "future_field": {"x": 1},`, 1)
	if _, err := Parse([]byte(withExtra)); err != nil {
		t.Errorf("unknown field rejected: %v", err)
	}

	if _, err := Generate(dist, "0.1.3", at, "0.1.0"); err == nil {
		t.Error("Generate must fail when an archive is missing")
	}
	rc, err := Generate(fakeDist(t, "0.2.0-rc.1"), "0.2.0-rc.1", at, "0.1.0")
	if err != nil || !rc.Prerelease {
		t.Errorf("pre-release manifest: %+v %v", rc, err)
	}
}

func TestValidateRejects(t *testing.T) {
	base := func() *Manifest {
		m, err := Generate(fakeDist(t, "0.1.2"), "0.1.2", time.Now(), "0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	cases := map[string]func(*Manifest){
		"schema":           func(m *Manifest) { m.SchemaVersion = 2 },
		"version":          func(m *Manifest) { m.Version = "v0.1.2" },
		"build metadata":   func(m *Manifest) { m.Version = "0.1.2+x" },
		"prerelease flag":  func(m *Manifest) { m.Prerelease = true },
		"published_at":     func(m *Manifest) { m.PublishedAt = time.Time{} },
		"min_upgrade_from": func(m *Manifest) { m.MinUpgradeFrom = "0.2.0" },
		"no assets":        func(m *Manifest) { m.Assets = nil },
		"duplicate":        func(m *Manifest) { m.Assets = append(m.Assets, m.Assets[0]) },
		"asset name":       func(m *Manifest) { m.Assets[0].Name = "../../evil.tar.gz" },
		"size":             func(m *Manifest) { m.Assets[0].Size = MaxArchiveBytes + 1 },
		"sha":              func(m *Manifest) { m.Assets[0].SHA256 = strings.ToUpper(m.Assets[0].SHA256) },
		"binary sha":       func(m *Manifest) { m.Assets[0].BinarySHA256 = "" },
		"npm package":      func(m *Manifest) { m.NPM.Package = "evil" },
		"npm version":      func(m *Manifest) { m.NPM.Version = "9.9.9" },
	}
	for name, mutate := range cases {
		m := base()
		mutate(m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: Validate accepted", name)
		}
	}
	if _, err := Parse(append([]byte(`{"schema_version":1}`), ` {}`...)); err == nil {
		t.Error("trailing data accepted")
	}
	if _, err := Parse(bytes.Repeat([]byte(" "), MaxManifestBytes+1)); err == nil {
		t.Error("oversized manifest accepted")
	}
}
