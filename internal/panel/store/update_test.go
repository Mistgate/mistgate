package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// A live instance is at version 12 (dns, health) with nodes, inbounds, events and alerts: 00013 must apply on top of
// that data without touching it, the old node rows must read with the new columns at their defaults, and it must roll
// back again and go forward a second time.
func TestUpdatesMigrationUpgradesVersion12(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "live.db")
	w, err := openDB(path, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := openDB(path, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{W: w, R: r}
	t.Cleanup(func() { s.Close() })
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 12); err != nil {
		t.Fatalf("up to 12: %v", err)
	}
	for _, table := range []string{"update_rollout", "update_step"} {
		var n int
		if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s before the migration: n=%d err=%v", table, n, err)
		}
	}
	nodeID, inboundID := fixtureInbound(t, s, "a")
	execT(t, s, `UPDATE node SET agent_version = '0.1.0-abc', desired_hash = 'h1', applied_hash = 'h1' WHERE id = ?`, nodeID)
	execT(t, s, `INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (1000, 1, 'node_blip', 'panel', ?, '{}')`, nodeID)
	execT(t, s, `INSERT INTO health_alert (id, kind, severity, node_id, title_key, first_seen, last_seen, created_at) VALUES ('alt_a', 'node_down', 3, ?, 't', 1, 1, 1)`, nodeID)

	if _, err := p.UpTo(ctx, 13); err != nil {
		t.Fatalf("up to 13: %v", err)
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM node) + (SELECT count(*) FROM inbound) + (SELECT count(*) FROM event) + (SELECT count(*) FROM health_alert)`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("existing rows after upgrade: %d %v", n, err)
	}
	// The Go row scan follows the newest schema (nodeCols carries columns added through 00036), so a node is read only after
	// the later migrations; rows of version 12 must have survived all of them.
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up to latest: %v", err)
	}
	node, err := s.Node(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.AgentVersion != "0.1.0-abc" || node.AppliedHash != "h1" || node.AgentBuilt != 0 || len(node.AgentCaps) != 0 || node.LastUpdateJSON != "" {
		t.Fatalf("old node row after upgrade: %+v", node)
	}
	if _, ok := node.LastUpdate(); ok {
		t.Fatal("an old node has no last update")
	}
	// the new tables work against the old rows
	ro := RolloutRow{ID: "rol_a", Status: RolloutRunning, ToVersion: "0.2.0", ToBuilt: 100, Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 1, CreatedAt: time.Unix(5, 0)}
	if err := s.CreateRollout(ctx, ro, []StepRow{{NodeID: nodeID, NodeName: "na", State: StepPending}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NodeHello(ctx, nodeID, HelloInfo{AgentVersion: "0.2.0", Built: 100, Caps: []string{"update/1"}}, time.Unix(9, 0)); err != nil {
		t.Fatal(err)
	}
	_ = inboundID

	if _, err := p.DownTo(ctx, 12); err != nil {
		t.Fatalf("down again: %v", err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM node) + (SELECT count(*) FROM inbound) + (SELECT count(*) FROM event) + (SELECT count(*) FROM health_alert)`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows after rolling back: %d %v", n, err)
	}
	var hash string
	if err := s.R.QueryRowContext(ctx, `SELECT applied_hash FROM node WHERE id = ?`, nodeID).Scan(&hash); err != nil || hash != "h1" {
		t.Fatalf("node after rolling back: %q %v", hash, err)
	}
	for _, table := range []string{"update_rollout", "update_step"} {
		if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s survived the rollback: n=%d err=%v", table, n, err)
		}
	}
	if _, err := p.UpTo(ctx, 13); err != nil { // and forward again
		t.Fatalf("up again: %v", err)
	}
}

func TestHelloStoresBuildCapsAndKeepsLastUpdate(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "a")
	now := time.Unix(1000, 0)
	lu := LastUpdateRow{Outcome: "rolled_back", FromVersion: "a", FromBuilt: 10, ToVersion: "b", ToBuilt: 20, Reason: "not_committed", AtUnix: 990}

	if _, _, err := s.NodeHello(ctx, nodeID, HelloInfo{AgentVersion: "b", Instance: "i1", Built: 10, Caps: []string{"doctor/1", "update/1", "update-guard/1"}, LastUpdateJSON: lu.JSON()}, now); err != nil {
		t.Fatal(err)
	}
	n, _ := s.Node(ctx, nodeID)
	if n.AgentBuilt != 10 || len(n.AgentCaps) != 3 || n.AgentCaps[1] != "update/1" {
		t.Fatalf("hello data: %+v", n)
	}
	got, ok := n.LastUpdate()
	if !ok || got != lu {
		t.Fatalf("last update: %+v %v", got, ok)
	}
	// the next Hello carries no outcome (the agent reported it once): what is stored stays
	if _, _, err := s.NodeHello(ctx, nodeID, HelloInfo{AgentVersion: "b", Instance: "i2", Built: 20, Caps: []string{"doctor/1"}}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, _ = s.Node(ctx, nodeID)
	if n.AgentBuilt != 20 || len(n.AgentCaps) != 1 {
		t.Fatalf("second hello: %+v", n)
	}
	if got, ok := n.LastUpdate(); !ok || got != lu {
		t.Fatalf("last update was not kept: %+v %v", got, ok)
	}
}

