package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

const (
	githubBundleMaxMetadata   = release.MaxManifestBytes
	githubBundleMaxSig        = ed25519.SignatureSize
	nodeBundleMetadataTimeout = 20 * time.Second
	nodeBundleTempStaleAge    = 24 * time.Hour
)

const nodeBundleTempPrefix = ".mistgate-node-bundle-"

var ErrGitHubNodeBundleMissing = errors.New("the selected GitHub release has no signed node bundle")

// GitHubNodeBundleConfig configures the signed agent-bundle source. Production uses the fixed official release API;
// the API URL and HTTP client are injectable for tests.
type GitHubNodeBundleConfig struct {
	DataDir    string
	Key        ed25519.PublicKey
	Channel    func() release.Channel
	APIURL     string
	HTTPClient *http.Client
	Now        func() time.Time
	Log        *slog.Logger
}

// GitHubNodeBundleSource downloads node binaries from the newest release allowed by the panel's channel. It accepts a package only
// when the installation's pinned release key verifies its manifest and every file matches that manifest.
type GitHubNodeBundleSource struct {
	distDir        string
	key            ed25519.PublicKey
	apiURL         string
	channel        func() release.Channel
	client         *http.Client
	now            func() time.Time
	log            *slog.Logger
	mu             sync.Mutex
	available      bool
	manifestSHA256 string
	releases       []githubRelease
	releasesETag   string
}

func NewGitHubNodeBundleSource(cfg GitHubNodeBundleConfig) *GitHubNodeBundleSource {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.APIURL == "" {
		cfg.APIURL = GitHubReleaseListURL
	}
	distDir := ""
	if cfg.DataDir != "" {
		cfg.DataDir, _ = filepath.Abs(cfg.DataDir)
		distDir = filepath.Join(cfg.DataDir, "dist")
	}
	if cfg.HTTPClient == nil {
		// Request contexts bound metadata and downloads separately; a client timeout here would cap the
		// owner-configurable download budget for large signed bundles.
		cfg.HTTPClient = &http.Client{CheckRedirect: githubReleaseRedirect}
	}
	return &GitHubNodeBundleSource{
		distDir: distDir, key: append(ed25519.PublicKey(nil), cfg.Key...),
		apiURL: cfg.APIURL, channel: cfg.Channel, client: cfg.HTTPClient, now: cfg.Now, log: cfg.Log,
	}
}

func (u *GitHubNodeBundleSource) updateChannel() release.Channel {
	if u.channel != nil {
		if channel := u.channel(); channel.Valid() {
			return channel
		}
	}
	return release.ChannelStable
}

// BundleStatus reports whether the newest channel-visible signed GitHub bundle exactly matches the current manifest in dist/.
func (u *GitHubNodeBundleSource) BundleStatus() (bool, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.available, u.manifestSHA256
}

// Prepare restores an interrupted bundle swap and removes old staging directories before the panel reads dist/.
func (u *GitHubNodeBundleSource) Prepare() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.prepareLocked()
}

// prepareLocked holds u.mu so it cannot remove a staging directory used by Sync.
func (u *GitHubNodeBundleSource) prepareLocked() error {
	if u.distDir == "" || filepath.Clean(u.distDir) == "." {
		return nil
	}
	parent := filepath.Dir(u.distDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create node bundle directory: %w", err)
	}
	if err := recoverBundleDirectory(u.distDir, u.key, u.now()); err != nil {
		return fmt.Errorf("recover node bundle directory: %w", err)
	}
	if err := removeStaleNodeBundleDirs(parent, time.Now()); err != nil {
		return fmt.Errorf("remove stale node bundle directories: %w", err)
	}
	return nil
}

