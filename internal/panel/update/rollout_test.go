package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestNodeState(t *testing.T) {
	caps := []string{capUpdate}
	row := func(built int64, caps []string, last string) store.NodeRow {
		return store.NodeRow{AgentBuilt: built, AgentCaps: caps, LastUpdateJSON: last}
	}
	rolled := `{"outcome":"rolled_back","reason":"crash_loop"}`
	failed := `{"outcome":"failed","reason":"failed: exec"}`
	ok := `{"outcome":"ok"}`
	const ref = 100
	for _, tc := range []struct {
		name                string
		n                   store.NodeRow
		connected, updating bool
		want                adminv1.NodeUpdateState
	}{
		{"updating wins over everything", row(50, nil, ""), false, true, adminv1.NodeUpdateState_NODE_UPDATE_STATE_UPDATING},
		{"offline", row(50, caps, ""), false, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_OFFLINE},
		{"up to date", row(100, caps, ok), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE},
		{"newer than the reference", row(200, nil, ""), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE},
		{"an old agent: unsupported", row(0, nil, ""), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED},
		{"unsupported before rolled back", row(50, []string{"doctor/1"}, rolled), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED},
		{"rolled back", row(50, caps, rolled), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK},
		{"failed", row(50, caps, failed), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_FAILED},
		// a rolled_back record left the node on from_built; a node on another build was updated since (its newer
		// outcome reaches the panel only with the next Hello), so the record is history
		{"rolled back, then updated since", row(50, caps, `{"outcome":"rolled_back","from_built":10,"reason":"manual"}`), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED},
		{"rolled back, still on from_built", row(50, caps, `{"outcome":"rolled_back","from_built":50,"reason":"manual"}`), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK},
		{"outdated", row(50, caps, ok), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED},
		{"outdated, never updated", row(50, caps, ""), true, false, adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED},
	} {
		if got := nodeState(tc.n, tc.connected, tc.updating, ref); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReferenceBuiltFallsBackToThePanel(t *testing.T) {
	e := newEnv(t) // no bundle: the panel's own built (newBuilt) is the reference
	old := e.addNode("old", nodeOpts{built: oldBuilt})
	cur := e.addNode("cur", nodeOpts{built: newBuilt})
	if e.nodeState(old) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED || e.nodeState(cur) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE {
		t.Fatal("without a bundle the panel's own build is the reference")
	}
	e.bundle("0.3.0", newBuilt+5000, map[string][]byte{"mistgate-node-linux-amd64": []byte("x")})
	if e.nodeState(cur) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED {
		t.Fatal("a trusted bundle is the reference")
	}
}

func idsOf(steps []store.StepRow, state string) []string {
	var out []string
	for _, x := range steps {
		if x.State == state {
			out = append(out, x.NodeID)
		}
	}
	return out
}

type fixedNodeBundleSource struct {
	manifestSHA256 string
}

func (s fixedNodeBundleSource) Sync(context.Context, int64) (bool, error) { return false, nil }
func (s fixedNodeBundleSource) BundleStatus() (bool, string)              { return true, s.manifestSHA256 }

func syncCurrentTestBundleFromGitHub(e *env) {
	b := e.s.current()
	if b == nil || !b.trusted {
		e.t.Fatal("test bundle is not trusted")
	}
	sum := sha256.Sum256(b.raw)
	e.s.cfg.NodeBundleSource = fixedNodeBundleSource{manifestSHA256: hex.EncodeToString(sum[:])}
	e.s.syncNodeBundle(e.ctx)
}

func TestTrustedGitHubBundleWaitsForExplicitNodeUpdate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	syncCurrentTestBundleFromGitHub(e)
	first := e.addNode("first", nodeOpts{})
	second := e.addNode("second", nodeOpts{})
	if _, err := e.st.LatestRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GitHub bundle sync started a rollout: %v", err)
	}
	ro, err := e.s.start(e.ctx, []string{first}, 0)
	if err != nil {
		t.Fatalf("explicit single-node update: %v", err)
	}
	steps, err := e.st.RolloutSteps(e.ctx, ro.ID)
	if err != nil || len(steps) != 1 || steps[0].NodeID != first {
		t.Fatalf("explicit node rollout steps: %+v, %v", steps, err)
	}
	for _, step := range steps {
		if step.NodeID == second {
			t.Fatal("an unselected node was included in the rollout")
		}
	}
}

