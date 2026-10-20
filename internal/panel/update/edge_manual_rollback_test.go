package update

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// jobsIsolate is a second Service on the same store: the cron Worker next to the admin Worker.
func jobsIsolate(t *testing.T, e *env) *Service {
	t.Helper()
	job, err := New(e.st, e.fl, e.s.hl, e.s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func eventCount(t *testing.T, e *env, nodeID, code string) int {
	t.Helper()
	var n int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM event WHERE node_id = ? AND code = ?`, nodeID, code).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The owner's rollback on the edge asks the agent over the link and waits for its answer like the VPS does, so no recorded
// rollback frame is sent. While it waits the step is claimed: the jobs isolate's tick leaves the node alone. The agent's
// ok ends the step ROLLED_BACK (manual) and pauses the rollout.
func TestEdgeManualRollbackAnswersInTheRPC(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	job := jobsIsolate(t, e)

	e.clk.Advance(e.s.cfg.GateWait + time.Second) // the gate is overdue, yet the tick must not touch a claimed step
	e.fl.mu.Lock()
	e.fl.onRollback = func(string) (*agentv1.CommandResult, error) {
		claimed := e.step(id)
		if claimed.State != store.StepGating || claimed.RequestID == "" || claimed.Params["manual"] != "1" {
			t.Errorf("manual rollback did not claim the shared step while it waits: %+v", claimed)
		}
		job.EdgeTick(e.ctx, e.clk.Now())
		if x := e.step(id); x.RequestID != claimed.RequestID {
			t.Errorf("the jobs isolate changed the claimed step: %+v", x)
		}
		return okResult(), nil
	}
	e.fl.mu.Unlock()

	if _, err := e.s.rollbackNode(e.ctx, id); err != nil {
		t.Fatalf("manual rollback: %v", err)
	}
	if got := e.fl.rolledBack(); len(got) != 1 || got[0] != id {
		t.Fatalf("RollbackAgent calls = %v, want one for %s", got, id)
	}
	if got := len(e.fl.rollbackRequestFrames()); got != 0 {
		t.Fatalf("recorded rollback frames = %d, want none for the owner's rollback", got)
	}
	x := e.wantStep(id, store.StepRolledBack, errManual)
	if x.RequestID != "" {
		t.Fatalf("settled step keeps a request id: %+v", x)
	}
	e.wantRollout(store.RolloutPaused, pauseOwner)
	if n := eventCount(t, e, id, "update_step_rolled_back"); n != 1 {
		t.Fatalf("rolled back events = %d, want 1", n)
	}
}

// The agent refuses (no .prev): the owner hears it, as on the VPS, and the step is exactly what it was, the rollout still
// running. This used to answer success and let the step fall back with no event and no log.
func TestEdgeManualRollbackRefusalReachesTheOwner(t *testing.T) {
	for _, state := range []string{store.StepGating, store.StepPending} {
		t.Run(state, func(t *testing.T) {
			e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
			a, b := edgeNode(e, "a", 0), edgeNode(e, "b", 0)
			startEdgeRollout(e, 1, a, b)
			id := a
			if state == store.StepGating {
				edgeAdvanceToGating(t, e, a)
			} else {
				id = b // the second stage has not started
			}
			before := e.step(id)
			if before.State != state {
				t.Fatalf("setup: step is %s, want %s", before.State, state)
			}
			e.fl.mu.Lock()
			e.fl.onRollback = func(string) (*agentv1.CommandResult, error) {
				return &agentv1.CommandResult{Error: "no_previous"}, nil
			}
			e.fl.mu.Unlock()

			_, err := e.s.rollbackNode(e.ctx, id)
			if err == nil || connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "agent refused: no_previous") {
				t.Fatalf("refused rollback = %v, want the agent's reason for the owner", err)
			}
			after := e.step(id)
			if after.State != before.State || after.RequestID != "" || !after.RequestAt.IsZero() || after.ErrorKey != "" ||
				!reflect.DeepEqual(after.Params, before.Params) || !after.ReconnectedAt.Equal(before.ReconnectedAt) {
				t.Fatalf("step after a refusal = %+v, want it as before: %+v", after, before)
			}
			e.wantRollout(store.RolloutRunning, "")
			if n := eventCount(t, e, id, "update_step_rolled_back"); n != 0 {
				t.Fatalf("a refused rollback left %d rolled back events", n)
			}
		})
	}
}

// cancelOnRollback plays an agent that does not answer within the RPC deadline: the request context ends during the wait.
type cancelOnRollback struct {
	*fakeFleet
	cancel context.CancelFunc
}

func (c cancelOnRollback) RollbackAgent(ctx context.Context, _ string, _ time.Duration) (*agentv1.CommandResult, error) {
	c.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}

// The 20 s RPC deadline passes while the agent is silent. The claim is put back on a context of its own: with the dead
// request context the restore failed, the claim stayed, and two minutes later the tick failed the step and paused the
// rollout though nothing had happened to the node.
func TestEdgeManualRollbackRestoresAfterTheDeadline(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	before := e.step(id)

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	admin, err := New(e.st, cancelOnRollback{e.fl, cancel}, e.s.hl, e.s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.st.Node(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.rollbackNodeEdge(ctx, n); !errors.Is(err, context.Canceled) {
		t.Fatalf("rollback with a dead request context = %v, want its context error", err)
	}
	after := e.step(id)
	if after.State != store.StepGating || after.RequestID != "" || !reflect.DeepEqual(after.Params, before.Params) {
		t.Fatalf("claim was not restored: %+v, want %+v", after, before)
	}
	e.clk.Advance(edgeRollbackTimeout + time.Minute)
	edgeTick(e)
	if r := e.rollout(); r.PauseKey == pauseOwner {
		t.Fatalf("the tick ended a restored step as a manual rollback: rollout %s/%s", r.Status, r.PauseKey)
	}
	if x := e.step(id); x.Params["manual"] != "" || x.Params["reason"] == errNoAnswer {
		t.Fatalf("the tick treated the restored step as an unanswered manual rollback: %+v", x)
	}
}

// If the RPC dies after the claim and cannot restore it (an evicted Worker), the tick ends the step when the claim's
// deadline passes: the new build still runs, so the rollback did not happen and the step fails with no_answer.
func TestEdgeManualRollbackClaimTimesOutInTheTick(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	claim := e.step(id)
	claim.State, claim.RequestID, claim.RequestAt = store.StepGating, store.NewID("req_"), e.clk.Now()
	claim.Params = map[string]string{"gate": errManual, "manual": "1"}
	if err := e.st.SaveStep(e.ctx, &claim); err != nil {
		t.Fatal(err)
	}

	e.clk.Advance(edgeRollbackTimeout - time.Second)
	edgeTick(e)
	e.wantStep(id, store.StepGating, "")
	e.clk.Advance(time.Second)
	edgeTick(e)
	x := e.wantStep(id, store.StepFailed, errRollbackFailed)
	if x.Params["reason"] != errNoAnswer {
		t.Fatalf("timed out claim params = %+v", x.Params)
	}
	e.wantRollout(store.RolloutPaused, pauseOwner)
}

// A jobs pass that loaded the step before the owner's rollback claimed it still runs failGate: the step compare-and-set
// refuses it, so no second rollback frame goes out and the owner's command is the only one.
func TestEdgeStaleJobsPassLosesToTheAdminClaim(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	ro := startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	job := jobsIsolate(t, e)
	stale, err := e.st.RolloutSteps(e.ctx, ro.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleFail := func() {
		p := &pass{s: job, ctx: e.ctx, ro: e.rollout(), steps: stale, now: e.clk.Now()}
		p.failGate(&p.steps[0], errNotCommitted, nil)
	}

	e.fl.mu.Lock()
	e.fl.onRollback = func(string) (*agentv1.CommandResult, error) {
		staleFail() // while the owner's claim waits for the agent
		return okResult(), nil
	}
	e.fl.mu.Unlock()
	if _, err := e.s.rollbackNode(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	staleFail() // and once more after the owner's rollback settled
	if got := len(e.fl.rollbackRequestFrames()); got != 0 {
		t.Fatalf("stale pass sent %d rollback frames beside the owner's command", got)
	}
	if got := e.fl.rolledBack(); len(got) != 1 {
		t.Fatalf("RollbackAgent calls = %v, want only the owner's", got)
	}
	e.wantStep(id, store.StepRolledBack, errManual)
	e.wantRollout(store.RolloutPaused, pauseOwner)
}

// The other order: the tick sends the gate's rollback frame first. The owner's rollback then finds the step claimed by that
// command and is refused; only the tick's frame went out.
func TestEdgeAdminRollbackAfterTheTickIsRefused(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	e.clk.Advance(e.s.cfg.GateWait)
	edgeTick(e)
	if got := len(e.fl.rollbackRequestFrames()); got != 1 {
		t.Fatalf("tick rollback frames = %d, want 1", got)
	}

	_, err := e.s.rollbackNode(e.ctx, id)
	if err == nil || connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "a command is already running") {
		t.Fatalf("admin rollback after the tick = %v, want \"a command is already running\"", err)
	}
	if got := len(e.fl.rollbackRequestFrames()); got != 1 {
		t.Fatalf("rollback frames = %d, want 1 in total", got)
	}
	if got := e.fl.rolledBack(); len(got) != 0 {
		t.Fatalf("admin rollback reached the agent: %v", got)
	}
}
