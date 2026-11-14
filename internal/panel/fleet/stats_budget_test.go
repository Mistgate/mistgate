package fleet

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func guardStatsForTest(f *Fleet, batch *agentv1.StatsBatch) guardedStats {
	return f.guardStatsForSession(batch, nil, 10*time.Second)
}

func testStatsFrame(seq uint64, intervalEnd time.Time, claimed time.Duration, inbound string, bytesDown uint64) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: intervalEnd.Add(-claimed).Unix(), IntervalEndUnix: intervalEnd.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: inbound, BytesDown: bytesDown}},
	}}}
}

func ingestTestStats(t *testing.T, e *env, core *SessionCore, ctx context.Context, state SessionState, receiveAt, intervalEnd time.Time,
	seq uint64, claimed time.Duration, inbound string, bytesDown uint64) SessionState {
	t.Helper()
	tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: receiveAt,
		Frame: testStatsFrame(seq, intervalEnd, claimed, inbound, bytesDown)})
	if err != nil || tr.Close != nil {
		t.Fatalf("stats seq %d at %s: close=%+v err=%v", seq, receiveAt, tr.Close, err)
	}
	return tr.State
}

func usedBytes(e *env) int64 {
	return e.count(`SELECT used_bytes FROM user WHERE id = 'usr_alice'`)
}

func TestStatsBudgetAllowsASeventyMinuteSession(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-long-session")
	ids := e.fixture(state.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 100
	state = stepHello(t, core, ctx, state, now, hello("instance-stats-long", 0, "").GetHello()).State
	for i := 0; i < 420; i++ {
		at := now.Add(time.Duration(i+1) * 10 * time.Second)
		state = ingestTestStats(t, e, core, ctx, state, at, at, uint64(i+1), 10*time.Second, ids.i1, 500)
	}
	if got, want := usedBytes(e), int64(420*500); got != want {
		t.Fatalf("70 minutes of 10-second batches accounted %d bytes, want %d", got, want)
	}
}

func TestStatsBudgetAcceptsThirtyMinuteOutboxReplayAfterReconnect(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-outbox-replay")
	ids := e.fixture(state.NodeID)
	const perBatch = 100_000_000
	reconnect := now.Add(30 * time.Minute)
	e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Unix(), state.NodeID)
	state = stepHello(t, core, ctx, state, reconnect, hello("instance-stats-replay", 0, "").GetHello()).State
	for i := 0; i < 180; i++ {
		intervalEnd := now.Add(time.Duration(i+1) * 10 * time.Second)
		receiveAt := reconnect.Add(time.Duration(i) * 5 * time.Millisecond)
		state = ingestTestStats(t, e, core, ctx, state, receiveAt, intervalEnd, uint64(i+1), 10*time.Second, ids.i1, perBatch)
	}
	if got, want := usedBytes(e), int64(180*perBatch); got != want {
		t.Fatalf("30-minute replay accounted %d bytes, want all %d", got, want)
	}
}

func TestStatsBudgetIsSharedAcrossReconnects(t *testing.T) {
	e, core, ctx, initial, now := coreFixture(t, "stats-reconnect-budget")
	ids := e.fixture(initial.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 100
	e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Add(-statsBudgetCap).Unix(), initial.NodeID)
	state := stepHello(t, core, ctx, initial, now, hello("instance-stats-reconnect", 0, "").GetHello()).State
	perHour := uint64(100 * time.Hour.Seconds())
	drain := int(statsBudgetCap / time.Hour)
	for i := 0; i < drain; i++ {
		state = ingestTestStats(t, e, core, ctx, state, now, now, uint64(i+1), time.Hour, ids.i1, perHour)
	}
	baseline := usedBytes(e)

	for i := 0; i < 5; i++ {
		at := now.Add(time.Duration(i+1) * time.Second)
		reconnected := initial
		owner, reconnectCtx := e.f.claimOwner(initial.NodeID, e.ctx)
		reconnected.OwnerGeneration = owner
		reconnected.HelloDeadline = at.Add(helloTimeout)
		reconnected = stepHello(t, core, reconnectCtx, reconnected, at, hello("instance-stats-reconnect", 0, "").GetHello()).State
		reconnected = ingestTestStats(t, e, core, reconnectCtx, reconnected, at, at, uint64(drain+i+1), 10*time.Second, ids.i1, 1_000)
		if got := usedBytes(e); got != baseline {
			t.Fatalf("reconnect %d added %d bytes after the bucket was drained; only %d bytes could refill by then",
				i+1, got-baseline, 100*(i+1))
		}
	}
}

