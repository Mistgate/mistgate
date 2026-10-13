package unit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type CommandRunner func(context.Context, string, ...string) ([]byte, error)
type UnitRenderer func(bin, stateDir string, ramBytes uint64) (string, error)

type RefreshConfig struct {
	UnitPath       string
	Binary         string
	StateDir       string
	FromGen        int
	ToGen          int
	RAMBytes       uint64
	Render         UnitRenderer
	Run            CommandRunner
	Now            func() time.Time
	Sleep          func(context.Context, time.Duration)
	WaitTimeout    time.Duration
	CommandTimeout time.Duration
	StableFor      time.Duration
	PollInterval   time.Duration
}

// RefreshInstall replaces the unit atomically, then reloads and restarts the service. A service that does not remain
// active for the stability window is rolled back to the saved unit and restarted again.
func RefreshInstall(ctx context.Context, c RefreshConfig) (RefreshOutcome, error) {
	out := RefreshOutcome{FromGen: c.FromGen, ToGen: c.ToGen, Result: RefreshFailed}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = 60 * time.Second
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = 30 * time.Second
	}
	if c.StableFor <= 0 {
		c.StableFor = 8 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.FromGen < 0 || c.ToGen != Generation || c.FromGen >= c.ToGen {
		return out, errors.New("invalid unit refresh generations")
	}
	if c.Render == nil || c.Run == nil {
		return out, errors.New("unit refresh is not configured")
	}
	rendered, err := c.Render(c.Binary, c.StateDir, c.RAMBytes)
	if err != nil {
		return out, fmt.Errorf("render unit: %w", err)
	}
	changed, err := WriteUnitWithPrevious(c.UnitPath, []byte(rendered))
	if err != nil {
		return out, err
	}
	if !changed {
		return out, errors.New("unit was not replaced")
	}
	if err := systemctl(ctx, c, "daemon-reload"); err != nil {
		return rollbackRefresh(ctx, c, out, fmt.Errorf("daemon-reload: %w", err))
	}
	if err := systemctl(ctx, c, "restart", ServiceName); err != nil {
		return rollbackRefresh(ctx, c, out, fmt.Errorf("restart: %w", err))
	}
	if err := waitActive(ctx, c); err != nil {
		return rollbackRefresh(ctx, c, out, err)
	}
	out.Result = RefreshOK
	return out, nil
}

func rollbackRefresh(ctx context.Context, c RefreshConfig, out RefreshOutcome, cause error) (RefreshOutcome, error) {
	out.Reason = shortReason(cause.Error())
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 150*time.Second)
	defer cancel()
	if err := RestorePreviousUnit(c.UnitPath); err != nil {
		out.Reason = shortReason(cause.Error() + "; restore failed: " + err.Error())
		return out, cause
	}
	if err := systemctl(rollbackCtx, c, "daemon-reload"); err != nil {
		out.Reason = shortReason(cause.Error() + "; rollback daemon-reload failed: " + err.Error())
		return out, cause
	}
	if err := systemctl(rollbackCtx, c, "restart", ServiceName); err != nil {
		out.Reason = shortReason(cause.Error() + "; rollback restart failed: " + err.Error())
		return out, cause
	}
	if err := waitActive(rollbackCtx, c); err != nil {
		out.Reason = shortReason(cause.Error() + "; rollback service did not recover: " + err.Error())
		return out, cause
	}
	out.Result = RefreshRolledBack
	return out, cause
}

func systemctl(ctx context.Context, c RefreshConfig, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, c.CommandTimeout)
	defer cancel()
	if out, err := c.Run(cctx, "systemctl", args...); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" { // "Job for ... failed because the control process exited ..."
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func waitActive(ctx context.Context, c RefreshConfig) error {
	deadline := c.Now().Add(c.WaitTimeout)
	var activeSince time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, c.CommandTimeout)
		out, err := c.Run(cctx, "systemctl", "is-active", ServiceName)
		cancel()
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			if activeSince.IsZero() {
				activeSince = c.Now()
			}
			if c.Now().Sub(activeSince) >= c.StableFor {
				return nil
			}
		} else {
			activeSince = time.Time{}
		}
		remaining := deadline.Sub(c.Now())
		if remaining <= 0 {
			return fmt.Errorf("%s did not stay active for %s within %s", ServiceName, c.StableFor, c.WaitTimeout)
		}
		d := min(c.PollInterval, remaining)
		c.Sleep(ctx, d)
	}
}

func shortReason(s string) string {
	if first, _, ok := strings.Cut(s, "\n"); ok {
		s = first
	}
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}
