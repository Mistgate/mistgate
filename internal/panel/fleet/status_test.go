package fleet

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// reasonOf is the status and reason of one node as the node page, the node list and the Overview card each show it:
// the three must agree.
func reasonOf(t *testing.T, e *env, id string) (adminv1.NodeStatus, *adminv1.StatusReason) {
	t.Helper()
	got, err := nodeService{e.f}.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
	if err != nil {
		t.Fatal(err)
	}
	list, err := nodeService{e.f}.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	ov, err := fleetService{e.f}.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	n := got.Msg.Node
	key := func(s adminv1.NodeStatus, r *adminv1.StatusReason) string {
		if r == nil {
			return s.String()
		}
		return s.String() + " " + r.Code + " " + strconv.Itoa(len(r.Params)) + r.Params["profile"] + r.Params["failed"] + r.Params["total"]
	}
	want := key(n.Status, n.Reason)
	for _, x := range list.Msg.Nodes {
		if x.Id == id && key(x.Status, x.Reason) != want {
			t.Errorf("node list says %s, node page %s", key(x.Status, x.Reason), want)
		}
	}
	for _, c := range ov.Msg.Nodes {
		if c.Id == id && key(c.Status, c.Reason) != want {
			t.Errorf("Overview card says %s, node page %s", key(c.Status, c.Reason), want)
		}
	}
	return n.Status, n.Reason
}

func setHealth(e *env, id string, h ...*agentv1.InboundHealth) {
	s := e.f.session(id)
	s.liveMu.Lock()
	s.health = h
	s.liveMu.Unlock()
}

// An online node is "healthy" only when someone can use it: without a single enabled profile it needs attention, and a
// failed profile names itself and says how many of the node's profiles are down (one of two works partly, two of two is
// broken).
func TestStatusCountsTheProfilesOfANode(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	connectFull(a, "inst1")

	st, r := reasonOf(t, e, a.nodeID)
	if st != adminv1.NodeStatus_NODE_STATUS_ONLINE || r == nil || r.Code != "no_profiles" {
		t.Fatalf("online without profiles: %v %v", st, r)
	}
	if e.f.NodeStatus(e.ctx, mustNode(e, a.nodeID)) != adminv1.NodeStatus_NODE_STATUS_ONLINE {
		t.Fatal("the status alone stays ONLINE")
	}

	ids := e.fixture(a.nodeID) // p1 (I1) and p2 (I2) on the node
	if st, r := reasonOf(t, e, a.nodeID); st != adminv1.NodeStatus_NODE_STATUS_ONLINE || r != nil {
		t.Fatalf("with profiles: %v %v", st, r)
	}

	running := agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING
	failed := agentv1.InboundRunState_INBOUND_RUN_STATE_FAILED
	setHealth(e, a.nodeID, &agentv1.InboundHealth{InboundId: ids.i1, State: failed, Detail: "listen udp :443: bind: address already in use"},
		&agentv1.InboundHealth{InboundId: ids.i2, State: running})
	_, r = reasonOf(t, e, a.nodeID)
	if r == nil || r.Code != "inbound_failed" || r.Params["profile"] != "p1" || r.Params["failed"] != "1" || r.Params["total"] != "2" ||
		r.Params["inbound"] != ids.i1 || r.Params["error"] != "listen udp :443: bind: address already in use" {
		t.Fatalf("one of two failed: %v", r)
	}

	setHealth(e, a.nodeID, &agentv1.InboundHealth{InboundId: ids.i1, State: failed}, &agentv1.InboundHealth{InboundId: ids.i2, State: failed, Detail: "x"})
	if _, r = reasonOf(t, e, a.nodeID); r == nil || r.Params["failed"] != "2" || r.Params["total"] != "2" || r.Params["profile"] != "p1" {
		t.Fatalf("two of two failed: %v", r)
	}

	// a profile the panel no longer has on the node is not the node's problem (the agent catches up a moment later)
	setHealth(e, a.nodeID, &agentv1.InboundHealth{InboundId: "inb_gone", State: failed}, &agentv1.InboundHealth{InboundId: ids.i1, State: running})
	if _, r = reasonOf(t, e, a.nodeID); r != nil {
		t.Fatalf("a failed inbound that is gone: %v", r)
	}

	// profiles switched off on the node: nobody gets it again
	e.exec(`UPDATE inbound SET enabled = 0 WHERE node_id = ?`, a.nodeID)
	setHealth(e, a.nodeID)
	if _, r = reasonOf(t, e, a.nodeID); r == nil || r.Code != "no_profiles" {
		t.Fatalf("only disabled profiles: %v", r)
	}
}

// A pending node says until when its install command works, and that it no longer does.
func TestPendingNodeSaysWhenTheCommandEnds(t *testing.T) {
	e := newEnv(t)
	id, _, _ := e.createEnrollment("nodea", "nodea.example.com")
	st, r := reasonOf(t, e, id)
	if st != adminv1.NodeStatus_NODE_STATUS_PENDING || r == nil || r.Code != "enrollment_pending" {
		t.Fatalf("pending: %v %v", st, r)
	}
	exp, _ := strconv.ParseInt(r.Params["expires_unix"], 10, 64)
	mins, _ := strconv.Atoi(r.Params["expires_in_minutes"])
	if d := time.Until(time.Unix(exp, 0)); d < 58*time.Minute || d > 61*time.Minute || mins < 58 || mins > 60 {
		t.Fatalf("expires: %v", r.Params)
	}
	e.exec(`UPDATE enrollment_token SET expires_at = expires_at - 7200`)
	if st, r := reasonOf(t, e, id); st != adminv1.NodeStatus_NODE_STATUS_PENDING || r == nil || r.Code != "enrollment_expired" {
		t.Fatalf("expired: %v %v", st, r)
	}
}

