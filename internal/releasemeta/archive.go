package releasemeta

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// maxArchiveEntries bounds the number of entries an archive may hold
// (the binary, README.md, LICENSE and NOTICE today).
const maxArchiveEntries = 32

// ErrArchive marks an archive that breaks the release layout.
var ErrArchive = errors.New("release archive rejected")

// ExtractBinary streams the gatherdeal executable of a release archive at
// archivePath into w and returns its SHA-256 (hex) and size. It reads only
// the expected entry and rejects the whole archive when any entry has an
// unsafe name (absolute, "..", backslashes), is a link or special file, is
// duplicated or oversized.
func ExtractBinary(archivePath, goos string, w io.Writer) (string, int64, error) {
	name := BinaryName(goos)
	if goos == "windows" {
		return extractZip(archivePath, name, w)
	}
	return extractTarGz(archivePath, name, w)
}

func archiveErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrArchive, fmt.Sprintf(format, args...))
}

func checkEntryName(name string) error {
	if name == "" || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return archiveErr("unsafe entry name %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return archiveErr("unsafe entry name %q", name)
		}
	}
	return nil
}

func copyBinary(w io.Writer, r io.Reader, declared int64) (string, int64, error) {
	if declared <= 0 || declared > MaxBinaryBytes {
		return "", 0, archiveErr("executable size %d outside 1..%d", declared, MaxBinaryBytes)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(r, declared+1))
	if err != nil {
		return "", 0, err
	}
	if n != declared {
		return "", 0, archiveErr("executable is %d bytes, header says %d", n, declared)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func extractTarGz(archivePath, binary string, w io.Writer) (string, int64, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(io.LimitReader(f, MaxArchiveBytes+1))
	if err != nil {
		return "", 0, archiveErr("not a gzip stream: %v", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var sum string
	var size int64
	found := false
	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, archiveErr("corrupt tar: %v", err)
		}
		if i >= maxArchiveEntries {
			return "", 0, archiveErr("more than %d entries", maxArchiveEntries)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if err := checkEntryName(name); err != nil {
			return "", 0, err
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			continue
		default:
			return "", 0, archiveErr("entry %q is a link or special file", name)
		}
		if hdr.Size > MaxBinaryBytes {
			return "", 0, archiveErr("entry %q is %d bytes", name, hdr.Size)
		}
		if name != binary {
			continue
		}
		if found {
			return "", 0, archiveErr("entry %q appears twice", name)
		}
		if hdr.FileInfo().Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			return "", 0, archiveErr("entry %q carries setuid, setgid or sticky bits", name)
		}
		found = true
		if sum, size, err = copyBinary(w, tr, hdr.Size); err != nil {
			return "", 0, err
		}
	}
	if !found {
		return "", 0, archiveErr("no %s entry", binary)
	}
	return sum, size, nil
}

func extractZip(archivePath, binary string, w io.Writer) (string, int64, error) {
	fi, err := os.Stat(archivePath)
	if err != nil {
		return "", 0, err
	}
	if fi.Size() > MaxArchiveBytes {
		return "", 0, archiveErr("archive is %d bytes", fi.Size())
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", 0, archiveErr("not a zip archive: %v", err)
	}
	defer zr.Close()
	if len(zr.File) > maxArchiveEntries {
		return "", 0, archiveErr("more than %d entries", maxArchiveEntries)
	}
	var target *zip.File
	for _, f := range zr.File {
		if err := checkEntryName(f.Name); err != nil {
			return "", 0, err
		}
		mode := f.Mode()
		if mode.IsDir() {
			continue
		}
		if !mode.IsRegular() {
			return "", 0, archiveErr("entry %q is a link or special file", f.Name)
		}
		if f.UncompressedSize64 > MaxBinaryBytes {
			return "", 0, archiveErr("entry %q is %d bytes", f.Name, f.UncompressedSize64)
		}
		if f.Name == binary {
			if target != nil {
				return "", 0, archiveErr("entry %q appears twice", f.Name)
			}
			target = f
		}
	}
	if target == nil {
		return "", 0, archiveErr("no %s entry", binary)
	}
	rc, err := target.Open()
	if err != nil {
		return "", 0, archiveErr("cannot open %s: %v", binary, err)
	}
	defer rc.Close()
	return copyBinary(w, rc, int64(target.UncompressedSize64)) //nolint:gosec // bounded above
}
