//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestPathSpeedsKeepSevenDaysPruneAndFollowTheNode(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	probeNodes(t, st)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Hour, 6 * 24 * time.Hour, 8 * 24 * time.Hour} {
		if err := st.AddPathSpeed(ctx, "nod_pp_a", "nod_pp_b", "down", now.Add(-age), `{"mb":16,"mbit_s":93.1}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddPathSpeed(ctx, "nod_pp_a", "nod_pp_b", "both", now, `{}`); err == nil {
		t.Fatal("a direction outside down and up was stored")
	}
	if err := st.AddPathSpeed(ctx, "nod_pp_a", "nod_missing", "up", now, `{}`); err == nil {
		t.Fatal("a test against a node that does not exist was stored")
	}
	n, err := st.PrunePathSpeeds(ctx, now.Add(-time.Duration(PathSpeedKeepHours)*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PrunePathSpeeds = %d, %v; want the one of 8 days", n, err)
	}
	rows, err := st.PathSpeeds(ctx, now.Add(-30*24*time.Hour), "", 0)
	if err != nil || len(rows) != 2 || !rows[0].At.After(rows[1].At) {
		t.Fatalf("rows = %+v, %v; want two, newest first", rows, err)
	}
	if got, _ := st.PathSpeeds(ctx, now.Add(-30*24*time.Hour), "nod_unknown", 0); len(got) != 0 {
		t.Fatalf("a node that is no end of a test matched: %+v", got)
	}
	if got, _ := st.PathSpeeds(ctx, now.Add(-30*24*time.Hour), "nod_pp_b", 1); len(got) != 1 {
		t.Fatalf("the limit or the target match failed: %+v", got)
	}
	// the node going away takes its tests
	if _, err := st.W.ExecContext(ctx, `DELETE FROM node WHERE id = 'nod_pp_b'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PathSpeeds(ctx, time.Unix(0, 0), "", 0); len(got) != 0 {
		t.Fatalf("tests left after their node was deleted: %+v", got)
	}
}
