package fleet

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// fakeHealth records what the stream hands over and answers what the admin views ask.
type fakeHealth struct {
	mu         sync.Mutex
	reports    []*agentv1.DoctorReport
	returned   []time.Time
	noTraffic  bool
	failed     int
	total      int
	doctorFail string
	active     uint32
	critical   uint32
}

func (h *fakeHealth) DoctorReport(_ context.Context, _ string, r *agentv1.DoctorReport) {
	h.mu.Lock()
	h.reports = append(h.reports, r)
	h.mu.Unlock()
}

func (h *fakeHealth) NodeReturned(_ context.Context, _ string, silentSince, _ time.Time) {
	h.mu.Lock()
	h.returned = append(h.returned, silentSince)
	h.mu.Unlock()
}

func (h *fakeHealth) NodeHealth(string) (bool, int, int, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.noTraffic, h.failed, h.total, h.doctorFail
}

func (h *fakeHealth) AlertCounts(context.Context) (uint32, uint32) { return h.active, h.critical }

func (h *fakeHealth) nReports() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reports)
}

func helloWithDoctor(instance string) *agentv1.ConnectRequest {
	m := hello(instance, 0, "")
	m.GetHello().Capabilities = []string{"doctor/1"}
	return m
}

// connectDoctor connects an agent that lists the doctor capability and confirms the full state.
func connectDoctor(a *agent, instance string) *conn {
	a.e.t.Helper()
	c := a.open()
	c.send(0, helloWithDoctor(instance))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	m := newModel()
	m.apply(ds)
	c.send(0, applied(ds, m.hash()))
	return c
}

// A panel with an agent that predates the doctor sends it nothing and says why.
func TestOldAgentIsNeverSentDoctorCommands(t *testing.T) {
	e := newEnv(t)
	e.f.unit = 10 * time.Millisecond
	a := e.enroll("nodea")
	e.fixture(a.nodeID)

	if _, err := e.f.RunDoctor(e.ctx, a.nodeID, nil, time.Second); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_offline" {
		t.Fatalf("offline: %v", err)
	}
	if up, _, _ := e.f.Live(a.nodeID); up {
		t.Fatal("live before connecting")
	}

	c, _, _ := connectFull(a, "inst1") // no capabilities in this Hello
	up, caps, drift := e.f.Live(a.nodeID)
	if !up || len(caps) != 0 || drift {
		t.Fatalf("live: %v %v %v", up, caps, drift)
	}
	_, err := e.f.RunDoctor(e.ctx, a.nodeID, nil, time.Second)
	if code(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "agent too old") {
		t.Fatalf("RunDoctor on an old agent: %v", err)
	}
	_, err = e.f.ApplyFix(e.ctx, a.nodeID, "journald_vacuum", true, nil)
	if code(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "agent too old") {
		t.Fatalf("ApplyFix on an old agent: %v", err)
	}
	// nothing reached the agent
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case m := <-c.in:
			if m.GetRunDoctor() != nil || m.GetApplyFix() != nil {
				t.Fatalf("an old agent was sent %v", m)
			}
		case <-deadline:
			return
		}
	}
}

