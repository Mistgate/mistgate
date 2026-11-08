//go:build !js

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/tasks/spec"
)

// Alert kinds the tasks raise through the health evaluator (health.AddConditionSource).
const (
	AlertProblem = "scheduled_check"
	AlertFailing = "scheduled_task_failing"
)

const (
	summaryRunes  = 300
	matchesPerRun = 5
	// finishRetryWindow gives transient store failures time to clear without blocking shutdown forever.
	finishRetryWindow = time.Minute
)

// Run is the scheduler: it ends the runs a stopped panel left, then starts the due tasks every tick until ctx ends, and waits
// for the runs in progress (they end as interrupted).
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	recoveryPending := true
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		if recoveryPending {
			n, err := s.cfg.Store.RecoverScheduledRuns(ctx, s.now())
			if err == nil && s.cfg.Assistant != nil {
				// follow-ups a stop of the panel left running run again
				var back int64
				if back, err = s.cfg.Store.RecoverAssistantFollowUps(ctx); err == nil && back > 0 {
					s.cfg.Log.Info("tasks: follow-ups interrupted by the last stop", "count", back)
				}
			}
			if err != nil {
				if ctx.Err() == nil {
					s.cfg.Log.Warn("tasks: recover interrupted runs", "code", "scheduled_recover_failed", "err", err)
				}
			} else {
				recoveryPending = false
				if n > 0 {
					s.cfg.Log.Info("tasks: runs interrupted by the last stop", "count", n)
				}
			}
		}
		if !recoveryPending {
			s.dispatch(ctx)
			s.dispatchFollowUps(ctx)
		}
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// dispatch starts the due tasks there is room for; the rest wait for the next tick.
func (s *Service) dispatch(ctx context.Context) {
	now := s.now()
	due, err := s.cfg.Store.DueScheduledTasks(ctx, now, 20)
	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Log.Warn("tasks: read due tasks", "code", "scheduled_due_failed", "err", err)
		}
		return
	}
	for _, t := range due {
		sem := s.runSemaphore(t.Kind)
		select {
		case sem <- struct{}{}:
		default:
			continue
		}
		trigger := "schedule"
		if now.Sub(t.NextRunAt) > missedAfter {
			trigger = "missed"
		}
		release := func() { <-sem }
		if _, started, err := s.start(t, trigger, "", time.Time{}, release); err != nil || !started {
			release()
			if err != nil {
				s.cfg.Log.Warn("tasks: start a run", "code", "scheduled_start_failed", "task_id", t.ID, "err", err)
			}
		}
	}
}

// start reserves cancellation before claiming the lease, so deleting a task can stop a run even while the claim is in flight.
// notBefore, for a manual run, refuses one that comes sooner than the task's manual cooldown; release gives back its slot.
func (s *Service) start(t store.ScheduledTask, trigger, actor string, notBefore time.Time, release func()) (string, bool, error) {
	now := s.now()
	run := store.ScheduledTaskRun{ID: store.NewID("run_"), TaskID: t.ID, Trigger: trigger, Actor: actor, StartedAt: now}
	lease := store.NewID("lease_")
	s.mu.Lock()
	base := s.base
	if _, exists := s.running[t.ID]; exists {
		s.mu.Unlock()
		return "", false, nil
	}
	runCtx, cancel := context.WithCancel(base)
	active := &activeRun{cancel: cancel}
	s.running[t.ID] = active
	s.mu.Unlock()
	ok, err := s.cfg.Store.ClaimScheduledTask(base, run, lease, now, now.Add(limitFor(t.Kind)+time.Minute), notBefore)
	if err != nil || !ok {
		s.clearRun(t.ID, active)
		return "", false, err
	}
	s.wg.Add(1)
	go s.execute(runCtx, t, run, lease, release, active)
	return run.ID, true, nil
}

func (s *Service) clearRun(taskID string, active *activeRun) {
	s.mu.Lock()
	if s.running[taskID] == active {
		delete(s.running, taskID)
	}
	s.mu.Unlock()
	active.cancel()
}

// outcome is what a runner found.
type outcome struct {
	status, reason, summary string
	detail                  runDetail
}

