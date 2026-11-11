package access

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The list of devices below the line: only devices that receive a VLESS REALITY server, judged by the profile fingerprints their
// person can use (the worst wins), counts of every state, the filters, the pages, and the owner's changes to the table.

type compatEnv struct {
	*env
	vless map[string]string // user name -> id
	seq   uint64            // the stats batches of hello
}

// fetchWith lets an app with a device ID fetch the person's subscription once: the device and its credentials are made.
func (c *compatEnv) fetchWith(user, hwid, os, ua string) {
	c.t.Helper()
	token := c.tokenOf(c.vless[user])
	must(c.s.SubscriptionWith(c.ctx, token, SubOptions{Device: NewDeviceHint(hwid, os, "", "", ua)}))
}

func (c *compatEnv) person(name, group string) {
	c.t.Helper()
	c.vless[name] = c.userBare(name, group, nil).User.Id
}

func (c *compatEnv) realityProfile(name, fingerprint string) string {
	c.t.Helper()
	settings := `{"reality":{"target":"www.example.org:443"` + map[bool]string{true: `,"fingerprint":"` + fingerprint + `"`, false: ""}[fingerprint != ""] + `}}`
	return must(c.s.CreateProfile(c.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: name, SettingsJson: settings}))).Msg.Profile.Id
}

func (c *compatEnv) list(m *adminv1.ListOutdatedDevicesRequest) *adminv1.ListOutdatedDevicesResponse {
	c.t.Helper()
	return must(c.s.ListOutdatedDevices(c.ctx, req(m))).Msg
}

func newCompatEnv(t *testing.T) *compatEnv {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	return &compatEnv{env: e, vless: map[string]string{}}
}

func namesOf(r *adminv1.ListOutdatedDevicesResponse) []string {
	var out []string
	for _, d := range r.Devices {
		out = append(out, d.UserName+":"+d.State.String()[len("CLIENT_COMPAT_STATE_"):])
	}
	return out
}

func countOf(r *adminv1.ListOutdatedDevicesResponse, st adminv1.ClientCompatState) (devices, people uint32) {
	for _, c := range r.Counts {
		if c.State == st {
			return c.Devices, c.People
		}
	}
	return 0, 0
}

