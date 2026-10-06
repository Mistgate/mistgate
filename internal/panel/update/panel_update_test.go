package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"debug/elf"

	"github.com/mistgate/mistgate/internal/release"
)

// The health window of a replaced panel is 45 s in production; tests that need one set it.
func init() { panelHealthWindow = 0 }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func githubResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func minimalELF(machine elf.Machine) []byte {
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F'})
	b[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	b[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	b[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(b[16:18], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(b[18:20], uint16(machine))
	binary.LittleEndian.PutUint32(b[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(b[52:54], 64)
	return b
}

var (
	panelTestKey   = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	panelTestPub   = panelTestKey.Public().(ed25519.PublicKey)
	panelTestNow   = time.Unix(1_800_000_000, 0)
	panelTestBuilt = int64(1_790_000_000) // the release
	panelOwnBuilt  = int64(1_780_000_000) // the running panel
)

// panelRelease is a GitHub release with a panel binary and, unless unsigned, its signed panel manifest.
type panelFixture struct {
	tag      string
	binary   []byte // what the manifest signs
	served   []byte // what GitHub serves; default binary
	built    int64
	expires  int64
	unsigned bool
	signer   ed25519.PrivateKey
	nodeSig  bool // signed in the node bundle's context
}

func newPanelFixture() *panelFixture {
	return &panelFixture{tag: "v1.2.0", binary: minimalELF(elf.EM_X86_64), built: panelTestBuilt, expires: 1_900_000_000, signer: panelTestKey}
}

func (f *panelFixture) digest() string {
	sum := sha256.Sum256(f.binary)
	return hex.EncodeToString(sum[:])
}

// client serves the release API and its assets, counting the requests by kind.
func (f *panelFixture) client(t *testing.T) (*http.Client, map[string]int) {
	t.Helper()
	served := f.served
	if served == nil {
		served = f.binary
	}
	assets := map[string][]byte{"mistgate-linux-amd64": served}
	if !f.unsigned {
		m := release.Manifest{Schema: release.Schema, Version: f.tag, Built: f.built, Expires: f.expires,
			Files: []release.File{{OS: "linux", Arch: "amd64", Name: "mistgate-linux-amd64", Size: int64(len(f.binary)), SHA256: f.digest()}}}
		body, err := m.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		sig := release.SignPanel(f.signer, body)
		if f.nodeSig {
			sig = release.Sign(f.signer, body)
		}
		assets[release.PanelManifestName], assets[release.PanelSignatureName] = body, sig
	}
	rel := githubRelease{TagName: f.tag, HTMLURL: githubReleasePageBase + f.tag, PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	for name, b := range assets {
		sum := sha256.Sum256(b)
		rel.Assets = append(rel.Assets, githubAsset{Name: name, Size: int64(len(b)), BrowserDownloadURL: panelReleasePrefix + f.tag + "/" + name,
			Digest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	relBody, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	counts := map[string]int{}
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Host == "api.github.com" {
			counts["api"]++
			return githubResponse(http.StatusOK, relBody), nil
		}
		name := filepath.Base(r.URL.Path)
		counts[name]++
		if b, ok := assets[name]; ok && r.URL.Host == "github.com" {
			return githubResponse(http.StatusOK, b), nil
		}
		return githubResponse(http.StatusNotFound, nil), nil
	})}, counts
}

type recorder struct {
	mu    sync.Mutex
	calls [][]string
	show  func(unit string) string // the answer to systemctl show
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == "systemctl" && len(args) > 0 && args[0] == "show" && r.show != nil {
		return []byte(r.show(args[len(args)-1])), nil
	}
	return nil, nil
}

func (r *recorder) launched() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]string
	for _, c := range r.calls {
		if !(c[0] == "systemctl" && c[1] == "show") {
			out = append(out, c)
		}
	}
	return out
}

func newPanelUpdater(t *testing.T, f *panelFixture, rec *recorder, mutate func(*PanelUpdateConfig)) (*GitHubPanelUpdater, map[string]int, string) {
	t.Helper()
	client, counts := f.client(t)
	dataDir := t.TempDir()
	cfg := PanelUpdateConfig{
		CurrentBuilt: panelOwnBuilt, Key: panelTestPub, DataDir: dataDir, ServiceUnit: "mistgate.service",
		Executable: filepath.Join(dataDir, "mistgate"), Enabled: true, OS: "linux", Arch: "amd64", HTTPClient: client,
		Runner: rec.run, Now: func() time.Time { return panelTestNow }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewGitHubPanelUpdater(cfg), counts, dataDir
}

// rootLayoutAt makes the fixed root unit's layout the test panel's own (its data directory, executable and service).
func rootLayoutAt(t *testing.T, dataDir string) {
	t.Helper()
	exe, data, service := rootHelperExecutable, rootHelperDataDir, rootHelperService
	rootHelperExecutable, rootHelperDataDir, rootHelperService = filepath.Join(dataDir, "mistgate"), dataDir, "mistgate.service"
	t.Cleanup(func() { rootHelperExecutable, rootHelperDataDir, rootHelperService = exe, data, service })
}

func readRequest(t *testing.T, dataDir string) panelUpdateRequest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, panelUpdateRequestName))
	if err != nil {
		t.Fatalf("no update request: %v", err)
	}
	var r panelUpdateRequest
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPanelUpdaterOffersSignedReleaseAndSchedulesTheHelper(t *testing.T) {
	f := newPanelFixture()
	rec := &recorder{}
	updater, counts, dataDir := newPanelUpdater(t, f, rec, nil)

	status := updater.Check(context.Background())
	if !status.Available || !status.Supported || !status.Installable || status.Version != "v1.2.0" || status.ErrorKey != "" ||
		status.Built != panelTestBuilt || status.SHA256 != f.digest() {
		t.Fatalf("unexpected release status: %+v", status)
	}
	if err := updater.Install(context.Background(), "v1.2.0", f.digest()); err != nil {
		t.Fatal(err)
	}
	if counts["api"] != 2 || counts["mistgate-linux-amd64"] != 0 {
		t.Fatalf("GitHub requests: %v (the helper downloads the binary, not the panel)", counts)
	}
	l := rec.launched()
	if len(l) != 1 || l[0][0] != "systemd-run" || !containsArg(l[0], "panel-update-helper") || !containsArg(l[0], "--fetch-latest") ||
		containsArg(l[0], "--sha256") || !containsArg(l[0], "--property=TimeoutStartSec=30min") {
		t.Fatalf("unexpected launch command: %#v", l)
	}
	if r := readRequest(t, dataDir); r.Version != "v1.2.0" || r.SHA256 != f.digest() {
		t.Fatalf("request = %+v", r)
	}
	if !updater.Status().Installing {
		t.Fatal("successful scheduling did not retain the installing state")
	}
}

func TestPanelUpdaterRequestsFixedRootHelperForNonRootService(t *testing.T) {
	f := newPanelFixture()
	rec := &recorder{show: func(string) string {
		return "LoadState=loaded\nActiveState=inactive\nInactiveExitTimestampMonotonic=5\n"
	}}
	updater, counts, dataDir := newPanelUpdater(t, f, rec, func(c *PanelUpdateConfig) { c.UseRootHelperService = true })
	rootLayoutAt(t, dataDir)
	if err := updater.Install(context.Background(), "v1.2.0", f.digest()); err != nil {
		t.Fatal(err)
	}
	if counts["api"] != 1 || counts["mistgate-linux-amd64"] != 0 {
		t.Fatalf("panel process GitHub requests: %v", counts)
	}
	l := rec.launched()
	if len(l) != 1 || strings.Join(l[0], " ") != "systemctl start --no-block "+panelUpdateHelperService {
		t.Fatalf("helper request = %#v", l)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "panel-update.new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unprivileged panel staged a binary for the root helper: %v", err)
	}
	if r := readRequest(t, dataDir); r.Version != "v1.2.0" {
		t.Fatalf("request = %+v", r)
	}
	if !updater.Status().Installing {
		t.Fatal("accepted helper request did not retain the installing state")
	}
}

// The fixed root unit names its paths: a non-root panel elsewhere gets an error that names them, and nothing is requested.
func TestPanelUpdaterRootHelperNeedsItsLayout(t *testing.T) {
	f := newPanelFixture()
	for name, mutate := range map[string]func(*PanelUpdateConfig){
		"another data directory": func(c *PanelUpdateConfig) { c.DataDir = filepath.Join(c.DataDir, "elsewhere") },
		"another executable":     func(c *PanelUpdateConfig) { c.Executable = filepath.Join(c.DataDir, "bin", "mistgate") },
		"another service":        func(c *PanelUpdateConfig) { c.ServiceUnit = "vpn-panel.service" },
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			var dataDir string
			updater, _, _ := newPanelUpdater(t, f, rec, func(c *PanelUpdateConfig) {
				c.UseRootHelperService, dataDir = true, c.DataDir
				mutate(c)
			})
			rootLayoutAt(t, dataDir)
			err := updater.Install(context.Background(), "v1.2.0", f.digest())
			if !errors.Is(err, ErrPanelUnsupported) || !strings.Contains(err.Error(), panelUpdateHelperService) ||
				!strings.Contains(err.Error(), rootHelperExecutable) || !strings.Contains(err.Error(), rootHelperDataDir) || !strings.Contains(err.Error(), rootHelperService) {
				t.Errorf("error: %v", err)
			}
			if len(rec.launched()) != 0 || updater.Status().Installing {
				t.Errorf("launched %v, installing %v", rec.launched(), updater.Status().Installing)
			}
			if _, err := os.Stat(filepath.Join(dataDir, panelUpdateRequestName)); err == nil {
				t.Error("wrote a request the helper never reads")
			}
		})
	}
	// production names the unit's own paths
	if rootHelperExecutable != "/usr/local/bin/mistgate" || rootHelperDataDir != "/var/lib/mistgate" || rootHelperService != "mistgate.service" {
		t.Errorf("layout %s %s %s", rootHelperExecutable, rootHelperDataDir, rootHelperService)
	}
}

// The owner confirms one exact release; any other answer from GitHub by then is refused before anything runs.
func TestPanelUpdaterInstallsOnlyTheConfirmedRelease(t *testing.T) {
	f := newPanelFixture()
	for name, args := range map[string][2]string{
		"other version": {"v1.1.9", f.digest()},
		"other digest":  {"v1.2.0", strings.Repeat("0", 64)},
		"nothing named": {"", ""},
	} {
		rec := &recorder{}
		updater, _, dataDir := newPanelUpdater(t, f, rec, nil)
		if err := updater.Install(context.Background(), args[0], args[1]); !errors.Is(err, ErrPanelChanged) {
			t.Errorf("%s: %v", name, err)
		}
		if len(rec.launched()) != 0 {
			t.Errorf("%s: launched %v", name, rec.launched())
		}
		if _, err := os.Stat(filepath.Join(dataDir, panelUpdateRequestName)); err == nil {
			t.Errorf("%s: wrote a request", name)
		}
	}
}

// Without a panel manifest signed with the compiled-in key a release is shown but cannot be installed.
func TestPanelUpdaterRefusesReleasesWithoutAValidPanelSignature(t *testing.T) {
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	for name, tc := range map[string]struct {
		mutate func(*panelFixture)
		cfg    func(*PanelUpdateConfig)
		key    string
	}{
		"no panel manifest":     {mutate: func(f *panelFixture) { f.unsigned = true }, key: "unsigned"},
		"another key":           {mutate: func(f *panelFixture) { f.signer = other }, key: "unsigned"},
		"a node signature":      {mutate: func(f *panelFixture) { f.nodeSig = true }, key: "unsigned"},
		"expired":               {mutate: func(f *panelFixture) { f.expires = panelTestNow.Unix() - 1 }, key: "expired"},
		"no compiled key":       {cfg: func(c *PanelUpdateConfig) { c.Key = nil }, key: "no_key"},
		"tampered binary":       {mutate: func(f *panelFixture) { f.served = append([]byte("x"), f.binary[1:]...) }}, // caught by the helper
		"no binary for my arch": {cfg: func(c *PanelUpdateConfig) { c.Arch = "arm64" }, key: "asset_missing"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPanelFixture()
			if tc.mutate != nil {
				tc.mutate(f)
			}
			rec := &recorder{}
			updater, _, _ := newPanelUpdater(t, f, rec, tc.cfg)
			status := updater.Check(context.Background())
			if tc.key == "" {
				if !status.Installable {
					t.Fatalf("status %+v", status)
				}
				return
			}
			if status.ErrorKey != tc.key || status.Installable || status.Version != "v1.2.0" {
				t.Fatalf("status %+v, want error %q", status, tc.key)
			}
			if err := updater.Install(context.Background(), "v1.2.0", f.digest()); err == nil {
				t.Fatal("installed")
			}
			if len(rec.launched()) != 0 {
				t.Fatalf("launched %v", rec.launched())
			}
		})
	}
}

