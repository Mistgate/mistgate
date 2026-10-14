package store

import (
	"context"
	"testing"
	"time"
)

// countryCheckStoreProof runs on SQLite and on D1 (see country_check_test.go and d1_store_test.go).
func countryCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if at, err := st.LastCountryCheckAt(ctx); err != nil || !at.IsZero() {
		t.Fatalf("LastCountryCheckAt on an empty table = %v, %v", at, err)
	}
	var rows []CountryCheck
	for i := range 4 {
		rows = append(rows,
			CountryCheck{NodeID: "nod_de1", At: base.Add(time.Duration(i) * time.Minute), Country: "RU", Verdict: "ok", Detail: `{"i":` + string(rune('0'+i)) + `}`},
			CountryCheck{NodeID: "nod_nl1", At: base.Add(time.Duration(i) * time.Minute), Country: "RU", Verdict: "blocked"})
	}
	if err := st.InsertCountryChecks(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// the same node and second again: replaced, not a primary key error
	if err := st.InsertCountryChecks(ctx, []CountryCheck{{NodeID: "nod_nl1", At: base, Country: "RU", Verdict: "unknown"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.RecentCountryChecks(ctx, 2)
	if err != nil || len(got) != 4 {
		t.Fatalf("RecentCountryChecks(2) = %+v, %v", got, err)
	}
	if got[0].NodeID != "nod_de1" || !got[0].At.Equal(base.Add(3*time.Minute)) || got[1].At.Before(got[0].At.Add(-2*time.Minute)) || got[0].Detail != `{"i":3}` {
		t.Fatalf("newest first inside a node: %+v", got)
	}
	if got[2].NodeID != "nod_nl1" || got[2].Verdict != "blocked" || got[3].Detail != "{}" {
		t.Fatalf("second node: %+v", got)
	}
	one, err := st.RecentCountryChecks(ctx, 10, "nod_nl1")
	if err != nil || len(one) != 4 || one[3].Verdict != "unknown" {
		t.Fatalf("RecentCountryChecks for one node = %+v, %v", one, err)
	}
	if at, err := st.LastCountryCheckAt(ctx); err != nil || !at.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("LastCountryCheckAt = %v, %v", at, err)
	}
	n, err := st.PruneCountryChecks(ctx, base.Add(2*time.Minute))
	if err != nil || n != 4 {
		t.Fatalf("PruneCountryChecks = %d, %v, want 4 rows older than the cut", n, err)
	}
	left, err := st.RecentCountryChecks(ctx, 10)
	if err != nil || len(left) != 4 {
		t.Fatalf("after the prune: %+v, %v", left, err)
	}
}
