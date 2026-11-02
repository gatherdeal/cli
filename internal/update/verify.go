// Package update implements `gatherdeal update`: install-method detection,
// the release client and the mandatory Sigstore verification of release
// metadata. cmd/releasecheck uses the same verifier for the release smoke
// test, so a release is checked exactly as clients will check it.
package update

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/gatherdeal/cli/internal/releasemeta"
	"github.com/gatherdeal/cli/internal/semver"
)

// The release identity. It is pinned here and irreversible once published
// binaries depend on it: moving the repository or the workflow needs a
// transition release that trusts both identities.
const (
	// RepositoryURL is the public repository releases are built from.
	RepositoryURL = "https://github.com/gatherdeal/cli"
	// WorkflowPath is the only workflow allowed to sign releases.
	WorkflowPath = ".github/workflows/release.yml"
	// OIDCIssuer is GitHub Actions' token issuer.
	OIDCIssuer = "https://token.actions.githubusercontent.com"
	// RunnerEnvironment requires a GitHub-hosted runner.
	RunnerEnvironment = "github-hosted"
	// BuildTrigger is the event of a tag push.
	BuildTrigger = "push"
)

// ErrVerify marks every verification failure: signature, certificate
// identity, transparency log, version binding or hash mismatch. It is
// never retryable and never yields a plan.
var ErrVerify = errors.New("release verification failed")

func verifyErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrVerify, fmt.Sprintf(format, args...))
}

// Identity is the exact signing identity of one release.
type Identity struct {
	// SAN is the certificate's subject alternative name: the workflow at
	// the release tag.
	SAN    string
	Issuer string
	// Ref is the certificate's source-repository ref (refs/tags/v<version>).
	Ref        string
	Repository string
}

// ExpectedIdentity returns the identity that must have signed version.
// Binding the tag ref to the version means a valid signature of another
// release (any other v* tag) is refused.
func ExpectedIdentity(version string) Identity {
	return identityAt(RepositoryURL, WorkflowPath, "refs/tags/v"+version)
}

func identityAt(repository, workflow, ref string) Identity {
	return Identity{
		SAN:        repository + "/" + workflow + "@" + ref,
		Issuer:     OIDCIssuer,
		Ref:        ref,
		Repository: repository,
	}
}

// extensions are the Fulcio certificate extensions the identity requires
// besides the issuer, which sigstore-go matches separately.
func (id Identity) extensions() certificate.Extensions {
	return certificate.Extensions{
		SourceRepositoryURI: id.Repository,
		SourceRepositoryRef: id.Ref,
		BuildSignerURI:      id.SAN,
		BuildConfigURI:      id.SAN,
		BuildTrigger:        BuildTrigger,
		RunnerEnvironment:   RunnerEnvironment,
	}
}