// Releases are ordered by the signed build time: a git-describe build of a later commit (v1.2.0-3-gabc) never sees
// its older tag as an update, whatever the version strings say.
func TestPanelUpdaterOrdersByBuildTime(t *testing.T) {
	f := newPanelFixture()
	for name, tc := range map[string]struct {
		own  int64
		want bool
	}{
		"older build":           {panelTestBuilt - 1, true},
		"the release itself":    {panelTestBuilt, false},
		"a later commit":        {panelTestBuilt + 3600, false},
		"a build without built": {0, true},
	} {
		updater, _, _ := newPanelUpdater(t, f, &recorder{}, func(c *PanelUpdateConfig) { c.CurrentBuilt = tc.own })
		if s := updater.Check(context.Background()); s.Available != tc.want || s.ErrorKey != "" {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

// The Check button is answered from memory for a few minutes; the unauthenticated GitHub quota is shared.
func TestPanelUpdaterCachesChecks(t *testing.T) {
	f := newPanelFixture()
	now := panelTestNow
	updater, counts, _ := newPanelUpdater(t, f, &recorder{}, func(c *PanelUpdateConfig) { c.Now = func() time.Time { return now } })
	updater.Check(context.Background())
	updater.Check(context.Background())
	if counts["api"] != 1 {
		t.Fatalf("API requests within the cache: %d", counts["api"])
	}
	now = now.Add(panelUpdateCheckCache)
	updater.Check(context.Background())
	if counts["api"] != 2 {
		t.Fatalf("API requests after the cache: %d", counts["api"])
	}
}

// A GitHub outage (a timeout, a 403, a 5xx) is not an answer about the release: the update the panel already knows
// stays on the Updates screen and in the card, with the error beside it, and Install (which re-fetches and re-verifies
// the signed release itself) stays offered. A definite verdict about the latest release (here: it expired) replaces it.
func TestPanelUpdaterKeepsTheKnownReleaseWhenACheckFails(t *testing.T) {
	f := newPanelFixture()
	now := panelTestNow
	down := false
	updater, _, _ := newPanelUpdater(t, f, &recorder{}, func(c *PanelUpdateConfig) {
		c.Now = func() time.Time { return now }
		inner := c.HTTPClient.Transport
		c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if down {
				return githubResponse(http.StatusServiceUnavailable, nil), nil
			}
			return inner.RoundTrip(r)
		})}
	})
	good := updater.Check(context.Background())
	if !good.Available || !good.Installable {
		t.Fatalf("setup: %+v", good)
	}

	down = true
	now = now.Add(panelUpdateCheckCache)
	failed := updater.Check(context.Background())
	if failed.ErrorKey != "check_failed" || failed.CheckedUnix != now.Unix() {
		t.Fatalf("failed check: %+v", failed)
	}
	want := good
	want.ErrorKey, want.CheckedUnix = "check_failed", now.Unix()
	if failed != want || updater.Status() != want {
		t.Fatalf("a failed check changed the known release:\n got %+v\nwant %+v", updater.Status(), want)
	}
	if err := updater.Install(context.Background(), good.Version, good.SHA256); err == nil {
		t.Fatal("install succeeded while GitHub was unavailable")
	}
	if installed := updater.Status(); installed != want {
		t.Fatalf("a failed install check changed the known release:\n got %+v\nwant %+v", installed, want)
	}

	down = false
	now = now.Add(panelUpdateCheckCache)
	if s := updater.Check(context.Background()); !s.Available || s.ErrorKey != "" {
		t.Fatalf("recovery: %+v", s)
	}

	f.expires = panelTestNow.Unix() - 1 // a definite verdict is not kept
	f2, _ := f.client(t)
	updater.client = f2
	now = now.Add(panelUpdateCheckCache)
	if s := updater.Check(context.Background()); s.ErrorKey != "expired" || s.Installable {
		t.Fatalf("expired: %+v", s)
	}
}

