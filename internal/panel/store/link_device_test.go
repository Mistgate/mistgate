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

// A link device per HWID: the shared device of a user is the one with hwid_hash NULL, an app that reports a HWID gets its own
// device with its own credentials, the first of them taking the shared device over. The races are decided by the guards of
// the batch; the tests make the competing writer win between the read and the write, as implicit_device_race_test does.

func linkFixture(t *testing.T, user string) *Store {
	t.Helper()
	s := openTemp(t)
	rewriteAccessGroup(t, s, "grp_link", "Link devices")
	rewriteAccessUser(t, s, user, "u-"+user, "grp_link")
	setLinkMode(t, s, LinkModeEnforce) // the limit passed to EnsureLinkDevice is the ceiling: ENFORCE holds a HWID and the shared device to it
	return s
}

// setLinkMode stores the mode of the device limit, as the panel's settings do.
func setLinkMode(t *testing.T, s *Store, mode string) {
	t.Helper()
	if err := s.SetSettings(context.Background(), map[string]string{SettingLinkDeviceLimit: mode}); err != nil {
		t.Fatal(err)
	}
}

var linkNow = time.Unix(1_800_000_000, 0)

// linkCreds issues a hysteria2 and a vless credential for the device (ids are made from tag), except for protocols it has.
func linkCreds(tag, userID string) LinkIssue {
	return func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		var out []AccessCred
		for _, protocol := range []string{"hysteria2", "vless"} {
			if !have[protocol] {
				out = append(out, AccessCred{ID: "crd_" + tag + "_" + protocol, UserID: userID, Protocol: protocol,
					SecretEnc: []byte("sealed-" + tag), DataJSON: `{}`})
			}
		}
		return out, nil
	}
}

func linkDevice(user, tag string, hash []byte) AccessDevice {
	return AccessDevice{ID: "dev_" + tag, UserID: user, HWIDHash: hash, Implicit: hash == nil, Platform: "ios", Client: "Happ/3.4.2"}
}

func liveDevices(t *testing.T, s *Store, user string) int {
	t.Helper()
	return countT(t, s, `SELECT count(*) FROM device d WHERE user_id = ? AND revoked_at IS NULL AND `+accDeviceLive, user)
}

func TestHWIDHashIsPerUserAndNeverAKeyHash(t *testing.T) {
	a, b := HWIDHash("usr_a", "0f8fad5bd9cb469fa16570867728950e"), HWIDHash("usr_b", "0f8fad5bd9cb469fa16570867728950e")
	if len(a) != 32 || string(a) == string(b) {
		t.Fatalf("hash %x / %x: want 32 bytes that differ between users", a, b)
	}
	if string(a) == string(HWIDHash("usr_a", "0f8fad5bd9cb469fa16570867728950f")) || string(a) != string(HWIDHash("usr_a", "0f8fad5bd9cb469fa16570867728950e")) {
		t.Fatal("the hash does not follow the HWID")
	}
	if string(a) == string(AWGHwidHash("dev_x")) {
		t.Fatal("a HWID hash equals a key hash")
	}
}

