package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// countryCheckStoreProof runs on SQLite and on D1 (see country_check_test.go and d1_store_test.go).
func countryCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if at, err := st.LastCountryCheckAt(ctx, 4); err != nil || !at.IsZero() {
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
	got, err := st.RecentCountryChecks(ctx, 4, 2)
	if err != nil || len(got) != 4 {
		t.Fatalf("RecentCountryChecks(2) = %+v, %v", got, err)
	}
	if got[0].NodeID != "nod_de1" || !got[0].At.Equal(base.Add(3*time.Minute)) || got[1].At.Before(got[0].At.Add(-2*time.Minute)) || got[0].Detail != `{"i":3}` {
		t.Fatalf("newest first inside a node: %+v", got)
	}
	if got[2].NodeID != "nod_nl1" || got[2].Verdict != "blocked" || got[3].Detail != "{}" {
		t.Fatalf("second node: %+v", got)
	}
	one, err := st.RecentCountryChecks(ctx, 4, 10, "nod_nl1")
	if err != nil || len(one) != 4 || one[3].Verdict != "unknown" {
		t.Fatalf("RecentCountryChecks for one node = %+v, %v", one, err)
	}
	if at, err := st.LastCountryCheckAt(ctx, 4); err != nil || !at.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("LastCountryCheckAt = %v, %v", at, err)
	}
	n, err := st.PruneCountryChecks(ctx, base.Add(2*time.Minute))
	if err != nil || n != 4 {
		t.Fatalf("PruneCountryChecks = %d, %v, want 4 rows older than the cut", n, err)
	}
	left, err := st.RecentCountryChecks(ctx, 4, 10)
	if err != nil || len(left) != 4 {
		t.Fatalf("after the prune: %+v, %v", left, err)
	}

	// the verdict changes of one country: the first round of a node in the window and each round that differs from the one before
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	if err := st.InsertCountryChecks(ctx, []CountryCheck{
		{NodeID: "nod_fi1", At: at(-10), Country: "RU", Verdict: "blocked"}, // before the window
		{NodeID: "nod_fi1", At: at(0), Country: "RU", Verdict: "ok"},
		{NodeID: "nod_fi1", At: at(1), Country: "RU", Verdict: "ok"},
		{NodeID: "nod_fi1", At: at(2), Country: "RU", Verdict: "tcp_blocked"},
		{NodeID: "nod_fi1", At: at(3), Country: "RU", Verdict: "tcp_blocked"},
		{NodeID: "nod_fi1", At: at(4), Country: "KZ", Verdict: "blocked"}, // another country: not in the lane, not a break
		{NodeID: "nod_fi1", At: at(5), Country: "RU", Verdict: "tcp_blocked"},
		{NodeID: "nod_fi1", At: at(6), Country: "RU", Verdict: "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	changes, err := st.CountryVerdictChanges(ctx, "RU", 4, base)
	if err != nil {
		t.Fatal(err)
	}
	var got2 []string
	for _, c := range changes {
		got2 = append(got2, fmt.Sprintf("%s %d %s %s", c.NodeID, int(c.At.Sub(base)/time.Minute), c.Verdict, c.Country))
	}
	want := []string{"nod_de1 2 ok RU", "nod_fi1 0 ok RU", "nod_fi1 2 tcp_blocked RU", "nod_fi1 6 ok RU", "nod_nl1 2 blocked RU"}
	if !slices.Equal(got2, want) {
		t.Fatalf("CountryVerdictChanges(RU) = %q, want %q", got2, want)
	}
	if kz, err := st.CountryVerdictChanges(ctx, "KZ", 4, base); err != nil || len(kz) != 1 || kz[0].Verdict != "blocked" {
		t.Fatalf("CountryVerdictChanges(KZ) = %+v, %v", kz, err)
	}
}
