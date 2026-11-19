package update

import (
	"sync"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// How the agent rollout and the OS rollout pause for a step, in every way a step can end. A step that fails ends it and
// pauses the rollout; the owner's pause gives way to a failure; a pass that did not win the write of its step, or whose
// rollout changed under it, must leave the rollout as it found it.

// afterNextLoad runs q once, after the next pass has read its rows and before it writes any: what the owner or another
// writer does in the middle of a pass.
func (e *env) afterNextLoad(q string, args ...any) {
	e.t.Helper()
	var once sync.Once
	e.fl.mu.Lock()
	e.fl.afterLive = func(*fakeFleet) { once.Do(func() { e.exec(q, args...) }) }
	e.fl.mu.Unlock()
}

// notBackNode sends the step of the canary (its command is acknowledged; a second node waits in the next stage) and moves
// the clock to where the canary is overdue: the next pass ends the step as not reconnected, which pauses the rollout.
func notBackNode(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("de1", nodeOpts{})
	e.addNode("nl1", nodeOpts{online: 1})
	e.startRollout()
	e.tick()
	if x := e.step(a); x.State != store.StepSent || x.AckedAt.IsZero() {
		t.Fatalf("step after the send: %+v", x)
	}
	e.clk.Advance(e.s.cfg.ReconnectWait + time.Second)
	return e, a
}

// jobOverdueNode is the same for the OS rollout: the job was sent and acknowledged, and shows nothing past its wait.
func jobOverdueNode(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	a, b := e.osNode("de1"), e.osNode("nl1")
	if _, err := e.s.startOs(e.ctx, []string{a, b}, store.OsModeSecurity, store.OsRebootIfRequired); err != nil {
		t.Fatal(err)
	}
	e.osTick()
	if x := e.osStep(b); x.State == store.OsStepSent { // the canary is whichever the rollout put first
		a = b
	}
	if x := e.osStep(a); x.State != store.OsStepSent || x.AckedAt.IsZero() {
		t.Fatalf("OS step after the send: %+v", x)
	}
	e.clk.Advance(e.s.cfg.OsJobWait + time.Second)
	return e, a
}

func TestAgentRolloutPauseOutcomes(t *testing.T) {
	t.Run("the step passes: no pause", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("de1", nodeOpts{})
		e.startRollout()
		e.tick()
		e.upgrade(a)
		e.tick()
		e.commit(a)
		e.tick()
		e.wantStep(a, store.StepPassed, "")
		e.wantRollout(store.RolloutDone, "")
	})

	t.Run("the node refuses: the step fails and the rollout pauses", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("de1", nodeOpts{})
		e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
			return &agentv1.CommandResult{Ok: false, Error: "hash_mismatch"}, nil
		}
		e.startRollout()
		e.tick()
		e.wantStep(a, store.StepFailed, "hash_mismatch")
		ro := e.wantRollout(store.RolloutPaused, pauseStepFailed)
		if ro.PauseParams["node_id"] != a || ro.PauseParams["reason"] != "hash_mismatch" {
			t.Fatalf("pause params %v", ro.PauseParams)
		}
	})

	t.Run("the node never reconnects: the step fails and the rollout pauses", func(t *testing.T) {
		e, a := notBackNode(t)
		e.tick()
		e.wantStep(a, store.StepFailed, errNotReconnected)
		e.wantRollout(store.RolloutPaused, pauseGateFailed)
	})

	t.Run("the owner paused before the pass: the failure takes the pause over", func(t *testing.T) {
		e, a := notBackNode(t)
		if _, err := e.s.pauseByOwner(e.ctx, ""); err != nil {
			t.Fatal(err)
		}
		e.tick()
		e.wantStep(a, store.StepFailed, errNotReconnected)
		e.wantRollout(store.RolloutPaused, pauseGateFailed)
	})

	t.Run("the step was closed under the pass: it loses its write and does not pause", func(t *testing.T) {
		e, a := notBackNode(t)
		e.afterNextLoad(`UPDATE update_step SET state = 'skipped', error_key = 'cancelled' WHERE node_id = ?`, a)
		e.tick()
		e.wantStep(a, store.StepSkipped, "cancelled")
		e.wantRollout(store.RolloutRunning, "")
	})

	t.Run("the owner pauses in the middle of a pass: the pause stays the owner's", func(t *testing.T) {
		e, a := notBackNode(t)
		e.afterNextLoad(`UPDATE update_rollout SET status = 'paused', pause_key = 'owner' WHERE id = ?`, e.rollout().ID)
		e.tick()
		e.wantStep(a, store.StepFailed, errNotReconnected)
		e.wantRollout(store.RolloutPaused, pauseOwner)
	})

	t.Run("another failure paused the rollout in the middle of a pass: the first reason stays", func(t *testing.T) {
		e, a := notBackNode(t)
		e.afterNextLoad(`UPDATE update_rollout SET status = 'paused', pause_key = 'step_failed',
			pause_params_json = '{"node":"nl1","reason":"hash_mismatch"}' WHERE id = ?`, e.rollout().ID)
		e.tick()
		e.wantStep(a, store.StepFailed, errNotReconnected)
		ro := e.wantRollout(store.RolloutPaused, pauseStepFailed)
		if ro.PauseParams["reason"] != "hash_mismatch" {
			t.Fatalf("pause params %v", ro.PauseParams)
		}
	})

	t.Run("cancel with the step in flight: the step ends it and the rollout fails", func(t *testing.T) {
		e, a := notBackNode(t)
		if _, err := e.s.cancel(e.ctx, ""); err != nil {
			t.Fatal(err)
		}
		e.wantRollout(store.RolloutRunning, "")
		e.tick()
		e.wantStep(a, store.StepFailed, errNotReconnected)
		e.wantRollout(store.RolloutFailed, "")
	})
}

