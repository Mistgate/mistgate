//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/node/agent"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

const unitDir = "/etc/systemd/system"

func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory (must be the one used by enroll)")
	bin := fs.String("bin", defaultBin, "where the binary lives; this executable is copied there if it is elsewhere")
	noStart := fs.Bool("no-start", false, "write and enable the unit but do not start it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := install(*stateDir, *bin, !*noStart); err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	return 0
}

func install(stateDir, bin string, start bool) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if !agent.IsEnrolled(stateDir) {
		return fmt.Errorf("%s holds no identity: run `mistgate-node enroll` first", stateDir)
	}
	ram := hostctl.New(slog.New(slog.DiscardHandler)).Facts(context.Background()).RAMTotal
	unit, err := renderUnit(bin, stateDir, ram)
	if err != nil {
		return err
	}
	if err := placeBinary(bin); err != nil {
		return err
	}
	// ReadWritePaths entries must exist when the unit starts.
	for _, d := range []string{"/etc/sysctl.d", "/etc/systemd/journald.conf.d"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(unitDir, unitName), []byte(unit), 0o644); err != nil {
		return err
	}
	steps := [][]string{{"daemon-reload"}, {"enable", unitName}}
	if start {
		steps = append(steps, []string{"restart", unitName})
	}
	for _, s := range steps {
		if out, err := exec.Command("systemctl", s...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", s[0], err, out)
		}
	}
	fmt.Printf("installed %s (state %s)\n", filepath.Join(unitDir, unitName), stateDir)
	if start {
		fmt.Println("started; follow it with: journalctl -u mistgate-node -f")
	}
	return nil
}

// placeBinary copies this executable to bin (atomically) unless it already is bin.
func placeBinary(bin string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if dst, err := filepath.EvalSymlinks(bin); err == nil && dst == self {
		return nil
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	tmp := bin + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, bin)
}
