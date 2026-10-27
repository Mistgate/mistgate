package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// extCheckStoreProof runs on SQLite and on D1 (see country_ext_check_test.go and d1_store_test.go).
func extCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if at, err := st.LastExtCheckAt(ctx, "cheburcheck", "nod_de1"); err != nil || !at.IsZero() {
		t.Fatalf("LastExtCheckAt on an empty table = %v, %v", at, err)
	}
	rows := []ExtCheck{
		{NodeID: "nod_de1", Source: "cheburcheck", At: base, Verdict: "ok", DetailJSON: `{"vantages":[{"key":"probe:17","network":"Example ISP","region":"Example city","kind":"home","state":"reach"}]}`},
		{NodeID: "nod_de1", Source: "cheburcheck", At: base.Add(time.Minute), Verdict: "tcp_blocked"},
		{NodeID: "nod_de1", Source: "cheburcheck", At: base.Add(2 * time.Minute), Verdict: "tcp_blocked"},
		{NodeID: "nod_de1", Source: "cheburcheck", At: base.Add(3 * time.Minute), Verdict: "ok"},
		{NodeID: "nod_de1", Source: "bsbord", At: base, Verdict: "blocked"},
		{NodeID: "nod_de1", Source: "bsbord", At: base.Add(time.Minute), Verdict: "blocked"},
		{NodeID: "nod_nl1", Source: "cheburcheck", At: base, Verdict: "blocked"},
		{NodeID: "nod_nl1", Source: "cheburcheck", At: base.Add(time.Minute), Verdict: "blocked"},
		{NodeID: "nod_fi1", Source: "bsbord", At: base, Verdict: "unknown"},
	}
	if err := st.InsertExtChecks(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// The same node, source and second replaces the old row.
	if err := st.InsertExtChecks(ctx, []ExtCheck{{NodeID: "nod_de1", Source: "cheburcheck", At: base, Verdict: "partial", DetailJSON: `{"reason":"replacement"}`}}); err != nil {
		t.Fatal(err)
	}

	got, err := st.RecentExtChecks(ctx, 2)
	if err != nil || len(got) != 7 {
		t.Fatalf("RecentExtChecks(2) = %+v, %v", got, err)
	}
	if got[0].NodeID != "nod_de1" || got[0].Source != "bsbord" || !got[0].At.Equal(base.Add(time.Minute)) ||
		got[2].NodeID != "nod_de1" || got[2].Source != "cheburcheck" || got[2].Verdict != "ok" ||
		got[3].Verdict != "tcp_blocked" || got[4].NodeID != "nod_fi1" || got[6].DetailJSON != "{}" {
		t.Fatalf("newest rows by node and source: %+v", got)
	}
	if got[0].DetailJSON != "{}" {
		t.Fatalf("empty detail = %q, want {}", got[0].DetailJSON)
	}
	bySource, err := st.RecentExtChecks(ctx, 2, "cheburcheck")
	if err != nil || len(bySource) != 4 {
		t.Fatalf("RecentExtChecks for cheburcheck = %+v, %v", bySource, err)
	}
	for _, c := range bySource {
		if c.Source != "cheburcheck" {
			t.Fatalf("source filter returned %+v", c)
		}
	}
	newest, err := st.RecentExtChecks(ctx, 1, "bsbord")
	if err != nil || len(newest) != 2 || newest[0].NodeID != "nod_de1" || !newest[0].At.Equal(base.Add(time.Minute)) ||
		newest[1].NodeID != "nod_fi1" {
		t.Fatalf("RecentExtChecks(1, bsbord) = %+v, %v", newest, err)
	}
	if at, err := st.LastExtCheckAt(ctx, "cheburcheck", "nod_de1"); err != nil || !at.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("LastExtCheckAt = %v, %v", at, err)
	}
	if at, err := st.LastExtCheckAt(ctx, "bsbord", "nod_missing"); err != nil || !at.IsZero() {
		t.Fatalf("LastExtCheckAt for a missing node = %v, %v", at, err)
	}

	changes, err := st.ExtVerdictChanges(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	var gotChanges []string
	for _, c := range changes {
		gotChanges = append(gotChanges, fmt.Sprintf("%s/%s %d %s", c.NodeID, c.Source, int(c.At.Sub(base)/time.Minute), c.Verdict))
	}
	wantChanges := []string{
		"nod_de1/bsbord 0 blocked",
		"nod_de1/cheburcheck 0 partial", "nod_de1/cheburcheck 1 tcp_blocked", "nod_de1/cheburcheck 3 ok",
		"nod_fi1/bsbord 0 unknown",
		"nod_nl1/cheburcheck 0 blocked",
	}
	if !slices.Equal(gotChanges, wantChanges) {
		t.Fatalf("ExtVerdictChanges = %q, want %q", gotChanges, wantChanges)
	}

	if err := st.DeleteExtChecks(ctx, "cheburcheck"); err != nil {
		t.Fatal(err)
	}
	if left, err := st.RecentExtChecks(ctx, 10, "cheburcheck"); err != nil || len(left) != 0 {
		t.Fatalf("after source delete: %+v, %v", left, err)
	}
	left, err := st.RecentExtChecks(ctx, 10)
	if err != nil || len(left) != 3 {
		t.Fatalf("other sources after delete: %+v, %v", left, err)
	}
	n, err := st.PruneExtChecks(ctx, base.Add(time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("PruneExtChecks = %d, %v, want 2 rows older than the cut", n, err)
	}
	left, err = st.RecentExtChecks(ctx, 10)
	if err != nil || len(left) != 1 || left[0].NodeID != "nod_de1" || !left[0].At.Equal(base.Add(time.Minute)) {
		t.Fatalf("after prune: %+v, %v", left, err)
	}
	if err := st.DeleteExtChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if left, err := st.RecentExtChecks(ctx, 10); err != nil || len(left) != 0 {
		t.Fatalf("after deleting all sources: %+v, %v", left, err)
	}
}
