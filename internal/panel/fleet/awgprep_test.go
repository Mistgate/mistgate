package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The panel side of the automatic kernel-module build: the RPC, the capability gate, what is stored while it runs, and the
// one rule everything hangs on: awg_backend becomes "kernel" only after the node said the module was built and loaded.

var prepCaps = []string{"doctor/1", "awg/1", "awg-prepare/1"}

// answerPrepare plays the agent: it answers the next PrepareAwgKernel with params and hands the request back.
func answerPrepare(c *conn, params map[string]string) <-chan *agentv1.PrepareAwgKernel {
	got := make(chan *agentv1.PrepareAwgKernel, 1)
	go func() {
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetPrepareAwgKernel() != nil })
		p := m.GetPrepareAwgKernel()
		got <- p
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
			RequestId: p.RequestId, Ok: true, Params: params}}})
	}()
	return got
}

func prepEvent(seq uint64, code string, params map[string]string) *agentv1.ConnectRequest {
	sev := agentv1.Severity_SEVERITY_INFO
	if code == "awg_kernel_prepare_failed" {
		sev = agentv1.Severity_SEVERITY_ERROR
	}
	return &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Severity: sev, Code: code, TimeUnix: time.Now().Unix(), Params: params}}}
}

func (x *l3Env) prep() (store.AwgPrepareRow, string) {
	x.t.Helper()
	n, err := x.st.Node(x.ctx, x.nodeIDOf())
	if err != nil {
		x.t.Fatal(err)
	}
	return n.AwgPrepare(), n.AwgBackend
}

