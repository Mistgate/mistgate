//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The two store calls of the client API's sync and leave: credentials issued to an enrolled device by its key, and its removal by
// the same key. Both are single guarded batches that re-check the key; the basic paths keep their key_hash IS NULL guards.

// enrolledDevice enrols a device of usr_alice (a hysteria2 and a vless credential) and returns it as the key lookup gives it.
func enrolledDevice(t *testing.T, s *Store, tag string, hwid []byte) AccessDevice {
	t.Helper()
	ctx := context.Background()
	// Every device is enrolled under the one current link of the person.
	d := enrolDevice("usr_alice", tag, hwid)
	d.EnrolTokenHash = deviceHash("current link")
	execT(t, s, `UPDATE user SET sub_token_hash = ? WHERE id = 'usr_alice'`, d.EnrolTokenHash)
	if _, err := s.Access().EnrolLinkDevice(ctx, d, 5, linkNow, linkCreds(tag, "usr_alice")); err != nil {
		t.Fatal(err)
	}
	dev, owner, err := s.Access().LinkDeviceByKeyHash(ctx, deviceHash("key-"+tag))
	if err != nil || owner.UserID != "usr_alice" {
		t.Fatalf("lookup of %s: %+v %+v %v", tag, dev, owner, err)
	}
	return dev
}

func wantAWG(profile, tag string) []AWGImplicitWant {
	return []AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: awgIssueFor(tag)}}
}

func totalRows(t *testing.T, s *Store) int {
	t.Helper()
	return countT(t, s, `SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential) + (SELECT count(*) FROM awg_peer)`)
}