// The shared device and the devices of the HWIDs are found by the same statement; each sees its own credentials only.
func TestEnsureLinkDeviceHWIDsGetCredentialsOfTheirOwn(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	hashA, hashB := HWIDHash("usr_l", "aaaaaaaaaaaaaaaa"), HWIDHash("usr_l", "bbbbbbbbbbbbbbbb")

	a, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "a", hashA), 10, linkNow, linkCreds("a", "usr_l"))
	if err != nil || a.Device.ID != "dev_a" || len(a.Added) != 2 || len(a.Creds) != 2 || a.Device.Implicit {
		t.Fatalf("device A = %+v, %v", a, err)
	}
	b, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "b", hashB), 10, linkNow, linkCreds("b", "usr_l"))
	if err != nil || b.Device.ID != "dev_b" || len(b.Added) != 2 {
		t.Fatalf("device B = %+v, %v", b, err)
	}
	shared, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l"))
	if err != nil || shared.Device.ID != "dev_s" || !shared.Device.Implicit || len(shared.Added) != 2 {
		t.Fatalf("shared device = %+v, %v", shared, err)
	}
	if n := liveDevices(t, s, "usr_l"); n != 3 {
		t.Fatalf("live devices = %d, want 3", n)
	}
	for hash, want := range map[string]string{string(hashA): "dev_a", string(hashB): "dev_b", "": "dev_s"} {
		var h []byte
		if hash != "" {
			h = []byte(hash)
		}
		data, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, h)
		if err != nil || data.Device.ID != want || len(data.DeviceCreds) != 2 || len(data.Devices) != 3 {
			t.Fatalf("SubscriptionData for %q: device %q with %d credentials of %d devices, %v; want %q", want, data.Device.ID, len(data.DeviceCreds), len(data.Devices), err, want)
		}
		for _, c := range data.DeviceCreds {
			if c.DeviceID != want {
				t.Fatalf("device %s was handed the credential %s of %s", want, c.ID, c.DeviceID)
			}
		}
	}
	if data, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, HWIDHash("usr_l", "cccccccccccccccc")); err != nil || data.Device.ID != "" || len(data.DeviceCreds) != 0 {
		t.Fatalf("an unknown HWID reads %+v, %v; want no device", data.Device, err)
	}
	// None of them is a key.
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l' AND kind = 'link'`); n != 3 {
		t.Fatalf("link devices = %d, want 3", n)
	}
}

// A device that is known is filled in, never made twice; nothing is written when it has everything.
func TestEnsureLinkDeviceFillsAKnownDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	hash := HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")
	only := func(protocol string) LinkIssue {
		return func(deviceID string, have map[string]bool) ([]AccessCred, error) {
			if have[protocol] {
				return nil, nil
			}
			return []AccessCred{{ID: "crd_known_" + protocol, UserID: "usr_l", Protocol: protocol, SecretEnc: []byte{1}, DataJSON: `{}`}}, nil
		}
	}
	if r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "a", hash), 10, linkNow, only("hysteria2")); err != nil || len(r.Added) != 1 {
		t.Fatalf("first = %+v, %v", r, err)
	}
	r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "other", hash), 10, linkNow, only("vless"))
	if err != nil || r.Device.ID != "dev_a" || len(r.Added) != 1 || len(r.Creds) != 2 || len(r.Device.Protocols) != 2 {
		t.Fatalf("filling the known device = %+v, %v; want dev_a with both credentials", r, err)
	}
	r, err = s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "other", hash), 10, linkNow, linkCreds("again", "usr_l"))
	if err != nil || r.Device.ID != "dev_a" || len(r.Added) != 0 || len(r.Creds) != 2 {
		t.Fatalf("a complete device = %+v, %v; want nothing added", r, err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l'`); n != 1 {
		t.Fatalf("devices = %d, want 1", n)
	}
	// A device is never born empty.
	r, err = s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "none", HWIDHash("usr_l", "zzzzzzzzzzzzzzzz")), 10, linkNow,
		func(string, map[string]bool) ([]AccessCred, error) { return nil, nil })
	if err != nil || r.Device.ID != "" || countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l'`) != 1 {
		t.Fatalf("with nothing to issue: %+v, %v", r, err)
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_gone", "x", hash), 10, linkNow, linkCreds("x", "usr_gone")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a user that is gone: %v", err)
	}
}

