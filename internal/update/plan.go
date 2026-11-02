package update

import (
	"context"
	"errors"
	"fmt"

	"github.com/gatherdeal/cli/internal/releasemeta"
	"github.com/gatherdeal/cli/internal/semver"
)

// ManifestVerifier authenticates release.json for a version (the Sigstore
// Verifier; a fake in command tests).
type ManifestVerifier interface {
	VerifyRelease(data, bundleJSON []byte, version string) (*releasemeta.Manifest, error)
}

// States of a plan.
const (
	StateUpToDate        = "up_to_date"
	StateUpdateAvailable = "update_available"
	// StateDowngrade: --version names an older release.
	StateDowngrade = "downgrade"
	// StateAheadOfLatest: the latest release is older than this binary
	// (for example after a release was pulled).
	StateAheadOfLatest = "ahead_of_latest"
	// StateDevBuild: a development build has no comparable version.
	StateDevBuild = "dev_build"
)

// Action kinds.
const (
	ActionDelegate = "delegate"
	ActionManual   = "manual"
	ActionNone     = "none"
)

// Action says how to reach the target version. Argv is a command for the
// person (or the agent's operator) to run, never a shell string; the CLI
// never runs it itself.
type Action struct {
	Kind         string   `json:"kind"`
	Argv         []string `json:"argv,omitempty"`
	ProjectDir   string   `json:"project_dir,omitempty"`
	DownloadURL  string   `json:"download_url,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	Instructions string   `json:"instructions"`
}

// Plan is the result of `gatherdeal update [--check]`. It is produced only
// from a verified manifest, so Verified is always true.
type Plan struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	TargetVersion   string `json:"target_version"`
	UpdateAvailable bool   `json:"update_available"`
	State           string `json:"state"`
	InstallMethod   Method `json:"install_method"`
	Executable      string `json:"executable,omitempty"`
	Verified        bool   `json:"verified"`
	// TrustedRoot is the Sigstore trust root used: "tuf" or "embedded".
	TrustedRoot string `json:"trusted_root"`
	Action      Action `json:"action"`
	ReleaseURL  string `json:"release_url"`
	// Prerelease is true when the target is a pre-release.
	Prerelease bool `json:"prerelease"`
}

// ErrLatestIsPrerelease: the latest release resolved to a pre-release,
// which is never selected implicitly.
var ErrLatestIsPrerelease = errors.New("the latest release is a pre-release")

// Checker builds verified update plans.
type Checker struct {
	Host        Host
	Releases    *Releases
	Verifier    ManifestVerifier
	TrustedRoot string
}

// Check fetches and verifies the target release (requested, or the latest
// stable release when requested is "") and plans the update for the
// detected install method. requested must be canonical.
func (c *Checker) Check(ctx context.Context, requested string) (*Plan, error) {
	target, err := c.fetchVerified(ctx, requested)
	if err != nil {
		return nil, err
	}
	if requested == "" && target.Prerelease {
		return nil, ErrLatestIsPrerelease
	}
	inst := Detect(c.Host)
	if inst.RawCandidate() {
		own := target
		if target.Version != c.Host.Version {
			// Confirming a raw install needs this binary's own release;
			// any failure leaves the method unknown (manual), never fatal.
			own, _ = c.fetchVerified(ctx, c.Host.Version)
		}
		inst = Confirm(c.Host, inst, own)
	}
	return BuildPlan(c.Host, inst, target, requested != "", c.TrustedRoot), nil
}

func (c *Checker) fetchVerified(ctx context.Context, version string) (*releasemeta.Manifest, error) {
	if version != "" && !semver.Valid(version) {
		return nil, fmt.Errorf("%w: version %q", ErrReleaseNotFound, version)
	}
	f, err := c.Releases.Fetch(ctx, version)
	if err != nil {
		return nil, err
	}
	return c.Verifier.VerifyRelease(f.Manifest, f.Bundle, f.Version)
}

// BuildPlan plans the update from the verified target manifest.
func BuildPlan(h Host, inst Install, target *releasemeta.Manifest, explicit bool, trustedRoot string) *Plan {
	p := &Plan{
		CurrentVersion: h.Version,
		TargetVersion:  target.Version,
		InstallMethod:  inst.Method,
		Executable:     inst.Path,
		Verified:       true,
		TrustedRoot:    trustedRoot,
		ReleaseURL:     TagURL(target.Version),
		Prerelease:     target.Prerelease,
	}
	if !explicit {
		p.LatestVersion = target.Version
	}
	cur, err := semver.Parse(h.Version)
	tv := semver.MustParse(target.Version)
	switch {
	case err != nil:
		p.State = StateDevBuild
	case semver.Compare(cur, tv) == 0:
		p.State = StateUpToDate
	case semver.Less(cur, tv):
		p.State = StateUpdateAvailable
		p.UpdateAvailable = true
	case explicit:
		p.State = StateDowngrade
	default:
		p.State = StateAheadOfLatest
	}
	if p.State == StateUpToDate || p.State == StateAheadOfLatest {
		p.Action = Action{Kind: ActionNone, Instructions: "Nothing to do."}
		if p.State == StateAheadOfLatest {
			p.Action.Instructions = fmt.Sprintf("This CLI (%s) is newer than the latest release (%s); nothing to do. To go back to it, run `gatherdeal update --version %s --allow-downgrade`.", h.Version, target.Version, target.Version)
		}
		return p
	}
	p.Action = planAction(h, inst, target)
	return p
}

func planAction(h Host, inst Install, m *releasemeta.Manifest) Action {
	v := m.Version
	pkg := releasemeta.NPMPackage + "@" + v
	switch inst.Method {
	case MethodNPMGlobal:
		return Action{Kind: ActionDelegate, Argv: []string{"npm", "install", "-g", pkg, "--ignore-scripts"},
			Instructions: "Installed with npm (global). Run the command in argv to update; the CLI never runs npm itself."}
	case MethodNPMLocal:
		return Action{Kind: ActionManual, Argv: []string{"npm", "install", pkg, "--ignore-scripts"}, ProjectDir: inst.ProjectDir,
			Instructions: "Installed as a dependency of the project in project_dir. Update the dependency there (for example with the command in argv) and commit the lockfile change."}
	case MethodNPX:
		return Action{Kind: ActionNone, Argv: []string{"npx", pkg},
			Instructions: "Started with npx. Nothing to update: npx runs the version you name, for example the command in argv."}
	case MethodGoInstall:
		return Action{Kind: ActionManual, Argv: []string{"go", "install", "github.com/gatherdeal/cli/cmd/gatherdeal@v" + v},
			Instructions: "Installed with go install. Run the command in argv to update."}
	case MethodDev:
		return Action{Kind: ActionManual,
			Instructions: "This is a development build. Rebuild it from source, or install a release from release_url."}
	}
	a := Action{Kind: ActionManual}
	if asset, ok := m.Asset(h.GOOS, h.GOARCH); ok {
		a.DownloadURL = DownloadURL(v, asset.Name)
		a.SHA256 = asset.SHA256
	}
	switch inst.Method {
	case MethodReleaseBinary:
		a.Instructions = "Installed from a release archive. Download the archive at download_url, check that its SHA-256 equals sha256, and replace the executable with the gatherdeal binary inside it. This version of the CLI does not replace itself."
	case MethodUnwritable:
		a.Instructions = "The executable is in a directory you cannot write. Ask whoever installed it to update it, or install the release from download_url into a directory you own (the documented location is " + RawInstallPath(h) + "). The CLI never uses sudo or changes permissions."
	default:
		a.Instructions = "The CLI cannot tell how it was installed. Update it with the tool you installed it with, or install the release from download_url (check that its SHA-256 equals sha256)."
	}
	return a
}
