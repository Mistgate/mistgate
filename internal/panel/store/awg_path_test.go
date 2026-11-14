//go:build !js

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func insertAWGPathCred(t *testing.T, s *Store, id, deviceID, userID, profileID, path string, idx int, createdAt time.Time) {
	t.Helper()
	execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
		VALUES (?, ?, ?, 'awg', ?, ?, x'01', '{}', 0, ?)`, id, deviceID, userID, profileID, path, unix(createdAt))
	execT(t, s, `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, profileID, idx, "pub_"+id, unix(createdAt))
}

func awgPathFixture(t *testing.T) (*Store, string, string, int) {
	t.Helper()
	s := openTemp(t)
	rewriteAccessGroup(t, s, "grp_alice", "alice")
	rewriteAccessUser(t, s, "usr_alice", "alice", "grp_alice")
	profileID, _ := awgNet(t, s, "de1")
	idx, err := awgDevice(t, s, profileID, "usr_alice", "dev_alice", 32)
	if err != nil {
		t.Fatal(err)
	}
	return s, profileID, "dev_alice", idx
}

func TestAWGPathCredentialsDoNotChangeDeviceViews(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	rewriteAccessGroup(t, s, "grp_alice", "alice")
	rewriteAccessUser(t, s, "usr_alice", "alice", "grp_alice")
	setLinkMode(t, s, LinkModeEnforce)
	profileID, _ := awgNet(t, s, "de1")
	directIdx, err := awgDevice(t, s, profileID, "usr_alice", "dev_alice", 32)
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "bob", nil), 5, linkNow, []AWGImplicitWant{{
		ProfileID: profileID,
		MaxIdx:    32,
		Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_bob_awg", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_awg", nil
		},
	}})
	if err != nil || len(link.Added) != 1 {
		t.Fatalf("link AWG credential = %+v, %v", link, err)
	}
	linkIdx := directIdx + 1
	if link.Added[0].ID != "crd_bob_awg" {
		t.Fatalf("link credential id = %q", link.Added[0].ID)
	}

	type views struct {
		Devices      []AccessDevice
		DeviceCounts map[string]AccessDeviceCounts
		UserCounts   AccessUserCounts
		AWGDevices   []AccessAWGDevice
		LinkAWGKeys  int
		ProfileLive  int
		ProfileUsers []string
		PerProfile   map[string]int
		HealthAWG    []HealthAWGSignal
		DeviceScope  AccessAWGDeviceScope
	}
	readViews := func() views {
		t.Helper()
		var got views
		got.Devices, err = s.Access().Devices(ctx, "usr_alice", linkNow)
		if err != nil {
			t.Fatal(err)
		}
		got.DeviceCounts, err = s.Access().DeviceCounts(ctx, []string{"usr_alice"}, linkNow)
		if err != nil {
			t.Fatal(err)
		}
		users, more, counts, err := s.Access().ListUsers(ctx, AccessUserQuery{GroupID: "grp_alice", Limit: 10, Now: linkNow})
		if err != nil || more || len(users) != 1 {
			t.Fatalf("user list = %d users, more=%v, counts=%+v, err=%v", len(users), more, counts, err)
		}
		got.UserCounts = counts
		got.AWGDevices, err = s.Access().AWGDevices(ctx, "usr_alice")
		if err != nil {
			t.Fatal(err)
		}
		got.LinkAWGKeys, err = s.Access().CountLinkAWGKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got.ProfileLive, got.ProfileUsers, err = s.Access().ProfileLiveAWG(ctx, profileID, 10)
		if err != nil {
			t.Fatal(err)
		}
		got.PerProfile, err = s.Access().AWGDevicesPerProfile(ctx, "", "usr_alice")
		if err != nil {
			t.Fatal(err)
		}
		health, err := s.HealthSignals(ctx, linkNow)
		if err != nil {
			t.Fatal(err)
		}
		got.HealthAWG = health.AWGDevices
		got.DeviceScope, err = s.Access().AWGDeviceScope(ctx, "dev_alice")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	before := readViews()
	if len(before.Devices) != 2 || len(before.AWGDevices) != 1 || before.LinkAWGKeys != 1 || before.ProfileLive != 1 ||
		before.PerProfile[profileID] != 1 || len(before.HealthAWG) != 1 {
		t.Fatalf("fixture did not start with one key per AWG device: %+v", before)
	}
	insertAWGPathCred(t, s, "crd_alice_path", "dev_alice", "usr_alice", profileID, "relay:nod_msk1", directIdx+2, linkNow)
	insertAWGPathCred(t, s, "crd_bob_path", link.Device.ID, "usr_alice", profileID, "relay:nod_msk1", linkIdx+2, linkNow)
	after := readViews()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("path credentials changed device views:\nbefore: %+v\nafter:  %+v", before, after)
	}

	data, err := s.Access().SubscriptionData(ctx, "usr_alice", "grp_alice", linkNow, false, linkNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data.Device.ID != link.Device.ID || len(data.DeviceCreds) != 1 || data.DeviceCreds[0].ID != "crd_bob_awg" || data.AWG[0].ID != "dev_alice" {
		t.Fatalf("link snapshot must hold the direct key only, next to the direct AWG device list: device=%+v creds=%+v awg=%+v", data.Device, data.DeviceCreds, data.AWG)
	}
	// The create paths see the direct key too: a path key neither hides it nor counts for it.
	again, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "bob", nil), 5, linkNow, []AWGImplicitWant{{
		ProfileID: profileID,
		MaxIdx:    32,
		Issue: func(int) (AccessCred, string, error) {
			return AccessCred{}, "", errors.New("a key is already there: nothing to issue")
		},
	}})
	if err != nil || len(again.Added) != 0 || len(again.Creds) != 1 || again.Creds[0].ID != "crd_bob_awg" {
		t.Fatalf("EnsureLinkAWGCreds with a path key next to the direct one = %+v, %v", again, err)
	}
}

