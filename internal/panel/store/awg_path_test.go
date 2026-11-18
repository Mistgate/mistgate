//go:build !js

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
		// The scope carries the relay keys of the device on purpose (the configs through an entry need them): they are the one
		// thing that must differ, and nothing else in the scope may.
		got.DeviceScope.PathCreds = nil
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
	if scope, err := s.Access().AWGDeviceScope(ctx, "dev_alice"); err != nil || len(scope.PathCreds) != 1 || scope.PathCreds[0].ID != "crd_alice_path" || scope.PathCreds[0].Path != "relay:nod_msk1" {
		t.Fatalf("the device scope must carry the device's own relay key: %+v, %v", scope.PathCreds, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("path credentials changed device views:\nbefore: %+v\nafter:  %+v", before, after)
	}

	data, err := s.Access().SubscriptionData(ctx, "usr_alice", linkNow, false, linkNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data.Device.ID != link.Device.ID || len(data.DeviceCreds) != 2 || data.DeviceCreds[0].ID != "crd_bob_awg" ||
		data.DeviceCreds[0].Path != "" || data.DeviceCreds[1].ID != "crd_bob_path" || data.DeviceCreds[1].Path != "relay:nod_msk1" || data.AWG[0].ID != "dev_alice" {
		t.Fatalf("link snapshot did not retain both credential paths next to the direct AWG device list: device=%+v creds=%+v awg=%+v", data.Device, data.DeviceCreds, data.AWG)
	}
	// The create paths see the direct key too: a path key neither hides it nor counts for it.
	again, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "bob", nil), 5, linkNow, []AWGImplicitWant{{
		ProfileID: profileID,
		MaxIdx:    32,
		Issue: func(int) (AccessCred, string, error) {
			return AccessCred{}, "", errors.New("a key is already there: nothing to issue")
		},
	}})
	if err != nil || len(again.Added) != 0 || len(again.Creds) != 2 || again.Creds[0].ID != "crd_bob_awg" {
		t.Fatalf("EnsureLinkAWGCreds with a path key next to the direct one = %+v, %v", again, err)
	}
}

func TestEnsureKeyPathCredRequiresRelayAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, profileID, deviceID, _ := awgPathFixture(t)
	path := "relay:nod_msk1"
	issue := func(idx int) (AccessCred, string, error) {
		return AccessCred{ID: NewID("crd_path_"), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_path", nil
	}
	if _, _, err := s.Access().EnsureKeyPathCred(ctx, deviceID, profileID, path, 32, linkNow, issue); !errors.Is(err, ErrNotFound) {
		t.Fatalf("path issue without a relay = %v, want ErrNotFound", err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND path = ? AND revoked_at IS NULL`, deviceID, path); got != 0 {
		t.Fatalf("path credential without a relay = %d, want 0", got)
	}
	execT(t, s, `INSERT INTO node (id, name, address, country_code, state, created_at, agent_caps) VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 1, 'relay-forward/1')`)
	if err := s.CreateRelay(ctx, Relay{ID: "rly_msk1", InboundID: "inb_de1", ViaNodeID: "nod_msk1", Network: "udp", Port: 8443}, linkNow); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	issue = func(idx int) (AccessCred, string, error) {
		id := NewID("crd_path_")
		calls.Add(1)
		return AccessCred{ID: id, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_path", nil
	}
	var wg sync.WaitGroup
	creds := make([]AccessCred, 2)
	created := make([]bool, 2)
	errs := make([]error, 2)
	for i := range creds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			creds[i], created[i], errs[i] = s.Access().EnsureKeyPathCred(ctx, deviceID, profileID, path, 32, linkNow, issue)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("parallel path issue %d: %v", i, err)
		}
	}
	if creds[0].ID == "" || creds[0].ID != creds[1].ID || creds[0].Path != path || created[0] == created[1] {
		t.Fatalf("parallel results = %+v/%v and %+v/%v; want one stored key", creds[0], created[0], creds[1], created[1])
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = ? AND revoked_at IS NULL`, deviceID, profileID, path); got != 1 {
		t.Fatalf("live path credentials = %d, want one", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = ? AND released_at = 0`, creds[0].ID); got != 1 {
		t.Fatalf("path peer allocation = %d, want one", got)
	}
}

func TestEnsureLinkAWGSkipsAPathWithoutItsRelay(t *testing.T) {
	ctx := context.Background()
	s, profileID, _, _ := awgPathFixture(t)
	direct := AWGImplicitWant{ProfileID: profileID, MaxIdx: 32, Issue: func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_bob_awg", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_awg", nil
	}}
	path := AWGImplicitWant{ProfileID: profileID, Path: "relay:nod_msk1", MaxIdx: 32, Issue: func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_bob_path", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_path", nil
	}}
	result, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "bob", nil), 5, linkNow, []AWGImplicitWant{direct, path})
	if err != nil || len(result.Added) != 1 || result.Added[0].Path != "" || len(result.Creds) != 1 || result.Creds[0].Path != "" {
		t.Fatalf("link AWG issue without a relay = %+v, %v; want the direct key only", result, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND path = ? AND revoked_at IS NULL`, result.Device.ID, path.Path); got != 0 {
		t.Fatalf("path key without its relay = %d, want 0", got)
	}
}

func TestEnsureLinkAWGIssuesIPv6PathOnlyWhileEntryIsOn(t *testing.T) {
	ctx := context.Background()
	s, profileID, _, _ := awgPathFixture(t)
	direct := AWGImplicitWant{ProfileID: profileID, MaxIdx: 32, Issue: func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_bob_awg", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_awg", nil
	}}
	ipv6 := AWGImplicitWant{ProfileID: profileID, Path: "ipv6", MaxIdx: 32, Issue: func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_bob_ipv6", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_ipv6", nil
	}}
	dev := linkDevice("usr_alice", "bob", nil)
	result, err := s.Access().EnsureLinkAWGCreds(ctx, dev, 5, linkNow, []AWGImplicitWant{direct, ipv6})
	if err != nil || len(result.Added) != 1 || result.Added[0].Path != "" || len(result.Creds) != 1 {
		t.Fatalf("link AWG issue with the entry off = %+v, %v; want the direct key only", result, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = 'ipv6' AND revoked_at IS NULL`, result.Device.ID, profileID); got != 0 {
		t.Fatalf("IPv6 path key with the entry off = %d, want 0", got)
	}
	execT(t, s, `UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_de1'`)
	result, err = s.Access().EnsureLinkAWGCreds(ctx, dev, 5, linkNow, []AWGImplicitWant{direct, ipv6})
	if err != nil || len(result.Added) != 1 || result.Added[0].Path != "ipv6" || len(result.Creds) != 2 {
		t.Fatalf("link AWG issue with the entry on = %+v, %v; want one IPv6 path key next to direct", result, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = 'ipv6' AND revoked_at IS NULL`, result.Device.ID, profileID); got != 1 {
		t.Fatalf("IPv6 path keys with the entry on = %d, want 1", got)
	}
}

func TestEnsureLinkAWGIPv6PathNetworkFullKeepsDirect(t *testing.T) {
	ctx := context.Background()
	s, profileID, _, _ := awgPathFixture(t)
	execT(t, s, `UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_de1'`)
	result, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "bob", nil), 5, linkNow, []AWGImplicitWant{
		{ProfileID: profileID, MaxIdx: 3, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_bob_awg", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_awg", nil
		}},
		{ProfileID: profileID, Path: "ipv6", MaxIdx: 3, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_bob_ipv6", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_ipv6", nil
		}},
	})
	if err != nil || len(result.Added) != 1 || result.Added[0].Path != "" || len(result.Creds) != 1 || len(result.FullPaths) != 1 || result.FullPaths[0].Path != "ipv6" {
		t.Fatalf("link AWG issue with only the direct index free = %+v, %v; want direct and a skipped IPv6 path", result, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = '' AND revoked_at IS NULL`, result.Device.ID, profileID); got != 1 {
		t.Fatalf("direct credential after path pool exhaustion = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = 'ipv6' AND revoked_at IS NULL`, result.Device.ID, profileID); got != 0 {
		t.Fatalf("IPv6 path credential after pool exhaustion = %d, want 0", got)
	}
}

