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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

func signedNodeRelease(t *testing.T, priv ed25519.PrivateKey, tag string, built int64, expires int64, files map[string][]byte) ([]byte, map[string][]byte) {
	t.Helper()
	manifest := &release.Manifest{Schema: release.Schema, Version: tag, Built: built, Expires: expires}
	assets := make(map[string][]byte, len(files)+2)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
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
	api, err := json.Marshal([]githubRelease{r})
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
		Now: func() time.Time { return time.Unix(1_900_000_000, 0) },
	})
	return u, dataDir
}

func writeTestNodeBundleDir(t *testing.T, dir string, priv ed25519.PrivateKey, version string, built int64, files map[string][]byte) {
	t.Helper()
	writeTestNodeBundleDirWithExpiry(t, dir, priv, version, built, built+1000, files)
}

func writeTestNodeBundleDirWithExpiry(t *testing.T, dir string, priv ed25519.PrivateKey, version string, built, expires int64, files map[string][]byte) {
	t.Helper()
	_, assets := signedNodeRelease(t, priv, version, built, expires, files)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range assets {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitHubNodeBundleSourceRecoversInterruptedSwap(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("amd64"),
		"mistgate-node-linux-arm64": []byte("arm64"),
	}

	t.Run("before first rename", func(t *testing.T) {
		dataDir := t.TempDir()
		dist := filepath.Join(dataDir, "dist")
		writeTestNodeBundleDir(t, dist, priv, "v0.1.0", 10, files)
		stage, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
		if err != nil {
			t.Fatal(err)
		}
		writeTestNodeBundleDir(t, stage, priv, "v0.2.0", 20, files)
		stale := time.Now().Add(-nodeBundleTempStaleAge - time.Minute)
		if err := os.Chtimes(stage, stale, stale); err != nil {
			t.Fatal(err)
		}

		source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{DataDir: dataDir, Key: pub, Now: func() time.Time { return time.Unix(30, 0) }})
		if err := source.Prepare(); err != nil {
			t.Fatal(err)
		}
		assertBundleVersion(t, dist, "v0.1.0")
		if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale stage remains: %v", err)
		}
	})

	t.Run("between renames prefers complete new bundle", func(t *testing.T) {
		dataDir := t.TempDir()
		dist := filepath.Join(dataDir, "dist")
		writeTestNodeBundleDir(t, dist, priv, "v0.1.0", 10, files)
		stage, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
		if err != nil {
			t.Fatal(err)
		}
		writeTestNodeBundleDir(t, stage, priv, "v0.2.0", 20, files)
		backup := stage + ".previous"
		if err := os.Rename(dist, backup); err != nil {
			t.Fatal(err)
		}

		source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{DataDir: dataDir, Key: pub, Now: func() time.Time { return time.Unix(30, 0) }})
		if err := source.Prepare(); err != nil {
			t.Fatal(err)
		}
		assertBundleVersion(t, dist, "v0.2.0")
		if _, err := os.Stat(backup); err != nil {
			t.Fatalf("recent previous bundle should remain until it is stale: %v", err)
		}
	})

	t.Run("between renames restores complete previous bundle", func(t *testing.T) {
		dataDir := t.TempDir()
		stage, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, release.ManifestName), []byte("incomplete"), 0o600); err != nil {
			t.Fatal(err)
		}
		backup := stage + ".previous"
		writeTestNodeBundleDir(t, backup, priv, "v0.1.0", 10, files)

		source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{DataDir: dataDir, Key: pub, Now: func() time.Time { return time.Unix(30, 0) }})
		if err := source.Prepare(); err != nil {
			t.Fatal(err)
		}
		assertBundleVersion(t, filepath.Join(dataDir, "dist"), "v0.1.0")
	})

	t.Run("after second rename keeps dist and ages out previous bundle", func(t *testing.T) {
		dataDir := t.TempDir()
		dist := filepath.Join(dataDir, "dist")
		writeTestNodeBundleDir(t, dist, priv, "v0.2.0", 20, files)
		backup := filepath.Join(dataDir, nodeBundleTempPrefix+"swap.previous")
		writeTestNodeBundleDir(t, backup, priv, "v0.1.0", 10, files)

		source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{DataDir: dataDir, Key: pub, Now: func() time.Time { return time.Unix(30, 0) }})
		if err := source.Prepare(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(backup); err != nil {
			t.Fatalf("recent previous bundle should remain until it is stale: %v", err)
		}
		stale := time.Now().Add(-nodeBundleTempStaleAge - time.Minute)
		if err := os.Chtimes(backup, stale, stale); err != nil {
			t.Fatal(err)
		}
		if err := source.Prepare(); err != nil {
			t.Fatal(err)
		}
		assertBundleVersion(t, dist, "v0.2.0")
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale previous bundle remains: %v", err)
		}
	})

	t.Run("expired new bundle restores the usable previous bundle", func(t *testing.T) {
		dataDir := t.TempDir()
		stage, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
		if err != nil {
			t.Fatal(err)
		}
		writeTestNodeBundleDirWithExpiry(t, stage, priv, "v0.2.0", 20, 25, files)
		backup := stage + ".previous"
		writeTestNodeBundleDirWithExpiry(t, backup, priv, "v0.1.0", 10, 100, files)

		source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{DataDir: dataDir, Key: pub, Now: func() time.Time { return time.Unix(30, 0) }})
		if err := source.Prepare(); err != nil {
			t.Fatal(err)
		}
		assertBundleVersion(t, filepath.Join(dataDir, "dist"), "v0.1.0")
	})
}

