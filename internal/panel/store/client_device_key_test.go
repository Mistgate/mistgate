//go:build !js

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func deviceHash(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func clientDeviceKeyFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	rewriteAccessGroup(t, s, "grp_link", "Link devices")
	rewriteAccessUser(t, s, "usr_alice", "alice", "grp_link")
	setLinkMode(t, s, LinkModeEnforce)
	return s
}

// enrolDevice is the request device of an enrolment: the HWID of the user, the hashes of a key and of a link.
func enrolDevice(user, tag string, hwid []byte) AccessDevice {
	d := linkDevice(user, tag, hwid)
	d.KeyHash, d.EnrolTokenHash = deviceHash("key-"+tag), deviceHash("token-"+tag)
	return d
}

func liveCredsOf(t *testing.T, s *Store, deviceID string) int {
	t.Helper()
	return countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, deviceID)
}

func TestSetLinkDeviceKeyHashGuards(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	rewriteAccessUser(t, s, "usr_bob", "bob", "grp_link")
	a, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "key_a", HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa")), 10, linkNow, linkCreds("key_a", "usr_alice"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "key_b", HWIDHash("usr_alice", "bbbbbbbbbbbbbbbb")), 10, linkNow, linkCreds("key_b", "usr_alice"))
	if err != nil {
		t.Fatal(err)
	}
	keyA, tokenA := deviceHash("key-a"), deviceHash("token-a")
	keyB, tokenB := deviceHash("key-b"), deviceHash("token-b")

	// A3: the user is part of the guard; another user's id changes nothing.
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_bob", a.Device.ID, nil, keyA, tokenA); err != nil || changed {
		t.Fatalf("a foreign user set the key = %v, %v", changed, err)
	}
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", a.Device.ID, nil, keyA, tokenA); err != nil || !changed {
		t.Fatalf("first key set = %v, %v", changed, err)
	}
	// The expected hash is compared too (nil = a first set).
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", a.Device.ID, nil, keyB, tokenB); err != nil || changed {
		t.Fatalf("a first set over a key = %v, %v", changed, err)
	}
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", a.Device.ID, deviceHash("wrong-expected"), keyB, tokenB); err != nil || changed {
		t.Fatalf("wrong expected hash changed the row: %v, %v", changed, err)
	}
	var gotKey, gotToken []byte
	if err := s.R.QueryRowContext(ctx, `SELECT key_hash, enrol_token_hash FROM device WHERE id = ?`, a.Device.ID).Scan(&gotKey, &gotToken); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotKey, keyA) || !bytes.Equal(gotToken, tokenA) {
		t.Fatalf("a refused set changed the stored hashes: %x / %x", gotKey, gotToken)
	}
	// Keys are unique over all rows.
	if _, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", b.Device.ID, nil, keyA, tokenB); !errors.Is(err, ErrAccessExists) {
		t.Fatalf("duplicate key hash = %v, want ErrAccessExists", err)
	}
	// A rotation: the new hash, the new link, the old hash gone.
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", a.Device.ID, keyA, keyB, tokenB); err != nil || !changed {
		t.Fatalf("guarded key rotation = %v, %v", changed, err)
	}
	if _, _, err := s.Access().LinkDeviceByKeyHash(ctx, keyA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old hash after rotation = %v, want ErrNotFound", err)
	}
	if device, _, err := s.Access().LinkDeviceByKeyHash(ctx, keyB); err != nil || device.ID != a.Device.ID || !bytes.Equal(device.EnrolTokenHash, tokenB) {
		t.Fatalf("new hash after rotation = %+v, %v", device, err)
	}
	// A revoked device cannot be given a key.
	if _, err := s.Access().RevokeDevice(ctx, b.Device.ID, linkNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", b.Device.ID, nil, deviceHash("key-c"), tokenB); err != nil || changed {
		t.Fatalf("a key on a revoked device = %v, %v", changed, err)
	}
}