// A helper that ends while this panel still runs has failed: the "installing" state (and with it the reservation
// against rollouts) ends at once instead of after the 35-minute guard.
func TestPanelUpdaterNoticesAFailedHelper(t *testing.T) {
	setWatch(t)
	f := newPanelFixture()
	var mu sync.Mutex
	state := "LoadState=loaded\nActiveState=failed\nInactiveExitTimestampMonotonic=5\n" // a previous failed attempt
	rec := &recorder{show: func(string) string { mu.Lock(); defer mu.Unlock(); return state }}
	updater, _, dataDir := newPanelUpdater(t, f, rec, func(c *PanelUpdateConfig) { c.UseRootHelperService = true })
	rootLayoutAt(t, dataDir)
	if err := updater.Install(context.Background(), "v1.2.0", f.digest()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if !updater.Status().Installing {
		t.Fatal("the previous run's failed state ended this install")
	}
	mu.Lock()
	state = "LoadState=loaded\nActiveState=activating\nInactiveExitTimestampMonotonic=9\n"
	mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	if !updater.Status().Installing {
		t.Fatal("a running helper ended the install")
	}
	mu.Lock()
	state = "LoadState=loaded\nActiveState=failed\nInactiveExitTimestampMonotonic=9\n"
	mu.Unlock()
	waitFor(t, func() bool { return !updater.Status().Installing })
	if s := updater.Status(); s.ErrorKey != "install_failed" {
		t.Fatalf("status %+v", s)
	}

	// a transient unit is collected when it ends
	rec2 := &recorder{show: func(string) string {
		return "LoadState=not-found\nActiveState=inactive\nInactiveExitTimestampMonotonic=0\n"
	}}
	u2, _, _ := newPanelUpdater(t, f, rec2, nil)
	if err := u2.Install(context.Background(), "v1.2.0", f.digest()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !u2.Status().Installing })
}

