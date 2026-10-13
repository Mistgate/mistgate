package subs_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// What an app that is refused gets: HTTP 200 with one entry that says why, and the headers the apps know.

func (r *rig) setLinkMode(mode string) {
	r.t.Helper()
	if err := r.st.SetSettings(r.ctx, map[string]string{store.SettingLinkDeviceLimit: mode}); err != nil {
		r.t.Fatal(err)
	}
}

func refusalRig(t *testing.T, mode string, limit uint32) (r *rig, h http.Handler, uid, tok string) {
	t.Helper()
	r = newRig(t, "/k3xq8")
	hh, _ := r.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1; c.AfterResponse = func(f func()) { f() } })
	uid, tok = r.user("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = limit })
	r.setLinkMode(mode)
	return r, hh, uid, tok
}

func TestRefusalIsAnEntryThatSaysWhy(t *testing.T) {
	r, h, uid, tok := refusalRig(t, store.LinkModeEnforce, 1)
	if rec := fetch(h, "/"+tok, happUA, "X-HWID", hwidA); rec.Code != 200 || rec.Header().Get("X-Hwid-Active") != "" {
		t.Fatalf("the first device: %d, X-Hwid-Active %q", rec.Code, rec.Header().Get("X-Hwid-Active"))
	}
	writes := func() int {
		var n int
		r.st.R.QueryRow(`SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential)`).Scan(&n)
		return n
	}
	before := writes()

	rec := fetch(h, "/"+tok, happUA, "X-HWID", hwidB, "X-Device-OS", "Android")
	if rec.Code != 200 {
		t.Fatalf("a refused device must get 200 (the apps hide the reason otherwise), got %d", rec.Code)
	}
	lines := strings.Split(decode(t, rec.Body.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "hysteria2://off@0.0.0.0:1/#") {
		t.Fatalf("the refusal body is %q, want the one placeholder entry", lines)
	}
	const note = "Device limit: 1 of 1. Remove one you no longer use on the subscription page"
	if want := "#" + strings.ReplaceAll(note, " ", "%20"); lines[0] != "hysteria2://off@0.0.0.0:1/"+want {
		t.Errorf("the entry is named %q, want %q", lines[0], want)
	}
	hd := rec.Header()
	if got := unb64(t, hd.Get("Announce")); got != note {
		t.Errorf("Announce = %q", got)
	}
	for name, want := range map[string]string{
		"Profile-Update-Interval": "1", "X-Hwid-Active": "true", "X-Hwid-Max-Devices-Reached": "true", "X-Hwid-Limit": "1",
	} {
		if got := hd.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if hd.Get("X-Hwid-Not-Supported") != "" || !strings.HasSuffix(hd.Get("Profile-Web-Page-Url"), "/"+tok) {
		t.Errorf("Not-Supported %q, page url %q", hd.Get("X-Hwid-Not-Supported"), hd.Get("Profile-Web-Page-Url"))
	}
	if writes() != before {
		t.Error("the refusal wrote devices or credentials")
	}
	var devices int
	r.st.R.QueryRow(`SELECT count(*) FROM device WHERE user_id = ?`, uid).Scan(&devices)
	if devices != 1 {
		t.Fatalf("devices = %d, want the one", devices)
	}

	// The same in the Mihomo profile: one proxy named by the reason.
	rec = fetch(h, "/"+tok, mihomoUA, "X-HWID", hwidB)
	if rec.Code != 200 || rec.Header().Get("X-Hwid-Max-Devices-Reached") != "true" || rec.Header().Get("Profile-Update-Interval") != "1" {
		t.Fatalf("mihomo refusal: %d %v", rec.Code, rec.Header())
	}
	p := parseYAML(t, rec.Body.Bytes())
	if len(p.Proxies) != 1 || p.Proxies[0]["name"] != note || p.Proxies[0]["server"] != "0.0.0.0" {
		t.Fatalf("mihomo proxies = %v, want the placeholder named by the reason", p.Proxies)
	}

	// The known device is served, without the refusal headers; the page (a browser) does not carry them either.
	rec = fetch(h, "/"+tok, happUA, "X-HWID", hwidA)
	if !strings.HasPrefix(decode(t, rec.Body.String()), "hysteria2://") || strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") || rec.Header().Get("X-Hwid-Active") != "" {
		t.Fatalf("the known device at the limit: %q %v", decode(t, rec.Body.String()), rec.Header())
	}
	if rec := fetch(h, "/"+tok, chrome, "X-HWID", hwidB); rec.Header().Get("X-Hwid-Active") != "" || strings.Contains(rec.Body.String(), "Device limit") {
		t.Fatalf("the page carries the refusal: %v", rec.Header())
	}
}

// A refused answer is cached like any other (MinInterval), per device: the device that is let in is not given the refusal of
// another, and the one refused is not given the keys of another.
func TestRefusalIsCachedPerDevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) {
		c.MinInterval = 10 * time.Minute
		c.MaxPerHour = -1
		c.AfterResponse = func(f func()) { f() }
	})
	_, tok := r.user("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	r.setLinkMode(store.LinkModeEnforce)
	ok := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Body.String())
	refused := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidB).Body.String())
	if !strings.Contains(refused, "0.0.0.0") || strings.Contains(ok, "0.0.0.0") {
		t.Fatalf("ok %q, refused %q", ok, refused)
	}
	again := fetch(h, "/"+tok, happUA, "X-HWID", hwidB)
	if decode(t, again.Body.String()) != refused || again.Header().Get("X-Hwid-Max-Devices-Reached") != "true" {
		t.Fatalf("the refusal from the cache: %q %v", decode(t, again.Body.String()), again.Header())
	}
	if got := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Body.String()); got != ok {
		t.Fatalf("the device that was let in: %q", got)
	}
}

