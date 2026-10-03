package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const (
	githubLatestReleaseURL     = "https://api.github.com/repos/Mistgate/mistgate/releases/latest"
	githubReleasePageBase      = "https://github.com/Mistgate/mistgate/releases/tag/"
	panelReleasePrefix         = "https://github.com/Mistgate/mistgate/releases/download/"
	panelUpdateInterval        = 6 * time.Hour
	panelUpdateCheckTimeout    = 10 * time.Second
	panelUpdateMaxSize         = 128 << 20
	panelUpdateApplyTimeout    = 30 * time.Minute
	panelUpdateGuardTimeout    = panelUpdateApplyTimeout + 5*time.Minute
	panelUpdateApplyTimeoutArg = "30min"
	panelUpdateHelperService   = "mistgate-panel-update.service"
)

var (
	ErrNoPanelRelease    = errors.New("no stable panel release is published")
	ErrNoPanelUpdate     = errors.New("the panel is already up to date")
	ErrPanelUnsupported  = errors.New("panel self-update requires a supported systemd update path")
	ErrPanelAssetMissing = errors.New("the latest release has no binary for this architecture")
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)

// PanelUpdateStatus is the release information exposed on the Updates screen. ErrorKey is a stable UI key, not
// an upstream error message.
type PanelUpdateStatus struct {
	Version       string
	URL           string
	PublishedUnix int64
	CheckedUnix   int64
	Available     bool
	Supported     bool
	Installable   bool
	Installing    bool
	ErrorKey      string
}

// PanelUpdater is the GitHub release checker and the privileged installer for the panel binary.
type PanelUpdater interface {
	Status() PanelUpdateStatus
	Check(context.Context) PanelUpdateStatus
	Install(context.Context) error
	Run(context.Context)
}

