//go:build !js

package tasks

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The assistant's follow-ups (internal/panel/assistant/follow_up.go) run on this scheduler: a one-time continuation of a
// conversation, due at the time the model set (or that long after a plan of the conversation was applied). Each tick arms the
// follow-ups whose plan was applied, takes the due ones (at most followUpsAtOnce at a time), holds each under a lease in its row
// so it runs once, and gives it to assistant.Service.RunFollowUp. A run that ends removes the row (the conversation keeps the
// answer, or a note with the reason); a deferred one (its conversation was busy, the store failed) gets a later time. A
// follow-up a stop of the panel left running is given back by the recovery at start and runs again; one missed while the panel
// was down runs once on start, or is dropped with a note when it is more than a day late (the assistant decides that).

const followUpsAtOnce = 2

// dispatchFollowUps starts the due follow-ups there is room for; the rest wait for the next tick.
func (s *Service) dispatchFollowUps(ctx context.Context) {
	if s.cfg.Assistant == nil {
		return
	}
	if _, err := s.cfg.Store.ArmAssistantFollowUps(ctx); err != nil && ctx.Err() == nil {
		s.cfg.Log.Warn("tasks: arm the follow-ups of applied plans", "code", "follow_up_arm_failed", "err", err)
	}
	now := s.now()
	due, err := s.cfg.Store.DueAssistantFollowUps(ctx, now, 20)
	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Log.Warn("tasks: read due follow-ups", "code", "follow_up_due_failed", "err", err)
		}
		return
	}
	s.mu.Lock()
	base := s.base
	s.mu.Unlock()
	for _, f := range due {
		select {
		case s.followSem <- struct{}{}:
		default:
			return
		}
		lease := store.NewID("lease_")
		ok, err := s.cfg.Store.ClaimAssistantFollowUp(base, f.ID, lease, now, now.Add(assistant.FollowUpRunLimit+time.Minute))
		if err != nil || !ok {
			<-s.followSem
			if err != nil {
				s.cfg.Log.Warn("tasks: claim a follow-up", "code", "follow_up_claim_failed", "follow_up_id", f.ID, "err", err)
			}
			continue
		}
		s.wg.Add(1)
		go s.runFollowUp(base, f, lease)
	}
}

// runFollowUp runs one claimed follow-up and records what came of it.
func (s *Service) runFollowUp(base context.Context, f store.AssistantFollowUp, lease string) {
	defer s.wg.Done()
	defer func() { <-s.followSem }()
	ctx, cancel := context.WithTimeout(base, assistant.FollowUpRunLimit)
	defer cancel()
	var out assistant.FollowUpOutcome
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.cfg.Log.Error("tasks: follow-up panicked", "code", "follow_up_panic", "follow_up_id", f.ID)
				out = assistant.FollowUpOutcome{Status: "failed", Reason: "internal"}
			}
		}()
		out = s.cfg.Assistant.RunFollowUp(ctx, f)
	}()
	if base.Err() != nil {
		// the panel is stopping: the lease stays, and the recovery at the next start gives the follow-up back to run again
		return
	}
	fctx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), finishRetryWindow)
	defer cancelFinish()
	record := func() error {
		if out.Status == "deferred" {
			return s.cfg.Store.DeferAssistantFollowUp(fctx, f.ID, lease, s.now().Add(out.RetryAfter), out.Counted)
		}
		return s.cfg.Store.FinishAssistantFollowUp(fctx, f.ID, lease)
	}
	s.retryStore(fctx, "tasks: record a follow-up", "follow_up_finish_failed", f.ID, record)
	switch out.Status {
	case "failed":
		s.cfg.Log.Warn("tasks: follow-up failed", "code", "follow_up_failed", "follow_up_id", f.ID, "reason", out.Reason)
	case "deferred":
		s.cfg.Log.Info("tasks: follow-up deferred", "follow_up_id", f.ID, "reason", out.Reason, "after", out.RetryAfter.String())
	default:
		s.cfg.Log.Info("tasks: follow-up finished", "follow_up_id", f.ID, "status", out.Status, "reason", out.Reason)
	}
}

// retryStore runs a store write until it works or ctx ends, with a growing wait; the first failure is logged.
func (s *Service) retryStore(ctx context.Context, what, code, id string, write func() error) {
	delay := 100 * time.Millisecond
	logged := false
	for {
		err := write()
		if err == nil {
			return
		}
		if !logged {
			s.cfg.Log.Warn(what, "code", code, "id", id, "err", err)
			logged = true
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}