// A1: the lookup tells a live key from the key of a removed or a blocked device and from a stranger's; the last two get no device.
func TestLinkDeviceByKeyHashTellsRemovedBlockedAndUnknown(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	mk := func(tag, hwid string) (LinkDeviceResult, []byte) {
		r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", tag, HWIDHash("usr_alice", hwid)), 10, linkNow, linkCreds(tag, "usr_alice"))
		if err != nil {
			t.Fatal(err)
		}
		key := deviceHash("key-" + tag)
		if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", r.Device.ID, nil, key, deviceHash("token")); err != nil || !changed {
			t.Fatalf("set key: %v, %v", changed, err)
		}
		return r, key
	}
	live, liveKey := mk("live", "aaaaaaaaaaaaaaaa")
	gone, goneKey := mk("gone", "bbbbbbbbbbbbbbbb")
	bad, badKey := mk("bad", "cccccccccccccccc")

	device, owner, err := s.Access().LinkDeviceByKeyHash(ctx, liveKey)
	if err != nil || device.ID != live.Device.ID || owner.UserID != "usr_alice" || len(owner.SubTokenHash) == 0 || len(device.KeyHash) != 32 || len(device.EnrolTokenHash) != 32 {
		t.Fatalf("live key lookup = device %+v owner %+v err %v", device, owner, err)
	}
	if _, err := s.Access().RevokeDevice(ctx, gone.Device.ID, linkNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if device, owner, err := s.Access().LinkDeviceByKeyHash(ctx, goneKey); !errors.Is(err, ErrDeviceRevoked) || device.ID != "" || len(device.KeyHash) != 0 || owner.UserID != "usr_alice" {
		t.Fatalf("removed device's key = device %+v owner %+v err %v; want ErrDeviceRevoked, no device", device, owner, err)
	}
	if _, err := s.Access().BlockDevice(ctx, bad.Device.ID, linkNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if device, owner, err := s.Access().LinkDeviceByKeyHash(ctx, badKey); !errors.Is(err, ErrAccessBlocked) || device.ID != "" || owner.UserID != "usr_alice" {
		t.Fatalf("blocked device's key = device %+v owner %+v err %v; want ErrAccessBlocked, no device", device, owner, err)
	}
	if device, owner, err := s.Access().LinkDeviceByKeyHash(ctx, deviceHash("nobody's key")); !errors.Is(err, ErrNotFound) || device.ID != "" || owner.UserID != "" {
		t.Fatalf("unknown key = device %+v owner %+v err %v; want ErrNotFound", device, owner, err)
	}
	// A key is never reusable: its hash stays on the removed row.
	if _, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", live.Device.ID, liveKey, goneKey, deviceHash("token")); !errors.Is(err, ErrAccessExists) {
		t.Fatalf("reuse of a removed device's key hash = %v, want ErrAccessExists", err)
	}
	// A foreign key hash (not a link device's) is no key.
	if _, _, err := s.Access().LinkDeviceByKeyHash(ctx, []byte("short")); err == nil {
		t.Fatal("a hash of the wrong length was looked up")
	}
}

// A1: the lookup of every request of a device must be an index search, not a scan of the device table.
func TestLinkDeviceByKeyHashUsesTheKeyIndex(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	rows, err := s.R.QueryContext(ctx, `EXPLAIN QUERY PLAN `+linkDeviceByKeyHashSQL, deviceHash("any key"))
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "SEARCH d USING INDEX device_key_hash") || strings.Contains(joined, "SCAN d") {
		t.Fatalf("the key lookup is not an index search:\n%s", joined)
	}
}

