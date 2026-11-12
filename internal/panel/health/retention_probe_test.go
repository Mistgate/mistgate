//go:build !js

package health

import (
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// One sweep prunes the on-demand path probes older than 7 days and nothing else of them.
func TestSweepPrunesPathProbesAfterSevenDays(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "hetzner", true)
	now := e.clock.Now()
	var from, to string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT (SELECT id FROM node WHERE name = 'de1'), (SELECT id FROM node WHERE name = 'nl1')`).Scan(&from, &to); err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{time.Hour, 6*24*time.Hour + 23*time.Hour, 7*24*time.Hour + time.Hour, 20 * 24 * time.Hour} {
		if err := e.st.AddPathProbe(e.ctx, from, to, "tcp", now.Add(-age), `{}`); err != nil {
			t.Fatal(err)
		}
	}
	e.s.sweep(e.ctx)
	rows, err := e.st.PathProbes(e.ctx, now.Add(-365*24*time.Hour), "", 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("probes left after the sweep: %d, %v; want the two inside 7 days", len(rows), err)
	}
}

// One sweep prunes the debug captures that started more than 24 hours ago, with their lines, and keeps the newer ones.
func TestSweepPrunesDebugCapturesAfterADay(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	now := e.clock.Now()
	var node string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT id FROM node WHERE name = 'de1'`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	for i, age := range []time.Duration{time.Hour, 23 * time.Hour, 25 * time.Hour, 40 * time.Hour} {
		id := "req_" + string(rune('a'+i))
		at := now.Add(-age)
		row := store.DebugCaptureRow{ID: id, NodeID: node, Actor: "adm_1", Level: "debug", DurationS: 60, StartedAt: at, EndsAt: at.Add(time.Minute)}
		if err := e.st.BeginDebugCapture(e.ctx, row, at); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := e.st.AppendDebugLines(e.ctx, node, id, []store.DebugLineIn{{AtMs: 1, Level: 1, Source: "agent", Message: "x"}}, nil, at); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err := e.st.FinishDebugCapture(e.ctx, node, id, "expired", 0); err != nil {
			t.Fatal(err)
		}
	}
	e.s.sweep(e.ctx)
	rows, err := e.st.DebugCaptures(e.ctx, "", now)
	if err != nil || len(rows) != 2 || rows[0].ID != "req_a" || rows[1].ID != "req_b" {
		t.Fatalf("captures left after the sweep: %+v, %v; want the two inside 24 hours", rows, err)
	}
	var lines int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM debug_capture_line`).Scan(&lines); err != nil || lines != 2 {
		t.Errorf("lines left = %d, %v", lines, err)
	}
}

// One sweep prunes the logs people sent for support and the owner's requests for them 7 days after the request, with their lines,
// and the rows whose device or person is gone; the newer ones, and the lines of node captures, stay.
func TestSweepPrunesClientLogsAfterAWeek(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_c', 'g', 1)`)
	e.exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_c', 'alice-liddell', 'grp_c', 1, x'01', x'02', 1)`)
	e.exec(`INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at) VALUES ('dev_c', 'usr_c', 1, 1, 1)`)
	row := func(id, device string, age time.Duration, state string) {
		at := now.Add(-age).Unix()
		e.exec(`INSERT INTO client_debug (id, user_id, device_id, requested_at, expires_at, state, closed_at, records) VALUES (?, 'usr_c', ?, ?, ?, ?, ?, 1)`, id, device, at, at+3600, state, at)
		e.exec(`INSERT INTO debug_capture_line (capture_id, seq, at_ms, level, source, message) VALUES (?, 1, 1, 1, 'service', 'x')`, id)
	}
	row("dbr_new", "dev_c", time.Hour, "received")
	row("dbr_6d", "dev_c", 6*24*time.Hour+23*time.Hour, "received")
	row("dbr_8d", "dev_c", 8*24*time.Hour, "received")
	row("dbr_8d_asked", "dev_c", 8*24*time.Hour, "declined")
	row("dbr_nodev", "dev_gone", time.Hour, "received")
	e.exec(`INSERT INTO debug_capture_line (capture_id, seq, at_ms, level, source, message) VALUES ('req_node', 1, 1, 1, 'agent', 'x')`)
	e.s.sweep(e.ctx)
	rows, err := e.st.ClientDebugList(e.ctx, "", now)
	if err != nil || len(rows) != 2 || rows[0].ID != "dbr_new" || rows[1].ID != "dbr_6d" {
		t.Fatalf("rows left after the sweep: %+v, %v; want the two inside 7 days", rows, err)
	}
	var lines int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM debug_capture_line`).Scan(&lines); err != nil || lines != 3 {
		t.Errorf("lines left = %d, %v; want the two bundles' and the node capture's", lines, err)
	}
}
