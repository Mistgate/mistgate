//go:build linux

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mistgate/mistgate/internal/release"
)

// The owner and mode checks need a root-owned fixture, so these tests run as root (CI containers, the WSL root user)
// and are skipped otherwise. Fixtures live in t.TempDir(), never in the repository checkout, whose mount may not
// honour ownership and modes.
func policyFixture(t *testing.T, body string, mode os.FileMode) (dir, path string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root to create root-owned fixtures")
	}
	dir = t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "panel-update-channel")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestRootPanelUpdateChannelReadsRootOwnedValue(t *testing.T) {
	for body, want := range map[string]release.Channel{
		"stable\n": release.ChannelStable, "testing\n": release.ChannelTesting, "dev\n": release.ChannelDev, "dev": release.ChannelDev,
	} {
		_, path := policyFixture(t, body, 0o644)
		if got, err := readRootAllowedPanelUpdateChannel(path); err != nil || got != want {
			t.Fatalf("policy %q = %q, %v; want %q", body, got, err, want)
		}
	}
}

func TestRootPanelUpdateChannelMissingMeansStable(t *testing.T) {
	dir, _ := policyFixture(t, "dev\n", 0o644)
	for _, path := range []string{filepath.Join(dir, "absent"), filepath.Join(dir, "no-such-dir", "panel-update-channel")} {
		if got, err := readRootAllowedPanelUpdateChannel(path); err != nil || got != release.ChannelStable {
			t.Fatalf("missing policy %s = %q, %v; want stable", path, got, err)
		}
	}
}

func TestRootPanelUpdateChannelRejectsUnsafeFiles(t *testing.T) {
	reject := func(t *testing.T, path string) {
		t.Helper()
		if got, err := readRootAllowedPanelUpdateChannel(path); !errors.Is(err, ErrPanelUpdateChannelConfig) || got != "" {
			t.Fatalf("policy = %q, %v; want a config error and no channel", got, err)
		}
	}

	t.Run("wrong owner", func(t *testing.T) {
		_, path := policyFixture(t, "dev\n", 0o644)
		if err := os.Chown(path, 65534, 0); err != nil {
			t.Skipf("cannot assign a non-root owner: %v", err)
		}
		reject(t, path)
	})
	t.Run("group or world writable file", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o664, 0o646, 0o666, 0o620} {
			_, path := policyFixture(t, "stable\n", mode)
			reject(t, path)
		}
	})
	t.Run("group or world writable directory", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o775, 0o757, 0o777} {
			dir, path := policyFixture(t, "stable\n", 0o644)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			reject(t, path)
		}
	})
	t.Run("directory of another owner", func(t *testing.T) {
		dir, path := policyFixture(t, "stable\n", 0o644)
		if err := os.Chown(dir, 65534, 0); err != nil {
			t.Skipf("cannot assign a non-root owner: %v", err)
		}
		reject(t, path)
	})
	t.Run("symlink", func(t *testing.T) {
		dir, _ := policyFixture(t, "stable\n", 0o644)
		link := filepath.Join(dir, "link")
		if err := os.Symlink(filepath.Join(dir, "panel-update-channel"), link); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		reject(t, link)
	})
	t.Run("symlinked directory", func(t *testing.T) {
		dir, _ := policyFixture(t, "stable\n", 0o644)
		link := filepath.Join(t.TempDir(), "etc-mistgate")
		if err := os.Symlink(dir, link); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		reject(t, filepath.Join(link, "panel-update-channel"))
	})
	t.Run("not a regular file", func(t *testing.T) {
		dir, _ := policyFixture(t, "stable\n", 0o644)
		fifo := filepath.Join(dir, "fifo")
		if err := syscall.Mkfifo(fifo, 0o644); err != nil {
			t.Skipf("fifo creation unavailable: %v", err)
		}
		reject(t, fifo)
		sub := filepath.Join(dir, "subdir")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		reject(t, sub)
	})
	t.Run("not exactly one channel", func(t *testing.T) {
		for _, body := range []string{
			"", "\n", " stable\n", "stable \n", "stable\n\n", "stable\r\n", "Stable\n", "STABLE\n", "latest\n", "nightly\n",
			"dev\nstable\n", "stable\ndev", "stable\x00\n", "# dev\n", "stable # dev\n", "\xef\xbb\xbfstable\n",
			strings.Repeat("a", 4096), strings.Repeat("stable\n", 10),
		} {
			_, path := policyFixture(t, body, 0o644)
			reject(t, path)
		}
	})
}
