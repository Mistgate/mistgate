package update

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"debug/elf"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func githubResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
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

func releaseFixture(t *testing.T, body []byte, tag string) []byte {
	t.Helper()
	digest := sha256.Sum256(body)
	release := githubRelease{
		TagName: tag, HTMLURL: githubReleasePageBase + tag,
		PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Assets: []githubAsset{{
			Name: "mistgate-linux-amd64", Size: int64(len(body)),
			BrowserDownloadURL: panelReleasePrefix + tag + "/mistgate-linux-amd64",
			Digest:             "sha256:" + hex.EncodeToString(digest[:]),
		}},
	}
	b, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGitHubPanelUpdaterChecksStableReleaseAndSchedulesInstall(t *testing.T) {
	binary := minimalELF(elf.EM_X86_64)
	releaseBody := releaseFixture(t, binary, "v1.2.0")
	var releaseRequests, assetRequests int
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.github.com":
			releaseRequests++
			return githubResponse(http.StatusOK, releaseBody), nil
		case "github.com":
			assetRequests++
			return githubResponse(http.StatusOK, binary), nil
		default:
			return nil, errors.New("unexpected host " + r.URL.Host)
		}
	})}
	var commands [][]string
	dataDir := t.TempDir()
	updater := NewGitHubPanelUpdater(PanelUpdateConfig{
		CurrentVersion: "v1.1.0", CurrentBuilt: 1, DataDir: dataDir, ServiceUnit: "mistgate.service",
		Executable: filepath.Join(dataDir, "mistgate"), Enabled: true, OS: "linux", Arch: "amd64", HTTPClient: client,
		Runner: func(_ context.Context, name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			return nil
		},
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})

	status := updater.Check(context.Background())
	if !status.Available || !status.Supported || !status.Installable || status.Version != "v1.2.0" || status.ErrorKey != "" {
		t.Fatalf("unexpected release status: %+v", status)
	}
	if err := updater.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if releaseRequests != 2 || assetRequests != 1 {
		t.Fatalf("GitHub requests: release=%d asset=%d", releaseRequests, assetRequests)
	}
	if len(commands) != 1 || commands[0][0] != "systemd-run" || !containsArg(commands[0], "panel-update-helper") || !containsArg(commands[0], "--sha256") || !containsArg(commands[0], "--property=TimeoutStartSec=30min") {
		t.Fatalf("unexpected launch command: %#v", commands)
	}
	staged, err := os.ReadFile(filepath.Join(dataDir, "panel-update.new"))
	if err != nil || string(staged) != string(binary) {
		t.Fatalf("staged file = %x, %v", staged, err)
	}
	if !updater.Status().Installing {
		t.Fatal("successful scheduling did not retain the installing state")
	}
}

func TestGitHubPanelUpdaterHandlesNoStableRelease(t *testing.T) {
	updater := NewGitHubPanelUpdater(PanelUpdateConfig{
		CurrentVersion: "v1.1.0", DataDir: t.TempDir(), ServiceUnit: "mistgate.service", Executable: "mistgate",
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

func TestGitHubPanelUpdaterRejectsTamperedDownloadBeforeScheduling(t *testing.T) {
	binary := minimalELF(elf.EM_X86_64)
	tampered := append([]byte(nil), binary...)
	tampered[0] = 'x' // keep the advertised size so the SHA-256 check is the failing guard
	releaseBody := releaseFixture(t, binary, "v1.2.0")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			return githubResponse(http.StatusOK, releaseBody), nil
		}
		return githubResponse(http.StatusOK, tampered), nil
	})}
	launched := false
	updater := NewGitHubPanelUpdater(PanelUpdateConfig{
		CurrentVersion: "v1.1.0", DataDir: t.TempDir(), ServiceUnit: "mistgate.service", Executable: filepath.Join(t.TempDir(), "mistgate"),
		Enabled: true, OS: "linux", Arch: "amd64", HTTPClient: client,
		Runner: func(context.Context, string, ...string) error { launched = true; return nil },
	})
	if err := updater.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered binary error = %v", err)
	}
	if launched {
		t.Fatal("scheduled a tampered binary")
	}
}

func TestApplyPanelUpdateReplacesBinaryAndKeepsPreviousCopy(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "mistgate.db"), []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	newBinary := minimalELF(elf.EM_X86_64)
	stage := filepath.Join(dataDir, "panel-update.new")
	if err := os.WriteFile(stage, newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "mistgate")
	if err := os.WriteFile(target, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newBinary)
	var calls [][]string
	err := applyPanelUpdate(dataDir, target, "mistgate.service", hex.EncodeToString(digest[:]), "amd64", func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(newBinary) {
		t.Fatalf("updated executable = %x, %v", got, err)
	}
	got, err = os.ReadFile(target + ".prev")
	if err != nil || string(got) != "previous" {
		t.Fatalf("previous executable = %q, %v", got, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file remains: %v", err)
	}
	if len(calls) != 5 || strings.Join(calls[0], " ") != "stop mistgate.service" || strings.Join(calls[1], " ") != "start mistgate.service" {
		t.Fatalf("systemctl calls = %#v", calls)
	}
	backupInfo, err := os.Stat(dataDir + ".panel-update-backup.tar.gz")
	if err != nil || (runtime.GOOS != "windows" && backupInfo.Mode().Perm() != 0o600) || backupInfo.Size() == 0 {
		t.Fatalf("data backup = %v, %v", backupInfo, err)
	}
}

func TestApplyPanelUpdateRollsBackWhenNewServiceDoesNotStayActive(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dataDir, "mistgate.db")
	if err := os.WriteFile(database, []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	newBinary := minimalELF(elf.EM_X86_64)
	if err := os.WriteFile(filepath.Join(dataDir, "panel-update.new"), newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "mistgate")
	if err := os.WriteFile(target, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newBinary)
	var calls [][]string
	restartCount := 0
	err := applyPanelUpdate(dataDir, target, "mistgate.service", hex.EncodeToString(digest[:]), "amd64", func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if len(args) == 2 && args[0] == "start" {
			restartCount++
			if restartCount == 1 {
				if err := os.WriteFile(database, []byte("new migrated database"), 0o600); err != nil {
					t.Fatal(err)
				}
				return errors.New("new service failed")
			}
		}
		return nil
	})
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
	failed, readErr := os.ReadFile(target + ".failed")
	if readErr != nil || string(failed) != string(newBinary) {
		t.Fatalf("failed executable = %x, %v", failed, readErr)
	}
}

func TestApplyPanelUpdateKeepsDataUntouchedIfFailedServiceCannotStop(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dataDir, "mistgate.db")
	if err := os.WriteFile(database, []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	newBinary := minimalELF(elf.EM_X86_64)
	if err := os.WriteFile(filepath.Join(dataDir, "panel-update.new"), newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "mistgate")
	if err := os.WriteFile(target, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newBinary)
	stops := 0
	err := applyPanelUpdate(dataDir, target, "mistgate.service", hex.EncodeToString(digest[:]), "amd64", func(args ...string) error {
		if len(args) == 2 && args[0] == "stop" {
			stops++
			if stops == 2 {
				return errors.New("service still running")
			}
		}
		if len(args) == 2 && args[0] == "start" {
			if err := os.WriteFile(database, []byte("new database"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if len(args) == 3 && args[0] == "is-active" {
			return errors.New("new panel is not stable")
		}
		return nil
	})
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