func TestStatsBudgetRefillsAtOneSecondPerSecondAndClampsClaims(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-claim-clamp")
	ids := e.fixture(state.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 100
	e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Add(-statsBudgetCap).Unix(), state.NodeID)
	state = stepHello(t, core, ctx, state, now, hello("instance-stats-clamp", 0, "").GetHello()).State
	perHour := uint64(100 * time.Hour.Seconds())
	drain := int(statsBudgetCap / time.Hour)
	for i := 0; i < drain; i++ {
		state = ingestTestStats(t, e, core, ctx, state, now, now, uint64(i+1), time.Hour, ids.i1, perHour)
	}
	baseline := usedBytes(e)

	state = ingestTestStats(t, e, core, ctx, state, now.Add(time.Second), now.Add(time.Second), uint64(drain+1), time.Hour, ids.i1, perHour)
	if got := usedBytes(e); got != baseline {
		t.Fatalf("one-hour claim one second after exhaustion added %d bytes, want none", got-baseline)
	}
	state = ingestTestStats(t, e, core, ctx, state, now.Add(2*time.Second), now.Add(2*time.Second), uint64(drain+2), time.Hour, ids.i1, 100)
	if got, want := usedBytes(e), baseline+100; got != want {
		t.Fatalf("one hour of claimed traffic with one second of refill accounted %d bytes, want %d", got-baseline, 100)
	}
}

