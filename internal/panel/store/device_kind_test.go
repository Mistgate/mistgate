//go:build !js

package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 00060 marks every device that carried a hwid_hash as a key (until then only AddAWGDevice wrote one) and leaves the
// implicit device a link; rolling back drops the column and it applies again.
func TestDeviceKindMigrationBackfillsKeys(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 59); err != nil {
		t.Fatalf("up to 59: %v", err)
	}
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_k', 'k', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_k', 'k', 'grp_k', 1, x'01', x'01', 1)`)
	execT(t, s, `INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at) VALUES
		('dev_implicit', 'usr_k', NULL, 1, 1, 1), ('dev_key', 'usr_k', x'aa', 0, 0, 1)`)
	if _, err := p.UpTo(ctx, 60); err != nil {
		t.Fatalf("up to 60: %v", err)
	}
	for id, want := range map[string]string{"dev_implicit": "link", "dev_key": "key"} {
		var kind string
		if err := s.W.QueryRowContext(ctx, `SELECT kind FROM device WHERE id = ?`, id).Scan(&kind); err != nil || kind != want {
			t.Fatalf("kind of %s = %q, %v; want %q", id, kind, err, want)
		}
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET kind = 'other' WHERE id = 'dev_key'`); err == nil {
		t.Fatal("the column accepted an unknown kind")
	}
	if _, err := p.DownTo(ctx, 59); err != nil {
		t.Fatalf("down to 59: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `SELECT kind FROM device`); err == nil {
		t.Fatal("the column is left after down")
	}
	if _, err := p.UpTo(ctx, 60); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// A device that is not an AmneziaVPN key may hold AWG credentials (a future HWID device with AmneziaWG inside a Mihomo
// profile). Nothing that means "a key" may list it, count it, rename or rotate it, or revoke it with the profile.
func TestLinkDeviceWithAWGCredentialsIsNotAKey(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, _ := awgNet(t, s, "k")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_k', 'k', 1)`)
	if err := s.Access().UpdateGroup(ctx, "grp_k", nil, &[]string{profile}, nil, nil); err != nil {
		t.Fatal(err)
	}
	u := AccessUser{
		ID: "usr_k", Name: "kk", GroupID: "grp_k", AllNodes: true, Status: "active", QuotaReset: "none", DeviceLimit: 5, AppAmnezia: true,
		SubTokenHash: []byte("kind-token"), SubTokenEnc: []byte{1}, CreatedAt: time.Unix(100, 0), PeriodStart: time.Unix(100, 0),
	}
	if err := s.Access().CreateUser(ctx, u, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := awgDevice(t, s, profile, u.ID, "dev_key", 8); err != nil {
		t.Fatal(err)
	}
	// The look-alike: its own hwid_hash, kind link, a live AWG credential with a peer.
	now := time.Now().Unix()
	execT(t, s, `INSERT INTO device (id, user_id, hwid_hash, kind, first_seen_at, last_seen_at, created_at) VALUES ('dev_link', ?, x'bb', 'link', ?, ?, ?)`, u.ID, now, now, now)
	execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, secret_enc, data_json, created_at)
		VALUES ('crd_link', 'dev_link', ?, 'awg', ?, x'01', '{}', ?)`, u.ID, profile, now)
	execT(t, s, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES ('crd_link', ?, 5, 'pub_link', ?)`, profile, now)

	if got, err := s.Access().AWGDevices(ctx, u.ID); err != nil || len(got) != 1 || got[0].ID != "dev_key" {
		t.Fatalf("AWGDevices = %+v, %v; want only dev_key", got, err)
	}
	if _, err := s.Access().AWGDevice(ctx, "dev_link"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AWGDevice(link) = %v, want ErrNotFound", err)
	}
	if _, err := s.Access().AWGDeviceScope(ctx, "dev_link"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AWGDeviceScope(link) = %v, want ErrNotFound", err)
	}
	if err := s.Access().RenameDevice(ctx, "dev_link", "renamed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RenameDevice(link) = %v, want ErrNotFound", err)
	}
	if _, err := s.Access().RotateAWGDevice(ctx, "dev_link", time.Now(), func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_rot", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_rot", nil
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RotateAWGDevice(link) = %v, want ErrNotFound", err)
	}
	if n, users, err := s.Access().ProfileLiveAWG(ctx, profile, 10); err != nil || n != 1 || len(users) != 1 {
		t.Fatalf("ProfileLiveAWG = %d %v, %v; want the key only", n, users, err)
	}
	if per, err := s.Access().AWGDevicesPerProfile(ctx, "", u.ID); err != nil || per[profile] != 1 {
		t.Fatalf("AWGDevicesPerProfile = %v, %v; want 1", per, err)
	}
	data, err := s.Access().SubscriptionData(ctx, u.ID, "grp_k", time.Unix(0, 0), true, time.Now(), nil)
	if err != nil || len(data.AWG) != 1 || data.AWG[0].ID != "dev_key" {
		t.Fatalf("SubscriptionData.AWG = %+v, %v; want only dev_key", data.AWG, err)
	}
	signals, err := s.HealthSignals(ctx, time.Now())
	if err != nil || len(signals.AWGDevices) != 1 || signals.AWGDevices[0].DeviceID != "dev_key" {
		t.Fatalf("health AWG signals = %+v, %v; want only dev_key", signals.AWGDevices, err)
	}
	// Both are devices of the user, and the kinds read back.
	devices, err := s.Access().Devices(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, d := range devices {
		kinds[d.ID] = d.Kind
	}
	if kinds["dev_key"] != DeviceKindKey || kinds["dev_link"] != DeviceKindLink || len(kinds) != 2 {
		t.Fatalf("device kinds = %v", kinds)
	}

	execT(t, s, `DELETE FROM inbound WHERE profile_id = ?`, profile)
	if err := s.Access().DeleteProfile(ctx, profile, time.Now()); err != nil {
		t.Fatal(err)
	}
	if countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_key' AND revoked_at IS NOT NULL`) != 1 {
		t.Fatal("DeleteProfile left the key device alive")
	}
	if countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_link' AND revoked_at IS NULL`) != 1 {
		t.Fatal("DeleteProfile revoked a link device")
	}
}

// "This is an AmneziaVPN key" is device.kind = 'key'. A hwid_hash that is not NULL says nothing about it: a device that
// reports a real HWID has one and is a link. No SQL of the panel may go back to guessing from it.
func TestNoSQLTreatsAHwidHashAsAKey(t *testing.T) {
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToLower(string(body)), "hwid_hash is not null") {
			t.Errorf("%s: use device.kind = 'key' instead of hwid_hash IS NOT NULL", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
