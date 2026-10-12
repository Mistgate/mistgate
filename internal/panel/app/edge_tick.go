package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mistgate/mistgate/internal/panel/edition"
)

const edgeTickJobTimeout = 20 * time.Second

var edgeTickJobs = map[string]string{
	"access":    "user status sweep every tick",
	"fleet":     "node-down sweep every tick and desired-state safety net every 10 minutes",
	"health":    "health retention at minute 17",
	"provision": "wakes the Provisioner every tick while an install is queued, running or being cancelled",
}

var edgeDeferred = map[string]string{
	"fleet":          "event-driven Run, local sessions, closeAll and recomputeAll remain VPS-only",
	"health":         "checker/evaluator (phase 2) and periodic UDP port rechecks",
	"updates":        "owner decision 2",
	"backups":        "R2",
	"telegram":       "enqueue remains local and messages are not delivered on edge",
	"mcp-plan-sweep": "MCP is not served on the edge",
}

// edgeDeferredFeatures maps every deferred job above to the admin features it takes away: the ids Me reports in
// "unavailable" (internal/panel/edition). A test keeps the two lists in step, so a job cannot be deferred without the
// admin saying so, nor a feature be listed with nothing behind it.
var edgeDeferredFeatures = map[string][]string{
	"fleet":          {edition.AgentLogs},
	"health":         {edition.HealthChecks},
	"updates":        {edition.AgentUpdates, edition.PanelUpdate},
	"backups":        {edition.Backups},
	"telegram":       {edition.Telegram},
	"mcp-plan-sweep": {edition.MCP},
}

// EdgeTick runs the scheduled work shared by the edge edition. Each pass gets a fresh timeout so one slow job does not
// consume another job's budget; all passes still inherit the invocation deadline.
func (p *Panel) EdgeTick(ctx context.Context, now time.Time) error {
	var errs []error
	run := func(name string, work func(context.Context) error) {
		jobCtx, cancel := context.WithTimeout(ctx, edgeTickJobTimeout)
		defer cancel()
		if err := work(jobCtx); err != nil {
			errs = append(errs, fmt.Errorf("edge tick %s: %w", name, err))
		}
	}

	run("user status sweep", func(ctx context.Context) error {
		_, err := p.Access.Sweep(ctx)
		return err
	})
	run("node-down sweep", func(ctx context.Context) error {
		p.Fleet.Sweep(ctx)
		return nil
	})
	if p.provisionKick != nil {
		// The installs run in the Provisioner Worker; this is the net under its alarm (an evicted object, a redeploy).
		run("provision wake", func(ctx context.Context) error {
			pending, err := p.Store.NodeProvisionJobsPending(ctx)
			if err != nil || !pending {
				return err
			}
			return p.provisionKick(ctx)
		})
	}
	if now.UTC().Minute() == 17 {
		run("health retention", func(ctx context.Context) error {
			p.Health.Retention(ctx)
			return nil
		})
	}
	if now.UTC().Minute()%10 == 0 {
		// StateChanged schedules its fan-out; that work owns its own 20-second context.
		p.Fleet.StateChanged()
	}
	return errors.Join(errs...)
}