func TestNewRecoversNodeBundleBeforeItsInitialScan(t *testing.T) {
	e := newEnv(t)
	e.clk.mu.Lock()
	e.clk.t = time.Unix(20, 0)
	e.clk.mu.Unlock()
	dataDir := e.dir
	stage, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("amd64"),
		"mistgate-node-linux-arm64": []byte("arm64"),
	}
	writeTestNodeBundleDir(t, stage, e.priv, "v0.2.0", 20, files)
	backup := stage + ".previous"
	writeTestNodeBundleDir(t, backup, e.priv, "v0.1.0", 10, files)
	stale, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-nodeBundleTempStaleAge - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{DataDir: dataDir, Key: e.pub, Now: e.clk.Now})
	cfg := e.s.cfg
	cfg.NodeBundleSource = source
	service, err := New(e.st, e.fl, e.hl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if current := service.current(); current == nil || !current.trusted || current.manifest.Version != "v0.2.0" {
		t.Fatalf("initial bundle after interrupted swap = %+v, want the recovered signed bundle", current)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale bundle directory remains after service start: %v", err)
	}
}

func assertBundleVersion(t *testing.T, dir, want string) {
	t.Helper()
	manifestBytes, err := os.ReadFile(filepath.Join(dir, release.ManifestName))
	if err != nil {
		t.Fatalf("read bundle manifest: %v", err)
	}
	manifest, err := release.Parse(manifestBytes)
	if err != nil || manifest.Version != want {
		t.Fatalf("bundle version = %+v, %v; want %s", manifest, err, want)
	}
}

func TestGitHubNodeBundleSyncCleansOnlyStaleStagingDirectories(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	stale, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := os.MkdirTemp(dataDir, nodeBundleTempPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-nodeBundleTempStaleAge - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{
		DataDir: dataDir, Key: priv.Public().(ed25519.PublicKey), APIURL: "https://example.org/releases",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
	})
	if _, err := source.Sync(context.Background(), 0); err == nil {
		t.Fatal("sync unexpectedly succeeded")
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale stage remains after sync start: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("recent stage was removed: %v", err)
	}
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

func TestGitHubNodeBundleMetadataHasShortBudgetAndAssetsUseCallerBudget(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("signed linux amd64 agent"),
		"mistgate-node-linux-arm64": []byte("signed linux arm64 agent"),
	}
	apiBody, assets := signedNodeRelease(t, priv, "v0.2.0", 1_900_000_000, 2_000_000_000, files)
	var metadataBudget, binaryBudget time.Duration
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			return nil, errors.New("bundle request has no deadline")
		}
		remaining := time.Until(deadline)
		name := filepath.Base(r.URL.Path)
		if r.URL.Host == "api.github.com" || name == release.ManifestName || name == release.SignatureName {
			metadataBudget = remaining
		} else if r.URL.Host == "github.com" {
			binaryBudget = remaining
		}
		if r.URL.Host == "api.github.com" {
			return githubResponse(http.StatusOK, apiBody), nil
		}
		if r.URL.Host == "github.com" {
			body, ok := assets[name]
			if !ok {
				return githubResponse(http.StatusNotFound, nil), nil
			}
			return githubResponse(http.StatusOK, body), nil
		}
		return nil, errors.New("unexpected host " + r.URL.Host)
	})}
	source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{
		DataDir: t.TempDir(), Key: priv.Public().(ed25519.PublicKey), HTTPClient: client,
		Now: func() time.Time { return time.Unix(1_900_000_000, 0) },
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if changed, err := source.Sync(ctx, 0); err != nil || !changed {
		t.Fatalf("bundle sync = %v, %v", changed, err)
	}
	if metadataBudget <= 0 || metadataBudget > nodeBundleMetadataTimeout || binaryBudget <= nodeBundleMetadataTimeout {
		t.Fatalf("request budgets: metadata=%s binary=%s; want metadata <= %s and binary > %s", metadataBudget, binaryBudget, nodeBundleMetadataTimeout, nodeBundleMetadataTimeout)
	}
}

