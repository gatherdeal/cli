package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func saveSkills(t *testing.T, root string, ins ...SkillInstall) error {
	t.Helper()
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s, err := LoadSkillInstalls(root)
	if err != nil {
		return err
	}
	for _, in := range ins {
		if err := s.Add(in); err != nil {
			return err
		}
	}
	return s.Save()
}

func TestSkillInstallsRoundTrip(t *testing.T) {
	root := newRoot(t)
	s, err := LoadSkillInstalls(root)
	if err != nil || len(s.List()) != 0 {
		t.Fatalf("missing file: %v %v", s, err)
	}
	base := filepath.Dir(root)
	user := SkillInstall{Path: filepath.Join(base, "home", ".claude", "skills", "gatherdeal"), Target: "claude", Scope: "user"}
	dir := SkillInstall{Path: filepath.Join(base, "custom", "gatherdeal")}
	if err := saveSkills(t, root, user, dir, user); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSkillInstalls(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.List(); !reflect.DeepEqual(got, []SkillInstall{user, dir}) {
		t.Fatalf("installs %+v", got)
	}
	skipOnWindows(t)
	fi, err := os.Stat(filepath.Join(root, skillsFileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
}

func TestSkillInstallsRejectInvalid(t *testing.T) {
	root := newRoot(t)
	abs := filepath.Join(filepath.Dir(root), "x", "gatherdeal")
	for _, in := range []SkillInstall{
		{Path: "relative/gatherdeal"},
		{Path: abs + string(filepath.Separator) + ".."},
		{Path: abs, Target: "claude"},
		{Path: abs, Scope: "user"},
		{Path: abs, Target: "vim", Scope: "user"},
		{Path: abs, Target: "claude", Scope: "global"},
	} {
		if err := saveSkills(t, root, in); err == nil {
			t.Errorf("%+v accepted", in)
		}
	}
}

func TestSkillInstallsUnsupportedSchema(t *testing.T) {
	for _, version := range []int{0, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := newRoot(t)
			if err := saveSkills(t, root); err != nil {
				t.Fatal(err)
			}
			raw := fmt.Appendf(nil, `{"schema_version":%d,"installs":[]}`, version)
			path := filepath.Join(root, skillsFileName)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var schema *SchemaError
			if _, err := LoadSkillInstalls(root); !errors.As(err, &schema) || schema.File != skillsFileName || schema.Found != version {
				t.Fatalf("read: %v", err)
			}
			if err := saveSkills(t, root); !errors.As(err, &schema) {
				t.Fatalf("write: %v", err)
			}
			if after, _ := os.ReadFile(path); string(after) != string(raw) {
				t.Fatal("file rewritten")
			}
		})
	}
}

func TestSkillInstallsCorrupt(t *testing.T) {
	root := newRoot(t)
	if err := saveSkills(t, root); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{`, `{"schema_version":1,"installs":[{"path":"rel"}]}`} {
		if err := os.WriteFile(filepath.Join(root, skillsFileName), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSkillInstalls(root); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
}
