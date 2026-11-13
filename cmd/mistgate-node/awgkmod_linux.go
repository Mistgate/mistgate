//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

func newAwgKmod(stateDir string) *awgprep.KmodController {
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
	if err != nil || !safePath(stateDir) || stateDir == "/" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil || !safePath(exe) || filepath.Dir(exe) == "/" {
		return nil
	}
	status := filepath.Join(stateDir, awgprep.KmodStatusFileName)
	if !safePath(status) {
		return nil
	}
	needBoot := func() bool {
		osRelease, err := os.ReadFile("/etc/os-release")
		if err != nil || awgprep.ParseOSRelease(strings.NewReader(string(osRelease)))["ID"] != "ubuntu" {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		installed, err := awgprep.KmodDKMSInstalled(ctx, awgprep.ExecRunner)
		if err != nil || !installed {
			return false
		}
		if _, err := os.Stat("/sys/module/amneziawg"); err != nil {
			return true
		}
		body, err := awgprep.KmodHookBody(exe, stateDir)
		if err != nil {
			return false
		}
		current, err := os.ReadFile(awgprep.KmodHookPath)
		return err != nil || string(current) != body
	}
	return awgprep.NewKmodController(status, needBoot, awgprep.KmodSystemdRun(exe, status, awgprep.KmodBoot))
}