func TestManualNodeUpdateJoinsActiveRolloutAfterCurrentStage(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	first := e.addNode("first", nodeOpts{online: 0})
	currentQueue := e.addNode("queued", nodeOpts{online: 1})
	selected := e.addNode("selected", nodeOpts{online: 2})
	ro := e.startRollout(first, currentQueue)

	e.tick()
	e.wantStep(first, store.StepSent, "")
	joined, err := e.s.start(e.ctx, []string{selected}, 0)
	if err != nil {
		t.Fatalf("add a different node during the rollout: %v", err)
	}
	if joined.ID != ro.ID {
		t.Fatalf("node created another rollout: got %s, want %s", joined.ID, ro.ID)
	}

	steps, err := e.st.RolloutSteps(e.ctx, ro.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantStages := map[string]int{first: 0, selected: 1, currentQueue: 2}
	for _, step := range steps {
		if stage := wantStages[step.NodeID]; step.Stage != stage {
			t.Errorf("step %s stage %d, want %d", step.NodeID, step.Stage, stage)
		}
	}
	e.wantStep(selected, store.StepPending, "")
	if sent := e.fl.sent(); len(sent) != 1 || sent[0] != first {
		t.Fatalf("added node started before the canary passed: %v", sent)
	}
}

func TestRolloutSuccess(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	n1 := e.addNode("n1", nodeOpts{online: 5, inbounds: 1})
	n2 := e.addNode("n2", nodeOpts{online: 0, inbounds: 1})
	n3 := e.addNode("n3", nodeOpts{online: 2, inbounds: 1})
	for _, id := range []string{n1, n2, n3} {
		e.hl.set(id, 1, 0, 0)
	}

	ro := e.startRollout()
	if ro.BatchSize != 1 {
		t.Fatalf("batch size %d: fewer than 5 nodes go one at a time", ro.BatchSize)
	}
	steps, _ := e.st.RolloutSteps(e.ctx, ro.ID)
	want := map[string]int{n2: 0, n3: 1, n1: 2} // canary: the node with the fewest users online
	for _, x := range steps {
		if x.Stage != want[x.NodeID] || x.State != store.StepPending {
			t.Fatalf("step %+v, stage of %s should be %d", x, x.NodeID, want[x.NodeID])
		}
	}

	for i, id := range []string{n2, n3, n1} {
		e.tick()
		if got := e.fl.sent(); len(got) != i+1 || got[i] != id {
			t.Fatalf("after pass %d the commands went to %v, want %s last and nobody else", i+1, got, id)
		}
		x := e.wantStep(id, store.StepSent, "")
		if !x.SentAt.Equal(e.clk.Now()) || x.AckedAt.IsZero() {
			t.Fatalf("sent step %+v", x)
		}
		if !e.s.Updating(id) {
			t.Fatalf("%s is not marked updating", id)
		}
		e.clk.Advance(20 * time.Second)
		e.tick() // not back yet: nothing moves, nothing is sent twice
		e.wantStep(id, store.StepSent, "")
		if len(e.fl.sent()) != i+1 {
			t.Fatal("UpdateAgent was sent twice")
		}

		e.drop(id)
		e.clk.Advance(5 * time.Second)
		e.upgrade(id)
		e.tick()
		g := e.wantStep(id, store.StepGating, "")
		if !g.ReconnectedAt.Equal(e.clk.Now()) {
			t.Fatalf("reconnected_at %v", g.ReconnectedAt)
		}
		// committed, but the synthetic round after the reconnect is still missing: the gate holds and asks for one
		e.commit(id)
		e.clk.Advance(10 * time.Second)
		e.tick()
		e.wantStep(id, store.StepGating, "")
		if e.hl.runs[id] == 0 {
			t.Fatal("the gate did not ask for a synthetic round")
		}
		if !e.hl.since[id].Equal(g.ReconnectedAt) {
			t.Fatalf("the round must have started after the reconnect: since %v", e.hl.since[id])
		}
		e.hl.set(id, 1, 1, 0)
		e.clk.Advance(10 * time.Second)
		e.tick()
		e.wantStep(id, store.StepPassed, "")
		if i < 2 {
			// the same pass went on to the next stage
			continue
		}
	}
	e.wantRollout(store.RolloutDone, "")
	if e.s.Updating(n1) || e.s.Updating(n2) || e.s.Updating(n3) {
		t.Fatal("a finished rollout leaves nobody updating")
	}
	if got := e.fl.rolledBack(); len(got) != 0 {
		t.Fatalf("rollbacks %v", got)
	}
	resp := e.get()
	for _, n := range resp.Nodes {
		if n.State != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE || n.Built != newBuilt {
			t.Errorf("node after the rollout: %+v", n)
		}
		// no Hello has carried the outcome yet: the page still shows the update
		if lu := n.LastUpdate; lu == nil || lu.Outcome != "ok" || lu.ToBuilt != newBuilt || lu.FromBuilt != oldBuilt || lu.FromVersion != "0.1.0-old" {
			t.Errorf("last_update of %s right after the step passed: %+v", n.NodeId, lu)
		}
	}
	if resp.Rollout.Status != adminv1.RolloutStatus_ROLLOUT_STATUS_DONE || len(resp.Rollout.Steps) != 3 || resp.Rollout.PauseKey != "" {
		t.Errorf("rollout message: %+v", resp.Rollout)
	}
	if conds := e.s.Conditions(e.ctx); len(conds) != 0 {
		t.Fatalf("conditions %+v", conds)
	}
	if err := e.s.rescanAudit(t); err != nil {
		t.Fatal(err)
	}
}

// rescanAudit checks that the start of the rollout was audited.
func (s *Service) rescanAudit(t *testing.T) error {
	rows, err := s.st.ListAudit(context.Background(), "", 0, 50)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Action == "update_rollout_start" && r.Actor == "adm_test" {
			return nil
		}
	}
	return errors.New("no audit row for the rollout start")
}

// A node without inbounds (nothing to probe) passes on the other conditions.
func TestGateWithoutProbeableInbounds(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	n := e.addNode("n1", nodeOpts{})
	e.hl.set(n, 0, 0, 0)
	e.startRollout()
	e.tick()
	e.upgrade(n)
	e.tick()
	e.wantStep(n, store.StepGating, "")
	e.commit(n)
	e.tick()
	e.wantStep(n, store.StepPassed, "")
	e.wantRollout(store.RolloutDone, "")
	if ev := e.wantEvent(n, "update_step_passed"); ev.Source != "panel" || ev.Severity != 1 || ev.Params["to_version"] == "" {
		t.Errorf("step event %+v", ev)
	}
}

func TestBatchSizes(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	var ids []string
	for i := range 6 {
		ids = append(ids, e.addNode(fmt.Sprintf("n%d", i), nodeOpts{online: i}))
	}
	ro := e.startRollout()
	if ro.BatchSize != 2 {
		t.Fatalf("batch size %d for 6 nodes", ro.BatchSize)
	}
	steps, _ := e.st.RolloutSteps(e.ctx, ro.ID)
	stages := map[int]int{}
	for _, x := range steps {
		stages[x.Stage]++
	}
	if stages[0] != 1 || stages[1] != 2 || stages[2] != 2 || stages[3] != 1 {
		t.Fatalf("stages %v: a canary, then batches of two", stages)
	}
	// an explicit batch size, larger than the list
	e.s.cancel(e.ctx, "")
	ro, err := e.s.start(e.ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ = e.st.RolloutSteps(e.ctx, ro.ID)
	for _, x := range steps {
		if x.Stage > 1 {
			t.Fatalf("batch 10 puts everything after the canary into one stage: %+v", x)
		}
	}
	_ = ids
}

// The first stage goes out alone; the second waits until it is decided; a batch goes out together.
func TestBatchesGoOutTogetherAndWaitForTheStageBefore(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	var ids []string
	for i := range 5 {
		ids = append(ids, e.addNode(fmt.Sprintf("n%d", i), nodeOpts{online: i}))
	}
	e.startRollout() // 5 nodes: batches of 2 -> stages {n0} {n1,n2} {n3,n4}
	e.tick()
	if got := e.fl.sent(); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("only the canary: %v", got)
	}
	e.upgrade(ids[0])
	e.tick()
	e.commit(ids[0])
	e.tick() // passes, and the same pass starts the batch
	got := e.fl.sent()
	if len(got) != 3 || !(got[1] == ids[1] && got[2] == ids[2] || got[1] == ids[2] && got[2] == ids[1]) {
		t.Fatalf("the second stage goes out together: %v", got)
	}
	e.upgrade(ids[1])
	e.commit(ids[1])
	e.tick()
	if len(e.fl.sent()) != 3 {
		t.Fatal("the third stage must wait for the whole second one")
	}
	e.upgrade(ids[2])
	e.commit(ids[2])
	e.tick()
	e.tick()
	if len(e.fl.sent()) != 5 {
		t.Fatalf("third stage: %v", e.fl.sent())
	}
}

