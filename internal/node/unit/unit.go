package unit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Generation is written into the hardened mistgate-node.service unit as MISTGATE_UNIT_GEN.
const Generation = 3

const (
	CapabilityRefresh = "unit-refresh/1"
	ServiceName       = "mistgate-node.service"
	RefreshUnitName   = "mistgate-unit-refresh"
	RefreshAckFile    = "unit-refresh.ack"
)

var ErrBusy = errors.New("busy")
var ErrPreflight = errors.New("unit refresh preflight failed")
var ErrLaunch = errors.New("unit refresh launch failed")

const refreshRuntimeMax = 300 * time.Second

// SystemdRun starts the installed binary in a transient root unit so it can rewrite its own systemd unit outside its
// service sandbox. The fixed unit name makes concurrent refresh attempts fail closed. The helper waits for an ack
// file so the panel's CommandResult is sent before it can restart this service.
func SystemdRun(exe, stateDir string, fromGen int, validate func(context.Context) error) func(context.Context, func()) error {
	return func(ctx context.Context, beforeLaunch func()) error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		run := command
		if err := refreshBusy(cctx, run); err != nil {
			if errors.Is(err, ErrBusy) {
				return err
			}
			return fmt.Errorf("%w: check refresh unit: %v", ErrPreflight, err)
		}
		if validate != nil {
			if err := validate(cctx); err != nil {
				return fmt.Errorf("%w: %v", ErrPreflight, err)
			}
		}
		if err := supportsCollect(cctx, run); err != nil {
			return fmt.Errorf("%w: %v", ErrPreflight, err)
		}
		token, err := newAckToken()
		if err != nil {
			return fmt.Errorf("%w: create refresh ack token: %v", ErrPreflight, err)
		}
		ackPath := filepath.Join(stateDir, RefreshAckFile)
		if err := removeIfExists(ackPath); err != nil {
			return fmt.Errorf("%w: clear refresh ack: %v", ErrPreflight, err)
		}
		if err := systemdRun(cctx, exe, stateDir, fromGen, token, run); err != nil {
			if errors.Is(err, ErrBusy) {
				return err
			}
			return fmt.Errorf("%w: %v", ErrLaunch, err)
		}
		if beforeLaunch != nil {
			beforeLaunch()
		}
		// The transient helper records a failed outcome if this marker cannot be written and its bounded ack wait expires.
		_ = WriteRefreshAck(ackPath, token)
		return nil
	}
}

// RefreshBusy reports whether the fixed transient refresh unit is already loaded. A current-generation agent uses it
// before returning a no-op so a second apply during the installer restart window still reports busy.
func RefreshBusy(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return refreshBusy(cctx, command)
}

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type commandRunner = CommandRunner

func newAckToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func supportsCollect(ctx context.Context, run commandRunner) error {
	out, err := run(ctx, "systemd-run", "--help")
	if err != nil {
		return fmt.Errorf("systemd-run --help: %w", err)
	}
	if !strings.Contains(string(out), "--collect") {
		return errors.New("systemd-run does not support --collect")
	}
	return nil
}

func refreshBusy(ctx context.Context, run commandRunner) error {
	out, err := run(ctx, "systemctl", "show", "--property=LoadState", "--value", RefreshUnitName+".service")
	if err != nil {
		return fmt.Errorf("systemctl show refresh unit: %w", err)
	}
	state := strings.TrimSpace(string(out))
	if state == "" || state == "not-found" {
		return nil
	}
	return ErrBusy
}

func systemdRun(ctx context.Context, exe, stateDir string, fromGen int, token string, run commandRunner) error {
	args := []string{
		"--quiet", "--collect", "--no-ask-password", "--unit=" + RefreshUnitName,
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int(refreshRuntimeMax/time.Second)),
		"--property=TimeoutStopSec=150",
		"--description=Refresh Mistgate node unit", "--", exe, "install",
		"--refresh-unit", "--state-dir", stateDir, "--bin", exe,
		"--from-generation", fmt.Sprint(fromGen), "--ack-token", token,
	}
	out, err := run(ctx, "systemd-run", args...)
	if err == nil {
		return nil
	}
	if systemdUnitExists(out) {
		return ErrBusy
	}
	return fmt.Errorf("systemd-run: %w", err)
}

func systemdUnitExists(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "already exists") || strings.Contains(s, "already loaded") || strings.Contains(s, "unitexists")
}
