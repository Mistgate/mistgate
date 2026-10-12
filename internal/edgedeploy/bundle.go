package edgedeploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

const githubDownloadBase = "https://github.com/Mistgate/mistgate/releases/download"

type bundle struct {
	manifest *release.EdgeManifest
	config   release.EdgeConfig
	files    map[string][]byte
}

func readBundle(ctx context.Context, opts Options, httpClient *http.Client, now time.Time) (*bundle, error) {
	if opts.InsecureSkipSignature && opts.BundleDir == "" {
		return nil, errors.New("--insecure-skip-signature is only available with --bundle DIR")
	}
	var manifestBytes, signature, tarball []byte
	if opts.BundleDir != "" {
		var err error
		manifestBytes, err = readLimitedFile(filepath.Join(opts.BundleDir, release.EdgeManifestName), release.MaxManifestBytes)
		if err != nil {
			return nil, fmt.Errorf("read edge manifest: %w", err)
		}
		signature, err = readLimitedFile(filepath.Join(opts.BundleDir, release.EdgeSignatureName), ed25519.SignatureSize)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read edge signature: %w", err)
		}
		tarball, err = readLimitedFile(filepath.Join(opts.BundleDir, release.EdgeBundleName), release.MaxEdgeBundle)
		if err != nil {
			return nil, fmt.Errorf("read edge bundle: %w", err)
		}
	} else {
		var err error
		manifestBytes, signature, tarball, err = downloadBundle(ctx, opts.Version, httpClient, opts.GitHubDownloadBaseURL)
		if err != nil {
			return nil, err
		}
	}

	if len(manifestBytes) == 0 {
		return nil, errors.New("edge bundle has no edge-manifest.json")
	}
	// A signature that is there is always verified when the binary has a release key, even with --insecure-skip-signature.
	var manifest *release.EdgeManifest
	var err error
	switch {
	case len(signature) != 0 && len(opts.PublicKey) == ed25519.PublicKeySize:
		manifest, err = release.VerifyEdge(opts.PublicKey, manifestBytes, signature)
		if err != nil {
			return nil, fmt.Errorf("verify edge manifest: %w", err)
		}
	case opts.InsecureSkipSignature:
		manifest, err = release.ParseEdge(manifestBytes)
		if err != nil {
			return nil, fmt.Errorf("parse edge manifest: %w", err)
		}
	case len(signature) == 0:
		return nil, errors.New("edge bundle has no edge-manifest.sig; use --insecure-skip-signature only for a development bundle")
	default:
		return nil, errors.New("this binary has no valid compiled-in release key; use a signed release binary or an unsigned development bundle with --insecure-skip-signature")
	}
	if err := manifest.Check(now); err != nil {
		return nil, fmt.Errorf("edge bundle: %w", err)
	}
	if opts.Version != "" && manifest.Version != opts.Version {
		return nil, fmt.Errorf("edge bundle version %s does not match requested version %s", manifest.Version, opts.Version)
	}
	if err := manifest.VerifyFile(bytes.NewReader(tarball)); err != nil {
		return nil, fmt.Errorf("verify edge bundle file: %w", err)
	}

	files, err := release.ReadEdgeBundle(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("read edge bundle: %w", err)
	}
	configBytes, ok := files[release.EdgeConfigName]
	if !ok {
		return nil, errors.New("edge bundle is missing edge.json")
	}
	var config release.EdgeConfig
	dec := json.NewDecoder(bytes.NewReader(configBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode edge.json: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode edge.json: trailing data")
	}
	if config.Schema != release.EdgeSchema || config.Version != manifest.Version {
		return nil, fmt.Errorf("edge.json schema or version does not match the signed manifest")
	}
	if !reflect.DeepEqual(config, release.NewEdgeConfig(manifest.Version)) {
		return nil, errors.New("edge.json bindings, migrations, or script modules do not match the release contract")
	}
	for _, name := range []string{"worker.js", "provision.js", "panel.wasm"} {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("edge bundle is missing %s", name)
		}
	}
	for name := range files {
		if name != release.EdgeConfigName && name != "worker.js" && name != "provision.js" && name != "panel.wasm" && !strings.HasPrefix(name, "assets/") {
			return nil, fmt.Errorf("edge bundle contains unexpected file %s", name)
		}
	}
	return &bundle{manifest: manifest, config: config, files: files}, nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", filepath.Base(path), limit)
	}
	return b, nil
}

func downloadBundle(ctx context.Context, version string, client *http.Client, downloadBase string) ([]byte, []byte, []byte, error) {
	if version == "" {
		return nil, nil, nil, errors.New("no edge release version to download")
	}
	if client == nil {
		client = newHTTPClient()
	}
	if downloadBase == "" {
		downloadBase = githubDownloadBase
	}
	base := strings.TrimRight(downloadBase, "/") + "/" + url.PathEscape(version) + "/"
	manifest, err := githubGet(ctx, client, base+release.EdgeManifestName, release.MaxManifestBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("download edge manifest: %w", err)
	}
	signature, err := githubGet(ctx, client, base+release.EdgeSignatureName, ed25519.SignatureSize)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("download edge signature: %w", err)
	}
	tarball, err := githubGet(ctx, client, base+release.EdgeBundleName, release.MaxEdgeBundle)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("download edge bundle: %w", err)
	}
	return manifest, signature, tarball, nil
}

func githubGet(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "mistgate-edge-deploy")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub release request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub release request returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("GitHub release asset exceeds the size limit")
	}
	return b, nil
}
