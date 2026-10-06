//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
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
	n, err := s.Access().EnsureImplicitAWGCreds(context.Background(), dev.UserID, dev, time.Unix(100, 0), want)
	if err != nil || n != 1 {
		t.Fatalf("EnsureImplicitAWGCreds = %d, %v; want one", n, err)
	}
	n, err = s.Access().EnsureImplicitAWGCreds(context.Background(), dev.UserID, dev, time.Unix(100, 0), []AWGImplicitWant{{
		ProfileID: profile, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{}, "", errors.New("issuer called for an existing peer")
		},
	}})
	if err != nil || n != 0 {
		t.Fatalf("repeat EnsureImplicitAWGCreds = %d, %v; want zero", n, err)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE profile_id = ? AND idx = 2 AND released_at = 0`, profile); n != 1 {
		t.Fatalf("live implicit peer at index 2 = %d, want 1", n)
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
	if err := s.Access().RecordDeviceConfig(ctx, scope.Device.CredID, 3, sig); err != nil {
		t.Fatal(err)
	}
	if err := s.Access().RecordDeviceConfig(ctx, scope.Device.CredID, 2, nil); err != nil {
		t.Fatal(err)
	}
	device, err := s.Access().AWGDevice(ctx, "dev_scope")
	if err != nil {
		t.Fatal(err)
	}
	if device.ConfigEpoch != 3 || len(device.DNSSig) != 0 {
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
