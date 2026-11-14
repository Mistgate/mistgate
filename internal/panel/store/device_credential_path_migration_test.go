//go:build !js

package store

import (
	"context"
	"strings"
	"testing"
)

func TestDeviceCredentialPathMigrationUpDown(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 135); err != nil {
		t.Fatalf("up to 135: %v", err)
	}

	rewriteAccessGroup(t, s, "grp_alice", "alice")
	rewriteAccessUser(t, s, "usr_alice", "alice", "grp_alice")
	profile, _ := awgNet(t, s, "de1")
	execT(t, s, `INSERT INTO device (id, user_id, hwid_hash, kind, first_seen_at, last_seen_at, created_at)
		VALUES ('dev_alice', 'usr_alice', x'01', 'key', 1, 1, 1)`)
	execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, secret_enc, data_json, config_epoch, created_at)
		VALUES ('crd_alice', 'dev_alice', 'usr_alice', 'awg', ?, x'01', '{}', 0, 1)`, profile)
	execT(t, s, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at)
		VALUES ('crd_alice', ?, 2, 'pub_alice', 1)`, profile)

	if _, err := p.UpTo(ctx, 136); err != nil {
		t.Fatalf("up to 136: %v", err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice' AND path = '' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("direct credential after upgrade = %d, want 1", got)
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = 'device_credential_path'`) != 1 {
		t.Fatal("path index missing after up")
	}
	insertPath := func(id, path string, idx int) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
			VALUES (?, 'dev_alice', 'usr_alice', 'awg', ?, ?, x'01', '{}', 0, 1)`, id, profile, path)
		if err == nil {
			_, err = s.W.ExecContext(ctx, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES (?, ?, ?, ?, 1)`, id, profile, idx, "pub_"+id)
		}
		return err
	}
	if err := insertPath("crd_alice_msk1", "relay:nod_msk1", 3); err != nil {
		t.Fatalf("direct and path credentials must be live together: %v", err)
	}
	if err := insertPath("crd_alice_nl1", "relay:nod_nl1", 4); err != nil {
		t.Fatalf("different paths must be live together: %v", err)
	}
	if err := insertPath("crd_alice_dup", "relay:nod_msk1", 5); err == nil {
		t.Fatal("duplicate live device/profile/path was accepted")
	}
	if err := insertPath("crd_alice_bad", "entry:msk1", 5); err == nil {
		t.Fatal("a path outside the relay namespace was accepted")
	}

	if _, err := p.DownTo(ctx, 135); err != nil {
		t.Fatalf("down to 135: %v", err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id IN ('crd_alice_msk1', 'crd_alice_nl1')`); got != 0 {
		t.Fatalf("path credentials after down = %d, want 0", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_alice_msk1', 'crd_alice_nl1')`); got != 0 {
		t.Fatalf("path peers after down = %d, want 0", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("direct credential after down = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice' AND released_at = 0`); got != 1 {
		t.Fatalf("direct peer after down = %d, want 1 live", got)
	}
	if countT(t, s, `SELECT count(*) FROM pragma_table_info('device_credential') WHERE name = 'path'`) != 0 {
		t.Fatal("path column remains after down")
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = 'device_credential_path'`) != 0 {
		t.Fatal("path index remains after down")
	}
	var indexSQL string
	if err := s.R.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'device_credential_live'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(indexSQL, "path") || !strings.Contains(indexSQL, "COALESCE(profile_id, '')") {
		t.Fatalf("old live index was not restored: %s", indexSQL)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, secret_enc, data_json, created_at)
		VALUES ('crd_alice_dup_direct', 'dev_alice', 'usr_alice', 'awg', ?, x'01', '{}', 2)`, profile); err == nil {
		t.Fatal("old live index allowed a duplicate direct credential")
	}

	if _, err := p.UpTo(ctx, 136); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice' AND path = '' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("direct credential after re-up = %d, want 1", got)
	}
}