func TestRolloutStore(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	t0 := time.Unix(10_000, 0)
	mk := func(id string, status string, at time.Time) RolloutRow {
		return RolloutRow{ID: id, Status: status, ToVersion: "v", ToBuilt: 5, Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 2, CreatedAt: at}
	}
	steps := []StepRow{
		{NodeID: "nod_b", NodeName: "b", Stage: 1, State: StepPending},
		{NodeID: "nod_a", NodeName: "A", Stage: 0, State: StepPending},
	}
	if _, err := s.ActiveRollout(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no rollout yet: %v", err)
	}
	if err := s.CreateRollout(ctx, mk("rol_1", RolloutRunning, t0), steps); err != nil {
		t.Fatal(err)
	}
	// one active rollout, enforced by the database (running and paused both count)
	if err := s.CreateRollout(ctx, mk("rol_2", RolloutRunning, t0), nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active rollout: %v", err)
	}
	got, err := s.RolloutSteps(ctx, "rol_1")
	if err != nil || len(got) != 2 || got[0].NodeID != "nod_a" || got[1].NodeID != "nod_b" {
		t.Fatalf("steps are ordered by stage: %+v %v", got, err)
	}
	// compare-and-set: only one of two pause calls wins
	ok, err := s.SetRolloutStatus(ctx, "rol_1", []string{RolloutRunning}, RolloutPaused, "gate_failed", map[string]string{"node": "a"}, time.Time{})
	if err != nil || !ok {
		t.Fatalf("pause: %v %v", ok, err)
	}
	if ok, _ := s.SetRolloutStatus(ctx, "rol_1", []string{RolloutRunning}, RolloutPaused, "owner", nil, time.Time{}); ok {
		t.Fatal("pausing a paused rollout must not win again")
	}
	ro, _ := s.ActiveRollout(ctx)
	if ro.Status != RolloutPaused || ro.PauseKey != "gate_failed" || ro.PauseParams["node"] != "a" || !ro.Active() {
		t.Fatalf("paused rollout: %+v", ro)
	}
	// a step is saved in place
	got[0].State, got[0].ErrorKey, got[0].PreFailed, got[0].SentAt = StepFailed, "hash_mismatch", []string{"inb_1"}, t0
	if err := s.SaveStep(ctx, got[0]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RolloutSteps(ctx, "rol_1")
	if got[0].State != StepFailed || got[0].ErrorKey != "hash_mismatch" || len(got[0].PreFailed) != 1 || !got[0].SentAt.Equal(t0) || !got[0].Decided() || got[1].Decided() {
		t.Fatalf("saved step: %+v", got)
	}
	if err := s.SaveStep(ctx, StepRow{RolloutID: "rol_1", NodeID: "nod_zz"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown step: %v", err)
	}
	if err := s.SkipPendingSteps(ctx, "rol_1", "cancelled", t0); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RolloutSteps(ctx, "rol_1")
	if got[1].State != StepSkipped || got[1].ErrorKey != "cancelled" || got[0].State != StepFailed {
		t.Fatalf("after skipping the pending steps: %+v", got)
	}
	if ok, _ := s.SetRolloutStatus(ctx, "rol_1", []string{RolloutRunning, RolloutPaused}, RolloutFailed, "ignored", nil, t0.Add(time.Hour)); !ok {
		t.Fatal("finish")
	}
	ro, err = s.Rollout(ctx, "rol_1")
	if err != nil || ro.Status != RolloutFailed || ro.PauseKey != "" || ro.Active() || !ro.FinishedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("finished rollout: %+v %v", ro, err)
	}
	// a finished one makes room for the next
	if err := s.CreateRollout(ctx, mk("rol_2", RolloutRunning, t0.Add(time.Hour)), nil); err != nil {
		t.Fatal(err)
	}
	if last, err := s.LatestRollout(ctx); err != nil || last.ID != "rol_2" {
		t.Fatalf("latest: %+v %v", last, err)
	}
}

func TestPruneRollouts(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	day := 24 * time.Hour
	base := time.Unix(1_000_000_000, 0)
	for i, status := range []string{RolloutDone, RolloutFailed, RolloutCancelled, RolloutDone} {
		id := "rol_" + string(rune('a'+i))
		if err := s.CreateRollout(ctx, RolloutRow{ID: id, Status: status, ToVersion: "v", ToBuilt: 1, Manifest: []byte("m"), Signature: []byte("s"),
			BatchSize: 1, CreatedAt: base.Add(time.Duration(i) * day), FinishedAt: base.Add(time.Duration(i)*day + time.Hour)},
			[]StepRow{{NodeID: "nod_1", NodeName: "n", State: StepPassed}}); err != nil {
			t.Fatal(err)
		}
	}
	active := RolloutRow{ID: "rol_live", Status: RolloutPaused, ToVersion: "v", ToBuilt: 1, Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 1, CreatedAt: base}
	if err := s.CreateRollout(ctx, active, []StepRow{{NodeID: "nod_1", NodeName: "n", State: StepPending}}); err != nil {
		t.Fatal(err)
	}
	// keep the newest 2 finished ones; anything older than the cutoff that is not among them goes, the active one stays
	n, err := s.PruneRollouts(ctx, 2, base.Add(10*day))
	if err != nil || n != 2 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	var left int
	s.R.QueryRowContext(ctx, `SELECT count(*) FROM update_rollout`).Scan(&left)
	var stepsLeft int
	s.R.QueryRowContext(ctx, `SELECT count(*) FROM update_step`).Scan(&stepsLeft)
	if left != 3 || stepsLeft != 3 { // rol_c, rol_d and the active one, steps with them
		t.Fatalf("rollouts left %d, steps left %d", left, stepsLeft)
	}
	if _, err := s.Rollout(ctx, "rol_live"); err != nil {
		t.Fatal(err)
	}
	// nothing is old enough: nothing goes
	if n, _ := s.PruneRollouts(ctx, 0, base.Add(-day)); n != 0 {
		t.Fatalf("pruned %d young rollouts", n)
	}
}

func TestNodeEventSince(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (1000, 1, 'update_committed', 'agent', ?, '{}')`, nodeID)
	execT(t, s, `INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (2000, 1, 'update_committed', 'agent', ?, '{"to_built":"77"}')`, nodeID)
	for _, tc := range []struct {
		code    string
		since   int64
		toBuilt int64
		want    bool
	}{
		{"update_committed", 1000, 0, true}, {"update_committed", 2001, 0, false}, {"update_rolled_back", 0, 0, false},
		// an event without to_built (older agent) is judged by time alone; one that has it must name the build
		{"update_committed", 1000, 5, true}, {"update_committed", 1001, 77, true}, {"update_committed", 1001, 5, false},
		{"update_committed", 2001, 77, false},
	} {
		got, err := s.NodeEventSince(ctx, nodeID, tc.code, time.Unix(tc.since, 0), tc.toBuilt)
		if err != nil || got != tc.want {
			t.Fatalf("%s since %d build %d: got %v err %v", tc.code, tc.since, tc.toBuilt, got, err)
		}
	}
	if got, _ := s.NodeEventSince(ctx, "nod_other", "update_committed", time.Unix(0, 0), 0); got {
		t.Fatal("another node event")
	}
}

func TestSetLastUpdateIfNewer(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "a")
	built := func() int64 {
		n, err := s.Node(ctx, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		l, _ := n.LastUpdate()
		return l.ToBuilt
	}
	if err := s.SetLastUpdateIfNewer(ctx, nodeID, LastUpdateRow{Outcome: "ok", ToBuilt: 5, AtUnix: 100}); err != nil || built() != 5 {
		t.Fatalf("nothing stored yet: %d %v", built(), err)
	}
	if err := s.SetLastUpdateIfNewer(ctx, nodeID, LastUpdateRow{Outcome: "ok", ToBuilt: 6, AtUnix: 90}); err != nil || built() != 5 {
		t.Fatalf("an older outcome replaced a newer one: %d %v", built(), err)
	}
	if err := s.SetLastUpdateIfNewer(ctx, nodeID, LastUpdateRow{Outcome: "ok", ToBuilt: 7, AtUnix: 100}); err != nil || built() != 7 {
		t.Fatalf("a newer outcome was not stored: %d %v", built(), err)
	}
}