// certificateIdentity is the sigstore-go policy for the identity.
func (id Identity) certificateIdentity() (verify.CertificateIdentity, error) {
	san, err := verify.NewSANMatcher(id.SAN, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuer, err := verify.NewIssuerMatcher(id.Issuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	return verify.NewCertificateIdentity(san, issuer, id.extensions())
}

// checkIdentity compares the bundle's signing certificate with id before
// any cryptographic check, so a signature by anything else fails with a
// precise message. sigstore-go enforces the same identity again during
// verification.
func checkIdentity(b *bundle.Bundle, id Identity) error {
	vc, err := b.VerificationContent()
	if err != nil {
		return verifyErr("signature bundle: %v", err)
	}
	cert := vc.Certificate()
	if cert == nil {
		return verifyErr("signature bundle is not signed with a Sigstore certificate")
	}
	got, err := certificate.SummarizeCertificate(cert)
	if err != nil {
		return verifyErr("signing certificate: %v", err)
	}
	want := id.extensions()
	if got.SubjectAlternativeName != id.SAN {
		return verifyErr("signed by %q, want %q", got.SubjectAlternativeName, id.SAN)
	}
	for _, f := range []struct{ name, got, want string }{
		{"issuer", got.Issuer, id.Issuer},
		{"source repository", got.SourceRepositoryURI, want.SourceRepositoryURI},
		{"source ref", got.SourceRepositoryRef, want.SourceRepositoryRef},
		{"build signer", got.BuildSignerURI, want.BuildSignerURI},
		{"build config", got.BuildConfigURI, want.BuildConfigURI},
		{"build trigger", got.BuildTrigger, want.BuildTrigger},
		{"runner environment", got.RunnerEnvironment, want.RunnerEnvironment},
	} {
		if f.got != f.want {
			return verifyErr("signing certificate %s is %q, want %q", f.name, f.got, f.want)
		}
	}
	return nil
}

// Verifier checks Sigstore bundles against a trusted root and the pinned
// release identity.
type Verifier struct {
	trusted root.TrustedMaterial
}

// NewVerifier returns a verifier for the trusted material (the embedded or
// TUF-refreshed Sigstore trusted root; a test root in tests).
func NewVerifier(trusted root.TrustedMaterial) *Verifier { return &Verifier{trusted: trusted} }

// VerifyBlob verifies that bundleJSON is a Sigstore bundle over artifact,
// signed by the release workflow at tag v<version>: Fulcio certificate
// chain, an embedded certificate transparency timestamp, a Rekor
// transparency log entry and an observer timestamp, and the full pinned
// certificate identity. There is no unverified mode.
func (v *Verifier) VerifyBlob(artifact, bundleJSON []byte, version string) error {
	sv, err := semver.Parse(version)
	if err != nil || sv.Build != "" || sv.String() != version {
		return verifyErr("version %q is not canonical", version)
	}
	if len(bundleJSON) == 0 {
		return verifyErr("signature bundle is missing")
	}
	if len(bundleJSON) > releasemeta.MaxManifestBytes {
		return verifyErr("signature bundle is larger than %d bytes", releasemeta.MaxManifestBytes)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return verifyErr("signature bundle is malformed: %v", err)
	}
	want := ExpectedIdentity(version)
	if err := checkIdentity(&b, want); err != nil {
		return err
	}
	id, err := want.certificateIdentity()
	if err != nil {
		return verifyErr("%v", err)
	}
	sev, err := verify.NewVerifier(v.trusted,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return verifyErr("%v", err)
	}
	res, err := sev.Verify(&b, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(artifact)), verify.WithCertificateIdentity(id)))
	if err != nil {
		return verifyErr("%v", err)
	}
	// Belt and braces: the policy already matched these.
	if res.Signature == nil || res.Signature.Certificate == nil ||
		res.Signature.Certificate.SourceRepositoryRef != want.Ref ||
		res.Signature.Certificate.SubjectAlternativeName != want.SAN {
		return verifyErr("certificate identity does not match %s", want.SAN)
	}
	return nil
}

// VerifyManifest verifies release.json against its bundle and returns the
// authenticated manifest. The signature must come from the tag of the
// manifest's own version (version binding), so a manifest of one release
// cannot be passed off with another release's signature.
func (v *Verifier) VerifyManifest(data, bundleJSON []byte) (*releasemeta.Manifest, error) {
	m, err := releasemeta.Parse(data)
	if err != nil {
		return nil, verifyErr("%v", err)
	}
	if err := v.VerifyBlob(data, bundleJSON, m.Version); err != nil {
		return nil, err
	}
	return m, nil
}

// VerifyRelease verifies release.json and binds it to the requested
// version: the authenticated manifest must be exactly that release.
func (v *Verifier) VerifyRelease(data, bundleJSON []byte, version string) (*releasemeta.Manifest, error) {
	m, err := v.VerifyManifest(data, bundleJSON)
	if err != nil {
		return nil, err
	}
	if m.Version != version {
		return nil, verifyErr("release.json is for %s, not the requested %s", m.Version, version)
	}
	return m, nil
}
