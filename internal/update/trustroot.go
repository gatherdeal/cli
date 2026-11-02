package update

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/httpx"
)

// embeddedTrustedRoot is the Sigstore public-good trusted root (Fulcio,
// Rekor, CT log and TSA keys). It is refreshed before every CLI release
// with `go run ./cmd/releasecheck trusted-root -write`; the release
// workflow refuses to build when it is stale.
//
//go:embed trusted_root.json
var embeddedTrustedRoot []byte

// TrustedRootTarget is the TUF target holding the trusted root.
const TrustedRootTarget = "trusted_root.json"

// tufTimeout bounds the TUF refresh of the trusted root.
const tufTimeout = 15 * time.Second

// Trust sources reported by TrustedMaterial.
const (
	TrustSourceTUF      = "tuf"
	TrustSourceEmbedded = "embedded"
)

// EmbeddedTrustedRoot returns the trusted root compiled into this binary.
func EmbeddedTrustedRoot() (*root.TrustedRoot, error) {
	return root.NewTrustedRootFromJSON(embeddedTrustedRoot)
}

// EmbeddedTrustedRootJSON returns a copy of the embedded trusted root.
func EmbeddedTrustedRootJSON() []byte { return append([]byte(nil), embeddedTrustedRoot...) }

// FetchTrustedRootJSON fetches the current Sigstore public-good trusted
// root through TUF, anchored in the TUF root that sigstore-go embeds. The
// client reaches only the Sigstore TUF host, carries no credential and
// caches nothing on disk. base nil uses the default transport.
func FetchTrustedRootJSON(ctx context.Context, base http.RoundTripper) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, tufTimeout)
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		f := fetcher.NewDefaultFetcher()
		f.SetHTTPClient(httpx.NewAllowlisted(httpx.AllowlistOptions{
			Hosts:     []string{httpx.SigstoreTUFHost},
			UserAgent: buildinfo.UserAgent(),
			Base:      base,
			Timeout:   tufTimeout,
		}))
		opts := tuf.DefaultOptions().WithDisableLocalCache().WithFetcher(f).WithContext(ctx)
		c, err := tuf.New(opts)
		if err != nil {
			ch <- result{err: err}
			return
		}
		data, err := c.GetTarget(TrustedRootTarget)
		ch <- result{data: data, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("sigstore TUF: %w", r.err)
		}
		return r.data, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("sigstore TUF: %w", ctx.Err())
	}
}

// TrustedMaterial returns the Sigstore trusted root to verify releases
// with, and its source: the TUF-refreshed root when the TUF repository is
// reachable (it carries key rotations newer than this binary), else the
// root embedded in this binary. Either way verification stays mandatory;
// only the source of the trust root differs.
func TrustedMaterial(ctx context.Context, base http.RoundTripper) (root.TrustedMaterial, string, error) {
	if data, err := FetchTrustedRootJSON(ctx, base); err == nil {
		if tr, err := root.NewTrustedRootFromJSON(data); err == nil {
			return tr, TrustSourceTUF, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	tr, err := EmbeddedTrustedRoot()
	if err != nil {
		return nil, "", errors.Join(ErrVerify, err)
	}
	return tr, TrustSourceEmbedded, nil
}
