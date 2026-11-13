//go:build linux

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

func cmdAwgKmodSync(args []string) int {
	fs := flag.NewFlagSet("awg kmod-sync", flag.ContinueOnError)
	boot := fs.Bool("boot", false, "sync kernels at agent start")
	afterApt := fs.Bool("after-apt", false, "sync after an apt transaction")
	repair := fs.Bool("repair", false, "sync and load the running kernel module")
	patchOnly := fs.Bool("patch-only", false, "repair only a known compat.h before apt")
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "the agent state directory")
	statusFile := fs.String("status-file", "", "write progress and the result here")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	selected := 0
	mode := awgprep.KmodMode("")
	for _, choice := range []struct {
		set  bool
		mode awgprep.KmodMode
	}{{*boot, awgprep.KmodBoot}, {*afterApt, awgprep.KmodAfterApt}, {*repair, awgprep.KmodRepair}, {*patchOnly, awgprep.KmodPatchOnly}} {
		if choice.set {
			selected++
			mode = choice.mode
		}
	}
	if selected != 1 || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: mistgate-node awg kmod-sync (--boot|--after-apt|--repair|--patch-only) [--status-file FILE]")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "awg kmod-sync: must run as root")
		return 1
	}
	absState, err := filepath.Abs(*stateDir)
	if err != nil || !safePath(absState) || absState == "/" {
		fmt.Fprintln(os.Stderr, "awg kmod-sync: unsafe state directory")
		return 2
	}
	*stateDir = absState
	if *statusFile == "" {
		*statusFile = filepath.Join(*stateDir, awgprep.KmodStatusFileName)
	} else {
		absStatus, err := filepath.Abs(*statusFile)
		if err != nil || !safePath(absStatus) {
			fmt.Fprintln(os.Stderr, "awg kmod-sync: unsafe status file path")
			return 2
		}
		*statusFile = absStatus
		*stateDir = filepath.Dir(absStatus)
	}
	exe, err := os.Executable()
	if err != nil || !safePath(exe) {
		fmt.Fprintln(os.Stderr, "awg kmod-sync: cannot determine a safe node binary path")
		return 2
	}
	sys := awgprep.RealSys()
	if mode == awgprep.KmodPatchOnly {
		err := awgprep.RunKmodSync(context.Background(), awgprep.KmodConfig{Mode: mode, Out: io.Discard, Run: awgprep.ExecRunner, Sys: sys})
		if err != nil {
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil || awgprep.ParseOSRelease(bytes.NewReader(osRelease))["ID"] != "ubuntu" {
		_ = sys.RemoveFile(awgprep.KmodHookPath)
		fmt.Println("plain build: run prepare-kernel")
		return 0
	}
	err = awgprep.RunKmodSync(ctx, awgprep.KmodConfig{
		Mode: mode, StatusPath: *statusFile, NodePath: exe, StateDir: *stateDir,
		Out: os.Stdout, Run: awgprep.ExecRunner, Sys: sys,
		SecureBoot: func(ctx context.Context) bool { return awgprep.ReadEnv(ctx).SecureBoot },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "awg kmod-sync:", err)
		return 1
	}
	return 0
}
