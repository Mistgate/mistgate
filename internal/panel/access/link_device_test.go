package access

import (
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// What apps say about their device, and what the panel makes of it: a device per HWID with credentials of its own, the
// first HWID of a person taking over the shared device, a ceiling after which an app takes the shared place.

const (
	hwidPhone  = "0F8FAD5B-D9CB-469F-A165-70867728950E" // INCY sends it in upper case
	hwidTablet = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	hwidPC     = "d3b07384d113edec49eaa6238ad5ff00"
)

func phone() DeviceHint {
	return NewDeviceHint(hwidPhone, "iOS", "18.1", "iPhone 15", "Happ/3.4.2 (iOS)")
}

func tablet() DeviceHint {
	return NewDeviceHint(hwidTablet, "Android", "15", "Pixel Tablet", "Happ/3.4.2")
}

func TestCanonHWID(t *testing.T) {
	for in, want := range map[string]string{
		"0f8fad5b-d9cb-469f-a165-70867728950e":  "0f8fad5bd9cb469fa16570867728950e", // dashes go
		"0F8FAD5B-D9CB-469F-A165-70867728950E":  "0f8fad5bd9cb469fa16570867728950e", // case goes
		"  0f8fad5bd9cb469fa16570867728950e \t": "0f8fad5bd9cb469fa16570867728950e",
		"abcdefghij":                            "abcdefghij", // 10 characters is the least
		"AbC=-123456789":                        "abc=123456789",
		strings.Repeat("a", 64):                 strings.Repeat("a", 64),
		"":                                      "",
		"abcdefghi":                             "", // 9
		strings.Repeat("a", 65):                 "", // 65
		"0f8fad5b d9cb469fa165":                 "", // a space inside
		"0f8fad5b_d9cb469fa165":                 "",
		"0f8fad5b+d9cb469fa165":                 "",
		"0f8fadé5bd9cb469fa165":                 "",
		"----------":                            "", // nothing but dashes
	} {
		if got := CanonHWID(in); got != want {
			t.Errorf("CanonHWID(%q) = %q, want %q", in, got, want)
		}
	}
	if a, b := NewDeviceHint(hwidPhone, "", "", "", ""), NewDeviceHint(strings.ToLower(strings.ReplaceAll(hwidPhone, "-", "")), "", "", "", ""); a.HWID == "" || a.HWID != b.HWID {
		t.Errorf("the alternative HWID and the UUID of one phone name different devices: %q / %q", a.HWID, b.HWID)
	}
}

func TestDeviceHintMeta(t *testing.T) {
	long := strings.Repeat("я", 100)
	for _, c := range []struct {
		name string
		hint DeviceHint
		want deviceMeta
	}{
		{"ios", NewDeviceHint(hwidPhone, "iOS", "18.1", "iPhone 15", "Happ/3.4.2 (iOS 18.1)"), deviceMeta{"ios", "iPhone 15", "18.1", "Happ/3.4.2"}},
		{"android", NewDeviceHint(hwidPhone, "Android", "15", "Pixel", "Happ/3.4.2"), deviceMeta{"android", "Pixel", "15", "Happ/3.4.2"}},
		{"windows", NewDeviceHint(hwidPhone, "Windows", "11", "DESKTOP-1", "mihomo/1.19.31"), deviceMeta{"windows", "DESKTOP-1", "11", "mihomo/1.19.31"}},
		{"mac", NewDeviceHint(hwidPhone, "macOS", "15.0", "MacBook", ""), deviceMeta{"macos", "MacBook", "15.0", ""}},
		{"mac long name", NewDeviceHint(hwidPhone, "Mac OS X", "", "", ""), deviceMeta{"macos", "", "", ""}},
		{"linux", NewDeviceHint(hwidPhone, "Linux", "", "", ""), deviceMeta{"linux", "", "", ""}},
		{"a HWID without an OS is still a platform", NewDeviceHint(hwidPhone, "", "", "", "Happ/3"), deviceMeta{"other", "", "", "Happ/3"}},
		{"an unknown OS", NewDeviceHint(hwidPhone, "Tizen", "", "", ""), deviceMeta{"other", "", "", ""}},
		{"no HWID, no OS: no platform", NewDeviceHint("", "", "", "", "Hiddify/2.0"), deviceMeta{"", "", "", "Hiddify/2.0"}},
		{"no HWID but an OS", NewDeviceHint("", "Android", "", "", ""), deviceMeta{"android", "", "", ""}},
		{"control characters and length", DeviceHint{OS: "ios", Model: "ph\x00o\nne" + long, OSVersion: "1\t8", Agent: "A\x01pp/1" + strings.Repeat("x", 80)},
			deviceMeta{"ios", "phone" + strings.Repeat("я", 59), "18", "App/1" + strings.Repeat("x", 35)}},
	} {
		if got := c.hint.meta(); got != c.want {
			t.Errorf("%s: meta = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// storeHash is the hwid_hash of the device a hint names; hwidOf the same as a query argument (NULL for no HWID).
func storeHash(userID string, hint DeviceHint) []byte { return store.HWIDHash(userID, hint.HWID) }

func hwidOf(userID string, hint DeviceHint) any {
	if hint.HWID == "" {
		return nil
	}
	return storeHash(userID, hint)
}

func fetchAs(e *env, token string, hint DeviceHint, format plugin.ClientFormat) SubView {
	e.t.Helper()
	return must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Device: hint, Format: format}))
}

func (e *env) devicesOf(userID string) map[string]string { // id -> "hashed" or "shared"
	e.t.Helper()
	rows, err := e.st.R.Query(`SELECT id, hwid_hash IS NULL FROM device WHERE user_id = ? AND revoked_at IS NULL AND kind = 'link'`, userID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id string
		var shared bool
		if err := rows.Scan(&id, &shared); err != nil {
			e.t.Fatal(err)
		}
		out[id] = "hashed"
		if shared {
			out[id] = "shared"
		}
	}
	return out
}

func (e *env) credsOf(deviceID string) map[string]string { // protocol -> credential id
	e.t.Helper()
	rows, err := e.st.R.Query(`SELECT protocol, id FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, deviceID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var protocol, id string
		if err := rows.Scan(&protocol, &id); err != nil {
			e.t.Fatal(err)
		}
		out[protocol] = id
	}
	return out
}

// Each app that reports a HWID is a device with credentials of its own; the line it gets is its own too.
func TestEachHWIDGetsADeviceAndCredentialsOfItsOwn(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	e.resetNotify()

	a := fetchAs(e, token, phone(), "")
	if e.notify.n.Load() == 0 {
		t.Error("a new device's credentials did not notify the nodes")
	}
	b := fetchAs(e, token, tablet(), "")
	none := fetchAs(e, token, DeviceHint{}, "")
	if len(a.Lines) != 1 || len(b.Lines) != 1 || len(none.Lines) != 1 || a.Lines[0] == b.Lines[0] || a.Lines[0] == none.Lines[0] || b.Lines[0] == none.Lines[0] {
		t.Fatalf("lines differ per device: %q / %q / %q", a.Lines, b.Lines, none.Lines)
	}
	devices := e.devicesOf(r.User.Id)
	if len(devices) != 3 {
		t.Fatalf("devices = %v, want three", devices)
	}
	shared := 0
	for id, kind := range devices {
		if kind == "shared" {
			shared++
		}
		if len(e.credsOf(id)) != 2 { // hysteria2 and vless
			t.Errorf("device %s holds %v", id, e.credsOf(id))
		}
	}
	if shared != 1 {
		t.Errorf("shared devices = %d, want 1", shared)
	}
	// Each credential reaches the nodes with its device: three devices, three credentials of a protocol.
	if got := len(must(e.s.Desired(e.ctx, f.nodeID))[0].Creds); got != 3 {
		t.Errorf("credentials in the desired state = %d, want 3", got)
	}

	// The facts of the device come from the headers.
	var platform, model, osVersion, client string
	if err := e.st.R.QueryRow(`SELECT platform, model, os_version, client FROM device WHERE user_id = ? AND hwid_hash = ?`, r.User.Id,
		storeHash(r.User.Id, phone())).Scan(&platform, &model, &osVersion, &client); err != nil {
		t.Fatal(err)
	}
	if platform != "ios" || model != "iPhone 15" || osVersion != "18.1" || client != "Happ/3.4.2" {
		t.Errorf("phone facts = %q %q %q %q", platform, model, osVersion, client)
	}
	// The page lists them all.
	v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true}))
	if v.DevicesUsed != 3 || len(v.Devices) != 3 {
		t.Errorf("the page lists %d of %d devices, want 3", len(v.Devices), v.DevicesUsed)
	}
}

// A person's first app with a HWID takes over the shared device they already had: nothing is reissued, nothing reconnects,
// no slot is spent. The apps without a HWID get a shared device again, with new credentials.
func TestFirstHWIDAdoptsTheSharedDevice(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	token := e.tokenOf(r.User.Id)
	old := fetchAs(e, token, DeviceHint{}, "") // the link in an app that sends no HWID, as before this version
	before := e.devicesOf(r.User.Id)
	if len(before) != 1 {
		t.Fatalf("devices before = %v", before)
	}
	var sharedID string
	for id := range before {
		sharedID = id
	}
	credsBefore := e.credsOf(sharedID)
	e.resetNotify()

	adopted := fetchAs(e, token, phone(), "")
	if adopted.Lines[0] != old.Lines[0] {
		t.Errorf("the adopting app's credentials changed:\n%s\n%s", old.Lines[0], adopted.Lines[0])
	}
	after := e.devicesOf(r.User.Id)
	if len(after) != 1 || after[sharedID] != "hashed" {
		t.Fatalf("devices after the adoption = %v, want %s alone, with a hash", after, sharedID)
	}
	if got := e.credsOf(sharedID); len(got) != len(credsBefore) || got["hysteria2"] != credsBefore["hysteria2"] || got["vless"] != credsBefore["vless"] {
		t.Errorf("credentials changed on adoption: %v -> %v", credsBefore, got)
	}
	if e.notify.n.Load() != 0 {
		t.Error("an adoption changes no credential, yet the nodes were notified")
	}
	var model string
	if err := e.st.R.QueryRow(`SELECT model FROM device WHERE id = ?`, sharedID).Scan(&model); err != nil || model != "iPhone 15" {
		t.Errorf("model after adoption = %q, %v", model, err)
	}
	// The limit is 1 and the person has one device; the ceiling of 2 still allows a second HWID, which gets its own.
	second := fetchAs(e, token, tablet(), "")
	if second.Lines[0] == adopted.Lines[0] || len(e.devicesOf(r.User.Id)) != 2 {
		t.Errorf("the second HWID: %q, devices %v", second.Lines, e.devicesOf(r.User.Id))
	}
	// An app without a HWID after that: the shared place is free again, a new shared device with credentials of its own.
	again := fetchAs(e, token, DeviceHint{}, "")
	if again.Lines[0] == old.Lines[0] || again.Lines[0] == second.Lines[0] {
		t.Errorf("the app without a HWID got the credentials of another device: %q", again.Lines)
	}
}

// Past twice the limit a new HWID is no longer given a device: it takes the shared place, which the apps without a HWID use,
// and no device is written for it. (Counting the devices is all this version does with the limit.)
func TestHWIDOverTheCeilingTakesTheSharedPlace(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	token := e.tokenOf(r.User.Id)
	hints := make([]DeviceHint, 5)
	for i := range hints {
		hints[i] = NewDeviceHint(strings.Repeat(string(rune('a'+i)), 16), "Android", "", "", "Happ/3")
	}
	lines := map[string]bool{}
	for _, hint := range hints[:4] { // 2 × 2 = 4 devices
		lines[fetchAs(e, token, hint, "").Lines[0]] = true
	}
	if len(lines) != 4 || len(e.devicesOf(r.User.Id)) != 4 {
		t.Fatalf("devices under the ceiling: %v, %d distinct lines", e.devicesOf(r.User.Id), len(lines))
	}
	e.resetNotify()
	fifth := fetchAs(e, token, hints[4], "")
	if lines[fifth.Lines[0]] {
		t.Fatal("the fifth HWID was given the credentials of another device")
	}
	plain := fetchAs(e, token, DeviceHint{}, "")
	if fifth.Lines[0] != plain.Lines[0] {
		t.Errorf("the HWID over the ceiling and the app without a HWID do not share the place:\n%s\n%s", fifth.Lines[0], plain.Lines[0])
	}
	devices := e.devicesOf(r.User.Id)
	shared := 0
	for _, kind := range devices {
		if kind == "shared" {
			shared++
		}
	}
	if len(devices) != 5 || shared != 1 {
		t.Errorf("devices = %v, want the four and one shared", devices)
	}
	// Repeated: still the shared place, and nothing new is written.
	for range 3 {
		if fetchAs(e, token, hints[4], "").Lines[0] != plain.Lines[0] {
			t.Fatal("the shared place changed")
		}
	}
	if len(e.devicesOf(r.User.Id)) != 5 {
		t.Errorf("devices after repeats = %v", e.devicesOf(r.User.Id))
	}
}

// A malformed X-HWID is no HWID at all: the shared device answers.
func TestInvalidHWIDIsTheSharedDevice(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	plain := fetchAs(e, token, DeviceHint{}, "")
	for _, bad := range []string{"short", strings.Repeat("a", 65), "has space in the middle of it", "not_valid_chars!"} {
		if got := fetchAs(e, token, NewDeviceHint(bad, "iOS", "", "", ""), ""); got.Lines[0] != plain.Lines[0] {
			t.Errorf("%q was taken for a device", bad)
		}
	}
	if n := len(e.devicesOf(r.User.Id)); n != 1 {
		t.Errorf("devices = %d, want the shared one", n)
	}
}

// A fetch records the version of the OS and the app on its device, at most once an hour.
func TestTouchRecordsTheOSVersionAndTheApp(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 4)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, phone(), "")
	facts := func() (osVersion, client string, seen int64) {
		if err := e.st.R.QueryRow(`SELECT os_version, client, last_seen_at FROM device WHERE user_id = ?`, r.User.Id).Scan(&osVersion, &client, &seen); err != nil {
			t.Fatal(err)
		}
		return
	}
	if osVersion, client, _ := facts(); osVersion != "18.1" || client != "Happ/3.4.2" {
		t.Fatalf("a new device = %q %q", osVersion, client)
	}
	assertNoTouchStarted(t, touches) // made just now: nothing to touch

	// An hour later the phone has updated.
	e.clock = e.clock.Add(61 * time.Minute)
	upgraded := NewDeviceHint(hwidPhone, "iOS", "18.2", "iPhone 15", "Happ/3.5.0 (iOS)")
	fetchAs(e, token, upgraded, "")
	waitTouchEvent(t, touches, true)
	waitTouchEvent(t, touches, false)
	if osVersion, client, seen := facts(); osVersion != "18.2" || client != "Happ/3.5.0" || seen != e.clock.Unix() {
		t.Fatalf("after the touch = %q %q at %d", osVersion, client, seen)
	}
	// Within the hour another update is not written.
	e.clock = e.clock.Add(10 * time.Minute)
	fetchAs(e, token, NewDeviceHint(hwidPhone, "iOS", "18.3", "iPhone 15", "Happ/3.6.0"), "")
	assertNoTouchStarted(t, touches)
	if osVersion, client, _ := facts(); osVersion != "18.2" || client != "Happ/3.5.0" {
		t.Errorf("a fetch within the hour wrote %q %q", osVersion, client)
	}
}

// An app of a person without access keeps its device fresh for health; one the panel has no device for is seen on the shared one.
func TestExpiredFetchTouchesTheDeviceOfTheRequest(t *testing.T) {
	f := newFixture(t)
	e := f.e
	touches := make(chan bool, 4)
	e.s.SetTouchHookForTest(func(started bool) { touches <- started })
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, phone(), "")
	fetchAs(e, token, DeviceHint{}, "")
	e.sql(`UPDATE device SET last_seen_at = ? WHERE user_id = ?`, e.clock.Add(-3*time.Hour).Unix(), r.User.Id)
	e.sql(`UPDATE user SET expires_at = ? WHERE id = ?`, e.clock.Add(-time.Minute).Unix(), r.User.Id)

	v := fetchAs(e, token, phone(), "")
	if v.Status != StatusExpired || len(v.Lines) != 0 {
		t.Fatalf("expired view = %q with %d lines", v.Status, len(v.Lines))
	}
	waitTouchEvent(t, touches, true)
	waitTouchEvent(t, touches, false)
	seen := func(hint DeviceHint) (n int64) {
		e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS ?`, r.User.Id, hwidOf(r.User.Id, hint)).Scan(&n)
		return n
	}
	if seen(phone()) != e.clock.Unix() || seen(DeviceHint{}) == e.clock.Unix() {
		t.Errorf("the phone's device was touched at %d, the shared one at %d, want only the phone's at %d", seen(phone()), seen(DeviceHint{}), e.clock.Unix())
	}
	// A HWID no device is known by: no device is made, the shared device stands for it.
	fetchAs(e, token, tablet(), "")
	waitTouchEvent(t, touches, true)
	waitTouchEvent(t, touches, false)
	if seen(DeviceHint{}) != e.clock.Unix() || len(e.devicesOf(r.User.Id)) != 2 {
		t.Errorf("shared device touched at %d, devices %v", seen(DeviceHint{}), e.devicesOf(r.User.Id))
	}
}

// The AmneziaWG proxies of a Mihomo profile are the credentials of the device that fetched it.
func TestMihomoAWGPeerPerDevice(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	token := e.tokenOf(f.user)
	a := fetchAs(e, token, phone(), plugin.FormatMihomo)
	b := fetchAs(e, token, tablet(), plugin.FormatMihomo)
	plain := fetchAs(e, token, DeviceHint{}, plugin.FormatMihomo)
	if len(a.Lines) != 1 || len(b.Lines) != 1 || len(plain.Lines) != 1 || a.Lines[0] == b.Lines[0] || a.Lines[0] == plain.Lines[0] {
		t.Fatalf("proxies differ per device: %q / %q / %q", a.Lines, b.Lines, plain.Lines)
	}
	if devices := e.devicesOf(f.user); len(devices) != 3 {
		t.Fatalf("devices = %v, want three", devices)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 3 {
		t.Errorf("live peers = %d, want one per device", n)
	}
	if n := e.count(`SELECT count(DISTINCT idx) FROM awg_peer WHERE released_at = 0`); n != 3 {
		t.Errorf("distinct tunnel addresses = %d, want 3", n)
	}
	// Repeated fetches add nothing and give the same proxy.
	e.resetNotify()
	if again := fetchAs(e, token, phone(), plugin.FormatMihomo); again.Lines[0] != a.Lines[0] || e.notify.n.Load() != 0 {
		t.Error("a second Mihomo fetch of the phone changed its proxy or notified")
	}
	// They are not AmneziaVPN keys: the admin lists none, and the user's key count is untouched.
	got := f.user_(f.user)
	for _, d := range got.Devices {
		if d.AwgProfileId != "" {
			t.Errorf("a Mihomo peer is listed as a key: %+v", d)
		}
	}
	if keys := must(e.st.Access().AWGDevices(e.ctx, f.user)); len(keys) != 0 {
		t.Errorf("AWGDevices = %d, want 0", len(keys))
	}
	if len(f.desired()) != 3 {
		t.Errorf("AWG credentials in the desired state = %d, want 3", len(f.desired()))
	}
}

// A new user has no device, so the list of people counts none, and one with many devices is counted once for each.
func TestUsersListCountsDevicesOfManyHWIDs(t *testing.T) {
	f := newFixture(t)
	e := f.e
	none := e.userBare("none", f.group, nil)
	many := e.userBare("many", f.group, nil)
	token := e.tokenOf(many.User.Id)
	for _, hint := range []DeviceHint{phone(), tablet(), NewDeviceHint(hwidPC, "Windows", "11", "PC", "mihomo/1.19.31"), {}} {
		fetchAs(e, token, hint, "")
	}
	got := must(e.s.ListUsers(e.ctx, req(&adminv1.ListUsersRequest{}))).Msg
	if used := listUserCountValue(got.Counts, "devices_used", 5); used != 4 {
		t.Errorf("devices used = %d, want 4", used)
	}
	byName := map[string]uint32{}
	for _, u := range got.Users {
		byName[u.Name] = u.DevicesUsed
	}
	if byName["many"] != 4 || byName["none"] != 0 || none.User.DevicesUsed != 0 {
		t.Errorf("devices per user = %v", byName)
	}
}

// An app of a person with only the AmneziaVPN access that reports a HWID takes over the shared device its Mihomo profile
// already made, with the same peer: nothing is reissued.
func TestMihomoAWGAdoptsTheSharedDevicesPeer(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	token := e.tokenOf(f.user)
	old := fetchAs(e, token, DeviceHint{}, plugin.FormatMihomo)
	e.resetNotify()
	adopted := fetchAs(e, token, phone(), plugin.FormatMihomo)
	if len(adopted.Lines) != 1 || adopted.Lines[0] != old.Lines[0] {
		t.Fatalf("the proxy changed on adoption:\n%q\n%q", old.Lines, adopted.Lines)
	}
	if devices := e.devicesOf(f.user); len(devices) != 1 {
		t.Fatalf("devices = %v, want the one that was taken over", devices)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 1 {
		t.Errorf("live peers = %d, want 1", n)
	}
	if e.notify.n.Load() != 0 {
		t.Error("an adoption notified the nodes")
	}
}
