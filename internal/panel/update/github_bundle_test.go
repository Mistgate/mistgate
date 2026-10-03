package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

func signedNodeRelease(t *testing.T, priv ed25519.PrivateKey, tag string, built int64, expires int64, files map[string][]byte) ([]byte, map[string][]byte) {
	t.Helper()
	manifest := &release.Manifest{Schema: release.Schema, Version: tag, Built: built, Expires: expires}
	assets := make(map[string][]byte, len(files)+2)
	for name, data := range files {
		base, goos, arch, err := release.ParseBinaryName(name)
		if err != nil || base != "mistgate-node" {
			t.Fatalf("invalid test node binary name %q: %v", name, err)
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, release.File{OS: goos, Arch: arch, Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		assets[name] = data
	}
	body, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	assets[release.ManifestName] = body
	assets[release.SignatureName] = release.Sign(priv, body)

	r := githubRelease{TagName: tag, HTMLURL: githubReleasePageBase + tag, PublishedAt: time.Unix(built, 0).UTC()}
	for name, data := range assets {
		sum := sha256.Sum256(data)
		r.Assets = append(r.Assets, githubAsset{Name: name, Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(sum[:]),
			BrowserDownloadURL: panelReleasePrefix + tag + "/" + url.PathEscape(name)})
	}
	api, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return api, assets
}

func newTestGitHubNodeBundleSource(t *testing.T, pub []byte, apiBody []byte, assets map[string][]byte, requested map[string]int) (*GitHubNodeBundleSource, string) {
	t.Helper()
	return newTestGitHubNodeBundleSourceAt(t, pub, apiBody, assets, requested, t.TempDir())
}

func newTestGitHubNodeBundleSourceAt(t *testing.T, pub []byte, apiBody []byte, assets map[string][]byte, requested map[string]int, dataDir string) (*GitHubNodeBundleSource, string) {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			return githubResponse(http.StatusOK, apiBody), nil
		}
		if r.URL.Host == "github.com" {
			name := filepath.Base(r.URL.Path)
			requested[name]++
			body, ok := assets[name]
			if !ok {
				return githubResponse(http.StatusNotFound, nil), nil
			}
			return githubResponse(http.StatusOK, body), nil
		}
		return nil, errors.New("unexpected host " + r.URL.Host)
	})}
	u := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{
		DataDir: dataDir, Key: pub, HTTPClient: client,
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	return u, dataDir
}

