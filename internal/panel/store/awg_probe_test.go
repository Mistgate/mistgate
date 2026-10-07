//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// A live database (00019) with a Hysteria2 probe credential: 00020 adds one column with a default, the row stays valid
// with index 0, the column refuses an index no peer can have, rolling back drops the column and the AWG credentials (not
// the Hysteria2 one), and it applies again.
func TestAWGProbeMigrationUpDownUp(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 19); err != nil {
		t.Fatalf("up to 19: %v", err)
	}
	_, inbound := fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO health_probe_cred (inbound_id, cred_id, secret_enc, data_json, created_at) VALUES (?, 'crd_p', x'01', '{}', 1)`, inbound)
	if _, err := p.UpTo(ctx, 20); err != nil {
		t.Fatalf("up to 20: %v", err)
	}
	row, err := s.ProbeCred(ctx, inbound)
	if err != nil || row.CredID != "crd_p" || row.AWGIdx != 0 {
		t.Fatalf("a probe credential after 00020 = %+v, %v", row, err)
	}
	for _, bad := range []int{1, -3} {
		if _, err := s.W.ExecContext(ctx, `UPDATE health_probe_cred SET awg_idx = ?`, bad); err == nil {
			t.Fatalf("the index %d was accepted", bad)
		}
	}
	_, awgInbound := awgNet(t, s, "w")
	execT(t, s, `INSERT INTO health_probe_cred (inbound_id, cred_id, secret_enc, data_json, created_at, awg_idx) VALUES (?, 'crd_w', x'01', '{}', 1, 5)`, awgInbound)
	if _, err := p.DownTo(ctx, 19); err != nil {
		t.Fatalf("down to 19: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM health_probe_cred WHERE cred_id = 'crd_p'`) != 1 {
		t.Fatal("down touched the Hysteria2 credential")
	}
	if countT(t, s, `SELECT count(*) FROM health_probe_cred WHERE cred_id = 'crd_w'`) != 0 {
		t.Fatal("down kept an AWG credential without its index: after Up its address would go to a device")
	}
	if _, err := s.W.ExecContext(ctx, `SELECT awg_idx FROM health_probe_cred`); err == nil {
		t.Fatal("the column is left after down")
	}
	if _, err := p.UpTo(ctx, 20); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// awgNet puts an AWG profile with one inbound in the store and returns what the allocator needs.
func awgNet(t *testing.T, s *Store, suffix string) (profile, inbound string) {
	t.Helper()
	node := "nod_" + suffix
	profile, inbound = "prf_"+suffix, "inb_"+suffix
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, 'n.example.com', 'active', 1)`, node, "n"+suffix)
	execT(t, s, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, 'awg', ?, '{}', 1, 1)`, profile, "p"+suffix)
	execT(t, s, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, inbound, profile, node)
	return profile, inbound
}

func awgDevice(t *testing.T, s *Store, profile, user, dev string, maxIdx int) (int, error) {
	t.Helper()
	return s.Access().AddAWGDevice(context.Background(), AWGDeviceAdd{Device: AccessDevice{ID: dev, UserID: user}, ProfileID: profile, MaxIdx: maxIdx, Limit: 1000}, time.Unix(100, 0),
		func(idx int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_" + dev, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_" + dev, nil
		})
}

