package update

import (
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type cancelAnswer struct {
	status   string
	pauseKey string
	errCode  connect.Code
}

func TestCancelDecisionMatchesBothRollouts(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		pauseKey string
		errCode  connect.Code
	}{
		{name: "nothing in flight", status: store.RolloutCancelled},
		{name: "step in flight with pending steps", status: store.RolloutPaused, pauseKey: pauseCancelWait},
		{name: "step in flight with nothing pending and nothing skipped", status: store.RolloutPaused, pauseKey: pauseCancelWait},
		{name: "already cancelled", status: store.RolloutCancelled, errCode: connect.CodeFailedPrecondition},
		{name: "finished", status: store.RolloutDone, errCode: connect.CodeFailedPrecondition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := cancelAgentCase(t, tt.name)
			os := cancelOsCase(t, tt.name)
			if agent != os {
				t.Fatalf("agent cancel = %+v, OS cancel = %+v", agent, os)
			}
			want := cancelAnswer{status: tt.status, pauseKey: tt.pauseKey, errCode: tt.errCode}
			if agent != want {
				t.Fatalf("cancel answer = %+v, want %+v", agent, want)
			}
		})
	}
}

func cancelAgentCase(t *testing.T, name string) cancelAnswer {
	t.Helper()
	e := newEnv(t)
	e.defaultBundle()
	first := e.addNode("de1", nodeOpts{online: 0})
	ids := []string{first}
	if name == "step in flight with pending steps" {
		ids = append(ids, e.addNode("nl1", nodeOpts{online: 1}))
	}
	ro := e.startRollout(ids...)
	cancel := func() error {
		_, err := e.s.cancel(e.ctx, ro.ID)
		return err
	}
	var err error
	switch name {
	case "nothing in flight":
		err = cancel()
	case "step in flight with pending steps", "step in flight with nothing pending and nothing skipped":
		e.tick()
		err = cancel()
	case "already cancelled":
		if err := cancel(); err != nil {
			t.Fatal(err)
		}
		err = cancel()
	case "finished":
		e.tick()
		e.upgrade(first)
		e.tick()
		e.commit(first)
		e.tick()
		err = cancel()
	default:
		t.Fatalf("unknown cancel case %q", name)
	}
	stored := e.rollout()
	answer := cancelAnswer{status: stored.Status, pauseKey: stored.PauseKey}
	if err != nil {
		answer.errCode = connect.CodeOf(err)
	}
	return answer
}

func cancelOsCase(t *testing.T, name string) cancelAnswer {
	t.Helper()
	e := newEnv(t)
	first := e.osNode("de1")
	ids := []string{first}
	if name == "step in flight with pending steps" {
		ids = append(ids, e.osNode("nl1"))
	}
	reboot := store.OsRebootIfRequired
	if name == "finished" {
		e.hl.set(first, 1, 0, 0)
		reboot = store.OsRebootNever
	}
	ro, err := e.s.startOs(e.ctx, ids, store.OsModeSecurity, reboot)
	if err != nil {
		t.Fatal(err)
	}
	cancel := func() error {
		_, err := e.s.cancelOs(e.ctx, ro.ID)
		return err
	}
	switch name {
	case "nothing in flight":
		err = cancel()
	case "step in flight with pending steps", "step in flight with nothing pending and nothing skipped":
		e.osTick()
		err = cancel()
	case "already cancelled":
		if err := cancel(); err != nil {
			t.Fatal(err)
		}
		err = cancel()
	case "finished":
		e.osTick()
		step := e.osStep(first)
		e.osJob(first, &agentv1.OsJob{JobId: step.JobID, Action: "upgrade", Mode: "security", State: "done", Upgraded: 2, Reboot: "not_asked"})
		e.osEvent(first, "os_update_done", map[string]string{"job_id": step.JobID, "upgraded": "2", "reboot": "not_asked"})
		e.osTick()
		e.hl.set(first, 1, 1, 0)
		e.osTick()
		err = cancel()
	default:
		t.Fatalf("unknown cancel case %q", name)
	}
	stored := e.osRollout()
	answer := cancelAnswer{status: stored.Status, pauseKey: stored.PauseKey}
	if err != nil {
		answer.errCode = connect.CodeOf(err)
	}
	return answer
}
