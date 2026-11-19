package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LockTimeout bounds how long a command waits for another gatherdeal
// process to finish changing the local state.
var LockTimeout = 30 * time.Second

const lockFileName = ".lock"

// errLocked is returned by tryLock when another process holds the lock.
var errLocked = errors.New("locked")

// Lock takes the interprocess lock that serializes every change to
// profiles.json and the stored credentials. Readers do not lock: files are
// replaced atomically.
func Lock(root string) (unlock func(), err error) {
	if err := ensurePrivateDir(root); err != nil {
		return nil, &InsecureError{Err: err}
	}
	path := filepath.Join(root, lockFileName)
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := checkPrivateFile(path); err != nil {
		_ = f.Close()
		return nil, &InsecureError{Err: err}
	}
	deadline := time.Now().Add(LockTimeout)
	for {
		err := tryLock(f)
		if err == nil {
			return func() { _ = unlockFile(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, errLocked) {
			_ = f.Close()
			return nil, fmt.Errorf("cannot lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("another gatherdeal process is changing the local state (%s); try again", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|openNoFollow, 0o600)
	if err != nil {
		return nil, err
	}
	if err := restrictToOwner(path, false); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
