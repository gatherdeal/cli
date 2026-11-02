//go:build unix

package store

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// checkOwnerOnly refuses a path that is not owned by the current user or
// whose mode grants any access to group or others.
func checkOwnerOnly(path string, fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: cannot read file ownership", path)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not the current user (uid %d)", path, st.Uid, os.Getuid())
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s has mode %04o; it must not be accessible to group or others (expected %04o)", path, perm, expectedMode(fi))
	}
	return nil
}

func expectedMode(fi fs.FileInfo) fs.FileMode {
	if fi.IsDir() {
		return 0o700
	}
	return 0o600
}

// restrictToOwner is a no-op on Unix: files and directories are created with
// owner-only modes.
func restrictToOwner(string, bool) error { return nil }

// syncDir flushes a directory entry after a rename.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
