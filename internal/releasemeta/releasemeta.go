// Package releasemeta defines release.json, the signed release manifest
// published next to every release's archives. The release workflow writes
// it with Generate; `gatherdeal update` and cmd/releasecheck read it with
// Parse. Keeping one definition means the schema cannot drift.
//
// The schema is irreversible once published binaries depend on it: fields
// may be added, never removed or redefined, and SchemaVersion changes only
// with a transition release.
package releasemeta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/gatherdeal/cli/internal/semver"
)

const (
	// SchemaVersion is the only manifest schema this package reads.
	SchemaVersion = 1
	// ManifestName is the file name of the manifest in a release.
	ManifestName = "release.json"
	// ChecksumsName is GoReleaser's checksums file, signed like the manifest.
	ChecksumsName = "checksums.txt"
	// BundleSuffix names the Sigstore bundle of a signed file
	// (release.json.sigstore.json).
	BundleSuffix = ".sigstore.json"
	// NPMPackage is the npm launcher package.
	NPMPackage = "@gatherdeal/cli"
	// MinUpgradeFrom is the default min_upgrade_from of new releases: the
	// oldest version that may update in place. Raising it is a deliberate
	// change (for example when the credential or profile format changes).
	MinUpgradeFrom = "0.1.0"

	// MaxManifestBytes bounds release.json and its bundle.
	MaxManifestBytes = 64 << 10
	// MaxArchiveBytes bounds a release archive.
	MaxArchiveBytes = 64 << 20
	// MaxBinaryBytes bounds the extracted executable.
	MaxBinaryBytes = 192 << 20
)

// Platforms are the release targets as GOOS/GOARCH pairs.
var Platforms = [][2]string{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// Manifest is release.json.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Version       string    `json:"version"`
	Prerelease    bool      `json:"prerelease"`
	PublishedAt   time.Time `json:"published_at"`
	// MinUpgradeFrom is the oldest version that may update to this one in
	// place; older installs get manual instructions.
	MinUpgradeFrom string  `json:"min_upgrade_from"`
	Assets         []Asset `json:"assets"`
	NPM            NPM     `json:"npm"`
}

// Asset is one release archive.
type Asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// BinarySHA256 is the SHA-256 of the gatherdeal executable inside the
	// archive (the same bytes the npm platform package ships).
	BinarySHA256 string `json:"binary_sha256"`
}

// NPM names the npm release of the same version.
type NPM struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

// ArchiveName is the GoReleaser archive name of a target.
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "gatherdeal_" + version + "_" + goos + "_" + goarch + ext
}

// BinaryName is the executable's name inside an archive.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "gatherdeal.exe"
	}
	return "gatherdeal"
}

var (
	hexSHA256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	platformRE  = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
)

// Parse decodes and validates a manifest. Unknown fields are ignored so a
// newer release can add some; everything this CLI relies on is checked.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("release manifest is larger than %d bytes", MaxManifestBytes)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	if dec.More() {
		return nil, errors.New("release manifest: trailing data")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks every field this CLI relies on.
func (m *Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("release manifest schema_version %d is not supported (want %d)", m.SchemaVersion, SchemaVersion)
	}
	v, err := semver.Parse(m.Version)
	if err != nil || v.Build != "" || v.String() != m.Version {
		return fmt.Errorf("release manifest version %q is not a canonical version", m.Version)
	}
	if m.Prerelease != v.Prerelease() {
		return fmt.Errorf("release manifest prerelease=%v contradicts version %s", m.Prerelease, m.Version)
	}
	if m.PublishedAt.IsZero() {
		return errors.New("release manifest published_at is missing")
	}
	minFrom, err := semver.Parse(m.MinUpgradeFrom)
	if err != nil {
		return fmt.Errorf("release manifest min_upgrade_from %q is not a version", m.MinUpgradeFrom)
	}
	if semver.Less(v, minFrom) {
		return fmt.Errorf("release manifest min_upgrade_from %s is newer than the release", m.MinUpgradeFrom)
	}
	if len(m.Assets) == 0 {
		return errors.New("release manifest has no assets")
	}
	seen := map[string]bool{}
	for i, a := range m.Assets {
		if !platformRE.MatchString(a.OS) || !platformRE.MatchString(a.Arch) {
			return fmt.Errorf("release manifest asset %d has a malformed platform", i)
		}
		key := a.OS + "/" + a.Arch
		if seen[key] {
			return fmt.Errorf("release manifest lists %s twice", key)
		}
		seen[key] = true
		if a.Name != ArchiveName(m.Version, a.OS, a.Arch) {
			return fmt.Errorf("release manifest asset %s is named %q, want %q", key, a.Name, ArchiveName(m.Version, a.OS, a.Arch))
		}
		if a.Size <= 0 || a.Size > MaxArchiveBytes {
			return fmt.Errorf("release manifest asset %s has size %d outside 1..%d", key, a.Size, MaxArchiveBytes)
		}
		if !hexSHA256RE.MatchString(a.SHA256) || !hexSHA256RE.MatchString(a.BinarySHA256) {
			return fmt.Errorf("release manifest asset %s has a malformed sha256", key)
		}
	}
	if m.NPM.Package != NPMPackage || m.NPM.Version != m.Version {
		return fmt.Errorf("release manifest npm %s@%s does not match %s@%s", m.NPM.Package, m.NPM.Version, NPMPackage, m.Version)
	}
	return nil
}

// Asset returns the archive of a target.
func (m *Manifest) Asset(goos, goarch string) (Asset, bool) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Asset{}, false
}

// Marshal encodes m as indented JSON with a trailing newline.
func (m *Manifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
