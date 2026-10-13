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

// The limit of link devices: the ceiling per mode, the refusals, the owner's block, the replacement of a silent device, the
// switch to STRICT, the sweep of old rows, the counts. As in link_device_test.go, a race is staged by letting the competing
// writer win between the read and the write of the attempt under test.

var silentSince = linkNow.Add(-LinkEvictAfter - time.Hour) // a last_seen_at older than the replacement window

func hwidOf(user string, n int) []byte {
	return HWIDHash(user, fmt.Sprintf("hwid-value-%02d-padding", n))
}

// awgIssueFor issues the peer of a Mihomo AmneziaWG proxy, with ids made from tag.
func awgIssueFor(tag string) AWGIssue {
	return func(idx int) (AccessCred, string, error) {
		return AccessCred{ID: "crd_awg_" + tag, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_" + tag, nil
	}
}

func ensureHWID(ctx context.Context, s *Store, user string, n, limit int) (LinkDeviceResult, error) {
	tag := fmt.Sprintf("h%02d", n)
	if user != "usr_l" {
		tag = user + "_" + tag
	}
	return s.Access().EnsureLinkDevice(ctx, linkDevice(user, tag, hwidOf(user, n)), limit, linkNow, linkCreds(tag, user))
}

func silence(t *testing.T, s *Store, deviceID string, at time.Time) {
	t.Helper()
	execT(t, s, `UPDATE device SET last_seen_at = ? WHERE id = ?`, at.Unix(), deviceID)
}

func TestLinkModeSetsTheCeiling(t *testing.T) {
	ctx := context.Background()
	t.Run("track counts: twice the limit for a HWID, none for the shared device", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		setLinkMode(t, s, LinkModeTrack)
		for n := 1; n <= 4; n++ {
			if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
				t.Fatalf("HWID %d under twice the limit: %v", n, err)
			}
		}
		var limit *AccessLimitError
		if _, err := ensureHWID(ctx, s, "usr_l", 5, 2); !errors.As(err, &limit) || limit.Used != 4 || limit.Limit != 4 || limit.Mode != LinkModeTrack {
			t.Fatalf("the fifth HWID = %v, want a limit error 4/4 in track", err)
		}
		if r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 2, linkNow, linkCreds("s", "usr_l")); err != nil || r.Device.ID != "dev_s" || !r.Born {
			t.Fatalf("the shared device over the ceiling = %+v, %v; want one made", r, err)
		}
	})

	t.Run("a panel without a mode counts only", func(t *testing.T) {
		s := openTemp(t)
		rewriteAccessGroup(t, s, "grp_link", "Link devices")
		rewriteAccessUser(t, s, "usr_l", "u-usr_l", "grp_link")
		for n := 1; n <= 4; n++ {
			if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
				t.Fatalf("HWID %d: %v", n, err)
			}
		}
		if LinkModeOf("") != LinkModeTrack || LinkModeOf("nonsense") != LinkModeTrack {
			t.Fatal("an unknown mode must read as track")
		}
	})

	t.Run("enforce holds both a HWID and the shared device to the limit, and an existing shared device always serves", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		if _, err := ensureHWID(ctx, s, "usr_l", 1, 2); err != nil {
			t.Fatal(err)
		}
		only := func(protocol string) LinkIssue {
			return func(deviceID string, have map[string]bool) ([]AccessCred, error) {
				if have[protocol] {
					return nil, nil
				}
				return []AccessCred{{ID: "crd_shared_" + protocol, UserID: "usr_l", Protocol: protocol, SecretEnc: []byte{1}, DataJSON: `{}`}}, nil
			}
		}
		if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 2, linkNow, only("hysteria2")); err != nil {
			t.Fatalf("the shared device under the limit: %v", err)
		}
		var limit *AccessLimitError
		if _, err := ensureHWID(ctx, s, "usr_l", 2, 2); !errors.As(err, &limit) || limit.Used != 2 || limit.Limit != 2 || limit.Mode != LinkModeEnforce {
			t.Fatalf("a new HWID at the limit = %v, want a limit error 2/2 in enforce", err)
		}
		// The shared device is there: it is filled in whatever the count.
		r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s2", nil), 2, linkNow, only("vless"))
		if err != nil || r.Device.ID != "dev_s" || len(r.Added) != 1 || r.Born {
			t.Fatalf("filling the existing shared device at the limit = %+v, %v", r, err)
		}
		if n := liveDevices(t, s, "usr_l"); n != 2 {
			t.Fatalf("live devices = %d, want 2", n)
		}
	})

	t.Run("enforce refuses to make the shared device at the limit", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		for n := 1; n <= 2; n++ {
			if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
				t.Fatal(err)
			}
		}
		var limit *AccessLimitError
		if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 2, linkNow, linkCreds("s", "usr_l")); !errors.As(err, &limit) || limit.Mode != LinkModeEnforce {
			t.Fatalf("the shared device at the limit = %v, want a limit error", err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l' AND hwid_hash IS NULL`); n != 0 {
			t.Fatalf("a shared device was made: %d", n)
		}
	})

	t.Run("limit 0 is no limit in every mode", func(t *testing.T) {
		for _, mode := range []string{LinkModeTrack, LinkModeEnforce, LinkModeStrict} {
			s := linkFixture(t, "usr_l")
			setLinkMode(t, s, mode)
			for n := 1; n <= 12; n++ {
				if _, err := ensureHWID(ctx, s, "usr_l", n, 0); err != nil {
					t.Fatalf("%s, HWID %d with no limit: %v", mode, n, err)
				}
			}
		}
	})

	t.Run("strict has no shared device but is enforce for a HWID", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		setLinkMode(t, s, LinkModeStrict)
		if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 5, linkNow, linkCreds("s", "usr_l")); !errors.Is(err, ErrNoSharedDevice) {
			t.Fatalf("the shared device in strict = %v, want ErrNoSharedDevice", err)
		}
		for n := 1; n <= 2; n++ {
			if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
				t.Fatal(err)
			}
		}
		var limit *AccessLimitError
		if _, err := ensureHWID(ctx, s, "usr_l", 3, 2); !errors.As(err, &limit) || limit.Mode != LinkModeStrict {
			t.Fatalf("a third HWID in strict = %v, want a limit error", err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l' AND hwid_hash IS NULL`); n != 0 {
			t.Fatalf("a shared device was made in strict: %d", n)
		}
	})
}