func TestStartPreconditions(t *testing.T) {
	e := newEnv(t)
	n := e.addNode("n1", nodeOpts{})
	other := e.addNode("n2", nodeOpts{built: newBuilt}) // already current

	check := func(what string, err error, code connect.Code, msg string) {
		t.Helper()
		if connectCode(err) != code || err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v, want %v %q", what, err, code, msg)
		}
	}
	_, err := e.s.start(e.ctx, nil, 0)
	check("no bundle", err, connect.CodeFailedPrecondition, "no trusted bundle")

	e.defaultBundle()
	_, err = e.s.start(e.ctx, nil, 11)
	check("batch size", err, connect.CodeInvalidArgument, "batch_size must be 0 to 10")
	_, err = e.s.start(e.ctx, []string{"nod_nope"}, 0)
	check("unknown node", err, connect.CodeNotFound, "no such node")
	_, err = e.s.start(e.ctx, []string{other}, 0)
	check("only a current node", err, connect.CodeFailedPrecondition, "no node to update")

	unsigned := e.newService(nil)
	_, err = unsigned.start(e.ctx, nil, 0)
	check("no key", err, connect.CodeFailedPrecondition, "no release key in this build")

	e.startRollout(n)
	_, err = e.s.start(e.ctx, nil, 0)
	check("second rollout", err, connect.CodeFailedPrecondition, "a rollout is already active")
}

func TestPanelUpdateAndRolloutAreMutuallyExclusive(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	node := e.addNode("n1", nodeOpts{})

	if err := e.s.beginPanelUpdate(e.ctx); err != nil {
		t.Fatalf("reserve panel update: %v", err)
	}
	if _, err := e.s.start(e.ctx, []string{node}, 0); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "panel update") {
		t.Fatalf("rollout during panel update: %v", err)
	}
	e.s.finishPanelUpdate(false)

	e.startRollout(node)
	if err := e.s.beginPanelUpdate(e.ctx); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "active node rollout") {
		t.Fatalf("panel update during rollout: %v", err)
	}
}

// A listed node that is up to date, offline or unsupported becomes a SKIPPED step; it does not make a rollout.
func TestOldAgentsAndOfflineNodesAreSkipped(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	old := e.addNode("old", nodeOpts{caps: []string{"doctor/1"}, built: 0})
	gone := e.addNode("gone", nodeOpts{offline: true})
	cur := e.addNode("cur", nodeOpts{built: newBuilt})
	ok := e.addNode("ok", nodeOpts{})
	if e.nodeState(old) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED || e.nodeState(gone) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OFFLINE ||
		e.nodeState(cur) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE || e.nodeState(ok) != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED {
		t.Fatalf("states: %+v", e.get().Nodes)
	}
	// the default candidates are the OUTDATED nodes only
	ro := e.startRollout()
	steps, _ := e.st.RolloutSteps(e.ctx, ro.ID)
	if len(steps) != 1 || steps[0].NodeID != ok {
		t.Fatalf("default candidates: %+v", steps)
	}
	e.s.cancel(e.ctx, "")

	// listed by hand: the canary plus one SKIPPED step per node that cannot be updated, after the last stage
	ro = e.startRollout(old, gone, cur, ok)
	steps, _ = e.st.RolloutSteps(e.ctx, ro.ID)
	if len(steps) != 4 {
		t.Fatalf("steps %+v", steps)
	}
	for id, key := range map[string]string{old: "unsupported", gone: "offline", cur: "up_to_date"} {
		x := e.wantStep(id, store.StepSkipped, key)
		if x.Stage <= e.step(ok).Stage {
			t.Errorf("skipped step of %s sits in stage %d, next to the canary", id, x.Stage)
		}
	}
	e.tick()
	e.upgrade(ok)
	e.tick()
	e.commit(ok)
	e.tick()
	e.wantRollout(store.RolloutDone, "")
	for _, id := range e.fl.sent() {
		if id != ok {
			t.Fatalf("a command went to %s, which cannot update", id)
		}
	}
	// only nodes that cannot update: nothing to roll out
	e.upgrade(ok)
	if _, err := e.s.start(e.ctx, []string{old, gone}, 0); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no node to update") {
		t.Fatalf("only skipped nodes: %v", err)
	}
}

// A node that went offline, or lost the capability, between the start and its turn is skipped, not failed.
func TestStepIsSkippedWhenTheNodeIsGoneAtItsTurn(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0})
	b := e.addNode("b", nodeOpts{online: 1})
	c := e.addNode("c", nodeOpts{online: 2})
	e.startRollout()
	e.drop(b)
	e.fl.mu.Lock()
	e.fl.caps[c] = []string{"doctor/1"}
	e.fl.mu.Unlock()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.tick()
	e.wantStep(b, store.StepSkipped, "offline")
	e.tick()
	e.wantStep(c, store.StepSkipped, "unsupported")
	e.wantRollout(store.RolloutDone, "")
	if got := e.fl.sent(); len(got) != 1 {
		t.Fatalf("commands %v", got)
	}
}

func TestAgentAnswersNoop(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	n := e.addNode("n1", nodeOpts{})
	e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"noop": "1"}}, nil
	}
	e.startRollout()
	e.tick()
	e.wantStep(n, store.StepSkipped, "up_to_date")
	e.wantRollout(store.RolloutDone, "")
	if e.s.Updating(n) {
		t.Fatal("a noop leaves nobody updating")
	}
}

func TestAgentRefusesTheUpdate(t *testing.T) {
	for _, tc := range []struct {
		answer string
		key    string
		detail string
	}{
		{"hash_mismatch", "hash_mismatch", ""},
		{"expired", "expired", ""},
		{"failed: disk full", "failed", "failed: disk full"},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			e := newEnv(t)
			e.defaultBundle()
			a := e.addNode("a", nodeOpts{online: 0})
			e.addNode("b", nodeOpts{online: 1})
			e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
				return &agentv1.CommandResult{Ok: false, Error: tc.answer}, nil
			}
			e.startRollout()
			e.tick()
			x := e.wantStep(a, store.StepFailed, tc.key)
			if x.Params["detail"] != tc.detail {
				t.Errorf("params %v", x.Params)
			}
			ro := e.wantRollout(store.RolloutPaused, "step_failed")
			if ro.PauseParams["node"] != "a" || ro.PauseParams["reason"] != tc.key {
				t.Errorf("pause params %v", ro.PauseParams)
			}
			if e.s.Updating(a) {
				t.Error("a failed step is not updating")
			}
			e.tick() // paused: the next stage does not go out
			if got := e.fl.sent(); len(got) != 1 {
				t.Fatalf("commands %v", got)
			}
			conds := e.s.Conditions(e.ctx)
			if len(conds) != 1 || conds[0].Kind != "update_failed" || conds[0].NodeID != a || conds[0].Subject != ro.ID ||
				conds[0].Why != "health.alert.update_failed.why.step_failed" || conds[0].Params["reason"] != tc.key || conds[0].Params["node"] != "a" {
				t.Fatalf("conditions %+v", conds)
			}
		})
	}
}

