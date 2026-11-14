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

func TestDeviceCredentialIPv6PathMigrationPreservesRelayKeys(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 136); err != nil {
		t.Fatalf("up to 136: %v", err)
	}
	rewriteAccessGroup(t, s, "grp_alice", "alice")
	rewriteAccessUser(t, s, "usr_alice", "alice", "grp_alice")
	profile, _ := awgNet(t, s, "de1")
	execT(t, s, `INSERT INTO device (id, user_id, hwid_hash, kind, first_seen_at, last_seen_at, created_at)
		VALUES ('dev_alice', 'usr_alice', x'01', 'key', 1, 1, 1)`)
	for _, c := range []struct {
		id   string
		idx  int
		path string
	}{{"crd_alice", 2, ""}, {"crd_alice_relay", 3, "relay:nod_nl1"}} {
		execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
			VALUES (?, 'dev_alice', 'usr_alice', 'awg', ?, ?, x'01', '{}', 0, 1)`, c.id, profile, c.path)
		execT(t, s, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at)
			VALUES (?, ?, ?, ?, 1)`, c.id, profile, c.idx, "pub_"+c.id)
	}

	if _, err := p.UpTo(ctx, 138); err != nil {
		t.Fatalf("up to 138: %v", err)
	}
	indexesIntact := func(when string) {
		t.Helper()
		var live, path string
		if err := s.R.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'device_credential_live'`).Scan(&live); err != nil {
			t.Fatalf("live index %s: %v", when, err)
		}
		if err := s.R.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'device_credential_path'`).Scan(&path); err != nil {
			t.Fatalf("path index %s: %v", when, err)
		}
		if !strings.Contains(live, "COALESCE(profile_id, ''), path)") || !strings.Contains(live, "UNIQUE") || !strings.Contains(path, "path <> ''") {
			t.Fatalf("indexes %s lost their definition: %s / %s", when, live, path)
		}
	}
	indexesIntact("after up")
	insertIPv6 := func(id string) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
			VALUES (?, 'dev_alice', 'usr_alice', 'awg', ?, 'ipv6', x'01', '{}', 0, 1)`, id, profile)
		return err
	}
	if err := insertIPv6("crd_alice_ipv6"); err != nil {
		t.Fatalf("ipv6 path was rejected: %v", err)
	}
	if err := insertIPv6("crd_alice_ipv6_duplicate"); err == nil {
		t.Fatal("duplicate live device/protocol/profile/ipv6 path was accepted")
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id IN ('crd_alice', 'crd_alice_relay') AND revoked_at IS NULL`); got != 2 {
		t.Fatalf("direct and relay credentials after up = %d, want 2", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_alice', 'crd_alice_relay')`); got != 2 {
		t.Fatalf("direct and relay peers after up = %d, want 2", got)
	}
	if rows, err := s.R.QueryContext(ctx, `PRAGMA foreign_key_check`); err != nil {
		t.Fatalf("foreign key check after up: %v", err)
	} else {
		defer rows.Close()
		if rows.Next() {
			t.Fatal("foreign key violation after up")
		}
	}

	if _, err := p.DownTo(ctx, 136); err != nil {
		t.Fatalf("down to 136: %v", err)
	}
	indexesIntact("after down")
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_ipv6'`); got != 0 {
		t.Fatalf("ipv6 credential after down = %d, want 0", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice_ipv6'`); got != 0 {
		t.Fatalf("ipv6 peer after down = %d, want 0", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id IN ('crd_alice', 'crd_alice_relay') AND revoked_at IS NULL`); got != 2 {
		t.Fatalf("direct and relay credentials after down = %d, want 2", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_alice', 'crd_alice_relay')`); got != 2 {
		t.Fatalf("direct and relay peers after down = %d, want 2", got)
	}
	if err := insertIPv6("crd_alice_ipv6_down"); err == nil {
		t.Fatal("the old path CHECK accepted ipv6 after down")
	}
	if rows, err := s.R.QueryContext(ctx, `PRAGMA foreign_key_check`); err != nil {
		t.Fatalf("foreign key check after down: %v", err)
	} else {
		defer rows.Close()
		if rows.Next() {
			t.Fatal("foreign key violation after down")
		}
	}
}