// Refusals read and write nothing, however many ask at once: eight HWIDs at the limit are all refused, no row is added.
func TestRefusalsAtTheLimitWriteNothing(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for n := 1; n <= 2; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
			t.Fatal(err)
		}
	}
	rows := func() [3]int {
		return [3]int{countT(t, s, `SELECT count(*) FROM device`), countT(t, s, `SELECT count(*) FROM device_credential`), countT(t, s, `SELECT count(*) FROM event`)}
	}
	before := rows()
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for n := 10; n < 18; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ensureHWID(ctx, s, "usr_l", n, 2)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		var limit *AccessLimitError
		if !errors.As(err, &limit) {
			t.Fatalf("a HWID at the limit = %v, want a limit error", err)
		}
	}
	if after := rows(); after != before {
		t.Fatalf("rows (devices, credentials, events) before %v, after %v: a refusal wrote", before, after)
	}
	// The same through the snapshot of a fetch: it already knows, and the store is not asked.
	data, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, hwidOf("usr_l", 99))
	if err != nil {
		t.Fatal(err)
	}
	var limit *AccessLimitError
	if err := data.LinkRefusal(2, true, linkNow); !errors.As(err, &limit) || limit.Used != 2 || limit.Mode != LinkModeEnforce {
		t.Fatalf("LinkRefusal = %v, want a limit error 2/..", err)
	}
	if err := data.LinkRefusal(3, true, linkNow); err != nil {
		t.Fatalf("LinkRefusal with room = %v", err)
	}
	if err := data.LinkRefusal(0, true, linkNow); err != nil {
		t.Fatalf("LinkRefusal with no limit = %v", err)
	}
}

// The first HWID of a person takes the shared device over and spends no place, even at the limit.
func TestLinkRefusalLetsTheFirstHWIDAdoptTheSharedDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "adopt")
	if _, err := awgDevice(t, s, profile, "usr_l", "dev_key", 8); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 2, linkNow, linkCreds("s", "usr_l")); err != nil {
		t.Fatal(err)
	}
	data, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, hwidOf("usr_l", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := data.LinkRefusal(2, true, linkNow); err != nil {
		t.Fatalf("a first HWID at the limit with a shared device = %v, want the store to adopt", err)
	}
	shared, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, nil)
	if err != nil || shared.Device.ID != "dev_s" {
		t.Fatalf("the snapshot of an app without a HWID holds %q, %v; want the shared device", shared.Device.ID, err)
	}
	if err := shared.LinkRefusal(2, false, linkNow); err != nil {
		t.Fatalf("an app without a HWID reads the shared device it has: %v", err)
	}
}