func (s *Service) execute(base context.Context, t store.ScheduledTask, run store.ScheduledTaskRun, lease string, release func(), active *activeRun) {
	defer s.wg.Done()
	if release != nil {
		defer release()
	}
	ctx, cancel := context.WithTimeout(base, limitFor(t.Kind))
	defer func() {
		cancel()
		s.clearRun(t.ID, active)
	}()
	var o outcome
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.cfg.Log.Error("tasks: run panicked", "code", "scheduled_run_panic", "task_id", t.ID)
				o = outcome{status: "failed", reason: "internal"}
			}
		}()
		if t.Kind == spec.KindAssistant {
			o = s.runAssistant(ctx, t)
		} else {
			o = s.runCheck(ctx, t)
		}
	}()
	switch {
	case base.Err() != nil:
		o.status, o.reason = "failed", "interrupted"
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && o.status == "failed" && o.reason != "timeout":
		o.reason = "timeout"
	}
	detail, _ := json.Marshal(o.detail)
	run.Status, run.Reason, run.Summary, run.Detail, run.FinishedAt = o.status, o.reason, clip(o.summary, summaryRunes), string(detail), s.now()
	fctx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), finishRetryWindow)
	defer cancelFinish()
	next := timeZero
	if cur, err := s.cfg.Store.ScheduledTask(fctx, t.ID); err == nil && cur.Enabled {
		// the schedule as it is now: the owner may have changed it during the run
		sc := scheduleOf(cur)
		next = sc.Next(s.now(), s.timezone(fctx), spec.Jitter(cur.ID, sc))
	}
	s.finishRun(fctx, t.ID, run, lease, next)
	if run.Status == "failed" && run.Reason != "interrupted" {
		s.cfg.Log.Warn("tasks: run failed", "code", "scheduled_run_failed", "task_id", t.ID, "reason", run.Reason)
	} else {
		s.cfg.Log.Info("tasks: run finished", "task_id", t.ID, "status", run.Status, "reason", run.Reason, "trigger", run.Trigger)
	}
}

func (s *Service) finishRun(ctx context.Context, taskID string, run store.ScheduledTaskRun, lease string, next time.Time) {
	delay := 100 * time.Millisecond
	logged := false
	for {
		if err := s.cfg.Store.FinishScheduledRun(ctx, run, lease, next); err == nil {
			return
		} else if !logged {
			s.cfg.Log.Warn("tasks: record a run", "code", "scheduled_finish_failed", "task_id", taskID, "err", err)
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

// cancelRun stops the run of a task in progress (the task was deleted).
func (s *Service) cancelRun(taskID string) {
	s.mu.Lock()
	active := s.running[taskID]
	s.mu.Unlock()
	if active != nil {
		active.cancel()
	}
}

func clip(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n-1]) + "…"
}

// runCheck runs a check task's steps in order through MCP and tests their conditions. A step whose tool fails fails the run,
// unless the step counts a failure as a problem; a step whose condition holds found a problem. A problem wins over a failure.
func (s *Service) runCheck(ctx context.Context, t store.ScheduledTask) outcome {
	steps := stepsOf(t)
	if len(steps) == 0 {
		return outcome{status: "failed", reason: "invalid_step"}
	}
	session, err := s.session(ctx, t.CreatedBy)
	if err != nil {
		reason := "tools_unavailable"
		if err.Error() == "not_allowed" {
			reason = "not_allowed"
		}
		return outcome{status: "failed", reason: reason}
	}
	defer session.Close()
	var o outcome
	var problems, failures []string
	for _, st := range steps {
		if ctx.Err() != nil {
			failures = append(failures, st.Tool+": timeout")
			o.detail.Steps = append(o.detail.Steps, stepResult{Tool: st.Tool, Error: "timeout"})
			continue
		}
		started := s.now()
		res, err := session.CallTool(ctx, st.Tool, st.Args)
		sr := stepResult{Tool: st.Tool, DurationMs: uint32(min(max(s.now().Sub(started).Milliseconds(), 0), 1<<31))}
		if err != nil || res.IsError {
			msg := "tool call failed"
			if err == nil && strings.TrimSpace(res.Text) != "" {
				msg = res.Text
			}
			sr.Error = clip(strings.TrimSpace(mcp.ScrubText(msg)), 300)
			if st.ErrorIsProblem {
				sr.Problem = true
				sr.Matches = []string{"error: " + clip(sr.Error, 120)}
				problems = append(problems, st.Tool+": "+clip(sr.Error, 120))
			} else {
				failures = append(failures, st.Tool+": "+clip(sr.Error, 120))
			}
		} else {
			sr.OK = true
			text := mcp.ScrubJSON(res.Text)
			sr.Excerpt = clip(text, excerptRunes)
			for _, c := range st.Conditions {
				left := matchesPerRun - len(sr.Matches)
				if left <= 0 {
					break
				}
				sr.Matches = append(sr.Matches, c.Match([]byte(text), left)...)
			}
			if len(sr.Matches) > 0 {
				sr.Problem = true
				problems = append(problems, st.Tool+": "+strings.Join(sr.Matches, ", "))
			}
		}
		o.detail.Steps = append(o.detail.Steps, sr)
	}
	switch {
	case len(problems) > 0:
		o.status, o.summary = "problem", strings.Join(problems, "; ")
	case len(failures) > 0:
		o.status, o.reason, o.summary = "failed", "tool_failed", strings.Join(failures, "; ")
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			o.reason = "timeout"
		}
	default:
		o.status = "ok"
	}
	return o
}