// The first HWID of a person whose shared device holds credentials takes it over: same device, same credentials, no slot spent.
func TestEnsureLinkDeviceAdoptsTheSharedDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	shared, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l"))
	if err != nil || len(shared.Added) != 2 {
		t.Fatalf("shared = %+v, %v", shared, err)
	}
	execT(t, s, `UPDATE device SET first_seen_at = 0 WHERE id = 'dev_s'`)
	hash := HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")
	dev := linkDevice("usr_l", "a", hash)
	dev.Model, dev.OSVersion = "iPhone 15", "18.1"
	// A ceiling of 1 would refuse a new device: an adoption spends no slot.
	adopted, err := s.Access().EnsureLinkDevice(ctx, dev, 1, linkNow.Add(time.Minute), linkCreds("a", "usr_l"))
	if err != nil || adopted.Device.ID != "dev_s" || adopted.Device.Implicit || len(adopted.Added) != 0 || len(adopted.Creds) != 2 {
		t.Fatalf("adoption = %+v, %v; want dev_s kept with both credentials, none added", adopted, err)
	}
	for i, c := range adopted.Creds {
		if c.ID != shared.Creds[i].ID || string(c.SecretEnc) != string(shared.Creds[i].SecretEnc) {
			t.Fatalf("credential %d changed on adoption: %+v -> %+v", i, shared.Creds[i], c)
		}
	}
	if adopted.Device.Platform != "ios" || adopted.Device.Model != "iPhone 15" || adopted.Device.OSVersion != "18.1" || adopted.Device.Client != "Happ/3.4.2" ||
		adopted.Device.FirstSeenAt.Unix() != linkNow.Add(time.Minute).Unix() {
		t.Fatalf("adopted device facts = %+v", adopted.Device)
	}
	if n := liveDevices(t, s, "usr_l"); n != 1 {
		t.Fatalf("live devices after the adoption = %d, want 1", n)
	}
	// The shared place is empty now: an app without a HWID is a new device, and a second HWID does not adopt anything.
	if data, err := s.Access().SubscriptionData(ctx, "usr_l", "grp_link", linkNow, true, linkNow, nil); err != nil || data.Device.ID != "" {
		t.Fatalf("the shared device after the adoption reads %+v, %v; want none", data.Device, err)
	}
	second, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "b", HWIDHash("usr_l", "bbbbbbbbbbbbbbbb")), 10, linkNow, linkCreds("b", "usr_l"))
	if err != nil || second.Device.ID != "dev_b" || len(second.Added) != 2 {
		t.Fatalf("second HWID = %+v, %v; want a device of its own", second, err)
	}
}