func TestBlockedHashIsNeverBornAgain(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	hash := hwidOf("usr_l", 1)
	if _, err := ensureHWID(ctx, s, "usr_l", 1, 5); err != nil {
		t.Fatal(err)
	}
	userID, err := s.Access().BlockDevice(ctx, "dev_h01", linkNow)
	if err != nil || userID != "usr_l" {
		t.Fatalf("BlockDevice = %q, %v", userID, err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = 'dev_h01' AND revoked_at IS NULL`); n != 0 {
		t.Fatalf("the blocked device holds %d live credentials", n)
	}
	before := countT(t, s, `SELECT count(*) FROM device`)
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "again", hash), 5, linkNow, linkCreds("again", "usr_l")); !errors.Is(err, ErrAccessBlocked) {
		t.Fatalf("the blocked HWID = %v, want ErrAccessBlocked", err)
	}
	if countT(t, s, `SELECT count(*) FROM device`) != before {
		t.Fatal("a blocked HWID made a device")
	}
	// The snapshot says it too, so a refused fetch costs no store call.
	data, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, hash)
	if err != nil || !data.Blocked {
		t.Fatalf("SubscriptionData.Blocked = %v, %v", data.Blocked, err)
	}
	if err := data.LinkRefusal(5, true, linkNow); !errors.Is(err, ErrAccessBlocked) {
		t.Fatalf("LinkRefusal = %v, want ErrAccessBlocked", err)
	}
	// A block of one HWID is not a block of another, and not of the shared device.
	if _, err := ensureHWID(ctx, s, "usr_l", 2, 5); err != nil {
		t.Fatalf("another HWID: %v", err)
	}
	// The block is the person's: the same HWID on another subscription hashes differently anyway.
	list, err := s.Access().BlockedDevices(ctx, "usr_l")
	if err != nil || len(list) != 1 || list[0].ID != "dev_h01" || len(list[0].Fingerprint) != 6 || list[0].Platform != "ios" || list[0].Client != "Happ/3.4.2" {
		t.Fatalf("BlockedDevices = %+v, %v", list, err)
	}
	if want := fmt.Sprintf("%x", hash[:3]); list[0].Fingerprint != want {
		t.Fatalf("fingerprint %q, want the first 6 hex of the hash %q", list[0].Fingerprint, want)
	}

	if _, err := s.Access().UnblockDevice(ctx, "dev_h01"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().UnblockDevice(ctx, "dev_h01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unblocking twice = %v, want ErrNotFound", err)
	}
	r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "back", hash), 5, linkNow, linkCreds("back", "usr_l"))
	if err != nil || r.Device.ID != "dev_back" || len(r.Added) != 2 {
		t.Fatalf("after the unblock = %+v, %v; want a new device with new credentials", r, err)
	}
	if list, _ := s.Access().BlockedDevices(ctx, "usr_l"); len(list) != 0 {
		t.Fatalf("BlockedDevices after the unblock = %+v", list)
	}
}

func TestBlockDeviceRefusesWhatHasNoHash(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "block")
	if _, err := awgDevice(t, s, profile, "usr_l", "dev_key", 8); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dev_key", "dev_s"} {
		if _, err := s.Access().BlockDevice(ctx, id, linkNow); !errors.Is(err, ErrCannotBlock) {
			t.Fatalf("BlockDevice(%s) = %v, want ErrCannotBlock", id, err)
		}
	}
	if _, err := s.Access().BlockDevice(ctx, "dev_nope", linkNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BlockDevice(unknown) = %v, want ErrNotFound", err)
	}
	if n := liveDevices(t, s, "usr_l"); n != 2 {
		t.Fatalf("live devices = %d: a refused block must revoke nothing", n)
	}
}

// The owner blocks the device after the attempt read it: the credentials are not written onto it and the HWID is not born again.
func TestBlockedDuringIssueFailsTheGuard(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	hash := hwidOf("usr_l", 1)
	only := func(protocol string) LinkIssue {
		return func(deviceID string, have map[string]bool) ([]AccessCred, error) {
			if have[protocol] {
				return nil, nil
			}
			return []AccessCred{{ID: "crd_blk_" + protocol, UserID: "usr_l", Protocol: protocol, SecretEnc: []byte{1}, DataJSON: `{}`}}, nil
		}
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "h01", hash), 5, linkNow, only("hysteria2")); err != nil {
		t.Fatal(err)
	}
	blocked := false
	_, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "h01", hash), 5, linkNow, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !blocked {
			blocked = true
			if _, err := s.Access().BlockDevice(ctx, "dev_h01", linkNow); err != nil {
				return nil, err
			}
		}
		return only("vless")(deviceID, have)
	})
	if !errors.Is(err, ErrAccessBlocked) {
		t.Fatalf("filling a device blocked after the read = %v, want ErrAccessBlocked", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = 'dev_h01' AND protocol = 'vless'`); n != 0 {
		t.Fatalf("a credential was written onto the blocked device (%d)", n)
	}

	// The same for a HWID that had no device: the block lands between its read and its write.
	hash2 := hwidOf("usr_l", 2)
	if _, err := ensureHWID(ctx, s, "usr_l", 2, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().RevokeDevice(ctx, "dev_h02", linkNow); err != nil { // gone, so the next fetch has to make it
		t.Fatal(err)
	}
	execT(t, s, `INSERT INTO device (id, user_id, hwid_hash, kind, first_seen_at, last_seen_at, created_at, revoked_at, blocked_at) VALUES ('dev_blocker', 'usr_l', ?, 'link', 1, 1, 1, 1, NULL)`, hash2)
	late := false
	_, err = s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "h02b", hash2), 5, linkNow, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !late {
			late = true
			execT(t, s, `UPDATE device SET blocked_at = 2 WHERE id = 'dev_blocker'`)
		}
		return linkCreds("h02b", "usr_l")(deviceID, have)
	})
	if !errors.Is(err, ErrAccessBlocked) {
		t.Fatalf("a new device blocked after the read = %v, want ErrAccessBlocked", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_h02b'`); n != 0 {
		t.Fatal("the blocked HWID was born")
	}
}