// The probe credential and the devices draw from one pool of tunnel addresses, in either order, and the probe's index
// is free again with its inbound (no quarantine: nothing outside the panel held that address).
func TestAWGProbeCredentialSharesTheAllocator(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, inbound := awgNet(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	now := time.Unix(100, 0)
	issue := func(credID string) ProbeCredIssue {
		return func(idx int) (ProbeCredRow, error) {
			return ProbeCredRow{CredID: credID, SecretEnc: []byte{9}, DataJSON: fmt.Sprintf(`{"idx":%d}`, idx)}, nil
		}
	}

	// a device first (index 2), then the probe (3), then a device again (4)
	if idx, err := awgDevice(t, s, profile, "usr_a", "dev_1", 10); err != nil || idx != 2 {
		t.Fatalf("first device: %d %v", idx, err)
	}
	if err := s.InsertProbeCredIdx(ctx, inbound, profile, 10, now, issue("crd_p1")); err != nil {
		t.Fatal(err)
	}
	row, err := s.ProbeCred(ctx, inbound)
	if err != nil || row.AWGIdx != 3 || row.CredID != "crd_p1" || row.DataJSON != `{"idx":3}` {
		t.Fatalf("probe credential: %+v %v", row, err)
	}
	if idx, err := awgDevice(t, s, profile, "usr_a", "dev_2", 10); err != nil || idx != 4 {
		t.Fatalf("a device after the probe got %d, %v (the probe holds 3)", idx, err)
	}
	// a second call keeps the first credential and does not even ask for an index (the issuer is not called)
	if err := s.InsertProbeCredIdx(ctx, inbound, profile, 10, now, func(int) (ProbeCredRow, error) { return ProbeCredRow{}, errors.New("called") }); err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if again, _ := s.ProbeCred(ctx, inbound); again.CredID != "crd_p1" || again.AWGIdx != 3 {
		t.Fatalf("the credential changed: %+v", again)
	}
	// the probe is no peer, no device, no credential of anyone
	for _, q := range []string{`SELECT count(*) FROM awg_peer WHERE idx = 3`, `SELECT count(*) FROM device_credential WHERE id = 'crd_p1'`} {
		if n := countT(t, s, q); n != 0 {
			t.Fatalf("%s = %d", q, n)
		}
	}

	// an issuer that fails leaves nothing behind (and takes no address)
	const inbound2 = "inb_b" // the same profile on another node
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_b', 'nb', 'b.example.com', 'active', 1)`)
	execT(t, s, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES (?, ?, 'nod_b', 1, 1)`, inbound2, profile)
	if err := s.InsertProbeCredIdx(ctx, inbound2, profile, 10, now, func(int) (ProbeCredRow, error) { return ProbeCredRow{}, errors.New("no") }); err == nil {
		t.Fatal("a failing issuer was accepted")
	}
	if _, err := s.ProbeCred(ctx, inbound2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a row was left: %v", err)
	}
	if err := s.InsertProbeCredIdx(ctx, inbound2, profile, 10, now, issue("crd_p2")); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.ProbeCred(ctx, inbound2); r.AWGIdx != 5 {
		t.Fatalf("the second inbound's probe got %d, want 5", r.AWGIdx)
	}

	// an inbound that does not exist
	if err := s.InsertProbeCredIdx(ctx, "inb_missing", profile, 10, now, issue("crd_x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing inbound: %v", err)
	}

	// the inbound goes, its credential and its address go with it: the next device takes the lowest free index
	execT(t, s, `DELETE FROM inbound WHERE id = ?`, inbound)
	if _, err := s.ProbeCred(ctx, inbound); !errors.Is(err, ErrNotFound) {
		t.Fatalf("credential outlived its inbound: %v", err)
	}
	if idx, err := awgDevice(t, s, profile, "usr_a", "dev_3", 10); err != nil || idx != 3 {
		t.Fatalf("after the inbound went: %d %v", idx, err)
	}
}

func TestAddAWGDeviceAllocatesAndGuardsThePeer(t *testing.T) {
	s := openTemp(t)
	profile, _ := awgNet(t, s, "batch")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_batch', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_batch', 'u', 'grp_batch', 1, x'01', x'02', 1)`)

	idx, err := s.Access().AddAWGDevice(context.Background(), AWGDeviceAdd{
		Device:    AccessDevice{ID: "dev_batch", UserID: "usr_batch", Platform: "linux", Model: "test"},
		ProfileID: profile, MaxIdx: 8, Limit: 5,
	}, time.Unix(100, 0), func(idx int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_batch", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_batch", nil
	})
	if err != nil || idx != 2 {
		t.Fatalf("AddAWGDevice = %d, %v; want index 2", idx, err)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE profile_id = ? AND idx = 2 AND released_at = 0`, profile); n != 1 {
		t.Fatalf("live peer at index 2 = %d, want 1", n)
	}
}

func TestEnsureImplicitAWGCredsCreatesOnce(t *testing.T) {
	s := openTemp(t)
	profile, _ := awgNet(t, s, "implicit_batch")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_batch', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_implicit_batch', 'u', 'grp_implicit_batch', 1, x'01', x'02', 1)`)
	want := []AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: func(idx int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_implicit_batch", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_implicit_batch", nil
	}}}
	dev := AccessDevice{ID: "dev_implicit_batch", UserID: "usr_implicit_batch", Implicit: true}
	result, err := s.Access().EnsureImplicitAWGCreds(context.Background(), dev.UserID, dev, time.Unix(100, 0), want)
	if err != nil || result.Device.ID != dev.ID || len(result.Added) != 1 || len(result.Creds) != 1 {
		t.Fatalf("EnsureImplicitAWGCreds = %+v, %v; want the device and one added/live credential", result, err)
	}
	result, err = s.Access().EnsureImplicitAWGCreds(context.Background(), dev.UserID, dev, time.Unix(100, 0), []AWGImplicitWant{{
		ProfileID: profile, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{}, "", errors.New("issuer called for an existing peer")
		},
	}})
	if err != nil || result.Device.ID != dev.ID || len(result.Added) != 0 || len(result.Creds) != 1 {
		t.Fatalf("repeat EnsureImplicitAWGCreds = %+v, %v; want the existing live credential and no additions", result, err)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE profile_id = ? AND idx = 2 AND released_at = 0`, profile); n != 1 {
		t.Fatalf("live implicit peer at index 2 = %d, want 1", n)
	}
}

func TestEnsureImplicitDeviceConcurrentIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const userID = "usr_implicit_device_race"
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_device_race', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, 'u', 'grp_implicit_device_race', 1, x'01', x'02', 1)`, userID)
	now := time.Unix(100, 0)
	start := make(chan struct{})
	type result struct {
		device  AccessDevice
		creds   []AccessCred
		created bool
		err     error
	}
	results := make(chan result, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			suffix, devID := "a", "dev_implicit_device_a"
			if i == 1 {
				suffix, devID = "b", "dev_implicit_device_b"
			}
			dev := AccessDevice{ID: devID, UserID: userID, Implicit: true, CreatedAt: now, NoInitialSeen: true}
			creds := []AccessCred{
				{ID: "crd_implicit_device_" + suffix + "_hysteria", DeviceID: dev.ID, UserID: userID, Protocol: "hysteria2", SecretEnc: []byte{1}, DataJSON: `{}`, CreatedAt: now},
				{ID: "crd_implicit_device_" + suffix + "_tuic", DeviceID: dev.ID, UserID: userID, Protocol: "tuic", SecretEnc: []byte{2}, DataJSON: `{}`, CreatedAt: now},
			}
			gotDevice, gotCreds, created, err := s.Access().EnsureImplicitDevice(ctx, dev, creds)
			results <- result{device: gotDevice, creds: gotCreds, created: created, err: err}
		}(i)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("EnsureImplicitDevice errors = %v, %v", first.err, second.err)
	}
	if first.created == second.created {
		t.Fatalf("created flags = %v, %v; want exactly one true", first.created, second.created)
	}
	for _, got := range []result{first, second} {
		if got.device.ID == "" || len(got.creds) != 2 || len(got.device.Protocols) != 2 {
			t.Fatalf("EnsureImplicitDevice returned %+v", got)
		}
		protocols := map[string]bool{}
		for _, cred := range got.creds {
			protocols[cred.Protocol] = cred.DeviceID == got.device.ID
		}
		if len(protocols) != 2 || !protocols["hysteria2"] || !protocols["tuic"] {
			t.Fatalf("EnsureImplicitDevice credentials = %+v, want one of each protocol on %q", got.creds, got.device.ID)
		}
	}
	if first.device.ID != second.device.ID || !reflect.DeepEqual(first.creds, second.creds) {
		t.Fatalf("callers saw different implicit state: %+v / %+v", first, second)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, userID); n != 1 {
		t.Fatalf("live implicit devices = %d, want 1", n)
	}
	for _, protocol := range []string{"hysteria2", "tuic"} {
		if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = ? AND revoked_at IS NULL`, userID, protocol); n != 1 {
			t.Fatalf("live %s credentials = %d, want 1", protocol, n)
		}
	}
	var firstSeen, lastSeen int64
	if err := s.R.QueryRowContext(ctx, `SELECT first_seen_at, last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, userID).Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if firstSeen != 0 || lastSeen != 0 {
		t.Fatalf("NoInitialSeen stored %d/%d, want 0/0", firstSeen, lastSeen)
	}
}

