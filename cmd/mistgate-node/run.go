package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/node/agent"
	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/update"
)

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory")
	linkURL := fs.String("link-url", envOr("MISTGATE_LINK_URL", ""), "optional signed WebSocket link URL (wss://host/<secret-prefix>)")
	level := fs.String("log-level", envOr("MISTGATE_LOG_LEVEL", "info"), "debug, info, warn or error")
	format := fs.String("log-format", envOr("MISTGATE_LOG_FORMAT", "text"), "text or json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(*level)); err != nil {
		fmt.Fprintln(os.Stderr, "run: bad --log-level:", err)
		return 2
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts) // journald adds the timestamp when the unit runs
	if *format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	log := slog.New(h)

	a, err := newAgent(*stateDir, *linkURL, log)
	if errors.Is(err, agent.ErrNotEnrolled) {
		log.Error(err.Error())
		return exitNotEnrolled
	}
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("mistgate-node starting", "state_dir", *stateDir)
	switch err := a.Run(ctx); {
	case errors.Is(err, agent.ErrRetired):
		log.Warn("node retired; exiting")
		return 0 // nothing left to run; Restart=on-failure does not restart a clean exit
	case err != nil:
		log.Error("agent stopped", "err", err)
		return 1
	}
	return 0
}

// newUpdater builds the self-update machinery from what the build stamped in. A build
// without a release key gets an updater that refuses everything ("unsigned build: update by hand"); a key that is
// stamped but broken is logged and treated the same way, never as a reason not to start.
func newUpdater(stateDir string, log *slog.Logger) *update.Updater {
	key, err := buildinfo.ReleasePublicKey()
	if err != nil && !errors.Is(err, buildinfo.ErrUnsignedBuild) {
		log.Error("release key in this build is unusable; self-update is off", "err", err)
	}
	gen := unitGen()
	if key != nil && gen < 2 {
		log.Info("self-update is available only after `mistgate-node install` refreshes the systemd unit", "unit_gen", gen)
	}
	return update.New(update.Config{
		StateDir: stateDir, Version: buildinfo.Version, Built: buildinfo.BuiltUnix(), Key: key, UnitGen: gen,
		Exec: syscall.Exec, Log: log.With("source", "update"),
	})
}

// unitGen is the generation of the systemd unit this process runs from (MISTGATE_UNIT_GEN, written by "install");
// 0 = started by hand or by a unit that predates the variable.
func unitGen() int {
	n, _ := strconv.Atoi(os.Getenv("MISTGATE_UNIT_GEN"))
	return n
}

// newAwgPrepare is the agent's way to build the AmneziaWG kernel module when the panel asks (agent.proto "AWG AND WARP"):
// it decides and follows, and starts `<this binary> awg prepare-kernel --yes` as its own transient systemd unit, because
// the agent's unit may not install packages (and must not be loosened for it). Linux only; elsewhere there is no module.
func newAwgPrepare(stateDir string) *awgprep.Controller {
	exe, err := os.Executable()
	if err != nil || runtime.GOOS != "linux" {
		return nil
	}
	status := filepath.Join(stateDir, "awg-prepare.json")
	return awgprep.New(awgprep.Config{
		StatusPath: status, Env: awgprep.ReadEnv, Ready: awgprep.ModuleReady, Launch: awgprep.SystemdRun(exe, status),
	})
}

// newAgent wires the agent to the real host and the engines of this build (see wire.go).
func newAgent(stateDir, linkURL string, log *slog.Logger) (*agent.Agent, error) {
	hooks := &agentHooks{}
	cfg, engines, err := wire(stateDir, log, hooks)
	if err != nil {
		return nil, err
	}
	cfg.Log = log
	cfg.LinkURL = linkURL
	cfg.Built = buildinfo.BuiltUnix()
	cfg.Updater = newUpdater(stateDir, log)
	cfg.UnitGen = unitGen()
	cfg.AwgPrepare = newAwgPrepare(stateDir)
	a, err := agent.New(cfg, engines, hostctl.New(log))
	if err != nil {
		return nil, err
	}
	// The engines and the WARP manager were built before the agent existed; from here on they can ask it.
	hooks.dns, hooks.awgMode, hooks.warpEmit, hooks.noClientV6 = a.DNS, a.AwgBackend, a.WarpEvent, a.ClientIPv6Disabled
	return a, nil
}
