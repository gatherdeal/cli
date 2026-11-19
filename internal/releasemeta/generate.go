package releasemeta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gatherdeal/cli/internal/semver"
)

// Generate builds the manifest of version from the GoReleaser archives in
// distDir. Every platform in Platforms must have its archive; the
// executable's hash is taken from inside the archive, exactly as a client
// will extract it.
func Generate(distDir, version string, publishedAt time.Time, minUpgradeFrom string) (*Manifest, error) {
	v, err := semver.Parse(version)
	if err != nil {
		return nil, fmt.Errorf("version %q: %w", version, err)
	}
	m := &Manifest{
		SchemaVersion:  SchemaVersion,
		Version:        v.String(),
		Prerelease:     v.Prerelease(),
		PublishedAt:    publishedAt.UTC().Truncate(time.Second),
		MinUpgradeFrom: minUpgradeFrom,
		NPM:            NPM{Package: NPMPackage, Version: v.String()},
	}
	for _, p := range Platforms {
		name := ArchiveName(m.Version, p[0], p[1])
		archive := filepath.Join(distDir, name)
		size, sum, err := fileSHA256(archive)
		if err != nil {
			return nil, err
		}
		binSum, _, err := ExtractBinary(archive, p[0], io.Discard)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		m.Assets = append(m.Assets, Asset{OS: p[0], Arch: p[1], Name: name, Size: size, SHA256: sum, BinarySHA256: binSum})
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func fileSHA256(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxArchiveBytes+1))
	if err != nil {
		return 0, "", err
	}
	if n > MaxArchiveBytes {
		return 0, "", fmt.Errorf("%s is larger than %d bytes", path, MaxArchiveBytes)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
