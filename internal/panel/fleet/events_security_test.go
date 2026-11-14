package fleet

import (
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestAgentEventDailyBudgetIsPerClass(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectFull(a, "event-security")
	now := time.Now().UTC().Truncate(time.Second)
	c.send(1, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		TimeUnix: now.Unix(), Severity: 1, Code: "agent_note", Params: map[string]string{"status": "ready"},
	}}})
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 1 })
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND source = 'agent' AND code = 'agent_note'`, a.nodeID); got != 1 {
		t.Fatalf("normal agent event stored %d rows, want one", got)
	}
	ingest := func(seq uint64, at time.Time, sev int, code string) bool {
		t.Helper()
		_, over, err := e.st.IngestEventBounded(e.ctx, a.nodeID, "event-security", seq, at, store.EventRow{Time: at, Severity: sev, Code: code})
		if err != nil {
			t.Fatalf("event seq %d: %v", seq, err)
		}
		return over
	}
	// Fill the info budget (the first row is already in): the next info event is refused, the other classes are not.
	seq := uint64(2)
	for ; seq <= 2000; seq++ {
		if ingest(seq, now, 1, "agent_note") {
			t.Fatalf("info event %d refused before the budget of 2000", seq)
		}
	}
	if !ingest(seq, now, 1, "agent_note") {
		t.Fatal("2001st info event of the day was stored")
	}
	seq++
	if ingest(seq, now, 3, "engine_failed") || ingest(seq+1, now, 2, "clock_skew") || ingest(seq+2, now, 2, "torrent_attempt") {
		t.Fatal("an error, warning or torrent event was refused because the info budget is spent")
	}
	count := func(where string) int64 {
		return e.count(`SELECT count(*) FROM event WHERE node_id = ? AND source = 'agent' AND `+where, a.nodeID)
	}
	if got := count(`severity = 1`); got != 2000 {
		t.Fatalf("stored info rows = %d, want the budget 2000", got)
	}
	if count(`code = 'engine_failed'`) != 1 || count(`code = 'clock_skew'`) != 1 || count(`code = 'torrent_attempt'`) != 1 {
		t.Fatal("events of the other classes were not stored")
	}
	// Torrent detections do not use the warning budget: a thousand of them leave the warnings alone.
	for i := uint64(0); i < 1100; i++ {
		ingest(seq+10+i, now, 2, "torrent_attempt")
	}
	if got := count(`code = 'torrent_attempt'`); got != 1000 {
		t.Fatalf("stored torrent rows = %d, want the budget 1000", got)
	}
	if ingest(seq+2000, now, 2, "clock_skew") {
		t.Fatal("a warning was refused because torrent detections were over their budget")
	}
	markers := func(level string) int64 {
		return e.count(`SELECT count(*) FROM event WHERE node_id = ? AND source = 'panel' AND code = 'node_event_daily_limit' AND json_extract(params_json, '$.level') = ?`, a.nodeID, level)
	}
	if markers("info") != 1 || markers("torrent") != 1 || markers("warning") != 0 || markers("error") != 0 {
		t.Fatalf("markers info=%d torrent=%d warning=%d error=%d, want one for each spent class only",
			markers("info"), markers("torrent"), markers("warning"), markers("error"))
	}
	// The next UTC day starts the budgets afresh.
	if ingest(seq+3000, now.Add(24*time.Hour), 1, "agent_note") {
		t.Fatal("first info event of the next day was refused")
	}
}

func TestPanelEventCodesAreReservedAndLastEventLooksAtPanelRows(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectFull(a, "reserved-event")
	for i, code := range []string{"node_down", "stats_rejected"} {
		c.send(uint64(i+1), &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
			TimeUnix: time.Now().Unix(), Severity: 3, Code: code,
		}}})
	}
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 2 })
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND source = 'agent' AND code IN ('node_down','stats_rejected')`, a.nodeID); got != 0 {
		t.Fatalf("node stored %d panel-reserved event rows", got)
	}
	// stats_dropped is raised by honest agents too (their outbox overflowed): it is stored.
	c.send(3, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		TimeUnix: time.Now().Unix(), Severity: 2, Code: "stats_dropped", Params: map[string]string{"batches": "3"},
	}}})
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 3 })
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND source = 'agent' AND code = 'stats_dropped'`, a.nodeID); got != 1 {
		t.Fatalf("the agent's own stats_dropped event stored %d rows, want one", got)
	}
	// A legacy/other-source row with the same code cannot suppress the panel's node state.
	e.exec(`INSERT INTO event (ts, received_at, severity, code, source, node_id, params_json) VALUES (?, ?, 3, 'node_down', 'agent', ?, '{}')`,
		time.Now().Unix(), time.Now().Unix(), a.nodeID)
	if code, err := e.st.LastNodeEventCode(e.ctx, a.nodeID, "node_down", "node_recovered"); err != nil || code != "" {
		t.Fatalf("last event from an agent row = %q, %v, want empty", code, err)
	}
	e.f.event(e.ctx, 3, "node_down", a.nodeID, map[string]string{"minutes": "20"})
	if code, err := e.st.LastNodeEventCode(e.ctx, a.nodeID, "node_down", "node_recovered"); err != nil || code != "node_down" {
		t.Fatalf("last panel event = %q, %v, want node_down", code, err)
	}
}

func TestPanelEventCodeRegistryIsSpecific(t *testing.T) {
	for _, code := range []string{"node_down", "node_recovered", "stats_rejected", "traffic_stopped", "update_step_failed", "node_event_daily_limit"} {
		if !store.IsPanelEventCode(code) {
			t.Errorf("panel code %q is not reserved", code)
		}
	}
	for _, code := range []string{"agent_note", "os_update_done", "warp_needs_attention", "torrent_attempt", "stats_dropped"} {
		if store.IsPanelEventCode(code) {
			t.Errorf("agent code %q is incorrectly reserved", code)
		}
	}
}