func TestEnsureImplicitDeviceOnExistingDevice(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_device_existing', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_implicit_device_existing', 'u', 'grp_implicit_device_existing', 1, x'01', x'02', 1)`)
	dev := AccessDevice{ID: "dev_implicit_device_existing", UserID: "usr_implicit_device_existing", Implicit: true, CreatedAt: time.Unix(100, 0)}
	firstCred := AccessCred{ID: "crd_implicit_device_existing_hysteria", UserID: dev.UserID, Protocol: "hysteria2", SecretEnc: []byte{1}, DataJSON: `{}`, CreatedAt: dev.CreatedAt}
	device, creds, created, err := s.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{firstCred})
	if err != nil || !created || device.ID != dev.ID || len(creds) != 1 {
		t.Fatalf("first EnsureImplicitDevice = %+v, %+v, %v, %v", device, creds, created, err)
	}
	secondCred := AccessCred{ID: "crd_implicit_device_existing_tuic", UserID: dev.UserID, Protocol: "tuic", SecretEnc: []byte{2}, DataJSON: `{}`, CreatedAt: dev.CreatedAt}
	device, creds, created, err = s.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{secondCred})
	if err != nil || !created || device.ID != dev.ID || len(creds) != 2 {
		t.Fatalf("EnsureImplicitDevice adding to existing device = %+v, %+v, %v, %v", device, creds, created, err)
	}
	device, creds, created, err = s.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{secondCred})
	if err != nil || created || device.ID != dev.ID || len(creds) != 2 {
		t.Fatalf("repeat EnsureImplicitDevice on existing device = %+v, %+v, %v, %v", device, creds, created, err)
	}
}

func TestEnsureImplicitAWGCredsSkipsFullProfile(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profileA, _ := awgNet(t, s, "implicit_full_a")
	profileB, _ := awgNet(t, s, "implicit_full_b")
	const userID = "usr_implicit_full_networks"
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_full_networks', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, 'u', 'grp_implicit_full_networks', 1, x'01', x'02', 1)`, userID)
	if idx, err := awgDevice(t, s, profileA, userID, "dev_implicit_full_a", 2); err != nil || idx != 2 {
		t.Fatalf("fill profile A network = %d, %v; want index 2", idx, err)
	}
	want := []AWGImplicitWant{
		{ProfileID: profileA, MaxIdx: 2, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{}, "", errors.New("issuer called for an exhausted profile")
		}},
		{ProfileID: profileB, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_implicit_full_b", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_implicit_full_b", nil
		}},
	}
	result, err := s.Access().EnsureImplicitAWGCreds(ctx, userID, AccessDevice{ID: "dev_implicit_full", UserID: userID, Implicit: true}, time.Unix(100, 0), want)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FullProfiles) != 1 || result.FullProfiles[0] != profileA {
		t.Fatalf("full profiles = %v, want %q", result.FullProfiles, profileA)
	}
	if len(result.Added) != 1 || result.Added[0].ProfileID != profileB || len(result.Creds) != 1 || result.Creds[0].ProfileID != profileB {
		t.Fatalf("added/live credentials = %+v / %+v, want profile B only", result.Added, result.Creds)
	}
}