func TestOutdatedDevicesListsOnlyWhatReceivesReality(t *testing.T) {
	c := newCompatEnv(t)
	reality := c.realityProfile("reality", "")
	c.inbound(reality, "nod_de1")
	hy2 := c.profile("hy2", "").Id
	c.inbound(hy2, "nod_nl1")
	realityGroup := c.group("reality", reality)
	plainGroup := c.group("plain", hy2)

	for _, p := range []struct{ name, hwid, os, ua string }{
		{"alice", "aaaaaaaaaa01", "Android", "Happ/2.7.1"},    // below
		{"bob", "bbbbbbbbbb01", "Android", "SFA/1.14.3"},      // never
		{"carol", "cccccccccc01", "Android", "Happ/3.4.2"},    // ok
		{"dave", "dddddddddd01", "Windows", "SuperVPN/1.0"},   // unknown app
		{"frank", "ffffffffff01", "Android", "SFA/1.14.3"},    // disabled
		{"grace", "gggggggggg01", "Android", "SFA/1.14.3"},    // device blocked
		{"heidi", "hhhhhhhhhh01", "iOS", "Shadowrocket/2086"}, // unknown version
	} {
		c.person(p.name, realityGroup)
		c.fetchWith(p.name, p.hwid, p.os, p.ua)
	}
	// a second device of alice: a good Mihomo; and a person on a hysteria2-only group has no VLESS credential at all
	c.fetchWith("alice", "aaaaaaaaaa02", "Windows", "mihomo/1.19.33")
	c.person("erin", plainGroup)
	c.fetchWith("erin", "eeeeeeeeee01", "Android", "SFA/1.14.3")
	c.sql(`UPDATE user SET disabled = 1, status = 'disabled' WHERE id = ?`, c.vless["frank"])
	c.sql(`UPDATE device SET blocked_at = 1 WHERE user_id = ?`, c.vless["grace"])

	all := c.list(&adminv1.ListOutdatedDevicesRequest{})
	// worst first: unsupported, below, unknown; ok is not in the default list
	if got := namesOf(all); !slices.Equal(got, []string{"bob:UNSUPPORTED", "alice:BELOW_MINIMUM", "dave:UNKNOWN", "heidi:UNKNOWN"}) && !slices.Equal(got, []string{"bob:UNSUPPORTED", "alice:BELOW_MINIMUM", "heidi:UNKNOWN", "dave:UNKNOWN"}) {
		t.Fatalf("default list = %v", got)
	}
	if all.Total != 4 || !slices.Equal(all.Fingerprints, []string{"chrome"}) {
		t.Errorf("total %d, fingerprints %v", all.Total, all.Fingerprints)
	}
	if d, p := countOf(all, adminv1.ClientCompatState_CLIENT_COMPAT_STATE_OK); d != 2 || p != 2 { // carol, alice's Mihomo
		t.Errorf("ok: %d devices, %d people", d, p)
	}
	if d, p := countOf(all, adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM); d != 1 || p != 1 {
		t.Errorf("below: %d devices, %d people", d, p)
	}
	if d, p := countOf(all, adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNSUPPORTED); d != 1 || p != 1 {
		t.Errorf("unsupported: %d devices, %d people", d, p)
	}
	if d, p := countOf(all, adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNKNOWN); d != 2 || p != 2 {
		t.Errorf("unknown: %d devices, %d people", d, p)
	}
	if len(all.Counts) != 4 {
		t.Errorf("counts = %v", all.Counts)
	}
	for _, d := range all.Devices {
		if d.UserName == "frank" || d.UserName == "grace" || d.UserName == "erin" {
			t.Errorf("%s is not served a REALITY server but is listed", d.UserName)
		}
	}
	first := all.Devices[0]
	if first.Reason != ReasonApp || first.Client != "SFA/1.14.3" || first.Platform != "android" || first.Fingerprint != "chrome" || first.AppTitle == "" || first.LastSeenUnix == 0 {
		t.Errorf("first row = %+v", first)
	}
	if below := all.Devices[1]; below.Reason != ReasonVersion || below.NeedVersion != "3.1.1" || below.UserId != c.vless["alice"] {
		t.Errorf("below row = %+v", below)
	}

	// the filters
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{State: adminv1.ClientCompatState_CLIENT_COMPAT_STATE_OK}); r.Total != 2 {
		t.Errorf("ok list: %v", namesOf(r))
	}
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{State: adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNKNOWN}); r.Total != 2 {
		t.Errorf("unknown list: %v", namesOf(r))
	}
	one := c.list(&adminv1.ListOutdatedDevicesRequest{UserId: c.vless["alice"], State: adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM})
	if one.Total != 1 || one.Devices[0].UserName != "alice" {
		t.Errorf("one person: %v", namesOf(one))
	}
	if d, _ := countOf(one, adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNSUPPORTED); d != 1 { // the counts do not depend on the filters
		t.Errorf("counts follow the filter: %v", one.Counts)
	}
	// pages
	p1 := c.list(&adminv1.ListOutdatedDevicesRequest{PageSize: 3})
	p2 := c.list(&adminv1.ListOutdatedDevicesRequest{PageSize: 3, Offset: 3})
	if len(p1.Devices) != 3 || len(p2.Devices) != 1 || p1.Total != 4 || p2.Total != 4 || p1.Devices[2].DeviceId == p2.Devices[0].DeviceId {
		t.Errorf("pages: %v / %v", namesOf(p1), namesOf(p2))
	}
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{Offset: 99}); len(r.Devices) != 0 || r.Total != 4 {
		t.Errorf("a page past the end: %v", namesOf(r))
	}
	// bad requests
	_, err := c.s.ListOutdatedDevices(c.ctx, req(&adminv1.ListOutdatedDevicesRequest{State: 99}))
	wantCode(t, err, connect.CodeInvalidArgument)
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{UserId: "usr_nobody"}); r.Total != 0 || len(r.Counts) != 4 { // an unknown person has no devices
		t.Errorf("unknown person: %v", namesOf(r))
	}
}