func callPrepare(x *l3Env, nodeID string, confirm bool) (*adminv1.PrepareAwgKernelResponse, error) {
	r, err := (nodeService{x.f}).PrepareAwgKernel(x.ctx, connect.NewRequest(&adminv1.PrepareAwgKernelRequest{NodeId: nodeID, Confirm: confirm}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func TestPrepareAwgKernelNeedsAConnectedAgentThatListsTheCapability(t *testing.T) {
	x, a := newL3Env(t)
	if _, err := callPrepare(x, "nod_nope", false); code(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
	if _, err := callPrepare(x, a.nodeID, true); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_offline" {
		t.Errorf("offline: %v", err)
	}
	connectCaps(a, "old", "doctor/1", "awg/1") // an agent from before this feature
	if _, err := callPrepare(x, a.nodeID, true); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Errorf("an agent without awg-prepare/1: %v", err)
	}
	if r, _ := x.prep(); r != (store.AwgPrepareRow{}) {
		t.Errorf("a refused request left a record: %+v", r)
	}
}

func TestPrepareAwgKernelDryRunChangesNothing(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", prepCaps...)
	for state, want := range map[string]adminv1.PrepareAwgOutcome{
		"ready":         adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_READY,
		"needs_prepare": adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_NEEDS_PREPARE,
	} {
		req := answerPrepare(c, map[string]string{"state": state})
		resp, err := callPrepare(x, a.nodeID, false)
		if err != nil || resp.Outcome != want {
			t.Fatalf("%s: %v %v", state, resp, err)
		}
		if p := <-req; !p.DryRun {
			t.Errorf("%s: the question was not a dry run", state)
		}
		if r, b := x.prep(); r != (store.AwgPrepareRow{}) || b != "auto" {
			t.Errorf("%s: dry run changed the record: %+v %q", state, r, b)
		}
	}
	req := answerPrepare(c, map[string]string{"state": "unsupported", "code": "container", "reason": "this is a lxc container"})
	resp, err := callPrepare(x, a.nodeID, false)
	<-req
	if err != nil || resp.Outcome != adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_UNSUPPORTED || resp.ReasonCode != "container" || resp.Reason == "" {
		t.Fatalf("unsupported: %v %v", resp, err)
	}
	if !resp.Node.AwgPrepare.Supported {
		t.Error("the node view says the agent cannot prepare, it listed the capability")
	}
}

func TestConfirmedPrepareKeepsTheBackendAndRecordsTheWish(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", prepCaps...)
	req := answerPrepare(c, map[string]string{"state": "started", "kernel": "6.8.0-142-generic"})
	resp, err := callPrepare(x, a.nodeID, true)
	if err != nil || resp.Outcome != adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_STARTED {
		t.Fatalf("start: %v %v", resp, err)
	}
	if p := <-req; p.DryRun {
		t.Error("a confirmed request was sent as a dry run")
	}
	r, backend := x.prep()
	if r.State != store.AwgPrepareRunning || !r.Want || r.Since == 0 || backend != "auto" {
		t.Fatalf("record = %+v backend %q: the backend must stay what it was while the module builds", r, backend)
	}
	ap := resp.Node.AwgPrepare
	if ap.State != adminv1.AwgPrepareState_AWG_PREPARE_STATE_RUNNING || ap.SinceUnix != r.Since || !ap.Supported || resp.Node.AwgBackend != "auto" {
		t.Errorf("node view = %+v", ap)
	}
	// a second press while it runs: the node says "running", the panel keeps the first start
	req = answerPrepare(c, map[string]string{"state": "running", "since_unix": "1"})
	resp, err = callPrepare(x, a.nodeID, true)
	<-req
	if err != nil || resp.Outcome != adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_RUNNING {
		t.Fatalf("repeat: %v %v", resp, err)
	}
	if r2, _ := x.prep(); r2.State != store.AwgPrepareRunning || r2.Since != r.Since || !r2.Want {
		t.Errorf("repeat changed the record: %+v", r2)
	}
	// the audit row names the press
	if x.count(`SELECT count(*) FROM audit WHERE action = 'node.awg_prepare'`) != 1 {
		t.Error("the confirmed request left no audit row")
	}
}

// A request the node does not carry out (the module is already there, the host cannot) leaves no wish behind.
func TestConfirmedPrepareThatStartsNothingKeepsNoWish(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", prepCaps...)
	for _, state := range []string{"ready", "unsupported"} {
		req := answerPrepare(c, map[string]string{"state": state, "code": "secure_boot", "reason": "Secure Boot is on"})
		if _, err := callPrepare(x, a.nodeID, true); err != nil {
			t.Fatal(err)
		}
		<-req
		if r, _ := x.prep(); r != (store.AwgPrepareRow{}) {
			t.Errorf("%s left a record: %+v", state, r)
		}
	}
	if x.count(`SELECT count(*) FROM audit WHERE action = 'node.awg_prepare'`) != 0 {
		t.Error("an audit row for a request that started nothing")
	}
}

func TestConfirmedPrepareThatFailsToReachTheNodeRestoresTheRecord(t *testing.T) {
	x, a := newL3Env(t)
	x.f.unit = 20 * time.Millisecond // apply timeout 120 -> 2.4 s
	x.exec(`UPDATE node SET liveness_timeout_s = 3600`)
	connectCaps(a, "new", prepCaps...)
	_ = x.st.SetAwgPrepare(x.ctx, a.nodeID, store.AwgPrepareRow{State: store.AwgPrepareFailed, Since: 5, Code: "timeout", Reason: "slow"})
	if _, err := callPrepare(x, a.nodeID, true); code(err) != connect.CodeDeadlineExceeded { // nobody answers
		t.Fatalf("no answer: %v", err)
	}
	if r, _ := x.prep(); r.State != store.AwgPrepareFailed || r.Code != "timeout" || r.Want {
		t.Errorf("the record after a request that did not get through = %+v", r)
	}
}

// The rule: the backend is "kernel" only after "done". A failed build leaves it alone and says why; a later success switches
// it and pushes the setting to the node.
func TestBackendSwitchesToKernelOnlyAfterTheNodeSaysDone(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", prepCaps...)
	start := func() {
		req := answerPrepare(c, map[string]string{"state": "started", "kernel": "6.8.0"})
		if _, err := callPrepare(x, a.nodeID, true); err != nil {
			t.Fatal(err)
		}
		<-req
	}

	start()
	c.send(1, prepEvent(1, "awg_kernel_prepare_started", map[string]string{"kernel": "6.8.0"}))
	c.send(2, prepEvent(2, "awg_kernel_prepare_failed", map[string]string{"code": "step_failed", "reason": "step 2 of 5 failed: install the build tools", "minutes": "3"}))
	within(t, "the failure is recorded", func() bool { r, _ := x.prep(); return r.State == store.AwgPrepareFailed })
	r, backend := x.prep()
	if r.Code != "step_failed" || r.Reason == "" || r.Want || backend != "auto" {
		t.Fatalf("after a failure: %+v backend %q", r, backend)
	}
	c.quiet(300 * time.Millisecond) // nothing is pushed to the node for a failure

	// the node view says it in the words the UI needs
	n, _ := x.st.Node(x.ctx, a.nodeID)
	if m := awgPrepareMsg(n, prepCaps, x.f.now()); m.State != adminv1.AwgPrepareState_AWG_PREPARE_STATE_FAILED || m.ReasonCode != "step_failed" || m.Reason == "" {
		t.Errorf("failed view = %+v", m)
	}

	// retry and succeed
	start()
	c.send(3, prepEvent(3, "awg_kernel_prepare_started", nil))
	c.send(4, prepEvent(4, "awg_kernel_prepare_done", map[string]string{"kernel": "6.8.0", "minutes": "2"}))
	d := c.desired()
	if d.Settings == nil || d.Settings.AwgBackend != "kernel" || len(d.Inbounds) != 0 {
		t.Fatalf("the settings the node got after done = %v", d)
	}
	r, backend = x.prep()
	if r.State != store.AwgPrepareDone || r.Want || backend != "kernel" {
		t.Fatalf("after done: %+v backend %q", r, backend)
	}
	if x.count(`SELECT count(*) FROM event WHERE code = 'awg_kernel_switched'`) != 1 {
		t.Error("the switch left no event")
	}
	for _, code := range []string{"awg_kernel_prepare_started", "awg_kernel_prepare_failed", "awg_kernel_prepare_done"} {
		if x.count(`SELECT count(*) FROM event WHERE code = ?`, code) == 0 {
			t.Errorf("the node's %s event is not in the event list", code)
		}
	}
}

// The admin changed their mind while it built: the finished module does not override the choice.
func TestAFinishedBuildDoesNotOverrideAChoiceMadeMeanwhile(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", prepCaps...)
	req := answerPrepare(c, map[string]string{"state": "started"})
	if _, err := callPrepare(x, a.nodeID, true); err != nil {
		t.Fatal(err)
	}
	<-req
	if _, err := (nodeService{x.f}).UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, AwgBackend: ptr("userspace")})); err != nil {
		t.Fatal(err)
	}
	c.desired() // the settings delta of that choice
	c.send(1, prepEvent(1, "awg_kernel_prepare_done", map[string]string{"kernel": "6.8.0", "minutes": "2"}))
	within(t, "done is recorded", func() bool { r, _ := x.prep(); return r.State == store.AwgPrepareDone })
	if _, backend := x.prep(); backend != "userspace" {
		t.Fatalf("backend = %q: a build that finished overrode the admin", backend)
	}
	c.quiet(300 * time.Millisecond)
}