func TestEnsureLinkAWGCredsHonoursBlockAndLimit(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "awgblock")
	want := func(tag string) []AWGImplicitWant {
		return []AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: awgIssueFor(tag)}}
	}
	hash := hwidOf("usr_l", 1)
	first, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "k1", hash), 5, linkNow, want("k1"))
	if err != nil || first.Device.ID == "" {
		t.Fatalf("the first peer = %+v, %v", first, err)
	}
	if _, err := s.Access().BlockDevice(ctx, first.Device.ID, linkNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "k1b", hash), 5, linkNow, want("k1b")); !errors.Is(err, ErrAccessBlocked) {
		t.Fatalf("a blocked HWID asking for a peer = %v, want ErrAccessBlocked", err)
	}
	for n := 2; n <= 3; n++ {
		tag := fmt.Sprintf("k%d", n)
		if _, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", tag, hwidOf("usr_l", n)), 2, linkNow, want(tag)); err != nil {
			t.Fatalf("HWID %d: %v", n, err)
		}
	}
	var limit *AccessLimitError
	if _, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "k4", hwidOf("usr_l", 4)), 2, linkNow, want("k4")); !errors.As(err, &limit) || limit.Mode != LinkModeEnforce {
		t.Fatalf("a third device asking for a peer at the limit = %v, want a limit error", err)
	}
}