// A basic fetch whose snapshot is older than an enrolment must not be handed the enrolled device's credentials: the basic paths
// refuse the HWID of an enrolled device and write nothing on it (the review's probe, kept).
func TestBasicPathsRefuseTheHWIDOfAnEnrolledDevice(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	hwid := HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa")
	if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "enr", hwid), 5, linkNow, linkCreds("enr", "usr_alice")); err != nil {
		t.Fatal(err)
	}
	rows := func() int {
		return countT(t, s, `SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential) + (SELECT count(*) FROM awg_peer)`)
	}
	before := rows()
	// The usual issuer, the template id of a fetch.
	r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "basic", hwid), 5, linkNow, linkCreds("basic", "usr_alice"))
	if !errors.Is(err, ErrDeviceEnrolled) || r.Device.ID != "" || len(r.Creds) != 0 || len(r.Device.KeyHash) != 0 {
		t.Fatalf("EnsureLinkDevice on an enrolled HWID = %+v, %v; want ErrDeviceEnrolled and nothing of the device", r, err)
	}
	profile, _ := awgNet(t, s, "enrolled")
	before = rows()
	r, err = s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "ignored", hwid), 5, linkNow,
		[]AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: awgIssueFor("enrolled")}})
	if !errors.Is(err, ErrDeviceEnrolled) || r.Device.ID != "" || len(r.Creds) != 0 {
		t.Fatalf("EnsureLinkAWGCreds on an enrolled HWID = %+v, %v; want ErrDeviceEnrolled", r, err)
	}
	if after := rows(); after != before {
		t.Fatalf("rows %d -> %d: a refused basic path wrote something", before, after)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_peer`); n != 0 {
		t.Fatalf("%d AWG peers issued to an enrolled device", n)
	}
	// The same through the batch's guards, when the enrolment lands between the read and the write.
	s2 := clientDeviceKeyFixture(t)
	hwid2 := hwidOf("usr_alice", 2)
	if _, err := s2.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "keyless", hwid2), 5, linkNow, linkCreds("keyless", "usr_alice")); err != nil {
		t.Fatal(err)
	}
	profile2, _ := awgNet(t, s2, "late")
	raced := false
	r, err = s2.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "ignored", hwid2), 5, linkNow.Add(time.Minute),
		[]AWGImplicitWant{{ProfileID: profile2, MaxIdx: 8, Issue: func(idx int) (AccessCred, string, error) {
			if !raced {
				raced = true
				if _, err := s2.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "late", hwid2), 5, linkNow.Add(time.Minute), linkCreds("late", "usr_alice")); err != nil {
					return AccessCred{}, "", err
				}
			}
			return awgIssueFor("late")(idx)
		}}})
	if !errors.Is(err, ErrDeviceEnrolled) {
		t.Fatalf("EnsureLinkAWGCreds raced by an enrolment = %+v, %v; want ErrDeviceEnrolled", r, err)
	}
	if n := countT(t, s2, `SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 0 {
		t.Fatalf("%d live AWG peers on an enrolled device", n)
	}
}