func TestOsRolloutPauseOutcomes(t *testing.T) {
	t.Run("the node refuses: the step fails and the rollout pauses", func(t *testing.T) {
		e := newEnv(t)
		a := e.osNode("de1")
		e.fl.onOs = func(string, OsJobRequest) (*agentv1.CommandResult, error) {
			return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": osupdate.CodeDiskFull}}, nil
		}
		e.startOs(a, store.OsModeSecurity, store.OsRebootIfRequired)
		e.osTick()
		e.wantOsStep(a, store.OsStepFailed, osErrAgentRefused)
		ro := e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
		if ro.PauseParams["node_id"] != a || ro.PauseParams["reason"] != osErrAgentRefused {
			t.Fatalf("pause params %v", ro.PauseParams)
		}
	})

	t.Run("the job shows nothing: the step fails and the rollout pauses", func(t *testing.T) {
		e, a := jobOverdueNode(t)
		e.osTick()
		e.wantOsStep(a, store.OsStepFailed, errNoAnswer)
		e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	})

	t.Run("the owner paused before the pass: the failure takes the pause over", func(t *testing.T) {
		e, a := jobOverdueNode(t)
		if _, err := e.s.pauseOs(e.ctx, ""); err != nil {
			t.Fatal(err)
		}
		e.osTick()
		e.wantOsStep(a, store.OsStepFailed, errNoAnswer)
		e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	})

	t.Run("the step was closed under the pass: it loses its write and does not pause", func(t *testing.T) {
		e, a := jobOverdueNode(t)
		e.afterNextLoad(`UPDATE os_step SET state = 'skipped', error_key = 'cancelled' WHERE node_id = ?`, a)
		e.osTick()
		e.wantOsStep(a, store.OsStepSkipped, "cancelled")
		e.wantOsRollout(store.RolloutRunning, "")
	})

	t.Run("the owner pauses in the middle of a pass: the pause stays the owner's", func(t *testing.T) {
		e, a := jobOverdueNode(t)
		e.afterNextLoad(`UPDATE os_rollout SET status = 'paused', pause_key = 'owner' WHERE id = ?`, e.osRollout().ID)
		e.osTick()
		e.wantOsStep(a, store.OsStepFailed, errNoAnswer)
		e.wantOsRollout(store.RolloutPaused, pauseOwner)
	})

	t.Run("another failure paused the rollout in the middle of a pass: the first reason stays", func(t *testing.T) {
		e, a := jobOverdueNode(t)
		e.afterNextLoad(`UPDATE os_rollout SET status = 'paused', pause_key = 'step_failed',
			pause_params_json = '{"node":"nl1","reason":"agent_refused"}' WHERE id = ?`, e.osRollout().ID)
		e.osTick()
		e.wantOsStep(a, store.OsStepFailed, errNoAnswer)
		ro := e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
		if ro.PauseParams["reason"] != "agent_refused" {
			t.Fatalf("pause params %v", ro.PauseParams)
		}
	})
}

// The edge pass has no lock between passes: another pass or the owner can change the rollout while one runs.
func TestEdgeRolloutPauseOutcomes(t *testing.T) {
	overdue := func(t *testing.T) (*env, string) {
		t.Helper()
		e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		a, b := edgeNode(e, "de1", 0), edgeNode(e, "nl1", 0)
		startEdgeRollout(e, 0, a, b)
		edgeTick(e)
		if e.step(b).State == store.StepSent { // the canary is whichever the rollout put first
			a = b
		}
		e.wantStep(a, store.StepSent, "")
		e.clk.Advance(e.s.cfg.AnswerWait + e.s.cfg.ReconnectWait + time.Second)
		return e, a
	}
	t.Run("the step fails: the rollout pauses", func(t *testing.T) {
		e, a := overdue(t)
		edgeTick(e)
		e.wantStep(a, store.StepFailed, errNoAnswer)
		e.wantRollout(store.RolloutPaused, pauseGateFailed)
	})
	t.Run("the step was closed under the pass: it loses its write and does not pause", func(t *testing.T) {
		e, a := overdue(t)
		e.afterNextLoad(`UPDATE update_step SET state = 'skipped', error_key = 'cancelled' WHERE node_id = ?`, a)
		edgeTick(e)
		e.wantStep(a, store.StepSkipped, "cancelled")
		e.wantRollout(store.RolloutRunning, "")
	})
	t.Run("the owner pauses in the middle of a pass: the pause stays the owner's", func(t *testing.T) {
		e, a := overdue(t)
		e.afterNextLoad(`UPDATE update_rollout SET status = 'paused', pause_key = 'owner' WHERE id = ?`, e.rollout().ID)
		edgeTick(e)
		e.wantStep(a, store.StepFailed, errNoAnswer)
		e.wantRollout(store.RolloutPaused, pauseOwner)
	})
}