// The fingerprint comes from the profiles the person can use; where there are several, the worst verdict stands.
func TestOutdatedDevicesJudgeByTheProfilesFingerprints(t *testing.T) {
	c := newCompatEnv(t)
	chrome := c.realityProfile("chrome", "chrome")
	firefox := c.realityProfile("firefox", "firefox")
	ios := c.realityProfile("ios", "ios")
	c.node("nod_se1", "se1", "se1.example.com", "active")
	c.inbound(chrome, "nod_de1")
	c.inbound(firefox, "nod_nl1")
	c.inbound(ios, "nod_se1")
	chromeOnly := c.group("chrome-only", chrome)
	both := c.group("both", chrome, firefox)
	classical := c.group("classical", chrome, ios)

	c.person("alice", chromeOnly)
	c.fetchWith("alice", "aaaaaaaaaa01", "Android", "Happ/3.4.2") // chrome: ok
	c.person("bob", both)
	c.fetchWith("bob", "bbbbbbbbbb01", "Android", "Happ/3.4.2") // chrome ok, firefox needs 3.20.0: below
	c.person("carol", both)
	c.fetchWith("carol", "cccccccccc01", "Android", "Happ/3.20.0") // both ok
	c.person("dave", classical)
	c.fetchWith("dave", "dddddddddd01", "iOS", "Happ/6.0.0") // a good app, but one profile of the group sends a classical hello

	r := c.list(&adminv1.ListOutdatedDevicesRequest{})
	if got := namesOf(r); !slices.Equal(got, []string{"dave:UNSUPPORTED", "bob:BELOW_MINIMUM"}) {
		t.Fatalf("list = %v", got)
	}
	if d := r.Devices[0]; d.Reason != ReasonFingerprint || d.Fingerprint != "ios" || d.NeedVersion != "" {
		t.Errorf("dave = %+v", d)
	}
	if d := r.Devices[1]; d.Fingerprint != "firefox" || d.NeedVersion != "3.20.0" {
		t.Errorf("bob = %+v", d)
	}
	if ok, _ := countOf(r, adminv1.ClientCompatState_CLIENT_COMPAT_STATE_OK); ok != 2 {
		t.Errorf("ok devices = %d (alice, carol)", ok)
	}
	if !slices.Equal(r.Fingerprints, []string{"chrome", "firefox", "ios"}) {
		t.Errorf("fingerprints in use = %v", r.Fingerprints)
	}
}

// A server that is not live (its node is not active) is not handed out, so its fingerprint judges nobody.
func TestOutdatedDevicesIgnoreServersThatAreNotLive(t *testing.T) {
	c := newCompatEnv(t)
	chrome := c.realityProfile("chrome", "chrome")
	ios := c.realityProfile("ios", "ios")
	c.inbound(chrome, "nod_de1")
	c.inbound(ios, "nod_nl1")
	g := c.group("both", chrome, ios)
	c.person("alice", g)
	c.fetchWith("alice", "aaaaaaaaaa01", "Android", "Happ/3.4.2")
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{}); r.Total != 1 || r.Devices[0].Fingerprint != "ios" {
		t.Fatalf("with both live: %+v", r.Devices)
	}
	c.sql(`UPDATE node SET state = 'pending' WHERE id = 'nod_nl1'`)
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{}); r.Total != 0 || !slices.Equal(r.Fingerprints, []string{"chrome"}) || len(r.Counts) != 4 {
		t.Fatalf("with the ios node down: %+v / %v / %v", r.Devices, r.Fingerprints, r.Counts)
	}
	// nothing at all to judge: an empty panel answers with empty counts, not an error
	empty := newCompatEnv(t)
	if r := empty.list(&adminv1.ListOutdatedDevicesRequest{}); r.Total != 0 || len(r.Devices) != 0 || len(r.Counts) != 4 {
		t.Errorf("empty panel: %+v", r)
	}
}