func TestEnrolLinkDeviceBirthEvictionAndRefusals(t *testing.T) {
	ctx := context.Background()
	hwid := func(n int) []byte { return hwidOf("usr_alice", n) }

	t.Run("a new HWID gets a device with credentials and both hashes in one write, and no event", func(t *testing.T) {
		s := clientDeviceKeyFixture(t)
		dev := enrolDevice("usr_alice", "new", hwid(1))
		r, err := s.Access().EnrolLinkDevice(ctx, dev, 5, linkNow, linkCreds("new", "usr_alice"))
		if err != nil || r.Device.ID != "dev_new" || !r.Born || r.Replaced != "" || len(r.Added) != 2 || len(r.Creds) != 2 {
			t.Fatalf("enrol = %+v, %v", r, err)
		}
		if !bytes.Equal(r.Device.KeyHash, dev.KeyHash) || !bytes.Equal(r.Device.EnrolTokenHash, dev.EnrolTokenHash) {
			t.Fatalf("the result does not know the hashes: %x / %x", r.Device.KeyHash, r.Device.EnrolTokenHash)
		}
		if got, _, err := s.Access().LinkDeviceByKeyHash(ctx, dev.KeyHash); err != nil || got.ID != "dev_new" {
			t.Fatalf("lookup = %+v, %v", got, err)
		}
		if n := countT(t, s, `SELECT count(*) FROM event WHERE code = 'device_replaced'`); n != 0 {
			t.Fatalf("%d device_replaced events for a device nobody replaced", n)
		}
		// The device is never born without credentials, and nothing is written for it.
		_, err = s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "empty", hwid(2)), 5, linkNow,
			func(string, map[string]bool) ([]AccessCred, error) { return nil, nil })
		if !errors.Is(err, ErrNoLinkCredentials) {
			t.Fatalf("enrol with nothing to issue = %v, want ErrNoLinkCredentials", err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_empty'`); n != 0 {
			t.Fatalf("%d rows of a device born empty", n)
		}
		// Every other live device of the user is still counted, the new one too.
		if got := liveDevices(t, s, "usr_alice"); got != 1 {
			t.Fatalf("live devices = %d, want 1", got)
		}
	})

	t.Run("the shared device is never adopted", func(t *testing.T) {
		s := clientDeviceKeyFixture(t)
		shared, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "shared", nil), 5, linkNow, linkCreds("shared", "usr_alice"))
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "own", hwid(1)), 5, linkNow, linkCreds("own", "usr_alice"))
		if err != nil || r.Device.ID != "dev_own" || r.Device.ID == shared.Device.ID || !r.Born {
			t.Fatalf("enrol beside the shared device = %+v, %v", r, err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE id = ? AND revoked_at IS NULL AND hwid_hash IS NULL AND key_hash IS NULL`, shared.Device.ID); n != 1 {
			t.Fatal("the shared device was changed")
		}
		if liveCredsOf(t, s, shared.Device.ID) != 2 || liveCredsOf(t, s, "dev_own") != 2 {
			t.Fatal("the credentials of the shared device and the new one are not both live")
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_alice' AND revoked_at IS NULL`); n != 2 {
			t.Fatalf("live devices = %d, want the shared one and the new one", n)
		}
	})

	t.Run("a keyless device of this HWID keeps its row: credentials replaced, AWG peer quarantined, no event", func(t *testing.T) {
		s := clientDeviceKeyFixture(t)
		profile, _ := awgNet(t, s, "enrol")
		basic, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "basic", hwid(1)), 5, linkNow, linkCreds("basic", "usr_alice"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_alice", "ignored", hwid(1)), 5, linkNow,
			[]AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: awgIssueFor("basic")}}); err != nil {
			t.Fatal(err)
		}
		oldCreds := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, basic.Device.ID)
		if oldCreds != 3 || countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = 0`) != 1 {
			t.Fatalf("setup: %d live credentials, want 3 with one live peer", oldCreds)
		}
		dev := enrolDevice("usr_alice", "later", hwid(1)) // another id: the row is still the old one
		dev.Platform, dev.Model = "android", "Pixel 9"
		at := linkNow.Add(time.Hour)
		r, err := s.Access().EnrolLinkDevice(ctx, dev, 5, at, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
			if deviceID != basic.Device.ID || len(have) != 0 {
				t.Errorf("issue for %q with have %v; want the old row with nothing held", deviceID, have)
			}
			return linkCreds("fresh", "usr_alice")(deviceID, have)
		})
		if err != nil || r.Device.ID != basic.Device.ID || r.Born || r.Replaced != "" || len(r.Added) != 2 || len(r.Creds) != 2 {
			t.Fatalf("enrol over a keyless device = %+v, %v", r, err)
		}
		if !bytes.Equal(r.Device.KeyHash, dev.KeyHash) {
			t.Fatal("the result does not carry the new key hash")
		}
		for _, c := range r.Creds {
			if !strings.HasPrefix(c.ID, "crd_fresh_") {
				t.Fatalf("the result still holds an old credential %s", c.ID)
			}
		}
		if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, basic.Device.ID); n != 2 {
			t.Fatalf("live credentials = %d, want only the 2 new ones", n)
		}
		if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at = ?`, basic.Device.ID, at.Unix()); n != 3 {
			t.Fatalf("%d credentials revoked at the enrolment, want the 3 old ones", n)
		}
		if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = ?`, at.Unix()); n != 1 {
			t.Fatalf("%d peers quarantined at the enrolment, want 1", n)
		}
		if n := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 0 {
			t.Fatalf("%d AWG peers still live", n)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_alice'`); n != 1 {
			t.Fatalf("%d device rows, want the one", n)
		}
		if n := countT(t, s, `SELECT count(*) FROM event WHERE code = 'device_replaced'`); n != 0 {
			t.Fatalf("%d device_replaced events: nothing was evicted", n)
		}
		// What the enrolling client says about itself replaces what the basic fetch had recorded; what it leaves out stays.
		var platform, model, client string
		if err := s.R.QueryRowContext(ctx, `SELECT platform, model, client FROM device WHERE id = ?`, basic.Device.ID).Scan(&platform, &model, &client); err != nil ||
			platform != "android" || model != "Pixel 9" || client != "Happ/3.4.2" {
			t.Fatalf("device recorded as %q %q %q (%v)", platform, model, client, err)
		}
		if got, _, err := s.Access().LinkDeviceByKeyHash(ctx, dev.KeyHash); err != nil || got.ID != basic.Device.ID || !bytes.Equal(got.EnrolTokenHash, dev.EnrolTokenHash) {
			t.Fatalf("lookup = %+v, %v", got, err)
		}
	})

	t.Run("an enrolled device of this HWID is not enrolled again", func(t *testing.T) {
		s := clientDeviceKeyFixture(t)
		first := enrolDevice("usr_alice", "one", hwid(1))
		if _, err := s.Access().EnrolLinkDevice(ctx, first, 5, linkNow, linkCreds("one", "usr_alice")); err != nil {
			t.Fatal(err)
		}
		second := enrolDevice("usr_alice", "two", hwid(1))
		if _, err := s.Access().EnrolLinkDevice(ctx, second, 5, linkNow, linkCreds("two", "usr_alice")); !errors.Is(err, ErrAccessExists) {
			t.Fatalf("second enrol of one HWID = %v, want ErrAccessExists", err)
		}
		if _, _, err := s.Access().LinkDeviceByKeyHash(ctx, second.KeyHash); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the refused key is known: %v", err)
		}
		if got, _, err := s.Access().LinkDeviceByKeyHash(ctx, first.KeyHash); err != nil || got.ID != "dev_one" {
			t.Fatalf("the first key stopped working: %+v, %v", got, err)
		}
		if liveCredsOf(t, s, "dev_one") != 2 {
			t.Fatal("the refusal touched the credentials")
		}
	})

	t.Run("a blocked HWID, a user who is gone, the limit in ENFORCE and in TRACK", func(t *testing.T) {
		s := clientDeviceKeyFixture(t)
		gone, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "gone", hwid(7)), 5, linkNow, linkCreds("gone", "usr_alice"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Access().BlockDevice(ctx, gone.Device.ID, linkNow); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "blocked", hwid(7)), 5, linkNow, linkCreds("blocked", "usr_alice")); !errors.Is(err, ErrAccessBlocked) {
			t.Fatalf("enrol of a blocked HWID = %v, want ErrAccessBlocked", err)
		}
		if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_nobody", "x", hwidOf("usr_nobody", 1)), 5, linkNow, linkCreds("x", "usr_nobody")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("enrol of a missing user = %v, want ErrNotFound", err)
		}
		for n := 1; n <= 2; n++ {
			if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", fmt.Sprintf("d%d", n), hwid(n)), 2, linkNow, linkCreds(fmt.Sprintf("d%d", n), "usr_alice")); err != nil {
				t.Fatal(err)
			}
		}
		var limit *AccessLimitError
		if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "d3", hwid(3)), 2, linkNow, linkCreds("d3", "usr_alice")); !errors.As(err, &limit) || limit.Used != 2 || limit.Limit != 2 || limit.Mode != LinkModeEnforce {
			t.Fatalf("enrol at the limit = %v, want *AccessLimitError 2/2 in ENFORCE", err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_d3'`); n != 0 {
			t.Fatal("a refused enrolment left a row")
		}

		// TRACK: no shared place to fall back to, so a HWID is refused at twice the limit.
		setLinkMode(t, s, LinkModeTrack)
		for n := 3; n <= 4; n++ {
			if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", fmt.Sprintf("d%d", n), hwid(n)), 2, linkNow, linkCreds(fmt.Sprintf("d%d", n), "usr_alice")); err != nil {
				t.Fatalf("TRACK enrol %d: %v", n, err)
			}
		}
		if _, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "d5", hwid(5)), 2, linkNow, linkCreds("d5", "usr_alice")); !errors.As(err, &limit) || limit.Used != 4 || limit.Limit != 4 || limit.Mode != LinkModeTrack {
			t.Fatalf("TRACK enrol at twice the limit = %v, want *AccessLimitError 4/4", err)
		}
	})

	t.Run("at the limit a device silent for 30 days gives its place away, with the event", func(t *testing.T) {
		s := clientDeviceKeyFixture(t)
		old, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "old", hwid(1)), 2, linkNow, linkCreds("old", "usr_alice"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "busy", hwid(2)), 2, linkNow, linkCreds("busy", "usr_alice")); err != nil {
			t.Fatal(err)
		}
		silence(t, s, old.Device.ID, linkNow.Add(-LinkEvictAfter-time.Hour))
		r, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "new", hwid(3)), 2, linkNow, linkCreds("new", "usr_alice"))
		if err != nil || r.Device.ID != "dev_new" || r.Replaced != old.Device.ID {
			t.Fatalf("enrol in the place of a silent device = %+v, %v", r, err)
		}
		if n := countT(t, s, `SELECT count(*) FROM event WHERE code = 'device_replaced' AND user_id = 'usr_alice'`); n != 1 {
			t.Fatalf("device_replaced events = %d, want 1", n)
		}
		if n := liveDevices(t, s, "usr_alice"); n != 2 {
			t.Fatalf("live devices = %d, want 2", n)
		}
	})
}

