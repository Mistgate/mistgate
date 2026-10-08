package health

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func portCheckTimeFor(nodeID string, from time.Time) time.Time {
	period := int64(PortCheckInterval / time.Second)
	window := from.Unix() - from.Unix()%period
	slot := window + portCheckSlot(nodeID)*int64(portCheckSlotWidth/time.Second)
	if slot <= from.Unix() {
		slot += period
	}
	return time.Unix(slot+1, 0).UTC()
}

func TestDuePortCheckNodesUsesSixHourMinimumAndEligibility(t *testing.T) {
	now := portCheckTimeFor("node-a", time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	eligible := PortCheckScheduleNode{ID: "node-a", State: "active", Online: true, UDPCheck: true,
		HasEnabledInbound: true, LastCheckedAt: now.Add(-PortCheckInterval)}
	if got := DuePortCheckNodes(now, []PortCheckScheduleNode{eligible}); len(got) != 1 || got[0] != eligible.ID {
		t.Fatalf("six-hour check selection = %v", got)
	}
	eligible.LastCheckedAt = now.Add(-PortCheckInterval + time.Second)
	if got := DuePortCheckNodes(now, []PortCheckScheduleNode{eligible}); len(got) != 0 {
		t.Fatalf("recently checked node is due: %v", got)
	}
	eligible.LastCheckedAt = now.Add(time.Minute - PortCheckInterval)
	if got := DuePortCheckNodes(now.Add(time.Minute), []PortCheckScheduleNode{eligible}); len(got) != 1 || got[0] != eligible.ID {
		t.Fatalf("node checked after its prior slot was not due at six hours: %v", got)
	}
	eligible.LastCheckedAt = time.Time{}
	for _, mutate := range []func(*PortCheckScheduleNode){
		func(n *PortCheckScheduleNode) { n.State = "pending" },
		func(n *PortCheckScheduleNode) { n.Online = false },
		func(n *PortCheckScheduleNode) { n.UDPCheck = false },
		func(n *PortCheckScheduleNode) { n.HasEnabledInbound = false },
	} {
		node := eligible
		mutate(&node)
		if got := DuePortCheckNodes(now, []PortCheckScheduleNode{node}); len(got) != 0 {
			t.Fatalf("ineligible node %+v is due: %v", node, got)
		}
	}
}

func TestDuePortCheckNodesSpreadsNodesAcrossSixHourWindow(t *testing.T) {
	ids := []string{"node-a", "node-b", "node-c", "node-d", "node-e"}
	var first, second string
	for _, a := range ids {
		for _, b := range ids {
			if portCheckSlot(a) != portCheckSlot(b) {
				first, second = a, b
				break
			}
		}
		if first != "" {
			break
		}
	}
	if first == "" {
		t.Fatal("test node ids unexpectedly share one slot")
	}
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	now := portCheckTimeFor(first, base)
	nodes := []PortCheckScheduleNode{
		{ID: first, State: "active", Online: true, UDPCheck: true, HasEnabledInbound: true},
		{ID: second, State: "active", Online: true, UDPCheck: true, HasEnabledInbound: true},
	}
	if got := DuePortCheckNodes(now, nodes); len(got) != 1 || got[0] != first {
		t.Fatalf("slot for %q selected %v", first, got)
	}
	if got := DuePortCheckNodes(portCheckTimeFor(second, now), nodes); len(got) != 1 || got[0] != second {
		t.Fatalf("slot for %q selected %v", second, got)
	}
}

func TestPortCheckScheduleCandidatesUseLiveStateEnabledInboundsAndStoredChecks(t *testing.T) {
	e := newEnv(t)
	now := portCheckTimeFor("node-a", e.clock.Now())
	e.clock.Advance(now.Sub(e.clock.Now()))
	e.node("node-a", "provider-a", true)
	e.inbound("node-a", 8443)
	e.fl.set("node-a", liveState{up: true, caps: []string{portCheckCapability}})

	e.node("node-b", "provider-b", true)
	e.inbound("node-b", 8443)
	e.fl.set("node-b", liveState{up: true, caps: []string{portCheckCapability}})

	e.node("node-c", "provider-c", false)
	e.inbound("node-c", 8443)
	e.fl.set("node-c", liveState{up: false, caps: []string{portCheckCapability}})

	e.node("node-d", "provider-d", true)
	e.inbound("node-d", 8443)
	e.fl.set("node-d", liveState{up: true, caps: []string{capDoctor}})

	e.node("node-e", "provider-e", true)
	disabled := e.inbound("node-e", 8443)
	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, disabled)
	e.fl.set("node-e", liveState{up: true, caps: []string{portCheckCapability}})

	e.node("node-f", "provider-f", true) // no enabled inbound
	e.fl.set("node-f", liveState{up: true, caps: []string{portCheckCapability}})

	putStoredPortCheck(t, e, "node-a", 8443, now.Add(-PortCheckInterval), "panel")
	putStoredPortCheck(t, e, "node-b", 8443, now.Add(-5*time.Hour), "node-c")
	e.s.invalidateSnapshot()
	sn, err := e.s.snapshot(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := e.s.portCheckScheduleNodes(e.ctx, now, sn)
	if err != nil {
		t.Fatal(err)
	}
	if got := DuePortCheckNodes(now, candidates); len(got) != 1 || got[0] != "node-a" {
		t.Fatalf("scheduled candidates %+v, due %v", candidates, got)
	}
}

func putStoredPortCheck(t *testing.T, e *env, nodeID string, port uint16, checkedAt time.Time, sender string) {
	t.Helper()
	node, err := e.st.Node(e.ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	check := store.PortCheck{NodeID: nodeID, Address: node.Address, Port: port, Sent: 300, Got: 300,
		Verdict: "ok", Sender: sender, CheckedAt: checkedAt}
	if err := e.st.PutPortChecks(e.ctx, []store.PortCheck{check}); err != nil {
		t.Fatal(err)
	}
}

func TestPeriodicPortCheckConfirmsOnlyBadEnabledInboundPorts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		firstPort  uint16
		firstState string
		wantCalls  int
	}{{"clean", 8443, "ok", 1}, {"unused bad port", 2053, "broken", 1}, {"bad inbound port", 8443, "lossy", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			now := portCheckTimeFor("node-a", e.clock.Now())
			e.clock.Advance(now.Sub(e.clock.Now()))
			e.node("node-a", "provider-a", true)
			e.inbound("node-a", 8443)
			e.fl.set("node-a", liveState{up: true, caps: []string{portCheckCapability}})
			calls := 0
			e.s.cfg.CheckPorts = func(ctx context.Context, nodeID string, _ []uint16) ([]store.PortCheck, string, string) {
				calls++
				verdict, got := tc.firstState, uint32(100)
				port := tc.firstPort
				if calls > 1 {
					verdict, got, port = "ok", 300, 8443
				}
				if verdict == "ok" {
					got = 300
				}
				check := store.PortCheck{NodeID: nodeID, Address: nodeID + ".example.com", Port: port, Sent: 300,
					Got: got, Verdict: verdict, Sender: "panel", CheckedAt: e.clock.Now().Add(time.Duration(calls) * time.Second)}
				if err := e.st.PutPortChecks(ctx, []store.PortCheck{check}); err != nil {
					e.t.Fatal(err)
				}
				return []store.PortCheck{check}, "panel", ""
			}
			ran := e.s.runOnePortRecheck(e.ctx)
			if !ran || calls != tc.wantCalls {
				t.Fatalf("run = %v, CheckPorts calls = %d, want %d", ran, calls, tc.wantCalls)
			}
			if tc.wantCalls == 2 {
				latest, err := e.st.PortChecks(e.ctx, "node-a")
				if err != nil || len(latest) != 1 || latest[0].Verdict != "ok" {
					t.Fatalf("latest verdict = %+v, %v", latest, err)
				}
			}
		})
	}
}

func TestPeriodicPortChecksRunOneAtATime(t *testing.T) {
	e := newEnv(t)
	now := portCheckTimeFor("node-a", e.clock.Now())
	e.clock.Advance(now.Sub(e.clock.Now()))
	e.node("node-a", "provider-a", true)
	e.inbound("node-a", 8443)
	e.fl.set("node-a", liveState{up: true, caps: []string{portCheckCapability}})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	var mu sync.Mutex
	calls := 0
	e.s.cfg.CheckPorts = func(context.Context, string, []uint16) ([]store.PortCheck, string, string) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(entered)
		<-release
		return nil, "panel", ""
	}
	go func() { done <- e.s.runOnePortRecheck(e.ctx) }()
	<-entered
	if e.s.runOnePortRecheck(e.ctx) {
		t.Fatal("a second periodic run started while the first was in flight")
	}
	close(release)
	if !<-done {
		t.Fatal("first periodic run did not start")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("CheckPorts calls = %d, want one", calls)
	}
}