func TestEnsureImplicitAWGCredsRetryReturnsCommittedCreds(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, _ := awgNet(t, s, "implicit_retry")
	const userID = "usr_implicit_retry"
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_retry', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, 'u', 'grp_implicit_retry', 1, x'01', x'02', 1)`, userID)
	now := time.Unix(100, 0)
	dev := AccessDevice{ID: "dev_implicit_retry", UserID: userID, Implicit: true}
	calls := 0
	want := []AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: func(idx int) (AccessCred, string, error) {
		calls++
		if calls == 1 {
			competingIdx, err := s.Access().AddAWGDevice(ctx, AWGDeviceAdd{
				Device: AccessDevice{ID: "dev_competing_retry", UserID: userID}, ProfileID: profile, MaxIdx: 8, Limit: 5,
			}, now, func(idx int) (AccessCred, string, error) {
				return AccessCred{ID: "crd_competing_retry", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_competing_retry", nil
			})
			if err != nil {
				return AccessCred{}, "", err
			}
			if competingIdx != idx {
				return AccessCred{}, "", fmt.Errorf("competing peer index = %d, want %d", competingIdx, idx)
			}
		}
		id := fmt.Sprintf("crd_implicit_retry_%d", calls)
		return AccessCred{ID: id, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_" + id, nil
	}}}

	result, err := s.Access().EnsureImplicitAWGCreds(ctx, userID, dev, now, want)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("issuer calls = %d, want 2 after the competing peer forces a retry", calls)
	}
	if result.Device.ID == "" || len(result.Added) != 1 || len(result.Creds) != 1 {
		t.Fatalf("returned result = %+v, want one added/live credential on the implicit device", result)
	}
	var implicitDeviceID, rowCredID string
	if err := s.R.QueryRowContext(ctx, `SELECT d.id, c.id FROM device d JOIN device_credential c ON c.device_id = d.id
		WHERE d.user_id = ? AND d.hwid_hash IS NULL AND d.revoked_at IS NULL AND c.profile_id = ? AND c.revoked_at IS NULL`, userID, profile).Scan(&implicitDeviceID, &rowCredID); err != nil {
		t.Fatal(err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential c JOIN device d ON d.id = c.device_id
		WHERE d.user_id = ? AND d.hwid_hash IS NULL AND d.revoked_at IS NULL AND c.profile_id = ? AND c.revoked_at IS NULL`, userID, profile); n != 1 {
		t.Fatalf("live implicit credentials for profile = %d, want one", n)
	}
	if result.Added[0].ID != rowCredID || result.Added[0].DeviceID != implicitDeviceID || result.Added[0].UserID != userID || result.Added[0].ProfileID != profile {
		t.Fatalf("added credential %+v does not match live database row %s on device %s", result.Added[0], rowCredID, implicitDeviceID)
	}
	for _, cred := range result.Creds {
		if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = ? AND revoked_at IS NULL`, cred.ID); n != 1 {
			t.Errorf("returned credential %q has %d live database rows", cred.ID, n)
		}
	}
}

