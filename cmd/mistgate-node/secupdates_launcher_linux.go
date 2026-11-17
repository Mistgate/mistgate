//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/node/nftinstall"
	"github.com/mistgate/mistgate/internal/node/secupdates"
)

func newSecurityUpdatesLauncher(stateDir string) func(context.Context) error {
	return hostJobLauncher(stateDir, secupdates.StatusFileName, secupdates.SystemdRun)
}

func newNftInstallLauncher(stateDir string) func(context.Context) error {
	return hostJobLauncher(stateDir, nftinstall.StatusFileName, nftinstall.SystemdRun)
}

// hostJobLauncher is what the doctor fixes that install packages share: a root agent under systemd starts the job in its
// own transient unit, outside its service sandbox; nil on any other host, where the fix is not offered.
func hostJobLauncher(stateDir, statusFile string, start func(exe, statusPath string) func(context.Context) error) func(context.Context) error {
	if os.Geteuid() != 0 {
		return nil
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return nil
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return nil
	}
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return start(exe, filepath.Join(stateDir, statusFile))
}