func TestGitHubNodeBundleSourceDownloadsAndVerifiesRelease(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("signed linux amd64 agent"),
		"mistgate-node-linux-arm64": []byte("signed linux arm64 agent"),
	}
	apiBody, assets := signedNodeRelease(t, priv, "v0.2.0", 1_900_000_000, 2_000_000_000, files)
	requested := map[string]int{}
	u, dataDir := newTestGitHubNodeBundleSource(t, priv.Public().(ed25519.PublicKey), apiBody, assets, requested)

	changed, err := u.Sync(context.Background(), 0)
	if err != nil || !changed {
		t.Fatalf("sync = %v, %v", changed, err)
	}
	if available, hash := u.BundleStatus(); !available || len(hash) != 64 {
		t.Fatalf("GitHub release not marked available: available=%v hash=%q", available, hash)
	}
	dist := filepath.Join(dataDir, "dist")
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("downloaded %s = %q, %v", name, got, err)
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(dist, release.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile(filepath.Join(dist, release.SignatureName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := release.Verify(priv.Public().(ed25519.PublicKey), manifestBytes, signature)
	if err != nil || manifest.Version != "v0.2.0" {
		t.Fatalf("installed manifest: %+v, %v", manifest, err)
	}
	if runtime.GOOS != "windows" {
		if got, err := os.Stat(filepath.Join(dist, "mistgate-node-linux-amd64")); err != nil || got.Mode().Perm()&0o111 == 0 {
			t.Fatalf("agent executable mode: %v, %v", got, err)
		}
	}

	changed, err = u.Sync(context.Background(), manifest.Built)
	if err != nil || changed {
		t.Fatalf("unchanged release sync = %v, %v", changed, err)
	}
	if available, hash := u.BundleStatus(); !available || len(hash) != 64 {
		t.Fatalf("same GitHub release lost availability: available=%v hash=%q", available, hash)
	}
	if requested["mistgate-node-linux-amd64"] != 1 || requested["mistgate-node-linux-arm64"] != 1 {
		t.Fatalf("same-build poll re-downloaded binaries: %#v", requested)
	}
}

func TestGitHubNodeBundleSourceRenewsExpiryForSameBuild(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("same amd64 binary"),
		"mistgate-node-linux-arm64": []byte("same arm64 binary"),
	}
	firstAPI, firstAssets := signedNodeRelease(t, priv, "v0.2.0", 1_800_000_100, 1_850_000_000, files)
	requested := map[string]int{}
	u, dataDir := newTestGitHubNodeBundleSource(t, priv.Public().(ed25519.PublicKey), firstAPI, firstAssets, requested)
	if changed, err := u.Sync(context.Background(), 0); err != nil || !changed {
		t.Fatalf("initial sync = %v, %v", changed, err)
	}

	renewedAPI, renewedAssets := signedNodeRelease(t, priv, "v0.2.0", 1_800_000_100, 2_100_000_000, files)
	requested = map[string]int{}
	renewed, _ := newTestGitHubNodeBundleSourceAt(t, priv.Public().(ed25519.PublicKey), renewedAPI, renewedAssets, requested, dataDir)
	if changed, err := renewed.Sync(context.Background(), 1_800_000_100); err != nil || !changed {
		t.Fatalf("same-build expiry renewal = %v, %v", changed, err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(dataDir, "dist", release.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := release.Parse(manifestBytes)
	if err != nil || manifest.Expires != 2_100_000_000 {
		t.Fatalf("renewed manifest expiry = %+v, %v", manifest, err)
	}
	if available, hash := renewed.BundleStatus(); !available || len(hash) != 64 {
		t.Fatalf("renewed release not marked available: available=%v hash=%q", available, hash)
	}
}

func TestGitHubNodeBundleSourceLeavesCurrentBundleOnTamperedBinary(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("correct amd64 binary"),
		"mistgate-node-linux-arm64": []byte("correct arm64 binary"),
	}
	apiBody, assets := signedNodeRelease(t, priv, "v0.2.0", 1_900_000_000, 2_000_000_000, files)
	assets["mistgate-node-linux-amd64"] = []byte("corrupt amd64 binary") // same size; API digest matches the tampered body
	requested := map[string]int{}
	u, dataDir := newTestGitHubNodeBundleSource(t, priv.Public().(ed25519.PublicKey), apiBody, assets, requested)
	dist := filepath.Join(dataDir, "dist")
	if err := os.MkdirAll(dist, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "keep-me"), []byte("old bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := u.Sync(context.Background(), 0)
	if err == nil || changed || !errors.Is(err, release.ErrHashMismatch) {
		t.Fatalf("tampered bundle sync = %v, %v", changed, err)
	}
	got, readErr := os.ReadFile(filepath.Join(dist, "keep-me"))
	if readErr != nil || string(got) != "old bundle" {
		t.Fatalf("previous bundle was changed: %q, %v", got, readErr)
	}
}

func TestGitHubNodeBundleSourceRequiresSignedSidecars(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	apiBody, assets := signedNodeRelease(t, priv, "v0.2.0", 1_900_000_000, 2_000_000_000, map[string][]byte{
		"mistgate-node-linux-amd64": []byte("amd64"), "mistgate-node-linux-arm64": []byte("arm64"),
	})
	delete(assets, release.SignatureName)
	// Keep the release API response structurally valid while removing the signature asset.
	var r githubRelease
	if err := json.Unmarshal(apiBody, &r); err != nil {
		t.Fatal(err)
	}
	for i := range r.Assets {
		if r.Assets[i].Name == release.SignatureName {
			r.Assets = append(r.Assets[:i], r.Assets[i+1:]...)
			break
		}
	}
	apiBody, _ = json.Marshal(r)
	requested := map[string]int{}
	u, dataDir := newTestGitHubNodeBundleSource(t, priv.Public().(ed25519.PublicKey), apiBody, assets, requested)
	changed, err := u.Sync(context.Background(), 0)
	if changed || !errors.Is(err, ErrGitHubNodeBundleMissing) {
		t.Fatalf("unsigned release sync = %v, %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "dist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created dist for unsigned release: %v", err)
	}
}

func TestGitHubNodeBundleSourceRejectsSignatureFromAnotherKey(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	apiBody, assets := signedNodeRelease(t, priv, "v0.2.0", 1_900_000_000, 2_000_000_000, map[string][]byte{
		"mistgate-node-linux-amd64": []byte("amd64"), "mistgate-node-linux-arm64": []byte("arm64"),
	})
	requested := map[string]int{}
	u, dataDir := newTestGitHubNodeBundleSource(t, otherPub, apiBody, assets, requested)
	changed, err := u.Sync(context.Background(), 0)
	if changed || !errors.Is(err, release.ErrBadSignature) {
		t.Fatalf("foreign signature sync = %v, %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "dist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created dist for a foreign signature: %v", err)
	}
}

func TestGitHubNodeBundleAssetRejectsUntrustedURL(t *testing.T) {
	asset := githubAsset{Name: "mistgate-node-linux-amd64", Size: 1, BrowserDownloadURL: "https://example.com/binary"}
	if err := validateGitHubBundleAsset(asset, "v0.2.0", release.MaxFileSize); err == nil {
		t.Fatal("accepted an untrusted asset host")
	}
}
