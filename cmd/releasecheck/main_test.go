package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/gatherdeal/cli/internal/releasemeta"
)

var testTime = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)

func writeArchives(t *testing.T, dir, version string) {
	t.Helper()
	for _, p := range releasemeta.Platforms {
		var buf bytes.Buffer
		name := releasemeta.BinaryName(p[0])
		body := []byte("binary " + p[0] + p[1])
		if p[0] == "windows" {
			zw := zip.NewWriter(&buf)
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(body)
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write(body)
			_ = tw.Close()
			_ = gz.Close()
		}
		if err := os.WriteFile(filepath.Join(dir, releasemeta.ArchiveName(version, p[0], p[1])), buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManifestDefaultsMinUpgradeFrom(t *testing.T) {
	for version, want := range map[string]string{"0.2.0": releasemeta.MinUpgradeFrom, "0.1.0-rc.1": "0.1.0-rc.1"} {
		dir := t.TempDir()
		writeArchives(t, dir, version)
		if err := run(context.Background(), []string{"manifest", "-dist", dir, "-version", version, "-published-at", "2026-11-02T08:00:00Z"}, io.Discard); err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, releasemeta.ManifestName))
		if err != nil {
			t.Fatal(err)
		}
		m, err := releasemeta.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if m.MinUpgradeFrom != want || m.Version != version || len(m.Assets) != len(releasemeta.Platforms) {
			t.Errorf("%s: %+v", version, m)
		}
	}
}

func TestChecksumsListMustAgree(t *testing.T) {
	dir := t.TempDir()
	writeArchives(t, dir, "0.2.0")
	m, err := releasemeta.Generate(dir, "0.2.0", testTime, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, a := range m.Assets {
		b.WriteString(a.SHA256 + "  " + a.Name + "\n")
	}
	if err := checkChecksumsList([]byte(b.String()), m); err != nil {
		t.Fatalf("agreeing list: %v", err)
	}
	bad := strings.Replace(b.String(), m.Assets[0].SHA256, strings.Repeat("0", 64), 1)
	if err := checkChecksumsList([]byte(bad), m); err == nil {
		t.Fatal("a disagreeing checksums.txt was accepted")
	}
}

func TestVerifyRefusesUnsignedRelease(t *testing.T) {
	dir := t.TempDir()
	writeArchives(t, dir, "0.2.0")
	if err := run(context.Background(), []string{"manifest", "-dist", dir, "-version", "0.2.0"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"release.json.sigstore.json", "checksums.txt", "checksums.txt.sigstore.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	offline := func(context.Context, http.RoundTripper) (root.TrustedMaterial, string, error) {
		return nil, "", errors.New("offline")
	}
	defer func(f func(context.Context, http.RoundTripper) (root.TrustedMaterial, string, error)) {
		trustedMaterial = f
	}(trustedMaterial)
	trustedMaterial = offline
	if err := run(context.Background(), []string{"verify", "-dir", dir, "-version", "0.2.0"}, io.Discard); err == nil {
		t.Fatal("an unsigned release verified")
	}
}
