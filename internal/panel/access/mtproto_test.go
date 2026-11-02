package access

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// tgFix: two nodes with an MTProto inbound each (de1, nl1) and de1 with a Hysteria2 inbound, one group holding both profiles.
type tgFix struct {
	*env
	group  string
	tg     *adminv1.ProfileSummary
	hy     *adminv1.ProfileSummary
	tokens map[string]string
}

func newTGFix(t *testing.T) *tgFix {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	tg := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg 443", SettingsJson: `{"port":443}`}))).Msg.Profile
	hy := e.profile("hy2 443", "")
	e.inbound(tg.Id, "nod_de1")
	e.inbound(tg.Id, "nod_nl1")
	e.inbound(hy.Id, "nod_de1")
	return &tgFix{env: e, group: e.group("tg", tg.Id, hy.Id), tg: tg, hy: hy, tokens: map[string]string{}}
}

func (f *tgFix) person(name string, telegram bool, mut ...func(*adminv1.CreateUserRequest)) *adminv1.User {
	r := f.userBare(name, f.group, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: telegram}
		for _, fn := range mut {
			fn(m)
		}
	})
	f.tokens[name] = r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	return r.User
}

func (f *tgFix) apps(u *adminv1.User, happ, amnezia, telegram bool) error {
	_, err := f.s.UpdateUser(f.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, Apps: &adminv1.AppToggles{Happ: happ, Amnezia: amnezia, Telegram: telegram}}))
	return err
}

func (f *tgFix) links(u *adminv1.User) *adminv1.GetTelegramLinksResponse {
	f.t.Helper()
	return must(f.s.GetTelegramLinks(f.ctx, req(&adminv1.GetTelegramLinksRequest{UserId: u.Id}))).Msg
}

