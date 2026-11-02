package update

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"os"

	"github.com/gatherdeal/cli/internal/releasemeta"
)

// CheckFile verifies a downloaded archive's size and SHA-256 against its
// authenticated manifest entry.
func CheckFile(path string, size int64, sha256Hex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, releasemeta.MaxArchiveBytes+1))
	if err != nil {
		return err
	}
	if n != size {
		return verifyErr("%s has %d bytes, release.json says %d", path, n, size)
	}
	if !equalHex(hex.EncodeToString(h.Sum(nil)), sha256Hex) {
		return verifyErr("%s does not match its SHA-256 in release.json", path)
	}
	return nil
}

// ExtractVerified extracts the executable of a verified archive to dest
// (created owner-only and executable) and checks it against binarySHA256.
// On any failure dest is removed.
func ExtractVerified(archive, goos, dest, binarySHA256 string) (err error) {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700) //nolint:gosec // an owner-only executable
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dest)
		}
	}()
	sum, _, err := releasemeta.ExtractBinary(archive, goos, f)
	if err != nil {
		return verifyErr("%v", err)
	}
	if !equalHex(sum, binarySHA256) {
		return verifyErr("the extracted binary does not match binary_sha256 in release.json")
	}
	return f.Sync()
}

func equalHex(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
