package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
	panelupdate "github.com/mistgate/mistgate/internal/panel/update"
)

// UpdateOs: the panel's one command for OS jobs. It reaches only an agent that listed os-update/1, carries only the enumerated
// fields, and the node's answer comes back whole (the updates module reads params.state).

var osCapsList = []string{"doctor/1", osupdate.Capability, osupdate.CapabilityUpdate}

func TestUpdateOsReachesOnlyAnAgentThatListsTheCapability(t *testing.T) {
	x, a := newL3Env(t)
	req := panelupdate.OsJobRequest{Action: "upgrade", Mode: "security", Reboot: "if_required", JobID: "osj_abcdefghijklmnop"}

	if _, err := x.f.UpdateOs(x.ctx, a.nodeID, req, time.Second); err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("an offline node: %v", err)
	}
	connectCaps(a, "old", "doctor/1", osupdate.Capability) // reads its OS, cannot run a job
	if _, err := x.f.UpdateOs(x.ctx, a.nodeID, req, time.Second); err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Fatalf("an agent without os-update/1: %v", err)
	}

	c, _, _ := connectCaps(a, "new", osCapsList...)
	got := make(chan *agentv1.UpdateOs, 1)
	go func() {
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetUpdateOs() != nil })
		u := m.GetUpdateOs()
		got <- u
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
			RequestId: u.RequestId, Ok: true, Params: map[string]string{"state": osupdate.AnswerStarted, "job_id": u.JobId}}}})
	}()
	res, err := x.f.UpdateOs(x.ctx, a.nodeID, req, 5*time.Second)
	if err != nil || !res.Ok || res.Params["state"] != osupdate.AnswerStarted || res.Params["job_id"] != req.JobID {
		t.Fatalf("UpdateOs = %+v, %v", res, err)
	}
	u := <-got
	if u.Action != "upgrade" || u.Mode != "security" || u.Reboot != "if_required" || u.JobId != req.JobID || u.RequestId == "" {
		t.Fatalf("the agent got %+v", u)
	}
}

// An OS job and the AmneziaWG module build never run on a node side by side: the node refuses the second, and the panel does
// not even ask while it knows an update is in flight there.
func TestPrepareAwgKernelIsRefusedWhileAnUpdateIsInFlight(t *testing.T) {
	x, a := newL3Env(t)
	connectCaps(a, "new", prepCaps...)
	updates := &fakeUpdates{updating: map[string]bool{a.nodeID: true}}
	x.f.SetUpdates(updates)
	_, err := callPrepare(x, a.nodeID, true)
	if code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_updating" {
		t.Fatalf("confirmed prepare during an update: %v", err)
	}
	if r, _ := x.prep(); r != (store.AwgPrepareRow{}) {
		t.Fatalf("the refused request left a record: %+v", r)
	}
	// looking (a dry run) is allowed
	c, _, _ := connectCaps(a, "newer", prepCaps...)
	req := answerPrepare(c, map[string]string{"state": "ready"})
	if _, err := callPrepare(x, a.nodeID, false); err != nil {
		t.Fatalf("dry run during an update: %v", err)
	}
	<-req
}

// osWatcher is the updates module as far as a stored snapshot matters: a step that follows a job is told to look now.
type osWatcher struct {
	osStatusUpdates
	changed chan string
}

func (w osWatcher) OsStatusChanged(nodeID string) { w.changed <- nodeID }

func TestAStoredSnapshotWakesTheUpdatesModule(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "oswake")
	w := osWatcher{osStatusUpdates{&fakeUpdates{}}, make(chan string, 4)}
	e.f.SetUpdates(w)
	h := hello("instance-wake", 0, "").GetHello()
	h.Capabilities = []string{capOSStatus}
	tr := stepHello(t, core, ctx, state, now, h)
	if _, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: osStatusFrame(1, osStatusFixture(now))}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-w.changed:
		if id != state.NodeID {
			t.Fatalf("woken for %q, want %q", id, state.NodeID)
		}
	default:
		t.Fatal("the updates module was not told about the new snapshot")
	}
	// a rejected snapshot changes nothing and wakes nobody
	bad := osStatusFixture(now)
	bad.Pending, bad.Security = 1, 5
	if _, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: osStatusFrame(2, bad)}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-w.changed:
		t.Fatalf("woken by a rejected snapshot (%s)", id)
	default:
	}
}

func TestAnEndOfJobEventWakesTheUpdatesModule(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "osevent")
	w := osWatcher{osStatusUpdates{&fakeUpdates{}}, make(chan string, 4)}
	e.f.SetUpdates(w)
	tr := stepHello(t, core, ctx, state, now, hello("instance-event", 0, "").GetHello())
	for i, code := range []string{"os_update_started", "os_update_done"} {
		frame := &agentv1.ConnectRequest{Seq: uint64(i + 1), Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
			Severity: agentv1.Severity_SEVERITY_INFO, Code: code, TimeUnix: now.Unix(), Params: map[string]string{"job_id": "osj_abcdefghijklmnop"}}}}
		next, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: frame})
		if err != nil {
			t.Fatal(err)
		}
		tr = next
		woken := len(w.changed) > 0
		if want := code == "os_update_done"; woken != want {
			t.Fatalf("%s: woken = %v, want %v", code, woken, want)
		}
	}
}

// the events of an OS job carry the panel's time: the node's clock is not trusted to order them against the command
func TestOsJobEventsCarryThePanelTime(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "ostime")
	tr := stepHello(t, core, ctx, state, now, hello("instance-time", 0, "").GetHello())
	for i, code := range []string{"os_update_done", "os_update_failed", "os_reboot_started", "update_step_x"} {
		frame := &agentv1.ConnectRequest{Seq: uint64(i + 1), Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
			Severity: agentv1.Severity_SEVERITY_INFO, Code: code, TimeUnix: now.Add(-3 * time.Hour).Unix(), Params: map[string]string{"job_id": "osj_abcdefghijklmnop"}}}}
		next, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: frame})
		if err != nil {
			t.Fatal(err)
		}
		tr = next
		var ts int64
		if err := e.st.R.QueryRowContext(ctx, `SELECT ts FROM event WHERE node_id = ? AND code = ?`, state.NodeID, code).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		want := now.Add(-3 * time.Hour).Unix()
		if code != "update_step_x" {
			want = now.Unix()
		}
		if ts != want {
			t.Errorf("%s stored at %d, want %d", code, ts, want)
		}
	}
}
