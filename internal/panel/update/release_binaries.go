package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

// releaseDownloadBase is where a release's signed node bundle is served from (a GitHub release download prefix, then
// "<tag>/<file>"). An e2e build points it at a local fake release with -X.
var releaseDownloadBase = panelReleasePrefix

const releaseMetadataTimeout = 30 * time.Second

// ReleaseNodeBinaries is a provision.BinarySource that has no bundle directory (the edge edition has no disk): it streams
// the node binary of this panel's own release from the release download URLs, so the node needs no route to GitHub.
// Trust is the compiled-in release key: the manifest must verify under Key, name this exact Version, be unexpired, and
// list the platform; the caller then checks the streamed bytes against the size and digest it gets back.
type ReleaseNodeBinaries struct {
	// Version is the release tag this panel was built from (v1.2.3). A development build has no tag and gets an error.
	Version string
	// Key verifies the release manifest (buildinfo.ReleasePublicKey()).
	Key ed25519.PublicKey
	// Client fetches the files; nil uses a client that follows only GitHub's own redirects. Its Timeout, if any, also
	// cuts the binary stream, so leave it unset and let the context bound the transfer.
	Client *http.Client
	// Base overrides releaseDownloadBase.
	Base string
}

// OpenNodeBinary implements provision.BinarySource. The returned reader streams the binary; nothing is buffered.
func (r *ReleaseNodeBinaries) OpenNodeBinary(ctx context.Context, goos, goarch string) (io.ReadCloser, int64, string, error) {
	if len(r.Key) != ed25519.PublicKeySize {
		return nil, 0, "", errors.New("update: this build has no release key to verify the node bundle")
	}
	if !validReleaseTag(r.Version) {
		return nil, 0, "", fmt.Errorf("update: %q is not a release tag: only a release build can fetch its node binary", r.Version)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{CheckRedirect: githubReleaseRedirect}
	}
	base := r.Base
	if base == "" {
		base = releaseDownloadBase
	}
	fileURL := func(name string) string { return base + url.PathEscape(r.Version) + "/" + url.PathEscape(name) }

	metaCtx, cancel := context.WithTimeout(ctx, releaseMetadataTimeout)
	defer cancel()
	manifestRaw, err := fetchSmall(metaCtx, client, fileURL(release.ManifestName), release.MaxManifestBytes)
	if err != nil {
		return nil, 0, "", fmt.Errorf("update: fetch the release manifest: %w", err)
	}
	sig, err := fetchSmall(metaCtx, client, fileURL(release.SignatureName), ed25519.SignatureSize)
	if err != nil {
		return nil, 0, "", fmt.Errorf("update: fetch the release signature: %w", err)
	}
	manifest, err := release.Verify(r.Key, manifestRaw, sig)
	if err != nil {
		return nil, 0, "", fmt.Errorf("update: verify the release manifest: %w", err)
	}
	if manifest.Version != r.Version {
		return nil, 0, "", fmt.Errorf("update: the manifest is for %q, not %q", manifest.Version, r.Version)
	}
	if time.Now().Unix() > manifest.Expires {
		return nil, 0, "", fmt.Errorf("update: the release manifest: %w", release.ErrExpired)
	}
	entry, err := manifest.FileFor(goos, goarch)
	if err != nil {
		return nil, 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL(entry.Name), nil)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("User-Agent", "Mistgate-node-bundle-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("update: fetch the node binary: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, "", fmt.Errorf("update: the node binary download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != entry.Size {
		resp.Body.Close()
		return nil, 0, "", fmt.Errorf("update: the node binary is %d bytes, the manifest says %d: %w", resp.ContentLength, entry.Size, release.ErrSizeMismatch)
	}
	return resp.Body, entry.Size, entry.SHA256, nil
}

// fetchSmall reads a whole small file that must be at most limit bytes.
func fetchSmall(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mistgate-node-bundle-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("larger than allowed")
	}
	return data, nil
}
