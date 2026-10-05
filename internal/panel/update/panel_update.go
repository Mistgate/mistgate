package update

import (
	"context"
	"crypto/ed25519"
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

	"github.com/mistgate/mistgate/internal/release"
)

const (
	githubLatestReleaseURL     = "https://api.github.com/repos/Mistgate/mistgate/releases/latest"
	githubReleasePageBase      = "https://github.com/Mistgate/mistgate/releases/tag/"
	panelReleasePrefix         = "https://github.com/Mistgate/mistgate/releases/download/"
	panelUpdateInterval        = 6 * time.Hour
	panelUpdateCheckTimeout    = 10 * time.Second
	panelUpdateCheckCache      = 3 * time.Minute // CheckPanelUpdate answers from the last lookup this long
	panelUpdateMaxSize         = 128 << 20
	panelUpdateApplyTimeout    = 30 * time.Minute
	panelUpdateGuardTimeout    = panelUpdateApplyTimeout + 5*time.Minute
	panelUpdateApplyTimeoutArg = "30min"
	panelUpdateHelperService   = "mistgate-panel-update.service"
	panelUpdateRequestName     = "panel-update.request"
)

var (
	ErrNoPanelRelease    = errors.New("no stable panel release is published")
	ErrNoPanelUpdate     = errors.New("the panel is already up to date")
	ErrPanelUnsupported  = errors.New("panel self-update requires a supported systemd update path")
	ErrPanelAssetMissing = errors.New("the latest release has no binary for this architecture")
	ErrPanelUnsigned     = errors.New("the latest release has no panel manifest signed with this panel's release key")
	ErrPanelExpired      = errors.New("the signed panel manifest of the latest release has expired")
	ErrPanelNoKey        = errors.New("this panel build has no release key to verify panel releases")
	ErrPanelChanged      = errors.New("the latest signed panel release is not the one that was confirmed")
)

// panelWatchInterval is how often an accepted update looks at its helper unit (a test seam).
var panelWatchInterval = 5 * time.Second

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)

// PanelUpdateStatus is the release information exposed on the Updates screen. ErrorKey is a stable UI key, not
// an upstream error message. Built and SHA256 come from the release's signed panel manifest: the build time orders
// releases, and the digest of this architecture's binary is what an install must name back.
type PanelUpdateStatus struct {
	Version       string
	URL           string
	PublishedUnix int64
	CheckedUnix   int64
	Built         int64
	SHA256        string
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
	// Install installs the latest signed release, only when it is the version and digest the owner confirmed.
	Install(ctx context.Context, version, sha256 string) error
	Run(context.Context)
}