// A4: the user is never over the limit when new HWIDs enrol in parallel.
func TestEnrolLinkDeviceNeverExceedsTheLimitInParallel(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	const limit, callers = 2, 6
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for n := 1; n <= callers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tag := fmt.Sprintf("p%d", n)
			_, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", tag, hwidOf("usr_alice", n)), limit, linkNow, linkCreds(tag, "usr_alice"))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, refused := 0, 0
	for err := range errs {
		var le *AccessLimitError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &le):
			refused++
		default:
			t.Fatalf("parallel enrol: %v", err)
		}
	}
	if ok != limit || refused != callers-limit {
		t.Fatalf("%d enrolled, %d refused; want %d and %d", ok, refused, limit, callers-limit)
	}
	if got := liveDevices(t, s, "usr_alice"); got != limit {
		t.Fatalf("live devices = %d, want %d", got, limit)
	}
}

// A4: the same HWID twice at once makes one device.
func TestEnrolLinkDeviceTwiceForOneHWIDMakesOneDevice(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	hwid := hwidOf("usr_alice", 1)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, tag := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", tag, hwid), 5, linkNow, linkCreds(tag, "usr_alice"))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, exists := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrAccessExists):
			exists++
		default:
			t.Fatalf("enrol: %v", err)
		}
	}
	if ok != 1 || exists != 1 {
		t.Fatalf("%d enrolled, %d refused as existing; want one each", ok, exists)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_alice' AND hwid_hash = ? AND revoked_at IS NULL AND key_hash IS NOT NULL`, hwid); n != 1 {
		t.Fatalf("live enrolled devices of the HWID = %d, want 1", n)
	}
}