func TestEnsureImplicitAWGCredsConcurrentSameUserCommitsBothProfiles(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profileA, _ := awgNet(t, s, "implicit_multi_a")
	profileB, _ := awgNet(t, s, "implicit_multi_b")
	const userID = "usr_implicit_multi"
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_multi', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, 'u', 'grp_implicit_multi', 1, x'01', x'02', 1)`, userID)
	dev := AccessDevice{ID: "dev_implicit_multi", UserID: userID, Implicit: true}
	now := time.Unix(100, 0)
	innerWant := []AWGImplicitWant{
		{ProfileID: profileA, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_implicit_multi_a", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_implicit_multi_a", nil
		}},
		{ProfileID: profileB, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_implicit_multi_b", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_implicit_multi_b", nil
		}},
	}
	var outerACalls int
	outerWant := []AWGImplicitWant{
		{ProfileID: profileA, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			outerACalls++
			if outerACalls == 1 {
				committed, err := s.Access().EnsureImplicitAWGCreds(ctx, userID, dev, now, innerWant)
				if err != nil {
					return AccessCred{}, "", err
				}
				if len(committed.Added) != 2 || len(committed.Creds) != 2 {
					return AccessCred{}, "", fmt.Errorf("concurrent EnsureImplicitAWGCreds returned %d additions and %d live credentials, want two of each", len(committed.Added), len(committed.Creds))
				}
			}
			return AccessCred{ID: fmt.Sprintf("crd_implicit_multi_outer_a_%d", outerACalls), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_implicit_multi_outer_a", nil
		}},
		{ProfileID: profileB, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_implicit_multi_outer_b", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_implicit_multi_outer_b", nil
		}},
	}

	result, err := s.Access().EnsureImplicitAWGCreds(ctx, userID, dev, now, outerWant)
	if err != nil {
		t.Fatal(err)
	}
	if outerACalls != 1 {
		t.Fatalf("outer profile A issuer calls = %d, want one before the concurrent commit", outerACalls)
	}
	if result.Device.ID != dev.ID || len(result.Added) != 0 || len(result.Creds) != 2 {
		t.Fatalf("outer call returned %+v after the concurrent call committed both profiles", result)
	}

	actual, live, err := s.Access().ImplicitDeviceCreds(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ID != dev.ID || len(live) != 2 {
		t.Fatalf("implicit device = %q with %d live credentials, want %q with two", actual.ID, len(live), dev.ID)
	}
	if !reflect.DeepEqual(result.Creds, live) {
		t.Fatalf("returned live credentials = %+v, database live credentials = %+v", result.Creds, live)
	}
	counts := map[string]int{}
	for _, cred := range live {
		counts[cred.ProfileID]++
	}
	if counts[profileA] != 1 || counts[profileB] != 1 {
		t.Fatalf("live credentials by profile = %v, want one each for %q and %q", counts, profileA, profileB)
	}
}

func TestEnsureImplicitAWGCredsReturnsFullLiveSetAfterWrite(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profileA, _ := awgNet(t, s, "implicit_live_a")
	profileB, _ := awgNet(t, s, "implicit_live_b")
	const userID = "usr_implicit_live_set"
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_implicit_live_set', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, 'u', 'grp_implicit_live_set', 1, x'01', x'02', 1)`, userID)
	now := time.Unix(100, 0)
	dev := AccessDevice{ID: "dev_implicit_live_set", UserID: userID, Implicit: true, CreatedAt: now}
	hysteria := AccessCred{ID: "crd_implicit_live_hysteria", DeviceID: dev.ID, UserID: userID, Protocol: "hysteria2", SecretEnc: []byte{1}, DataJSON: `{}`, CreatedAt: now}
	if err := s.Access().AddDevice(ctx, dev, []AccessCred{hysteria}); err != nil {
		t.Fatal(err)
	}

	innerWant := []AWGImplicitWant{{ProfileID: profileB, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_implicit_live_b", Protocol: "awg", SecretEnc: []byte{2}, DataJSON: `{}`}, "pub_implicit_live_b", nil
	}}}
	concurrentCalls := 0
	want := []AWGImplicitWant{{ProfileID: profileA, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
		if concurrentCalls == 0 {
			concurrentCalls++
			concurrent, err := s.Access().EnsureImplicitAWGCreds(ctx, userID, dev, now, innerWant)
			if err != nil {
				return AccessCred{}, "", err
			}
			if len(concurrent.Added) != 1 {
				return AccessCred{}, "", fmt.Errorf("concurrent profile write added %d credentials, want one", len(concurrent.Added))
			}
		}
		return AccessCred{ID: "crd_implicit_live_a", Protocol: "awg", SecretEnc: []byte{3}, DataJSON: `{}`}, "pub_implicit_live_a", nil
	}}}

	result, err := s.Access().EnsureImplicitAWGCreds(ctx, userID, dev, now, want)
	if err != nil {
		t.Fatal(err)
	}
	actual, live, err := s.Access().ImplicitDeviceCreds(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Device.ID != actual.ID || !reflect.DeepEqual(result.Creds, live) {
		t.Fatalf("returned device/credentials = %+v/%+v, database = %+v/%+v", result.Device, result.Creds, actual, live)
	}
	if len(result.Added) != 1 || result.Added[0].ProfileID != profileA {
		t.Fatalf("added credentials = %+v, want the current write for profile %q", result.Added, profileA)
	}
	protocols := map[string]int{}
	profiles := map[string]int{}
	for _, cred := range result.Creds {
		protocols[cred.Protocol]++
		profiles[cred.ProfileID]++
	}
	if protocols["hysteria2"] != 1 || profiles[profileA] != 1 || profiles[profileB] != 1 || len(result.Creds) != 3 {
		t.Fatalf("returned full set = %+v; want hysteria2 and AWG for profiles %q and %q", result.Creds, profileA, profileB)
	}
}

