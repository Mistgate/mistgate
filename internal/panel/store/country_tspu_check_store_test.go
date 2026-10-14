package store

import (
	"context"
	"testing"
	"time"
)

// tspuCheckStoreProof runs on SQLite and on D1 (see country_tspu_check_test.go and d1_store_test.go).
func tspuCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if got, err := st.RecentTSPUChecks(ctx); err != nil || len(got) != 0 {
		t.Fatalf("RecentTSPUChecks on an empty table = %+v, %v", got, err)
	}
	rows := []TSPUCheck{
		{NodeID: "nod_de1", Vantage: "nod_probe1", At: base, Verdict: "open", DPIHop: 4, ControlOK: true, DetailJSON: `[{"port":443,"verdict":"open"}]`},
		{NodeID: "nod_de1", Vantage: "nod_probe1", At: base.Add(time.Minute), Verdict: "blocked", DPIHop: 4, ControlOK: true,
			DetailJSON: `[{"port":443,"verdict":"blocked"},{"port":8443,"verdict":"blocked"}]`},
		{NodeID: "nod_de1", Vantage: "nod_probe2", At: base, Verdict: "partial", DPIHop: 2, ControlOK: true},
		{NodeID: "nod_nl1", Vantage: "nod_probe1", At: base, Verdict: "unknown"},
	}
	if err := st.InsertTSPUChecks(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// the same node, vantage and second again: replaced, not a primary key error
	if err := st.InsertTSPUChecks(ctx, []TSPUCheck{{NodeID: "nod_nl1", Vantage: "nod_probe1", At: base, Verdict: "open", DPIHop: 3, ControlOK: true}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.RecentTSPUChecks(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("RecentTSPUChecks = %+v, %v", got, err)
	}
	de1p1, de1p2, nl1 := got[0], got[1], got[2]
	if de1p1.NodeID != "nod_de1" || de1p1.Vantage != "nod_probe1" || !de1p1.At.Equal(base.Add(time.Minute)) || de1p1.Verdict != "blocked" ||
		de1p1.DPIHop != 4 || !de1p1.ControlOK || de1p1.DetailJSON != `[{"port":443,"verdict":"blocked"},{"port":8443,"verdict":"blocked"}]` {
		t.Fatalf("newest row of de1 from probe1: %+v", de1p1)
	}
	if de1p2.Vantage != "nod_probe2" || de1p2.Verdict != "partial" || de1p2.DPIHop != 2 || de1p2.DetailJSON != "[]" {
		t.Fatalf("row of de1 from probe2: %+v", de1p2)
	}
	if nl1.NodeID != "nod_nl1" || nl1.Verdict != "open" || nl1.DPIHop != 3 || !nl1.ControlOK {
		t.Fatalf("row of nl1 was not replaced: %+v", nl1)
	}
	one, err := st.RecentTSPUChecks(ctx, "nod_nl1")
	if err != nil || len(one) != 1 || one[0].NodeID != "nod_nl1" {
		t.Fatalf("RecentTSPUChecks for one node = %+v, %v", one, err)
	}
	n, err := st.PruneTSPUChecks(ctx, base.Add(30*time.Second))
	if err != nil || n != 3 {
		t.Fatalf("PruneTSPUChecks = %d, %v, want the 3 rows older than the cut", n, err)
	}
	if left, err := st.RecentTSPUChecks(ctx); err != nil || len(left) != 1 || left[0].Verdict != "blocked" {
		t.Fatalf("after the prune: %+v, %v", left, err)
	}
}