func setWatch(t *testing.T) {
	t.Helper()
	previous := panelWatchInterval
	panelWatchInterval = time.Millisecond
	t.Cleanup(func() { panelWatchInterval = previous })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestPanelUpdaterHandlesNoStableRelease(t *testing.T) {
	updater := NewGitHubPanelUpdater(PanelUpdateConfig{
		Key: panelTestPub, DataDir: t.TempDir(), ServiceUnit: "mistgate.service", Executable: "mistgate",
		Enabled: true, OS: "linux", Arch: "amd64", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return githubResponse(http.StatusNotFound, nil), nil
		})},
	})
	status := updater.Check(context.Background())
	if status.ErrorKey != "no_release" || status.Available || status.CheckedUnix == 0 {
		t.Fatalf("unexpected no-release status: %+v", status)
	}
}

func TestGitHubReleaseRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"github", "https://github.com/Mistgate/mistgate/releases/latest", true},
		{"API", "https://api.github.com/repos/Mistgate/mistgate/releases/latest", true},
		{"release assets", "https://release-assets.githubusercontent.com/file", true},
		{"HTTP downgrade", "http://github.com/Mistgate/mistgate/releases/latest", false},
		{"untrusted host", "https://example.com/release", false},
		{"nonstandard port", "https://github.com:8443/release", false},
		{"userinfo", "https://user@github.com/release", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := githubReleaseRedirect(req, nil) == nil
			if got != tc.want {
				t.Fatalf("redirect allowed = %v, want %v", got, tc.want)
			}
		})
	}
	if err := githubReleaseRedirect(mustRequest(t, "https://github.com/release"), make([]*http.Request, 5)); err == nil {
		t.Fatal("accepted more than five redirects")
	}
}

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// systemctlRecorder answers like a healthy systemd (NRestarts stays 0) unless hook returns an error or an answer.
func systemctlRecorder(calls *[][]string, hook func(args []string) (string, error)) systemctlFunc {
	return func(args ...string) (string, error) {
		*calls = append(*calls, append([]string(nil), args...))
		if hook != nil {
			if out, err := hook(args); out != "" || err != nil {
				return out, err
			}
		}
		if len(args) > 0 && args[0] == "show" {
			return "0", nil
		}
		return "", nil
	}
}

