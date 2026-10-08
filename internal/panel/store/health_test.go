//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func execT(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.W.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// fixtureInbound inserts a node, a profile and an inbound (the rows health tables point at).
func fixtureInbound(t *testing.T, s *Store, suffix string) (nodeID, inboundID string) {
	t.Helper()
	nodeID, inboundID = "nod_"+suffix, "inb_"+suffix
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, 'n.example.com', 'active', 1)`, nodeID, "n"+suffix)
	execT(t, s, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, 'hysteria2', ?, '{}', 1, 1)`, "prf_"+suffix, "p"+suffix)
	execT(t, s, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, inboundID, "prf_"+suffix, nodeID)
	return nodeID, inboundID
}

func TestAlertLifecycleInStore(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	t0 := time.Unix(10_000, 0)
	a := HealthAlert{Kind: "no_traffic", Severity: 3, NodeID: "nod_a", TitleKey: "t", WhyKey: "w", Params: map[string]string{"failed": "2"}}

	opened, reopened, err := s.OpenAlert(ctx, a, time.Hour, t0)
	if err != nil || reopened || opened.ID == "" || !opened.FirstSeen.Equal(t0) || !opened.OpenedAt.Equal(opened.FirstSeen) {
		t.Fatalf("open: %+v reopened=%v err=%v", opened, reopened, err)
	}
	// a second active alert of the same key is refused by the unique index, not duplicated
	if _, err := s.W.ExecContext(ctx, `INSERT INTO health_alert (id, kind, severity, node_id, subject, title_key, first_seen, last_seen, created_at)
		VALUES ('alt_dup', 'no_traffic', 3, 'nod_a', '', 't', 1, 1, 1)`); err == nil {
		t.Fatal("two active alerts of one key")
	}
	if ok, err := s.ResolveAlert(ctx, opened.ID, "cleared", t0.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("resolve: %v %v", ok, err)
	}
	if ok, _ := s.ResolveAlert(ctx, opened.ID, "cleared", t0.Add(2*time.Minute)); ok {
		t.Fatal("a resolved alert resolved twice")
	}
	if act, _ := s.ActiveAlerts(ctx); len(act) != 0 {
		t.Fatalf("active after resolve: %d", len(act))
	}
	// within the hour the same key re-opens the same row; its mute survives
	if _, err := s.MuteAlert(ctx, opened.ID, t0.Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("mute of a resolved alert: %v", err)
	}
	again, reopened, err := s.OpenAlert(ctx, a, time.Hour, t0.Add(30*time.Minute))
	if err != nil || !reopened || again.ID != opened.ID || !again.FirstSeen.Equal(t0) {
		t.Fatalf("reopen: %+v reopened=%v err=%v", again, reopened, err)
	}
	// the re-open starts a new episode: first_seen stays, opened_at moves (and is stored, not just returned)
	if !again.OpenedAt.Equal(t0.Add(30 * time.Minute)) {
		t.Fatalf("reopen opened_at = %v, want %v", again.OpenedAt, t0.Add(30*time.Minute))
	}
	if stored, err := s.HealthAlert(ctx, again.ID); err != nil || !stored.OpenedAt.Equal(again.OpenedAt) || !stored.FirstSeen.Equal(t0) {
		t.Fatalf("stored reopen: %+v %v", stored, err)
	}
	if m, err := s.MuteAlert(ctx, again.ID, t0.Add(2*time.Hour)); err != nil || !m.MutedUntil.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("mute: %+v %v", m, err)
	}
	if n, crit, _ := s.AlertCounts(ctx, t0.Add(31*time.Minute)); n != 0 || crit != 0 {
		t.Fatalf("muted alert counted: %d %d", n, crit)
	}
	if n, crit, _ := s.AlertCounts(ctx, t0.Add(3*time.Hour)); n != 1 || crit != 1 {
		t.Fatalf("counts after the mute ended: %d %d", n, crit)
	}
	// a warning about a person is not a problem of the fleet: the badge leaves it out
	if _, _, err := s.OpenAlert(ctx, HealthAlert{Kind: "access_ended", Severity: 2, Subject: "usr_1", TitleKey: "t", WhyKey: "w"}, time.Hour, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, crit, _ := s.AlertCounts(ctx, t0.Add(3*time.Hour)); n != 1 || crit != 1 {
		t.Fatalf("alerts about people counted: %d %d", n, crit)
	}
	// after more than an hour a new row is made
	if _, err := s.ResolveAlert(ctx, again.ID, "cleared", t0.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fresh, reopened, err := s.OpenAlert(ctx, a, time.Hour, t0.Add(40*time.Minute+61*time.Minute))
	if err != nil || reopened || fresh.ID == again.ID || !fresh.FirstSeen.Equal(fresh.OpenedAt) || !fresh.OpenedAt.Equal(t0.Add(101*time.Minute)) {
		t.Fatalf("new row: %+v reopened=%v err=%v", fresh, reopened, err)
	}
	hist, err := s.AlertHistory(ctx, t0, "", 10)
	if err != nil || len(hist) != 1 || hist[0].ID != again.ID || hist[0].Resolution != "cleared" {
		t.Fatalf("history: %+v %v", hist, err)
	}
}

func TestProbeCredFollowsItsInbound(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, inboundID := fixtureInbound(t, s, "a")
	now := time.Unix(100, 0)
	row := ProbeCredRow{InboundID: inboundID, CredID: "crd_1", SecretEnc: []byte{9}, DataJSON: `{"auth_sha256":"x"}`}
	if err := s.InsertProbeCred(ctx, row, now); err != nil {
		t.Fatal(err)
	}
	row2 := row
	row2.CredID = "crd_2"
	if err := s.InsertProbeCred(ctx, row2, now); err != nil { // a racing second insert is ignored
		t.Fatal(err)
	}
	got, err := s.ProbeCred(ctx, inboundID)
	if err != nil || got.CredID != "crd_1" {
		t.Fatalf("probe cred: %+v %v", got, err)
	}
	if err := s.InsertProbeCred(ctx, ProbeCredRow{InboundID: "inb_missing", CredID: "crd_3", SecretEnc: []byte{1}, DataJSON: `{}`}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cred of an unknown inbound: %v", err)
	}
	if err := s.Access().DeleteInbound(ctx, inboundID, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProbeCred(ctx, inboundID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cred survived its inbound: %v", err)
	}
}

func TestDoctorRowsReplaceAndMerge(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "a")
	row := func(id string, st int) DoctorRow {
		return DoctorRow{NodeID: nodeID, CheckID: id, Status: st, Params: map[string]string{"k": "v"}, Measured: time.Unix(50, 0)}
	}
	t1, t2 := time.Unix(100, 0), time.Unix(200, 0)
	if err := s.PutDoctor(ctx, nodeID, []DoctorRow{row("disk_space", 2), row("resolver", 1)}, true, t1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDoctor(ctx, nodeID, []DoctorRow{row("disk_space", 1)}, false, t2); err != nil { // partial: merge
		t.Fatal(err)
	}
	got, _ := s.DoctorResults(ctx, nodeID)
	if len(got) != 2 || got[0].CheckID != "disk_space" || got[0].Status != 1 || !got[0].Received.Equal(t2) || !got[1].Received.Equal(t1) || got[0].Params["k"] != "v" {
		t.Fatalf("after merge: %+v", got)
	}
	if err := s.PutDoctor(ctx, nodeID, []DoctorRow{row("ipv6", 4)}, true, t2); err != nil { // full: replace
		t.Fatal(err)
	}
	if got, _ := s.DoctorResults(ctx, ""); len(got) != 1 || got[0].CheckID != "ipv6" {
		t.Fatalf("after replace: %+v", got)
	}
}

func TestSamplesRollupAndPrune(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, in := fixtureInbound(t, s, "a")
	day := int64(20_000) * 86400
	put := func(at int64, st int, lat uint32) {
		t.Helper()
		if err := s.InsertSample(ctx, CheckSample{InboundID: in, At: time.Unix(at, 0), Status: st, LatencyMS: lat}); err != nil {
			t.Fatal(err)
		}
	}
	put(day+100, 1, 100)
	put(day+400, 1, 300)
	put(day+700, 2, 200)
	put(day+1000, 3, 0)
	put(day+86400+50, 1, 50)                 // the next day is still running at `now`
	now := time.Unix(day+86400+3600+1100, 0) // 25 h and 18 min after the first round

	if err := s.RollupDaily(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RollupDaily(ctx, now); err != nil { // idempotent
		t.Fatal(err)
	}
	rows, _ := s.DailyRows(ctx, in)
	if len(rows) != 1 || rows[0].OK != 2 || rows[0].Degraded != 1 || rows[0].Failed != 1 || rows[0].P50MS != 200 || rows[0].P95MS != 300 || rows[0].Day.Unix() != day {
		t.Fatalf("daily: %+v", rows)
	}
	recent, _ := s.RecentSamples(ctx, in, 2)
	if len(recent) != 2 || recent[0].At.Unix() != day+86400+50 {
		t.Fatalf("recent: %+v", recent)
	}

	// raw rounds older than 25 h go, the aggregate stays until it is 90 days old
	if _, err := s.PruneHealth(ctx, now, 25*time.Hour, 90*24*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	left, _ := s.SamplesSince(ctx, time.Unix(0, 0))
	if len(left) != 1 || left[0].At.Unix() != day+86400+50 {
		t.Fatalf("samples after prune: %+v", left)
	}
	if rows, _ := s.DailyRows(ctx, ""); len(rows) != 1 {
		t.Fatalf("daily after prune: %d", len(rows))
	}
	if _, err := s.PruneHealth(ctx, now.Add(91*24*time.Hour), 25*time.Hour, 90*24*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.DailyRows(ctx, ""); len(rows) != 0 {
		t.Fatalf("daily after 90 days: %d", len(rows))
	}
}

func TestPruneEventsKeepsWarningsLonger(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Unix(1_800_000_000, 0)
	day := 24 * time.Hour
	add := func(sev int, age time.Duration) {
		t.Helper()
		execT(t, s, `INSERT INTO event (ts, severity, code, source, params_json) VALUES (?, ?, 'x', 'panel', '{}')`, now.Add(-age).Unix(), sev)
	}
	add(1, 10*day)                                        // kept
	add(1, 91*day)                                        // pruned
	add(1, 500*day)                                       // pruned
	add(2, 100*day)                                       // kept: warnings live 400 days
	add(3, 399*day)                                       // kept
	add(3, 401*day)                                       // pruned
	add(2, 900*day)                                       // pruned
	n, err := s.PruneEvents(ctx, now, 90*day, 400*day, 2) // batch 2 to walk the loop
	if err != nil || n != 4 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	var left int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM event`).Scan(&left); err != nil || left != 3 {
		t.Fatalf("left %d: %v", left, err)
	}
}
