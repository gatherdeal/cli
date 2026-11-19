package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// skillsFileName records where `skills install` wrote the agent skill, so
// `uninstall` can remove it. It holds no secrets.
const skillsFileName = "skills.json"

// skillsSchemaVersion is the initial version of skills.json.
// Persisted-field additions must raise it to prevent older binaries losing data.
const skillsSchemaVersion = 1

// SkillInstall is one location the agent skill was installed to.
type SkillInstall struct {
	// Path is the absolute, symlink-resolved gatherdeal skill directory.
	Path string `json:"path"`
	// Target and Scope are the --target and --scope of the install; both
	// are empty for an install with --dir.
	Target string `json:"target,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

func (s SkillInstall) validate() error {
	if s.Path == "" || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path {
		return fmt.Errorf("skill path %q is not a clean absolute path", s.Path)
	}
	switch s.Target {
	case "", "claude", "agents":
	default:
		return fmt.Errorf("unknown skill target %q", s.Target)
	}
	switch s.Scope {
	case "", "project", "user":
	default:
		return fmt.Errorf("unknown skill scope %q", s.Scope)
	}
	if (s.Target == "") != (s.Scope == "") {
		return errors.New("skill target and scope must be set together")
	}
	return nil
}

type skillsFile struct {
	SchemaVersion int            `json:"schema_version"`
	Installs      []SkillInstall `json:"installs"`
}

// SkillInstalls is skills.json under the config root.
type SkillInstalls struct {
	Root string
	data skillsFile
}

// LoadSkillInstalls reads skills.json; a missing file is an empty list.
func LoadSkillInstalls(root string) (*SkillInstalls, error) {
	s := &SkillInstalls{Root: root, data: skillsFile{SchemaVersion: skillsSchemaVersion, Installs: []SkillInstall{}}}
	path := filepath.Join(root, skillsFileName)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	// The list decides which directories uninstall removes, so it gets the
	// same protection as the profile metadata.
	if err := checkPrivateDir(root); err != nil {
		return nil, &InsecureError{Err: err}
	}
	if err := checkPrivateFile(path); err != nil {
		return nil, &InsecureError{Err: err}
	}
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", path, err)
	}
	if head.SchemaVersion != skillsSchemaVersion {
		return nil, &SchemaError{File: skillsFileName, Found: head.SchemaVersion, Supported: skillsSchemaVersion}
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", path, err)
	}
	if s.data.Installs == nil {
		s.data.Installs = []SkillInstall{}
	}
	for _, in := range s.data.Installs {
		if err := in.validate(); err != nil {
			return nil, fmt.Errorf("%s is corrupt: %w", path, err)
		}
	}
	return s, nil
}

// List returns the recorded installs in the order they were first recorded.
func (s *SkillInstalls) List() []SkillInstall {
	return append([]SkillInstall(nil), s.data.Installs...)
}

// Add records in (in memory; call Save). A location recorded before is
// replaced in place.
func (s *SkillInstalls) Add(in SkillInstall) error {
	if err := in.validate(); err != nil {
		return err
	}
	for i, old := range s.data.Installs {
		if old.Path == in.Path {
			s.data.Installs[i] = in
			return nil
		}
	}
	s.data.Installs = append(s.data.Installs, in)
	return nil
}

// Save writes skills.json owner-only and atomically. Like every change of
// the config directory, it runs under Lock.
func (s *SkillInstalls) Save() error {
	if s.data.SchemaVersion != skillsSchemaVersion {
		return &SchemaError{File: skillsFileName, Found: s.data.SchemaVersion, Supported: skillsSchemaVersion}
	}
	// Recheck the on-disk boundary even for a previously loaded instance.
	if _, err := LoadSkillInstalls(s.Root); err != nil {
		return err
	}
	if err := ensurePrivateDir(s.Root); err != nil {
		return &InsecureError{Err: err}
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Root, skillsFileName, append(data, '\n'))
}