func TestSilentDeviceGivesItsPlaceToANewHWID(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for n := 1; n <= 3; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 3); err != nil {
			t.Fatal(err)
		}
	}
	silence(t, s, "dev_h01", silentSince.Add(-48*time.Hour))
	silence(t, s, "dev_h02", silentSince) // silent too, but the other one is older
	r, err := ensureHWID(ctx, s, "usr_l", 4, 3)
	if err != nil || r.Device.ID != "dev_h04" || r.Replaced != "dev_h01" || !r.Born || len(r.Added) != 2 {
		t.Fatalf("a new HWID at the limit with silent devices = %+v, %v; want dev_h04 in the place of dev_h01", r, err)
	}
	if n := liveDevices(t, s, "usr_l"); n != 3 {
		t.Fatalf("live devices = %d, want 3", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_h01' AND revoked_at IS NOT NULL AND blocked_at IS NULL`); n != 1 {
		t.Fatal("the oldest silent device was not revoked (or was blocked)")
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = 'dev_h01' AND revoked_at IS NULL`); n != 0 {
		t.Fatalf("the replaced device keeps %d live credentials", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_h02' AND revoked_at IS NULL`); n != 1 {
		t.Fatal("a second silent device was revoked for one new HWID")
	}
	var params string
	if err := s.R.QueryRowContext(ctx, `SELECT params_json FROM event WHERE code = 'device_replaced' AND user_id = 'usr_l' AND device_id = 'dev_h04'`).Scan(&params); err != nil {
		t.Fatalf("the device_replaced event: %v", err)
	}
	if want := `{"last_seen":"` + fmt.Sprint(silentSince.Add(-48*time.Hour).Unix()) + `","model":"","old_device":"dev_h01","platform":"ios"}`; params != want {
		t.Fatalf("event params %s, want %s", params, want)
	}
}

func TestOnlySilentLinkDevicesGivePlacesAndNeverInTrack(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "evict")
	if _, err := awgDevice(t, s, profile, "usr_l", "dev_key", 8); err != nil {
		t.Fatal(err)
	}
	silence(t, s, "dev_key", silentSince.Add(-24*365*time.Hour)) // a key silent for a year
	if _, err := ensureHWID(ctx, s, "usr_l", 1, 2); err != nil {
		t.Fatal(err)
	}
	var limit *AccessLimitError
	// A key is never a place to give away, and a link device silent for less than the window is not one either.
	silence(t, s, "dev_h01", linkNow.Add(-LinkEvictAfter+time.Hour))
	if _, err := ensureHWID(ctx, s, "usr_l", 2, 2); !errors.As(err, &limit) {
		t.Fatalf("at the limit with a silent key and a link device silent for 29 days = %v, want a limit error", err)
	}
	// The shared device may be made only under the limit, and takes no place from anyone.
	silence(t, s, "dev_h01", silentSince)
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 2, linkNow, linkCreds("s", "usr_l")); !errors.As(err, &limit) {
		t.Fatalf("the shared device at the limit with a silent device = %v, want a limit error", err)
	}
	// TRACK has its ceiling instead: twice the limit, and a silent device is no help there.
	setLinkMode(t, s, LinkModeTrack)
	for n := 2; n <= 3; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
			t.Fatalf("HWID %d in track under twice the limit: %v", n, err)
		}
	}
	if _, err := ensureHWID(ctx, s, "usr_l", 4, 2); !errors.As(err, &limit) || limit.Mode != LinkModeTrack {
		t.Fatalf("a new HWID in track at twice the limit = %v, want a limit error", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l' AND revoked_at IS NOT NULL`); n != 0 {
		t.Fatalf("%d devices were revoked without a replacement", n)
	}
	// STRICT replaces too.
	setLinkMode(t, s, LinkModeStrict)
	if r, err := ensureHWID(ctx, s, "usr_l", 5, 2); err != nil || r.Replaced != "dev_h01" {
		t.Fatalf("a new HWID in strict = %+v, %v; want the place of dev_h01", r, err)
	}
}

