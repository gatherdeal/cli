package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// populate writes the files a used config directory holds.
func populate(t *testing.T, root string) {
	t.Helper()
	if err := saveSkills(t, root, SkillInstall{Path: filepath.Join(filepath.Dir(root), "s", "gatherdeal")}); err != nil {
		t.Fatal(err)
	}
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	unlock()
	files := NewFileStore(root)
	if err := files.Set("account", "secret"); err != nil {
		t.Fatal(err)
	}
}

func TestInspectAndRemoveRoot(t *testing.T) {
	root := newRoot(t)
	if c, err := InspectRoot(root); err != nil || c.Exists {
		t.Fatalf("missing root: %+v %v", c, err)
	}
	if foreign, err := RemoveRoot(root); err != nil || foreign != nil {
		t.Fatalf("missing root: %v %v", foreign, err)
	}
	populate(t, root)
	c, err := InspectRoot(root)
	if err != nil || !c.Exists || len(c.Foreign) != 0 {
		t.Fatalf("inspect %+v %v", c, err)
	}
	want := []string{".lock", "profiles.json", "skills.json", "credentials/" + filepath.Base(NewFileStore(root).Path("account")), "credentials"}
	if !reflect.DeepEqual(c.Owned, want) {
		t.Fatalf("owned %v", c.Owned)
	}
	if foreign, err := RemoveRoot(root); err != nil || foreign != nil {
		t.Fatalf("remove: %v %v", foreign, err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root left: %v", err)
	}
}

func TestRemoveRootKeepsForeignEntries(t *testing.T) {
	root := newRoot(t)
	populate(t, root)
	for _, p := range []string{"notes.txt", filepath.Join("credentials", "mine.txt")} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	foreign, err := RemoveRoot(root)
	if err != nil || !reflect.DeepEqual(foreign, []string{"notes.txt", "credentials/mine.txt"}) {
		t.Fatalf("remove: %v %v", foreign, err)
	}
	var left []string
	_ = filepath.WalkDir(root, func(path string, _ os.DirEntry, _ error) error {
		rel, _ := filepath.Rel(root, path)
		left = append(left, filepath.ToSlash(rel))
		return nil
	})
	if !reflect.DeepEqual(left, []string{".", "credentials", "credentials/mine.txt", "notes.txt"}) {
		t.Fatalf("left %v", left)
	}
}

func TestRemoveRootRefusesProfiles(t *testing.T) {
	root := newRoot(t)
	populate(t, root)
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := LoadProfiles(root)
	p.Put("default", &Profile{Server: "https://api.example.com", Issuer: "https://api.example.com/oidc"})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := RemoveRoot(root); !errors.Is(err, ErrProfilesRemain) {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "profiles.json")); err != nil {
		t.Fatal("profiles.json removed")
	}
}

func TestRemoveRootRefusesSymlink(t *testing.T) {
	realDir := filepath.Join(t.TempDir(), "realDir")
	populate(t, realDir)
	root := newRoot(t)
	if err := os.Symlink(realDir, root); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := RemoveRoot(root); err == nil {
		t.Fatal("followed a symlinked root")
	}
	if _, err := os.Stat(filepath.Join(realDir, "profiles.json")); err != nil {
		t.Fatal("symlink target changed")
	}
}