// runAssistant gives an assistant task's prompt to the assistant.
func (s *Service) runAssistant(ctx context.Context, t store.ScheduledTask) outcome {
	if s.cfg.Assistant == nil {
		return outcome{status: "skipped", reason: "assistant_off"}
	}
	prompt, err := s.openPrompt(t)
	if err != nil {
		return outcome{status: "failed", reason: "internal"}
	}
	out := s.cfg.Assistant.RunScheduled(ctx, assistant.ScheduledRun{TaskID: t.ID, TaskName: t.Name, AdminID: t.CreatedBy, Prompt: prompt, ConversationID: t.ConversationID})
	if out.ConversationID != "" && out.ConversationID != t.ConversationID {
		if err := s.cfg.Store.SetScheduledTaskConversation(context.WithoutCancel(ctx), t.ID, out.ConversationID); err != nil {
			s.cfg.Log.Warn("tasks: remember the conversation", "code", "scheduled_conversation_unsaved", "task_id", t.ID, "err", err)
		}
	}
	return outcome{status: out.Status, reason: out.Reason, summary: out.Summary, detail: runDetail{
		ConversationID: out.ConversationID, Provider: out.Provider, Model: out.Model, USD: out.USD, FellBack: out.FellBack}}
}

// conditions caches the alert conditions: a failed read keeps the last ones, so a busy database does not close the alerts.
type conditions struct {
	mu   sync.Mutex
	last []health.ExtCond
}

// Conditions is the health condition source (health.Service.AddConditionSource): SCHEDULED_CHECK for an enabled task whose last
// finished run found a problem, SCHEDULED_TASK_FAILING for one that failed three runs in a row. Both close when the condition
// goes: a clean run, the task disabled or deleted.
func (s *Service) Conditions(ctx context.Context) []health.ExtCond {
	list, err := s.cfg.Store.ScheduledTaskAlerts(ctx)
	if err != nil {
		s.conds.mu.Lock()
		defer s.conds.mu.Unlock()
		return s.conds.last
	}
	var out []health.ExtCond
	for _, t := range list {
		telegram := "0"
		if t.NotifyTelegram {
			telegram = "1"
		}
		if t.Problem {
			out = append(out, health.ExtCond{Kind: AlertProblem, Subject: t.ID, Severity: 2, Why: "health.alert.scheduled_check.why",
				Params: map[string]string{"task": t.Name, "task_id": t.ID, "task_kind": t.Kind, "summary": clip(t.ProblemSummary, summaryRunes), "telegram": telegram}})
		}
		if t.FailStreak >= store.ScheduledFailingStreak {
			out = append(out, health.ExtCond{Kind: AlertFailing, Subject: t.ID, Severity: 2, Why: "health.alert.scheduled_task_failing.why",
				Params: map[string]string{"task": t.Name, "task_id": t.ID, "task_kind": t.Kind, "count": strconv.Itoa(t.FailStreak), "reason": t.FailReason, "telegram": telegram}})
		}
	}
	s.conds.mu.Lock()
	s.conds.last = out
	s.conds.mu.Unlock()
	return out
}