// Two new HWIDs find the same silent device and both try to take its place: the one that is late finds it gone and, at the
// limit again, is refused.
func TestTwoEvictionsOfOneVictimMakeOneDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for n := 1; n <= 2; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
			t.Fatal(err)
		}
	}
	silence(t, s, "dev_h01", silentSince)
	racing := false
	var inner error
	var innerRes LinkDeviceResult
	issueX := func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !racing {
			racing = true
			innerRes, inner = ensureHWID(ctx, s, "usr_l", 6, 2)
		}
		return linkCreds("h05", "usr_l")(deviceID, have)
	}
	_, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "h05", hwidOf("usr_l", 5)), 2, linkNow, issueX)
	var limit *AccessLimitError
	if inner != nil || innerRes.Replaced != "dev_h01" || !errors.As(err, &limit) || limit.Used != 2 {
		t.Fatalf("the early writer got %+v, %v; the late one %v; want the early one in the place and the late one refused", innerRes, inner, err)
	}
	if n := liveDevices(t, s, "usr_l"); n != 2 {
		t.Fatalf("live devices = %d, want 2", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM event WHERE code = 'device_replaced'`); n != 1 {
		t.Fatalf("device_replaced events = %d, want 1", n)
	}

	// With real concurrency: one silent device and six new HWIDs. One takes the place, five are refused.
	s2 := linkFixture(t, "usr_l")
	for n := 1; n <= 2; n++ {
		if _, err := ensureHWID(ctx, s2, "usr_l", n, 2); err != nil {
			t.Fatal(err)
		}
	}
	silence(t, s2, "dev_h01", silentSince)
	start := make(chan struct{})
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for n := 10; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ensureHWID(ctx, s2, "usr_l", n, 2)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	made, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			made++
		case errors.As(err, &limit):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if made != 1 || refused != 5 || liveDevices(t, s2, "usr_l") != 2 || countT(t, s2, `SELECT count(*) FROM event WHERE code = 'device_replaced'`) != 1 {
		t.Fatalf("made %d, refused %d, live %d; want 1, 5, 2", made, refused, liveDevices(t, s2, "usr_l"))
	}
}

// A silent device that was used again after the attempt read it is no longer a place to give away.
func TestVictimUsedAgainAfterTheReadKeepsItsPlace(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for n := 1; n <= 2; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 2); err != nil {
			t.Fatal(err)
		}
	}
	silence(t, s, "dev_h01", silentSince)
	touched := false
	_, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "h03", hwidOf("usr_l", 3)), 2, linkNow, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !touched {
			touched = true
			if err := s.Access().TouchDevice(ctx, "dev_h01", linkNow, linkNow.Add(time.Second), "", ""); err != nil {
				return nil, err
			}
		}
		return linkCreds("h03", "usr_l")(deviceID, have)
	})
	var limit *AccessLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("a new HWID whose victim was used meanwhile = %v, want a limit error", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_h01' AND revoked_at IS NULL`); n != 1 {
		t.Fatal("the device that was used again was revoked")
	}
}

