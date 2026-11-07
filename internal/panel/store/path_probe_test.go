//go:build !js

package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func probeNodes(t *testing.T, st *Store) {
	t.Helper()
	for _, id := range []string{"nod_pp_a", "nod_pp_b"} {
		if _, err := st.W.ExecContext(context.Background(), `INSERT INTO node (id, name, address, created_at) VALUES (?, ?, '203.0.113.10', 1)`, id, "n-"+id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPathProbesKeepSevenDaysAndPrune(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	probeNodes(t, st)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Hour, 6 * 24 * time.Hour, 8 * 24 * time.Hour} {
		if err := st.AddPathProbe(ctx, "nod_pp_a", "nod_pp_b", "udp", now.Add(-age), `{"ports":[]}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddPathProbe(ctx, "nod_pp_a", "nod_pp_b", "icmp", now, `{}`); err == nil {
		t.Fatal("a probe kind outside udp, tcp and tls was stored")
	}
	n, err := st.PrunePathProbes(ctx, now.Add(-time.Duration(PathProbeKeepHours)*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PrunePathProbes = %d, %v; want the one of 8 days", n, err)
	}
	rows, err := st.PathProbes(ctx, now.Add(-30*24*time.Hour), "", 0)
	if err != nil || len(rows) != 2 || !rows[0].At.After(rows[1].At) {
		t.Fatalf("rows = %+v, %v; want two, newest first", rows, err)
	}
	if got, _ := st.PathProbes(ctx, now.Add(-30*24*time.Hour), "nod_unknown", 0); len(got) != 0 {
		t.Fatalf("a node that is no end of a probe matched: %+v", got)
	}
	// the node going away takes its probes
	if _, err := st.W.ExecContext(ctx, `DELETE FROM node WHERE id = 'nod_pp_b'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PathProbes(ctx, time.Unix(0, 0), "", 0); len(got) != 0 {
		t.Fatalf("probes left after their node was deleted: %+v", got)
	}
}

func TestNodeTracesKeepTwentyPerPair(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	probeNodes(t, st)
	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < NodeTraceKeep+5; i++ {
		if err := st.AddNodeTrace(ctx, "nod_pp_a", "nod_pp_b", base.Add(time.Duration(i)*time.Minute), fmt.Sprintf(`{"n":%d}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddNodeTrace(ctx, "nod_pp_b", "nod_pp_a", base, `{"n":"other pair"}`); err != nil {
		t.Fatal(err)
	}
	rows, err := st.NodeTraces(ctx, "nod_pp_a", "nod_pp_b", 0)
	if err != nil || len(rows) != NodeTraceKeep {
		t.Fatalf("kept %d, %v; want %d", len(rows), err, NodeTraceKeep)
	}
	if rows[0].ResultJSON != `{"n":24}` || rows[len(rows)-1].ResultJSON != `{"n":5}` {
		t.Errorf("kept the wrong traces: newest %s, oldest %s", rows[0].ResultJSON, rows[len(rows)-1].ResultJSON)
	}
	if other, _ := st.NodeTraces(ctx, "nod_pp_b", "nod_pp_a", 0); len(other) != 1 {
		t.Errorf("the reverse pair has %d traces, want its own 1", len(other))
	}
}

func TestNodeDNSRunsKeepTwentyFourOldestFirst(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	probeNodes(t, st)
	base := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for i := 0; i < NodeDNSRunKeep+3; i++ {
		if err := st.AddNodeDNSRun(ctx, NodeDNSRun{NodeID: "nod_pp_a", At: base.Add(time.Duration(i) * 10 * time.Minute), P50MS: uint32(i), P95MS: uint32(i * 2), Total: 3}); err != nil {
			t.Fatal(err)
		}
	}
	// the same second twice is one run
	if err := st.AddNodeDNSRun(ctx, NodeDNSRun{NodeID: "nod_pp_a", At: base, P50MS: 999, Total: 3}); err != nil {
		t.Fatal(err)
	}
	runs, err := st.NodeDNSRuns(ctx, "nod_pp_a")
	if err != nil || len(runs) != NodeDNSRunKeep {
		t.Fatalf("kept %d, %v; want %d", len(runs), err, NodeDNSRunKeep)
	}
	if runs[0].P50MS != 3 || runs[len(runs)-1].P50MS != uint32(NodeDNSRunKeep+2) || !runs[0].At.Before(runs[1].At) {
		t.Errorf("runs = %+v ... want the newest 24, oldest first", runs[:2])
	}
}