func (f *tgFix) user(u *adminv1.User) *adminv1.User {
	f.t.Helper()
	return must(f.s.GetUser(f.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.User
}

// keyOf is the key part of the secret in a tg:// link.
func keyOf(t *testing.T, link string) string {
	key, _ := secretOf(t, link)
	return key
}

// secretOf splits the secret of a tg:// link into the key and the domain of the Fake TLS handshake.
func secretOf(t *testing.T, link string) (key, domain string) {
	t.Helper()
	uri, err := url.Parse(link)
	if err != nil || uri.Scheme != "tg" || uri.Host != "proxy" {
		t.Fatalf("link = %q, %v", link, err)
	}
	secret := uri.Query().Get("secret")
	if !strings.HasPrefix(secret, "ee") || len(secret) <= 34 {
		t.Fatalf("secret = %q", secret)
	}
	name, err := hex.DecodeString(secret[34:])
	if err != nil {
		t.Fatalf("secret = %q: %v", secret, err)
	}
	return secret[2:34], string(name)
}

// mtprotoKeys are the keys a node is told to accept, by user: one per credential of its MTProto inbounds.
func (f *tgFix) mtprotoKeys(node string) map[string]string {
	f.t.Helper()
	out := map[string]string{}
	for _, in := range must(f.s.Desired(f.ctx, node)) {
		if in.Spec.Protocol != "mtproto" {
			continue
		}
		for _, c := range in.Creds {
			var d struct{ Key string }
			if err := json.Unmarshal(c.Data, &d); err != nil || d.Key == "" {
				f.t.Fatalf("credential data %s: %v", c.Data, err)
			}
			out[c.UserID] = d.Key
		}
	}
	return out
}

func (f *tgFix) count(q string, args ...any) (n int) {
	f.t.Helper()
	if err := f.st.R.QueryRowContext(f.ctx, q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestTelegramKeyIsTheOnlyCredentialAndMatchesTheLinks(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	bob := f.person("bob", false)
	if !alice.TelegramIssued || bob.TelegramIssued {
		t.Fatalf("telegram_key: alice %v, bob %v", alice.TelegramIssued, bob.TelegramIssued)
	}
	if alice.DevicesUsed != 0 {
		t.Errorf("the key fills no place of the device limit: devices_used = %d", alice.DevicesUsed)
	}
	if n := f.count(`SELECT count(*) FROM device WHERE user_id = ? AND kind = 'key' AND revoked_at IS NULL`, alice.Id); n != 1 {
		t.Errorf("%d key devices, want the one that is listed", n)
	}

	r := f.links(alice)
	if !r.HasKey || r.KeyDeviceId == "" || r.KeyIssuedUnix != f.clock.Unix() || len(r.Links) != 2 {
		t.Fatalf("links = %+v", r)
	}
	got := map[string]bool{}
	for _, l := range r.Links {
		key := keyOf(t, l.Url)
		got[l.NodeName] = true
		if want := f.mtprotoKeys(l.NodeId)[alice.Id]; want != key {
			t.Errorf("%s: the key in the link is %q, the node is told %q", l.NodeName, key, want)
		}
	}
	if !got["de1"] || !got["nl1"] {
		t.Errorf("links of %v", got)
	}
	if _, ok := f.mtprotoKeys("nod_de1")[bob.Id]; ok {
		t.Error("a person without Telegram is on the node")
	}
	if r := f.links(bob); r.HasKey || len(r.Links) != 0 {
		t.Errorf("bob: %+v", r)
	}

	// An app that fetches the subscription gets a device of its own, and no MTProto credential on it.
	tok := f.tokens["alice"]
	for _, hint := range []DeviceHint{phone(), tablet(), {}} {
		view := must(f.s.SubscriptionWith(f.ctx, tok, SubOptions{Device: hint}))
		for _, l := range view.Lines {
			if strings.Contains(l, "tg://") {
				t.Fatalf("a Telegram line in the subscription: %s", l)
			}
		}
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE protocol = 'mtproto' AND revoked_at IS NULL AND user_id = ?`, alice.Id); n != 1 {
		t.Errorf("%d live MTProto credentials, want the key alone", n)
	}
	if n := f.count(`SELECT count(*) FROM device_credential c JOIN device d ON d.id = c.device_id WHERE c.protocol = 'mtproto' AND d.kind = 'link'`); n != 0 {
		t.Errorf("%d MTProto credentials on link devices", n)
	}
	if got := f.mtprotoKeys("nod_de1"); len(got) != 1 {
		t.Errorf("de1 is told %v", got)
	}
}

func TestGetTelegramLinksIsARead(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	f.person("bob", false)
	snapshot := func() string {
		rows, err := f.st.R.QueryContext(f.ctx, `SELECT id, user_id, kind, last_seen_at, first_seen_at, revoked_at FROM device ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var id, user, kind string
			var seen, first int64
			var revoked sql.NullInt64
			if err := rows.Scan(&id, &user, &kind, &seen, &first, &revoked); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, id, user, kind, seen, first, revoked)
		}
		return b.String()
	}
	before := snapshot()
	f.clock = f.clock.Add(48 * time.Hour) // a read at another time must not touch a device either
	for range 3 {
		f.links(alice)
	}
	if after := snapshot(); after != before {
		t.Errorf("devices changed by a read:\nbefore\n%safter\n%s", before, after)
	}
	// A person with Telegram on and no key (the key was revoked) gets no device from a read either.
	must(f.s.RevokeTelegramKey(f.ctx, req(&adminv1.RevokeTelegramKeyRequest{UserId: alice.Id})))
	before = snapshot()
	if r := f.links(alice); r.HasKey || len(r.Links) != 0 {
		t.Errorf("links without a key: %+v", r)
	}
	if after := snapshot(); after != before {
		t.Errorf("a read made a device:\n%s", after)
	}
}

func TestTelegramOffRevokesTheKey(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	first := keyOf(t, f.links(alice).Links[0].Url)
	if f.mtprotoKeys("nod_de1")[alice.Id] != first {
		t.Fatal("the key is not on the node")
	}
	f.resetNotify()
	if err := f.apps(alice, true, true, false); err != nil {
		t.Fatal(err)
	}
	if f.notify.n.Load() == 0 {
		t.Error("the nodes were not told")
	}
	if len(f.mtprotoKeys("nod_de1")) != 0 || len(f.mtprotoKeys("nod_nl1")) != 0 {
		t.Error("the credential is still in the desired state of a node")
	}
	if r := f.links(alice); r.HasKey || len(r.Links) != 0 {
		t.Errorf("links after Telegram off: %+v", r)
	}
	if n := f.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, alice.Id); n != 0 {
		t.Errorf("%d live devices after Telegram off", n)
	}
	if u := f.user(alice); u.TelegramIssued || u.DevicesUsed != 0 {
		t.Errorf("user: key %v, devices %d", u.TelegramIssued, u.DevicesUsed)
	}

	// On again: a new key, not the old one.
	if err := f.apps(alice, true, true, true); err != nil {
		t.Fatal(err)
	}
	second := keyOf(t, f.links(alice).Links[0].Url)
	if second == first || f.mtprotoKeys("nod_de1")[alice.Id] != second {
		t.Errorf("the old key came back or the node does not have the new one: %q %q", first, second)
	}
}

func TestTelegramKeyIssueRotateRevoke(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	key := func() string { return keyOf(t, f.links(alice).Links[0].Url) }
	k0 := key()

	// issue again: the same key
	r := must(f.s.IssueTelegramKey(f.ctx, req(&adminv1.IssueTelegramKeyRequest{UserId: alice.Id}))).Msg.Proxy
	if !r.HasKey || keyOf(t, r.Links[0].Url) != k0 {
		t.Errorf("issue changed the key: %+v", r)
	}
	if n := f.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, alice.Id); n != 1 {
		t.Errorf("%d devices after a second issue", n)
	}

	r = must(f.s.RotateTelegramKey(f.ctx, req(&adminv1.RotateTelegramKeyRequest{UserId: alice.Id}))).Msg.Proxy
	k1 := keyOf(t, r.Links[0].Url)
	if k1 == k0 || f.mtprotoKeys("nod_nl1")[alice.Id] != k1 || r.KeyDeviceId != f.links(alice).KeyDeviceId {
		t.Errorf("rotate: old %q new %q node %q", k0, k1, f.mtprotoKeys("nod_nl1")[alice.Id])
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'mtproto'`, alice.Id); n != 2 {
		t.Errorf("%d credential rows after a rotation, want the revoked one and the new one", n)
	}

	// removing the key device (the device list of the user card) revokes the key, as RevokeDevice does for any device
	must(f.s.RevokeDevice(f.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: r.KeyDeviceId})))
	if got := f.links(alice); got.HasKey || len(f.mtprotoKeys("nod_de1")) != 0 {
		t.Errorf("after RevokeDevice: %+v", got)
	}
	_, err := f.s.RotateTelegramKey(f.ctx, req(&adminv1.RotateTelegramKeyRequest{UserId: alice.Id}))
	wantCode(t, err, connect.CodeNotFound)

	must(f.s.IssueTelegramKey(f.ctx, req(&adminv1.IssueTelegramKeyRequest{UserId: alice.Id})))
	if key() == k1 {
		t.Error("a revoked key came back")
	}
	gone := must(f.s.RevokeTelegramKey(f.ctx, req(&adminv1.RevokeTelegramKeyRequest{UserId: alice.Id}))).Msg.Proxy
	if gone.HasKey || len(gone.Links) != 0 || len(f.mtprotoKeys("nod_de1")) != 0 {
		t.Errorf("revoke: %+v", gone)
	}
	if got := f.user(alice); !got.Apps.Telegram {
		t.Error("revoking the key turned Telegram off")
	}
	// revoking again is a no-op; issuing needs the toggle
	must(f.s.RevokeTelegramKey(f.ctx, req(&adminv1.RevokeTelegramKeyRequest{UserId: alice.Id})))
	bob := f.person("bob", false)
	_, err = f.s.IssueTelegramKey(f.ctx, req(&adminv1.IssueTelegramKeyRequest{UserId: bob.Id}))
	wantCode(t, err, connect.CodeFailedPrecondition)
}

// The Telegram key is a device of the person and is listed with the others, but it fills no place of the device limit: not
// when it is made, not when an app registers, and in no count that is shown.
func TestTelegramKeyIsOutsideTheDeviceLimit(t *testing.T) {
	f := newTGFix(t)
	f.syncAfterResponse()
	f.setLinkMode(store.LinkModeEnforce)
	alice := f.person("alice", false, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	for i := range 2 {
		if view := must(f.s.SubscriptionWith(f.ctx, f.tokens["alice"], SubOptions{Device: hintN(i)})); view.Refusal != "" {
			t.Fatalf("device %d: %q", i, view.Refusal)
		}
	}
	if u := f.user(alice); u.DevicesUsed != 2 {
		t.Fatalf("devices_used = %d before Telegram", u.DevicesUsed)
	}

	// at 2/2 the person can have Telegram: the toggle and the issue call both work, and nothing changes in the count
	if err := f.apps(alice, true, true, true); err != nil {
		t.Fatalf("Telegram on at the limit: %v", err)
	}
	r := must(f.s.IssueTelegramKey(f.ctx, req(&adminv1.IssueTelegramKeyRequest{UserId: alice.Id}))).Msg.Proxy
	if !r.HasKey || len(r.Links) == 0 {
		t.Fatalf("issue: %+v", r)
	}
	if f.events("device_limit_reached") != 0 {
		t.Error("making the key reported the limit")
	}
	got := must(f.s.GetUser(f.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg
	var keys, others int
	for _, d := range got.Devices {
		if d.Kind == adminv1.DeviceKind_DEVICE_KIND_TELEGRAM_KEY {
			keys++
			if d.Id != r.KeyDeviceId {
				t.Errorf("the key device is %s, not %s", d.Id, r.KeyDeviceId)
			}
		} else {
			others++
		}
	}
	if keys != 1 || others != 2 || got.User.DevicesUsed != 2 || !got.User.TelegramIssued {
		t.Errorf("devices: %d keys, %d others, devices_used %d, issued %v", keys, others, got.User.DevicesUsed, got.User.TelegramIssued)
	}
	list := must(f.s.ListUsers(f.ctx, req(&adminv1.ListUsersRequest{}))).Msg
	if list.Counts.DevicesUsed != 2 || list.Users[0].DevicesUsed != 2 {
		t.Errorf("list counts: %+v, user %d", list.Counts, list.Users[0].DevicesUsed)
	}
	if over := must(f.s.ListUsers(f.ctx, req(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_OVER_DEVICE_LIMIT}))).Msg; len(over.Users) != 0 || over.Counts.OverDeviceLimit != 0 {
		t.Errorf("over the limit with the key: %d users, %+v", len(over.Users), over.Counts)
	}

	// the limit is still the limit for apps: a third app is refused, and the key does not make it any sooner
	if view := must(f.s.SubscriptionWith(f.ctx, f.tokens["alice"], SubOptions{Device: hintN(2)})); view.Refusal != RefusalDeviceLimit {
		t.Errorf("third device: refusal %q", view.Refusal)
	}
	if view := must(f.s.SubscriptionWith(f.ctx, f.tokens["alice"], SubOptions{Device: hintN(0)})); view.Refusal != "" || view.DevicesUsed != 2 || len(view.Devices) != 3 {
		t.Errorf("a known device: refusal %q, used %d, listed %d", view.Refusal, view.DevicesUsed, len(view.Devices))
	}
	// removing the key frees nothing: it never took a place
	must(f.s.RevokeTelegramKey(f.ctx, req(&adminv1.RevokeTelegramKeyRequest{UserId: alice.Id})))
	if view := must(f.s.SubscriptionWith(f.ctx, f.tokens["alice"], SubOptions{Device: hintN(2)})); view.Refusal != RefusalDeviceLimit {
		t.Errorf("third device after the key is gone: refusal %q", view.Refusal)
	}
}

// With the key already there, a limit of 2 still allows 2 devices of the person's own.
func TestDeviceLimitStillAllowsItsDevicesWithTheKey(t *testing.T) {
	f := newTGFix(t)
	f.setLinkMode(store.LinkModeEnforce)
	bob := f.person("bob", true, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	if !bob.TelegramIssued {
		t.Fatal("no key")
	}
	for i := range 2 {
		if view := must(f.s.SubscriptionWith(f.ctx, f.tokens["bob"], SubOptions{Device: hintN(i)})); view.Refusal != "" {
			t.Fatalf("device %d refused with the key present: %q", i, view.Refusal)
		}
	}
	if view := must(f.s.SubscriptionWith(f.ctx, f.tokens["bob"], SubOptions{Device: hintN(2)})); view.Refusal != RefusalDeviceLimit {
		t.Errorf("third device: %q", view.Refusal)
	}
	if u := f.user(bob); u.DevicesUsed != 2 {
		t.Errorf("devices_used = %d", u.DevicesUsed)
	}
	// the message of a refusal names the devices that fill the limit, not the key
	if names := f.s.limitDevices(f.ctx, bob.Id); strings.Contains(names, "Telegram") || strings.Count(names, ",") != 1 {
		t.Errorf("limit devices %q", names)
	}
}

func TestTelegramKeyFollowsGroupAndNodeSelection(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	bob := f.person("bob", true, func(m *adminv1.CreateUserRequest) { m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_de1"}} })
	nodesOf := func(u *adminv1.User) []string {
		var out []string
		for _, l := range f.links(u).Links {
			out = append(out, l.NodeName)
		}
		slices.Sort(out)
		return out
	}
	if got := nodesOf(alice); !slices.Equal(got, []string{"de1", "nl1"}) {
		t.Errorf("alice: %v", got)
	}
	if got := nodesOf(bob); !slices.Equal(got, []string{"de1"}) {
		t.Errorf("bob (de1 only): %v", got)
	}
	if _, ok := f.mtprotoKeys("nod_nl1")[bob.Id]; ok {
		t.Error("bob is on nl1")
	}
	if _, ok := f.mtprotoKeys("nod_nl1")[alice.Id]; !ok {
		t.Error("alice is not on nl1")
	}

	must(f.s.UpdateGroup(f.ctx, req(&adminv1.UpdateGroupRequest{GroupId: f.group, DisabledNodeIds: &adminv1.NodeIds{Values: []string{"nod_nl1"}}})))
	if got := nodesOf(alice); !slices.Equal(got, []string{"de1"}) {
		t.Errorf("alice with nl1 disabled for the group: %v", got)
	}
	if len(f.mtprotoKeys("nod_nl1")) != 0 {
		t.Error("a node the group does not use still has the key")
	}
	if len(f.mtprotoKeys("nod_de1")) != 2 {
		t.Errorf("de1: %v", f.mtprotoKeys("nod_de1"))
	}
}

// The profile editor previews the Telegram link, and says that no subscription format carries it.
func TestPreviewMTProtoProfile(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	r := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "mtproto", SettingsJson: `{"port":443}`, NodeId: "nod_de1"}))).Msg
	if len(r.Errors) != 0 || r.ClientLabel != "Telegram · tg://proxy link" || !strings.HasPrefix(r.ClientPreview, "tg://proxy?port=443&secret=") {
		t.Fatalf("preview = %+v", r)
	}
	for _, f := range r.Formats {
		if f.Ok {
			t.Errorf("subscription format %v carries the Telegram link", f.Format)
		}
	}
}

// The owner's list tells the node's own link from its IPv6 twin.
func TestTelegramLinksMarkTheIPv6Twin(t *testing.T) {
	f := newTGFix(t)
	f.sql(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_de1'`)
	alice := f.person("alice", true)
	var plain, v6 int
	for _, l := range f.links(alice).Links {
		switch {
		case l.Ipv6 && l.NodeName == "de1" && strings.Contains(l.Url, "server=2001"):
			v6++
		case !l.Ipv6 && !l.Relay && l.Via == "":
			plain++
		default:
			t.Errorf("unexpected link %+v", l)
		}
	}
	if plain != 2 || v6 != 1 {
		t.Errorf("%d plain links and %d IPv6 twins, want 2 and 1", plain, v6)
	}
}

