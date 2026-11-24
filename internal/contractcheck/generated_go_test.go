package contractcheck

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gatherdeal/cli/contracts"
)

func TestGeneratedGoDigest(t *testing.T) {
	root := repoRoot(t)
	for _, b := range manifest(t).Bundles {
		t.Run(b.Service+"/"+b.Version, func(t *testing.T) {
			sources := os.DirFS(filepath.Join(root, filepath.FromSlash(b.GoPackage)))
			if err := verifyGeneratedGoDigest(b, sources); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func verifyGeneratedGoDigest(b contracts.Bundle, sources fs.FS) error {
	if b.GeneratedGoDigest == "" {
		return fmt.Errorf("contracts/manifest.json: bundle %s/%s is missing generated_go_digest; re-export with utilities apigen --public-dist that writes this field", b.Service, b.Version)
	}
	got, err := generatedGoDigest(sources)
	if err != nil {
		return err
	}
	if got != b.GeneratedGoDigest {
		return fmt.Errorf("generated Go digest mismatch for %s: got %s, manifest wants %s", b.GoPackage, got, b.GeneratedGoDigest)
	}
	return nil
}

func generatedGoDigest(sources fs.FS) (string, error) {
	entries, err := fs.ReadDir(sources, ".") // sorted by name
	if err != nil {
		return "", err
	}
	var input bytes.Buffer
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		data, err := fs.ReadFile(sources, name)
		if err != nil {
			return "", err
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		// Frame each file as name, NUL, decimal byte count, NUL, contents.
		input.WriteString(name)
		input.WriteByte(0)
		input.WriteString(strconv.Itoa(len(data)))
		input.WriteByte(0)
		input.Write(data)
	}
	return digest(input.Bytes()), nil
}

func TestVerifyGeneratedGoDigest(t *testing.T) {
	const binding = "package v1\nvar FilterFields = []string{\"status\"}\n"
	const types = "package v1\n"
	m, err := contracts.Parse([]byte(`{"format_version":2,"bundles":[{
		"service":"example",
		"version":"v1",
		"go_package":"internal/generated/example/v1",
		"generated_go_digest":"sha256:1585333b8b3b1e105aed5d65f6cc9f777ca2fb487eaededb4f460f67f67feee9"
	}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b := m.Bundles[0]
	sources := fstest.MapFS{
		"types_gen.go":        {Data: []byte(types)},
		"cli_bindings_gen.go": {Data: []byte(binding)},
		"notes.txt":           {Data: []byte("ignored")},
	}
	if err := verifyGeneratedGoDigest(b, sources); err != nil {
		t.Fatal(err)
	}
	sources["cli_bindings_gen.go"].Data = []byte(strings.ReplaceAll(binding, "\n", "\r\n"))
	if err := verifyGeneratedGoDigest(b, sources); err != nil {
		t.Fatalf("CRLF checkout: %v", err)
	}

	for _, tc := range []struct {
		name   string
		change func(fstest.MapFS)
	}{
		{"edit filter allowlist", func(files fstest.MapFS) {
			files["cli_bindings_gen.go"] = &fstest.MapFile{Data: []byte(strings.Replace(binding, "status", "outlet_id", 1))}
		}},
		{"add Go file", func(files fstest.MapFS) { files["extra.go"] = &fstest.MapFile{Data: []byte(types)} }},
		{"remove Go file", func(files fstest.MapFS) { delete(files, "types_gen.go") }},
		{"rename Go file", func(files fstest.MapFS) {
			files["model_gen.go"] = files["types_gen.go"]
			delete(files, "types_gen.go")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fstest.MapFS{
				"types_gen.go":        {Data: []byte(types)},
				"cli_bindings_gen.go": {Data: []byte(binding)},
			}
			tc.change(files)
			if err := verifyGeneratedGoDigest(b, files); err == nil || !strings.Contains(err.Error(), "generated Go digest mismatch") {
				t.Fatalf("got %v, want generated Go digest mismatch", err)
			}
		})
	}

	b.GeneratedGoDigest = ""
	if err := verifyGeneratedGoDigest(b, sources); err == nil || !strings.Contains(err.Error(), "missing generated_go_digest") {
		t.Fatalf("got %v, want missing generated_go_digest", err)
	}
}