// Two new HWIDs at the ceiling: the one that loses the race finds the place taken. Every writer here is the one that reads
// "room for one more" and then writes after the winner.
func TestEnsureLinkDeviceTwoNewHWIDsAtTheCeilingMakeOneDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "first", HWIDHash("usr_l", "1111111111111111")), 3, linkNow, linkCreds("first", "usr_l")); err != nil {
		t.Fatal(err)
	}
	hashX, hashY := HWIDHash("usr_l", "xxxxxxxxxxxxxxxx"), HWIDHash("usr_l", "yyyyyyyyyyyyyyyy")
	// The ceiling is 2 devices of 1 now: room for one.
	racing := false
	var inner error
	issueX := func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !racing {
			racing = true
			_, inner = s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "y", hashY), 2, linkNow, linkCreds("y", "usr_l"))
		}
		return linkCreds("x", "usr_l")(deviceID, have)
	}
	_, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "x", hashX), 2, linkNow, issueX)
	var limit *AccessLimitError
	if inner != nil || !errors.As(err, &limit) || limit.Used != 2 || limit.Limit != 2 {
		t.Fatalf("the late writer got %v (the early one %v), want *AccessLimitError 2/2", err, inner)
	}
	if n := liveDevices(t, s, "usr_l"); n != 2 {
		t.Fatalf("live devices = %d, want 2: one of the two was made", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE id IN ('dev_x', 'dev_y') AND revoked_at IS NULL`); n != 1 {
		t.Fatalf("of the two new HWIDs %d were made, want 1", n)
	}
}

// Real concurrency, same rule: eight new HWIDs and room for three.
func TestEnsureLinkDeviceConcurrentHWIDsStopAtTheCeiling(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	const writers, ceiling = 8, 3
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tag := fmt.Sprintf("c%d", i)
			_, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", tag, HWIDHash("usr_l", tag+"-hwid-value")), ceiling, linkNow, linkCreds(tag, "usr_l"))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	made, refused := 0, 0
	for err := range errs {
		var limit *AccessLimitError
		switch {
		case err == nil:
			made++
		case errors.As(err, &limit):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if made != ceiling || refused != writers-ceiling || liveDevices(t, s, "usr_l") != ceiling {
		t.Fatalf("made %d, refused %d, live %d; want %d, %d, %d", made, refused, liveDevices(t, s, "usr_l"), ceiling, writers-ceiling, ceiling)
	}
}

// The same HWID twice at once: the second request reads no device, the first writes one, the second finds it on its retry
// and writes nothing; both get the same device.
func TestEnsureLinkDeviceSameHWIDTwiceMakesOneDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	hash := HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")
	racing := false
	var inner LinkDeviceResult
	var innerErr error
	issue := func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !racing {
			racing = true
			inner, innerErr = s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "inner", hash), 10, linkNow, linkCreds("inner", "usr_l"))
		}
		return linkCreds("outer", "usr_l")(deviceID, have)
	}
	outer, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "outer", hash), 10, linkNow, issue)
	if err != nil || innerErr != nil {
		t.Fatalf("outer %v, inner %v", err, innerErr)
	}
	if outer.Device.ID != "dev_inner" || outer.Device.ID != inner.Device.ID || len(outer.Added) != 0 || len(outer.Creds) != 2 || len(inner.Added) != 2 {
		t.Fatalf("outer %+v, inner %+v; want one device, made by the inner call, seen by the outer", outer, inner)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l'`); n != 1 {
		t.Fatalf("devices = %d, want 1", n)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE user_id = 'usr_l' AND revoked_at IS NULL`); n != 2 {
		t.Fatalf("credentials = %d, want 2", n)
	}
}

// Two HWIDs that both read "the shared device is free to take": one takes it, the other makes a device of its own.
func TestEnsureLinkDeviceTwoAdoptionsTakeTheSharedDeviceOnce(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l")); err != nil {
		t.Fatal(err)
	}
	hashX, hashY := HWIDHash("usr_l", "xxxxxxxxxxxxxxxx"), HWIDHash("usr_l", "yyyyyyyyyyyyyyyy")
	racing := false
	var inner LinkDeviceResult
	var innerErr error
	issue := func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !racing {
			racing = true
			inner, innerErr = s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "y", hashY), 10, linkNow, linkCreds("y", "usr_l"))
		}
		return linkCreds("x", "usr_l")(deviceID, have)
	}
	outer, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "x", hashX), 10, linkNow, issue)
	if err != nil || innerErr != nil {
		t.Fatalf("outer %v, inner %v", err, innerErr)
	}
	if inner.Device.ID != "dev_s" || len(inner.Added) != 0 {
		t.Fatalf("the first writer got %+v, want the adopted dev_s", inner)
	}
	if outer.Device.ID != "dev_x" || len(outer.Added) != 2 {
		t.Fatalf("the second writer got %+v, want a new device of its own", outer)
	}
	if n := countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_l' AND hwid_hash = ?`, hashY); n != 1 {
		t.Fatalf("devices with the adopter's hash = %d, want 1", n)
	}
	if n := liveDevices(t, s, "usr_l"); n != 2 {
		t.Fatalf("live devices = %d, want 2", n)
	}
}

