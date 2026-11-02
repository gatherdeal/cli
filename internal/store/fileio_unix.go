//go:build unix

package store

import "os"

// readFile, renameFile and removeFile are the plain calls on Unix, where an
// open file never blocks replacing or removing it.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func renameFile(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func removeFile(path string) error { return os.Remove(path) }
