//go:build !js

package health

import (
	"testing"
	"time"
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