func helperEnv(t *testing.T) (dataDir, target string) {
	t.Helper()
	root := t.TempDir()
	dataDir = filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(root, "mistgate")
	if err := os.WriteFile(target, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dataDir, target
}

func helperConfig(t *testing.T, f *panelFixture, dataDir, target string) PanelUpdateConfig {
	client, _ := f.client(t)
	return PanelUpdateConfig{CurrentBuilt: panelOwnBuilt, Key: panelTestPub, DataDir: dataDir, ServiceUnit: "mistgate.service", Executable: target,
		OS: "linux", Arch: "amd64", HTTPClient: client, Now: func() time.Time { return panelTestNow }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestRootHelperInstallsTheConfirmedSignedRelease(t *testing.T) {
	f := newPanelFixture()
	dataDir, target := helperEnv(t)
	if err := writePanelUpdateRequest(dataDir, panelUpdateRequest{Version: "v1.2.0", SHA256: f.digest()}); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	if err := installLatestPanelUpdate(context.Background(), helperConfig(t, f, dataDir, target), systemctlRecorder(&calls, nil)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, f.binary) {
		t.Fatalf("installed binary = %x, %v", got, err)
	}
	got, err = os.ReadFile(target + ".prev")
	if err != nil || string(got) != "previous" {
		t.Fatalf("previous binary = %q, %v", got, err)
	}
	if len(calls) == 0 || strings.Join(calls[0], " ") != "stop mistgate.service" {
		t.Fatalf("systemctl calls = %#v", calls)
	}
	if _, err := os.Stat(filepath.Join(dataDir, panelUpdateRequestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the request was not consumed: %v", err)
	}
}

// The helper trusts nothing the panel's service user can write except the narrowing request: it verifies the signed
// manifest again, and anything that does not match stops it before the panel is touched.
func TestRootHelperRefusesBeforeStoppingThePanel(t *testing.T) {
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	for name, tc := range map[string]struct {
		mutate  func(*panelFixture)
		request *panelUpdateRequest
		cfg     func(*PanelUpdateConfig)
		want    string
	}{
		"tampered asset":      {mutate: func(f *panelFixture) { f.served = append([]byte("x"), f.binary[1:]...) }, want: "SHA-256"},
		"no request":          {request: &panelUpdateRequest{}, want: "no confirmed panel update request"},
		"another release":     {request: &panelUpdateRequest{Version: "v1.1.0", SHA256: strings.Repeat("a", 64)}, want: ErrPanelChanged.Error()},
		"unsigned release":    {mutate: func(f *panelFixture) { f.unsigned = true }, want: ErrPanelUnsigned.Error()},
		"foreign signature":   {mutate: func(f *panelFixture) { f.signer = other }, want: ErrPanelUnsigned.Error()},
		"not newer":           {cfg: func(c *PanelUpdateConfig) { c.CurrentBuilt = panelTestBuilt }, want: ErrNoPanelUpdate.Error()},
		"no key in the build": {cfg: func(c *PanelUpdateConfig) { c.Key = nil }, want: ErrPanelNoKey.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPanelFixture()
			if tc.mutate != nil {
				tc.mutate(f)
			}
			dataDir, target := helperEnv(t)
			req := panelUpdateRequest{Version: "v1.2.0", SHA256: f.digest()}
			if tc.request != nil {
				req = *tc.request
			}
			if req.Version != "" {
				if err := writePanelUpdateRequest(dataDir, req); err != nil {
					t.Fatal(err)
				}
			}
			cfg := helperConfig(t, f, dataDir, target)
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			var calls [][]string
			err := installLatestPanelUpdate(context.Background(), cfg, systemctlRecorder(&calls, nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if len(calls) != 0 {
				t.Fatalf("systemctl ran: %#v", calls)
			}
			if got, _ := os.ReadFile(target); string(got) != "previous" {
				t.Fatalf("installed binary changed: %q", got)
			}
		})
	}
}

func TestPanelUpdateRequestIsValidated(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{`{"version":"v1.2.0","sha256":"short"}`, `{"version":"latest","sha256":"` + strings.Repeat("a", 64) + `"}`, `not json`} {
		if err := os.WriteFile(filepath.Join(dir, panelUpdateRequestName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := takePanelUpdateRequest(dir); err == nil {
			t.Errorf("accepted %s", body)
		}
		if _, err := os.Stat(filepath.Join(dir, panelUpdateRequestName)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an invalid request was kept: %v", err)
		}
	}
}

func stagedUpdate(t *testing.T) (dataDir, target, digest, database string) {
	t.Helper()
	dataDir, target = helperEnv(t)
	database = filepath.Join(dataDir, "mistgate.db")
	if err := os.WriteFile(database, []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	newBinary := minimalELF(elf.EM_X86_64)
	if err := os.WriteFile(filepath.Join(dataDir, "panel-update.new"), newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(newBinary)
	return dataDir, target, hex.EncodeToString(sum[:]), database
}

func TestApplyPanelUpdateReplacesBinaryAndKeepsPreviousCopy(t *testing.T) {
	dataDir, target, digest, _ := stagedUpdate(t)
	var calls [][]string
	if err := applyPanelUpdate(dataDir, target, "mistgate.service", digest, "amd64", systemctlRecorder(&calls, nil)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, minimalELF(elf.EM_X86_64)) {
		t.Fatalf("updated executable = %x, %v", got, err)
	}
	got, err = os.ReadFile(target + ".prev")
	if err != nil || string(got) != "previous" {
		t.Fatalf("previous executable = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "panel-update.new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file remains: %v", err)
	}
	if len(calls) < 4 || strings.Join(calls[0], " ") != "stop mistgate.service" || strings.Join(calls[1], " ") != "start mistgate.service" ||
		strings.Join(calls[2], " ") != "show --property=NRestarts --value mistgate.service" {
		t.Fatalf("systemctl calls = %#v", calls)
	}
	backupInfo, err := os.Stat(dataDir + ".panel-update-backup.tar.gz")
	if err != nil || (runtime.GOOS != "windows" && backupInfo.Mode().Perm() != 0o600) || backupInfo.Size() == 0 {
		t.Fatalf("data backup = %v, %v", backupInfo, err)
	}
}

func TestApplyPanelUpdateRollsBackWhenNewServiceDoesNotStayActive(t *testing.T) {
	dataDir, target, digest, database := stagedUpdate(t)
	dataDirOwner := snapshotFileOwner(t, dataDir)
	databaseOwner := snapshotFileOwner(t, database)
	var calls [][]string
	restartCount := 0
	err := applyPanelUpdate(dataDir, target, "mistgate.service", digest, "amd64", systemctlRecorder(&calls, func(args []string) (string, error) {
		if len(args) == 2 && args[0] == "start" {
			restartCount++
			if restartCount == 1 {
				if err := os.WriteFile(database, []byte("new migrated database"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", errors.New("new service failed")
			}
		}
		return "", nil
	}))
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("update error = %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || string(got) != "previous" {
		t.Fatalf("restored executable = %q, %v", got, readErr)
	}
	got, readErr = os.ReadFile(database)
	if readErr != nil || string(got) != "old database" {
		t.Fatalf("restored database = %q, %v", got, readErr)
	}
	assertFileOwner(t, dataDir, dataDirOwner)
	assertFileOwner(t, database, databaseOwner)
	failed, readErr := os.ReadFile(target + ".failed")
	if readErr != nil || !bytes.Equal(failed, minimalELF(elf.EM_X86_64)) {
		t.Fatalf("failed executable = %x, %v", failed, readErr)
	}
}

// A panel that crashes a few seconds after the start and is restarted by systemd (Restart=on-failure) is a failed
// update: the restart counter moves within the health window, and the binary and the data come back.
func TestApplyPanelUpdateRollsBackAPanelThatSystemdRestarts(t *testing.T) {
	previousWindow, previousPoll := panelHealthWindow, panelHealthPoll
	panelHealthWindow, panelHealthPoll = time.Second, time.Millisecond
	t.Cleanup(func() { panelHealthWindow, panelHealthPoll = previousWindow, previousPoll })
	dataDir, target, digest, database := stagedUpdate(t)
	var calls [][]string
	shows, starts := 0, 0
	err := applyPanelUpdate(dataDir, target, "mistgate.service", digest, "amd64", systemctlRecorder(&calls, func(args []string) (string, error) {
		switch args[0] {
		case "start":
			starts++
			if starts == 1 {
				_ = os.WriteFile(database, []byte("new migrated database"), 0o600)
			}
		case "show":
			if starts == 1 {
				shows++
				if shows > 3 { // the new panel crashed after a while; systemd brought it back
					return "1", nil
				}
			}
		}
		return "", nil
	}))
	if err == nil || !strings.Contains(err.Error(), "rolled back") || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("update error = %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "previous" {
		t.Fatalf("restored executable = %q", got)
	}
	if got, _ := os.ReadFile(database); string(got) != "old database" {
		t.Fatalf("restored database = %q", got)
	}
	if starts != 2 {
		t.Fatalf("the previous panel was not started again: %d starts", starts)
	}
}

type fileOwner struct {
	uid, gid uint64
	known    bool
}

func snapshotFileOwner(t *testing.T, path string) fileOwner {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sys := reflect.ValueOf(info.Sys())
	if sys.Kind() == reflect.Pointer {
		sys = sys.Elem()
	}
	if !sys.IsValid() || sys.Kind() != reflect.Struct {
		return fileOwner{}
	}
	uid, gid := sys.FieldByName("Uid"), sys.FieldByName("Gid")
	if !uid.IsValid() || !gid.IsValid() || uid.Kind() < reflect.Uint || uid.Kind() > reflect.Uint64 || gid.Kind() < reflect.Uint || gid.Kind() > reflect.Uint64 {
		return fileOwner{}
	}
	return fileOwner{uid: uid.Uint(), gid: gid.Uint(), known: true}
}

func assertFileOwner(t *testing.T, path string, want fileOwner) {
	t.Helper()
	if !want.known {
		return
	}
	got := snapshotFileOwner(t, path)
	if !got.known || got.uid != want.uid || got.gid != want.gid {
		t.Fatalf("restored ownership for %s = %+v, want %+v", path, got, want)
	}
}

func TestApplyPanelUpdateKeepsDataUntouchedIfFailedServiceCannotStop(t *testing.T) {
	dataDir, target, digest, database := stagedUpdate(t)
	var calls [][]string
	stops := 0
	err := applyPanelUpdate(dataDir, target, "mistgate.service", digest, "amd64", systemctlRecorder(&calls, func(args []string) (string, error) {
		if len(args) == 2 && args[0] == "stop" {
			stops++
			if stops == 2 {
				return "", errors.New("service still running")
			}
		}
		if len(args) == 2 && args[0] == "start" {
			if err := os.WriteFile(database, []byte("new database"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if len(args) == 3 && args[0] == "is-active" {
			return "", errors.New("new panel is not stable")
		}
		return "", nil
	}))
	if err == nil || !strings.Contains(err.Error(), "could not stop the panel") {
		t.Fatalf("update error = %v", err)
	}
	got, readErr := os.ReadFile(database)
	if readErr != nil || string(got) != "new database" {
		t.Fatalf("data was modified during an unconfirmed rollback: %q, %v", got, readErr)
	}
	got, readErr = os.ReadFile(target + ".prev")
	if readErr != nil || string(got) != "previous" {
		t.Fatalf("previous executable was not kept for manual recovery: %q, %v", got, readErr)
	}
	if _, err := os.Stat(dataDir + ".panel-update-backup.tar.gz"); err != nil {
		t.Fatalf("data backup was not preserved: %v", err)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want || strings.HasPrefix(arg, want+"=") {
			return true
		}
	}
	return false
}