func TestAddAWGDeviceConcurrentAtDeviceLimit(t *testing.T) {
	s := openTemp(t)
	profile, _ := awgNet(t, s, "concurrent_limit")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_concurrent_limit', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_concurrent_limit', 'u', 'grp_concurrent_limit', 1, x'01', x'02', 1)`)

	type result struct {
		idx int
		err error
	}
	start := make(chan struct{})
	var issueBarrier, done sync.WaitGroup
	issueBarrier.Add(2)
	done.Add(2)
	results := make([]result, 2)
	for i := range results {
		go func(i int) {
			defer done.Done()
			<-start
			firstIssue := true
			results[i].idx, results[i].err = s.Access().AddAWGDevice(context.Background(), AWGDeviceAdd{
				Device:    AccessDevice{ID: fmt.Sprintf("dev_limit_%d", i), UserID: "usr_concurrent_limit"},
				ProfileID: profile, MaxIdx: 8, Limit: 1,
			}, time.Unix(100, 0), func(int) (AccessCred, string, error) {
				if firstIssue {
					firstIssue = false
					issueBarrier.Done()
					issueBarrier.Wait()
				}
				return AccessCred{ID: fmt.Sprintf("crd_limit_%d", i), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, fmt.Sprintf("pub_limit_%d", i), nil
			})
		}(i)
	}
	close(start)
	done.Wait()

	succeeded, limited := 0, 0
	for _, result := range results {
		if result.err == nil {
			succeeded++
			continue
		}
		var limitErr *AccessLimitError
		if errors.As(result.err, &limitErr) && limitErr.Used == 1 && limitErr.Limit == 1 {
			limited++
			continue
		}
		t.Fatalf("concurrent AddAWGDevice error = %v, want *AccessLimitError{Used: 1, Limit: 1}", result.err)
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent results: succeeded %d, limited %d; want one of each", succeeded, limited)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_concurrent_limit' AND revoked_at IS NULL`); n != 1 {
		t.Fatalf("live devices = %d, want 1", n)
	}
}

func TestAddAWGDeviceConcurrentUsersGetDistinctIndexes(t *testing.T) {
	s := openTemp(t)
	profile, _ := awgNet(t, s, "concurrent_users")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_concurrent_users', 'g', 1)`)
	for _, id := range []string{"usr_concurrent_a", "usr_concurrent_b"} {
		execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, ?, 'grp_concurrent_users', 1, ?, x'02', 1)`, id, id, []byte(id))
	}
	type result struct {
		idx int
		err error
	}
	start := make(chan struct{})
	var issueBarrier, done sync.WaitGroup
	issueBarrier.Add(2)
	done.Add(2)
	results := make([]result, 2)
	users := []string{"usr_concurrent_a", "usr_concurrent_b"}
	for i := range users {
		go func(i int) {
			defer done.Done()
			<-start
			firstIssue := true
			results[i].idx, results[i].err = s.Access().AddAWGDevice(context.Background(), AWGDeviceAdd{
				Device:    AccessDevice{ID: fmt.Sprintf("dev_user_%d", i), UserID: users[i]},
				ProfileID: profile, MaxIdx: 8, Limit: 1,
			}, time.Unix(100, 0), func(int) (AccessCred, string, error) {
				if firstIssue {
					firstIssue = false
					issueBarrier.Done()
					issueBarrier.Wait()
				}
				return AccessCred{ID: fmt.Sprintf("crd_user_%d", i), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, fmt.Sprintf("pub_user_%d", i), nil
			})
		}(i)
	}
	close(start)
	done.Wait()
	if results[0].err != nil || results[1].err != nil {
		t.Fatalf("concurrent AddAWGDevice errors = %v, %v", results[0].err, results[1].err)
	}
	if results[0].idx == results[1].idx {
		t.Fatalf("concurrent peer indexes = %d and %d, want distinct indexes", results[0].idx, results[1].idx)
	}
}

func TestAWGProbeAndDeviceRacingForIndexStayDistinct(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, inbound := awgNet(t, s, "probe_device_race")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_probe_device_race', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_probe_device_race', 'u', 'grp_probe_device_race', 1, x'01', x'02', 1)`)

	start := make(chan struct{})
	var issueBarrier, done sync.WaitGroup
	issueBarrier.Add(2)
	done.Add(2)
	type result struct {
		deviceIdx   int
		deviceErr   error
		probeErr    error
		deviceFirst int
		probeFirst  int
	}
	var got result
	now := time.Unix(100, 0)
	go func() {
		defer done.Done()
		<-start
		first := true
		got.deviceIdx, got.deviceErr = s.Access().AddAWGDevice(ctx, AWGDeviceAdd{
			Device:    AccessDevice{ID: "dev_probe_device_race", UserID: "usr_probe_device_race"},
			ProfileID: profile, MaxIdx: 8, Limit: 5,
		}, now, func(idx int) (AccessCred, string, error) {
			if first {
				first = false
				got.deviceFirst = idx
				issueBarrier.Done()
				issueBarrier.Wait()
			}
			return AccessCred{ID: "crd_probe_device_race", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_probe_device_race", nil
		})
	}()
	go func() {
		defer done.Done()
		<-start
		first := true
		got.probeErr = s.InsertProbeCredIdx(ctx, inbound, profile, 8, now, func(idx int) (ProbeCredRow, error) {
			if first {
				first = false
				got.probeFirst = idx
				issueBarrier.Done()
				issueBarrier.Wait()
			}
			return ProbeCredRow{CredID: "crd_probe_race", SecretEnc: []byte{1}, DataJSON: `{}`}, nil
		})
	}()
	close(start)
	done.Wait()
	if got.deviceErr != nil || got.probeErr != nil {
		t.Fatalf("racing device/probe errors = %v / %v", got.deviceErr, got.probeErr)
	}
	if got.deviceFirst != 2 || got.probeFirst != 2 {
		t.Fatalf("first candidate indexes were %d and %d, want both to read 2", got.deviceFirst, got.probeFirst)
	}
	probe, err := s.ProbeCred(ctx, inbound)
	if err != nil {
		t.Fatal(err)
	}
	if got.deviceIdx == probe.AWGIdx {
		t.Fatalf("device and probe both hold peer index %d", got.deviceIdx)
	}
}

