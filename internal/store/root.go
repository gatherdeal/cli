package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ErrProfilesRemain refuses to remove a config directory whose
// profiles.json still names a profile: its credentials would be lost
// without being revoked.
var ErrProfilesRemain = errors.New("the config directory still has profiles")

// credentialFileRE is the name of a credential file (FileStore.fileName).
var credentialFileRE = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)

const credentialsDirName = "credentials"

// RootContents lists a config directory. Owned holds the entries the CLI
// creates there (relative, slash-separated, files before the directories
// that hold them); Foreign holds every other entry. Exists is false when
// there is no directory.
type RootContents struct {
	Exists  bool
	Owned   []string
	Foreign []string
}

// isTempName matches the temporary files of writeFileAtomic.
func isTempName(name string) bool { return strings.HasPrefix(name, ".tmp-") }

// InspectRoot lists root without changing it. root itself must be a real
// directory, not a symlink.
func InspectRoot(root string) (RootContents, error) {
	var c RootContents
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return c, fmt.Errorf("%s must be a directory, not a symlink or file", root)
	}
	c.Exists = true
	entries, err := os.ReadDir(root)
	if err != nil {
		return c, err
	}
	credentials := false
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.Type().IsRegular() && (name == profilesFileName || name == skillsFileName || name == lockFileName || isTempName(name)):
			c.Owned = append(c.Owned, name)
		case e.IsDir() && e.Type()&fs.ModeSymlink == 0 && name == credentialsDirName:
			credentials = true
		default:
			c.Foreign = append(c.Foreign, name)
		}
	}
	if credentials {
		owned, foreign, err := inspectCredentials(filepath.Join(root, credentialsDirName))
		if err != nil {
			return c, err
		}
		c.Owned = append(c.Owned, owned...)
		c.Foreign = append(c.Foreign, foreign...)
		if len(foreign) == 0 {
			c.Owned = append(c.Owned, credentialsDirName)
		}
	}
	return c, nil
}

func inspectCredentials(dir string) (owned, foreign []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		rel := path.Join(credentialsDirName, e.Name())
		if e.Type().IsRegular() && (credentialFileRE.MatchString(e.Name()) || e.Name() == ".preflight" || isTempName(e.Name())) {
			owned = append(owned, rel)
		} else {
			foreign = append(foreign, rel)
		}
	}
	return owned, foreign, nil
}

// RemoveRoot deletes the files the CLI keeps in root, then root itself,
// under the lock. It refuses with ErrProfilesRemain while profiles.json
// names a profile. Entries the CLI does not create are left in place, and
// so is root then; RemoveRoot returns them. Symlinks are never followed.
func RemoveRoot(root string) (foreign []string, err error) {
	c, err := InspectRoot(root)
	if err != nil || !c.Exists {
		return nil, err
	}
	unlock, err := Lock(root)
	if err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	p, err := LoadProfiles(root)
	if err != nil {
		return nil, err
	}
	if len(p.Names()) > 0 {
		return nil, ErrProfilesRemain
	}
	// List again under the lock: another process may have changed root.
	if c, err = InspectRoot(root); err != nil {
		return nil, err
	}
	for _, rel := range c.Owned {
		if rel == lockFileName {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		if rel == credentialsDirName {
			err = os.Remove(full)
		} else {
			err = removeFile(full)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return c.Foreign, err
		}
	}
	// The lock file goes last, once its handle is closed (Windows cannot
	// delete an open file).
	locked = false
	unlock()
	if slices.Contains(c.Owned, lockFileName) {
		if err := removeFile(filepath.Join(root, lockFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return c.Foreign, err
		}
	}
	if len(c.Foreign) > 0 {
		return c.Foreign, nil
	}
	if err := os.Remove(root); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return nil, nil
}
