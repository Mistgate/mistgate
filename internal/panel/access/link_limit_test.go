package access

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The limit of link devices as a person's app and the owner meet it: refusals instead of servers, the event that tells the
// owner, STRICT, the owner's block, a silent device giving its place away, and the page's own remove and rename.

func (e *env) setLinkMode(mode string) {
	e.t.Helper()
	if err := e.st.SetSettings(e.ctx, map[string]string{store.SettingLinkDeviceLimit: mode}); err != nil {
		e.t.Fatal(err)
	}
}

// syncAfterResponse runs the work that follows a response at once, so a test sees the event it queued.
func (e *env) syncAfterResponse() { e.s.afterResponse = func(work func()) { work() } }

func hintN(n int) DeviceHint {
	return NewDeviceHint(strings.Repeat(string(rune('a'+n)), 16), "Android", "15", "Pixel", "Happ/3.4.2")
}

func pu32(v uint32) *uint32 { return &v }

func (e *env) events(code string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM event WHERE code = ?`, code)
}

func (e *env) auditActions() []string {
	e.t.Helper()
	var out []string
	for _, r := range must(e.st.ListAudit(e.ctx, "", 0, 100)) {
		out = append(out, r.Action)
	}
	return out
}

func TestEnforceRefusesTheDeviceOverTheLimit(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.syncAfterResponse()
	e.setLinkMode(store.LinkModeEnforce)
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	token := e.tokenOf(r.User.Id)
	for n := range 2 {
		if v := fetchAs(e, token, hintN(n), ""); v.Refusal != "" || len(v.Lines) != 1 {
			t.Fatalf("device %d under the limit: refusal %q, %d lines", n, v.Refusal, len(v.Lines))
		}
	}
	e.resetNotify()
	for _, format := range []plugin.ClientFormat{"", plugin.FormatMihomo} {
		v := fetchAs(e, token, hintN(2), format)
		if v.Refusal != RefusalDeviceLimit || len(v.Lines) != 0 || len(v.Servers) != 0 || v.Status != StatusActive || v.DevicesUsed != 2 || v.DeviceLimit != 2 {
			t.Fatalf("the third device (%q): refusal %q, %d lines, %d servers, %d of %d", format, v.Refusal, len(v.Lines), len(v.Servers), v.DevicesUsed, v.DeviceLimit)
		}
	}
	// An app without a HWID needs the shared place, and there is none to be had either.
	if v := fetchAs(e, token, DeviceHint{}, ""); v.Refusal != RefusalDeviceLimit {
		t.Fatalf("an app without a HWID at the limit: refusal %q", v.Refusal)
	}
	if n := len(e.devicesOf(r.User.Id)); n != 2 {
		t.Fatalf("devices after the refusals = %d, want 2", n)
	}
	if e.notify.n.Load() != 0 {
		t.Error("a refusal notified the nodes")
	}
	// A known device is served as before, and the page (which only looks) shows the person's own servers, not a refusal.
	if v := fetchAs(e, token, hintN(0), ""); v.Refusal != "" || len(v.Lines) != 1 {
		t.Fatalf("a known device at the limit: refusal %q, %d lines", v.Refusal, len(v.Lines))
	}
	if v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true})); v.Refusal != "" || len(v.Servers) != 1 {
		t.Fatalf("the page at the limit: refusal %q, %d servers", v.Refusal, len(v.Servers))
	}
	// The owner raises the limit and the refused app gets in at once.
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: r.User.Id, DeviceLimit: pu32(3)})))
	if v := fetchAs(e, token, hintN(2), ""); v.Refusal != "" || len(v.Lines) != 1 {
		t.Fatalf("after raising the limit: refusal %q", v.Refusal)
	}
}

// TRACK counts only: past the limit the device is made, up to twice the limit; the event tells the owner.
func TestTrackLetsTheDeviceThroughAndSaysSo(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.syncAfterResponse()
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	token := e.tokenOf(r.User.Id)
	for n := range 2 {
		fetchAs(e, token, hintN(n), "")
	}
	if e.events("device_limit_reached") != 0 {
		t.Fatal("an event before the limit was reached")
	}
	if v := fetchAs(e, token, hintN(2), ""); v.Refusal != "" || len(v.Lines) != 1 {
		t.Fatalf("a third device in track: refusal %q", v.Refusal)
	}
	if n := len(e.devicesOf(r.User.Id)); n != 3 {
		t.Fatalf("devices = %d, want 3", n)
	}
	if n := e.events("device_limit_reached"); n != 1 {
		t.Fatalf("limit events = %d, want 1", n)
	}
	var params string
	e.st.R.QueryRow(`SELECT params_json FROM event WHERE code = 'device_limit_reached' AND user_id = ?`, r.User.Id).Scan(&params)
	if !strings.Contains(params, `"limit":"2"`) || !strings.Contains(params, `"used":"3"`) {
		t.Errorf("event params %s, want used 3 of limit 2", params)
	}
}

// One event an hour per person, however often the refused app asks.
func TestLimitEventIsWrittenOncePerHourPerUser(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.syncAfterResponse()
	e.setLinkMode(store.LinkModeEnforce)
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	b := e.userBare("bob", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	token, tokenB := e.tokenOf(r.User.Id), e.tokenOf(b.User.Id)
	fetchAs(e, token, hintN(0), "")
	fetchAs(e, tokenB, hintN(0), "")
	for range 5 {
		fetchAs(e, token, hintN(1), "")
		fetchAs(e, tokenB, hintN(1), "")
	}
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'device_limit_reached' AND user_id = ?`, r.User.Id); n != 1 {
		t.Fatalf("events of alice = %d, want 1", n)
	}
	if n := e.events("device_limit_reached"); n != 2 {
		t.Fatalf("events = %d, want one per person", n)
	}
	e.clock = e.clock.Add(61 * time.Minute)
	fetchAs(e, token, hintN(1), "")
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'device_limit_reached' AND user_id = ?`, r.User.Id); n != 2 {
		t.Fatalf("events of alice an hour later = %d, want 2", n)
	}
}

func TestStrictRefusesAnAppWithoutADeviceID(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.setLinkMode(store.LinkModeStrict)
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	e.resetNotify()
	for _, format := range []plugin.ClientFormat{"", plugin.FormatMihomo} {
		v := fetchAs(e, token, DeviceHint{}, format)
		if v.Refusal != RefusalNoDeviceID || len(v.Lines) != 0 {
			t.Fatalf("no device ID in strict (%q): refusal %q, %d lines", format, v.Refusal, len(v.Lines))
		}
	}
	// An invalid X-HWID is no device ID either.
	if v := fetchAs(e, token, NewDeviceHint("short", "iOS", "", "", ""), ""); v.Refusal != RefusalNoDeviceID {
		t.Fatalf("an invalid HWID in strict: refusal %q", v.Refusal)
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ?`, r.User.Id); n != 0 || e.notify.n.Load() != 0 {
		t.Fatalf("refusals left %d devices and %d notifications", n, e.notify.n.Load())
	}
	if v := fetchAs(e, token, phone(), ""); v.Refusal != "" || len(v.Lines) != 1 {
		t.Fatalf("an app with a device ID in strict: refusal %q", v.Refusal)
	}
	// The page is no app: it lists the person's servers.
	if v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true})); v.Refusal != "" || len(v.Servers) != 1 {
		t.Fatalf("the page in strict: refusal %q", v.Refusal)
	}
}

