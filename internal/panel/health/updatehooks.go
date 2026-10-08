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

const kUpdateFailed = "update_failed"

// ExtCond is a condition another module holds right now. Only kinds this module knows are accepted (update_failed
// today); the rest is dropped. Why is the i18n key of the explanation, Severity one of 1 info, 2 warning, 3 critical.
type ExtCond struct {
	Kind, NodeID, Subject, Why string
	Severity                   int
	Params                     map[string]string
}

var extKinds = map[string]bool{kUpdateFailed: true}

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
// DEGRADED. An inbound without such a round is neither.
func (s *Service) NodeChecksSince(ctx context.Context, nodeID string, since time.Time) (probeable, ok, failed int, err error) {
	sn, err := s.snapshot(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	live, err := s.liveNode(ctx, nodeID)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, t := range sn.byNode[nodeID] {
		if s.skipReason(t, live) != "" {
			continue
		}
		probeable++
		c := s.cellOf(ctx, t.in.ID)
		if c.last == nil || c.last.At.Before(since) {
			continue
		}
		if c.last.Status == adminv1.CheckStatus_CHECK_STATUS_OK {
			ok++
		} else {
			failed++
		}
	}
	return probeable, ok, failed, nil
}