// The gate fails (probe never comes back ok): RollbackAgent goes out, the step is ROLLED_BACK, the rollout pauses and
// the alert condition appears; resuming continues with the next stage and the alert goes away.
func TestGateFailureRollsBackAndPauses(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0, inbounds: 1})
	b := e.addNode("b", nodeOpts{online: 1, inbounds: 1})
	e.hl.set(a, 1, 0, 1) // the round after the update fails
	e.hl.set(b, 1, 1, 0)
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.clk.Advance(2 * time.Minute)
	e.tick()
	e.wantStep(a, store.StepGating, "")
	e.clk.Advance(3*time.Minute + time.Second) // the gate window is over
	e.tick()
	if got := e.fl.rolledBack(); len(got) != 1 || got[0] != a {
		t.Fatalf("rollbacks %v, want one to %s", got, a)
	}
	e.wantStep(a, store.StepRolledBack, "probe_failed")
	if ev := e.wantEvent(a, "update_step_rolled_back"); ev.Severity != 2 || ev.Params["reason"] != "probe_failed" {
		t.Errorf("rollback event %+v", ev)
	}
	ro := e.wantRollout(store.RolloutPaused, "gate_failed")
	if ro.PauseParams["node"] != "a" || ro.PauseParams["reason"] != "probe_failed" {
		t.Fatalf("pause params %v", ro.PauseParams)
	}
	conds := e.s.Conditions(e.ctx)
	if len(conds) != 1 || conds[0].NodeID != a || conds[0].Why != "health.alert.update_failed.why.gate_failed" || conds[0].Severity != 2 {
		t.Fatalf("conditions %+v", conds)
	}
	m := e.get().Rollout
	if m.PauseKey != "updates.pause.gate_failed" || m.PauseParams["node"] != "a" || m.PauseParams["node_id"] != "" {
		t.Fatalf("rollout message %+v", m)
	}
	e.tick() // paused: nothing goes to b
	if len(e.fl.sent()) != 1 {
		t.Fatal("a paused rollout sends nothing")
	}

	// the owner resumes: the failed step stays as it is, the rest goes on, the alert is gone
	if _, err := e.s.resume(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	if conds := e.s.Conditions(e.ctx); len(conds) != 0 {
		t.Fatalf("conditions after resume %+v", conds)
	}
	e.tick()
	if got := e.fl.sent(); len(got) != 2 || got[1] != b {
		t.Fatalf("commands %v", got)
	}
	e.upgrade(b)
	e.tick()
	e.commit(b)
	e.tick()
	e.wantStep(b, store.StepPassed, "")
	e.wantStep(a, store.StepRolledBack, "probe_failed")
	e.wantRollout(store.RolloutFailed, "") // finished with a rolled back step
}

// Hard failure: an inbound that worked before is FAILED on two evaluations at least 30 s apart: the gate does not
// wait for its deadline. An inbound that was already FAILED before the update does not count.
func TestGateFailsAtOnceOnAFailedInbound(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{inbounds: 2})
	e.hl.set(a, 0, 0, 0)
	e.exec(`UPDATE inbound SET state = 'failed' WHERE id = 'inb_a_0'`) // broken before the update
	e.startRollout()
	e.tick()
	if pre := e.step(a).PreFailed; len(pre) != 1 || pre[0] != "inb_a_0" {
		t.Fatalf("pre_failed %v", pre)
	}
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.tick()
	e.wantStep(a, store.StepPassed, "") // the old failure is not the update's

	e2 := newEnv(t)
	e2.defaultBundle()
	b := e2.addNode("b", nodeOpts{inbounds: 2})
	e2.hl.set(b, 0, 0, 0)
	e2.startRollout()
	e2.tick()
	e2.upgrade(b)
	e2.tick()
	e2.commit(b)
	e2.exec(`UPDATE inbound SET state = 'failed' WHERE id = 'inb_b_1'`)
	e2.tick() // first sighting
	e2.wantStep(b, store.StepGating, "")
	e2.clk.Advance(29 * time.Second)
	e2.tick()
	e2.wantStep(b, store.StepGating, "")
	e2.clk.Advance(2 * time.Second) // 31 s since the first sighting
	e2.tick()
	x := e2.wantStep(b, store.StepRolledBack, "inbound_failed")
	if x.Params["inbound"] != "inb_b_1" {
		t.Errorf("params %v", x.Params)
	}
	if got := e2.fl.rolledBack(); len(got) != 1 {
		t.Fatalf("rollbacks %v", got)
	}
	e2.wantRollout(store.RolloutPaused, "gate_failed")
}

// A failure that goes away again before the second evaluation is not a failure.
func TestInboundFailureThatHealsDoesNotFailTheGate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{inbounds: 1})
	e.hl.set(a, 0, 0, 0)
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.exec(`UPDATE inbound SET state = 'failed' WHERE id = 'inb_a_0'`)
	e.tick()
	e.exec(`UPDATE inbound SET state = 'active' WHERE id = 'inb_a_0'`)
	e.clk.Advance(time.Minute)
	e.tick()
	e.wantStep(a, store.StepGating, "")
	e.exec(`UPDATE inbound SET state = 'failed' WHERE id = 'inb_a_0'`)
	e.tick() // a new first sighting: the clock starts again
	e.wantStep(a, store.StepGating, "")
}

func TestGateNeedsTheStateToBeApplied(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{})
	e.exec(`UPDATE node SET desired_hash = 'h2', applied_hash = 'h1' WHERE id = ?`, a)
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.tick()
	e.wantStep(a, store.StepGating, "") // committed, but the applied hash differs from the desired one
	e.exec(`UPDATE node SET applied_hash = 'h2' WHERE id = ?`, a)
	e.tick()
	e.wantStep(a, store.StepPassed, "")
}

func TestGateTimesOutWithoutACommit(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{})
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.clk.Advance(5*time.Minute + time.Second)
	e.tick()
	e.wantStep(a, store.StepRolledBack, "state_not_applied")
	if got := e.fl.rolledBack(); len(got) != 1 {
		t.Fatalf("rollbacks %v", got)
	}
}

// The commit event of an earlier update of the same node must not pass the gate of a later one: the owner rolls the
// node back and updates it again within the minute (the old rule counted events up to a minute before the send).
func TestGateIgnoresTheCommitOfAnEarlierUpdate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{})
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.clk.Advance(10 * time.Second)
	e.tick()
	e.wantStep(a, store.StepPassed, "")
	e.wantRollout(store.RolloutDone, "")

	e.clk.Advance(5 * time.Second)
	if _, err := e.s.rollbackNode(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.hello(a, "0.1.0-old", oldBuilt, []string{"doctor/1", capUpdate, capGuard}, "")
	e.clk.Advance(10 * time.Second)
	e.startRollout()
	e.tick()
	e.wantStep(a, store.StepSent, "")
	e.clk.Advance(5 * time.Second)
	e.upgrade(a)
	e.tick()
	e.wantStep(a, store.StepGating, "")
	e.clk.Advance(10 * time.Second)
	e.tick()
	e.wantStep(a, store.StepGating, "") // the first commit is 30 s old: not this update's
	e.commit(a)
	e.tick()
	e.wantStep(a, store.StepPassed, "")
}