func TestStatsBudgetDuplicateDoesNotSpendAndStoreRefusalDoesNotSpend(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		e, core, ctx, state, now := coreFixture(t, "stats-duplicate-budget")
		ids := e.fixture(state.NodeID)
		core.f.cfg.MaxNodeBytesPerSec = 100
		e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Add(-statsBudgetCap).Unix(), state.NodeID)
		state = stepHello(t, core, ctx, state, now, hello("instance-stats-duplicate", 0, "").GetHello()).State
		perHour := uint64(100 * time.Hour.Seconds())
		state = ingestTestStats(t, e, core, ctx, state, now, now, 1, time.Hour, ids.i1, perHour)
		tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Hour),
			Frame: testStatsFrame(1, now, time.Hour, ids.i1, perHour)})
		if err != nil || tr.Close != nil {
			t.Fatalf("duplicate stats: close=%+v err=%v", tr.Close, err)
		}
		state = tr.State
		for seq := uint64(2); seq <= 7; seq++ {
			state = ingestTestStats(t, e, core, ctx, state, now.Add(time.Hour), now.Add(time.Hour), seq, time.Hour, ids.i1, perHour)
		}
		if got, want := usedBytes(e), int64(7*perHour); got != want {
			t.Fatalf("accepted accounting after one duplicate = %d, want %d", got, want)
		}
	})

	t.Run("store refusal", func(t *testing.T) {
		e, core, ctx, state, now := coreFixture(t, "stats-refusal-budget")
		ids := e.fixture(state.NodeID)
		core.f.cfg.MaxNodeBytesPerSec = 100
		e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Add(-statsBudgetCap).Unix(), state.NodeID)
		state = stepHello(t, core, ctx, state, now, hello("instance-stats-refusal", 0, "").GetHello()).State
		e.exec(`CREATE TRIGGER reject_budget_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
		perHour := uint64(100 * time.Hour.Seconds())
		state = ingestTestStats(t, e, core, ctx, state, now, now, 1, time.Hour, ids.i1, perHour)
		e.exec(`DROP TRIGGER reject_budget_stats`)
		for seq := uint64(2); seq <= 7; seq++ {
			state = ingestTestStats(t, e, core, ctx, state, now, now, seq, time.Hour, ids.i1, perHour)
		}
		if got, want := usedBytes(e), int64(6*perHour); got != want {
			t.Fatalf("accounting after the refused batch = %d, want six full hours of budget (%d)", got, want)
		}
	})
}

func TestStatsBudgetStateSurvivesCloudflareLinkReconnect(t *testing.T) {
	r := newLinkRig(t, "stats-budget-reconnect")
	ids := r.e.fixture(r.nodeID)
	r.e.f.cfg.MaxNodeBytesPerSec = 100
	r.helloed(1, "instance-stats-budget-reconnect")
	r.frame(testStatsFrame(1, r.now, 10*time.Second, ids.i1, 1_000))
	spent := r.decoded()
	if spent.StatsBudgetLeftNanos != int64(statsBudgetInitial-10*time.Second) || !spent.StatsBudgetAt.Equal(r.now) {
		t.Fatalf("state after accepted batch = left %s at %s", time.Duration(spent.StatsBudgetLeftNanos), spent.StatsBudgetAt)
	}
	closed := r.call(LinkIn{Kind: LinkClosed, At: r.now.Add(time.Second)})
	reopened := r.call(LinkIn{Kind: LinkOpen, State: closed.State, Generation: 2, At: r.now.Add(time.Second)})
	var state SessionState
	if err := json.Unmarshal([]byte(reopened.State), &state); err != nil {
		t.Fatal(err)
	}
	if state.StatsBudgetLeftNanos != spent.StatsBudgetLeftNanos || !state.StatsBudgetAt.Equal(spent.StatsBudgetAt) {
		t.Fatalf("LinkOpen reset the node's stored stats bucket: left=%s at=%s", time.Duration(state.StatsBudgetLeftNanos), state.StatsBudgetAt)
	}
}

func TestStatsBudgetIsRemovedWhenNodeIsRetired(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "retire-stats-budget")
	ids := e.fixture(state.NodeID)
	state = stepHello(t, core, ctx, state, now, hello("instance-retire-budget", 0, "").GetHello()).State
	state = ingestTestStats(t, e, core, ctx, state, now, now, 1, time.Second, ids.i1, 100)
	e.f.statsBudgetMapMu.Lock()
	_, present := e.f.statsBudgets[state.NodeID]
	e.f.statsBudgetMapMu.Unlock()
	if !present {
		t.Fatal("accepted stats did not create a per-node budget")
	}
	_, err := (nodeService{e.f}).RetireNode(ctx, connect.NewRequest(&adminv1.RetireNodeRequest{
		NodeId: state.NodeID, ConfirmName: "retire-stats-budget",
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.f.statsBudgetMapMu.Lock()
	_, present = e.f.statsBudgets[state.NodeID]
	e.f.statsBudgetMapMu.Unlock()
	if present {
		t.Fatal("retired node kept its in-memory stats budget")
	}
}

func TestClaimedStatsSpanClampsToOneSecondThroughOneHour(t *testing.T) {
	cases := []struct {
		name  string
		start int64
		end   int64
		want  time.Duration
	}{
		{name: "same second", start: 10, end: 10, want: time.Second},
		{name: "backward", start: 11, end: 10, want: time.Second},
		{name: "ordinary", start: 10, end: 20, want: 10 * time.Second},
		{name: "over one hour", start: 0, end: 3601, want: time.Hour},
		{name: "crosses zero", start: -5, end: 5, want: 10 * time.Second},
		{name: "endpoints span int64", start: -1 << 63, end: 1<<63 - 1, want: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claimedStatsSpan(&agentv1.StatsBatch{IntervalStartUnix: tc.start, IntervalEndUnix: tc.end})
			if got != tc.want {
				t.Fatalf("claimed span = %s, want %s", got, tc.want)
			}
		})
	}
}

// The agent arms its timer again only after a batch is built, so the 2,160 batches of a full outbox claim a little
// more than six hours. All of them are counted when they arrive in one burst after a long outage.
func TestStatsBudgetAcceptsAFullOutboxWhoseBatchesRunALittleLong(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-full-outbox")
	ids := e.fixture(state.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 1000
	reconnect := now.Add(8 * time.Hour)
	e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Unix(), state.NodeID)
	state = stepHello(t, core, ctx, state, reconnect, hello("instance-stats-outbox", 0, "").GetHello()).State
	end := reconnect.Add(-7 * time.Hour)
	var want int64
	for i := 0; i < 2160; i++ {
		claimed := 10 * time.Second
		if i%20 == 19 { // 10.05 s on average
			claimed = 11 * time.Second
		}
		end = end.Add(claimed)
		bytes := uint64(800 * claimed.Seconds())
		want += int64(bytes)
		state = ingestTestStats(t, e, core, ctx, state, reconnect.Add(time.Duration(i)*3*time.Millisecond), end, uint64(i+1), claimed, ids.i1, bytes)
	}
	if got := usedBytes(e); got != want {
		t.Fatalf("a full outbox replayed in a burst accounted %d bytes, want all %d", got, want)
	}
}

// Between its retirement and the end of its session a node may report one more interval, once: not one per frame.
func TestStatsBudgetOfARetiringNodeIsSpentOnce(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-retire-window")
	ids := e.fixture(state.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 100
	state = stepHello(t, core, ctx, state, now, hello("instance-stats-retire", 0, "").GetHello()).State
	state = ingestTestStats(t, e, core, ctx, state, now, now, 1, 10*time.Second, ids.i1, 1000)
	if err := e.st.RetireNode(ctx, state.NodeID, now); err != nil {
		t.Fatal(err)
	}
	tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{RequestID: "req_retire",
		Deadline: now.Add(time.Minute), Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Retire{Retire: &agentv1.Retire{RequestId: "req_retire"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	state = tr.State
	if state.RetireAt.IsZero() {
		t.Fatal("the retire step did not start the window")
	}
	e.f.forgetStatsBudget(state.NodeID)
	base := usedBytes(e)
	// the last honest batch is counted
	state = ingestTestStats(t, e, core, ctx, state, now.Add(time.Second), now.Add(time.Second), 2, 10*time.Second, ids.i1, 1000)
	if got := usedBytes(e) - base; got != 1000 {
		t.Fatalf("the last batch of a retiring node accounted %d bytes, want 1000", got)
	}
	// and then nothing more than the seconds that pass
	for i := 0; i < 200; i++ {
		at := now.Add(time.Second + time.Duration(i+1)*20*time.Millisecond)
		if !at.Before(state.RetireAt) {
			break
		}
		state = ingestTestStats(t, e, core, ctx, state, at, at, uint64(3+i), 10*time.Second, ids.i1, 1000)
	}
	window := state.RetireAt.Sub(now)
	if got, limit := usedBytes(e)-base, int64(100*(retireStatsBudget+window).Seconds()); got > limit {
		t.Fatalf("frames in the retire window accounted %d bytes, more than the %d a node's link carries in that time", got, limit)
	}
	e.f.statsBudgetMapMu.Lock()
	_, present := e.f.statsBudgets[state.NodeID]
	e.f.statsBudgetMapMu.Unlock()
	if present {
		t.Fatal("a frame in the retire window put the node's budget back into the map")
	}
}

// A frame stamped earlier that is handled later does not move the bucket's mark back.
func TestStatsBudgetMarkOnlyMovesForward(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	bucket := refillStatsBudget(statsBudgetBucket{left: time.Minute, at: at}, at.Add(-30*time.Second))
	if !bucket.at.Equal(at) || bucket.left != time.Minute {
		t.Fatalf("an earlier frame changed the bucket: %+v", bucket)
	}
	bucket = refillStatsBudget(bucket, at.Add(10*time.Second))
	if bucket.left != time.Minute+10*time.Second {
		t.Fatalf("ten seconds refilled %s", bucket.left-time.Minute)
	}
}

// The panel was down for an hour with the node's batches queuing: the first hello after it credits the hour plus one
// minute, so the whole hour of 10-second batches is booked.
func TestStatsBudgetAfterAnHourOfPanelDowntimeAcceptsTheQueuedHour(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-restart-hour")
	ids := e.fixture(state.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 100
	e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, now.Unix(), state.NodeID)
	reconnect := now.Add(time.Hour)
	state = stepHello(t, core, ctx, state, reconnect, hello("instance-stats-restart", 0, "").GetHello()).State
	const perBatch = 1000 // 100 bytes/s over ten seconds: exactly what the guard allows
	for i := 0; i < 360; i++ {
		intervalEnd := now.Add(time.Duration(i+1) * 10 * time.Second)
		state = ingestTestStats(t, e, core, ctx, state, reconnect, intervalEnd, uint64(i+1), 10*time.Second, ids.i1, perBatch)
	}
	if got, want := usedBytes(e), int64(360*perBatch); got != want {
		t.Fatalf("the queued hour accounted %d bytes, want all %d", got, want)
	}
}

// A node the panel has never heard from gets one minute of credit, not seven hours: a claim of an hour of traffic is
// refused.
func TestStatsBudgetOfANeverSeenNodeIsOneMinute(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "stats-fresh-node")
	ids := e.fixture(state.NodeID)
	core.f.cfg.MaxNodeBytesPerSec = 100
	state = stepHello(t, core, ctx, state, now, hello("instance-stats-fresh", 0, "").GetHello()).State
	if got := time.Duration(state.StatsBudgetLeftNanos); got != statsBudgetInitial {
		t.Fatalf("a fresh node starts with %s of stats credit, want %s", got, statsBudgetInitial)
	}
	state = ingestTestStats(t, e, core, ctx, state, now, now, 1, time.Hour, ids.i1, 100*3600)
	if got := usedBytes(e); got != 0 {
		t.Fatalf("a fresh node booked %d bytes for an hour it claimed", got)
	}
	// The refused claim used the minute up; a normal batch ten seconds later is credited by the time that passed.
	later := now.Add(10 * time.Second)
	ingestTestStats(t, e, core, ctx, state, later, later, 2, 10*time.Second, ids.i1, 1000)
	if got := usedBytes(e); got != 1000 {
		t.Fatalf("a normal batch booked %d bytes, want 1000", got)
	}
}

// Credit carried in the session state (the edge keeps it between sockets) is not thrown away by a hello that finds less
// elapsed panel time, and a hello never adds more than the time that really passed.
func TestStatsBudgetHelloKeepsLargerCarriedCredit(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "stats-carried")
	state.StatsBudgetLeftNanos, state.StatsBudgetAt = int64(3*time.Hour), now
	state = stepHello(t, core, ctx, state, now.Add(time.Minute), hello("instance-stats-carried", 0, "").GetHello()).State
	if got, want := time.Duration(state.StatsBudgetLeftNanos), 3*time.Hour+time.Minute; got != want {
		t.Fatalf("carried credit after hello = %s, want %s", got, want)
	}
}
