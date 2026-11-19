//go:build windows

package store

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows refuses to replace or remove a file while another handle has it
// open without delete sharing, as a concurrent reader or an antivirus scan
// may briefly do. Readers here open with delete sharing so they never block
// a writer, and writers retry for a bounded time.

// sharingRetryTimeout bounds how long an operation retries while another
// handle holds the file.
const sharingRetryTimeout = 2 * time.Second

// retrySharing runs op until it succeeds, fails with an error other than a
// sharing or access conflict, or sharingRetryTimeout passes.
func retrySharing(op func() error) error {
	deadline := time.Now().Add(sharingRetryTimeout)
	delay := time.Millisecond
	for {
		err := op()
		if err == nil || !isSharingConflict(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		delay = min(2*delay, 100*time.Millisecond)
	}
}

// isSharingConflict reports the errors Windows returns while another handle
// holds a file: a sharing violation, or access denied for a file whose
// replacement or deletion is still pending.
func isSharingConflict(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// readFile reads path through a handle that shares delete access, so a
// concurrent rename over path or removal of it is not blocked.
func readFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var h windows.Handle
	err = retrySharing(func() (err error) {
		h, err = windows.CreateFile(name, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		return err
	})
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}

func renameFile(oldpath, newpath string) error {
	return retrySharing(func() error { return os.Rename(oldpath, newpath) })
}

func removeFile(path string) error {
	return retrySharing(func() error { return os.Remove(path) })
}