func TestOrphanPathQueryUsesPathIndex(t *testing.T) {
	s := openTemp(t)
	rows, err := s.R.QueryContext(context.Background(), `EXPLAIN QUERY PLAN SELECT count(*) FROM `+awgOrphanPathCredFrom+` WHERE `+awgOrphanPathCredWhere)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if joined := strings.Join(plan, "; "); !strings.Contains(joined, "SCAN c USING INDEX device_credential_path") && !strings.Contains(joined, "SEARCH c USING INDEX device_credential_path") {
		t.Fatalf("the orphan path count does not use the partial path index: %v", plan)
	}
}

func TestRotateAWGDeviceRevokesPathCredentials(t *testing.T) {
	ctx := context.Background()
	s, profileID, deviceID, directIdx := awgPathFixture(t)
	insertAWGPathCred(t, s, "crd_alice_path", deviceID, "usr_alice", profileID, "relay:nod_msk1", directIdx+1, linkNow.Add(-time.Hour))
	now := linkNow.Add(time.Hour)
	userID, err := s.Access().RotateAWGDevice(ctx, deviceID, now, func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_alice_rotated", Protocol: "awg", SecretEnc: []byte{2}, DataJSON: `{}`}, "pub_alice_rotated", nil
	})
	if err != nil || userID != "usr_alice" {
		t.Fatalf("RotateAWGDevice = %q, %v", userID, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_path' AND revoked_at = ?`, now.Unix()); got != 1 {
		t.Fatalf("path credential revoked at rotation = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice_path' AND released_at = ?`, now.Unix()); got != 1 {
		t.Fatalf("path peer quarantined at rotation = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_rotated' AND path = '' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("replacement direct credential = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice_path' AND released_at > 0`); got != 1 {
		t.Fatalf("path address was not left in quarantine: %d", got)
	}
}

func TestRevokeDeviceIncludesAWGPathCredentials(t *testing.T) {
	ctx := context.Background()
	s, profileID, deviceID, directIdx := awgPathFixture(t)
	insertAWGPathCred(t, s, "crd_alice_path", deviceID, "usr_alice", profileID, "relay:nod_msk1", directIdx+1, linkNow)
	now := linkNow.Add(time.Hour)
	if userID, err := s.Access().RevokeDevice(ctx, deviceID, now); err != nil || userID != "usr_alice" {
		t.Fatalf("RevokeDevice = %q, %v", userID, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at = ?`, deviceID, now.Unix()); got != 2 {
		t.Fatalf("device credentials revoked = %d, want direct and path", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_dev_alice', 'crd_alice_path') AND released_at = ?`, now.Unix()); got != 2 {
		t.Fatalf("device peer addresses quarantined = %d, want 2", got)
	}
}

func TestRevokeOrphanPathCredsAndDeleteRelay(t *testing.T) {
	ctx := context.Background()
	s, profileID, deviceID, directIdx := awgPathFixture(t)
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_nl1', 'nl1', '192.0.2.20', 'active', 1)`)
	execT(t, s, `INSERT INTO node (id, name, address, country_code, state, created_at, agent_caps) VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 1, 'relay-forward/1')`)
	execT(t, s, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_nl1', ?, 'nod_nl1', 1, 1)`, profileID)
	now := linkNow.Add(time.Hour)
	for _, relay := range []Relay{
		{ID: "rly_de1", InboundID: "inb_de1", ViaNodeID: "nod_msk1", Network: "udp", Port: 8443},
		{ID: "rly_nl1", InboundID: "inb_nl1", ViaNodeID: "nod_msk1", Network: "udp", Port: 8444},
	} {
		if err := s.CreateRelay(ctx, relay, now); err != nil {
			t.Fatal(err)
		}
	}
	insertAWGPathCred(t, s, "crd_alice_orphan", deviceID, "usr_alice", profileID, "relay:nod_nl1", directIdx+1, linkNow)
	insertAWGPathCred(t, s, "crd_alice_msk1", deviceID, "usr_alice", profileID, "relay:nod_msk1", directIdx+2, linkNow)

	if n, err := s.RevokeOrphanPathCreds(ctx, now); err != nil || n != 1 {
		t.Fatalf("RevokeOrphanPathCreds = %d, %v; want one orphan", n, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_orphan' AND revoked_at = ?`, now.Unix()); got != 1 {
		t.Fatalf("orphan credential revocation = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice_orphan' AND released_at = ?`, now.Unix()); got != 1 {
		t.Fatalf("orphan peer quarantine = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_msk1' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("non-orphan credential revocation = %d, want 0", got)
	}

	if err := s.DeleteRelay(ctx, "rly_de1"); err != nil {
		t.Fatal(err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_msk1' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("path credential with another relay was revoked: %d", got)
	}
	if err := s.DeleteRelay(ctx, "rly_nl1"); err != nil {
		t.Fatal(err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_msk1' AND revoked_at IS NOT NULL`); got != 1 {
		t.Fatalf("last relay deletion did not revoke path credential: %d", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice_msk1' AND released_at > 0`); got != 1 {
		t.Fatalf("last relay deletion did not quarantine peer address: %d", got)
	}
}