func TestEnsureLinkAWGPathIssueFailureKeepsTheDirectCredential(t *testing.T) {
	ctx := context.Background()
	s, profileID, _, _ := awgPathFixture(t)
	setLinkMode(t, s, LinkModeEnforce)
	execT(t, s, `INSERT INTO node (id, name, address, country_code, state, created_at, agent_caps)
		VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 1, 'relay-forward/1')`)
	if err := s.CreateRelay(ctx, Relay{ID: "rly_msk1", InboundID: "inb_de1", ViaNodeID: "nod_msk1", Network: "udp", Port: 8443}, linkNow); err != nil {
		t.Fatal(err)
	}
	result, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "bob", nil), 5, linkNow, []AWGImplicitWant{
		{ProfileID: profileID, MaxIdx: 32, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_bob_awg", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_bob_awg", nil
		}},
		{ProfileID: profileID, Path: "relay:nod_msk1", MaxIdx: 32, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{}, "", errors.New("temporary key generation failure")
		}},
	})
	if err != nil || len(result.Added) != 1 || result.Added[0].Path != "" || len(result.Creds) != 1 || len(result.FailedPaths) != 1 ||
		result.FailedPaths[0].ProfileID != profileID || result.FailedPaths[0].Path != "relay:nod_msk1" || result.FailedPaths[0].Err == nil {
		t.Fatalf("link AWG result after a path issue failure = %+v, %v", result, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = '' AND revoked_at IS NULL`, result.Device.ID, profileID); got != 1 {
		t.Fatalf("direct key after path issue failure = %d, want one", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND profile_id = ? AND path = 'relay:nod_msk1' AND revoked_at IS NULL`, result.Device.ID, profileID); got != 0 {
		t.Fatalf("path key after issue failure = %d, want none", got)
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
	insertAWGPathCred(t, s, "crd_alice_ipv6", deviceID, "usr_alice", profileID, "ipv6", directIdx+2, linkNow.Add(-time.Hour))
	now := linkNow.Add(time.Hour)
	userID, err := s.Access().RotateAWGDevice(ctx, deviceID, now, func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_alice_rotated", Protocol: "awg", SecretEnc: []byte{2}, DataJSON: `{}`}, "pub_alice_rotated", nil
	})
	if err != nil || userID != "usr_alice" {
		t.Fatalf("RotateAWGDevice = %q, %v", userID, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id IN ('crd_alice_path', 'crd_alice_ipv6') AND revoked_at = ?`, now.Unix()); got != 2 {
		t.Fatalf("path credentials revoked at rotation = %d, want 2", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_alice_path', 'crd_alice_ipv6') AND released_at = ?`, now.Unix()); got != 2 {
		t.Fatalf("path peers quarantined at rotation = %d, want 2", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_rotated' AND path = '' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("replacement direct credential = %d, want 1", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_alice_path', 'crd_alice_ipv6') AND released_at > 0`); got != 2 {
		t.Fatalf("path addresses were not left in quarantine: %d", got)
	}
}

func TestRevokeDeviceIncludesAWGPathCredentials(t *testing.T) {
	ctx := context.Background()
	s, profileID, deviceID, directIdx := awgPathFixture(t)
	insertAWGPathCred(t, s, "crd_alice_path", deviceID, "usr_alice", profileID, "relay:nod_msk1", directIdx+1, linkNow)
	insertAWGPathCred(t, s, "crd_alice_ipv6", deviceID, "usr_alice", profileID, "ipv6", directIdx+2, linkNow)
	now := linkNow.Add(time.Hour)
	if userID, err := s.Access().RevokeDevice(ctx, deviceID, now); err != nil || userID != "usr_alice" {
		t.Fatalf("RevokeDevice = %q, %v", userID, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at = ?`, deviceID, now.Unix()); got != 3 {
		t.Fatalf("device credentials revoked = %d, want direct and both paths", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id IN ('crd_dev_alice', 'crd_alice_path', 'crd_alice_ipv6') AND released_at = ?`, now.Unix()); got != 3 {
		t.Fatalf("device peer addresses quarantined = %d, want 3", got)
	}
}

func TestRevokeOrphanIPv6PathCredsFollowsConfiguredEntry(t *testing.T) {
	ctx := context.Background()
	s, profileID, deviceID, directIdx := awgPathFixture(t)
	insertAWGPathCred(t, s, "crd_alice_ipv6", deviceID, "usr_alice", profileID, "ipv6", directIdx+1, linkNow)
	now := linkNow.Add(time.Hour)
	execT(t, s, `UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_de1'`)
	if n, err := s.RevokeOrphanPathCreds(ctx, now); err != nil || n != 0 {
		t.Fatalf("sweep with IPv6 entry on = %d, %v; want no revocation", n, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_ipv6' AND revoked_at IS NULL`); got != 1 {
		t.Fatalf("IPv6 key with entry on is live = %d, want 1", got)
	}
	execT(t, s, `UPDATE node SET ipv6_entry = '' WHERE id = 'nod_de1'`)
	if n, err := s.RevokeOrphanPathCreds(ctx, now); err != nil || n != 1 {
		t.Fatalf("sweep with IPv6 entry off = %d, %v; want one revocation", n, err)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_alice_ipv6' AND revoked_at = ?`, now.Unix()); got != 1 {
		t.Fatalf("IPv6 key after entry off = %d, want revoked", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_alice_ipv6' AND released_at = ?`, now.Unix()); got != 1 {
		t.Fatalf("IPv6 peer after entry off = %d, want quarantined", got)
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