// An update_committed event that names another build is not this rollout's commit.
func TestGateIgnoresTheCommitOfAnotherBuild(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{})
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.clk.Advance(time.Second)
	e.exec(`INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (?, 1, 'update_committed', 'agent', ?, ?)`,
		e.clk.Now().Unix(), a, `{"to_built":"1"}`)
	e.tick()
	e.wantStep(a, store.StepGating, "")
	e.exec(`INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (?, 1, 'update_committed', 'agent', ?, ?)`,
		e.clk.Now().Unix(), a, fmt.Sprintf(`{"to_built":"%d"}`, newBuilt))
	e.tick()
	e.wantStep(a, store.StepPassed, "")
}

// RollbackAgent itself fails (no .prev): the step is FAILED with that reason and the pause says so.
func TestRollbackThatFails(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{})
	e.fl.onRollback = func(string) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: false, Error: "no_previous"}, nil
	}
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.clk.Advance(5*time.Minute + time.Second)
	e.tick()
	x := e.wantStep(a, store.StepFailed, "rollback_failed")
	if x.Params["reason"] != "no_previous" || x.Params["gate"] != "state_not_applied" {
		t.Fatalf("params %v", x.Params)
	}
	e.wantRollout(store.RolloutPaused, "gate_failed")
}

// The agent rolled itself back (its 5-minute timer, or the crash-loop guard): no RollbackAgent is sent, the step is
// ROLLED_BACK with the agent's reason. Seen while GATING and, if the panel was slower, while still SENT.
func TestAgentRolledItselfBack(t *testing.T) {
	rolled := func(e *env, reason string) string {
		return store.LastUpdateRow{Outcome: "rolled_back", FromVersion: "0.1.0-old", FromBuilt: oldBuilt, ToVersion: "0.2.0-new", ToBuilt: newBuilt,
			Reason: reason, AtUnix: e.clk.Now().Unix()}.JSON()
	}
	t.Run("while gating", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.startRollout()
		e.tick()
		e.upgrade(a)
		e.tick()
		e.wantStep(a, store.StepGating, "")
		e.clk.Advance(4 * time.Minute)
		e.drop(a)
		e.hello(a, "0.1.0-old", oldBuilt, []string{capUpdate}, rolled(e, "not_committed"))
		e.tick()
		x := e.wantStep(a, store.StepRolledBack, "rolled_back_by_agent")
		if x.Params["reason"] != "not_committed" {
			t.Fatalf("params %v", x.Params)
		}
		if len(e.fl.rolledBack()) != 0 {
			t.Fatal("the agent already rolled back: nothing to send")
		}
		ro := e.wantRollout(store.RolloutPaused, "gate_failed")
		if ro.PauseParams["reason"] != "rolled_back_by_agent" {
			t.Fatalf("pause params %v", ro.PauseParams)
		}
	})
	t.Run("while sent", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.startRollout()
		e.tick()
		e.clk.Advance(30 * time.Second)
		e.drop(a)
		e.hello(a, "0.1.0-old", oldBuilt, []string{capUpdate}, rolled(e, "crash_loop"))
		e.tick()
		x := e.wantStep(a, store.StepRolledBack, "rolled_back_by_agent")
		if x.Params["reason"] != "crash_loop" {
			t.Fatalf("params %v", x.Params)
		}
	})
	t.Run("an old outcome of the same build is not this step's", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		stale := store.LastUpdateRow{Outcome: "rolled_back", ToBuilt: newBuilt, Reason: "crash_loop", AtUnix: e.clk.Now().Add(-time.Hour).Unix()}.JSON()
		a := e.addNode("a", nodeOpts{})
		e.hello(a, "0.1.0-old", oldBuilt, []string{capUpdate}, stale)
		e.startRollout(a)
		e.tick()
		e.clk.Advance(10 * time.Second)
		e.tick()
		e.wantStep(a, store.StepSent, "") // a retry after an earlier rollback is not judged by that rollback
	})
}

func TestUpdateTimeouts(t *testing.T) {
	t.Run("no answer", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("the node did not answer in time"))
		}
		e.startRollout()
		e.tick()
		e.wantStep(a, store.StepFailed, "no_answer")
		e.wantRollout(store.RolloutPaused, "gate_failed")
	})
	t.Run("no answer, but the node came back with the new build", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.fl.onUpdate = func(id string) (*agentv1.CommandResult, error) {
			e.upgrade(id) // it updated, and its answer got lost
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("the node did not answer in time"))
		}
		e.startRollout()
		e.tick()
		e.wantStep(a, store.StepSent, "")
		e.tick()
		e.wantStep(a, store.StepGating, "")
	})
	t.Run("the link drops with the answer lost", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the node link dropped"))
		}
		e.startRollout()
		e.tick()
		e.wantStep(a, store.StepSent, "") // the pass decides from what it sees
		e.clk.Advance(12 * time.Minute)
		e.tick()
		e.wantStep(a, store.StepSent, "")
		e.clk.Advance(2 * time.Minute)
		e.tick()
		e.wantStep(a, store.StepFailed, "no_answer")
		e.wantRollout(store.RolloutPaused, "gate_failed")
	})
	t.Run("acknowledged but never back", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.startRollout()
		e.tick()
		e.drop(a)
		e.clk.Advance(2*time.Minute + 50*time.Second)
		e.tick()
		e.wantStep(a, store.StepSent, "")
		e.clk.Advance(20 * time.Second)
		e.tick()
		e.wantStep(a, store.StepFailed, "not_reconnected")
		e.wantRollout(store.RolloutPaused, "gate_failed")
		if len(e.fl.rolledBack()) != 0 {
			t.Fatal("an offline node cannot be sent anything")
		}
	})
	t.Run("gone again while gating", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.startRollout()
		e.tick()
		e.upgrade(a)
		e.tick()
		e.drop(a)
		e.clk.Advance(5*time.Minute + time.Second)
		e.tick()
		e.wantStep(a, store.StepFailed, "not_reconnected")
	})
}