func TestStrictAnswersAnAppWithoutADeviceID(t *testing.T) {
	_, h, _, tok := refusalRig(t, store.LinkModeStrict, 5)
	rec := fetch(h, "/"+tok, happUA)
	if rec.Code != 200 || !strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") {
		t.Fatalf("no device ID in strict: %d %q", rec.Code, decode(t, rec.Body.String()))
	}
	hd := rec.Header()
	if hd.Get("X-Hwid-Active") != "true" || hd.Get("X-Hwid-Not-Supported") != "true" || hd.Get("X-Hwid-Max-Devices-Reached") != "" || hd.Get("X-Hwid-Limit") != "5" || hd.Get("Profile-Update-Interval") != "1" {
		t.Errorf("headers = %v", hd)
	}
	note := unb64(t, hd.Get("Announce"))
	if !strings.Contains(note, "kl!ck") || !strings.Contains(note, "HWID") {
		t.Errorf("Announce %q does not tell the way out", note)
	}
	if rec := fetch(h, "/"+tok, happUA, "X-HWID", hwidA); strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") {
		t.Fatalf("an app with a device ID is refused in strict")
	}
}

func TestBlockedDeviceAnswer(t *testing.T) {
	r, h, uid, tok := refusalRig(t, store.LinkModeTrack, 5)
	fetch(h, "/"+tok, happUA, "X-HWID", hwidA)
	var id string
	r.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ?`, uid).Scan(&id)
	must(r.svc.RevokeDevice(r.ctx, connect.NewRequest(&adminv1.RevokeDeviceRequest{DeviceId: id, Block: true})))
	rec := fetch(h, "/"+tok, happUA, "X-HWID", hwidA)
	if rec.Code != 200 || !strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") || unb64(t, rec.Header().Get("Announce")) != "This device was turned off by the owner" ||
		rec.Header().Get("X-Hwid-Active") != "true" || rec.Header().Get("Profile-Update-Interval") != "1" ||
		rec.Header().Get("X-Hwid-Max-Devices-Reached") != "" { // not the limit: no "free a place"
		t.Fatalf("blocked device: %d %q %v", rec.Code, decode(t, rec.Body.String()), rec.Header())
	}
	must(r.svc.UnblockDevice(r.ctx, connect.NewRequest(&adminv1.UnblockDeviceRequest{DeviceId: id})))
	if rec := fetch(h, "/"+tok, happUA, "X-HWID", hwidA); strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") {
		t.Fatal("still refused after the unblock")
	}
}

// An X-HWID the panel cannot use is answered as an app with no device ID: served from the shared device, and told so.
func TestInvalidHWIDGetsNotSupported(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = 10 * time.Minute; c.MaxPerHour = -1 })
	_, tok := r.user("alice", nil)
	for i := range 2 { // the second answer comes from the cache and still carries the header
		rec := fetch(h, "/"+tok, happUA, "X-HWID", "short")
		if rec.Code != 200 || rec.Header().Get("X-Hwid-Not-Supported") != "true" || rec.Header().Get("X-Hwid-Active") != "" {
			t.Fatalf("invalid HWID, fetch %d: %d %v", i, rec.Code, rec.Header())
		}
	}
	if rec := fetch(h, "/"+tok, mihomoUA, "X-HWID", strings.Repeat("a", 65)); rec.Header().Get("X-Hwid-Not-Supported") != "true" {
		t.Errorf("mihomo: %v", rec.Header())
	}
	for name, rec := range map[string]interface{ Header() http.Header }{
		"valid":     fetch(h, "/"+tok, happUA, "X-HWID", hwidA),
		"no header": fetch(h, "/"+tok, happUA),
		"browser":   fetch(h, "/"+tok, chrome, "X-HWID", "short"),
	} {
		if rec.Header().Get("X-Hwid-Not-Supported") != "" {
			t.Errorf("%s: X-Hwid-Not-Supported = %q", name, rec.Header().Get("X-Hwid-Not-Supported"))
		}
	}
}

type notified struct{ n int }

func (f *notified) StateChanged() { f.n++ }

func TestSwitchToStrictRevokesTheSharedDevices(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, cache := r.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	for _, name := range []string{"alice", "bob"} {
		_, tok := r.user(name, nil)
		fetch(h, "/"+tok, happUA) // the shared device
	}
	_, withHWID := r.user("carol", nil)
	fetch(h, "/"+withHWID, happUA, "X-HWID", hwidA)
	svc := subs.NewService(r.st, cache, builtin.Registry(), func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, r.st) }, nil, nil)
	tell := &notified{}
	svc.NotifyWith(tell)
	ctx := context.Background()
	get := func() *adminv1.GetSubscriptionSettingsResponse {
		return must(svc.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg
	}

	// An existing install is counted only; an empty one refuses (here, users were made after the cache: ENFORCE).
	g := get()
	if g.SharedLinkUsers != 2 || g.Settings.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE {
		t.Fatalf("settings before: %d shared users, mode %v", g.SharedLinkUsers, g.Settings.GetLinkDeviceLimit())
	}
	admin := auth.WithAdmin(ctx, store.Admin{ID: "adm_1"})
	// A save without a mode leaves the stored one.
	set := g.Settings
	set.LinkDeviceLimit = nil
	must(svc.UpdateSubscriptionSettings(admin, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set})))
	if g := get(); g.Settings.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE || tell.n != 0 {
		t.Fatalf("a save without a mode: mode %v, notified %d", g.Settings.GetLinkDeviceLimit(), tell.n)
	}
	bad := adminv1.LinkDeviceLimit(99)
	set.LinkDeviceLimit = &bad
	if _, err := svc.UpdateSubscriptionSettings(admin, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an unknown mode = %v, want InvalidArgument", err)
	}

	set.LinkDeviceLimit = adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_STRICT.Enum()
	resp := must(svc.UpdateSubscriptionSettings(admin, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))).Msg
	if resp.Settings.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_STRICT || tell.n != 1 {
		t.Fatalf("after the switch: mode %v, notified %d", resp.Settings.GetLinkDeviceLimit(), tell.n)
	}
	if g := get(); g.SharedLinkUsers != 0 || g.Settings.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_STRICT {
		t.Fatalf("settings after: %d shared users, mode %v", g.SharedLinkUsers, g.Settings.GetLinkDeviceLimit())
	}
	var live int
	r.st.R.QueryRow(`SELECT count(*) FROM device WHERE revoked_at IS NULL`).Scan(&live)
	if live != 1 {
		t.Fatalf("live devices after the switch = %d, want carol's one", live)
	}
	// The apps that report none are refused at once; carol's goes on.
	_, tok := r.user("dave", nil)
	if rec := fetch(h, "/"+tok, happUA); !strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") {
		t.Fatalf("strict: an app without a device ID got servers")
	}
	if rec := fetch(h, "/"+withHWID, happUA, "X-HWID", hwidA); strings.Contains(decode(t, rec.Body.String()), "0.0.0.0") {
		t.Fatal("strict: a device with an ID lost its servers")
	}
	// Back to ENFORCE changes nothing on the nodes.
	set.LinkDeviceLimit = adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE.Enum()
	must(svc.UpdateSubscriptionSettings(admin, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set})))
	if tell.n != 1 {
		t.Errorf("notified %d times, want once (for the switch to strict)", tell.n)
	}
}

func TestPageSaysWhatThePersonMayDoWithTheirDevices(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	_, tok := r.user("alice", nil)
	fetch(h, "/"+tok, happUA, "X-HWID", hwidA, "X-Device-OS", "iOS")
	fetch(h, "/"+tok, happUA) // the shared device
	data, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if !strings.HasSuffix(data["devices_endpoint"].(string), "/"+tok+"/devices") {
		t.Errorf("devices_endpoint = %v", data["devices_endpoint"])
	}
	kinds := map[string]map[string]any{}
	for _, d := range data["devices"].([]any) {
		m := d.(map[string]any)
		kinds[m["kind"].(string)] = m
	}
	if len(kinds) != 2 || kinds["link"]["can_remove"] != true || kinds["link"]["can_rename"] != true || kinds["shared"]["can_remove"] != false || kinds["shared"]["can_rename"] != false {
		t.Fatalf("devices = %v", data["devices"])
	}
}
