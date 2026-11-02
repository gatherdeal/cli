//go:build unix

package update

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// ownedByUser reports whether path (not following a final symlink) is
// owned by the current user.
func ownedByUser(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// writableDir reports whether the current user may create files in dir.
func writableDir(dir string) bool { return unix.Access(dir, unix.W_OK) == nil }