func TestPauseResumeCancel(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0})
	b := e.addNode("b", nodeOpts{online: 1})
	c := e.addNode("c", nodeOpts{online: 2})
	ro := e.startRollout()

	if _, err := e.s.resume(e.ctx, ""); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "rollout is not paused") {
		t.Fatalf("resume a running rollout: %v", err)
	}
	if _, err := e.s.pauseByOwner(e.ctx, "rol_nope"); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("pause an unknown rollout: %v", err)
	}
	if got, err := e.s.pauseByOwner(e.ctx, ro.ID); err != nil || got.Status != store.RolloutPaused || got.PauseKey != "owner" {
		t.Fatalf("pause: %+v %v", got, err)
	}
	if got, err := e.s.pauseByOwner(e.ctx, ""); err != nil || got.Status != store.RolloutPaused { // idempotent
		t.Fatalf("pause again: %+v %v", got, err)
	}
	if conds := e.s.Conditions(e.ctx); len(conds) != 0 {
		t.Fatalf("a pause by the owner raises no alert: %+v", conds)
	}
	e.tick()
	if len(e.fl.sent()) != 0 {
		t.Fatal("a paused rollout sends nothing")
	}
	if _, err := e.s.resume(e.ctx, ro.ID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.fl.sent(); len(got) != 1 || got[0] != a {
		t.Fatalf("after resume: %v", got)
	}

	// pause while a step is in flight: it finishes and is gated, nothing new starts
	if _, err := e.s.pauseByOwner(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.tick()
	e.wantStep(a, store.StepPassed, "")
	if len(e.fl.sent()) != 1 {
		t.Fatal("paused: b must not start")
	}

	// cancel: pending steps are skipped, the rollout ends CANCELLED
	got, err := e.s.cancel(e.ctx, "")
	if err != nil || got.Status != store.RolloutCancelled {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	e.wantStep(b, store.StepSkipped, "cancelled")
	e.wantStep(c, store.StepSkipped, "cancelled")
	e.wantStep(a, store.StepPassed, "")
	if _, err := e.s.cancel(e.ctx, ""); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "rollout is not active") {
		t.Fatalf("cancel a finished rollout: %v", err)
	}
	if _, err := e.s.cancel(e.ctx, ro.ID); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancel a finished rollout by id: %v", err)
	}
	// and there is room for the next
	e.startRollout(b)
}

// Cancel while a step is in flight: it is finished and gated first, and the rollout ends CANCELLED afterwards.
func TestCancelWaitsForTheStepInFlight(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0})
	b := e.addNode("b", nodeOpts{online: 1})
	e.startRollout()
	e.tick()
	got, err := e.s.cancel(e.ctx, "")
	if err != nil || got.Status != store.RolloutRunning {
		t.Fatalf("cancel with a step in flight: %+v %v", got, err)
	}
	e.wantStep(b, store.StepSkipped, "cancelled")
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.tick()
	e.wantStep(a, store.StepPassed, "")
	e.wantRollout(store.RolloutCancelled, "")

	// the same with a step that fails: FAILED wins over CANCELLED
	e2 := newEnv(t)
	e2.defaultBundle()
	c := e2.addNode("c", nodeOpts{online: 0})
	e2.addNode("d", nodeOpts{online: 1})
	e2.startRollout()
	e2.tick()
	e2.s.cancel(e2.ctx, "")
	e2.upgrade(c)
	e2.tick()
	e2.clk.Advance(6 * time.Minute)
	e2.tick()
	e2.wantRollout(store.RolloutFailed, "")
}

// A rollout paused by a failed step with nothing left pending (a fleet of one) still ends when the owner cancels it,
// and its alert goes: it used to stay PAUSED for ever because the cancel had no step to mark as cancelled.
func TestCancelEndsAPausedRolloutWithNothingPending(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0})
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.clk.Advance(6 * time.Minute) // the new build never commits: the gate fails and the rollout pauses
	e.tick()
	e.wantRollout(store.RolloutPaused, "gate_failed")
	if len(e.s.Conditions(e.ctx)) != 1 {
		t.Fatalf("a gate failure raises the alert: %+v", e.s.Conditions(e.ctx))
	}
	if got, err := e.s.cancel(e.ctx, ""); err != nil || got.Status != store.RolloutFailed {
		t.Fatalf("cancel a paused rollout with nothing pending: %+v %v", got, err)
	}
	if conds := e.s.Conditions(e.ctx); len(conds) != 0 {
		t.Fatalf("the alert outlives the cancelled rollout: %+v", conds)
	}
}

// The owner replaces the bundle under a running rollout: it pauses with bundle_changed (no alert), and resuming is
// refused until the bundle is the release the rollout ships again.
func TestBundleChangedPausesTheRollout(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0})
	e.addNode("b", nodeOpts{online: 1})
	ro := e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.bundle("0.3.0-newer", newBuilt+100, map[string][]byte{"mistgate-node-linux-amd64": []byte("v3"), "mistgate-node-linux-arm64": []byte("v3 arm")})
	e.tick()
	e.wantStep(a, store.StepPassed, "") // the step in flight was judged first
	e.wantRollout(store.RolloutPaused, "bundle_changed")
	if len(e.fl.sent()) != 1 {
		t.Fatal("b must not get the newer bundle under this rollout")
	}
	if conds := e.s.Conditions(e.ctx); len(conds) != 0 {
		t.Fatalf("bundle_changed raises no alert: %+v", conds)
	}
	if _, err := e.s.resume(e.ctx, ro.ID); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("resume with another bundle on disk: %v", err)
	}
}

func TestRolloutSurvivesAPanelRestart(t *testing.T) {
	t.Run("sent, the node came back meanwhile", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		hold := make(chan struct{})
		e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
			<-hold // the panel dies before the answer arrives
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the node link dropped"))
		}
		e.startRollout()
		e.s.tick(e.ctx)
		e.wantStep(a, store.StepSent, "")
		old := e.s
		e.restart()
		e.upgrade(a) // it updated while the panel was down
		e.clk.Advance(40 * time.Second)
		e.tick()
		g := e.wantStep(a, store.StepGating, "")
		if !g.ReconnectedAt.Equal(e.clk.Now()) {
			t.Fatalf("reconnected_at %v", g.ReconnectedAt)
		}
		if !e.s.Updating(a) {
			t.Fatal("updating is rebuilt from the database")
		}
		e.commit(a)
		e.tick()
		e.wantStep(a, store.StepPassed, "")
		e.wantRollout(store.RolloutDone, "")
		if len(e.fl.sent()) != 1 {
			t.Fatalf("nothing is sent twice: %v", e.fl.sent())
		}
		close(hold)
		old.wg.Wait() // the dead panel's goroutine ends without touching the row
		e.wantStep(a, store.StepPassed, "")
	})
	t.Run("sent, no contact since: ambiguous", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		hold := make(chan struct{})
		e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
			<-hold
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the node link dropped"))
		}
		e.startRollout()
		e.s.tick(e.ctx)
		old := e.s
		e.restart()
		e.drop(a)
		e.clk.Advance(12 * time.Minute)
		e.tick()
		e.wantStep(a, store.StepSent, "")
		e.clk.Advance(2 * time.Minute)
		e.tick()
		e.wantStep(a, store.StepFailed, "no_answer")
		ro := e.wantRollout(store.RolloutPaused, "panel_restart_ambiguous")
		conds := e.s.Conditions(e.ctx)
		if len(conds) != 1 || conds[0].Why != "health.alert.update_failed.why.panel_restart_ambiguous" || conds[0].NodeID != a || conds[0].Subject != ro.ID {
			t.Fatalf("conditions %+v", conds)
		}
		close(hold)
		old.wg.Wait()
	})
	t.Run("gating: the window starts again", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("a", nodeOpts{})
		e.startRollout()
		e.tick()
		e.upgrade(a)
		e.tick()
		e.wantStep(a, store.StepGating, "")
		e.clk.Advance(4 * time.Minute) // the old window would end in a minute
		e.restart()
		e.clk.Advance(2 * time.Minute)
		e.tick()
		e.wantStep(a, store.StepGating, "") // six minutes after the reconnect, but only two after the restart
		e.clk.Advance(3*time.Minute + time.Second)
		e.tick()
		e.wantStep(a, store.StepRolledBack, "state_not_applied")
	})
	t.Run("a paused rollout stays paused", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		e.addNode("a", nodeOpts{online: 0})
		e.addNode("b", nodeOpts{online: 1})
		ro := e.startRollout()
		e.s.pauseByOwner(e.ctx, ro.ID)
		e.restart()
		e.tick()
		e.wantRollout(store.RolloutPaused, "owner")
		if len(e.fl.sent()) != 0 {
			t.Fatal("a paused rollout sends nothing after a restart")
		}
	})
}

