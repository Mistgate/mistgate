//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
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
