package awgprep

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

func KmodSystemdRun(exe, statusPath string, mode KmodMode) func(context.Context) error {
	return func(ctx context.Context) error {
		args, err := KmodSystemdArgs(exe, statusPath, mode)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "systemd-run", args...).Run()
	}
}

func KmodSystemdArgs(exe, statusPath string, mode KmodMode) ([]string, error) {
	if !SafePath(exe) || !SafePath(statusPath) || filepath.Dir(exe) == "/" ||
		mode != KmodBoot && mode != KmodRepair {
		return nil, fmt.Errorf("refusing unsafe AmneziaWG module unit arguments")
	}
	limit := KmodRuntimeMaxSec
	if mode == KmodRepair {
		limit = KmodUnitMaxSec
	}
	args := []string{"--quiet", "--collect", "--no-ask-password", "--unit=" + KmodUnitName,
		fmt.Sprintf("--property=RuntimeMaxSec=%d", limit), "--", exe, "awg", "kmod-sync", "--" + string(mode), "--status-file", statusPath}
	return args, nil
}
