package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRefusesUnsafeDestination(t *testing.T) {
	skipOnWindows(t)
	fs := NewFileStore(newRoot(t))
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fs.Dir, fs.fileName("acct"))
	var insecure *InsecureError

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.Set("acct", secret); !errors.As(err, &insecure) {
		t.Fatalf("broad destination replaced: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "x"), path); err != nil {
		t.Fatal(err)
	}
	if err := fs.Set("acct", secret); !errors.As(err, &insecure) {
		t.Fatalf("symlink destination replaced: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fs.Set("acct", secret); err == nil {
		t.Fatal("directory destination replaced")
	}
	entries, _ := os.ReadDir(fs.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}
