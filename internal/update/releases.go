package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/releasemeta"
)

// ReleasesURL is the only source of release metadata: fixed GitHub release
// download URLs of the pinned repository. There is no GitHub API call, no
// token and no server-provided URL.
const ReleasesURL = RepositoryURL + "/releases"

// ErrReleaseNotFound means the requested release (or, for the latest
// release, any release) does not exist.
var ErrReleaseNotFound = errors.New("release not found")

// NetworkError wraps a failure to fetch release metadata: network, TLS,
// redirect policy, unexpected status or size limit.
type NetworkError struct{ Err error }

func (e *NetworkError) Error() string { return "fetching release metadata: " + e.Err.Error() }
func (e *NetworkError) Unwrap() error { return e.Err }

// Releases fetches release.json and its Sigstore bundle.
type Releases struct {
	client *http.Client
}

// NewReleases returns a release-metadata client on base (nil: the default
// transport, honoring the standard proxy variables). It reaches only
// github.com and GitHub's release-asset hosts, follows at most
// httpx.MaxReleaseRedirects redirects and sends no credential.
func NewReleases(base http.RoundTripper) *Releases {
	return &Releases{client: httpx.NewAllowlisted(httpx.AllowlistOptions{
		Hosts:         []string{httpx.ReleaseHost},
		RedirectHosts: httpx.ReleaseAssetHosts,
		MaxRedirects:  httpx.MaxReleaseRedirects,
		UserAgent:     buildinfo.UserAgent(),
		Base:          base,
	})}
}

// TagURL is the release page of version.
func TagURL(version string) string { return ReleasesURL + "/tag/v" + version }

// DownloadURL is the download URL of a release asset.
func DownloadURL(version, name string) string {
	return ReleasesURL + "/download/v" + version + "/" + name
}

// Fetched is an unverified release.json with its bundle. Version is the
// version the manifest claims; Verifier.VerifyRelease authenticates it.
type Fetched struct {
	Version  string
	Manifest []byte
	Bundle   []byte
}

// Fetch downloads release.json and its bundle of version, or of the latest
// stable release when version is "". For the latest release the bundle is
// fetched from the exact version the manifest names, so the pair cannot
// straddle two releases.
func (r *Releases) Fetch(ctx context.Context, version string) (*Fetched, error) {
	manifestURL := ReleasesURL + "/latest/download/" + releasemeta.ManifestName
	if version != "" {
		manifestURL = DownloadURL(version, releasemeta.ManifestName)
	}
	data, err := r.get(ctx, manifestURL)
	if err != nil {
		return nil, err
	}
	claimed := version
	if claimed == "" {
		// Unverified until VerifyRelease binds it to the signing tag.
		m, err := releasemeta.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrVerify, err)
		}
		claimed = m.Version
	}
	bundle, err := r.get(ctx, DownloadURL(claimed, releasemeta.ManifestName+releasemeta.BundleSuffix))
	if err != nil {
		return nil, err
	}
	return &Fetched{Version: claimed, Manifest: data, Bundle: bundle}, nil
}

func (r *Releases) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, ErrReleaseNotFound
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &NetworkError{Err: fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)}
	}
	data, err := httpx.ReadLimited(resp, releasemeta.MaxManifestBytes)
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	return data, nil
}
