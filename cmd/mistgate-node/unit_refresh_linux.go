//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/node/unit"
	"github.com/mistgate/mistgate/internal/node/update"
)

func newUnitRefresh(stateDir string, updater *update.Updater) unitRefreshConfig {
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return unitRefreshConfig{reason: "not_systemd"}
	}
	managed, matches := unit.DetectSystemdService(cgroup, unit.ServiceName)
	if !managed {
		return unitRefreshConfig{reason: "not_systemd"}
	}
	if !matches {
		return unitRefreshConfig{reason: "other_unit"}
	}
	result := unitRefreshConfig{underSystemd: true}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return result
	}
	exe, err := os.Executable()
	if err != nil {
		return result
	}
	exe, err = filepath.EvalSymlinks(exe) // match the path the updater resolves for this binary
	if err != nil {
		return result
	}
	validate := func(context.Context) error { return validateRefreshInstall(stateDir, exe) }
	result.busy = func(ctx context.Context) error {
		if updater != nil && updater.RefreshBusy() {
			return unit.ErrBusy
		}
		return unit.RefreshBusy(ctx)
	}
	result.reserve = func(context.Context) (func(bool), error) {
		if updater == nil {
			return func(bool) {}, nil
		}
		release, err := updater.ReserveUnitRefresh()
		if errors.Is(err, update.ErrRefreshBusy) {
			return nil, unit.ErrBusy
		}
		return release, err
	}
	result.launch = unit.SystemdRun(exe, stateDir, unitGen(), validate)
	return result
}
