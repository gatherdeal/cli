package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ensurePrivateDir creates dir (and missing parents) owner-only, then
// verifies it: a real directory (not a symlink), owned by the current user
// and inaccessible to anyone else.
func ensurePrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := restrictToOwner(dir, true); err != nil {
			return fmt.Errorf("%s: cannot restrict access: %w", dir, err)
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s must be a directory, not a symlink or file", dir)
	}
	return checkOwnerOnly(dir, fi)
}

// checkPrivateDir verifies an existing directory is owner-only and not a
// symlink.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s must be a directory, not a symlink or file", dir)
	}
	return checkOwnerOnly(dir, fi)
}

// checkPrivateFile verifies an existing regular file is owner-only.
func checkPrivateFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symlink or special file", path)
	}
	return checkOwnerOnly(path, fi)
}

// writeFileAtomic writes data to dir/name through an owner-only temporary
// file, fsync and rename, so readers see either the old or the new file.
func writeFileAtomic(dir, name string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, ".tmp-"+name+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	if err = restrictToOwner(tmpName, false); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Never replace something that is not our own owner-only file.
	dest := filepath.Join(dir, name)
	if _, statErr := os.Lstat(dest); statErr == nil {
		if cerr := checkPrivateFile(dest); cerr != nil {
			err = &InsecureError{Err: fmt.Errorf("refusing to replace %s: %w", dest, cerr)}
			return err
		}
	}
	if err = renameFile(tmpName, dest); err != nil {
		return err
	}
	return syncDir(dir)
}