// A4: on a keyless device the enrolment and the first key set compete for one row. The writer that wins between the other's read
// and its write is made explicit, so that only the guards of the batches decide (the unique index of the HWID decides nothing
// here: there is one row).
func TestEnrolLinkDeviceAndSetKeyHashRaceOnOneKeylessRow(t *testing.T) {
	ctx := context.Background()
	hwid := hwidOf("usr_alice", 1)
	setup := func(t *testing.T) (*Store, LinkDeviceResult) {
		s := clientDeviceKeyFixture(t)
		basic, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "basic", hwid), 5, linkNow, linkCreds("basic", "usr_alice"))
		if err != nil {
			t.Fatal(err)
		}
		return s, basic
	}

	t.Run("a key set between the enrolment's read and write wins", func(t *testing.T) {
		s, basic := setup(t)
		winner, winnerToken := deviceHash("winner"), deviceHash("winner-token")
		raced := false
		dev := enrolDevice("usr_alice", "late", hwid)
		_, err := s.Access().EnrolLinkDevice(ctx, dev, 5, linkNow.Add(time.Minute), func(deviceID string, have map[string]bool) ([]AccessCred, error) {
			if !raced {
				raced = true
				if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", basic.Device.ID, nil, winner, winnerToken); err != nil || !changed {
					return nil, fmt.Errorf("the competing key set = %v, %v", changed, err)
				}
			}
			return linkCreds("late", "usr_alice")(deviceID, have)
		})
		if !errors.Is(err, ErrAccessExists) {
			t.Fatalf("enrol that lost the race = %v, want ErrAccessExists", err)
		}
		var key []byte
		if err := s.R.QueryRowContext(ctx, `SELECT key_hash FROM device WHERE id = ?`, basic.Device.ID).Scan(&key); err != nil || !bytes.Equal(key, winner) {
			t.Fatalf("stored key %x (%v), want the winner's", key, err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL AND id LIKE 'crd_basic_%'`, basic.Device.ID); n != 2 {
			t.Fatalf("the loser changed the credentials: %d of the old ones live", n)
		}
		if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id LIKE 'crd_late_%'`); n != 0 {
			t.Fatalf("the loser's credentials were written: %d", n)
		}
	})

	t.Run("an enrolment that lands first leaves the key set nothing to change", func(t *testing.T) {
		s, basic := setup(t)
		dev := enrolDevice("usr_alice", "first", hwid)
		if _, err := s.Access().EnrolLinkDevice(ctx, dev, 5, linkNow.Add(time.Minute), linkCreds("first", "usr_alice")); err != nil {
			t.Fatal(err)
		}
		if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", basic.Device.ID, nil, deviceHash("late"), deviceHash("late-token")); err != nil || changed {
			t.Fatalf("a first set after the enrolment = %v, %v", changed, err)
		}
		if got, _, err := s.Access().LinkDeviceByKeyHash(ctx, dev.KeyHash); err != nil || got.ID != basic.Device.ID {
			t.Fatalf("the enrolled key = %+v, %v", got, err)
		}
	})

	t.Run("in parallel exactly one of them wins", func(t *testing.T) {
		s, basic := setup(t)
		const callers = 4
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for n := 0; n < callers; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				var ok bool
				if n%2 == 0 {
					_, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", fmt.Sprintf("e%d", n), hwid), 5, linkNow.Add(time.Minute), linkCreds(fmt.Sprintf("e%d", n), "usr_alice"))
					ok = err == nil
					if err != nil && !errors.Is(err, ErrAccessExists) {
						t.Errorf("enrol: %v", err)
					}
				} else {
					changed, err := s.Access().SetLinkDeviceKeyHash(ctx, "usr_alice", basic.Device.ID, nil, deviceHash(fmt.Sprintf("k%d", n)), deviceHash("t"))
					ok = changed
					if err != nil {
						t.Errorf("set: %v", err)
					}
				}
				if ok {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins != 1 {
			t.Fatalf("%d writers won, want 1", wins)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_alice' AND revoked_at IS NULL AND key_hash IS NOT NULL`); n != 1 {
			t.Fatalf("%d enrolled devices, want 1", n)
		}
	})
}

