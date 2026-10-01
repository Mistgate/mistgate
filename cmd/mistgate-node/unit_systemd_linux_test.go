//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/update"
)

// TestGuardUnderRealSystemd starts the RENDERED unit (hardening included, ExecStart replaced by /bin/true) three times
// with a pending update marker and checks that the ExecStartPre guard, after systemd's own $ / quote parsing and inside the
// sandbox, restores the previous binary on the third start. It needs root and a running systemd, so it is opt-in:
//
//	MISTGATE_SYSTEMD_TESTS=1 go test -run GuardUnderRealSystemd ./cmd/mistgate-node   (as root, in WSL or a disposable VM)
//
// Everything it creates is named mgu-* and lives in /run (a transient unit file, removed at the end).
func TestGuardUnderRealSystemd(t *testing.T) {
	if os.Getenv("MISTGATE_SYSTEMD_TESTS") == "" {
		t.Skip("set MISTGATE_SYSTEMD_TESTS=1 to run against the real systemd (root only)")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if out, _ := exec.Command("systemctl", "is-system-running").Output(); !strings.Contains(string(out), "running") && !strings.Contains(string(out), "degraded") {
		t.Skip("systemd is not running")
	}
	for _, d := range []string{"/etc/sysctl.d", "/etc/systemd/journald.conf.d", "/run/systemd/system"} {
		if _, err := os.Stat(d); err != nil {
			t.Skipf("%s is missing (ReadWritePaths would fail the unit)", d)
		}
	}
	root, err := os.MkdirTemp("/run", "mgu-guard-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	binDir, state := filepath.Join(root, "bin"), filepath.Join(root, "state")
	for _, d := range []string{binDir, state} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(binDir, "mistgate-node")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(bin, "NEW")
	write(bin+".prev", "OLD")
	write(filepath.Join(state, update.FilePending), `{"from_version":"a","from_built":1,"to_version":"b","to_built":2}`)

	u, err := renderUnit(bin, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(u, "\n") {
		switch {
		case strings.HasPrefix(l, "ExecStart="):
			l = "ExecStart=/bin/true"
		case strings.HasPrefix(l, "Type="):
			l = "Type=oneshot"
		case l == "WantedBy=multi-user.target":
			continue
		}
		lines = append(lines, l)
	}
	name := fmt.Sprintf("mgu-guard-%d.service", os.Getpid())
	unitPath := filepath.Join("/run/systemd/system", name)
	write(unitPath, strings.Join(lines, "\n"))
	defer func() {
		_ = os.Remove(unitPath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}()
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		t.Fatalf("daemon-reload: %v: %s", err, out)
	}
	start := func(n int) {
		if out, err := exec.Command("systemctl", "start", name).CombinedOutput(); err != nil {
			j, _ := exec.Command("journalctl", "-u", name, "--no-pager", "-n", "20").CombinedOutput()
			t.Fatalf("start %d: %v: %s\n%s", n, err, out, j)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }

	start(1)
	start(2)
	if read(bin) != "NEW" || read(filepath.Join(state, update.FileStarts)) != "2" {
		t.Fatalf("after two starts: bin=%q counter=%q", read(bin), read(filepath.Join(state, update.FileStarts)))
	}
	start(3)
	if read(bin) != "OLD" {
		j, _ := exec.Command("journalctl", "-u", name, "--no-pager", "-n", "20").CombinedOutput()
		t.Fatalf("third start did not restore the previous binary: bin=%q\n%s", read(bin), j)
	}
	if read(filepath.Join(state, update.FileRolledBackWhy)) != "crash_loop" {
		t.Errorf("reason = %q", read(filepath.Join(state, update.FileRolledBackWhy)))
	}
}