func TestAddAWGDeviceReturnsSubnetFull(t *testing.T) {
	s := openTemp(t)
	profile, _ := awgNet(t, s, "subnet_full")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_subnet_full', 'g', 1)`)
	for _, id := range []string{"usr_subnet_full_a", "usr_subnet_full_b"} {
		execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, ?, 'grp_subnet_full', 1, ?, x'02', 1)`, id, id, []byte(id))
	}
	if idx, err := awgDevice(t, s, profile, "usr_subnet_full_a", "dev_subnet_full", 2); err != nil || idx != 2 {
		t.Fatalf("fill the network: index %d, error %v", idx, err)
	}
	_, err := s.Access().AddAWGDevice(context.Background(), AWGDeviceAdd{
		Device:    AccessDevice{ID: "dev_subnet_full_2", UserID: "usr_subnet_full_b"},
		ProfileID: profile, MaxIdx: 2, Limit: 1,
	}, time.Unix(100, 0), func(int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_subnet_full_2", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_subnet_full_2", nil
	})
	if !errors.Is(err, ErrAccessSubnetFull) {
		t.Fatalf("full network error = %v, want ErrAccessSubnetFull", err)
	}
}

func TestAddAWGDeviceRetriesAfterCriticalEpochChange(t *testing.T) {
	s := openTemp(t)
	profile, _ := awgNet(t, s, "epoch_retry")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_epoch_retry', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_epoch_retry', 'u', 'grp_epoch_retry', 1, x'01', x'02', 1)`)

	calls := 0
	idx, err := s.Access().AddAWGDevice(context.Background(), AWGDeviceAdd{
		Device:    AccessDevice{ID: "dev_epoch_retry", UserID: "usr_epoch_retry"},
		ProfileID: profile, MaxIdx: 8, Limit: 1,
	}, time.Unix(100, 0), func(int) (AccessCred, string, error) {
		calls++
		if calls == 1 {
			if _, err := s.W.ExecContext(context.Background(), `UPDATE profile SET critical_epoch = critical_epoch + 1 WHERE id = ?`, profile); err != nil {
				return AccessCred{}, "", err
			}
		}
		return AccessCred{ID: "crd_epoch_retry", Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_epoch_retry", nil
	})
	if err != nil || idx != 2 {
		t.Fatalf("AddAWGDevice after epoch retry = %d, %v; want index 2", idx, err)
	}
	if calls != 2 {
		t.Fatalf("issuer calls = %d, want 2", calls)
	}
	if epoch := countT(t, s, `SELECT config_epoch FROM device_credential WHERE id = 'crd_epoch_retry'`); epoch != 1 {
		t.Fatalf("stored config epoch = %d, want retried epoch 1", epoch)
	}
}