func TestBlockedDeviceIsRefusedUntilUnblocked(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, tablet(), "")
	fetchAs(e, token, phone(), "")
	fetchAs(e, token, DeviceHint{}, "")
	var phoneID, sharedID, tabletID string
	phoneHash, tabletHash := storeHash(r.User.Id, phone()), storeHash(r.User.Id, tablet())
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash = ?`, r.User.Id, phoneHash).Scan(&phoneID)
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash = ?`, r.User.Id, tabletHash).Scan(&tabletID)
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash IS NULL`, r.User.Id).Scan(&sharedID)
	oldLine := fetchAs(e, token, phone(), "").Lines[0]

	// The shared device cannot be blocked: it has no HWID to refuse.
	if _, err := e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: sharedID, Block: true})); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot_block") {
		t.Fatalf("blocking the shared device = %v, want FailedPrecondition cannot_block", err)
	}
	e.resetNotify()
	resp := must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: phoneID, Block: true}))).Msg
	if resp.User.DevicesUsed != 2 || e.notify.n.Load() == 0 {
		t.Fatalf("after the block: %d devices, notified %d", resp.User.DevicesUsed, e.notify.n.Load())
	}
	if len(e.credsOf(phoneID)) != 0 {
		t.Fatal("the blocked device keeps credentials")
	}
	v := fetchAs(e, token, phone(), "")
	if v.Refusal != RefusalDeviceBlocked || len(v.Lines) != 0 {
		t.Fatalf("the blocked phone: refusal %q, %d lines", v.Refusal, len(v.Lines))
	}
	if n := len(e.devicesOf(r.User.Id)); n != 2 {
		t.Fatalf("devices = %d: the blocked phone must not come back", n)
	}
	// The others are untouched.
	if v := fetchAs(e, token, tablet(), ""); v.Refusal != "" {
		t.Fatalf("the tablet: refusal %q", v.Refusal)
	}
	// The owner sees it blocked, with a fingerprint but no HWID.
	got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: r.User.Id}))).Msg
	if len(got.BlockedDevices) != 1 || got.BlockedDevices[0].Id != phoneID || len(got.BlockedDevices[0].Fingerprint) != 6 || got.BlockedDevices[0].Platform != "ios" ||
		got.BlockedDevices[0].Client != "Happ/3.4.2" || got.BlockedDevices[0].BlockedUnix != e.clock.Unix() {
		t.Fatalf("blocked devices = %+v", got.BlockedDevices)
	}
	if got.BlockedDevices[0].Fingerprint != fmt.Sprintf("%x", phoneHash[:3]) {
		t.Errorf("fingerprint %q is not the start of the hash", got.BlockedDevices[0].Fingerprint)
	}
	for _, d := range got.Devices {
		if d.Id == phoneID {
			t.Error("the blocked device is listed among the live ones")
		}
	}

	if _, err := e.s.UnblockDevice(e.ctx, req(&adminv1.UnblockDeviceRequest{DeviceId: tabletID})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unblocking a device that is not blocked = %v, want NotFound", err)
	}
	must(e.s.UnblockDevice(e.ctx, req(&adminv1.UnblockDeviceRequest{DeviceId: phoneID})))
	back := fetchAs(e, token, phone(), "")
	if back.Refusal != "" || len(back.Lines) != 1 || back.Lines[0] == oldLine {
		t.Fatalf("after the unblock: refusal %q, lines %d, same keys as before: %v", back.Refusal, len(back.Lines), len(back.Lines) == 1 && back.Lines[0] == oldLine)
	}
	if got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: r.User.Id}))).Msg; len(got.BlockedDevices) != 0 {
		t.Errorf("blocked devices after the unblock = %+v", got.BlockedDevices)
	}

	// A plain revoke is not a block: the app registers again.
	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: tabletID})))
	if v := fetchAs(e, token, tablet(), ""); v.Refusal != "" || len(v.Lines) != 1 {
		t.Fatalf("a revoked device registering again: refusal %q", v.Refusal)
	}
	// Both are in the audit log.
	var revokes, unblocks int
	for _, a := range e.auditActions() {
		switch a {
		case "device_revoke":
			revokes++
		case "device_unblock":
			unblocks++
		}
	}
	if revokes != 2 || unblocks != 1 {
		t.Fatalf("audit: %d device_revoke and %d device_unblock, want 2 and 1 (%v)", revokes, unblocks, e.auditActions())
	}
	for _, r := range must(e.st.ListAudit(e.ctx, "", 0, 100)) {
		if r.Action == "device_revoke" && strings.Contains(r.Params, `"block":true`) {
			var p map[string]any
			if json.Unmarshal([]byte(r.Params), &p) != nil || p["user"] != resp.User.Id || p["device"] != phoneID {
				t.Errorf("audit params of the block: %s", r.Params)
			}
		}
	}
}

// A silent device gives its place away in ENFORCE and STRICT, and the owner can read it in the events.
func TestSilentDeviceGivesItsPlaceInEnforce(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.syncAfterResponse()
	e.setLinkMode(store.LinkModeEnforce)
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, hintN(0), "")
	fetchAs(e, token, hintN(1), "")
	if v := fetchAs(e, token, hintN(2), ""); v.Refusal != RefusalDeviceLimit {
		t.Fatalf("no silent device: refusal %q", v.Refusal)
	}
	e.sql(`UPDATE device SET last_seen_at = ? WHERE user_id = ? AND hwid_hash = ?`, e.clock.Add(-31*24*time.Hour).Unix(), r.User.Id, storeHash(r.User.Id, hintN(0)))
	e.resetNotify()
	v := fetchAs(e, token, hintN(2), "")
	if v.Refusal != "" || len(v.Lines) != 1 {
		t.Fatalf("a device silent for 31 days did not give its place: refusal %q", v.Refusal)
	}
	if e.notify.n.Load() == 0 {
		t.Error("the replacement did not notify the nodes")
	}
	if got := e.devicesOf(r.User.Id); len(got) != 2 {
		t.Fatalf("devices = %v, want 2", got)
	}
	// The old one is refused now (a new HWID with no place), the new one is served.
	if v := fetchAs(e, token, hintN(0), ""); v.Refusal != RefusalDeviceLimit {
		t.Fatalf("the device that gave its place: refusal %q", v.Refusal)
	}
	if n := e.events("device_replaced"); n != 1 {
		t.Fatalf("device_replaced events = %d, want 1", n)
	}
}

// A refused fetch costs the response nothing: no write on its path, even with the writer taken by somebody else (the event
// is written after the response). The same holds for the three reasons.
func TestRefusedFetchIsReadOnly(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.s.afterResponse = func(func()) {} // the event is another test's; here only the response path is under test
	r := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	token := e.tokenOf(r.User.Id)
	e.setLinkMode(store.LinkModeEnforce)
	for n := range 2 {
		fetchAs(e, token, hintN(n), "")
	}
	var blockedID string
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash = ?`, r.User.Id, storeHash(r.User.Id, hintN(1))).Scan(&blockedID)
	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: blockedID, Block: true})))
	fetchAs(e, token, hintN(3), "") // fills the place the block freed
	cases := []struct {
		name, want string
		hint       DeviceHint
		strict     bool
	}{
		{"limit", RefusalDeviceLimit, hintN(4), false},
		{"blocked", RefusalDeviceBlocked, hintN(1), false},
		{"no device id", RefusalNoDeviceID, DeviceHint{}, true},
	}
	for _, c := range cases {
		if c.strict {
			e.setLinkMode(store.LinkModeStrict) // before the writer is taken
		}
		w, err := e.st.W.Conn(e.ctx) // "somebody else" holds the only writer connection
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			start := time.Now()
			v := fetchAs(e, token, c.hint, "")
			if v.Refusal != c.want {
				t.Fatalf("%s: refusal %q, want %q", c.name, v.Refusal, c.want)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("%s: a refusal waited %v for the writer", c.name, d)
			}
		}
		w.Close()
	}
}

