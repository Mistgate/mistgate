//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/unit"
	"github.com/mistgate/mistgate/internal/node/update"
)

const refreshAckTimeout = 15 * time.Second

func installUnitRefresh(stateDir, bin string, fromGen int, token string) (retErr error) {
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	ackPath := filepath.Join(stateDir, unit.RefreshAckFile)
	outcomePath := filepath.Join(stateDir, unit.RefreshOutcomeFile)
	outcome := unit.RefreshOutcome{FromGen: fromGen, ToGen: unit.Generation, Result: unit.RefreshFailed, Reason: "refresh helper did not finish"}
	defer func() {
		outcome.At = time.Now().UTC()
		if err := unit.WriteRefreshOutcome(outcomePath, outcome); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("write unit refresh outcome: %w", err))
		}
		if err := update.ClearUnitRefreshPending(stateDir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clear unit refresh reservation: %w", err))
		}
		_ = os.Remove(ackPath)
	}()

	if fromGen < 0 || fromGen >= unit.Generation || token == "" {
		outcome.Reason = "invalid refresh helper arguments"
		return errors.New(outcome.Reason)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	ackCtx, cancel := context.WithTimeout(ctx, refreshAckTimeout)
	err = unit.WaitRefreshAck(ackCtx, ackPath, token)
	cancel()
	if err != nil {
		outcome.Reason = "agent ack was not received: " + err.Error()
		return fmt.Errorf("wait for refresh ack: %w", err)
	}
	if err := validateRefreshInstall(stateDir, bin); err != nil {
		outcome.Reason = err.Error()
		return err
	}
	for _, dir := range []string{"/etc/sysctl.d", "/etc/systemd/journald.conf.d"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			outcome.Reason = "create unit write path: " + err.Error()
			return err
		}
	}
	ram := hostctl.New(slog.New(slog.DiscardHandler)).Facts(ctx).RAMTotal
	refreshed, err := unit.RefreshInstall(ctx, unit.RefreshConfig{
		UnitPath: filepath.Join(unitDir, unit.ServiceName), Binary: bin, StateDir: stateDir,
		FromGen: fromGen, ToGen: unit.Generation, RAMBytes: ram, Render: renderUnit,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	})
	outcome = refreshed
	if err != nil {
		if outcome.Reason == "" {
			outcome.Reason = err.Error()
		}
		return fmt.Errorf("unit refresh %s: %s", refreshed.Result, outcome.Reason)
	}
	return nil
}