func TestGitHubNodeBundleSourceFollowsChannelWithoutDowngrading(t *testing.T) {
	_, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"mistgate-node-linux-amd64": []byte("signed linux amd64 agent"),
		"mistgate-node-linux-arm64": []byte("signed linux arm64 agent"),
	}
	versions := []struct {
		tag   string
		built int64
	}{
		{"v0.2.0", 1_770_000_000},
		{"v0.2.1-testing.1", 1_780_000_000},
		{"v0.2.2-dev.1", 1_790_000_000},
	}
	var releases []githubRelease
	assetsByTag := make(map[string]map[string][]byte, len(versions))
	for _, version := range versions {
		body, assets := signedNodeRelease(t, priv, version.tag, version.built, 2_000_000_000, files)
		var rels []githubRelease
		if err := json.Unmarshal(body, &rels); err != nil || len(rels) != 1 {
			t.Fatalf("decode %s release: %v", version.tag, err)
		}
		releases = append(releases, rels[0])
		assetsByTag[version.tag] = assets
	}
	apiBody, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			return githubResponse(http.StatusOK, apiBody), nil
		}
		if r.URL.Host == "github.com" {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			tag, name := parts[len(parts)-2], filepath.Base(r.URL.Path)
			if data, ok := assetsByTag[tag][name]; ok {
				return githubResponse(http.StatusOK, data), nil
			}
			return githubResponse(http.StatusNotFound, nil), nil
		}
		return nil, errors.New("unexpected host " + r.URL.Host)
	})}
	channel := release.ChannelStable
	dataDir := t.TempDir()
	source := NewGitHubNodeBundleSource(GitHubNodeBundleConfig{
		DataDir: dataDir, Key: priv.Public().(ed25519.PublicKey), Channel: func() release.Channel { return channel }, HTTPClient: client,
		Now: func() time.Time { return time.Unix(1_900_000_000, 0) },
	})
	currentBuilt := int64(0)
	for _, tc := range []struct {
		channel release.Channel
		version string
		built   int64
	}{
		{release.ChannelStable, versions[0].tag, versions[0].built},
		{release.ChannelTesting, versions[1].tag, versions[1].built},
		{release.ChannelDev, versions[2].tag, versions[2].built},
	} {
		channel = tc.channel
		changed, err := source.Sync(context.Background(), currentBuilt)
		if err != nil || !changed {
			t.Fatalf("sync %s = %v, %v", tc.channel, changed, err)
		}
		manifestBytes, err := os.ReadFile(filepath.Join(dataDir, "dist", release.ManifestName))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := release.Parse(manifestBytes)
		if err != nil || manifest.Version != tc.version {
			t.Fatalf("channel %s installed %+v, err %v; want %s", tc.channel, manifest, err, tc.version)
		}
		currentBuilt = tc.built
	}
	channel = release.ChannelStable
	changed, err := source.Sync(context.Background(), currentBuilt)
	if err != nil || changed {
		t.Fatalf("switching back to stable downgraded the bundle: changed=%v err=%v", changed, err)
	}
}

func TestSameReleaseFilesIgnoresManifestOrder(t *testing.T) {
	first := release.File{Name: "mistgate-node-linux-amd64"}
	second := release.File{Name: "mistgate-node-linux-arm64"}
	if !sameReleaseFiles([]release.File{first, second}, []release.File{second, first}) {
		t.Fatal("the same signed files in a different manifest order must match")
	}
	if sameReleaseFiles([]release.File{first, second}, []release.File{first, {Name: "mistgate-node-linux-arm64", Size: 1}}) {
		t.Fatal("files with different signed metadata must not match")
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
	firstAPI, firstAssets := signedNodeRelease(t, priv, "v0.2.0", 1_800_000_100, 1_950_000_000, files)
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
	var releases []githubRelease
	if err := json.Unmarshal(apiBody, &releases); err != nil || len(releases) != 1 {
		t.Fatal(err)
	}
	r := releases[0]
	for i := range r.Assets {
		if r.Assets[i].Name == release.SignatureName {
			r.Assets = append(r.Assets[:i], r.Assets[i+1:]...)
			break
		}
	}
	apiBody, _ = json.Marshal([]githubRelease{r})
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