func TestSelfServiceRemovesAndRenamesAnyOwnDeviceButTheSharedOne(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, nil)
	other := e.userBare("bob", f.group, nil)
	token, otherToken := e.tokenOf(r.User.Id), e.tokenOf(other.User.Id)
	fetchAs(e, token, phone(), "")
	fetchAs(e, token, tablet(), "")
	fetchAs(e, token, DeviceHint{}, "")
	fetchAs(e, otherToken, phone(), "")
	var phoneID, sharedID, theirs string
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash = ?`, r.User.Id, storeHash(r.User.Id, phone())).Scan(&phoneID)
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash IS NULL`, r.User.Id).Scan(&sharedID)
	e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ?`, other.User.Id).Scan(&theirs)

	by := "user:" + r.User.Id
	dev, err := e.s.RelabelDevice(e.ctx, r.User.Id, phoneID, "  Anna's phone ")
	if err != nil || dev.ID != phoneID || dev.Model != "Anna's phone" || dev.AWG {
		t.Fatalf("RelabelDevice = %+v, %v", dev, err)
	}
	if got := e.count(`SELECT count(*) FROM device WHERE id = ? AND model = 'Anna''s phone'`, phoneID); got != 1 {
		t.Fatal("the label was not stored")
	}
	for name, err := range map[string]error{
		"rename the shared device": func() error { _, err := e.s.RelabelDevice(e.ctx, r.User.Id, sharedID, "x"); return err }(),
		"revoke the shared device": e.s.RevokeOwnDevice(e.ctx, by, r.User.Id, sharedID),
	} {
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "shared_device") {
			t.Errorf("%s = %v, want FailedPrecondition shared_device", name, err)
		}
	}
	for name, err := range map[string]error{
		"rename another person's device": func() error { _, err := e.s.RelabelDevice(e.ctx, r.User.Id, theirs, "x"); return err }(),
		"revoke another person's device": e.s.RevokeOwnDevice(e.ctx, by, r.User.Id, theirs),
		"revoke an unknown device":       e.s.RevokeOwnDevice(e.ctx, by, r.User.Id, "dev_nope"),
	} {
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s = %v, want NotFound", name, err)
		}
	}
	if got := e.count(`SELECT count(*) FROM device WHERE id = ? AND revoked_at IS NULL`, theirs); got != 1 {
		t.Fatal("a person removed another person's device")
	}
	if got := e.count(`SELECT count(*) FROM device WHERE id = ? AND model <> 'x'`, theirs); got != 1 {
		t.Fatal("a person renamed another person's device")
	}

	e.resetNotify()
	if err := e.s.RevokeOwnDevice(e.ctx, by, r.User.Id, phoneID); err != nil {
		t.Fatal(err)
	}
	if e.notify.n.Load() == 0 || len(e.credsOf(phoneID)) != 0 || len(e.devicesOf(r.User.Id)) != 2 {
		t.Fatalf("after removing the phone: notified %d, credentials %v, devices %v", e.notify.n.Load(), e.credsOf(phoneID), e.devicesOf(r.User.Id))
	}
	if err := e.s.RevokeOwnDevice(e.ctx, by, r.User.Id, phoneID); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("removing it twice = %v, want NotFound", err)
	}
	// Removing is no block: the app is back as a new device.
	if v := fetchAs(e, token, phone(), ""); v.Refusal != "" || len(e.devicesOf(r.User.Id)) != 3 {
		t.Fatalf("the removed app registering again: refusal %q, devices %v", v.Refusal, e.devicesOf(r.User.Id))
	}
	var audited bool
	for _, row := range must(e.st.ListAudit(e.ctx, "", 0, 100)) {
		audited = audited || row.Action == "device_revoke" && row.Actor == by && strings.Contains(row.Params, phoneID)
	}
	if !audited {
		t.Errorf("the removal is not in the audit log: %v", e.auditActions())
	}
	// The admin's rename takes the device of an app too.
	renamed := must(e.s.RenameDevice(e.ctx, req(&adminv1.RenameDeviceRequest{DeviceId: theirs, Label: "Bob's"}))).Msg.Device
	if renamed.Id != theirs || renamed.Model != "Bob's" {
		t.Fatalf("RenameDevice = %+v", renamed)
	}
}

func TestPageDevicesSayWhatThePersonMayDo(t *testing.T) {
	f := newFixture(t)
	e := f.e
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, phone(), "")
	fetchAs(e, token, DeviceHint{}, "")
	kinds := map[string]int{}
	for _, d := range must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true})).Devices {
		kinds[d.Kind]++
	}
	if kinds["link"] != 1 || kinds["shared"] != 1 || len(kinds) != 2 {
		t.Fatalf("device kinds on the page = %v, want one link and one shared", kinds)
	}
}

func TestOverDeviceLimitFilterAndBadge(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.syncAfterResponse()
	over := e.userBare("over", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	e.userBare("fine", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 5 })
	for n := range 2 { // TRACK lets 2 devices of a limit of 1 through
		fetchAs(e, e.tokenOf(over.User.Id), hintN(n), "")
	}
	resp := must(e.s.ListUsers(e.ctx, req(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_OVER_DEVICE_LIMIT}))).Msg
	if len(resp.Users) != 1 || resp.Users[0].Id != over.User.Id || resp.Counts.OverDeviceLimit != 1 || resp.Counts.All != 2 {
		t.Fatalf("filter over_device_limit: %d users, counts %+v", len(resp.Users), resp.Counts)
	}
	all := must(e.s.ListUsers(e.ctx, req(&adminv1.ListUsersRequest{}))).Msg
	if len(all.Users) != 2 || all.Counts.OverDeviceLimit != 1 {
		t.Fatalf("unfiltered: %d users, counts %+v", len(all.Users), all.Counts)
	}
}

func TestAddAWGDeviceErrorNamesTheDevicesThatFillTheLimit(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.sql(`UPDATE user SET device_limit = 2 WHERE id = ?`, f.user)
	f.add(f.user, "Kitchen router")
	e.clock = e.clock.Add(time.Second) // the devices are listed in the order they were made
	f.add(f.user, "Laptop")
	_, err := e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: f.user, ProfileId: f.profile, Platform: "ios", Label: "third"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "device_limit: 2/2: Kitchen router, Laptop") {
		t.Fatalf("the error = %v, want the limit and the devices that fill it", err)
	}
}