// A new agent: RunDoctor and ApplyFix are relayed, the report reaches Health (periodic ones too), and the
// answer to a request is the report that echoes its id.
func TestDoctorRelay(t *testing.T) {
	e := newEnv(t)
	e.f.unit = 20 * time.Millisecond // apply timeout 120 -> 2.4 s
	h := &fakeHealth{}
	e.f.SetHealth(h)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	e.exec(`UPDATE node SET liveness_timeout_s = 3600`)
	c := connectDoctor(a, "inst1")
	if _, caps, _ := e.f.Live(a.nodeID); len(caps) != 1 || caps[0] != "doctor/1" {
		t.Fatalf("capabilities %v", caps)
	}

	// the periodic report: request_id empty, goes to Health, wakes nobody
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DoctorReport{DoctorReport: &agentv1.DoctorReport{
		Results: []*agentv1.DoctorResult{{Id: "disk_space", Status: agentv1.DoctorStatus_DOCTOR_STATUS_OK}}}}})
	deadline := time.Now().Add(2 * time.Second)
	for h.nReports() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.nReports() != 1 {
		t.Fatalf("periodic report not handed over: %d", h.nReports())
	}

	// RunDoctor with checks: the agent answers with the echoing report
	go func() {
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetRunDoctor() != nil })
		rd := m.GetRunDoctor()
		if len(rd.Checks) != 1 || rd.Checks[0] != "resolver" {
			t.Errorf("checks %v", rd.Checks)
		}
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DoctorReport{DoctorReport: &agentv1.DoctorReport{
			RequestId: rd.RequestId, Partial: true, DurationMs: 12,
			Results: []*agentv1.DoctorResult{{Id: "resolver", Status: agentv1.DoctorStatus_DOCTOR_STATUS_WARN, FixId: "set_resolver"}}}}})
	}()
	rep, err := e.f.RunDoctor(e.ctx, a.nodeID, []string{"resolver"}, 2*time.Second)
	if err != nil || !rep.Partial || len(rep.Results) != 1 || rep.Results[0].FixId != "set_resolver" {
		t.Fatalf("RunDoctor: %+v %v", rep, err)
	}
	if h.nReports() != 2 { // handed to Health before the call returned
		t.Fatalf("reports at Health: %d", h.nReports())
	}

	// a run that is never answered times out and the stream stays up
	if _, err := e.f.RunDoctor(e.ctx, a.nodeID, nil, 100*time.Millisecond); code(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("no answer: %v", err)
	}
	if e.f.session(a.nodeID) == nil {
		t.Fatal("the stream dropped")
	}

	// ApplyFix: dry run and the real thing, each answered by one CommandResult with the agent's facts
	go func() {
		for range 2 {
			m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetApplyFix() != nil })
			af := m.GetApplyFix()
			if af.FixId != "restart_inbound" || af.Params["inbound_id"] != "inb_x" {
				t.Errorf("apply fix: %+v", af)
			}
			res := &agentv1.CommandResult{RequestId: af.RequestId, Ok: true, Detail: "would restart", Params: map[string]string{"n": "1"}}
			if !af.DryRun {
				res.Affected, res.Detail = 1, "restarted"
			}
			c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: res}})
		}
	}()
	dry, err := e.f.ApplyFix(e.ctx, a.nodeID, "restart_inbound", true, map[string]string{"inbound_id": "inb_x"})
	if err != nil || !dry.Ok || dry.Detail != "would restart" || dry.Params["n"] != "1" || dry.Affected != 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	done, err := e.f.ApplyFix(e.ctx, a.nodeID, "restart_inbound", false, map[string]string{"inbound_id": "inb_x"})
	if err != nil || !done.Ok || done.Affected != 1 || done.Detail != "restarted" {
		t.Fatalf("real run: %+v %v", done, err)
	}
}

// The status of a node and the Overview carry what Health says.
func TestHealthSeamFeedsStatusAndOverview(t *testing.T) {
	e := newEnv(t)
	h := &fakeHealth{active: 3, critical: 1}
	e.f.SetHealth(h)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	connectFull(a, "inst1")
	nodes := nodeService{e.f}
	statusOf := func() (adminv1.NodeStatus, *adminv1.StatusReason) {
		t.Helper()
		resp, err := nodes.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Node.Status, resp.Msg.Node.Reason
	}
	overview := func() *adminv1.OverviewResponse {
		t.Helper()
		resp, err := fleetService{e.f}.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}

	if st, r := statusOf(); st != adminv1.NodeStatus_NODE_STATUS_ONLINE || r != nil {
		t.Fatalf("healthy: %v %v", st, r)
	}
	base := overview().NodesProblem // the fixture has one more node that never connected
	if o := overview(); o.AlertsActive != 3 || o.AlertsCritical != 1 {
		t.Fatalf("overview: %+v", o)
	}

	h.mu.Lock()
	h.doctorFail = "dstate_tasks"
	h.mu.Unlock()
	if st, r := statusOf(); st != adminv1.NodeStatus_NODE_STATUS_ONLINE || r == nil || r.Code != "doctor_fail" || r.Params["check"] != "dstate_tasks" {
		t.Fatalf("doctor fail: %v %v", st, r)
	}

	h.mu.Lock()
	h.noTraffic, h.failed, h.total = true, 2, 2
	h.mu.Unlock()
	st, r := statusOf()
	if st != adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC || r.Code != "no_traffic" || r.Params["failed"] != "2" || r.Params["total"] != "2" {
		t.Fatalf("no traffic: %v %v", st, r)
	}
	if o := overview(); o.NodesProblem != base+1 {
		t.Fatalf("a node without traffic is a problem node: %+v", o)
	}
	list, _ := nodes.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if list.Msg.Nodes[0].Status != adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC || e.f.NodeStatus(e.ctx, mustNode(e, a.nodeID)) != adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC {
		t.Fatalf("list status %v", list.Msg.Nodes[0].Status)
	}
	var cardStatus adminv1.NodeStatus
	for _, c := range overview().Nodes {
		if c.Id == a.nodeID {
			cardStatus = c.Status
		}
	}
	if cardStatus != adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC {
		t.Fatalf("card status %v", cardStatus)
	}

	e.f.SetHealth(nil) // without Health nothing changes
	if st, _ := statusOf(); st != adminv1.NodeStatus_NODE_STATUS_ONLINE {
		t.Fatalf("without health: %v", st)
	}
}