func TestRollbackNode(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{built: newBuilt})
	old := e.addNode("old", nodeOpts{caps: []string{"doctor/1"}})
	gone := e.addNode("gone", nodeOpts{offline: true})

	v, err := e.s.rollbackNode(e.ctx, a)
	if err != nil || v.row.ID != a {
		t.Fatalf("rollback: %+v %v", v, err)
	}
	if got := e.fl.rolledBack(); len(got) != 1 || got[0] != a {
		t.Fatalf("rollbacks %v", got)
	}
	if _, err := e.s.rollbackNode(e.ctx, old); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot update") {
		t.Fatalf("old agent: %v", err)
	}
	if _, err := e.s.rollbackNode(e.ctx, gone); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline: %v", err)
	}
	if _, err := e.s.rollbackNode(e.ctx, "nod_nope"); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if len(e.fl.rolledBack()) != 1 {
		t.Fatal("nothing is sent to a node that cannot or is not there")
	}
	e.fl.onRollback = func(string) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: false, Error: "no_previous"}, nil
	}
	if _, err := e.s.rollbackNode(e.ctx, a); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "agent refused: no_previous") {
		t.Fatalf("refused: %v", err)
	}
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 20)
	found := 0
	for _, r := range rows {
		if r.Action == "update_rollback_node" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("one audit row for the rollback that worked, %d found", found)
	}
}

// A manual rollback of a node that is in a running rollout marks its step and pauses the rollout.
func TestRollbackNodeInARollout(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 0})
	e.addNode("b", nodeOpts{online: 1})
	e.startRollout()
	e.tick()
	e.upgrade(a)
	e.tick()
	e.wantStep(a, store.StepGating, "")
	if _, err := e.s.rollbackNode(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.wantStep(a, store.StepRolledBack, "manual")
	e.wantRollout(store.RolloutPaused, "owner")
	if e.s.Updating(a) {
		t.Fatal("a rolled back node is not updating")
	}
	if conds := e.s.Conditions(e.ctx); len(conds) != 0 {
		t.Fatalf("the owner's own rollback raises no alert: %+v", conds)
	}
}

func TestOnlyOneActiveRolloutInTheDatabase(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	e.addNode("a", nodeOpts{})
	e.startRollout()
	// even a second writer that skipped the check in start cannot create one
	err := e.st.CreateRollout(e.ctx, store.RolloutRow{ID: "rol_second", Status: store.RolloutRunning, ToVersion: "v", ToBuilt: 1,
		Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 1, CreatedAt: e.clk.Now()}, nil)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second active rollout: %v", err)
	}
}

