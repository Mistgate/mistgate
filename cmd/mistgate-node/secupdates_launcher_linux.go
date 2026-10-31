//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/node/secupdates"
)

func newSecurityUpdatesLauncher(stateDir string) func(context.Context) error {
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
	statusPath := filepath.Join(stateDir, secupdates.StatusFileName)
	return secupdates.SystemdRun(exe, statusPath)
}
