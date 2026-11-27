package access

import (
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// reverseControlFix is a TCP relay, switched on, between two agents that both list relay-reverse/1, with the checker the other
// tests use; the tests set what the controller reads (the stages, the bridge report, the monitor) and call ControlRelay.
type reverseControlFix struct {
	*relayFix
	c  *fakeChecker
	id string
}

func newReverseControlFix(t *testing.T, active string) *reverseControlFix {
	t.Helper()
	f := newRelayFix(t)
	f.s.afterResponse = func(work func()) { work() } // what the controller starts after a switch runs where the test can see it
	f.s.SetRelayWaitForTest(200*time.Millisecond, 5*time.Millisecond)
	inbound := addTCPRelayServer(f)
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'nod_fi1'`)
	f.sql(`UPDATE node SET agent_caps = 'relay-reverse/1 tcpcheck/1' WHERE id = 'nod_de1'`)
	c := &fakeChecker{f: f, leg: okLeg, path: store.RelayStage{Status: "ok", Sender: "nod_nl1"}, reverse: store.RelayStage{Status: "ok", Sender: "nod_de1"},
		direct: store.RelayStage{Status: "ok", Sender: "nod_fi1", MB: 8}}
	f.s.cfg.CheckRelay = c.check
	r := must(f.s.CreateRelay(f.ctx, req(&adminv1.CreateRelayRequest{InboundId: inbound.Id, ViaNodeId: "nod_fi1", Mode: "auto"}))).Msg.Relay
	on := true
	if err := f.st.UpdateRelay(f.ctx, r.Id, store.RelayPatch{Enabled: &on}, f.clock); err != nil {
		t.Fatal(err)
	}
	// the entry's agent installed the forward
	if err := f.st.RelaysApplied(f.ctx, "nod_fi1", []store.RelayApplied{{ID: r.Id, OK: true, ToIP: "192.0.2.10"}}, f.clock); err != nil {
		t.Fatal(err)
	}
	x := &reverseControlFix{relayFix: f, c: c, id: r.Id}
	if active == store.RelayModeReverse {
		x.setActive(store.RelayModeReverse, "leg_failed", f.clock.Add(-2*time.Hour))
	}
	return x
}

func (x *reverseControlFix) setActive(active, reason string, at time.Time) {
	x.t.Helper()
	if err := x.st.SetRelayActiveMode(x.ctx, x.id, active, reason, at); err != nil {
		x.t.Fatal(err)
	}
}

// check writes the stages the controller reads; zero stages are left out.
func (x *reverseControlFix) check(leg, reach, path, direct store.RelayStage) {
	x.t.Helper()
	link := x.relayOf(x.id)
	entry := must(x.st.Node(x.ctx, "nod_fi1"))
	if reach.Status != "" {
		reach.Port = entry.BridgeEndpointPort()
	}
	if err := x.st.SetRelayCheck(x.ctx, x.id, store.RelayCheck{At: x.clock.Unix(), Network: "tcp", Port: link.Port, To: x.s.relayTo(x.ctx, link),
		Leg: leg, ReverseReach: reach, Path: path, DirectSpeed: direct}); err != nil {
		x.t.Fatal(err)
	}
}

func (x *reverseControlFix) stage(status string, ago time.Duration) store.RelayStage {
	return store.RelayStage{Status: status, At: x.clock.Add(-ago).Unix()}
}

// bridge reports the exit's side of the bridge: held since a minute ago, or not at all.
func (x *reverseControlFix) bridge(up bool) {
	x.t.Helper()
	report := &store.RelayBridgeSideCheck{AtUnix: x.clock.Unix()}
	if up {
		report = &store.RelayBridgeSideCheck{Connected: true, Conns: 2, SinceUnix: x.clock.Unix() - 60, AtUnix: x.clock.Unix()}
	}
	if err := x.st.SetRelayBridgeSide(x.ctx, x.id, "exit", report); err != nil {
		x.t.Fatal(err)
	}
}

func (x *reverseControlFix) monitor(ok bool, streak uint32) {
	x.t.Helper()
	_, key := x.s.relayTarget(x.ctx, x.relayOf(x.id))
	if err := x.st.SetRelayMonitorCheck(x.ctx, x.id, store.RelayMonitorCheck{AtUnix: x.clock.Unix(), OK: ok, FailStreak: streak, TargetKey: key}); err != nil {
		x.t.Fatal(err)
	}
}

func (x *reverseControlFix) modeEvents() int {
	rows, _, err := x.st.Events(x.ctx, store.EventFilter{Match: &store.CodeMatch{Codes: []string{"relay_mode_changed"}}, Limit: 50})
	if err != nil {
		x.t.Fatal(err)
	}
	return len(rows)
}

func (x *reverseControlFix) calls() []string {
	x.c.mu.Lock()
	defer x.c.mu.Unlock()
	var out []string
	for _, stages := range x.c.calls {
		out = append(out, strings.Join(stages, "+"))
	}
	return out
}

// The way back to direct is not left to the owner: after three direct samples an hour apart the panel runs the 8 MB flow of the
// direct way itself, and only a flow that passes moves the relay; one that fails starts the count again and is not repeated for
// six hours; one that could not start (the daily limit) is asked again after half an hour.
func TestControllerRunsTheProofOfTheDirectWayItself(t *testing.T) {
	ready := func(t *testing.T) *reverseControlFix {
		x := newReverseControlFix(t, store.RelayModeReverse)
		x.check(x.stage("ok", 0), x.stage("ok", 0), x.stage("ok", 0), store.RelayStage{})
		x.bridge(true)
		x.monitor(true, 0)
		if err := x.st.SetRelayDirectState(x.ctx, x.relayOf(x.id), 2, x.clock.Add(-31*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		return x
	}
	t.Run("a flow that passes returns the relay to direct, and the owner clicked nothing", func(t *testing.T) {
		x := ready(t)
		x.s.ControlRelay(x.ctx, x.id)
		if got := x.calls(); len(got) != 1 || got[0] != store.RelayStageDirectSpeed {
			t.Fatalf("checks asked for = %v, want one proof of the direct way", got)
		}
		l := x.relayOf(x.id)
		if l.ActiveMode != store.RelayModeDNAT || l.ModeReason != "direct_back" || x.modeEvents() != 1 {
			t.Fatalf("after the proof: active=%q reason=%q events=%d", l.ActiveMode, l.ModeReason, x.modeEvents())
		}
	})
	t.Run("a flow that freezes starts the count again and is not repeated", func(t *testing.T) {
		x := ready(t)
		x.c.direct = store.RelayStage{Status: "fail", Code: "timeout", Sender: "nod_fi1", MB: 8}
		x.s.ControlRelay(x.ctx, x.id)
		l := x.relayOf(x.id)
		if l.ActiveMode != store.RelayModeReverse || l.DirectStreak() != 0 {
			t.Fatalf("after a failed proof: active=%q streak=%d", l.ActiveMode, l.DirectStreak())
		}
		for i := 0; i < 3; i++ { // three more passes of the controller, and a new streak an hour later: the proof is not asked for again within 6 hours
			x.clock = x.clock.Add(31 * time.Minute)
			prev, _ := x.relayOf(x.id).Check()
			x.check(x.stage("ok", 0), x.stage("ok", 0), x.stage("ok", 0), prev.DirectSpeed)
			x.bridge(true)
			x.s.ControlRelay(x.ctx, x.id)
		}
		if got := x.calls(); len(got) != 1 {
			t.Fatalf("proofs asked for after a failed one = %v, want just the first", got)
		}
	})
	t.Run("a proof that could not start (the daily limit) is asked for again after half an hour", func(t *testing.T) {
		x := ready(t)
		x.c.direct = store.RelayStage{Status: "skipped", Code: "busy", Sender: "nod_fi1"}
		x.s.ControlRelay(x.ctx, x.id)
		x.s.ControlRelay(x.ctx, x.id)
		if got := x.calls(); len(got) != 1 {
			t.Fatalf("a proof that did not start was asked for again at once: %v", got)
		}
		if l := x.relayOf(x.id); l.ActiveMode != store.RelayModeReverse || l.DirectStreak() != 3 {
			t.Fatalf("a skipped proof moved the relay or lost the count: active=%q streak=%d", l.ActiveMode, l.DirectStreak())
		}
		x.clock = x.clock.Add(31 * time.Minute)
		x.bridge(true)
		x.c.direct = store.RelayStage{Status: "ok", Sender: "nod_fi1", MB: 8}
		x.s.ControlRelay(x.ctx, x.id)
		if got := x.calls(); len(got) != 2 || x.relayOf(x.id).ActiveMode != store.RelayModeDNAT {
			t.Fatalf("after half an hour: calls=%v active=%q", got, x.relayOf(x.id).ActiveMode)
		}
	})
	t.Run("the owner's reverse is never taken back", func(t *testing.T) {
		x := ready(t)
		mode := store.RelayModeReverse
		if err := x.st.UpdateRelay(x.ctx, x.id, store.RelayPatch{Mode: &mode}, x.clock); err != nil {
			t.Fatal(err)
		}
		x.s.ControlRelay(x.ctx, x.id)
		if got := x.calls(); len(got) != 0 || x.relayOf(x.id).ActiveMode != store.RelayModeReverse {
			t.Fatalf("a relay the owner pinned to reverse was proven and moved: calls=%v", got)
		}
	})
}

// A relay the controller moved to the bridge that does not carry people from a third node is not switched off, as one the owner
// switched on is: it worked in the way it was in, so it goes back to it, and stays there for the half hour of the rule.
func TestControllerSwitchToTheBridgeThatFailsGoesBackToDirectNotOff(t *testing.T) {
	prepare := func(t *testing.T) *reverseControlFix {
		x := newReverseControlFix(t, store.RelayModeDNAT)
		x.check(x.stage("fail", 0), x.stage("ok", 0), store.RelayStage{}, store.RelayStage{})
		x.bridge(true)
		x.monitor(false, 2)
		return x
	}
	t.Run("the path from a third node fails", func(t *testing.T) {
		x := prepare(t)
		x.c.path = store.RelayStage{Status: "fail", Code: "timeout", Sender: "nod_nl1"}
		x.s.ControlRelay(x.ctx, x.id)
		l := x.relayOf(x.id)
		if !l.Enabled || l.ActiveMode != store.RelayModeDNAT || l.ModeReason != "bridge_failed" {
			t.Fatalf("after a failed switch: enabled=%v active=%q reason=%q", l.Enabled, l.ActiveMode, l.ModeReason)
		}
		if x.modeEvents() != 2 {
			t.Fatalf("mode events = %d, want the switch and the way back", x.modeEvents())
		}
		if c, _ := l.Check(); c.Path.Status != "fail" || c.Path.Code != "timeout" {
			t.Fatalf("what the path found was not kept: %+v", c.Path)
		}
	})
	t.Run("the panel itself was the only sender: that proves nothing", func(t *testing.T) {
		x := prepare(t)
		x.c.path = store.RelayStage{Status: "ok", Sender: "panel"}
		x.s.ControlRelay(x.ctx, x.id)
		if l := x.relayOf(x.id); !l.Enabled || l.ActiveMode != store.RelayModeDNAT || l.ModeReason != "bridge_failed" {
			t.Fatalf("a path proved by the panel alone kept the relay in reverse: %+v", l.Relay)
		}
	})
	t.Run("people get through", func(t *testing.T) {
		x := prepare(t)
		x.s.ControlRelay(x.ctx, x.id)
		l := x.relayOf(x.id)
		if !l.Enabled || l.ActiveMode != store.RelayModeReverse || l.ModeReason != "leg_failed" || x.modeEvents() != 1 {
			t.Fatalf("after a good switch: enabled=%v active=%q reason=%q events=%d", l.Enabled, l.ActiveMode, l.ModeReason, x.modeEvents())
		}
	})
}

// A relay of a pair that cannot do it is never touched, and a reverse relay whose agent was rolled back is brought home at once.
func TestControllerLeavesRelaysOfOldAgentsAlone(t *testing.T) {
	x := newReverseControlFix(t, store.RelayModeDNAT)
	x.check(x.stage("fail", 0), x.stage("ok", 0), store.RelayStage{}, store.RelayStage{})
	x.bridge(true)
	x.monitor(false, 5)
	x.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_fi1'`)
	x.s.ControlRelay(x.ctx, x.id)
	if l := x.relayOf(x.id); l.ActiveMode != store.RelayModeDNAT || l.ModeReason != "" || x.modeEvents() != 0 {
		t.Fatalf("a relay of an old agent changed: active=%q reason=%q events=%d", l.ActiveMode, l.ModeReason, x.modeEvents())
	}
	// the exit's agent was rolled back while the relay stood in reverse
	x.sql(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'nod_fi1'`)
	x.sql(`UPDATE node SET agent_caps = 'tcpcheck/1' WHERE id = 'nod_de1'`)
	x.setActive(store.RelayModeReverse, "leg_failed", x.clock.Add(-time.Minute))
	x.s.ControlRelay(x.ctx, x.id)
	if l := x.relayOf(x.id); l.ActiveMode != store.RelayModeDNAT || l.ModeReason != "reverse_unsupported" {
		t.Fatalf("a reverse relay whose exit lost the capability: active=%q reason=%q", l.ActiveMode, l.ModeReason)
	}
}

// What the owner sees of the bridge: the people it carries now, the pool and what it carries at most, and "nearly full" at 80 %.
func TestRelayMessageCarriesTheBridgeOccupancy(t *testing.T) {
	x := newReverseControlFix(t, store.RelayModeReverse)
	side := func(clients uint32) {
		if err := x.st.SetRelayBridgeSide(x.ctx, x.id, "entry", &store.RelayBridgeSideCheck{Connected: true, Conns: 2, SinceUnix: x.clock.Unix() - 60,
			AtUnix: x.clock.Unix(), Clients: clients}); err != nil {
			t.Fatal(err)
		}
	}
	x.bridge(true)
	for _, tc := range []struct {
		clients, pool     uint32
		wantPool, wantCap uint32
		wantFull          bool
	}{
		{10, 0, 2, 242, false},
		{193, 0, 2, 242, false},
		{194, 0, 2, 242, true},
		{200, 4, 4, 484, false},
		{388, 4, 4, 484, true},
	} {
		side(tc.clients)
		if tc.pool != 0 {
			if err := x.st.SetRelayBridgePool(x.ctx, x.id, tc.pool, x.clock.Unix()); err != nil {
				t.Fatal(err)
			}
		}
		b := x.s.relayMsg(x.ctx, x.relayOf(x.id)).Bridge
		if b == nil || b.Clients != tc.clients || b.Pool != tc.wantPool || b.Capacity != tc.wantCap || b.NearFull != tc.wantFull {
			t.Fatalf("%d people, pool %d: bridge = %+v, want pool %d capacity %d near full %v", tc.clients, tc.pool, b, tc.wantPool, tc.wantCap, tc.wantFull)
		}
	}
	// a DNAT relay has no bridge to speak of
	dnat := x.relayOf(x.id)
	dnat.CheckJSON = ""
	if b := x.s.relayMsg(x.ctx, dnat).Bridge; b != nil {
		t.Fatalf("a relay with no bridge report shows one: %+v", b)
	}
}

// reverse_blocked on a candidate says what the relay's own field says, before the relay exists.
func TestRelayCandidatesSayWhyTheBridgeIsNotPossible(t *testing.T) {
	f := newRelayFix(t)
	addTCPRelayServer(f)
	list := func() map[string]*adminv1.RelayCandidate {
		out := map[string]*adminv1.RelayCandidate{}
		for _, c := range must(f.s.ListRelayCandidates(f.ctx, req(&adminv1.ListRelayCandidatesRequest{ViaNodeId: "nod_fi1"}))).Msg.Candidates {
			out[c.Network] = c
		}
		return out
	}
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_fi1'`)
	got := list()
	if got["tcp"] == nil || got["udp"] == nil {
		t.Fatalf("candidates = %+v", got)
	}
	if got["tcp"].ReverseBlocked != "entry_agent_old" || got["udp"].ReverseBlocked != "udp" {
		t.Fatalf("old entry: tcp %q udp %q, want entry_agent_old and udp (an update does not help a UDP server)", got["tcp"].ReverseBlocked, got["udp"].ReverseBlocked)
	}
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'nod_fi1'`)
	if got = list(); got["tcp"].ReverseBlocked != "exit_agent_old" {
		t.Fatalf("old exit: %q", got["tcp"].ReverseBlocked)
	}
	f.sql(`UPDATE node SET agent_caps = 'relay-reverse/1' WHERE id = 'nod_de1'`)
	if got = list(); got["tcp"].ReverseBlocked != "" || got["udp"].ReverseBlocked != "udp" {
		t.Fatalf("both new: tcp %q udp %q", got["tcp"].ReverseBlocked, got["udp"].ReverseBlocked)
	}
}

// The refusal of an auto relay whose leg and bridge both fail is the leg's, as it has always been, with what the bridge found beside it.
func TestAutoRelayRefusalIsTheLegsWithTheBridgeBeside(t *testing.T) {
	f := newRelayFix(t)
	inbound := addTCPRelayServer(f)
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'nod_fi1'`)
	f.sql(`UPDATE node SET agent_caps = 'relay-reverse/1 tcpcheck/1' WHERE id = 'nod_de1'`)
	entryServesItsBridgeSite(f)
	f.s.SetRelayWaitForTest(time.Second, 5*time.Millisecond)
	c := &fakeChecker{f: f, leg: store.RelayStage{Status: "fail", Code: "timeout"}, reverse: store.RelayStage{Status: "fail", Code: "timeout", Sender: "nod_de1"}}
	f.s.cfg.CheckRelay = c.check
	r := must(f.s.CreateRelay(f.ctx, req(&adminv1.CreateRelayRequest{InboundId: inbound.Id, ViaNodeId: "nod_fi1"}))).Msg.Relay
	on := true
	_, err := f.s.UpdateRelay(f.ctx, req(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	wantMsg(t, err, "failed_precondition: relay_check_failed: code=timeout&reverse_reach=timeout&stage=leg")
	if f.relayOf(r.Id).Enabled {
		t.Fatal("a refused relay was switched on")
	}
}

// The exit dials the entry only while the panel wants the bridge, and what makes it want it (a failed leg with the exit reaching the
// entry, found by a check or a round) is no event of its own: the controller, which reads those stages after every round, tells the
// nodes when the want appears or goes, once, so that the exit gets its BridgeDial before the controller waits for the bridge.
func TestControllerTellsTheNodesWhenTheBridgeBecomesWanted(t *testing.T) {
	x := newReverseControlFix(t, store.RelayModeDNAT)
	x.monitor(true, 0)
	x.check(x.stage("ok", 0), store.RelayStage{}, x.stage("ok", 0), store.RelayStage{})
	x.s.ControlRelay(x.ctx, x.id)
	base := x.notify.n.Load()

	x.check(x.stage("fail", 0), x.stage("ok", 0), store.RelayStage{}, store.RelayStage{})
	x.s.ControlRelay(x.ctx, x.id)
	if got := x.notify.n.Load(); got != base+1 {
		t.Fatalf("the leg failed and the exit reaches the entry: StateChanged calls %d -> %d, want one more", base, got)
	}
	x.s.ControlRelay(x.ctx, x.id)
	if got := x.notify.n.Load(); got != base+1 {
		t.Fatalf("an unchanged want told the nodes again: %d -> %d", base+1, got)
	}

	x.check(x.stage("ok", 0), x.stage("ok", 0), x.stage("ok", 0), store.RelayStage{})
	x.s.ControlRelay(x.ctx, x.id)
	if got := x.notify.n.Load(); got != base+2 {
		t.Fatalf("the leg is alive again: StateChanged calls %d -> %d, want one more (the exit stops dialing)", base+1, got)
	}
}