// An event the node queued and sends again after a reconnect (a duplicate seq) is not applied twice.
func TestADuplicateDoneEventSwitchesOnce(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", prepCaps...)
	req := answerPrepare(c, map[string]string{"state": "started"})
	if _, err := callPrepare(x, a.nodeID, true); err != nil {
		t.Fatal(err)
	}
	<-req
	c.send(1, prepEvent(1, "awg_kernel_prepare_done", map[string]string{"minutes": "1"}))
	d := c.desired()
	if d.Settings == nil || d.Settings.AwgBackend != "kernel" {
		t.Fatalf("settings = %v", d)
	}
	// the admin goes back to userspace; the same event arrives again: it must not flip the node a second time
	if _, err := (nodeService{x.f}).UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, AwgBackend: ptr("userspace")})); err != nil {
		t.Fatal(err)
	}
	c.desired()
	c.send(1, prepEvent(1, "awg_kernel_prepare_done", map[string]string{"minutes": "1"}))
	c.quiet(300 * time.Millisecond)
	if _, backend := x.prep(); backend != "userspace" {
		t.Fatalf("backend = %q", backend)
	}
}

func TestNodeViewSaysWhetherTheAgentCanPrepareAndDoesNotSpinForever(t *testing.T) {
	n := store.NodeRow{AgentCaps: []string{"awg/1"}}
	now := time.Unix(100_000, 0)
	if m := awgPrepareMsg(n, n.AgentCaps, now); m.Supported || m.State != adminv1.AwgPrepareState_AWG_PREPARE_STATE_NONE {
		t.Errorf("an old agent = %+v", m)
	}
	n.AwgPrepareJSON = `{"state":"running","since":99000,"want":true}`
	if m := awgPrepareMsg(n, prepCaps, now); !m.Supported || m.State != adminv1.AwgPrepareState_AWG_PREPARE_STATE_RUNNING || m.SinceUnix != 99000 {
		t.Errorf("a fresh run = %+v", m)
	}
	n.AwgPrepareJSON = `{"state":"running","since":1000,"want":true}`
	if m := awgPrepareMsg(n, prepCaps, now); m.State != adminv1.AwgPrepareState_AWG_PREPARE_STATE_FAILED || m.ReasonCode != "interrupted" {
		t.Errorf("a run nobody heard of for hours = %+v", m)
	}
}
