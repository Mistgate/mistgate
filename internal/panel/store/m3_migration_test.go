package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func openProvider(t *testing.T) (*Store, *goose.Provider) {
	t.Helper()
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
	return s, p
}

func countT(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.W.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The AWG and WARP migrations on an empty database: up, down to 13 and up again (down must leave exactly the
// schema before them).
func TestM3MigrationsEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, table := range []string{"awg_peer", "warp_account"} {
		if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table) != 1 {
			t.Fatalf("%s missing after up", table)
		}
	}
	if _, err := p.DownTo(ctx, 13); err != nil {
		t.Fatalf("down to 13: %v", err)
	}
	for _, table := range []string{"awg_peer", "warp_account"} {
		if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table) != 0 {
			t.Fatalf("%s left after down", table)
		}
	}
	// 00002 shape is back: no profile_id / config_epoch on device_credential.
	if countT(t, s, `SELECT count(*) FROM pragma_table_info('device_credential') WHERE name IN ('profile_id', 'config_epoch')`) != 0 {
		t.Fatal("device_credential still has the AWG columns")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// A live instance at version 13 with users, devices and Hysteria2 credentials: 00014 and 00015 must apply on top
// of that data without touching it, AWG rows must obey their constraints, and rolling back must drop only the AWG
// part and leave the Hysteria2 world intact.
func TestM3MigrationsUpgradeExistingData(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 13); err != nil {
		t.Fatalf("up to 13: %v", err)
	}
	nodeID, _ := fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	execT(t, s, `INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at) VALUES ('dev_hy', 'usr_a', 1, 1, 1)`)
	execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES ('crd_hy', 'dev_hy', 'usr_a', 'hysteria2', x'01', '{}', 1)`)

	if _, err := p.UpTo(ctx, 15); err != nil {
		t.Fatalf("up to 15: %v", err)
	}
	// old rows untouched, with the defaults of the new columns
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_hy' AND profile_id IS NULL AND config_epoch = 0`); n != 1 {
		t.Fatalf("hysteria2 credential after upgrade: %d", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM profile WHERE critical_epoch = 0`) + countT(t, s, `SELECT count(*) FROM node WHERE awg_backend = 'auto'`) +
		countT(t, s, `SELECT count(*) FROM inbound WHERE plugin_public_json = '{}' AND plugin_state_enc IS NULL AND awg_health_json = ''`); n != 3 {
		t.Fatalf("defaults of the new columns: %d", n)
	}
	// hysteria2 uniqueness is what it was: a second live hysteria2 credential of the device is refused
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES ('crd_hy2', 'dev_hy', 'usr_a', 'hysteria2', x'01', '{}', 1)`); err == nil {
		t.Fatal("two live hysteria2 credentials of one device")
	}

	// AWG: one live credential per (device, profile); the same device may also hold hysteria2 and another profile
	execT(t, s, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_awg', 'awg', 'awg31', '{}', 1, 1)`)
	execT(t, s, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_awg2', 'awg', 'awg20', '{}', 1, 1)`)
	execT(t, s, `INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at) VALUES ('dev_awg', 'usr_a', x'aa', 1, 1, 1)`)
	ins := func(id, dev, prf string) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, secret_enc, data_json, created_at) VALUES (?, ?, 'usr_a', 'awg', ?, x'01', '{}', 1)`, id, dev, prf)
		return err
	}
	if err := ins("crd_awg", "dev_awg", "prf_awg"); err != nil {
		t.Fatal(err)
	}
	if err := ins("crd_awg_dup", "dev_awg", "prf_awg"); err == nil {
		t.Fatal("two live credentials of one (device, profile)")
	}
	if err := ins("crd_awg2", "dev_awg", "prf_awg2"); err != nil {
		t.Fatalf("the same device on a second profile: %v", err)
	}
	execT(t, s, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES ('crd_awg', 'prf_awg', 2, 'pub1', 1)`)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES ('crd_awg2', 'prf_awg', 2, 'pub2', 1)`); err == nil {
		t.Fatal("two live peers with one idx in a profile")
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES ('crd_awg2', 'prf_awg2', 2, 'pub1', 1)`); err == nil {
		t.Fatal("two live peers with one public key")
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES ('crd_awg2', 'prf_awg2', 1, 'pub3', 1)`); err == nil {
		t.Fatal("idx below 2 accepted")
	}
	// a released row (quarantine) does not block the same idx and key being used again (rotation)
	execT(t, s, `UPDATE awg_peer SET released_at = 5 WHERE credential_id = 'crd_awg'`)
	execT(t, s, `UPDATE device_credential SET revoked_at = 5 WHERE id = 'crd_awg'`)
	if err := ins("crd_awg_new", "dev_awg", "prf_awg"); err != nil {
		t.Fatalf("new credential after the old one was revoked: %v", err)
	}
	execT(t, s, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES ('crd_awg_new', 'prf_awg', 2, 'pub1', 6)`)

	// the WARP account is per node
	execT(t, s, `INSERT INTO warp_account (node_id, source, secret_enc, peer_public_key, endpoint_v4, address_v4, created_at, updated_at) VALUES (?, 'imported', x'01', 'k', '203.0.113.10', '172.16.0.2/32', 1, 1)`, nodeID)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO warp_account (node_id, source, secret_enc, peer_public_key, endpoint_v4, address_v4, created_at, updated_at) VALUES (?, 'registered', x'01', 'k', '203.0.113.10', '172.16.0.2/32', 1, 1)`, nodeID); err == nil {
		t.Fatal("two WARP accounts on one node")
	}

	// deleting a profile removes its credentials and peers
	execT(t, s, `DELETE FROM profile WHERE id = 'prf_awg2'`)
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_awg2'`); n != 0 {
		t.Fatal("credential survived its profile")
	}

	// back to 13: the AWG device and credentials go, the Hysteria2 device and credential stay
	if _, err := p.DownTo(ctx, 13); err != nil {
		t.Fatalf("down to 13: %v", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_awg'`); n != 0 {
		t.Fatal("AWG-only device survived the rollback")
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_hy' AND protocol = 'hysteria2'`) + countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_hy'`) +
		countT(t, s, `SELECT count(*) FROM node WHERE id = ?`, nodeID); n != 3 {
		t.Fatalf("hysteria2 world after rollback: %d", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential`); n != 1 {
		t.Fatalf("credentials after rollback: %d", n)
	}
	// the old uniqueness is back and the rebuilt table still enforces it
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES ('crd_x', 'dev_hy', 'usr_a', 'hysteria2', x'01', '{}', 1)`); err == nil {
		t.Fatal("rebuilt table lost its unique index")
	}
	if _, err := p.UpTo(ctx, 15); err != nil { // and forward again
		t.Fatalf("up again: %v", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_hy'`); n != 1 {
		t.Fatal("credential lost over down and up")
	}
}
