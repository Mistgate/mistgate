//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestIdleDeviceCountsUseLastSeenOrConnectionThenCreationAndCanBeDisabled(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for n := 1; n <= 4; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 0); err != nil {
			t.Fatal(err)
		}
	}
	deviceID := func(n int) string {
		t.Helper()
		var id string
		if err := s.R.QueryRowContext(ctx, `SELECT id FROM device WHERE user_id = ? AND hwid_hash = ?`, "usr_l", hwidOf("usr_l", n)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ids := []string{deviceID(1), deviceID(2), deviceID(3), deviceID(4)}
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET last_seen_at = ? WHERE id = ?`, unix(linkNow.Add(-29*24*time.Hour)), ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET last_seen_at = ? WHERE id = ?`, unix(linkNow.Add(-31*24*time.Hour)), ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET last_seen_at = 0, created_at = ? WHERE id = ?`, unix(linkNow.Add(-31*24*time.Hour)), ids[2]); err != nil {
		t.Fatal(err)
	}
	// A link device that tunnels but never refetches: its last fetch is old, its last node session is not.
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET last_seen_at = ?, last_connected_at = ? WHERE id = ?`,
		unix(linkNow.Add(-40*24*time.Hour)), unix(linkNow.Add(-2*time.Hour)), ids[3]); err != nil {
		t.Fatal(err)
	}
	var configured string
	if err := s.R.QueryRowContext(ctx, `SELECT v FROM setting WHERE k = ?`, SettingDeviceIdleAfterDays).Scan(&configured); err != nil || configured != "30" {
		t.Fatalf("migrated idle setting = %q, %v; want 30 days", configured, err)
	}
	counts, err := s.Access().DeviceCounts(ctx, []string{"usr_l"}, linkNow)
	if err != nil || counts["usr_l"] != (AccessDeviceCounts{Used: 2, Idle: 2}) {
		t.Fatalf("29/31 day, never-connected and tunnel-only counts = %+v, %v", counts["usr_l"], err)
	}
	devices, err := s.Access().Devices(ctx, "usr_l", linkNow)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AccessDevice{}
	for _, d := range devices {
		byID[d.ID] = d
	}
	if byID[ids[0]].Idle || !byID[ids[1]].Idle || !byID[ids[2]].Idle || byID[ids[3]].Idle {
		t.Fatalf("idle flags: 29d=%v 31d=%v never=%v tunnel-only=%v", byID[ids[0]].Idle, byID[ids[1]].Idle, byID[ids[2]].Idle, byID[ids[3]].Idle)
	}
	wantIdleSince := linkNow.Add(-24 * time.Hour)
	if !byID[ids[1]].IdleSinceAt.Equal(wantIdleSince) || !byID[ids[2]].IdleSinceAt.Equal(wantIdleSince) {
		t.Fatalf("idle since: 31d=%v never=%v, want %v", byID[ids[1]].IdleSinceAt, byID[ids[2]].IdleSinceAt, wantIdleSince)
	}
	if err := s.SetSettings(ctx, map[string]string{SettingDeviceIdleAfterDays: "0"}); err != nil {
		t.Fatal(err)
	}
	counts, err = s.Access().DeviceCounts(ctx, []string{"usr_l"}, linkNow)
	if err != nil || counts["usr_l"] != (AccessDeviceCounts{Used: 4, Idle: 0}) {
		t.Fatalf("disabled idle counts = %+v, %v", counts["usr_l"], err)
	}
}