// The problem count of the Overview (and of MCP's fleet summary) follows the SPA's rule.
func TestProblemNodes(t *testing.T) {
	r := func(code string) *adminv1.StatusReason { return reason(code) }
	for _, c := range []struct {
		st   nodeStatus
		want bool
	}{
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_DOWN}, true},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC}, true},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_ONLINE}, false},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_ONLINE, reason: r("no_profiles")}, true},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_ONLINE, reason: r("inbound_failed")}, true},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_ONLINE, reason: r("doctor_fail")}, true},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_ONLINE, reason: r("clock_skew")}, false},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_BLIP, reason: r("host_blip")}, false},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_PENDING, reason: r("enrollment_pending")}, false},
		{nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_UPDATING}, false},
	} {
		if got := c.st.problem(); got != c.want {
			t.Errorf("%v %v: problem %v", c.st.status, c.st.reason, got)
		}
	}
}

func insertEvents(t *testing.T, e *env, rows ...store.EventRow) {
	t.Helper()
	for _, r := range rows {
		if r.Time.IsZero() {
			r.Time = time.Now()
		}
		if r.Source == "" {
			r.Source = "agent"
		}
		if err := e.st.InsertEvent(e.ctx, r); err != nil {
			t.Fatal(err)
		}
	}
}

func codesOf(events []*adminv1.Event) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.Code)
	}
	slices.Sort(out)
	return out
}

// The Overview feed answers "what happened": a fall, a return, a new node, an update, a user, and anything that warns.
// It never hides a fall (node_down, a failed profile, every warning and error); profile starts and applied
// configurations stay on the node's own tab.
func TestOverviewFeedKeepsWhatMatters(t *testing.T) {
	e := newEnv(t)
	ev := func(sev int, code string) store.EventRow { return store.EventRow{Severity: sev, Code: code} }
	insertEvents(t, e,
		ev(1, "engine_started"), ev(1, "engine_restarted"), ev(1, "state_applied"), ev(1, "agent_started"), ev(1, "profiles_restarted"),
		ev(1, "cert_renewed"), ev(1, "userish"), ev(1, "updates"), // look alike, but not a user_ or update_ code
		ev(2, "clock_skew"), ev(3, "stats_stale"), ev(3, "node_down"), ev(1, "node_blip"), ev(1, "node_recovered"),
		ev(1, "node_enrolled"), ev(1, "node_retired"), ev(3, "engine_failed"), ev(1, "update_committed"), ev(1, "update_step_passed"),
		ev(2, "update_rolled_back"), ev(1, "user_created"), ev(2, "user_over_quota"), ev(2, "subscription_shared_suspect"),
	)
	resp, err := fleetService{e.f}.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{EventLimit: 100}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clock_skew", "engine_failed", "node_blip", "node_down", "node_enrolled", "node_recovered", "node_retired", "stats_stale",
		"subscription_shared_suspect", "update_committed", "update_rolled_back", "update_step_passed", "user_created", "user_over_quota"}
	if got := codesOf(resp.Msg.Events); !slices.Equal(got, want) {
		t.Fatalf("feed:\n got %v\nwant %v", got, want)
	}
	// the default is the newest six of them
	resp, _ = fleetService{e.f}.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{}))
	if len(resp.Msg.Events) != 6 || resp.Msg.Events[0].Code != "subscription_shared_suspect" {
		t.Fatalf("default feed: %v", codesOf(resp.Msg.Events))
	}
}

// The Profiles and Agent filters of the node's Events tab are the server's: a page of 50 is 50 of that family, so an
// empty answer means there are none, not "none among the newest 50 of any kind".
func TestEventFamilies(t *testing.T) {
	e := newEnv(t)
	ev := func(code string) store.EventRow { return store.EventRow{Severity: 1, Code: code} }
	insertEvents(t, e, ev("engine_started"), ev("engine_failed"), ev("profile_added"), ev("hop_rejected"), ev("credential_expired"),
		ev("agent_started"), ev("node_down"), ev("clock_skew"), ev("state_applied"), ev("update_committed"))
	list := func(family string, limit uint32) ([]string, bool, error) {
		resp, err := fleetService{e.f}.ListEvents(e.ctx, connect.NewRequest(&adminv1.ListEventsRequest{Family: family, Limit: limit}))
		if err != nil {
			return nil, false, err
		}
		return codesOf(resp.Msg.Events), resp.Msg.HasMore, nil
	}
	if got, _, _ := list("profiles", 0); !slices.Equal(got, []string{"credential_expired", "engine_failed", "engine_started", "hop_rejected", "profile_added"}) {
		t.Errorf("profiles: %v", got)
	}
	if got, _, _ := list("agent", 0); !slices.Equal(got, []string{"agent_started", "clock_skew", "node_down", "state_applied", "update_committed"}) {
		t.Errorf("agent: %v", got)
	}
	if got, _, _ := list("", 0); len(got) != 10 {
		t.Errorf("all: %v", got)
	}
	if got, more, _ := list("profiles", 2); len(got) != 2 || !more {
		t.Errorf("a page of a family: %v more=%v", got, more)
	}
	if _, _, err := list("engines", 0); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown family: %v", err)
	}
}