type PanelUpdateConfig struct {
	CurrentBuilt int64
	DataDir      string
	ServiceUnit  string
	Executable   string
	// Key is the release public key compiled into this binary: the only key a panel release is accepted under (a
	// release.pub in the data directory is writable by the panel's service user, so it never qualifies). Nil = no
	// self-update.
	Key ed25519.PublicKey
	// Enabled is set only for a supported Linux systemd installation. Tests may set it explicitly.
	Enabled bool
	// UseRootHelperService makes Install request the fixed, root-owned helper unit. It is used by non-root panel
	// services; the helper independently fetches and verifies the release before replacing the panel.
	UseRootHelperService bool
	// APIURL, HTTPClient, Runner, OS, Arch and Now are injectable test seams. Production uses the fixed Mistgate
	// endpoint, the default GitHub client and the host values. Runner returns the command's standard output.
	APIURL     string
	HTTPClient *http.Client
	Runner     func(context.Context, string, ...string) ([]byte, error)
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

// panelRelease is what fetchStatus verified: the release tag, this architecture's asset and its signed entry.
type panelRelease struct {
	tag   string
	asset githubAsset
	file  release.File
}

type GitHubPanelUpdater struct {
	cfg       PanelUpdateConfig
	apiURL    string
	client    *http.Client
	runner    func(context.Context, string, ...string) ([]byte, error)
	os        string
	arch      string
	now       func() time.Time
	log       *slog.Logger
	supported bool

	watchEvery time.Duration
	checkMu    sync.Mutex
	mu         sync.RWMutex
	status     PanelUpdateStatus
	seq        uint64 // accepted installs, so a stale watcher cannot end a newer one
	install    sync.Mutex
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
		status: status, watchEvery: panelWatchInterval,
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

// Check fetches the latest stable release and its signed panel manifest. A lookup made in the last few minutes is
// answered from memory: the button costs nothing, and the unauthenticated GitHub quota it shares with the node bundle
// sync is not spent on clicks.
func (u *GitHubPanelUpdater) Check(ctx context.Context) PanelUpdateStatus {
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	if s := u.Status(); s.CheckedUnix > 0 && u.now().Sub(time.Unix(s.CheckedUnix, 0)) < panelUpdateCheckCache {
		return s
	}
	checkCtx, cancel := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	defer cancel()

	status, _, err := u.fetchStatus(checkCtx)
	if err != nil {
		status.ErrorKey = panelCheckErrorKey(err)
		if status.ErrorKey == "check_failed" || status.ErrorKey == "unsigned" {
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

// Install re-checks GitHub, requires the latest signed release to be exactly the version and digest the owner
// confirmed, records them for the helper and asks systemd to run the built-in helper outside the panel service's
// filesystem sandbox. The helper fetches and verifies the release again on its own before it replaces anything.
func (u *GitHubPanelUpdater) Install(ctx context.Context, version, digest string) error {
	if !u.supported {
		return ErrPanelUnsupported
	}
	if !u.install.TryLock() {
		return errors.New("a panel update is already being installed")
	}
	defer u.install.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	status, _, err := u.fetchStatus(checkCtx)
	cancel()
	status.CheckedUnix = u.now().Unix()
	if err != nil {
		status.ErrorKey = panelCheckErrorKey(err)
		u.setStatus(status, false, "")
		if status.ErrorKey != "check_failed" {
			return err
		}
		return fmt.Errorf("check latest panel release: %w", err)
	}
	if !status.Available {
		u.setStatus(status, false, "")
		return ErrNoPanelUpdate
	}
	if version != status.Version || digest != status.SHA256 {
		u.setStatus(status, false, "")
		return ErrPanelChanged
	}
	if !unitPattern.MatchString(strings.TrimSpace(u.cfg.ServiceUnit)) {
		return ErrPanelUnsupported
	}
	if u.cfg.UseRootHelperService {
		if err := u.rootHelperLayoutError(); err != nil {
			u.setStatus(status, false, "")
			return err
		}
	}
	if err := writePanelUpdateRequest(u.cfg.DataDir, panelUpdateRequest{Version: status.Version, SHA256: status.SHA256}); err != nil {
		u.setStatus(status, false, "schedule_failed")
		return fmt.Errorf("record panel update request: %w", err)
	}

	u.setStatus(status, true, "")
	runCtx, cancelRun := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRun()
	unit, started := panelUpdateHelperService, ""
	if u.cfg.UseRootHelperService {
		// The unit is root-owned and accepts no caller-supplied paths: the helper reads the confirmed version and
		// digest from the request file and still installs only what the signed manifest of the release names.
		started = u.unitProps(runCtx, unit)["InactiveExitTimestampMonotonic"]
		_, err = u.runner(runCtx, "systemctl", "start", "--no-block", unit)
	} else {
		unit = fmt.Sprintf("mistgate-panel-update-%d.service", u.now().UnixNano())
		args := []string{
			"--unit=" + unit, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=" + panelUpdateApplyTimeoutArg, "--",
			u.cfg.Executable, "panel-update-helper", "--fetch-latest", "--data-dir", u.cfg.DataDir, "--service", strings.TrimSpace(u.cfg.ServiceUnit),
		}
		_, err = u.runner(runCtx, "systemd-run", args...)
	}
	if err != nil {
		_ = os.Remove(filepath.Join(u.cfg.DataDir, panelUpdateRequestName))
		u.setStatus(status, false, "schedule_failed")
		return fmt.Errorf("schedule panel update: %w", err)
	}
	u.mu.Lock()
	u.seq++
	seq := u.seq
	u.mu.Unlock()
	go u.watchHelper(unit, started, seq)
	return nil
}

// The layout the fixed root unit (deploy/systemd/mistgate-panel-update.service) updates: its ExecStart names these
// paths, so a non-root panel elsewhere would hand it a request it never reads. Variables only so tests can point them
// at a temporary directory.
var (
	rootHelperExecutable = "/usr/local/bin/mistgate"
	rootHelperDataDir    = "/var/lib/mistgate"
	rootHelperService    = "mistgate.service"
)

// rootHelperLayoutError says, naming the expected paths, why the fixed root unit cannot update this panel; nil when it can.
func (u *GitHubPanelUpdater) rootHelperLayoutError() error {
	exe, data, service := filepath.Clean(u.cfg.Executable), filepath.Clean(u.cfg.DataDir), strings.TrimSpace(u.cfg.ServiceUnit)
	if exe == filepath.Clean(rootHelperExecutable) && data == filepath.Clean(rootHelperDataDir) && service == rootHelperService {
		return nil
	}
	return fmt.Errorf("%w: %s updates only %s with the data directory %s and the service %s, but this panel runs %s with %s and %s; "+
		"move the panel to that layout or run it as root, or update it by hand", ErrPanelUnsupported,
		panelUpdateHelperService, rootHelperExecutable, rootHelperDataDir, rootHelperService, exe, data, service)
}

// watchHelper ends the "installing" state when the helper unit finished without restarting this process: a helper
// that succeeds stops the panel, so a helper that is done while the panel still runs has failed (or found nothing to
// do). It gives up after the helper's own timeout.
func (u *GitHubPanelUpdater) watchHelper(unit, startedBefore string, seq uint64) {
	deadline := time.Now().Add(panelUpdateGuardTimeout)
	result := "install_failed"
	for {
		if time.Now().After(deadline) {
			result = ""
			break
		}
		time.Sleep(u.watchEvery)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p := u.unitProps(ctx, unit)
		cancel()
		if p == nil {
			continue
		}
		ran := p["InactiveExitTimestampMonotonic"] != startedBefore && p["InactiveExitTimestampMonotonic"] != "0"
		if p["LoadState"] == "not-found" || (ran && (p["ActiveState"] == "inactive" || p["ActiveState"] == "failed")) {
			u.log.Warn("the panel update helper finished but the panel was not replaced", "unit", unit, "state", p["ActiveState"])
			break
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seq == seq && u.status.Installing {
		u.status.Installing = false
		if result != "" {
			u.status.ErrorKey = result
		}
	}
}

// unitProps reads a few properties of a systemd unit; nil when systemctl fails.
func (u *GitHubPanelUpdater) unitProps(ctx context.Context, unit string) map[string]string {
	out, err := u.runner(ctx, "systemctl", "show", "--property=LoadState,ActiveState,InactiveExitTimestampMonotonic", unit)
	if err != nil {
		return nil
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

func panelCheckErrorKey(err error) string {
	switch {
	case errors.Is(err, ErrNoPanelRelease):
		return "no_release"
	case errors.Is(err, ErrPanelAssetMissing):
		return "asset_missing"
	case errors.Is(err, ErrPanelUnsigned):
		return "unsigned"
	case errors.Is(err, ErrPanelExpired):
		return "expired"
	case errors.Is(err, ErrPanelNoKey):
		return "no_key"
	default:
		return "check_failed"
	}
}

func (u *GitHubPanelUpdater) setStatus(status PanelUpdateStatus, installing bool, errorKey string) {
	status.Supported = u.supported
	status.Installable = status.Available && status.Supported && errorKey == "" && status.ErrorKey == ""
	status.Installing = installing
	if errorKey != "" {
		status.ErrorKey = errorKey
	}
	u.mu.Lock()
	u.status = status
	u.mu.Unlock()
}

// fetchStatus reads the latest stable release and verifies its signed panel manifest with the compiled-in release
// key. A release is offered only when that manifest is valid, unexpired and newer by build time than this binary:
// the version string orders nothing (a git-describe build of a later commit is newer than its tag).
func (u *GitHubPanelUpdater) fetchStatus(ctx context.Context) (PanelUpdateStatus, panelRelease, error) {
	status := PanelUpdateStatus{Supported: u.supported}
	r, err := fetchLatestRelease(ctx, u.client, u.apiURL, "Mistgate-panel-updater")
	if err != nil {
		return status, panelRelease{}, err
	}
	status.Version = r.TagName
	status.URL = r.HTMLURL
	if !r.PublishedAt.IsZero() {
		status.PublishedUnix = r.PublishedAt.Unix()
	}
	if len(u.cfg.Key) != ed25519.PublicKeySize {
		return status, panelRelease{}, ErrPanelNoKey
	}
	assets := make(map[string]githubAsset, len(r.Assets))
	for _, asset := range r.Assets {
		if _, dup := assets[asset.Name]; dup {
			return status, panelRelease{}, fmt.Errorf("GitHub release contains duplicate asset %q", asset.Name)
		}
		assets[asset.Name] = asset
	}
	manifestAsset, hasManifest := assets[release.PanelManifestName]
	signatureAsset, hasSignature := assets[release.PanelSignatureName]
	if !hasManifest || !hasSignature {
		return status, panelRelease{}, ErrPanelUnsigned
	}
	body, err := downloadGitHubAsset(ctx, u.client, r.TagName, manifestAsset, release.MaxManifestBytes)
	if err != nil {
		return status, panelRelease{}, fmt.Errorf("download panel manifest: %w", err)
	}
	sig, err := downloadGitHubAsset(ctx, u.client, r.TagName, signatureAsset, ed25519.SignatureSize)
	if err != nil {
		return status, panelRelease{}, fmt.Errorf("download panel manifest signature: %w", err)
	}
	m, err := release.VerifyPanel(u.cfg.Key, body, sig)
	if err != nil {
		return status, panelRelease{}, fmt.Errorf("%w: %v", ErrPanelUnsigned, err)
	}
	if m.Version != r.TagName {
		return status, panelRelease{}, fmt.Errorf("%w: it names version %q", ErrPanelUnsigned, m.Version)
	}
	status.Built = m.Built
	file, err := m.FileFor("linux", u.arch)
	if err != nil {
		return status, panelRelease{}, ErrPanelAssetMissing
	}
	status.SHA256 = file.SHA256
	switch err := m.Check(u.now(), u.cfg.CurrentBuilt); {
	case errors.Is(err, release.ErrExpired):
		return status, panelRelease{}, ErrPanelExpired
	case err != nil: // as old as this build or older
		return status, panelRelease{}, nil
	}
	status.Available = true
	asset, ok := assets[file.Name]
	if !ok {
		return status, panelRelease{}, ErrPanelAssetMissing
	}
	if asset.Size != file.Size {
		return status, panelRelease{}, fmt.Errorf("GitHub panel asset %q size %d does not match its signed size %d", file.Name, asset.Size, file.Size)
	}
	if err := validateGitHubBundleAsset(asset, r.TagName, panelUpdateMaxSize); err != nil {
		return status, panelRelease{}, err
	}
	return status, panelRelease{tag: r.TagName, asset: asset, file: file}, nil
}

func validReleaseTag(tag string) bool {
	return semver.IsValid(tag) && semver.Canonical(tag) != "" && semver.Prerelease(tag) == "" && semver.Build(tag) == ""
}

func validReleasePage(rawURL, tag string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && rawURL == githubReleasePageBase+url.PathEscape(tag)
}

// downloadAndStage downloads the release's binary for this architecture into <data-dir>/panel-update.new, accepting
// only the size and SHA-256 of its signed manifest entry.
func (u *GitHubPanelUpdater) downloadAndStage(ctx context.Context, rel panelRelease) (string, error) {
	if err := validateGitHubBundleAsset(rel.asset, rel.tag, panelUpdateMaxSize); err != nil {
		return "", err
	}
	dataDir, err := filepath.Abs(u.cfg.DataDir)
	if err != nil || dataDir == "." {
		return "", ErrPanelUnsupported
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create panel data directory: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.asset.BrowserDownloadURL, nil)
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
	if resp.ContentLength > rel.file.Size {
		return "", errors.New("panel release download exceeds its signed size")
	}

	tmp, err := os.CreateTemp(dataDir, ".panel-update-*")
	if err != nil {
		return "", fmt.Errorf("stage panel release: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, rel.file.Size+1))
	if copyErr != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write panel release: %w", copyErr)
	}
	if n != rel.file.Size {
		_ = tmp.Close()
		return "", fmt.Errorf("panel release size mismatch: got %d, signed %d", n, rel.file.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.file.SHA256 {
		_ = tmp.Close()
		return "", errors.New("panel release SHA-256 does not match the signed panel manifest")
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

// panelUpdateRequest is what the owner confirmed, handed from the panel to the helper through the data directory. It
// can only narrow what the helper installs: the helper still requires the release's signed panel manifest to name
// exactly this version and digest.
type panelUpdateRequest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func writePanelUpdateRequest(dataDir string, r panelUpdateRequest) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".panel-update-request-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dataDir, panelUpdateRequestName))
}

// takePanelUpdateRequest reads and removes the request, so one confirmation starts one install.
func takePanelUpdateRequest(dataDir string) (panelUpdateRequest, error) {
	path := filepath.Join(dataDir, panelUpdateRequestName)
	info, err := os.Lstat(path)
	if err != nil {
		return panelUpdateRequest{}, fmt.Errorf("no confirmed panel update request: %w", err)
	}
	defer os.Remove(path)
	if !info.Mode().IsRegular() || info.Size() > 4<<10 {
		return panelUpdateRequest{}, errors.New("the panel update request is not a small regular file")
	}
	// The helper runs as root and the data directory belongs to the panel's user: read the file that was checked,
	// not whatever the path points to by now.
	f, err := os.Open(path)
	if err != nil {
		return panelUpdateRequest{}, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return panelUpdateRequest{}, errors.New("the panel update request changed while it was read")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return panelUpdateRequest{}, err
	}
	var r panelUpdateRequest
	if err := json.Unmarshal(b, &r); err != nil || !validReleaseTag(r.Version) || !digestPattern.MatchString(r.SHA256) {
		return panelUpdateRequest{}, errors.New("the panel update request is invalid")
	}
	return r, nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
