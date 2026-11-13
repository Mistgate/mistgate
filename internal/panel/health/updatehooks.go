package health

import (
	"context"
	"sync"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The two small hooks of the updates module (internal/panel/update): a
// condition source, so a paused rollout raises UPDATE_FAILED under the same "alerts are a function of the current
// state" rule as every other alert, and a per-node "checked since" for the rollout gate.

const (
	kUpdateFailed   = "update_failed"
	kOsUpdateFailed = "os_update_failed" // an OS update of one node stopped (internal/panel/update, os_rollout.go)
)

// The scheduled tasks' alerts (internal/panel/tasks, the VPS edition): a run that found a problem, and a task that keeps failing.
const (
	kScheduledCheck   = "scheduled_check"
	kScheduledFailing = "scheduled_task_failing"
)

// ExtCond is a condition another module holds right now. Only kinds this module knows are accepted (update_failed, os_update_failed, and the
// scheduled tasks' scheduled_check and scheduled_task_failing); the rest is dropped. Why is the i18n key of the explanation, Severity one of 1 info, 2 warning, 3 critical.
type ExtCond struct {
	Kind, NodeID, Subject, Why string
	Severity                   int
	Params                     map[string]string
}

var extKinds = map[string]bool{kUpdateFailed: true, kOsUpdateFailed: true, kScheduledCheck: true, kScheduledFailing: true}

type condSources struct {
	mu  sync.Mutex
	src []func(ctx context.Context) []ExtCond
}

// AddConditionSource registers a function the evaluator calls on every pass; the alerts it describes open while it
// returns them and resolve (cleared) when it stops. The function must be cheap and must not call back into Service.
func (s *Service) AddConditionSource(f func(ctx context.Context) []ExtCond) {
	s.ext.mu.Lock()
	s.ext.src = append(s.ext.src, f)
	s.ext.mu.Unlock()
}

func (s *Service) extConds(ctx context.Context) []ExtCond {
	s.ext.mu.Lock()
	src := append([]func(context.Context) []ExtCond(nil), s.ext.src...)
	s.ext.mu.Unlock()
	var out []ExtCond
	for _, f := range src {
		for _, c := range f(ctx) {
			if extKinds[c.Kind] && c.Severity >= sevInfo && c.Severity <= sevCritical {
				out = append(out, c)
			}
		}
	}
	return out
}

// NodeChecksSince counts the inbounds of the node that the synthetic checker can probe right now (no skip reason)
// and how many of them have a round that ended at or after since: ok = the newest round is OK, failed = FAILED or
// DEGRADED. An inbound without such a round is neither. timeoutsOnly: there is a failed inbound and every FAILED or
// DEGRADED round since `since` of the node's inbounds ended in the error code "timeout" (not refused, auth, tls, exit_unreachable:
// those are the node's own faults, not a lossy path). failedFor is how long the failure has lasted: the shortest, over the
// failed inbounds, span from the first to the last round of their current failed run.
func (s *Service) NodeChecksSince(ctx context.Context, nodeID string, since time.Time) (probeable, ok, failed int, timeoutsOnly bool, failedFor time.Duration, err error) {
	probeable, ok, failed, timeoutsOnly, failedFor, _, err = s.checksSince(ctx, nodeID, since)
	return probeable, ok, failed, timeoutsOnly, failedFor, err
}

// NodeChecksPending lists the inbounds NodeChecksSince does not count as ok: probeable, and without an OK round that ended at
// or after since. The OS update shows their profiles while it waits for the node to settle.
func (s *Service) NodeChecksPending(ctx context.Context, nodeID string, since time.Time) ([]string, error) {
	_, _, _, _, _, pending, err := s.checksSince(ctx, nodeID, since)
	return pending, err
}

func (s *Service) checksSince(ctx context.Context, nodeID string, since time.Time) (probeable, ok, failed int, timeoutsOnly bool, failedFor time.Duration, pending []string, err error) {
	sn, err := s.snapshot(ctx)
	if err != nil {
		return 0, 0, 0, false, 0, nil, err
	}
	live, err := s.liveNode(ctx, nodeID)
	if err != nil {
		return 0, 0, 0, false, 0, nil, err
	}
	timeoutsOnly = true
	for _, t := range sn.byNode[nodeID] {
		if s.skipReason(t, live) != "" {
			continue
		}
		probeable++
		c := s.cellOf(ctx, t.in.ID)
		if c.last == nil || c.last.At.Before(since) {
			pending = append(pending, t.in.ID)
			continue
		}
		if c.last.Status == adminv1.CheckStatus_CHECK_STATUS_OK {
			ok++
			continue
		}
		failed++
		pending = append(pending, t.in.ID)
		only, span := s.failedRun(ctx, t.in.ID, since)
		timeoutsOnly = timeoutsOnly && only
		if failed == 1 || span < failedFor {
			failedFor = span
		}
	}
	return probeable, ok, failed, timeoutsOnly && failed > 0, failedFor, pending, nil
}

// failedRun reads the stored rounds of an inbound since `since` (newest first): only = every non-OK round among them is a
// timeout; span = from the first to the last round of the run of non-OK rounds that ends the list.
func (s *Service) failedRun(ctx context.Context, inboundID string, since time.Time) (only bool, span time.Duration) {
	rows, err := s.st.RecentSamples(ctx, inboundID, recentSamples)
	if err != nil {
		return false, 0
	}
	only, running := true, true
	var newest, oldest time.Time
	for _, r := range rows {
		if r.At.Before(since) {
			break
		}
		if r.Status == int(adminv1.CheckStatus_CHECK_STATUS_OK) {
			running = false
			continue
		}
		only = only && r.ErrorCode == "timeout"
		if running {
			if newest.IsZero() {
				newest = r.At
			}
			oldest = r.At
		}
	}
	return only, newest.Sub(oldest)
}