// The people of the Telegram proxy are listed by the server: a filter and its count, not the first page filtered by the client.
func TestListUsersTelegramFilter(t *testing.T) {
	f := newTGFix(t)
	f.person("alice", true)
	f.person("bob", false)
	f.person("carol", true)
	r := must(f.s.ListUsers(f.ctx, req(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_TELEGRAM}))).Msg
	var names []string
	for _, u := range r.Users {
		names = append(names, u.Name)
		if !u.Apps.Telegram {
			t.Errorf("%s has Telegram off", u.Name)
		}
	}
	if !slices.Equal(names, []string{"alice", "carol"}) || r.Counts.Telegram != 2 || r.Counts.All != 3 {
		t.Errorf("users %v, counts %+v", names, r.Counts)
	}
	// the count follows the query, not the filter
	r = must(f.s.ListUsers(f.ctx, req(&adminv1.ListUsersRequest{Query: "bob"}))).Msg
	if r.Counts.Telegram != 0 || r.Counts.All != 1 || len(r.Users) != 1 {
		t.Errorf("query bob: %+v", r.Counts)
	}
	// the filter pages like any other
	for i := range 3 {
		f.person(fmt.Sprintf("plain-%d", i), false)
	}
	r = must(f.s.ListUsers(f.ctx, req(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_TELEGRAM, PageSize: 1}))).Msg
	if len(r.Users) != 1 || r.NextPageToken == "" || r.Counts.Telegram != 2 {
		t.Errorf("first page of the filter: %d users, token %q, %+v", len(r.Users), r.NextPageToken, r.Counts)
	}
}

