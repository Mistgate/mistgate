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
