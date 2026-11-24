package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// relayModeStoreProof runs on SQLite and on D1 (relay_mode_test.go and d1_store_test.go): the columns of the reverse way, their
// defaults, the writers and the members of check_json they own.
func relayModeStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	relayFixture(t, st)
	relayExec(t, st, `UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'nod_de1'`)

	// A relay made the old way (no mode given) is "auto" on "dnat": what every relay was before the reverse way.
	if err := st.CreateRelay(ctx, Relay{ID: "rly_a", InboundID: "inb_de1_vl", ViaNodeID: "nod_fi1", Network: "tcp", Port: 8443}, now); err != nil {
		t.Fatal(err)
	}
	l := mustRelay(t, st, "rly_a")
	if l.Mode != RelayModeAuto || l.ActiveMode != RelayModeDNAT || l.ModeReason != "" || !l.ModeChangedAt.IsZero() || l.Reverse() {
		t.Fatalf("a new relay: mode %q active %q reason %q changed %v", l.Mode, l.ActiveMode, l.ModeReason, l.ModeChangedAt)
	}
	if l.DirectStreak() != 0 {
		t.Fatalf("direct streak of a new relay = %d", l.DirectStreak())
	}
	if _, ok := l.Bridge(); ok {
		t.Fatal("a new relay has a bridge report")
	}
	if len(l.ExitNodeCaps) != 2 || l.ExitNodeCaps[1] != "relay-reverse/1" || len(l.ViaNodeCaps) != 1 {
		t.Fatalf("caps of the two ends: exit %v entry %v", l.ExitNodeCaps, l.ViaNodeCaps)
	}

	// The owner's choice at creation, and the database refuses a word that is none of the three.
	if err := st.CreateRelay(ctx, Relay{ID: "rly_r", InboundID: "inb_nl1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 8444, Mode: RelayModeReverse}, now); err != nil {
		t.Fatal(err)
	}
	if r := mustRelay(t, st, "rly_r"); r.Mode != RelayModeReverse || r.ActiveMode != RelayModeDNAT {
		t.Fatalf("a relay made as reverse: mode %q active %q (the choice is not the state)", r.Mode, r.ActiveMode)
	}
	if err := st.CreateRelay(ctx, Relay{ID: "rly_bad", InboundID: "inb_nl1_hy2", ViaNodeID: "nod_fi1", Network: "udp", Port: 8445, Mode: "both"}, now); err == nil {
		t.Fatal("a relay with a mode that is none of the three was stored")
	}
	if !ValidRelayMode("auto") || !ValidRelayMode("dnat") || !ValidRelayMode("reverse") || ValidRelayMode("") || ValidRelayMode("both") {
		t.Fatal("ValidRelayMode")
	}

	// The patch changes the choice and leaves the forward as the agent reported it.
	if err := st.UpdateRelay(ctx, "rly_a", RelayPatch{Enabled: ptr(true)}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RelaysApplied(ctx, "nod_fi1", []RelayApplied{{ID: "rly_a", OK: true, ToIP: "192.0.2.10"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRelay(ctx, "rly_a", RelayPatch{Mode: ptr(RelayModeDNAT)}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if l = mustRelay(t, st, "rly_a"); l.Mode != RelayModeDNAT || l.State != "active" || l.ToIP != "192.0.2.10" || !l.Enabled || l.ActiveMode != RelayModeDNAT {
		t.Fatalf("after a mode patch: mode %q state %q to %q enabled %v", l.Mode, l.State, l.ToIP, l.Enabled)
	}
	if err := st.UpdateRelay(ctx, "rly_a", RelayPatch{Mode: ptr("both")}, now); err == nil {
		t.Fatal("a patch with a mode that is none of the three was stored")
	}

	// The way that stands: the change time moves with the way, not with the reason.
	t1, t2 := now.Add(2*time.Minute), now.Add(3*time.Minute)
	if err := st.SetRelayActiveMode(ctx, "rly_a", RelayModeReverse, "leg_failed", t1); err != nil {
		t.Fatal(err)
	}
	if l = mustRelay(t, st, "rly_a"); l.ActiveMode != RelayModeReverse || l.ModeReason != "leg_failed" || !l.ModeChangedAt.Equal(t1) || !l.Reverse() {
		t.Fatalf("after the switch to reverse: %q %q %v", l.ActiveMode, l.ModeReason, l.ModeChangedAt)
	}
	if err := st.SetRelayActiveMode(ctx, "rly_a", RelayModeReverse, "owner", t2); err != nil {
		t.Fatal(err)
	}
	if l = mustRelay(t, st, "rly_a"); l.ModeReason != "owner" || !l.ModeChangedAt.Equal(t1) {
		t.Fatalf("a new reason alone moved the change time: %q %v", l.ModeReason, l.ModeChangedAt)
	}
	if err := st.SetRelayActiveMode(ctx, "rly_a", RelayModeDNAT, "direct_back", t2); err != nil {
		t.Fatal(err)
	}
	if l = mustRelay(t, st, "rly_a"); l.ActiveMode != RelayModeDNAT || l.ModeReason != "direct_back" || !l.ModeChangedAt.Equal(t2) {
		t.Fatalf("after the way back: %q %q %v", l.ActiveMode, l.ModeReason, l.ModeChangedAt)
	}
	if err := st.SetRelayActiveMode(ctx, "rly_a", "auto", "", t2); err == nil {
		t.Fatal("an active mode of auto was stored")
	}
	if err := st.SetRelayActiveMode(ctx, "rly_none", RelayModeDNAT, "", t2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a mode of a relay that is gone: %v", err)
	}

	// check_json: every writer owns one member. The bridge report and the streak survive the check, the client-eye result and the
	// counters, and they do not damage them.
	if err := st.SetRelayCheck(ctx, "rly_a", RelayCheck{At: now.Unix(), Network: "tcp", Port: 8443, To: "192.0.2.10:443", Leg: RelayStage{Status: "ok"}, Path: RelayStage{Status: "ok"}, OK: true}); err != nil {
		t.Fatal(err)
	}
	report := RelayBridgeCheck{Connected: true, Conns: 2, RTTMS: 41, SinceUnix: now.Unix() - 60, AtUnix: now.Unix()}
	if err := st.SetRelayBridge(ctx, "rly_a", &report); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayDirectStreak(ctx, "rly_a", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayMonitorCheck(ctx, "rly_a", RelayMonitorCheck{AtUnix: now.Unix(), OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayCheck(ctx, "rly_a", RelayCheck{At: now.Unix() + 1, Network: "tcp", Port: 8443, To: "192.0.2.10:443", Leg: RelayStage{Status: "ok"}, Path: RelayStage{Status: "ok"}, OK: true}); err != nil {
		t.Fatal(err)
	}
	l = mustRelay(t, st, "rly_a")
	got, ok := l.Bridge()
	if !ok || got != report || l.DirectStreak() != 2 {
		t.Fatalf("bridge report %+v (%v), streak %d, check_json %s", got, ok, l.DirectStreak(), l.CheckJSON)
	}
	if c, ok := l.Check(); !ok || c.At != now.Unix()+1 || c.Monitor == nil || !c.Monitor.OK || !c.Verified() {
		t.Fatalf("the check lost a member next to the bridge report: %+v", c)
	}
	entrySide := RelayBridgeSideCheck{Connected: true, Conns: 1, SinceUnix: now.Unix() - 30, AtUnix: now.Unix()}
	exitSide := RelayBridgeSideCheck{Connected: true, Conns: 2, RTTMS: 43, SinceUnix: now.Unix() - 30, AtUnix: now.Unix()}
	if err := st.SetRelayBridgeSide(ctx, "rly_a", "entry", &entrySide); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayBridgeSide(ctx, "rly_a", "exit", &exitSide); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayDirectState(ctx, "rly_a", 2, now.Unix()-30); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayCheck(ctx, "rly_a", RelayCheck{At: now.Unix() + 2, Network: "tcp", Port: 8443,
		To: "192.0.2.10:443", Leg: RelayStage{Status: "ok"}, Path: RelayStage{Status: "ok"}, ReverseReach: RelayStage{Status: "ok", At: now.Unix()},
		DirectSpeed: RelayStage{Status: "ok", MB: 8, At: now.Unix()}, OK: true}); err != nil {
		t.Fatal(err)
	}
	l = mustRelay(t, st, "rly_a")
	got, ok = l.Bridge()
	if !ok || got.Entry == nil || *got.Entry != entrySide || got.Exit == nil || *got.Exit != exitSide || got.Conns != exitSide.Conns {
		t.Fatalf("the two bridge report paths did not merge: %+v; check_json=%s", got, l.CheckJSON)
	}
	if c, ok := l.Check(); !ok || c.DirectSpeed.MB != 8 || c.Monitor == nil || !c.Monitor.OK || l.DirectStreak() != 2 || l.DirectStreakAt() != now.Unix()-30 {
		t.Fatalf("the check writer lost relay-owned members: check=%+v streak=%d at=%d", c, l.DirectStreak(), l.DirectStreakAt())
	}
	// the bridge stage is derived from the exit's report, never stored
	for _, tc := range []struct {
		name         string
		at           time.Time
		report       RelayBridgeSideCheck
		status, code string
		stageAt      int64
	}{
		{"holding", now, exitSide, "ok", "", now.Unix()},
		{"warming", now, RelayBridgeSideCheck{Connected: true, Conns: 1, SinceUnix: now.Unix() - 3, AtUnix: now.Unix()}, "skipped", "warming", now.Unix()},
		{"an error of the agent", now, RelayBridgeSideCheck{Connected: true, Conns: 1, SinceUnix: now.Unix() - 60, Error: "pin_mismatch", AtUnix: now.Unix()}, "fail", "pin_mismatch", now.Unix()},
		{"no connection", now, RelayBridgeSideCheck{AtUnix: now.Unix()}, "fail", "disconnected", now.Unix()},
		{"silent for 91 seconds", now.Add(91 * time.Second), exitSide, "fail", "stale", now.Unix() + 91},
		{"a report from the future", now.Add(-time.Minute), exitSide, "fail", "stale", now.Unix() - 60},
	} {
		if err := st.SetRelayBridgeSide(ctx, "rly_a", "exit", &tc.report); err != nil {
			t.Fatal(err)
		}
		if got := mustRelay(t, st, "rly_a").BridgeStage(tc.at); got.Status != tc.status || got.Code != tc.code || got.At != tc.stageAt {
			t.Fatalf("bridge stage %s = %+v, want %s/%s at %d", tc.name, got, tc.status, tc.code, tc.stageAt)
		}
	}
	// the pool the panel asks for and the people the entry reports live next to the sides, and neither erases the other
	entrySide.Clients = 150
	if err := st.SetRelayBridgeSide(ctx, "rly_a", "entry", &entrySide); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayBridgePool(ctx, "rly_a", 4, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayBridgeSide(ctx, "rly_a", "exit", &exitSide); err != nil {
		t.Fatal(err)
	}
	got, ok = mustRelay(t, st, "rly_a").Bridge()
	if !ok || got.Pool != 4 || got.PoolAt != now.Unix() || got.Entry == nil || got.Entry.Clients != 150 || got.Exit == nil || got.Exit.Conns != 2 {
		t.Fatalf("pool and people did not survive the side reports: %+v", got)
	}
	if err := st.SetRelayDirectStreak(ctx, "rly_a", 9); err != nil {
		t.Fatal(err)
	}
	if l = mustRelay(t, st, "rly_a"); l.DirectStreak() != 3 {
		t.Fatalf("a streak above 3 = %d", l.DirectStreak())
	}
	if err := st.SetRelayDirectStreak(ctx, "rly_a", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayBridge(ctx, "rly_a", nil); err != nil {
		t.Fatal(err)
	}
	if l = mustRelay(t, st, "rly_a"); l.DirectStreak() != 0 {
		t.Fatal("a streak that was cleared")
	} else if _, ok := l.Bridge(); ok {
		t.Fatal("a bridge report that was cleared")
	} else if c, ok := l.Check(); !ok || c.Monitor == nil {
		t.Fatalf("clearing the members hurt the check: %+v", c)
	}
	if err := st.SetRelayBridge(ctx, "rly_none", &report); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a bridge report of a relay that is gone: %v", err)
	}
	if err := st.SetRelayDirectStreak(ctx, "rly_none", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a streak of a relay that is gone: %v", err)
	}

	// The entry's port and path of the bridge: a new node has none, the path is given once.
	n, err := st.Node(ctx, "nod_fi1")
	if err != nil || n.BridgePort != 0 || n.BridgePath != "" {
		t.Fatalf("a new node: port %d path %q (%v)", n.BridgePort, n.BridgePath, err)
	}
	if err := st.SetNodeBridgePort(ctx, "nod_fi1", 8443); err != nil {
		t.Fatal(err)
	}
	if path, err := st.EnsureNodeBridgePath(ctx, "nod_fi1", "/assets/a1"); err != nil || path != "/assets/a1" {
		t.Fatalf("first path: %q %v", path, err)
	}
	if path, err := st.EnsureNodeBridgePath(ctx, "nod_fi1", "/assets/b2"); err != nil || path != "/assets/a1" {
		t.Fatalf("a second caller must get the path that was set, got %q %v", path, err)
	}
	if n, err = st.Node(ctx, "nod_fi1"); err != nil || n.BridgePort != 8443 || n.BridgePath != "/assets/a1" {
		t.Fatalf("the node after: port %d path %q (%v)", n.BridgePort, n.BridgePath, err)
	}
	if err := st.SetNodeBridgePort(ctx, "nod_fi1", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeBridgePort(ctx, "nod_none", 443); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a port of a node that is gone: %v", err)
	}
	if _, err := st.EnsureNodeBridgePath(ctx, "nod_none", "/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a path of a node that is gone: %v", err)
	}
	relayExec(t, st, `UPDATE node SET state = 'retired' WHERE id = 'nod_nl1'`)
	if err := st.SetNodeBridgePort(ctx, "nod_nl1", 443); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a port of a retired node: %v", err)
	}
	if _, err := st.EnsureNodeBridgePath(ctx, "nod_nl1", "/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a path of a retired node: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func relayTargetChangeProof(t *testing.T, st *Store) {
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	relayFixture(t, st)
	if err := st.CreateRelay(ctx, Relay{ID: "rly_target", InboundID: "inb_de1_vl", ViaNodeID: "nod_fi1", Network: "tcp", Port: 2443}, now); err != nil {
		t.Fatal(err)
	}
	c := RelayCheck{At: now.Unix(), Network: "tcp", Port: 2443, To: "192.0.2.10:443", Leg: RelayStage{Status: "ok"}}
	if err := st.SetRelayCheck(ctx, "rly_target", c); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayDirectStreak(ctx, "rly_target", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRelayCheck(ctx, "rly_target", c); err != nil {
		t.Fatal(err)
	}
	if mustRelay(t, st, "rly_target").DirectStreak() != 2 {
		t.Fatal("same target lost recovery streak")
	}
	c.To = "192.0.2.10:8443"
	if err := st.SetRelayCheck(ctx, "rly_target", c); err != nil {
		t.Fatal(err)
	}
	if mustRelay(t, st, "rly_target").DirectStreak() != 0 {
		t.Fatal("new target inherited recovery streak")
	}
}

func profileSelectiveBumpProof(t *testing.T, st *Store) {
	ctx := context.Background()
	relayFixture(t, st)
	p, err := st.Access().Profile(ctx, "prf_hy")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "changed profile"
	changed, err := st.Access().UpdateProfile(ctx, p, p.Version, []string{"inb_de1_hy", "inb_de1_hy", "inb_de1_vl"}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	version := func(id string) uint64 {
		t.Helper()
		row, err := st.FleetInbound(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return row.SpecVersion
	}
	if version("inb_de1_hy") != 2 || version("inb_nl1_hy") != 1 || version("inb_de1_vl") != 1 {
		t.Fatal("bumped an unlisted inbound or one of another profile")
	}
	_, err = st.Access().UpdateProfile(ctx, p, p.Version, []string{"inb_nl1_hy"}, false, time.Now())
	if !errors.Is(err, ErrAccessVersion) || version("inb_nl1_hy") != 1 {
		t.Fatalf("stale update modified inbounds: %v", err)
	}
	if _, err = st.Access().UpdateProfile(ctx, changed, changed.Version, nil, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if version("inb_de1_hy") != 2 {
		t.Fatal("client-only update changed inbound version")
	}
}