// A group says how many nodes give its people a Telegram proxy, as it says it for the subscription and the keys.
func TestGroupReachCountsTelegram(t *testing.T) {
	f := newTGFix(t)
	groups := func() *adminv1.Group {
		for _, g := range must(f.s.ListGroups(f.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups {
			if g.Id == f.group {
				return g
			}
		}
		t.Fatal("no group")
		return nil
	}
	if g := groups(); g.TelegramNodes != 2 || g.HappNodes != 1 || g.AmneziaNodes != 0 {
		t.Errorf("reach: telegram %d, happ %d, amnezia %d", g.TelegramNodes, g.HappNodes, g.AmneziaNodes)
	}
	must(f.s.UpdateGroup(f.ctx, req(&adminv1.UpdateGroupRequest{GroupId: f.group, DisabledNodeIds: &adminv1.NodeIds{Values: []string{"nod_nl1"}}})))
	if g := groups(); g.TelegramNodes != 1 {
		t.Errorf("with nl1 disabled: %d", g.TelegramNodes)
	}
}

// The domain in the handshake is the node's own host name, set on the node's inbound for a Telegram client to put in the
// handshake; the node is told the same name and what to do with a stranger.
func TestTelegramLinkCarriesTheNodesOwnDomain(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	for _, l := range f.links(alice).Links {
		_, domain := secretOf(t, l.Url)
		want := map[string]string{"de1": "de1.example.com", "nl1": "nl1.example.com"}[l.NodeName]
		if domain != want {
			t.Errorf("%s: the handshake name is %q, want %q", l.NodeName, domain, want)
		}
	}
	for _, node := range []string{"nod_de1", "nod_nl1"} {
		for _, in := range must(f.s.Desired(f.ctx, node)) {
			if in.Spec.Protocol != "mtproto" {
				continue
			}
			var ns struct {
				Domains  []string `json:"domains"`
				Fallback string   `json:"fallback"`
			}
			if err := json.Unmarshal(in.Spec.Settings, &ns); err != nil || len(ns.Domains) != 1 || ns.Fallback != "decoy" ||
				!strings.HasSuffix(ns.Domains[0], ".example.com") || !strings.HasPrefix(ns.Domains[0], strings.TrimPrefix(node, "nod_")) {
				t.Errorf("%s is told %s (%v)", node, in.Spec.Settings, err)
			}
		}
	}
}

// A node with only an IP address cannot serve the node's own name: the profile is refused on it, with a reason the UI words;
// another site makes it usable; an existing inbound on a node that lost its name builds no more and its links are gone.
func TestMTProtoOnANodeWithoutADomain(t *testing.T) {
	e := newEnv(t)
	e.node("nod_ip1", "ip1", "203.0.113.9", "active")
	e.node("nod_de1", "de1", "de1.example.com", "active")
	own := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg own", SettingsJson: `{"port":443}`}))).Msg.Profile
	site := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg site", SettingsJson: `{"port":8443,"fake_tls":{"kind":"site","domain":"www.google.com"}}`}))).Msg.Profile

	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: own.Id, NodeId: "nod_ip1"}))
	wantCoded(t, err, connect.CodeInvalidArgument, "mtproto_needs_domain", map[string]string{"address": "203.0.113.9", "node": "ip1"})
	// a name set on the inbound is enough
	must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: own.Id, NodeId: "nod_ip1", TlsServerNameOverride: "tg.example.com"})))
	// and so is another site, without any name
	must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_ip1"})))
	deIn := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: own.Id, NodeId: "nod_de1"}))).Msg.Inbound
	g := e.group("tg", own.Id, site.Id)
	u := must(e.s.CreateUser(e.ctx, req(&adminv1.CreateUserRequest{Name: "alice", GroupId: g, Apps: &adminv1.AppToggles{Happ: true, Telegram: true}}))).Msg.User
	domains := map[string]string{} // by "node:port"
	links := must(e.s.GetTelegramLinks(e.ctx, req(&adminv1.GetTelegramLinksRequest{UserId: u.Id}))).Msg.Links
	for _, l := range links {
		_, domain := secretOf(t, l.Url)
		uri, _ := url.Parse(l.Url)
		domains[l.NodeName+":"+uri.Query().Get("port")] = domain
	}
	want := map[string]string{"ip1:443": "tg.example.com", "ip1:8443": "www.google.com", "de1:443": "de1.example.com"}
	if len(domains) != 3 || domains["ip1:443"] != want["ip1:443"] || domains["ip1:8443"] != want["ip1:8443"] || domains["de1:443"] != want["de1:443"] {
		t.Errorf("handshake names %v, want %v", domains, want)
	}
	for _, in := range must(e.s.Desired(e.ctx, "nod_ip1")) {
		var ns struct{ Fallback string }
		_ = json.Unmarshal(in.Spec.Settings, &ns)
		if in.Spec.Listen.Port == 8443 && ns.Fallback != "site" {
			t.Errorf("another site on ip1 is told %s", in.Spec.Settings)
		}
	}

	// de1 loses its name: its inbound cannot be built and its link is not rendered
	e.sql(`UPDATE node SET address = '198.51.100.7' WHERE id = 'nod_de1'`)
	for _, l := range must(e.s.GetTelegramLinks(e.ctx, req(&adminv1.GetTelegramLinksRequest{UserId: u.Id}))).Msg.Links {
		if l.NodeName == "de1" {
			t.Errorf("a link for a node without a name: %s", l.Url)
		}
	}
	for _, in := range must(e.s.Desired(e.ctx, "nod_de1")) {
		if in.Spec.ID == deIn.Id {
			t.Error("an inbound that cannot be built is in the desired state")
		}
	}
}

// The protocol list marks MTProto as experimental and nothing else.
func TestProtocolListMarksMTProtoExperimental(t *testing.T) {
	e := newEnv(t)
	got := map[string]bool{}
	for _, p := range must(e.s.ListProtocols(e.ctx, req(&adminv1.ListProtocolsRequest{}))).Msg.Protocols {
		got[p.Id] = p.Experimental
	}
	if len(got) != 4 || !got["mtproto"] || got["hysteria2"] || got["vless"] || got["awg"] {
		t.Errorf("experimental by protocol: %v", got)
	}
}