type PanelUpdateConfig struct {
	CurrentVersion string
	CurrentBuilt   int64
	DataDir        string
	ServiceUnit    string
	Executable     string
	// Enabled is set only for a supported Linux systemd installation. Tests may set it explicitly.
	Enabled bool
	// UseRootHelperService makes Install request the fixed, root-owned helper unit. It is used by non-root panel
	// services; the helper independently fetches and verifies the release before replacing the panel.
	UseRootHelperService bool
	// APIURL, HTTPClient, Runner, OS, Arch and Now are injectable test seams. Production uses the fixed Mistgate
	// endpoint, the default GitHub client and the host values.
	APIURL     string
	HTTPClient *http.Client
	Runner     func(context.Context, string, ...string) error
	OS         string
	Arch       string
	Now        func() time.Time
	Log        *slog.Logger
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	PublishedAt time.Time     `json:"published_at"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

type GitHubPanelUpdater struct {
	cfg       PanelUpdateConfig
	apiURL    string
	client    *http.Client
	runner    func(context.Context, string, ...string) error
	os        string
	arch      string
	now       func() time.Time
	log       *slog.Logger
	supported bool

	checkMu sync.Mutex
	mu      sync.RWMutex
	status  PanelUpdateStatus
	install sync.Mutex
}

// PanelUpdateHostSupported reports whether this installation has the required systemd update path.
func PanelUpdateHostSupported() bool { return panelUpdateHostSupported() }

// PanelUpdateUsesRootHelperService reports whether a non-root panel should request the fixed privileged helper unit.
func PanelUpdateUsesRootHelperService() bool { return panelUpdateUsesRootHelperService() }

// NewGitHubPanelUpdater creates a fail-closed checker. An unsupported installation can still read release metadata,
// but the RPC never schedules a binary replacement for it.
func NewGitHubPanelUpdater(cfg PanelUpdateConfig) *GitHubPanelUpdater {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.APIURL == "" {
		cfg.APIURL = githubLatestReleaseURL
	}
	if cfg.Executable == "" {
		cfg.Executable, _ = os.Executable()
	}
	if cfg.DataDir != "" {
		cfg.DataDir, _ = filepath.Abs(cfg.DataDir)
	}
	if cfg.Executable != "" {
		cfg.Executable, _ = filepath.Abs(cfg.Executable)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout:       2 * time.Minute,
			CheckRedirect: githubReleaseRedirect,
		}
	}
	if cfg.Runner == nil {
		cfg.Runner = runCommand
	}
	serviceUnit := strings.TrimSpace(cfg.ServiceUnit)
	executable, _ := filepath.Abs(cfg.Executable)
	dataDir, _ := filepath.Abs(cfg.DataDir)
	supported := cfg.Enabled && cfg.OS == "linux" && cfg.DataDir != "" && dataDir != "." && dataDir != string(filepath.Separator) && serviceUnit != "" && unitPattern.MatchString(serviceUnit) &&
		cfg.Executable != "" && executable != "." && filepath.IsAbs(executable)
	status := PanelUpdateStatus{Supported: supported}
	if !supported {
		status.ErrorKey = "unsupported"
	}
	return &GitHubPanelUpdater{
		cfg: cfg, apiURL: cfg.APIURL, client: cfg.HTTPClient, runner: cfg.Runner,
		os: cfg.OS, arch: cfg.Arch, now: cfg.Now, log: cfg.Log, supported: supported,
		status: status,
	}
}

func githubReleaseRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Port() != "" && req.URL.Port() != "443") {
		return errors.New("github release redirect must use HTTPS on the default port")
	}
	host := strings.ToLower(req.URL.Hostname())
	switch host {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
	default:
		return fmt.Errorf("github release redirect to untrusted host %q", host)
	}
	if len(via) >= 5 {
		return errors.New("too many github release redirects")
	}
	return nil
}

func (u *GitHubPanelUpdater) Status() PanelUpdateStatus {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status
}

// Check fetches the latest stable release. The endpoint is fixed to the Mistgate repository in production; every
// version, URL, asset name and digest is validated before any of it can reach the installer.
func (u *GitHubPanelUpdater) Check(ctx context.Context) PanelUpdateStatus {
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	checkCtx, cancel := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	defer cancel()

	status, _, err := u.fetchStatus(checkCtx)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoPanelRelease):
			status.ErrorKey = "no_release"
		case errors.Is(err, ErrPanelAssetMissing):
			status.ErrorKey = "asset_missing"
		default:
			status.ErrorKey = "check_failed"
			u.log.Warn("check GitHub panel release", "err", err)
		}
	}
	status.CheckedUnix = u.now().Unix()
	status.Supported = u.supported
	status.Installable = status.Available && status.Supported && err == nil
	u.mu.Lock()
	status.Installing = u.status.Installing
	u.status = status
	u.mu.Unlock()
	return status
}

func (u *GitHubPanelUpdater) Run(ctx context.Context) {
	if !u.supported {
		return
	}
	_ = u.Check(ctx)
	tick := time.NewTicker(panelUpdateInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = u.Check(ctx)
		}
	}
}

// Install re-checks GitHub immediately before download, verifies the API-provided SHA-256 and ELF architecture,
// then asks systemd to run the built-in helper outside the panel service's filesystem sandbox.
func (u *GitHubPanelUpdater) Install(ctx context.Context) error {
	if !u.supported {
		return ErrPanelUnsupported
	}
	if !u.install.TryLock() {
		return errors.New("a panel update is already being installed")
	}
	defer u.install.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	status, asset, err := u.fetchStatus(checkCtx)
	cancel()
	if err != nil {
		status.CheckedUnix = u.now().Unix()
		status.Supported = u.supported
		status.ErrorKey = panelCheckErrorKey(err)
		u.setStatus(status, false, "")
		if errors.Is(err, ErrNoPanelRelease) || errors.Is(err, ErrPanelAssetMissing) {
			return err
		}
		return fmt.Errorf("check latest panel release: %w", err)
	}
	status.CheckedUnix = u.now().Unix()
	status.Supported = u.supported
	status.Installable = status.Available && status.Supported
	if !status.Available {
		u.setStatus(status, false, "")
		return ErrNoPanelUpdate
	}
	if !status.Installable {
		u.setStatus(status, false, "")
		return ErrPanelAssetMissing
	}
	if !unitPattern.MatchString(strings.TrimSpace(u.cfg.ServiceUnit)) {
		return ErrPanelUnsupported
	}

	u.setStatus(status, true, "")
	accepted := false
	defer func() {
		if !accepted {
			u.mu.Lock()
			u.status.Installing = false
			u.mu.Unlock()
		}
	}()

	runCtx, cancelRun := context.WithTimeout(context.Background(), 15*time.Second)
	if u.cfg.UseRootHelperService {
		// The unit is root-owned and accepts no caller-supplied paths or checksums. It downloads the official asset
		// itself so a process running as the panel user cannot substitute a binary or digest between verification and
		// installation.
		err = u.runner(runCtx, "systemctl", "start", "--no-block", panelUpdateHelperService)
	} else {
		stagePath, stageErr := u.downloadAndStage(ctx, asset, status.Version)
		if stageErr != nil {
			cancelRun()
			u.setStatus(status, false, "download_failed")
			return stageErr
		}
		executable, pathErr := filepath.Abs(u.cfg.Executable)
		if pathErr != nil || executable == "." {
			_ = os.Remove(stagePath)
			cancelRun()
			return ErrPanelUnsupported
		}
		unit := fmt.Sprintf("mistgate-panel-update-%d.service", u.now().UnixNano())
		args := []string{
			"--unit=" + unit, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=" + panelUpdateApplyTimeoutArg, "--",
			executable, "panel-update-helper", "--data-dir", u.cfg.DataDir, "--service", strings.TrimSpace(u.cfg.ServiceUnit),
			"--sha256", strings.TrimPrefix(asset.Digest, "sha256:"),
		}
		err = u.runner(runCtx, "systemd-run", args...)
		if err != nil {
			_ = os.Remove(stagePath)
		}
	}
	cancelRun()
	if err != nil {
		u.setStatus(status, false, "schedule_failed")
		return fmt.Errorf("schedule panel update: %w", err)
	}
	accepted = true
	time.AfterFunc(panelUpdateGuardTimeout, func() {
		u.mu.Lock()
		u.status.Installing = false
		u.mu.Unlock()
	})
	return nil
}

func panelCheckErrorKey(err error) string {
	switch {
	case errors.Is(err, ErrNoPanelRelease):
		return "no_release"
	case errors.Is(err, ErrPanelAssetMissing):
		return "asset_missing"
	default:
		return "check_failed"
	}
}

func (u *GitHubPanelUpdater) setStatus(status PanelUpdateStatus, installing bool, errorKey string) {
	status.Supported = u.supported
	status.Installable = status.Available && status.Supported && errorKey == ""
	status.Installing = installing
	if errorKey != "" {
		status.ErrorKey = errorKey
	}
	u.mu.Lock()
	u.status = status
	u.mu.Unlock()
}

func (u *GitHubPanelUpdater) fetchStatus(ctx context.Context) (PanelUpdateStatus, githubAsset, error) {
	status := PanelUpdateStatus{Supported: u.supported}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL, nil)
	if err != nil {
		return status, githubAsset{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Mistgate-panel-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return status, githubAsset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return status, githubAsset{}, ErrNoPanelRelease
	}
	if resp.StatusCode != http.StatusOK {
		return status, githubAsset{}, fmt.Errorf("GitHub releases API returned HTTP %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release); err != nil {
		return status, githubAsset{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	if release.Draft || release.Prerelease || !validReleaseTag(release.TagName) || !validReleasePage(release.HTMLURL, release.TagName) {
		return status, githubAsset{}, errors.New("GitHub returned an invalid stable panel release")
	}
	status.Version = release.TagName
	status.URL = release.HTMLURL
	if !release.PublishedAt.IsZero() {
		status.PublishedUnix = release.PublishedAt.Unix()
	}
	status.Available = newerRelease(release.TagName, u.cfg.CurrentVersion, status.PublishedUnix, u.cfg.CurrentBuilt)
	if !status.Available {
		return status, githubAsset{}, nil
	}
	assetName := "mistgate-linux-" + u.arch
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			if err := validateReleaseAsset(asset, release.TagName); err != nil {
				return status, githubAsset{}, err
			}
			return status, asset, nil
		}
	}
	return status, githubAsset{}, ErrPanelAssetMissing
}

func validReleaseTag(tag string) bool {
	return semver.IsValid(tag) && semver.Canonical(tag) != "" && semver.Prerelease(tag) == "" && semver.Build(tag) == ""
}

func validReleasePage(rawURL, tag string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && rawURL == githubReleasePageBase+url.PathEscape(tag)
}

func validateReleaseAsset(asset githubAsset, tag string) error {
	if asset.Size <= 0 || asset.Size > panelUpdateMaxSize {
		return errors.New("GitHub panel asset has an invalid size")
	}
	if !strings.HasPrefix(asset.Digest, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(asset.Digest, "sha256:")) {
		return errors.New("GitHub panel asset has no valid SHA-256 digest")
	}
	u, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.EscapedPath(), "/Mistgate/mistgate/releases/download/") {
		return errors.New("GitHub panel asset URL is not an official Mistgate release URL")
	}
	expectedPath := "/Mistgate/mistgate/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(asset.Name)
	if u.EscapedPath() != expectedPath {
		return errors.New("GitHub panel asset URL does not match its release tag and asset name")
	}
	return nil
}

func newerRelease(latest, current string, publishedUnix, currentBuilt int64) bool {
	canonical := func(v string) string {
		if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		return semver.Canonical(v)
	}
	latestVersion := canonical(latest)
	currentVersion := canonical(current)
	if latestVersion == "" {
		return false
	}
	if currentVersion != "" {
		return semver.Compare(latestVersion, currentVersion) > 0
	}
	return currentBuilt <= 0 || publishedUnix > currentBuilt
}

func (u *GitHubPanelUpdater) downloadAndStage(ctx context.Context, asset githubAsset, tag string) (string, error) {
	if err := validateReleaseAsset(asset, tag); err != nil {
		return "", err
	}
	dataDir, err := filepath.Abs(u.cfg.DataDir)
	if err != nil || dataDir == "." {
		return "", ErrPanelUnsupported
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create panel data directory: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mistgate-panel-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download panel release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download panel release returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > panelUpdateMaxSize {
		return "", errors.New("panel release download exceeds the size limit")
	}

	tmp, err := os.CreateTemp(dataDir, ".panel-update-*")
	if err != nil {
		return "", fmt.Errorf("stage panel release: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, panelUpdateMaxSize+1))
	if copyErr != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write panel release: %w", copyErr)
	}
	if n > panelUpdateMaxSize || n != asset.Size {
		_ = tmp.Close()
		return "", fmt.Errorf("panel release size mismatch: got %d, expected %d", n, asset.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.TrimPrefix(asset.Digest, "sha256:") {
		_ = tmp.Close()
		return "", errors.New("panel release SHA-256 does not match GitHub metadata")
	}
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := verifyPanelELF(tmpName, u.arch); err != nil {
		return "", err
	}
	stagePath := filepath.Join(dataDir, "panel-update.new")
	if err := os.Remove(stagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(tmpName, stagePath); err != nil {
		return "", fmt.Errorf("finish staging panel release: %w", err)
	}
	return stagePath, nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