// The switch to STRICT and a fetch that decided on the old mode: the fetch fails the guard and repeats on the new one.
func TestStrictSwitchDuringAFetch(t *testing.T) {
	ctx := context.Background()
	t.Run("the shared device is not made after the switch", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		switched := false
		_, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 5, linkNow, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
			if !switched {
				switched = true
				if _, err := s.SaveSubscriptionSettings(ctx, nil, LinkModeStrict, linkNow); err != nil {
					return nil, err
				}
			}
			return linkCreds("s", "usr_l")(deviceID, have)
		})
		if !errors.Is(err, ErrNoSharedDevice) {
			t.Fatalf("the shared device made while the mode switched = %v, want ErrNoSharedDevice", err)
		}
		if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l'`); n != 0 {
			t.Fatalf("%d devices exist after the refusal", n)
		}
	})

	t.Run("a HWID adopting the shared device that the switch revoked gets a device of its own", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l")); err != nil {
			t.Fatal(err)
		}
		switched := false
		r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "h01", hwidOf("usr_l", 1)), 5, linkNow, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
			if !switched {
				switched = true
				if n, err := s.SaveSubscriptionSettings(ctx, nil, LinkModeStrict, linkNow); err != nil || n != 1 {
					return nil, fmt.Errorf("the switch revoked %d people's shared device: %v", n, err)
				}
			}
			return linkCreds("h01", "usr_l")(deviceID, have)
		})
		if err != nil || r.Device.ID != "dev_h01" || !r.Born || len(r.Added) != 2 {
			t.Fatalf("the HWID after the switch = %+v, %v; want a new device with both credentials", r, err)
		}
	})

	t.Run("the switch revokes every shared device, with the credentials and the addresses of its peers", func(t *testing.T) {
		s := linkFixture(t, "usr_l")
		rewriteAccessUser(t, s, "usr_m", "u-usr_m", "grp_link")
		profile, _ := awgNet(t, s, "strict")
		if _, err := ensureHWID(ctx, s, "usr_l", 1, 5); err != nil { // a device of an app that reports an ID stays (made first: a HWID adopts a shared device)
			t.Fatal(err)
		}
		for _, user := range []string{"usr_l", "usr_m"} {
			if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice(user, "s"+user, nil), 0, linkNow, linkCreds("s"+user, user)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "ignored", nil), 0, linkNow,
			[]AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: awgIssueFor("sa")}}); err != nil {
			t.Fatal(err)
		}
		if n, err := s.Access().SharedLinkUsers(ctx); err != nil || n != 2 {
			t.Fatalf("SharedLinkUsers = %d, %v; want 2", n, err)
		}
		n, err := s.SaveSubscriptionSettings(ctx, map[string]string{"subscription_settings": "{}"}, LinkModeStrict, linkNow)
		if err != nil || n != 2 {
			t.Fatalf("the switch revoked for %d people, %v; want 2", n, err)
		}
		if got := countT(t, s, `SELECT count(*) FROM device WHERE hwid_hash IS NULL AND revoked_at IS NULL`); got != 0 {
			t.Fatalf("%d shared devices live after the switch", got)
		}
		if got := countT(t, s, `SELECT count(*) FROM device_credential c JOIN device d ON d.id = c.device_id WHERE d.hwid_hash IS NULL AND c.revoked_at IS NULL`); got != 0 {
			t.Fatalf("%d credentials of shared devices live after the switch", got)
		}
		if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at = 0`); got != 0 {
			t.Fatalf("%d AmneziaWG peers of shared devices keep their address", got)
		}
		if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE released_at > 0`); got != 1 {
			t.Fatalf("%d peers in quarantine, want 1", got)
		}
		if got := liveDevices(t, s, "usr_l"); got != 1 {
			t.Fatalf("live devices of usr_l = %d, want the one with a HWID", got)
		}
		if v, err := s.Setting(ctx, SettingLinkDeviceLimit); err != nil || v != LinkModeStrict {
			t.Fatalf("stored mode %q, %v", v, err)
		}
		if v, err := s.Setting(ctx, "subscription_settings"); err != nil || v != "{}" {
			t.Fatalf("the document was stored as %q, %v", v, err)
		}
		// Back to ENFORCE: nothing comes back, and an unset mode leaves the stored one alone.
		if n, err := s.SaveSubscriptionSettings(ctx, nil, LinkModeEnforce, linkNow.Add(time.Minute)); err != nil || n != 0 {
			t.Fatalf("switch to enforce revoked %d, %v", n, err)
		}
		if _, err := s.SaveSubscriptionSettings(ctx, nil, "", linkNow.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if v, _ := s.Setting(ctx, SettingLinkDeviceLimit); v != LinkModeEnforce {
			t.Fatalf("stored mode %q, want enforce", v)
		}
	})
}

