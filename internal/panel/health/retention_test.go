package health

import (
	"testing"
	"time"
)

// One sweep applies every retention rule: a finished day is rolled up before its raw rounds are pruned (25 h),
// the aggregates and resolved alerts go after 90 days, events after 90 days (info) or 400 days (warning, error).
func TestSweepAppliesRetention(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	in := e.inbound("de1", 443)
	now := e.clock.Now()
	day := 24 * time.Hour

	// rounds: one from 26 h ago, one from an hour ago
	old := now.Add(-26 * time.Hour)
	e.s.record(e.ctx, in, Result{Status: cOK, At: old, LatencyMS: 100})
	e.s.record(e.ctx, in, Result{Status: cFail, At: now.Add(-time.Hour), ErrorCode: "tls"})

	// events
	add := func(sev int, age time.Duration) {
		e.exec(`INSERT INTO event (ts, severity, code, source, params_json) VALUES (?, ?, 'x', 'panel', '{}')`, now.Add(-age).Unix(), sev)
	}
	add(1, 89*day)
	add(1, 91*day)
	add(3, 399*day)
	add(3, 401*day)

	// alerts: a blip record from 100 days ago and one from yesterday
	e.s.NodeReturned(e.ctx, "de1", now.Add(-100*day-time.Minute), now.Add(-100*day))
	e.s.NodeReturned(e.ctx, "de1", now.Add(-day-time.Minute), now.Add(-day))

	e.s.sweep(e.ctx)

	rows, _ := e.st.SamplesSince(e.ctx, time.Unix(0, 0))
	if len(rows) != 1 || rows[0].Status != int(cFail) {
		t.Fatalf("raw rounds: %+v", rows)
	}
	var events int // only the rows added above: the fixture's inbound wrote a profile_added of its own
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM event WHERE code = 'x'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("events left: %d %v", events, err)
	}
	var alerts int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM health_alert`).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("alerts left: %d %v", alerts, err)
	}
	daily, _ := e.st.DailyRows(e.ctx, in)
	if len(daily) != 1 || daily[0].OK != 1 || daily[0].P50MS != 100 { // the round of 26 h ago was rolled up before it was pruned
		t.Fatalf("daily: %+v", daily)
	}

	e.clock.Advance(91 * day)
	e.s.sweep(e.ctx)
	if daily, _ := e.st.DailyRows(e.ctx, in); len(daily) != 0 {
		t.Fatalf("daily after 90 days: %+v", daily)
	}
}