func TestDriftIsVisibleToHealth(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, m, ds := connectFull(a, "inst1")
	// the agent keeps reporting a different hash after the automatic full resend: drift persists
	c.send(0, applied(ds, "0000"))
	ds2 := c.desired()
	m.apply(ds2)
	c.send(0, applied(ds2, "0000"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, drift := e.f.Live(a.nodeID); drift {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("persisting drift is not visible")
}

// A node that returns after a short silence with the same boot time is a blip record for Health; a reboot or a
// return after node_down is not.
func TestBlipIsHandedToHealthOnlyWhenTheHostDidNotReboot(t *testing.T) {
	e := newEnv(t)
	h := &fakeHealth{}
	e.f.SetHealth(h)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	c.st.CloseRequest()
	c.ended()
	time.Sleep(100 * time.Millisecond)
	e.exec(`UPDATE node SET last_seen_at = last_seen_at - 180, last_disconnected_at = last_disconnected_at - 180, last_connected_at = last_connected_at - 180 WHERE id = ?`, a.nodeID)

	c2 := a.open()
	c2.send(0, hello("inst1", 0, "")) // same BootUnix
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if len(h.returned) != 1 || time.Since(h.returned[0]) < 150*time.Second {
		t.Fatalf("blip not handed over: %v", h.returned)
	}
	if e.count(`SELECT count(*) FROM event WHERE code = 'node_blip'`) != 1 {
		t.Fatal("the existing node_blip event is gone")
	}

	c2.st.CloseRequest()
	c2.ended()
	time.Sleep(100 * time.Millisecond)
	e.exec(`UPDATE node SET last_seen_at = last_seen_at - 180, last_disconnected_at = last_disconnected_at - 180, last_connected_at = last_connected_at - 180 WHERE id = ?`, a.nodeID)
	c3 := a.open()
	rebooted := hello("inst1", 0, "")
	rebooted.GetHello().Facts.BootUnix = 99999 // a real reboot
	c3.send(0, rebooted)
	c3.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if len(h.returned) != 1 {
		t.Fatalf("a reboot became a blip record: %v", h.returned)
	}
}

// The node's country reaches the agent (the doctor's resolver check needs it) and a change is re-sent.
func TestCountryCodeReachesTheAgent(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	e.exec(`UPDATE node SET country_code = 'RU' WHERE id = ?`, a.nodeID)
	c := a.open()
	c.send(0, hello("inst1", 0, ""))
	ack := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil }).GetHelloAck()
	if ack.Settings.CountryCode != "RU" {
		t.Fatalf("settings in HelloAck: %+v", ack.Settings)
	}
	if got := strings.Join(ack.Settings.DnsResolvers, ","); got != "77.88.8.8,77.88.8.1" {
		t.Fatalf("Russian DNS resolvers in HelloAck = %q", got)
	}
	ds := c.desired()
	if ds.Settings.CountryCode != "RU" {
		t.Fatalf("settings in DesiredState: %+v", ds.Settings)
	}
	if got := strings.Join(ds.Settings.DnsResolvers, ","); got != "77.88.8.8,77.88.8.1" {
		t.Fatalf("Russian DNS resolvers in DesiredState = %q", got)
	}
	m := newModel()
	m.apply(ds)
	c.send(0, applied(ds, m.hash()))

	e.run()
	de := "de"
	e.exec(`UPDATE node SET liveness_timeout_s = 3600`)
	if _, err := (nodeService{e.f}).UpdateNode(e.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, CountryCode: &de})); err != nil {
		t.Fatal(err)
	}
	next := c.desired()
	if next.Settings.CountryCode != "DE" {
		t.Fatalf("a changed country was not re-sent: %+v", next.Settings)
	}
	if got := strings.Join(next.Settings.DnsResolvers, ","); got != "1.1.1.1,8.8.8.8" {
		t.Fatalf("world DNS resolvers after country change = %q", got)
	}
}

func mustNode(e *env, id string) store.NodeRow {
	n, err := e.st.Node(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}