func TestEnsureEnrolledLinkDeviceIssuesToTheKeysDevice(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	hwid := HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa")
	dev := enrolledDevice(t, s, "a", hwid)
	profile, _ := awgNet(t, s, "e1")
	devices := liveDevices(t, s, "usr_alice")

	// A protocol the device lacks and the AWG peer of a profile it has none for: one call, both.
	missing := func(string, map[string]bool) ([]AccessCred, error) { return nil, nil }
	r, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow.Add(time.Minute), missing, wantAWG(profile, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Device.ID != dev.ID || len(r.Added) != 1 || r.Added[0].Protocol != "awg" || r.Added[0].ProfileID != profile || r.Added[0].DeviceID != dev.ID || r.Born {
		t.Fatalf("result = %+v", r)
	}
	if n := len(r.Creds); n != 3 { // hysteria2, vless and the new peer
		t.Errorf("the device holds %d credentials, want 3", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = ? AND profile_id = ? AND released_at = 0`, r.Added[0].ID, profile); n != 1 {
		t.Errorf("%d peers for the new credential", n)
	}
	if liveDevices(t, s, "usr_alice") != devices {
		t.Error("a device was made or removed")
	}

	// A protocol the issuer adds comes in the same batch, tied to this device and user.
	r2, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow.Add(2*time.Minute), func(id string, have map[string]bool) ([]AccessCred, error) {
		if id != dev.ID || !have["hysteria2"] {
			t.Errorf("issuer called with %s %v", id, have)
		}
		if have["mtproto"] {
			return nil, nil
		}
		return []AccessCred{{ID: "crd_extra", UserID: "usr_alice", Protocol: "mtproto", SecretEnc: []byte("sealed"), DataJSON: `{}`}}, nil
	}, wantAWG(profile, "again"))
	if err != nil || len(r2.Added) != 1 || r2.Added[0].ID != "crd_extra" || len(r2.Creds) != 4 {
		t.Fatalf("second call = %+v, %v", r2, err)
	}

	// With nothing missing nothing is written, and the call still names the device.
	before := totalRows(t, s)
	r3, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow.Add(3*time.Minute), func(string, map[string]bool) ([]AccessCred, error) { return nil, nil }, wantAWG(profile, "third"))
	if err != nil || len(r3.Added) != 0 || r3.Device.ID != dev.ID || len(r3.Creds) != 4 || totalRows(t, s) != before {
		t.Fatalf("idempotent call = %+v, %v (rows %d -> %d)", r3, err, before, totalRows(t, s))
	}
	// The basic path still refuses this HWID, after the enrolled path filled it in.
	if r, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "basic", hwid), 5, linkNow, wantAWG(profile, "basic")); !errors.Is(err, ErrDeviceEnrolled) || r.Device.ID != "" {
		t.Fatalf("the basic path = %+v, %v; want ErrDeviceEnrolled", r, err)
	}
	if r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "basic", hwid), 5, linkNow, linkCreds("basic", "usr_alice")); !errors.Is(err, ErrDeviceEnrolled) || r.Device.ID != "" {
		t.Fatalf("the basic device path = %+v, %v; want ErrDeviceEnrolled", r, err)
	}
}

func TestEnsureEnrolledLinkDeviceRefusesAKeyThatIsNotTheDevices(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	dev := enrolledDevice(t, s, "a", HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa"))
	profile, _ := awgNet(t, s, "e2")
	none := func(string, map[string]bool) ([]AccessCred, error) { return nil, nil }

	// Input that is no enrolled device is refused before any read.
	for name, d := range map[string]AccessDevice{
		"no key":        {ID: dev.ID, UserID: dev.UserID, HWIDHash: dev.HWIDHash},
		"no hwid":       {ID: dev.ID, UserID: dev.UserID, KeyHash: dev.KeyHash},
		"no id":         {UserID: dev.UserID, HWIDHash: dev.HWIDHash, KeyHash: dev.KeyHash},
		"short key":     {ID: dev.ID, UserID: dev.UserID, HWIDHash: dev.HWIDHash, KeyHash: []byte{1}},
		"nobody's user": {ID: dev.ID, HWIDHash: dev.HWIDHash, KeyHash: dev.KeyHash},
	} {
		if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, d, linkNow, none, wantAWG(profile, "x")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow, nil, nil); err == nil {
		t.Error("a nil issuer was accepted")
	}

	before := totalRows(t, s)
	stale := dev
	stale.KeyHash = deviceHash("a key that was rotated away")
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, stale, linkNow, none, wantAWG(profile, "stale")); !errors.Is(err, ErrAccessExists) {
		t.Errorf("a stale key = %v, want ErrAccessExists", err)
	}
	other := dev
	other.ID = "dev_somebody_else"
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, other, linkNow, none, wantAWG(profile, "other")); !errors.Is(err, ErrAccessExists) {
		t.Errorf("another device's id = %v, want ErrAccessExists", err)
	}
	if totalRows(t, s) != before {
		t.Error("a refused call wrote rows")
	}

	// A link rotated since the enrolment: the device may not grow credentials until it enrols again with the new one.
	execT(t, s, `UPDATE user SET sub_token_hash = ? WHERE id = 'usr_alice'`, deviceHash("a rotated link"))
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow, none, wantAWG(profile, "rotated")); !errors.Is(err, ErrAccessExists) {
		t.Errorf("a device of a rotated link = %v, want ErrAccessExists", err)
	}
	if _, err := s.Access().SetLinkDeviceKeyHash(ctx, dev.UserID, dev.ID, dev.KeyHash, dev.KeyHash, deviceHash("a rotated link")); err != nil {
		t.Fatal(err) // enrolled again under the new link: whole again
	}
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow, none, wantAWG(profile, "again")); err != nil {
		t.Errorf("a device that enrolled again under the new link = %v", err)
	}
	execT(t, s, `DELETE FROM awg_peer`)
	execT(t, s, `DELETE FROM device_credential WHERE protocol = 'awg'`)
	// A device removed since the key lookup, and one blocked: nothing is issued to either.
	if _, err := s.Access().RevokeDevice(ctx, dev.ID, linkNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow, none, wantAWG(profile, "gone")); !errors.Is(err, ErrAccessExists) {
		t.Errorf("a removed device = %v, want ErrAccessExists", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer`); n != 0 {
		t.Errorf("%d peers issued to refused calls", n)
	}
}

// The key stops being the device's between the read and the write (a leave, a re-enrolment, an owner's block): the guard of the
// batch fails, the retry reads the new state, and nothing is issued to the stale request.
func TestEnsureEnrolledLinkDeviceKeyChangesUnderneath(t *testing.T) {
	ctx := context.Background()
	for name, race := range map[string]func(t *testing.T, s *Store, dev AccessDevice){
		"re-enrolment": func(t *testing.T, s *Store, dev AccessDevice) {
			if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, dev.UserID, dev.ID, dev.KeyHash, deviceHash("new key"), deviceHash("current link")); err != nil || !changed {
				t.Fatalf("rotate: %v %v", changed, err)
			}
		},
		"leave": func(t *testing.T, s *Store, dev AccessDevice) {
			if err := s.Access().RevokeEnrolledLinkDevice(ctx, dev, linkNow); err != nil {
				t.Fatal(err)
			}
		},
		"link rotation": func(t *testing.T, s *Store, dev AccessDevice) {
			execT(t, s, `UPDATE user SET sub_token_hash = ? WHERE id = ?`, deviceHash("a rotated link"), dev.UserID)
		},
		"block": func(t *testing.T, s *Store, dev AccessDevice) {
			if _, err := s.Access().BlockDevice(ctx, dev.ID, linkNow); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := clientDeviceKeyFixture(t)
			dev := enrolledDevice(t, s, "a", HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa"))
			profile, _ := awgNet(t, s, "e3")
			raced := false
			r, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow.Add(time.Minute), func(string, map[string]bool) ([]AccessCred, error) { return nil, nil },
				[]AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: func(idx int) (AccessCred, string, error) {
					if !raced {
						raced = true
						race(t, s, dev)
					}
					return awgIssueFor("late")(idx)
				}}})
			if !errors.Is(err, ErrAccessExists) || len(r.Added) != 0 {
				t.Fatalf("raced %s = %+v, %v; want ErrAccessExists", name, r, err)
			}
			if n := countT(t, s, `SELECT count(*) FROM awg_peer`); n != 0 {
				t.Errorf("%d peers issued to a stale key", n)
			}
			if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE protocol = 'awg'`); n != 0 {
				t.Errorf("%d AWG credentials issued to a stale key", n)
			}
		})
	}
}

// The profile network has no free address: the call succeeds without the peer and says which profile was full.
func TestEnsureEnrolledLinkDeviceReportsAFullNetwork(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	dev := enrolledDevice(t, s, "a", HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa"))
	profile, _ := awgNet(t, s, "e4")
	r, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow, func(string, map[string]bool) ([]AccessCred, error) { return nil, nil },
		[]AWGImplicitWant{{ProfileID: profile, MaxIdx: 0, Issue: awgIssueFor("full")}})
	if err != nil || len(r.FullProfiles) != 1 || r.FullProfiles[0] != profile || len(r.Added) != 0 {
		t.Fatalf("full network = %+v, %v", r, err)
	}
}

func TestRevokeEnrolledLinkDevice(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	rewriteAccessUser(t, s, "usr_bob", "bob", "grp_link")
	dev := enrolledDevice(t, s, "a", HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa"))
	keep := enrolledDevice(t, s, "keep", HWIDHash("usr_alice", "bbbbbbbbbbbbbbbb"))
	profile, _ := awgNet(t, s, "e5")
	none := func(string, map[string]bool) ([]AccessCred, error) { return nil, nil }
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow, none, wantAWG(profile, "a")); err != nil {
		t.Fatal(err)
	}
	peers := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = 0`)

	// A key that is not the device's, and a user that is not its owner: no-op.
	wrong := dev
	wrong.KeyHash = deviceHash("wrong")
	if err := s.Access().RevokeEnrolledLinkDevice(ctx, wrong, linkNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong key = %v, want ErrNotFound", err)
	}
	foreign := dev
	foreign.UserID = "usr_bob"
	if err := s.Access().RevokeEnrolledLinkDevice(ctx, foreign, linkNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign user = %v, want ErrNotFound", err)
	}
	if err := s.Access().RevokeEnrolledLinkDevice(ctx, AccessDevice{ID: dev.ID, UserID: dev.UserID}, linkNow); err == nil {
		t.Error("a device without a key hash was accepted")
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id = ? AND revoked_at IS NULL`, dev.ID); n != 1 {
		t.Fatal("a refused revocation removed the device")
	}

	if err := s.Access().RevokeEnrolledLinkDevice(ctx, dev, linkNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := liveCredsOf(t, s, dev.ID); n != 0 {
		t.Errorf("%d live credentials left", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != peers-1 {
		t.Errorf("%d live peers, want %d", n, peers-1)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = ?`, linkNow.Add(time.Minute).Unix()); n != 1 {
		t.Errorf("%d peers quarantined, want 1", n)
	}
	if liveCredsOf(t, s, keep.ID) != 2 {
		t.Error("another device lost credentials")
	}
	// The key now says removed, and a second revocation is a no-op.
	if _, _, err := s.Access().LinkDeviceByKeyHash(ctx, dev.KeyHash); !errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("lookup after the revocation = %v, want ErrDeviceRevoked", err)
	}
	if err := s.Access().RevokeEnrolledLinkDevice(ctx, dev, linkNow.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("second revocation = %v, want ErrNotFound", err)
	}
	// Nothing more is issued to the removed device.
	if _, err := s.Access().EnsureEnrolledLinkDevice(ctx, dev, linkNow.Add(3*time.Minute), none, wantAWG(profile, "late")); !errors.Is(err, ErrAccessExists) {
		t.Errorf("issue after the revocation = %v, want ErrAccessExists", err)
	}
}
