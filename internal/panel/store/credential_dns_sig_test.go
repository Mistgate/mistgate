package store

import (
	"context"
	"testing"
)

// 00047 on a live instance: the existing credentials get an empty signature (nothing recorded: not called stale), the
// time of the last fetch of 00044 is gone, and rolling back leaves the schema as it was.
func TestCredentialDNSSigMigrationUpgradesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 46); err != nil {
		t.Fatalf("up to 46: %v", err)
	}
	fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	execT(t, s, `INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at) VALUES ('dev_hy', 'usr_a', 1, 1, 1)`)
	execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES ('crd_hy', 'dev_hy', 'usr_a', 'hysteria2', x'01', '{}', 1)`)

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up through 47: %v", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_hy' AND dns_sig = ''`); n != 1 {
		t.Fatalf("the existing credential: %d rows with an empty signature", n)
	}
	if countT(t, s, `SELECT count(*) FROM pragma_table_info('device_credential') WHERE name = 'configs_at'`) != 0 {
		t.Error("configs_at is left")
	}
	if err := s.Access().SetDNSSig(ctx, "crd_hy", map[string]string{"nod_a": "1.1.1.1,8.8.8.8"}); err != nil {
		t.Fatal(err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_hy' AND dns_sig = '{"nod_a":"1.1.1.1,8.8.8.8"}'`); n != 1 {
		t.Errorf("the recorded signature: %d rows", n)
	}

	if _, err := p.DownTo(ctx, 46); err != nil {
		t.Fatalf("down to 46: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM pragma_table_info('device_credential') WHERE name = 'dns_sig'`) != 0 ||
		countT(t, s, `SELECT count(*) FROM pragma_table_info('device_credential') WHERE name = 'configs_at'`) != 1 {
		t.Fatal("the schema of 00047 is left after the rollback, or configs_at did not come back")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
