package store

import (
	"context"
	"testing"
	"time"
)

// countryCheckFamilyProof: rows of one node in the same second but of another family or country are rows of their own
// (not a replacement), and every read is scoped to one family. Runs on SQLite and on D1.
func countryCheckFamilyProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if at, err := st.LastCountryCheckAt(ctx, 6); err != nil || !at.IsZero() {
		t.Fatalf("LastCountryCheckAt(6) on an empty table = %v, %v", at, err)
	}
	if err := st.InsertCountryChecks(ctx, []CountryCheck{
		{NodeID: "nod_de1", At: base, Country: "RU", Verdict: "ok"}, // Family 0 is IPv4
		{NodeID: "nod_de1", At: base, Country: "RU", Family: 6, Verdict: "blocked", Detail: `{"v":6}`},
		{NodeID: "nod_de1", At: base, Country: "KZ", Family: 4, Verdict: "tcp_blocked"},
		{NodeID: "nod_de1", At: base, Country: "KZ", Family: 6, Verdict: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	// the same node, country, family and second again: replaced
	if err := st.InsertCountryChecks(ctx, []CountryCheck{{NodeID: "nod_de1", At: base, Country: "RU", Family: 4, Verdict: "partial"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM country_check`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows = %d, %v; want 4", n, err)
	}
	v4, err := st.RecentCountryChecks(ctx, 4, 10)
	if err != nil || len(v4) != 2 {
		t.Fatalf("RecentCountryChecks(4) = %+v, %v", v4, err)
	}
	for _, r := range v4 {
		if r.Family != 4 {
			t.Fatalf("an IPv4 read returned %+v", r)
		}
	}
	v6, err := st.RecentCountryChecks(ctx, 6, 10, "nod_de1")
	if err != nil || len(v6) != 2 || v6[0].Family != 6 || v6[1].Family != 6 {
		t.Fatalf("RecentCountryChecks(6) = %+v, %v", v6, err)
	}
	if none, err := st.RecentCountryChecks(ctx, 6, 10, "nod_nl1"); err != nil || len(none) != 0 {
		t.Fatalf("RecentCountryChecks(6) of another node = %+v, %v", none, err)
	}

	// the clock of each family is its own
	if err := st.InsertCountryChecks(ctx, []CountryCheck{{NodeID: "nod_de1", At: base.Add(time.Minute), Country: "RU", Family: 6, Verdict: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if at, err := st.LastCountryCheckAt(ctx, 4); err != nil || !at.Equal(base) {
		t.Fatalf("LastCountryCheckAt(4) = %v, %v", at, err)
	}
	if at, err := st.LastCountryCheckAt(ctx, 6); err != nil || !at.Equal(base.Add(time.Minute)) {
		t.Fatalf("LastCountryCheckAt(6) = %v, %v", at, err)
	}

	// the verdict changes do not mix families
	ch4, err := st.CountryVerdictChanges(ctx, "RU", 4, base)
	if err != nil || len(ch4) != 1 || ch4[0].Verdict != "partial" || ch4[0].Family != 4 {
		t.Fatalf("CountryVerdictChanges(RU, 4) = %+v, %v", ch4, err)
	}
	ch6, err := st.CountryVerdictChanges(ctx, "RU", 6, base)
	if err != nil || len(ch6) != 2 || ch6[0].Verdict != "blocked" || ch6[1].Verdict != "ok" || ch6[1].Family != 6 {
		t.Fatalf("CountryVerdictChanges(RU, 6) = %+v, %v", ch6, err)
	}

	// only IPv4 and IPv6 exist
	if err := st.InsertCountryChecks(ctx, []CountryCheck{{NodeID: "nod_de1", At: base, Country: "RU", Family: 5, Verdict: "ok"}}); err == nil {
		t.Fatal("a row of family 5 was stored")
	}

	// the sweep deletes by age, whatever the family
	if n, err := st.PruneCountryChecks(ctx, base.Add(30*time.Second)); err != nil || n != 4 {
		t.Fatalf("PruneCountryChecks = %d, %v; want the 4 rows of the first second", n, err)
	}
}