// The device is revoked after the read: the credentials are not written onto it, a new device takes the HWID.
func TestEnsureLinkDeviceRevokedAfterTheReadIsReplaced(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	hash := HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "old", hash), 10, linkNow, func(string, map[string]bool) ([]AccessCred, error) {
		return []AccessCred{{ID: "crd_old", UserID: "usr_l", Protocol: "hysteria2", SecretEnc: []byte{1}, DataJSON: `{}`}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	revoked := false
	r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "new", hash), 10, linkNow, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		if !revoked {
			revoked = true
			if _, err := s.Access().RevokeDevice(ctx, "dev_old", linkNow); err != nil {
				return nil, err
			}
		}
		return linkCreds("new", "usr_l")(deviceID, have)
	})
	if err != nil || r.Device.ID != "dev_new" || len(r.Added) != 2 {
		t.Fatalf("after the revoke: %+v, %v; want dev_new with both credentials", r, err)
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = 'dev_old' AND revoked_at IS NULL`); n != 0 {
		t.Fatalf("the revoked device holds %d live credentials", n)
	}
}

// The ceiling counts every live device of the person, keys and shared included.
func TestEnsureLinkDeviceCeilingCountsEveryLiveDevice(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "ceiling")
	if _, err := awgDevice(t, s, profile, "usr_l", "dev_key", 8); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s", nil), 0, linkNow, linkCreds("s", "usr_l")); err != nil {
		t.Fatal(err)
	}
	// A key and the shared device: two. The shared one is taken over by the first HWID, so the ceiling of 2 still lets a second HWID in only
	// when it fits: the HWID of the adopter stays at 2, a third new device is refused.
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "a", HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")), 2, linkNow, linkCreds("a", "usr_l")); err != nil {
		t.Fatalf("adoption at the ceiling: %v", err)
	}
	var limit *AccessLimitError
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "b", HWIDHash("usr_l", "bbbbbbbbbbbbbbbb")), 2, linkNow, linkCreds("b", "usr_l")); !errors.As(err, &limit) || limit.Used != 2 {
		t.Fatalf("a new device at the ceiling: %v", err)
	}
	// The shared device has no ceiling: it is the fallback of the HWID that was refused.
	if r, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "s2", nil), 0, linkNow, linkCreds("s2", "usr_l")); err != nil || r.Device.ID != "dev_s2" {
		t.Fatalf("the shared place over the ceiling: %+v, %v", r, err)
	}
}

// The AmneziaWG proxies of a Mihomo subscription are credentials of the device that fetched it: two HWIDs, two peers, and
// neither is listed, counted or revoked as an AmneziaVPN key.
func TestEnsureLinkAWGCredsGivesEachDeviceItsOwnPeer(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	profile, _ := awgNet(t, s, "perdev")
	want := func(tag string) []AWGImplicitWant {
		return []AWGImplicitWant{{ProfileID: profile, MaxIdx: 8, Issue: func(idx int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_awg_" + tag, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_" + tag, nil
		}}}
	}
	a, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "a", HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")), 10, linkNow, want("a"))
	if err != nil || a.Device.ID == "" || len(a.Added) != 1 || a.Device.Implicit {
		t.Fatalf("device A = %+v, %v; want a device of its own with one peer", a, err)
	}
	b, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "b", HWIDHash("usr_l", "bbbbbbbbbbbbbbbb")), 10, linkNow, want("b"))
	if err != nil || b.Device.ID == "" || b.Device.ID == a.Device.ID || len(b.Added) != 1 {
		t.Fatalf("device B = %+v, %v; want another device with its own peer", b, err)
	}
	if again, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "a", HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")), 10, linkNow, want("again")); err != nil || again.Device.ID != a.Device.ID || len(again.Added) != 0 || len(again.Creds) != 1 {
		t.Fatalf("A again = %+v, %v; want its peer and nothing added", again, err)
	}
	if n := countT(t, s, `SELECT count(DISTINCT idx) FROM awg_peer WHERE profile_id = ? AND released_at = 0`, profile); n != 2 {
		t.Fatalf("live peers with distinct addresses = %d, want 2", n)
	}
	if keys, err := s.Access().AWGDevices(ctx, "usr_l"); err != nil || len(keys) != 0 {
		t.Fatalf("AWGDevices = %+v, %v; want no key", keys, err)
	}
	if n, _, err := s.Access().ProfileLiveAWG(ctx, profile, 10); err != nil || n != 0 {
		t.Fatalf("ProfileLiveAWG = %d, %v; want 0", n, err)
	}
	// The ceiling applies to the device that would be born here.
	var limit *AccessLimitError
	if _, err := s.Access().EnsureLinkAWGCreds(ctx, linkDevice("usr_l", "c", HWIDHash("usr_l", "cccccccccccccccc")), 2, linkNow, want("c")); !errors.As(err, &limit) {
		t.Fatalf("a third device over a ceiling of 2: %v", err)
	}
}

// A person with several link devices, each holding several credentials, is counted once per device.
func TestDeviceCountsCountManyLinkDevicesOnce(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	rewriteAccessUser(t, s, "usr_o", "other", "grp_link")
	for i := range 4 {
		tag := fmt.Sprintf("m%d", i)
		if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", tag, HWIDHash("usr_l", tag+"-hwid-value")), 10, linkNow, linkCreds(tag, "usr_l")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "shared", nil), 0, linkNow, linkCreds("shared", "usr_l")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_o", "one", nil), 0, linkNow, linkCreds("one", "usr_o")); err != nil {
		t.Fatal(err)
	}
	counts, err := s.Access().DeviceCounts(ctx, []string{"usr_l", "usr_o"})
	if err != nil || counts["usr_l"] != 5 || counts["usr_o"] != 1 {
		t.Fatalf("DeviceCounts = %v, %v; want 5 and 1", counts, err)
	}
	users, _, listCounts, err := s.Access().ListUsers(ctx, AccessUserQuery{Sort: AccessUserSortDevices, Descending: true, Now: linkNow, Limit: 10})
	if err != nil || len(users) != 2 || users[0].ID != "usr_l" || listCounts.DevicesUsed != 6 {
		t.Fatalf("ListUsers by devices = %d users, first %q, %d devices used, %v; want usr_l first and 6 devices", len(users), users[0].ID, listCounts.DevicesUsed, err)
	}
}

// Health reads the last fetch of the person from their newest link device, and a fetch records the OS and the app.
func TestLinkDevicesFeedFetchTimeAndTouch(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for i, tag := range []string{"a", "b", "c"} {
		dev := linkDevice("usr_l", tag, HWIDHash("usr_l", tag+"-hwid-value-1"))
		if _, err := s.Access().EnsureLinkDevice(ctx, dev, 10, linkNow, linkCreds(tag, "usr_l")); err != nil {
			t.Fatal(err)
		}
		execT(t, s, `UPDATE device SET last_seen_at = ? WHERE id = ?`, linkNow.Unix()+int64(100*(1-i*i)), "dev_"+tag) // 100, 0, -300 after linkNow
	}
	signals, err := s.HealthSignals(ctx, linkNow)
	if err != nil {
		t.Fatal(err)
	}
	var fetch int64
	for _, u := range signals.Users {
		if u.ID == "usr_l" {
			fetch = u.SubscriptionFetchAt.Unix()
		}
	}
	if fetch != linkNow.Unix()+100 {
		t.Fatalf("SubscriptionFetchAt = %d, want the newest of the link devices, %d", fetch, linkNow.Unix()+100)
	}
	// touch: the stale last_seen_at moves, the os version and the client are recorded, empty ones leave the stored values.
	later := linkNow.Add(2 * time.Hour)
	if err := s.Access().TouchDevice(ctx, "dev_b", later, later.Add(-time.Hour), "18.2", "Happ/3.5.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.Access().TouchDevice(ctx, "dev_b", later.Add(2*time.Hour), later.Add(time.Hour), "", ""); err != nil {
		t.Fatal(err)
	}
	var seen int64
	var osVersion, client string
	if err := s.R.QueryRowContext(ctx, `SELECT last_seen_at, os_version, client FROM device WHERE id = 'dev_b'`).Scan(&seen, &osVersion, &client); err != nil {
		t.Fatal(err)
	}
	if seen != later.Add(2*time.Hour).Unix() || osVersion != "18.2" || client != "Happ/3.5.0" {
		t.Fatalf("touched device = %d %q %q", seen, osVersion, client)
	}
}

// Only the first HWID of a person takes the shared device over: once a device with a HWID exists, the shared device that
// an app without one made later stays the place of the apps without a HWID.
func TestEnsureLinkDeviceAdoptsOnlyForThePersonsFirstHWID(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	for _, dev := range []AccessDevice{
		linkDevice("usr_l", "s1", nil),
		linkDevice("usr_l", "a", HWIDHash("usr_l", "aaaaaaaaaaaaaaaa")), // takes s1
		linkDevice("usr_l", "s2", nil),                                  // the apps without a HWID get a new shared device
		linkDevice("usr_l", "b", HWIDHash("usr_l", "bbbbbbbbbbbbbbbb")),
	} {
		if _, err := s.Access().EnsureLinkDevice(ctx, dev, 10, linkNow, linkCreds(dev.ID, "usr_l")); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	rows, err := s.R.QueryContext(ctx, `SELECT id FROM device WHERE user_id = 'usr_l' AND revoked_at IS NULL AND hwid_hash IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids[id] = true
	}
	if !ids["dev_s2"] || len(ids) != 1 || liveDevices(t, s, "usr_l") != 3 {
		t.Fatalf("shared devices %v, live %d; want only dev_s2 shared and three devices (s1 became a, then b and s2 are new)", ids, liveDevices(t, s, "usr_l"))
	}
}
