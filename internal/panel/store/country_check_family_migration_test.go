//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestCountryCheckFamiliesSQLite(t *testing.T) {
	countryCheckFamilyProof(t, openTemp(t))
}

// 00084 adds family to country_check and widens the key to (node_id, country, family, at): old rows become IPv4 and
// keep everything else; going back keeps the IPv4 rows. The D1 path applies the same file (TestD1MigrationParity,
// TestD1CountryCheckFamilies).
func TestCountryCheckFamilyMigration(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 83); err != nil { // everything before the family column
		t.Fatalf("up to 83: %v", err)
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	execT(t, s, `INSERT INTO country_check (node_id, at, country, verdict, detail_json) VALUES
		('nod_a', ?, 'RU', 'blocked', '{"k":1}'), ('nod_a', ?, 'RU', 'ok', '{}'), ('nod_b', ?, 'KZ', 'tcp_blocked', '{}')`,
		at.Unix(), at.Add(time.Minute).Unix(), at.Unix())
	if _, err := p.UpTo(ctx, 84); err != nil {
		t.Fatalf("up to 84: %v", err)
	}

	got, err := s.RecentCountryChecks(ctx, 4, 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("old rows read as IPv4 = %+v, %v", got, err)
	}
	if got[0].NodeID != "nod_a" || got[0].Verdict != "ok" || got[1].Verdict != "blocked" || got[1].Detail != `{"k":1}` ||
		got[1].Country != "RU" || got[1].Family != 4 || !got[1].At.Equal(at) || got[2].NodeID != "nod_b" || got[2].Country != "KZ" {
		t.Fatalf("old rows after the rebuild: %+v", got)
	}
	if six, err := s.RecentCountryChecks(ctx, 6, 10); err != nil || len(six) != 0 {
		t.Fatalf("IPv6 rows after the rebuild = %+v, %v", six, err)
	}
	if last, err := s.LastCountryCheckAt(ctx, 4); err != nil || !last.Equal(at.Add(time.Minute)) {
		t.Fatalf("LastCountryCheckAt = %v, %v", last, err)
	}
	// the new key: both families and both countries of one node in one second
	if err := s.InsertCountryChecks(ctx, []CountryCheck{
		{NodeID: "nod_a", At: at, Country: "RU", Family: 6, Verdict: "ok"},
		{NodeID: "nod_a", At: at, Country: "KZ", Family: 4, Verdict: "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM country_check WHERE node_id = 'nod_a' AND at = ?`, at.Unix()).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows of one second = %d, %v; want 3", n, err)
	}
	var idx int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'country_check_at' AND tbl_name = 'country_check'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("the retention index is gone: %d, %v", idx, err)
	}

	if _, err := p.DownTo(ctx, 83); err != nil {
		t.Fatalf("down to 83: %v", err)
	}
	var cols int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('country_check') WHERE name = 'family'`).Scan(&cols); err != nil || cols != 0 {
		t.Fatalf("family column after down: %d, %v", cols, err)
	}
	// down keeps the IPv4 rows; the two (node, second) pairs with two countries keep one row each
	var rows int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM country_check`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("rows after down = %d, %v; want 3", rows, err)
	}
	if _, err := p.UpTo(ctx, 84); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