func ownerCtx(ctx context.Context) context.Context {
	return auth.WithAdmin(ctx, store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

// The owner's changes: owner only, with a step-up, audited, stored as the difference, and they change what the list says.
func TestClientCompatUpdateIsOwnerOnlySteppedUpAndAudited(t *testing.T) {
	c := newCompatEnv(t)
	reality := c.realityProfile("reality", "")
	c.inbound(reality, "nod_de1")
	c.person("alice", c.group("g", reality))
	c.fetchWith("alice", "aaaaaaaaaa01", "Android", "Happ/3.2.0")
	steps := 0
	c.s.cfg.StepUp = func(context.Context) error { steps++; return nil }

	got := must(c.s.GetClientCompat(c.ctx, req(&adminv1.GetClientCompatRequest{}))).Msg
	if len(got.Rules) != len(clientDefaults) || len(got.Defaults) != len(clientDefaults) || got.Rules[0].Title == "" {
		t.Fatalf("table: %d rules, %d defaults", len(got.Rules), len(got.Defaults))
	}
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{}); r.Total != 0 {
		t.Fatalf("Happ 3.2.0 on Android with chrome is fine, listed: %v", namesOf(r))
	}

	// raise the Happ Android minimum for chrome and add an app of the owner's own
	want := slices.Clone(got.Rules)
	for _, r := range want {
		if r.App == "happ" && r.Platform == "android" {
			r.MinChrome = "3.3.0"
		}
	}
	want = append(want, &adminv1.ClientCompatRule{App: "SuperVPN", Core: "xray", MinChrome: "1.0"})
	update := func(ctx context.Context, rules []*adminv1.ClientCompatRule) error {
		_, err := c.s.UpdateClientCompat(ctx, req(&adminv1.UpdateClientCompatRequest{Rules: rules}))
		return err
	}
	for _, denied := range []context.Context{
		c.ctx,
		auth.WithAdmin(c.ctx, store.Admin{ID: "adm_helper", Role: store.RoleHelper}),
		auth.WithAdmin(c.ctx, store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		auth.WithAdmin(c.ctx, store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		wantCode(t, update(denied, want), connect.CodePermissionDenied)
	}
	if steps != 0 {
		t.Fatalf("a refused change asked for a step-up %d times", steps)
	}
	if err := update(ownerCtx(c.ctx), want); err != nil || steps != 1 {
		t.Fatalf("owner's update: %v, step-ups %d", err, steps)
	}
	raw, err := c.st.Setting(c.ctx, store.SettingClientCompat)
	if err != nil || strings.Count(raw, `"app"`) != 2 || !strings.Contains(raw, `"min_chrome":"3.3.0"`) || !strings.Contains(raw, `"supervpn"`) {
		t.Fatalf("stored %q, %v", raw, err)
	}
	r := c.list(&adminv1.ListOutdatedDevicesRequest{})
	if r.Total != 1 || r.Devices[0].State != adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM || r.Devices[0].NeedVersion != "3.3.0" {
		t.Fatalf("after the change: %+v", r.Devices)
	}
	after := must(c.s.GetClientCompat(c.ctx, req(&adminv1.GetClientCompatRequest{}))).Msg
	if len(after.Rules) != len(clientDefaults)+1 || len(after.Defaults) != len(clientDefaults) {
		t.Errorf("table after: %d rules, %d defaults", len(after.Rules), len(after.Defaults))
	}

	// audited
	var actor, action, params string
	if err := c.st.R.QueryRowContext(c.ctx, `SELECT actor, action, params FROM audit WHERE action = 'client_compat.update'`).Scan(&actor, &action, &params); err != nil {
		t.Fatal(err)
	}
	if actor != "adm_owner" || !strings.Contains(params, `"changed":2`) || !strings.Contains(params, "happ/android") || !strings.Contains(params, "supervpn") {
		t.Errorf("audit: %s %s %s", actor, action, params)
	}

	// a refused table stores nothing
	bad := []*adminv1.ClientCompatRule{{App: "a", Core: "xray", MinChrome: "latest"}}
	wantCode(t, update(ownerCtx(c.ctx), bad), connect.CodeInvalidArgument)
	if raw2, _ := c.st.Setting(c.ctx, store.SettingClientCompat); raw2 != raw {
		t.Errorf("a refused table changed the stored value: %q", raw2)
	}

	// reset: the table of the release again, and the stored value is gone
	if err := update(ownerCtx(c.ctx), got.Defaults); err != nil {
		t.Fatal(err)
	}
	if _, err := c.st.Setting(c.ctx, store.SettingClientCompat); err == nil {
		t.Error("the stored changes survived a reset")
	}
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{}); r.Total != 0 {
		t.Errorf("after the reset: %v", namesOf(r))
	}
}

// Unreadable stored changes never break the list: the built-in table stays in force.
func TestClientCompatUnreadableChangesFallBackToTheBuiltInTable(t *testing.T) {
	c := newCompatEnv(t)
	reality := c.realityProfile("reality", "")
	c.inbound(reality, "nod_de1")
	c.person("alice", c.group("g", reality))
	c.fetchWith("alice", "aaaaaaaaaa01", "Android", "Happ/2.7.1")
	if err := c.st.SetSettings(c.ctx, map[string]string{store.SettingClientCompat: `[{"app":"happ","colour":1}`}); err != nil {
		t.Fatal(err)
	}
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{}); r.Total != 1 || r.Devices[0].State != adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM {
		t.Fatalf("list = %v", namesOf(r))
	}
	if got := must(c.s.GetClientCompat(c.ctx, req(&adminv1.GetClientCompatRequest{}))).Msg; len(got.Rules) != len(clientDefaults) {
		t.Errorf("rules = %d", len(got.Rules))
	}
}
