//go:build !js

package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// A live instance is at version 11 with nodes, inbounds, users and events: 00012 must apply on top of that
// data without touching it, and roll back again. The database is opened without the automatic migration and
// stops at 00011 (so later migrations of other modules play no part in this test).
func TestHealthMigrationUpgradesExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "live.db")
	w, err := openDB(path, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := openDB(path, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{W: w, R: r}
	t.Cleanup(func() { s.Close() })
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 11); err != nil {
		t.Fatalf("up to 11: %v", err)
	}
	for _, table := range []string{"health_alert", "health_probe_cred", "doctor_result", "health_check_sample", "health_check_daily"} {
		var n int
		if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s before the migration: n=%d err=%v", table, n, err)
		}
	}
	nodeID, inboundID := fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	execT(t, s, `INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (1000, 1, 'node_blip', 'panel', ?, '{}')`, nodeID)

	if _, err := p.UpTo(ctx, 12); err != nil {
		t.Fatalf("up to 12: %v", err)
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM node) + (SELECT count(*) FROM inbound) + (SELECT count(*) FROM user) + (SELECT count(*) FROM event)`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("existing rows after upgrade: %d %v", n, err)
	}
	// the new tables work against the old rows
	if err := s.InsertProbeCred(ctx, ProbeCredRow{InboundID: inboundID, CredID: "crd_p", SecretEnc: []byte{1}, DataJSON: `{}`}, time.Unix(5, 0)); err != nil {
		t.Fatal(err)
	}
	// raw SQL, not PutDoctor: that one writes the 00017 column, and this test stops at 00012
	execT(t, s, `INSERT INTO doctor_result (node_id, check_id, status, received_unix) VALUES (?, 'disk_space', 2, 5)`, nodeID)
	if _, err := p.DownTo(ctx, 11); err != nil {
		t.Fatalf("down again: %v", err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM node) + (SELECT count(*) FROM inbound) + (SELECT count(*) FROM event)`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows after rolling back: %d %v", n, err)
	}
	if _, err := p.UpTo(ctx, 12); err != nil { // and forward again
		t.Fatalf("up again: %v", err)
	}
}