func TestAWGDeviceScopeAndConfigRecord(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, _ := awgNet(t, s, "scope")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_scope', 'g', 1)`)
	if err := s.Access().UpdateGroup(ctx, "grp_scope", nil, &[]string{profile}, nil, nil); err != nil {
		t.Fatal(err)
	}
	u := AccessUser{
		ID: "usr_scope", Name: "u", GroupID: "grp_scope", AllNodes: true, Status: "active", QuotaReset: "none", DeviceLimit: 1, AppAmnezia: true,
		SubTokenHash: []byte("scope-token"), SubTokenEnc: []byte{1}, CreatedAt: time.Unix(100, 0), PeriodStart: time.Unix(100, 0),
	}
	if err := s.Access().CreateUser(ctx, u, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := awgDevice(t, s, profile, u.ID, "dev_scope", 8); err != nil {
		t.Fatal(err)
	}

	scope, err := s.Access().AWGDeviceScope(ctx, "dev_scope")
	if err != nil {
		t.Fatal(err)
	}
	if scope.Device.ID != "dev_scope" || scope.User.ID != u.ID || scope.Group.ID != "grp_scope" || scope.Profile.ID != profile {
		t.Fatalf("scope identifiers = device %q user %q group %q profile %q", scope.Device.ID, scope.User.ID, scope.Group.ID, scope.Profile.ID)
	}
	if len(scope.Group.ProfileIDs) != 1 || scope.Group.ProfileIDs[0] != profile || len(scope.Inbounds) != 1 {
		t.Fatalf("group profiles = %v, inbounds = %d", scope.Group.ProfileIDs, len(scope.Inbounds))
	}

	sig := map[string]string{"nod_scope": "1.1.1.1|2606:4700:4700::1111"}
	epoch := int64(3)
	if err := s.Access().RecordDeviceConfig(ctx, scope.Device.CredID, &epoch, sig); err != nil {
		t.Fatal(err)
	}
	lowerEpoch := int64(2)
	if err := s.Access().RecordDeviceConfig(ctx, scope.Device.CredID, &lowerEpoch, nil); err != nil {
		t.Fatal(err)
	}
	device, err := s.Access().AWGDevice(ctx, "dev_scope")
	if err != nil {
		t.Fatal(err)
	}
	if device.ConfigEpoch != 3 || len(device.DNSSig) != 1 || device.DNSSig["nod_scope"] != sig["nod_scope"] {
		t.Fatalf("recorded device state = epoch %d DNS %v", device.ConfigEpoch, device.DNSSig)
	}
}

func TestUserByTokenHashReturnsSelectedNodes(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, _ = awgNet(t, s, "token_nodes")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_token_nodes', 'g', 1)`)
	u := AccessUser{
		ID: "usr_token_nodes", Name: "u", GroupID: "grp_token_nodes", Status: "active", QuotaReset: "none", DeviceLimit: 1, AppHapp: true,
		NodeIDs: []string{"nod_token_nodes"}, SubTokenHash: []byte("token-nodes"), SubTokenEnc: []byte{1},
		CreatedAt: time.Unix(100, 0), PeriodStart: time.Unix(100, 0),
	}
	if err := s.Access().CreateUser(ctx, u, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Access().UserByTokenHash(ctx, u.SubTokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID || len(got.NodeIDs) != 1 || got.NodeIDs[0] != "nod_token_nodes" {
		t.Fatalf("token user = %+v", got)
	}
}

// With the network full the probe gets no credential (an error, not an address that is already somebody's), and a probe
// that holds the last address leaves none for a device.
func TestAWGProbeCredentialNeedsAFreeAddress(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, inbound := awgNet(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	for i := range 3 { // indexes 2..4
		if _, err := awgDevice(t, s, profile, "usr_a", fmt.Sprintf("dev_%d", i), 4); err != nil {
			t.Fatal(err)
		}
	}
	issue := func(int) (ProbeCredRow, error) {
		return ProbeCredRow{CredID: "crd_p", SecretEnc: []byte{1}, DataJSON: `{}`}, nil
	}
	if err := s.InsertProbeCredIdx(ctx, inbound, profile, 4, time.Unix(100, 0), issue); !errors.Is(err, ErrAccessSubnetFull) {
		t.Fatalf("full network: %v", err)
	}
	if _, err := s.ProbeCred(ctx, inbound); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a row was made: %v", err)
	}
	// ...and one index more room: the probe takes it, the next device finds the network full
	if err := s.InsertProbeCredIdx(ctx, inbound, profile, 5, time.Unix(100, 0), issue); err != nil {
		t.Fatal(err)
	}
	if _, err := awgDevice(t, s, profile, "usr_a", "dev_9", 5); !errors.Is(err, ErrAccessSubnetFull) {
		t.Fatalf("the probe's address was handed out: %v", err)
	}
}
