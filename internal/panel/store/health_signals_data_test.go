package store

import (
	"context"
	"testing"
	"time"
)

func seedHealthSignals(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	stamp := now.Unix()
	for _, q := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO user_group (id, name, created_at) VALUES ('grp_signals', 'signals', ?)`, []any{stamp}},
		{`INSERT INTO user (id, name, group_id, status, period_start, sub_token_hash, sub_token_enc, created_at, last_seen_at, expires_at)
			VALUES ('usr_expired', 'Expired', 'grp_signals', 'expired', ?, X'01', X'01', ?, ?, ?)`, []any{stamp, stamp, now.Add(-4 * 24 * time.Hour).Unix(), now.Add(-3 * 24 * time.Hour).Unix()}},
		{`INSERT INTO user (id, name, group_id, status, period_start, sub_token_hash, sub_token_enc, created_at, last_seen_at)
			VALUES ('usr_limited', 'Limited', 'grp_signals', 'limited', ?, X'02', X'02', ?, ?)`, []any{stamp, stamp, now.Add(-24 * time.Hour).Unix()}},
		{`INSERT INTO user (id, name, group_id, status, period_start, sub_token_hash, sub_token_enc, created_at, last_seen_at)
			VALUES ('usr_active', 'Active', 'grp_signals', 'active', ?, X'03', X'03', ?, ?)`, []any{stamp, stamp, now.Add(-2 * 24 * time.Hour).Unix()}},
		{`INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at) VALUES
			('dev_expired_implicit', 'usr_expired', NULL, ?, ?, ?),
			('dev_limited_implicit', 'usr_limited', NULL, ?, ?, ?),
			('dev_active_implicit', 'usr_active', NULL, ?, ?, ?),
			('dev_active_awg', 'usr_active', X'01', 0, ?, ?)`, []any{
			stamp, now.Add(-time.Hour).Unix(), stamp,
			stamp, now.Add(-10 * time.Minute).Unix(), stamp,
			stamp, now.Add(-12 * time.Hour).Unix(), stamp,
			now.Add(-2 * time.Hour).Unix(), now.Add(-3 * 24 * time.Hour).Unix(),
		}},
		{`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_signals', 'awg', 'signals', '{}', ?, ?)`, []any{stamp, stamp}},
		{`UPDATE profile SET critical_epoch = 2 WHERE id = 'prf_signals'`, nil},
		{`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
			VALUES ('crd_signals', 'dev_active_awg', 'usr_active', 'awg', X'01', '{}', ?, 'prf_signals', 1)`, []any{now.Add(-3 * 24 * time.Hour).Unix()}},
		{`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
			VALUES ('crd_signals_implicit', 'dev_active_implicit', 'usr_active', 'awg', X'02', '{}', ?, 'prf_signals', 1)`, []any{now.Add(-3 * 24 * time.Hour).Unix()}},
		{`INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at)
			VALUES ('crd_signals', 'prf_signals', 2, 'pub_signals', ?)`, []any{stamp}},
		{`INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at)
			VALUES ('crd_signals_implicit', 'prf_signals', 3, 'pub_signals_implicit', ?)`, []any{stamp}},
		{`INSERT INTO node (id, name, address, created_at) VALUES ('nod_signals', 'signals', '203.0.113.10', ?)`, []any{stamp}},
	} {
		if _, err := s.W.ExecContext(ctx, q.query, q.args...); err != nil {
			t.Fatalf("seed health signals: %v", err)
		}
	}
	currentHour := now.UTC().Truncate(time.Hour).Unix()
	for days := 0; days <= 7; days++ {
		peak := 5
		if days == 0 {
			peak = 1
		}
		if _, err := s.W.ExecContext(ctx,
			`INSERT INTO node_traffic_hour (node_id, protocol, hour_start, peak_users) VALUES ('nod_signals', 'hysteria2', ?, ?)`,
			currentHour-int64(days)*int64((24*time.Hour)/time.Second), peak); err != nil {
			t.Fatalf("seed node traffic hour: %v", err)
		}
	}
}
