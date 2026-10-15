package store

import (
	"context"
	"testing"
	"time"
)

// blocklistCheckStoreProof runs on SQLite and on D1 (see country_blocklist_check_test.go and d1_store_test.go).
func blocklistCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if got, err := st.RecentBlocklistChecks(ctx); err != nil || len(got) != 0 {
		t.Fatalf("RecentBlocklistChecks on an empty table = %+v, %v", got, err)
	}
	rows := []BlocklistCheck{
		{NodeID: "nod_de1", At: base, ListsAt: base.Add(-time.Hour)},
		{NodeID: "nod_de1", At: base.Add(time.Minute), InRKN: true, RKNSubnet: "203.0.113.0/24", ListedBy: "antifilter_ipsum", CDNProvider: "ExampleHost",
			CDNRegion: "eu", ListsAt: base},
		{NodeID: "nod_nl1", At: base, CDNProvider: "ExampleCloud"},
	}
	if err := st.InsertBlocklistChecks(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// the same node and second again: replaced, not a primary key error
	if err := st.InsertBlocklistChecks(ctx, []BlocklistCheck{{NodeID: "nod_nl1", At: base, InRKN: true, ListedBy: "antifilter_community"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.RecentBlocklistChecks(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("RecentBlocklistChecks = %+v, %v", got, err)
	}
	de, nl := got[0], got[1]
	if de.NodeID != "nod_de1" || !de.At.Equal(base.Add(time.Minute)) || !de.InRKN || de.RKNSubnet != "203.0.113.0/24" || de.ListedBy != "antifilter_ipsum" ||
		de.CDNProvider != "ExampleHost" || de.CDNRegion != "eu" || !de.ListsAt.Equal(base) {
		t.Fatalf("newest row of de1: %+v", de)
	}
	if nl.NodeID != "nod_nl1" || !nl.InRKN || nl.ListedBy != "antifilter_community" || nl.CDNProvider != "" || !nl.ListsAt.IsZero() {
		t.Fatalf("row of nl1 was not replaced: %+v", nl)
	}
	one, err := st.RecentBlocklistChecks(ctx, "nod_nl1")
	if err != nil || len(one) != 1 || one[0].NodeID != "nod_nl1" {
		t.Fatalf("RecentBlocklistChecks for one node = %+v, %v", one, err)
	}
	// the history: every check since the cut, oldest first inside a node, with whether a list held the address
	hist, err := st.BlocklistHistory(ctx, base)
	if err != nil || len(hist) != 3 || hist[0].NodeID != "nod_de1" || hist[0].InRKN || !hist[0].At.Equal(base) ||
		!hist[1].InRKN || hist[1].ListedBy != "antifilter_ipsum" || !hist[1].At.Equal(base.Add(time.Minute)) ||
		hist[2].NodeID != "nod_nl1" || !hist[2].InRKN || hist[2].ListedBy != "antifilter_community" {
		t.Fatalf("BlocklistHistory(base) = %+v, %v", hist, err)
	}
	if hist, err := st.BlocklistHistory(ctx, base.Add(30*time.Second)); err != nil || len(hist) != 1 || hist[0].NodeID != "nod_de1" {
		t.Fatalf("BlocklistHistory after the first check = %+v, %v", hist, err)
	}
	n, err := st.PruneBlocklistChecks(ctx, base.Add(30*time.Second))
	if err != nil || n != 2 {
		t.Fatalf("PruneBlocklistChecks = %d, %v, want the 2 rows older than the cut", n, err)
	}
	if left, err := st.RecentBlocklistChecks(ctx); err != nil || len(left) != 1 || left[0].NodeID != "nod_de1" {
		t.Fatalf("after the prune: %+v, %v", left, err)
	}
	if err := st.DeleteBlocklistChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if left, err := st.RecentBlocklistChecks(ctx); err != nil || len(left) != 0 {
		t.Fatalf("after the delete: %+v, %v", left, err)
	}
}