func TestClientDeviceKeyMigrationLeavesExistingDeviceUntouched(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t)
	dev, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "before_migration", HWIDHash("usr_alice", "aaaaaaaaaaaaaaaa")), 10, linkNow, linkCreds("before_migration", "usr_alice"))
	if err != nil {
		t.Fatal(err)
	}
	type deviceState struct {
		id, user, kind, platform, model, os, client string
		hwid                                        []byte
		first, last, created, used                  int64
		revoked, blocked                            sql.NullInt64
	}
	read := func() deviceState {
		var got deviceState
		if err := s.R.QueryRowContext(ctx, `SELECT id, user_id, kind, platform, model, os_version, client, hwid_hash, first_seen_at, last_seen_at, created_at, revoked_at, blocked_at, used_bytes
			FROM device WHERE id = ?`, dev.Device.ID).Scan(&got.id, &got.user, &got.kind, &got.platform, &got.model, &got.os, &got.client, &got.hwid, &got.first, &got.last, &got.created, &got.revoked, &got.blocked, &got.used); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := read()
	credsBefore := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ?`, dev.Device.ID)
	// The database goes back to the version before this migration by the Down part of every migration after it (a migration added
	// later is rolled back too), and migrates forward again.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 125); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if after := read(); !reflect.DeepEqual(after, before) {
		t.Fatalf("existing device changed across migration: before %+v, after %+v", before, after)
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = ?`, dev.Device.ID); got != credsBefore {
		t.Fatalf("existing credentials changed from %d to %d", credsBefore, got)
	}
	var keyHash, tokenHash []byte
	if err := s.R.QueryRowContext(ctx, `SELECT key_hash, enrol_token_hash FROM device WHERE id = ?`, dev.Device.ID).Scan(&keyHash, &tokenHash); err != nil {
		t.Fatal(err)
	}
	if keyHash != nil || tokenHash != nil {
		t.Fatalf("new hashes on a pre-existing device = %x / %x, want NULL", keyHash, tokenHash)
	}
}