func TestStepUpIsRequiredForEveryChange(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{})
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("confirm it is you first (step-up required)"))
	e.stepUp = func(context.Context) error { return denied }
	r := rpc{e.s}

	calls := map[string]func() error{
		"InstallPanelUpdate": func() error {
			_, err := r.InstallPanelUpdate(e.ctx, connect.NewRequest(&adminv1.InstallPanelUpdateRequest{}))
			return err
		},
		"StartRollout": func() error {
			_, err := r.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{}))
			return err
		},
		"PauseRollout": func() error {
			_, err := r.PauseRollout(e.ctx, connect.NewRequest(&adminv1.PauseRolloutRequest{}))
			return err
		},
		"ResumeRollout": func() error {
			_, err := r.ResumeRollout(e.ctx, connect.NewRequest(&adminv1.ResumeRolloutRequest{}))
			return err
		},
		"CancelRollout": func() error {
			_, err := r.CancelRollout(e.ctx, connect.NewRequest(&adminv1.CancelRolloutRequest{}))
			return err
		},
		"RollbackNode": func() error {
			_, err := r.RollbackNode(e.ctx, connect.NewRequest(&adminv1.RollbackNodeRequest{NodeId: a}))
			return err
		},
		"RescanBundle": func() error {
			_, err := r.RescanBundle(e.ctx, connect.NewRequest(&adminv1.RescanBundleRequest{}))
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil || connectCode(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "step-up required") {
			t.Errorf("%s without a step-up: %v", name, err)
		}
	}
	if _, err := e.st.LatestRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a rollout was started without a step-up")
	}
	if len(e.fl.rolledBack()) != 0 {
		t.Fatal("a rollback was sent without a step-up")
	}
	if rows, _ := e.st.ListAudit(e.ctx, "", 0, 20); len(rows) != 0 {
		t.Fatalf("audit rows without a step-up: %+v", rows)
	}
	// the page itself never asks
	if resp, err := r.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{})); err != nil || len(resp.Msg.Nodes) != 1 {
		t.Fatalf("GetUpdates: %v", err)
	}
	// with a fresh step-up the same calls work
	e.stepUp = func(context.Context) error { return nil }
	if _, err := r.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{NodeIds: []string{a}})); err != nil {
		t.Fatal(err)
	}
	resp, err := r.PauseRollout(e.ctx, connect.NewRequest(&adminv1.PauseRolloutRequest{}))
	if err != nil || resp.Msg.Rollout.Status != adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED || resp.Msg.Rollout.PauseKey != "updates.pause.owner" {
		t.Fatalf("pause: %+v %v", resp, err)
	}
	if _, err := r.RescanBundle(e.ctx, connect.NewRequest(&adminv1.RescanBundleRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestGetUpdatesMessage(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("a", nodeOpts{online: 3, inbounds: 2})
	e.addNode("b", nodeOpts{caps: []string{"doctor/1", capUpdate}, built: oldBuilt}) // no crash guard
	c := e.addNode("c", nodeOpts{built: newBuilt})
	e.addNode("d", nodeOpts{caps: []string{"doctor/1"}, built: 0}) // unsupported
	e.exec(`UPDATE node SET last_update_json = ? WHERE id = ?`, store.LastUpdateRow{Outcome: "rolled_back", FromVersion: "x", ToVersion: "y", ToBuilt: newBuilt, Reason: "crash_loop", AtUnix: 5}.JSON(), a)
	e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_pending', 'pending', 'p.example.com', 'pending', 1)`)
	resp := e.get()
	if resp.Panel.Version != "0.2.0-test" || resp.Panel.Built != newBuilt || !resp.Panel.HasReleaseKey || len(resp.Panel.ReleaseKeyFingerprint) != 16 {
		t.Fatalf("panel %+v", resp.Panel)
	}
	if resp.Bundle.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED || resp.Bundle.Version != "0.2.0-new" || len(resp.Bundle.Files) != 2 || resp.NowUnix != e.clk.Now().Unix() {
		t.Fatalf("bundle %+v", resp.Bundle)
	}
	if resp.Rollout != nil {
		t.Fatal("no rollout yet")
	}
	var names []string
	for _, n := range resp.Nodes {
		names = append(names, n.Name)
	}
	// problems first: a is ROLLED_BACK, then b OUTDATED; then the rest by name (c up to date, d unsupported)
	if strings.Join(names, ",") != "a,b,c,d" {
		t.Fatalf("order %v", names)
	}
	na := resp.Nodes[0]
	if na.State != adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK || !na.SupportsUpdate || !na.CrashGuard || na.Inbounds != 2 || na.OnlineUsers != 3 ||
		na.LastUpdate == nil || na.LastUpdate.Outcome != "rolled_back" || na.LastUpdate.Reason != "crash_loop" || na.Version != "0.1.0-old" || na.Built != oldBuilt {
		t.Fatalf("node a %+v", na)
	}
	if nb := resp.Nodes[1]; nb.CrashGuard || !nb.SupportsUpdate || nb.LastUpdate != nil {
		t.Fatalf("node b %+v", nb)
	}
	if nd := resp.Nodes[3]; nd.SupportsUpdate || nd.State != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED {
		t.Fatalf("node d %+v", nd)
	}
	_ = c

	// a rollout in progress: the active one is shown, its node is UPDATING; a finished one is shown when none is active
	e.startRollout(resp.Nodes[1].NodeId)
	e.tick()
	resp = e.get()
	if resp.Rollout.Status != adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING || len(resp.Rollout.Steps) != 1 || resp.Rollout.Steps[0].State != adminv1.StepState_STEP_STATE_SENT ||
		resp.Rollout.ToVersion != "0.2.0-new" || resp.Rollout.ToBuilt != newBuilt || resp.Rollout.BatchSize != 1 {
		t.Fatalf("rollout %+v", resp.Rollout)
	}
	for _, n := range resp.Nodes {
		if (n.Name == "b") != (n.State == adminv1.NodeUpdateState_NODE_UPDATE_STATE_UPDATING) {
			t.Errorf("node %s is %v", n.Name, n.State)
		}
	}
	e.s.cancel(e.ctx, "")
	e.upgrade(resp.Nodes[0].NodeId)
	e.tick()
	e.clk.Advance(time.Hour)
	if e.get().Rollout == nil {
		t.Fatal("the last rollout stays visible")
	}
}

// Step errors and the pause reason go out as whole i18n keys.
func TestRolloutMessageKeys(t *testing.T) {
	ro := store.RolloutRow{ID: "rol_x", Status: store.RolloutPaused, PauseKey: "gate_failed", BatchSize: 2,
		PauseParams: map[string]string{"node": "de1", "node_id": "nod_1", "reason": "probe_failed"}, CreatedAt: time.Unix(10, 0)}
	m := rolloutMsg(ro, []store.StepRow{
		{NodeID: "nod_1", NodeName: "de1", Stage: 0, State: store.StepRolledBack, ErrorKey: "probe_failed", SentAt: time.Unix(20, 0), FinishedAt: time.Unix(30, 0)},
		{NodeID: "nod_2", NodeName: "nl1", Stage: 1, State: store.StepPending},
	})
	if m.PauseKey != "updates.pause.gate_failed" || m.PauseParams["node"] != "de1" || m.PauseParams["reason"] != "probe_failed" || len(m.PauseParams) != 2 {
		t.Fatalf("pause %+v", m)
	}
	s0, s1 := m.Steps[0], m.Steps[1]
	if s0.ErrorKey != "updates.step.err.probe_failed" || s0.StartedUnix != 20 || s0.FinishedUnix != 30 || s0.State != adminv1.StepState_STEP_STATE_ROLLED_BACK ||
		s1.ErrorKey != "" || s1.StartedUnix != 0 || s1.FinishedUnix != 0 || s1.State != adminv1.StepState_STEP_STATE_PENDING || s1.Stage != 1 {
		t.Fatalf("steps %+v %+v", s0, s1)
	}
	ro.Status, ro.FinishedAt = store.RolloutDone, time.Unix(99, 0)
	if m := rolloutMsg(ro, nil); m.PauseKey != "" || m.FinishedUnix != 99 || m.Status != adminv1.RolloutStatus_ROLLOUT_STATUS_DONE {
		t.Fatalf("finished %+v", m)
	}
}

func TestRetention(t *testing.T) {
	e := newEnv(t)
	for i := range 25 {
		at := e.clk.Now().Add(-time.Duration(200+i) * 24 * time.Hour)
		err := e.st.CreateRollout(e.ctx, store.RolloutRow{ID: fmt.Sprintf("rol_%02d", i), Status: store.RolloutDone, ToVersion: "v", ToBuilt: 1,
			Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 1, CreatedAt: at, FinishedAt: at.Add(time.Hour)}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	e.s.pruneOld(e.ctx)
	var n int
	e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM update_rollout`).Scan(&n)
	if n != keepRollouts {
		t.Fatalf("%d rollouts left, want %d", n, keepRollouts)
	}
}

// Run processes an explicitly started per-node update and drives it to completion.
func TestRunLoop(t *testing.T) {
	e := newEnv(t)
	e.clk = &clock{t: time.Now()}
	e.s = e.newService(e.pub)
	e.s.cfg.Tick = 10 * time.Millisecond
	e.defaultBundle()
	syncCurrentTestBundleFromGitHub(e)
	a := e.addNode("a", nodeOpts{})
	e.fl.onUpdate = func(id string) (*agentv1.CommandResult, error) {
		go func() { // the agent re-executes and comes back with the new build
			time.Sleep(30 * time.Millisecond)
			e.clk.Advance(time.Second)
			e.upgrade(id)
			time.Sleep(30 * time.Millisecond)
			e.commit(id)
		}()
		return okResult(), nil
	}
	if _, err := e.s.start(e.ctx, []string{a}, 0); err != nil {
		t.Fatalf("start selected node update: %v", err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ro, err := e.st.LatestRollout(e.ctx); err == nil && ro.Status == store.RolloutDone {
			e.wantStep(a, store.StepPassed, "")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the rollout did not finish: %+v", e.rollout())
}
