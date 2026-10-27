package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	edgeTickJobTimeout = 20 * time.Second
	// A country round creates a measurement per node and port and waits for them together, up to a minute; the Worker's cron
	// invocation allows it, and the round's own deadline (health.Config.CountryRoundTimeout) is the same minute.
	edgeCountryTimeout = 55 * time.Second
	// The block lists download about a megabyte when a check is due (every six hours, or for a node without a result).
	edgeBlocklistTimeout = 30 * time.Second
)

var edgeTickJobs = map[string]string{
	"access":      "user status sweep every tick",
	"fleet":       "node-down sweep every tick and desired-state safety net every 10 minutes",
	"health":      "alert evaluation every tick, health retention at minute 17, and the country check when a round is due",
	"blocklist":   "the block-list check of the country check, when one is due (one setting read while it is off)",
	"cheburcheck": "one due cheburcheck source and node per tick; off means no site request",
	"bsbord":      "one previewed, capped mobile reachability request when a round is due; off means no site request",
	"provision":   "wakes the Provisioner every tick while an install is queued, running or being cancelled",
	"telegram":    "polls pending chat codes, checks the release and agent watcher every fifth tick, and delivers queued messages",
	"updates":     "advances the edge node rollout and scheduled update state machines",
}

// edgeDeferred describes background-job functions that stay VPS-only; a job can also have an edge phase above.
var edgeDeferred = map[string]string{
	"fleet":          "event-driven Run, local sessions, closeAll and recomputeAll remain VPS-only",
	"health":         "client-eye checker and periodic UDP port rechecks",
	"updates":        "installing a panel binary remains an edge deploy operation",
	"backups":        "R2",
	"mcp-plan-sweep": "MCP is not served on the edge",
	"tspu-probe":     "the TSPU probe of the country check: a run takes minutes (a batch of commands to a node in the country), the cron has 15 minutes of wall time and 30 seconds of CPU; Get says it is unavailable and nothing runs",
}

// edgeDeferredFeatures maps every deferred function above to the admin features it takes away: the ids Me reports in
// "unavailable" (internal/panel/edition). A test keeps the two lists in step, so a job cannot be deferred without the
// admin saying so, nor a feature be listed with nothing behind it.
var edgeDeferredFeatures = map[string][]string{
	"fleet":          {edition.AgentLogs},
	"health":         {edition.HealthChecks},
	"updates":        {edition.PanelUpdate},
	"backups":        {edition.Backups},
	"mcp-plan-sweep": {edition.MCP},
	"tspu-probe":     {edition.TSPUProbe},
}

// EdgeTick runs the scheduled work shared by the edge edition. Each pass gets a fresh timeout so one slow job does not
// consume another job's budget; all passes still inherit the invocation deadline.
func (p *Panel) EdgeTick(ctx context.Context, now time.Time) error {
	var errs []error
	runFor := func(name string, timeout time.Duration, work func(context.Context) error) {
		jobCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := work(jobCtx); err != nil {
			errs = append(errs, fmt.Errorf("edge tick %s: %w", name, err))
		}
	}
	run := func(name string, work func(context.Context) error) { runFor(name, edgeTickJobTimeout, work) }

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
	// The node updates go before the health evaluator: a step that fails its gate or a rollout that pauses shows as the
	// update_failed alert, which only the evaluator derives, so it is raised in this tick and not in the next.
	if p.Update != nil {
		run("updates", func(ctx context.Context) error {
			p.Update.EdgeTick(ctx, now)
			return nil
		})
	}
	if p.Health != nil {
		// Nothing happens (no request, no data sent) while the Globalping integration is off, which is its default; the one read
		// is its setting. Switching it off or disconnecting it reconciles the alerts at once (globalping.Config.OnChange).
		cctx, cancel := context.WithTimeout(ctx, edgeCountryTimeout)
		p.Health.CountryTick(cctx) // the evaluator below derives the COUNTRY_BLOCKED alerts from what the round stored
		cancel()
		// The block lists, the second source: off by default, then it costs one read (its setting) and sends nothing. When it is
		// on, a check is due for a node without a result and every six hours; the lists are then downloaded and kept for the
		// life of this isolate only.
		if p.Blocklist != nil {
			bctx, bcancel := context.WithTimeout(ctx, edgeBlocklistTimeout)
			p.Blocklist.Tick(bctx)
			bcancel()
		}
		run("health evaluator", func(ctx context.Context) error {
			p.Health.EvaluateOnce(ctx)
			return nil
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
	if p.Telegram != nil {
		run("telegram watcher", func(ctx context.Context) error {
			p.Telegram.WatchTick(ctx, now)
			return nil
		})
		run("telegram delivery", func(ctx context.Context) error {
			p.Telegram.FlushNow(ctx)
			return nil
		})
	}
	if p.Cheburcheck != nil {
		runFor("cheburcheck", 40*time.Second, func(ctx context.Context) error {
			return p.Cheburcheck.Tick(ctx)
		})
	}
	if p.Bsbord != nil {
		runFor("bsbord reach", 45*time.Second, func(ctx context.Context) error {
			p.Bsbord.Tick(ctx)
			return nil
		})
	}
	if p.Telegram != nil {
		// The link poll is last: it may long-poll for up to 25 s, and it must not delay what is already queued.
		runFor("telegram link poll", 40*time.Second, func(ctx context.Context) error {
			return p.Telegram.PollPendingLinkCode(ctx)
		})
	}
	return errors.Join(errs...)
}

// EdgeRoundLease is health.Config.RoundLease on the Cloudflare edition: a D1 lease with a holder of its own per round, so
// that of two isolates whose cron ticks overlap only one runs it. A failed read counts as taken by someone else.
func EdgeRoundLease(st *store.Store, log *slog.Logger) func(ctx context.Context, name string, ttl time.Duration) bool {
	return func(ctx context.Context, name string, ttl time.Duration) bool {
		taken, err := st.TakeLease(ctx, name, store.NewID("lease_"), ttl, time.Now())
		if err != nil {
			log.Warn("edge: taking a round lease", "lease", name, "err", err)
		}
		return taken
	}
}