func TestPruneRevokedDevices(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "prune")
	for n := 1; n <= 4; n++ {
		if _, err := ensureHWID(ctx, s, "usr_l", n, 10); err != nil {
			t.Fatal(err)
		}
	}
	kk, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "kk", hwidOf("usr_l", 5)), 10, linkNow, []AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: awgIssueFor("kk")}})
	if err != nil || kk.Device.ID == "" {
		t.Fatalf("the peer of the Mihomo device: %+v, %v", kk, err)
	}
	long, recent := linkNow.Add(-91*24*time.Hour), linkNow.Add(-89*24*time.Hour)
	for id, at := range map[string]time.Time{"dev_h01": long, "dev_h02": recent, kk.Device.ID: long} {
		if _, err := s.Access().RevokeDevice(ctx, id, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Access().BlockDevice(ctx, "dev_h03", long); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneRevokedDevices(ctx, linkNow.Add(-90*24*time.Hour), 1) // batches of one: the loop has to continue
	if err != nil || n != 2 {
		t.Fatalf("PruneRevokedDevices = %d, %v; want dev_h01 and dev_kk", n, err)
	}
	for id, want := range map[string]int{"dev_h01": 0, kk.Device.ID: 0, "dev_h02": 1, "dev_h03": 1, "dev_h04": 1} {
		if got := countT(t, s, `SELECT count(*) FROM device WHERE id = ?`, id); got != want {
			t.Errorf("rows of %s = %d, want %d", id, got, want)
		}
	}
	if got := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id IN (?, 'dev_h01')`, kk.Device.ID); got != 0 {
		t.Errorf("%d credentials outlive their pruned devices", got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE credential_id = 'crd_awg_kk'`); got != 0 {
		t.Errorf("%d peers outlive their pruned device", got)
	}
	// A device that is revoked but still holds a live credential is skipped (a revoke never leaves one).
	execT(t, s, `UPDATE device SET revoked_at = ? WHERE id = 'dev_h04'`, long.Unix())
	if n, err := s.PruneRevokedDevices(ctx, linkNow.Add(-90*24*time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("PruneRevokedDevices over a device with a live credential = %d, %v; want 0", n, err)
	}
}

func TestOverDeviceLimitCountsAndFilters(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for _, u := range []string{"usr_over", "usr_full", "usr_none"} {
		rewriteAccessUser(t, s, u, "u-"+u, "grp_link")
	}
	execT(t, s, `UPDATE user SET device_limit = CASE id WHEN 'usr_over' THEN 2 WHEN 'usr_full' THEN 2 ELSE 1 END`)
	execT(t, s, `UPDATE user SET device_limit = 1 WHERE id = 'usr_l'`)
	setLinkMode(t, s, LinkModeTrack)
	for n := 1; n <= 3; n++ { // usr_over: 3 devices of a limit of 2 (TRACK lets the ceiling of 4 pass)
		if _, err := ensureHWID(ctx, s, "usr_over", n, 2); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 2; n++ {
		if _, err := ensureHWID(ctx, s, "usr_full", n, 2); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 3; n++ { // a limit of 0 is no limit
		if _, err := ensureHWID(ctx, s, "usr_l", n, 5); err != nil {
			t.Fatal(err)
		}
	}
	execT(t, s, `PRAGMA ignore_check_constraints = ON`)
	execT(t, s, `UPDATE user SET device_limit = 0 WHERE id = 'usr_l'`)
	list := func(filter string) ([]AccessUser, AccessUserCounts) {
		users, _, counts, err := s.Access().ListUsers(ctx, AccessUserQuery{Filter: filter, Limit: 50, Now: linkNow, DeviceOnlineAfter: linkNow})
		if err != nil {
			t.Fatal(err)
		}
		return users, counts
	}
	_, counts := list("")
	if counts.OverDeviceLimit != 1 || counts.DevicesUsed != 8 {
		t.Fatalf("counts = %+v, want one person over the limit and 8 devices", counts)
	}
	users, _ := list("over_device_limit")
	if len(users) != 1 || users[0].ID != "usr_over" {
		t.Fatalf("the over_device_limit filter listed %+v, want usr_over only", users)
	}
	// The count ignores the filter, and a name search narrows it like the other badges.
	if _, counts := list("over_quota"); counts.OverDeviceLimit != 1 {
		t.Fatalf("the badge changed with another filter: %+v", counts)
	}
	_, byName, counts2, err := s.Access().ListUsers(ctx, AccessUserQuery{Query: "usr_full", Limit: 50, Now: linkNow, DeviceOnlineAfter: linkNow})
	if err != nil || byName || counts2.OverDeviceLimit != 0 {
		t.Fatalf("counts for a search = %+v, %v", counts2, err)
	}
	// The sort by devices still works next to the filter.
	if users, _, _, err := s.Access().ListUsers(ctx, AccessUserQuery{Filter: "over_device_limit", Sort: AccessUserSortDevices, Limit: 50, Now: linkNow, DeviceOnlineAfter: linkNow}); err != nil || len(users) != 1 {
		t.Fatalf("filter with sort = %+v, %v", users, err)
	}
}

func TestRenameDeviceNamesEveryDeviceButTheSharedOne(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	if _, err := ensureHWID(ctx, s, "usr_l", 1, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.Access().RenameDevice(ctx, "dev_h01", "Anna's phone"); err != nil {
		t.Fatalf("renaming a device of an app: %v", err)
	}
	dev, err := s.Access().Device(ctx, "dev_h01")
	if err != nil || dev.Model != "Anna's phone" || dev.Kind != DeviceKindLink || dev.Implicit {
		t.Fatalf("Device = %+v, %v", dev, err)
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l")); err != nil {
		t.Fatal(err)
	}
	if err := s.Access().RenameDevice(ctx, "dev_s", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renaming the shared device = %v, want ErrNotFound", err)
	}
	if _, err := s.Access().RevokeDevice(ctx, "dev_h01", linkNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().Device(ctx, "dev_h01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Device(revoked) = %v, want ErrNotFound", err)
	}
}
