package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGitWorkTreeThroughSymlinkedAncestor(t *testing.T) {
	skipOnWindows(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(outside, "cfg")
	if err := os.Symlink(filepath.Join(repo, "sub"), link); err != nil {
		t.Fatal(err)
	}
	// XDG_CONFIG_HOME=link: the lexical path has no .git above it.
	fs := NewFileStore(filepath.Join(link, "gatherdeal"))
	if got := FindGitWorkTree(fs.Dir); got == "" {
		t.Fatal("symlinked ancestor into a repository not detected")
	}
	var insecure *InsecureError
	if err := fs.Preflight(); !errors.As(err, &insecure) {
		t.Fatalf("Preflight = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub", "gatherdeal")); !os.IsNotExist(err) {
		t.Fatal("created a directory inside the repository")
	}
}
