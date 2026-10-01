package awgprep

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgnl"
)

// UnitName is the transient systemd unit the automatic run lives in: `journalctl -u mistgate-awg-prepare` is where its
// full output is. systemd refuses a second unit of the same name while one runs, which is one more guard against two runs.
const UnitName = "mistgate-awg-prepare"

// ReadEnv reads what the plan depends on from this machine. Everything is best effort: an unreadable fact makes the plan
// refuse (an unknown kernel, no recipe), never guess.
func ReadEnv(ctx context.Context) Env {
	e := Env{CPUs: runtime.NumCPU()}
	if f, err := os.Open("/etc/os-release"); err == nil {
		e.OSRelease = ParseOSRelease(f)
		f.Close()
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		e.Kernel = strings.TrimSpace(string(b))
	}
	// systemd-detect-virt prints "none" and exits 1 outside a container, so the output counts, not the exit code.
	out, _ := exec.CommandContext(ctx, "systemd-detect-virt", "--container").Output()
	if c := strings.TrimSpace(string(out)); c != "" && c != "none" {
		e.Container = c
	} else if _, err := os.Stat("/proc/vz"); err == nil {
		if _, err := os.Stat("/proc/bc"); err != nil { // inside an OpenVZ container, not on the host node
			e.Container = "openvz"
		}
	}
	if out, err := exec.CommandContext(ctx, "mokutil", "--sb-state").CombinedOutput(); err == nil {
		e.SecureBoot = strings.Contains(strings.ToLower(string(out)), "secureboot enabled")
	} else if m, _ := filepath.Glob("/sys/firmware/efi/efivars/SecureBoot-*"); len(m) == 1 {
		if b, err := os.ReadFile(m[0]); err == nil && len(b) > 0 {
			e.SecureBoot = b[len(b)-1] == 1
		}
	}
	if _, err := exec.LookPath("systemd-run"); err == nil {
		_, err := os.Stat("/run/systemd/system")
		e.Systemd = err == nil
	}
	return e
}

// ExecRunner is the real Runner: the command's output goes to w and is returned. apt never asks (DEBIAN_FRONTEND), the
// messages are in English (the lock detection reads them) and needrestart only lists what it would restart: a package
// install must not restart the services of a production node behind the owner's back.
func ExecRunner(ctx context.Context, w io.Writer, dir string, cmd ...string) (string, error) {
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C", "NEEDRESTART_MODE=l", "NEEDRESTART_SUSPEND=1")
	if os.Getenv("HOME") == "" { // a transient unit of root has none; gpg, git and launchpadlib want one
		c.Env = append(c.Env, "HOME=/root")
	}
	var buf bytes.Buffer
	c.Stdout, c.Stderr = io.MultiWriter(w, &buf), io.MultiWriter(w, &buf)
	c.WaitDelay = 10 * time.Second // a child that ignores the kill must not hold the job past its deadline
	err := c.Run()
	return buf.String(), err
}

// RealSys is the real machine behind Run, Verify and WaitAptLock.
func RealSys() Sys {
	return Sys{
		WriteFile: func(path, body string) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(body), 0o644)
		},
		Exists:  func(p string) bool { _, err := os.Stat(p); return err == nil },
		Probe:   probeModule,
		AptBusy: aptBusy,
	}
}

func probeModule() (uint32, uint32, error) {
	cl, err := awgnl.Dial()
	if err != nil {
		return 0, 0, err
	}
	defer cl.Close()
	return uint32(cl.Info.GenlVersion), cl.Info.MaxAttr, nil
}

// ModuleReady says whether the module is loaded and speaks AWG 3.1: what the engine needs to take the kernel backend.
func ModuleReady() bool {
	cl, err := awgnl.Dial()
	if err != nil {
		return false
	}
	defer cl.Close()
	return cl.Info.Is31()
}

// SystemdRun launches the job as a transient systemd unit: root, outside the agent's sandbox and its cgroup limits (the
// agent's unit has ProtectSystem=strict, a tiny capability set and MemoryMax; a package install and a compiler need none
// of those restrictions removed from the agent, they need to not be the agent). The unit also outlives an agent restart.
// RuntimeMaxSec is the backstop behind the job's own timeout. systemd-run returns once the unit started; how the job
// ends is in its status file.
func SystemdRun(exe, statusPath string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		args := []string{"--quiet", "--collect", "--no-ask-password", "--unit=" + UnitName,
			"--description=Mistgate: build and load the AmneziaWG kernel module",
			fmt.Sprintf("--property=RuntimeMaxSec=%d", int((JobTimeout+2*time.Minute)/time.Second)),
			"--", exe, "awg", "prepare-kernel", "--yes", "--status-file", statusPath, "--timeout", JobTimeout.String()}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, "systemd-run", args...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("systemd-run: %s", msg)
		}
		return nil
	}
}