// Sync fetches the selected release's signed package when its build is newer than the trusted local package. It returns
// true only after the complete package has been verified and atomically installed.
func (u *GitHubNodeBundleSource) Sync(ctx context.Context, currentBuilt int64) (bool, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultUpdateDownloadTimeout)
		defer cancel()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.available = false
	u.manifestSHA256 = ""
	if u.distDir == "" || filepath.Clean(u.distDir) == "." {
		return false, errors.New("node bundle source has no data directory")
	}
	if err := u.prepareLocked(); err != nil {
		return false, err
	}
	if len(u.key) != ed25519.PublicKeySize {
		return false, errors.New("node bundle source has no valid release key")
	}

	metadataCtx, cancelMetadata := context.WithTimeout(ctx, nodeBundleMetadataTimeout)
	defer cancelMetadata()
	list, newETag, notModified, err := fetchGitHubReleaseList(metadataCtx, u.client, u.apiURL, "Mistgate-node-bundle-updater", u.releasesETag)
	if err != nil {
		return false, err
	}
	if notModified {
		if len(u.releases) == 0 {
			list, newETag, notModified, err = fetchGitHubReleaseList(metadataCtx, u.client, u.apiURL, "Mistgate-node-bundle-updater", "")
			if err != nil {
				return false, err
			}
			if notModified {
				return false, errors.New("GitHub returned 304 without a cached release list")
			}
		} else {
			list = u.releases
		}
	}
	u.releases, u.releasesETag = list, newETag
	r, err := latestGitHubRelease(list, u.updateChannel())
	if err != nil {
		return false, err
	}
	assets := make(map[string]githubAsset, len(r.Assets))
	for _, asset := range r.Assets {
		if _, exists := assets[asset.Name]; exists {
			return false, fmt.Errorf("GitHub release contains duplicate asset %q", asset.Name)
		}
		assets[asset.Name] = asset
	}
	manifestAsset, hasManifest := assets[release.ManifestName]
	signatureAsset, hasSignature := assets[release.SignatureName]
	if !hasManifest || !hasSignature {
		return false, ErrGitHubNodeBundleMissing
	}
	manifestBytes, err := downloadGitHubAsset(metadataCtx, u.client, r.TagName, manifestAsset, githubBundleMaxMetadata)
	if err != nil {
		return false, fmt.Errorf("download node bundle manifest: %w", err)
	}
	signature, err := downloadGitHubAsset(metadataCtx, u.client, r.TagName, signatureAsset, githubBundleMaxSig)
	if err != nil {
		return false, fmt.Errorf("download node bundle signature: %w", err)
	}
	if len(signature) != githubBundleMaxSig {
		return false, errors.New("GitHub node bundle signature has an invalid size")
	}
	manifest, err := release.Verify(u.key, manifestBytes, signature)
	if err != nil {
		return false, fmt.Errorf("verify GitHub node bundle: %w", err)
	}
	if manifest.Version != r.TagName {
		return false, fmt.Errorf("node bundle version %q does not match GitHub release %q", manifest.Version, r.TagName)
	}
	if err := manifest.Check(u.now(), 0); err != nil && !errors.Is(err, release.ErrUpToDate) {
		return false, fmt.Errorf("GitHub node bundle cannot be installed: %w", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := manifest.FileFor("linux", arch); err != nil {
			return false, fmt.Errorf("GitHub node bundle is missing linux/%s: %w", arch, err)
		}
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	manifestHash := hex.EncodeToString(manifestDigest[:])
	if manifest.Built < currentBuilt {
		return false, nil
	}
	if manifest.Built == currentBuilt {
		localManifest, readErr := os.ReadFile(filepath.Join(u.distDir, release.ManifestName))
		if readErr != nil {
			return false, nil
		}
		if bytes.Equal(localManifest, manifestBytes) {
			u.available = true
			u.manifestSHA256 = manifestHash
			return false, nil
		}
		localSignature, readErr := os.ReadFile(filepath.Join(u.distDir, release.SignatureName))
		if readErr != nil {
			return false, nil
		}
		local, verifyErr := release.Verify(u.key, localManifest, localSignature)
		if verifyErr != nil || local.Version != manifest.Version || !sameReleaseFiles(local.Files, manifest.Files) || manifest.Expires <= local.Expires {
			return false, nil
		}
	}

	for _, asset := range []githubAsset{manifestAsset, signatureAsset} {
		if err := validateGitHubBundleAsset(asset, r.TagName, githubBundleMaxMetadata); err != nil {
			return false, err
		}
	}
	stage, err := os.MkdirTemp(filepath.Dir(u.distDir), ".mistgate-node-bundle-*")
	if err != nil {
		return false, fmt.Errorf("create node bundle staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := writeBundleFile(stage, release.ManifestName, manifestBytes, 0o644); err != nil {
		return false, fmt.Errorf("stage node bundle manifest: %w", err)
	}
	if err := writeBundleFile(stage, release.SignatureName, signature, 0o644); err != nil {
		return false, fmt.Errorf("stage node bundle signature: %w", err)
	}
	for _, file := range manifest.Files {
		asset, ok := assets[file.Name]
		if !ok {
			return false, fmt.Errorf("GitHub release is missing signed node asset %q", file.Name)
		}
		if asset.Size != file.Size {
			return false, fmt.Errorf("GitHub node asset %q size %d does not match signed size %d", file.Name, asset.Size, file.Size)
		}
		if err := validateGitHubBundleAsset(asset, r.TagName, release.MaxFileSize); err != nil {
			return false, err
		}
		if err := u.downloadNodeFile(ctx, r.TagName, asset, file, stage); err != nil {
			return false, fmt.Errorf("download signed node asset %q: %w", file.Name, err)
		}
	}
	if err := replaceBundleDirectory(u.distDir, stage); err != nil {
		return false, fmt.Errorf("install GitHub node bundle: %w", err)
	}
	u.log.Info("installed trusted node bundle from GitHub", "version", manifest.Version, "built", manifest.Built)
	u.available = true
	u.manifestSHA256 = manifestHash
	return true, nil
}

func sameReleaseFiles(a, b []release.File) bool {
	if len(a) != len(b) {
		return false
	}
	byName := make(map[string]release.File, len(a))
	for _, file := range a {
		byName[file.Name] = file
	}
	for _, file := range b {
		if byName[file.Name] != file {
			return false
		}
	}
	return true
}

func validateGitHubBundleAsset(asset githubAsset, tag string, maxSize int64) error {
	if asset.Size <= 0 || asset.Size > maxSize {
		return fmt.Errorf("GitHub asset %q has an invalid size", asset.Name)
	}
	if asset.Digest != "" && (!strings.HasPrefix(asset.Digest, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(asset.Digest, "sha256:"))) {
		return fmt.Errorf("GitHub asset %q has an invalid SHA-256 digest", asset.Name)
	}
	u, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("GitHub asset %q URL is not official", asset.Name)
	}
	expected := "/Mistgate/mistgate/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(asset.Name)
	if u.EscapedPath() != expected {
		return fmt.Errorf("GitHub asset %q URL does not match release %q", asset.Name, tag)
	}
	return nil
}

func downloadGitHubAsset(ctx context.Context, client *http.Client, tag string, asset githubAsset, maxSize int64) ([]byte, error) {
	if err := validateGitHubBundleAsset(asset, tag, maxSize); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
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
		return nil, fmt.Errorf("GitHub asset download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxSize {
		return nil, errors.New("GitHub node metadata exceeds the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != asset.Size {
		return nil, fmt.Errorf("GitHub asset size %d does not match API size %d", len(data), asset.Size)
	}
	if err := checkGitHubAssetDigest(asset, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (u *GitHubNodeBundleSource) downloadNodeFile(ctx context.Context, tag string, asset githubAsset, file release.File, stage string) error {
	if err := validateGitHubBundleAsset(asset, tag, release.MaxFileSize); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mistgate-node-bundle-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub asset download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > file.Size {
		return errors.New("GitHub node asset exceeds its signed size")
	}
	path := filepath.Join(stage, file.Name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, file.Size+1))
	if copyErr == nil && n != file.Size {
		copyErr = fmt.Errorf("downloaded %d bytes, signed manifest says %d", n, file.Size)
	}
	if copyErr == nil && hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		copyErr = release.ErrHashMismatch
	}
	if copyErr == nil {
		copyErr = checkGitHubAssetHash(asset, h)
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

func checkGitHubAssetDigest(asset githubAsset, data []byte) error {
	if asset.Digest == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.TrimPrefix(asset.Digest, "sha256:") {
		return fmt.Errorf("GitHub asset %q SHA-256 does not match API metadata", asset.Name)
	}
	return nil
}

func checkGitHubAssetHash(asset githubAsset, got hash.Hash) error {
	if asset.Digest == "" {
		return nil
	}
	if hex.EncodeToString(got.Sum(nil)) != strings.TrimPrefix(asset.Digest, "sha256:") {
		return fmt.Errorf("GitHub asset %q SHA-256 does not match API metadata", asset.Name)
	}
	return nil
}

func writeBundleFile(dir, name string, data []byte, mode os.FileMode) error {
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

func replaceBundleDirectory(dest, stage string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	info, statErr := os.Lstat(dest)
	exists := statErr == nil
	if exists {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing node bundle path is not a regular directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	backup := stage + ".previous"
	hadPrevious := exists
	if hadPrevious {
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				return fmt.Errorf("%w (restore previous bundle: %v)", err, restoreErr)
			}
		}
		return err
	}
	if hadPrevious {
		_ = os.RemoveAll(backup)
	}
	return nil
}

type nodeBundleCandidate struct {
	path     string
	manifest *release.Manifest
	modTime  time.Time
	previous bool
}

func recoverBundleDirectory(dest string, key ed25519.PublicKey, now time.Time) error {
	info, err := os.Lstat(dest)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing node bundle path is not a regular directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(dest)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	var candidates []nodeBundleCandidate
	for _, entry := range entries {
		name := entry.Name()
		previous := strings.HasSuffix(name, ".previous")
		base := strings.TrimSuffix(name, ".previous")
		if !strings.HasPrefix(base, nodeBundleTempPrefix) || len(base) == len(nodeBundleTempPrefix) {
			continue
		}
		path := filepath.Join(parent, name)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		manifest, ok := verifyNodeBundleDirectory(path, key, now)
		if !ok {
			continue
		}
		candidates = append(candidates, nodeBundleCandidate{path: path, manifest: manifest, modTime: info.ModTime(), previous: previous})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.manifest.Built != b.manifest.Built {
			return a.manifest.Built > b.manifest.Built
		}
		if a.manifest.Expires != b.manifest.Expires {
			return a.manifest.Expires > b.manifest.Expires
		}
		if a.previous != b.previous {
			return !a.previous
		}
		return a.modTime.After(b.modTime)
	})
	return os.Rename(candidates[0].path, dest)
}

func verifyNodeBundleDirectory(dir string, key ed25519.PublicKey, now time.Time) (*release.Manifest, bool) {
	if len(key) != ed25519.PublicKeySize {
		return nil, false
	}
	manifestBytes, err := readRegularNodeBundleFile(filepath.Join(dir, release.ManifestName))
	if err != nil {
		return nil, false
	}
	signature, err := readRegularNodeBundleFile(filepath.Join(dir, release.SignatureName))
	if err != nil {
		return nil, false
	}
	manifest, err := release.Verify(key, manifestBytes, signature)
	if err != nil {
		return nil, false
	}
	if err := manifest.Check(now, 0); err != nil && !errors.Is(err, release.ErrUpToDate) {
		return nil, false
	}
	for _, file := range manifest.Files {
		path := filepath.Join(dir, file.Name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, false
		}
		verifyErr := release.VerifyFile(file, f)
		closeErr := f.Close()
		if verifyErr != nil || closeErr != nil {
			return nil, false
		}
	}
	return manifest, true
}

func readRegularNodeBundleFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("node bundle file is not regular")
	}
	return os.ReadFile(path)
}

func removeStaleNodeBundleDirs(parent string, now time.Time) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		base := strings.TrimSuffix(name, ".previous")
		if !strings.HasPrefix(base, nodeBundleTempPrefix) || len(base) == len(nodeBundleTempPrefix) {
			continue
		}
		path := filepath.Join(parent, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.ModTime().After(now) || now.Sub(info.ModTime()) < nodeBundleTempStaleAge {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}
