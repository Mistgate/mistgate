package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
)

type fakeNotify struct{ n atomic.Int32 }

func (f *fakeNotify) StateChanged() { f.n.Add(1) }

type fakeOnline map[string]string

func (f fakeOnline) OnlineUsers() map[string]string { return f }

type env struct {
	t      *testing.T
	s      *Service
	st     *store.Store
	notify *fakeNotify
	online fakeOnline
	clock  time.Time
	ctx    context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, notify: &fakeNotify{}, online: fakeOnline{}, clock: ts("2026-09-30T12:00:00Z"), ctx: ctx}
	s, err := New(st, v, builtin.Registry(), e.notify, e.online, Config{SubscriptionBaseURL: "https://sub.example.com/k3xq/"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return e.clock }
	e.s = s
	return e
}

func (e *env) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) node(id, name, address, state string) {
	e.sql(`INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, ?, 1)`, id, name, address, state)
}

func (e *env) resetNotify() { e.notify.n.Store(0) }

// must unwraps a result; an unexpected error panics, which Go's test runner reports as a failure with the
// stack of the offending line.
func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("unexpected error: %v", err))
	}
	return v
}

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v, got success", code)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("want %v, got %v (%v)", code, got, err)
	}
}

// wantMsg checks a refusal the admin UI words by its code (web/src/lib/errors.ts): code and message as Connect prints them.
func wantMsg(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || err.Error() != msg {
		t.Fatalf("want %q, got %v", msg, err)
	}
}

func req[T any](m *T) *connect.Request[T] { return connect.NewRequest(m) }

func (e *env) profile(name, settings string) *adminv1.ProfileSummary {
	e.t.Helper()
	r := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: name, SettingsJson: settings})))
	return r.Msg.Profile
}

func (e *env) group(name string, profileIDs ...string) string {
	e.t.Helper()
	return must(e.s.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: name, ProfileIds: profileIDs}))).Msg.Group.Id
}

func (e *env) inbound(profileID, nodeID string) *adminv1.Inbound {
	e.t.Helper()
	return must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: profileID, NodeId: nodeID}))).Msg.Inbound
}

func (e *env) user(name, groupID string, mut func(*adminv1.CreateUserRequest)) *adminv1.CreateUserResponse {
	e.t.Helper()
	m := &adminv1.CreateUserRequest{Name: name, GroupId: groupID}
	if mut != nil {
		mut(m)
	}
	return must(e.s.CreateUser(e.ctx, req(m))).Msg
}

// fixture: one active node, one profile deployed on it, one group holding the profile.
type fixture struct {
	e       *env
	nodeID  string
	profile string
	inbound string
	group   string
}

func newFixture(t *testing.T) *fixture {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("hy2 443", "")
	in := e.inbound(p.Id, "nod_de1")
	return &fixture{e: e, nodeID: "nod_de1", profile: p.Id, inbound: in.Id, group: e.group("default", p.Id)}
}

func (f *fixture) desiredCreds() map[string]bool {
	f.e.t.Helper()
	ins := must(f.e.s.Desired(f.e.ctx, f.nodeID))
	out := map[string]bool{}
	for _, in := range ins {
		for _, c := range in.Creds {
			out[c.UserID] = true
		}
	}
	return out
}

func TestEffectiveAccess(t *testing.T) {
	f := newFixture(t)
	e := f.e
	g2 := e.group("no-profiles")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")

	a := e.user("alice", f.group, nil).User
	b := e.user("bob", f.group, nil).User
	c := e.user("carol", f.group, nil).User
	d := e.user("dave", f.group, func(m *adminv1.CreateUserRequest) { m.Apps = &adminv1.AppToggles{Amnezia: true} }).User
	eve := e.user("eve", f.group, func(m *adminv1.CreateUserRequest) {
		m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_nl1"}}
	}).User
	frank := e.user("frank", g2, nil).User
	gina := e.user("gina", f.group, func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 100; m.SpeedLimitBps = 5000; m.TermDays = 30 }).User

	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{b.Id}, Enabled: false})))
	e.sql(`UPDATE user SET expires_at = ? WHERE id = ?`, e.clock.Add(-time.Hour).Unix(), c.Id)
	if err := e.s.Recompute(e.ctx, []string{c.Id}); err != nil {
		t.Fatal(err)
	}

	got := f.desiredCreds()
	for _, want := range []string{a.Id, gina.Id} {
		if !got[want] {
			t.Errorf("user %s must get access", want)
		}
	}
	for name, id := range map[string]string{"bob disabled": b.Id, "carol expired": c.Id, "dave amnezia only": d.Id, "eve other node": eve.Id, "frank no profile": frank.Id} {
		if got[id] {
			t.Errorf("%s must not get access", name)
		}
	}
	if len(got) != 2 {
		t.Errorf("desired users = %v", got)
	}

	// Spec and credential details.
	ins := must(e.s.Desired(e.ctx, f.nodeID))
	if len(ins) != 1 || ins[0].Spec.ID != f.inbound || ins[0].Spec.Protocol != "hysteria2" || !ins[0].Spec.Enabled || ins[0].Spec.Version != 1 {
		t.Fatalf("desired inbounds = %+v", ins)
	}
	for _, cr := range ins[0].Creds {
		if !strings.HasPrefix(cr.CredID, "crd_") || cr.DeviceID == "" {
			t.Errorf("cred = %+v", cr)
		}
		if cr.UserID == gina.Id && (cr.RateLimitBps != 5000 || cr.ValidUntil.IsZero()) {
			t.Errorf("gina's limits were not carried: %+v", cr)
		}
		var data map[string]string
		if json.Unmarshal(cr.Data, &data) != nil || len(data["auth_sha256"]) != 64 {
			t.Errorf("verifier = %s", cr.Data)
		}
	}
	// The other node has no inbound: empty but valid.
	if ins := must(e.s.Desired(e.ctx, "nod_nl1")); len(ins) != 0 {
		t.Errorf("nl1 desired = %+v", ins)
	}

	// Switching Amnezia-only dave to Happ gives him access (and credentials).
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: d.Id, Apps: &adminv1.AppToggles{Happ: true, Amnezia: true}})))
	if !f.desiredCreds()[d.Id] {
		t.Error("dave must get access after enabling Happ")
	}
	// Re-enabling bob restores him; a disabled inbound removes everyone.
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{b.Id}, Enabled: true})))
	if !f.desiredCreds()[b.Id] {
		t.Error("bob must be back")
	}
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.inbound, Enabled: new(false)})))
	if ins := must(e.s.Desired(e.ctx, f.nodeID)); len(ins) != 0 {
		t.Errorf("disabled inbound still desired: %+v", ins)
	}
}

func TestQuotaLimitedNotifies(t *testing.T) {
	f := newFixture(t)
	e := f.e
	u := e.user("quota", f.group, func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 1000 }).User
	if u.Status != adminv1.UserStatus_USER_STATUS_ACTIVE {
		t.Fatalf("new user status %v", u.Status)
	}
	a := e.st.Access()

	// Under quota: no status change, no notification.
	must(struct{}{}, a.AddUsage(e.ctx, u.Id, 999))
	e.resetNotify()
	must(struct{}{}, e.s.Recompute(e.ctx, []string{u.Id}))
	if e.notify.n.Load() != 0 {
		t.Error("notified without a status change")
	}

	// Crossing the quota: LIMITED, credentials leave the desired state, nodes are notified.
	must(struct{}{}, a.AddUsage(e.ctx, u.Id, 1))
	must(struct{}{}, e.s.Recompute(e.ctx, []string{u.Id}))
	if e.notify.n.Load() != 1 {
		t.Errorf("StateChanged called %d times, want 1", e.notify.n.Load())
	}
	got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.User
	if got.Status != adminv1.UserStatus_USER_STATUS_LIMITED {
		t.Errorf("status = %v", got.Status)
	}
	if f.desiredCreds()[u.Id] {
		t.Error("a limited user's credentials must leave the desired state")
	}

	// A second recompute changes nothing and stays quiet.
	must(struct{}{}, e.s.Recompute(e.ctx, []string{u.Id}))
	if e.notify.n.Load() != 1 {
		t.Error("notified again without a change")
	}

	// Raising the quota lifts LIMITED and notifies.
	e.resetNotify()
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, QuotaBytes: new(uint64(5000))})))
	if e.notify.n.Load() == 0 || !f.desiredCreds()[u.Id] {
		t.Error("raising the quota must restore access and notify")
	}

	// A monthly reset at the period boundary clears usage and LIMITED.
	must(struct{}{}, a.AddUsage(e.ctx, u.Id, 10000))
	must(struct{}{}, e.s.Recompute(e.ctx, []string{u.Id}))
	e.resetNotify()
	e.clock = ts("2026-10-01T00:00:01Z")
	n := must(e.s.Sweep(e.ctx))
	if n != 1 || e.notify.n.Load() != 1 {
		t.Errorf("sweep changed %d, notified %d", n, e.notify.n.Load())
	}
	got = must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.User
	if got.Status != adminv1.UserStatus_USER_STATUS_ACTIVE || got.UsedBytes != 0 {
		t.Errorf("after reset: status %v used %d", got.Status, got.UsedBytes)
	}
}

func TestResetUserTraffic(t *testing.T) {
	f := newFixture(t)
	e := f.e
	u := e.user("quota", f.group, func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 1000 }).User
	off := e.user("off", f.group, nil).User
	a := e.st.Access()
	must(struct{}{}, a.AddUsage(e.ctx, u.Id, 1500))
	must(struct{}{}, a.AddUsage(e.ctx, off.Id, 700))
	must(struct{}{}, e.s.Recompute(e.ctx, []string{u.Id}))
	if f.desiredCreds()[u.Id] {
		t.Fatal("setup: the user should be limited")
	}
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{off.Id}, Enabled: false})))
	e.resetNotify()

	r := must(e.s.ResetUserTraffic(e.ctx, req(&adminv1.ResetUserTrafficRequest{UserIds: []string{u.Id, off.Id}}))).Msg.Users
	if len(r) != 2 {
		t.Fatalf("users = %d", len(r))
	}
	for _, got := range r {
		if got.UsedBytes != 0 {
			t.Errorf("%s: used %d, want 0", got.Name, got.UsedBytes)
		}
		switch got.Id {
		case u.Id:
			if got.Status != adminv1.UserStatus_USER_STATUS_ACTIVE || got.NextResetUnix != u.NextResetUnix {
				t.Errorf("limited user: status %v, next reset %d (was %d)", got.Status, got.NextResetUnix, u.NextResetUnix)
			}
		case off.Id:
			if got.Status != adminv1.UserStatus_USER_STATUS_DISABLED { // the reset must not re-enable anyone
				t.Errorf("disabled user: status %v", got.Status)
			}
		}
	}
	if !f.desiredCreds()[u.Id] || e.notify.n.Load() != 1 {
		t.Errorf("a lifted limit must restore access and notify once (notified %d)", e.notify.n.Load())
	}
	rows := must(e.st.ListAudit(e.ctx, "", 0, 10))
	n := 0
	for _, row := range rows {
		if row.Action == "user_reset_traffic" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("audit rows = %d, want 2: %+v", n, rows)
	}

	// A reset made through a token is attributed to the token's channel, not to the panel.
	must(e.s.ResetUserTraffic(store.WithAuditSource(e.ctx, store.AuditMCP), req(&adminv1.ResetUserTrafficRequest{UserIds: []string{u.Id}})))
	if top := must(e.st.ListAudit(e.ctx, "", 0, 1)); len(top) != 1 || top[0].Action != "user_reset_traffic" || top[0].Source != store.AuditMCP {
		t.Errorf("audit source = %+v", top)
	}

	_, err := e.s.ResetUserTraffic(e.ctx, req(&adminv1.ResetUserTrafficRequest{UserIds: []string{"usr_missing"}}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.ResetUserTraffic(e.ctx, req(&adminv1.ResetUserTrafficRequest{}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestSweepExpiry(t *testing.T) {
	f := newFixture(t)
	e := f.e
	u := e.user("termed", f.group, func(m *adminv1.CreateUserRequest) { m.TermDays = 2 }).User
	e.user("forever", f.group, nil)
	if u.ExpiresUnix != e.clock.AddDate(0, 0, 2).Unix() {
		t.Errorf("expires %d", u.ExpiresUnix)
	}
	e.resetNotify()
	if n := must(e.s.Sweep(e.ctx)); n != 0 || e.notify.n.Load() != 0 {
		t.Errorf("early sweep changed %d", n)
	}
	e.clock = e.clock.AddDate(0, 0, 3)
	if n := must(e.s.Sweep(e.ctx)); n != 1 || e.notify.n.Load() != 1 {
		t.Errorf("sweep changed %d, notified %d", n, e.notify.n.Load())
	}
	got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.User
	if got.Status != adminv1.UserStatus_USER_STATUS_EXPIRED {
		t.Errorf("status = %v", got.Status)
	}

	// Extending lifts EXPIRED: max(now, expiry) + days. Users without a term end are skipped.
	ext := must(e.s.ExtendUsers(e.ctx, req(&adminv1.ExtendUsersRequest{UserIds: []string{u.Id}, Days: 10}))).Msg.Users[0]
	if ext.Status != adminv1.UserStatus_USER_STATUS_ACTIVE || ext.ExpiresUnix != e.clock.AddDate(0, 0, 10).Unix() {
		t.Errorf("extended: %v %d", ext.Status, ext.ExpiresUnix)
	}
	forever := must(e.s.ListUsers(e.ctx, req(&adminv1.ListUsersRequest{Query: "forever"}))).Msg.Users[0]
	ext2 := must(e.s.ExtendUsers(e.ctx, req(&adminv1.ExtendUsersRequest{UserIds: []string{forever.Id}, Days: 10}))).Msg.Users[0]
	if ext2.ExpiresUnix != 0 {
		t.Error("a user without a term end must stay without one")
	}
	// Setting a future date in UpdateUser lifts EXPIRED too.
	e.sql(`UPDATE user SET expires_at = ?, status = 'expired' WHERE id = ?`, e.clock.Add(-time.Hour).Unix(), u.Id)
	up := must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, ExpiresUnix: new(e.clock.Add(time.Hour).Unix())}))).Msg.User
	if up.Status != adminv1.UserStatus_USER_STATUS_ACTIVE {
		t.Errorf("status after new date = %v", up.Status)
	}
}

func TestUserRules(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx

	// Validation.
	_, err := e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "x", GroupId: f.group, Apps: &adminv1.AppToggles{}}))
	wantCode(t, err, connect.CodeInvalidArgument) // both apps off
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: " ", GroupId: f.group}))
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "x"}))
	wantCode(t, err, connect.CodeInvalidArgument) // no group
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "x", GroupId: "grp_missing"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "x", GroupId: f.group, Nodes: &adminv1.NodeSelection{}}))
	wantCode(t, err, connect.CodeInvalidArgument) // neither all nor a list
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "x", GroupId: f.group, Nodes: &adminv1.NodeSelection{NodeIds: []string{"nod_nope"}}}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "x", GroupId: f.group, DeviceLimit: 101}))
	wantCode(t, err, connect.CodeInvalidArgument)

	r := e.user("Alice", f.group, nil)
	_, err = e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "alice", GroupId: f.group}))
	wantMsg(t, err, "already_exists: name_taken") // names are unique case-insensitively

	// Defaults.
	u := r.User
	if !u.Apps.Happ || !u.Apps.Amnezia || !u.Nodes.All || u.DeviceLimit != 5 || u.QuotaReset != adminv1.QuotaReset_QUOTA_RESET_MONTH ||
		u.Status != adminv1.UserStatus_USER_STATUS_ACTIVE || u.DevicesUsed != 1 || u.NextResetUnix != ts("2026-10-01T00:00:00Z").Unix() {
		t.Errorf("defaults: %+v", u)
	}
	if !strings.HasPrefix(r.SubscriptionUrl, "https://sub.example.com/k3xq/") || len(r.SubscriptionUrl) != len("https://sub.example.com/k3xq/")+43 {
		t.Errorf("subscription url %q", r.SubscriptionUrl)
	}

	// The link can be fetched again; rotating kills the old token.
	link := must(e.s.GetSubscriptionLink(ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: u.Id}))).Msg.Url
	if link != r.SubscriptionUrl {
		t.Error("link changed without rotate")
	}
	oldTok := link[strings.LastIndex(link, "/")+1:]
	rot := must(e.s.GetSubscriptionLink(ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: u.Id, Rotate: true}))).Msg.Url
	if rot == link {
		t.Fatal("rotate returned the same link")
	}
	if _, err := e.s.Subscription(ctx, oldTok); err != ErrUnknownToken {
		t.Errorf("old token: %v", err)
	}
	if _, err := e.s.Subscription(ctx, rot[strings.LastIndex(rot, "/")+1:]); err != nil {
		t.Errorf("new token: %v", err)
	}
	// Tokens are stored hashed + encrypted, never in clear.
	var hash, enc []byte
	e.st.R.QueryRow(`SELECT sub_token_hash, sub_token_enc FROM user WHERE id = ?`, u.Id).Scan(&hash, &enc)
	tok := rot[strings.LastIndex(rot, "/")+1:]
	sum := sha256.Sum256([]byte(tok))
	if hex.EncodeToString(hash) != hex.EncodeToString(sum[:]) || strings.Contains(string(enc), tok) {
		t.Error("token storage is wrong")
	}

	// Update: turning both apps off is refused; a name clash is refused.
	_, err = e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, Apps: &adminv1.AppToggles{}}))
	wantCode(t, err, connect.CodeInvalidArgument)
	b := e.user("bob", f.group, nil).User
	_, err = e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: b.Id, Name: new("ALICE")}))
	wantCode(t, err, connect.CodeAlreadyExists)
	_, err = e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: "usr_nope", Name: new("z")}))
	wantCode(t, err, connect.CodeNotFound)

	// Revoking the device drops its credential from the nodes; the next fetch issues a fresh device.
	devs := must(e.s.GetUser(ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.Devices
	if len(devs) != 1 || len(devs[0].Protocols) != 1 || devs[0].Protocols[0] != "hysteria2" {
		t.Fatalf("devices = %+v", devs)
	}
	e.resetNotify()
	rv := must(e.s.RevokeDevice(ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: devs[0].Id}))).Msg.User
	if rv.DevicesUsed != 0 || e.notify.n.Load() != 1 || f.desiredCreds()[u.Id] {
		t.Errorf("revoke: devices %d notified %d desired %v", rv.DevicesUsed, e.notify.n.Load(), f.desiredCreds()[u.Id])
	}
	_, err = e.s.RevokeDevice(ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: devs[0].Id}))
	wantCode(t, err, connect.CodeNotFound)
	if _, err := e.s.Subscription(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if !f.desiredCreds()[u.Id] {
		t.Error("the next subscription fetch must issue a fresh credential")
	}

	// Delete: everything goes, the link dies, nodes are told.
	e.resetNotify()
	must(e.s.DeleteUsers(ctx, req(&adminv1.DeleteUsersRequest{UserIds: []string{u.Id, b.Id}})))
	if e.notify.n.Load() != 1 || len(f.desiredCreds()) != 0 {
		t.Error("delete must notify and clear credentials")
	}
	if _, err := e.s.Subscription(ctx, tok); err != ErrUnknownToken {
		t.Errorf("deleted user's token: %v", err)
	}
	var left int
	e.st.R.QueryRow(`SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential)`).Scan(&left)
	if left != 0 {
		t.Errorf("%d device rows left", left)
	}
	_, err = e.s.DeleteUsers(ctx, req(&adminv1.DeleteUsersRequest{}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestListUsers(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx
	g2 := e.group("vip")
	var ids []string
	for _, n := range []string{"amy", "bea", "cid", "dan", "eli"} {
		ids = append(ids, e.user(n, f.group, nil).User.Id)
	}
	vip := e.user("fay", g2, func(m *adminv1.CreateUserRequest) { m.TermDays = 3 }).User
	over := e.user("gus", f.group, func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 10 }).User
	must(struct{}{}, e.st.Access().AddUsage(ctx, over.Id, 10))
	must(struct{}{}, e.s.Recompute(ctx, []string{over.Id}))
	e.online[ids[0]] = "nod_de1"
	e.online[ids[1]] = "nod_de1"
	farExp := e.user("hal", f.group, func(m *adminv1.CreateUserRequest) { m.TermDays = 40 }).User
	_ = farExp

	list := func(m *adminv1.ListUsersRequest) *adminv1.ListUsersResponse {
		t.Helper()
		return must(e.s.ListUsers(ctx, req(m))).Msg
	}
	names := func(us []*adminv1.User) string {
		var n []string
		for _, u := range us {
			n = append(n, u.Name)
		}
		return strings.Join(n, ",")
	}

	all := list(&adminv1.ListUsersRequest{})
	if names(all.Users) != "amy,bea,cid,dan,eli,fay,gus,hal" {
		t.Errorf("all = %s", names(all.Users))
	}
	if c := all.Counts; c.All != 8 || c.Online != 2 || c.Expiring != 1 || c.OverQuota != 1 {
		t.Errorf("counts = %v", c)
	}
	if got := names(list(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_ONLINE}).Users); got != "amy,bea" {
		t.Errorf("online = %s", got)
	}
	if !list(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_ONLINE}).Users[0].Online {
		t.Error("online flag missing")
	}
	if got := names(list(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_EXPIRING}).Users); got != "fay" {
		t.Errorf("expiring = %s", got)
	}
	if got := names(list(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_OVER_QUOTA}).Users); got != "gus" {
		t.Errorf("over quota = %s", got)
	}
	if got := names(list(&adminv1.ListUsersRequest{GroupId: g2}).Users); got != "fay" {
		t.Errorf("group = %s", got)
	}
	// Query matches name or group name, case-insensitively, and LIKE wildcards are literal.
	if got := names(list(&adminv1.ListUsersRequest{Query: "VIP"}).Users); got != "fay" {
		t.Errorf("query by group = %s", got)
	}
	if got := names(list(&adminv1.ListUsersRequest{Query: "EL"}).Users); got != "eli" {
		t.Errorf("query by name = %s", got)
	}
	if got := list(&adminv1.ListUsersRequest{Query: "%"}).Users; len(got) != 0 {
		t.Errorf("wildcard matched %d users", len(got))
	}
	// Counts ignore the filter but follow the query/group.
	if c := list(&adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_ONLINE, GroupId: g2}).Counts; c.All != 1 || c.Online != 0 || c.Expiring != 1 {
		t.Errorf("group counts = %v", c)
	}

	// Keyset pagination is stable and complete.
	var seen []string
	tok := ""
	for range 10 {
		p := list(&adminv1.ListUsersRequest{PageSize: 3, PageToken: tok})
		seen = append(seen, names(p.Users))
		if tok = p.NextPageToken; tok == "" {
			break
		}
	}
	if strings.Join(seen, "|") != "amy,bea,cid|dan,eli,fay|gus,hal" {
		t.Errorf("pages = %v", seen)
	}
	_, err := e.s.ListUsers(ctx, req(&adminv1.ListUsersRequest{PageToken: "!!"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	_ = vip
}

func TestBulkEnableAndVia(t *testing.T) {
	f := newFixture(t)
	e := f.e
	a := e.user("ann", f.group, nil).User
	b := e.user("ben", f.group, nil).User
	e.resetNotify()
	off := must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{a.Id, b.Id}, Enabled: false}))).Msg.Users
	if len(off) != 2 || off[0].Status != adminv1.UserStatus_USER_STATUS_DISABLED || off[1].Status != adminv1.UserStatus_USER_STATUS_DISABLED || e.notify.n.Load() != 1 {
		t.Errorf("disable: %v notified %d", off, e.notify.n.Load())
	}
	on := must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{a.Id}, Enabled: true}))).Msg.Users
	if on[0].Status != adminv1.UserStatus_USER_STATUS_ACTIVE {
		t.Errorf("enable: %v", on[0].Status)
	}

	// "Via": apps actually used in the last 7 days, independent of the toggles.
	e.sql(`INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, 'nod_de1', 'hysteria2', ?, 40, 60)`,
		a.Id, e.clock.Add(-2*time.Hour).Unix()/3600*3600)
	e.sql(`INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, 'nod_de1', 'hysteria2', ?, 1, 1)`,
		b.Id, e.clock.AddDate(0, 0, -9).Unix()/3600*3600)
	got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: a.Id}))).Msg
	if len(got.User.Via) != 1 || got.User.Via[0] != adminv1.App_APP_HAPP {
		t.Errorf("via = %v", got.User.Via)
	}
	if bb := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: b.Id}))).Msg.User; len(bb.Via) != 0 {
		t.Errorf("old traffic counted as via: %v", bb.Via)
	}
	if len(got.DailyTraffic) != 14 || got.DailyTraffic[13].Bytes != 100 || got.DailyTraffic[13].DayUnix != ts("2026-09-30T00:00:00Z").Unix() {
		t.Errorf("daily = %v", got.DailyTraffic)
	}
	if len(got.NodeTraffic) != 1 || got.NodeTraffic[0].Bytes != 100 || got.NodeTraffic[0].NodeName != "de1" {
		t.Errorf("node traffic = %v", got.NodeTraffic)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].Id != f.profile {
		t.Errorf("profiles = %v", got.Profiles)
	}
	if len(got.NodeAccess) != 1 || !got.NodeAccess[0].Selected || len(got.NodeAccess[0].Protocols) != 1 {
		t.Errorf("node access = %v", got.NodeAccess)
	}
}

func TestGroups(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx
	_, err := e.s.CreateGroup(ctx, req(&adminv1.CreateGroupRequest{Name: "DEFAULT"}))
	wantCode(t, err, connect.CodeAlreadyExists)
	_, err = e.s.CreateGroup(ctx, req(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{"prf_nope"}}))
	wantCode(t, err, connect.CodeNotFound)
	u := e.user("u", f.group, nil).User

	// Changing the profile set of a group with users changes desired state.
	e.resetNotify()
	g := must(e.s.UpdateGroup(ctx, req(&adminv1.UpdateGroupRequest{GroupId: f.group, ProfileIds: &adminv1.ProfileIds{}}))).Msg.Group
	if len(g.ProfileIds) != 0 || g.UserCount != 1 || e.notify.n.Load() != 1 || f.desiredCreds()[u.Id] {
		t.Errorf("group %+v notified %d", g, e.notify.n.Load())
	}
	e.resetNotify()
	must(e.s.UpdateGroup(ctx, req(&adminv1.UpdateGroupRequest{GroupId: f.group, Name: new("renamed")})))
	if e.notify.n.Load() != 0 {
		t.Error("a rename must not notify")
	}
	_, err = e.s.DeleteGroup(ctx, req(&adminv1.DeleteGroupRequest{GroupId: f.group}))
	wantMsg(t, err, "failed_precondition: group_not_empty: users=1")
	empty := e.group("empty")
	must(e.s.DeleteGroup(ctx, req(&adminv1.DeleteGroupRequest{GroupId: empty})))
	if gs := must(e.s.ListGroups(ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups; len(gs) != 1 || gs[0].Name != "renamed" {
		t.Errorf("groups = %v", gs)
	}
}

func TestProfileSecretsAndUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")

	p := e.profile("main", `{"obfs":{"password":"my-own-password"},"port":8443}`)
	if p.Version != 1 || p.Summary != "UDP 8443 · Salamander · Let's Encrypt" {
		t.Errorf("summary = %+v", p)
	}
	// Stored without the secret; the secret sits in the vault column.
	var settings string
	var enc []byte
	e.st.R.QueryRow(`SELECT settings_json, secrets_enc FROM profile WHERE id = ?`, p.Id).Scan(&settings, &enc)
	if strings.Contains(settings, "my-own-password") || strings.Contains(string(enc), "my-own-password") || len(enc) == 0 {
		t.Errorf("secret storage wrong: %s", settings)
	}
	var stored map[string]any
	json.Unmarshal([]byte(settings), &stored)
	if _, ok := stored["obfs"].(map[string]any)["password"]; ok {
		t.Error("password key present in settings_json")
	}

	get := func() *adminv1.GetProfileResponse {
		return must(e.s.GetProfile(ctx, req(&adminv1.GetProfileRequest{ProfileId: p.Id}))).Msg
	}
	if g := get(); !strings.Contains(g.SettingsJson, `"password":"••••"`) || strings.Contains(g.SettingsJson, "my-own-password") {
		t.Errorf("API must mask the secret: %s", g.SettingsJson)
	}

	// Stale version: ABORTED.
	_, err := e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: 7, SettingsJson: new(`{"port":9000}`)}))
	wantMsg(t, err, "aborted: stale_version")

	// Deploy, then dry-run: impact is computed, nothing changes.
	in1 := e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	g := e.group("g", p.Id)
	u := e.user("online-user", g, nil).User
	e.online[u.Id] = "nod_de1"
	e.resetNotify()
	dry := must(e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: p.Id, ExpectedVersion: 1, DryRun: true, SettingsJson: new(`{"port":9000,"obfs":{"password":"••••"}}`)}))).Msg
	if dry.Impact.InboundsRestarted != 2 || dry.Impact.UsersOnline != 1 || len(dry.Impact.AffectedUserNames) != 1 || dry.Impact.AffectedUserNames[0] != "online-user" ||
		len(dry.Impact.CriticalFields) != 1 || dry.Impact.CriticalFields[0] != "/port" || dry.Impact.DevicesNeedReissue != 0 {
		t.Errorf("impact = %v", dry.Impact)
	}
	if dry.Profile.Version != 1 || get().Profile.Version != 1 || !strings.Contains(get().SettingsJson, "8443") || e.notify.n.Load() != 0 {
		t.Error("dry run changed something")
	}

	// A real update: new version, inbounds' spec_version bumped, password kept by the mask, nodes notified.
	up := must(e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: p.Id, ExpectedVersion: 1, SettingsJson: new(`{"port":9000,"obfs":{"password":"••••"}}`)}))).Msg
	if up.Profile.Version != 2 || !strings.HasPrefix(up.Profile.Summary, "UDP 9000") || e.notify.n.Load() != 1 {
		t.Errorf("update: %+v notified %d", up.Profile, e.notify.n.Load())
	}
	var specVer int
	e.st.R.QueryRow(`SELECT spec_version FROM inbound WHERE id = ?`, in1.Id).Scan(&specVer)
	if specVer != 2 {
		t.Errorf("spec_version = %d", specVer)
	}
	ins := must(e.s.Desired(ctx, "nod_de1"))
	if ins[0].Spec.Listen.Port != 9000 || !strings.Contains(string(ins[0].Spec.Settings), "my-own-password") || ins[0].Spec.Version != 2 {
		t.Errorf("node spec = %+v %s", ins[0].Spec.Listen, ins[0].Spec.Settings)
	}

	// Regenerate the secret ("" and $generate), rename-only update does not bump inbounds or notify.
	must(e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: p.Id, ExpectedVersion: 2, SettingsJson: new(`{"obfs":{"password":"$generate"}}`)})))
	ins = must(e.s.Desired(ctx, "nod_de1"))
	if strings.Contains(string(ins[0].Spec.Settings), "my-own-password") || ins[0].Spec.Version != 3 {
		t.Errorf("secret not regenerated: %s v%d", ins[0].Spec.Settings, ins[0].Spec.Version)
	}
	e.resetNotify()
	must(e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: 3, Name: new("renamed")})))
	if ins = must(e.s.Desired(ctx, "nod_de1")); ins[0].Spec.Version != 3 || e.notify.n.Load() != 0 {
		t.Error("a rename must not bump specs or notify")
	}
	// Invalid settings come back with per-field errors as details.
	_, err = e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: 4, SettingsJson: new(`{"port":70000}`)}))
	wantCode(t, err, connect.CodeInvalidArgument)
	if ce := new(connect.Error); !asConnect(err, &ce) || len(ce.Details()) != 1 {
		t.Errorf("field error details missing: %v", err)
	}
	// A change that the nodes cannot take (ACME needs a host name) is rejected up front.
	e.node("nod_ip", "ip1", "203.0.113.10", "active")
	p2 := e.profile("self", `{"tls_mode":"self_signed"}`)
	e.inbound(p2.Id, "nod_ip")
	_, err = e.s.UpdateProfile(ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p2.Id, ExpectedVersion: 1, SettingsJson: new(`{"tls_mode":"acme_domain"}`)}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// Name clash and unknown protocol.
	_, err = e.s.CreateProfile(ctx, req(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "MAIN2"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.s.CreateProfile(ctx, req(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "Renamed"}))
	wantCode(t, err, connect.CodeAlreadyExists)
	_, err = e.s.CreateProfile(ctx, req(&adminv1.CreateProfileRequest{Protocol: "wireguard", Name: "x"}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// Delete: refused while deployed (the message names the nodes), allowed once the inbounds are gone.
	_, err = e.s.DeleteProfile(ctx, req(&adminv1.DeleteProfileRequest{ProfileId: p.Id}))
	wantMsg(t, err, "failed_precondition: profile_deployed: nodes=de1%2C+nl1")
	for _, i := range get().Inbounds {
		must(e.s.DeleteInbound(ctx, req(&adminv1.DeleteInboundRequest{InboundId: i.Id})))
	}
	must(e.s.DeleteProfile(ctx, req(&adminv1.DeleteProfileRequest{ProfileId: p.Id})))
	_, err = e.s.GetProfile(ctx, req(&adminv1.GetProfileRequest{ProfileId: p.Id}))
	wantCode(t, err, connect.CodeNotFound)
}

func asConnect(err error, target **connect.Error) bool {
	ce, ok := err.(*connect.Error)
	if ok {
		*target = ce
	}
	return ok
}

func TestPreviewProfile(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	e.node("nod_de1", "de1", "de1.example.com", "active")
	prev := func(settings, node string) *adminv1.PreviewProfileResponse {
		return must(e.s.PreviewProfile(ctx, req(&adminv1.PreviewProfileRequest{Protocol: "hysteria2", SettingsJson: settings, NodeId: node}))).Msg
	}

	r := prev(`{"obfs":{"password":"••••"}}`, "nod_de1")
	if len(r.Errors) != 0 || r.ClientLabel != "Subscription link · URI list" || r.Summary != "UDP 443 · Salamander · Let's Encrypt" {
		t.Fatalf("preview = %+v", r)
	}
	if !strings.HasPrefix(r.ClientPreview, "hysteria2://••••@de1.example.com:443/?obfs=salamander&obfs-password=••••&sni=de1.example.com#") {
		t.Errorf("client preview = %q", r.ClientPreview)
	}
	// Placeholder host without a node.
	if r := prev(`{}`, ""); !strings.Contains(r.ClientPreview, "@example.com:443/") {
		t.Errorf("placeholder preview = %q", r.ClientPreview)
	}
	// Per-field errors, nothing rendered.
	r = prev(`{"port":0,"obfs":{"password":"x"}}`, "")
	if len(r.Errors) != 2 || r.Errors[0].Pointer != "/port" || r.Errors[1].Pointer != "/obfs/password" || r.ClientPreview != "" {
		t.Errorf("errors = %v", r.Errors)
	}
	// ACME on a node that only has an IP address is reported against the SNI field.
	e.node("nod_ip", "ip1", "203.0.113.10", "active")
	if r := prev(`{}`, "nod_ip"); len(r.Errors) != 1 || r.Errors[0].Pointer != "/sni" {
		t.Errorf("ip node errors = %v", r.Errors)
	}
	_, err := e.s.PreviewProfile(ctx, req(&adminv1.PreviewProfileRequest{Protocol: "nope"}))
	wantCode(t, err, connect.CodeInvalidArgument)

	all := must(e.s.ListProtocols(ctx, req(&adminv1.ListProtocolsRequest{}))).Msg.Protocols
	var ps []*adminv1.ProtocolInfo
	for _, p := range all {
		if p.Id == "hysteria2" {
			ps = append(ps, p)
		}
	}
	if len(all) != 2 || len(ps) != 1 || len(ps[0].Apps) != 1 || ps[0].Apps[0] != adminv1.App_APP_HAPP ||
		!strings.Contains(ps[0].DefaultSettingsJson, `"password":"••••"`) || !json.Valid([]byte(ps[0].SettingsSchemaJson)) {
		t.Errorf("protocols = %+v", ps)
	}
}

func TestInbounds(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_new", "new1", "new1.example.com", "pending")
	e.node("nod_old", "old1", "old1.example.com", "retired")
	p := e.profile("p1", "")
	p2 := e.profile("p2", `{"port":8443,"hop":{"from":20000,"to":30000}}`)

	in := e.inbound(p.Id, "nod_de1")
	if in.Port != 443 || in.TlsServerName != "de1.example.com" || in.State != adminv1.InboundState_INBOUND_STATE_PENDING || in.NodeName != "de1" || in.ProfileName != "p1" {
		t.Errorf("inbound = %+v", in)
	}
	e.inbound(p.Id, "nod_new") // PENDING nodes are allowed
	_, err := e.s.CreateInbound(ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_old"}))
	wantCode(t, err, connect.CodeFailedPrecondition) // retired nodes take no inbounds
	_, err = e.s.CreateInbound(ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_de1"}))
	wantCode(t, err, connect.CodeAlreadyExists)
	_, err = e.s.CreateInbound(ctx, req(&adminv1.CreateInboundRequest{ProfileId: "prf_nope", NodeId: "nod_de1"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.CreateInbound(ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_new", PortOverride: 70000}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// A second profile on the same UDP port of a node clashes; a different port, or a hop range that
	// covers the other port, is judged on the overlap.
	e.resetNotify()
	_, err = e.s.CreateInbound(ctx, req(&adminv1.CreateInboundRequest{ProfileId: p2.Id, NodeId: "nod_de1", PortOverride: 443}))
	wantCode(t, err, connect.CodeAlreadyExists)
	if e.notify.n.Load() != 0 {
		t.Error("a rejected inbound notified")
	}
	in2 := must(e.s.CreateInbound(ctx, req(&adminv1.CreateInboundRequest{ProfileId: p2.Id, NodeId: "nod_de1"}))).Msg.Inbound
	if in2.Port != 8443 || e.notify.n.Load() != 1 {
		t.Errorf("second inbound %+v notified %d", in2, e.notify.n.Load())
	}
	_, err = e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, PortOverride: new(uint32(25000))}))
	wantCode(t, err, connect.CodeAlreadyExists) // inside p2's hop range

	// Overrides bump the spec version, reset the state and notify; clearing the override returns to the profile.
	e.resetNotify()
	up := must(e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, PortOverride: new(uint32(4443)), TlsServerNameOverride: new("vpn.example.com")}))).Msg.Inbound
	if up.Port != 4443 || up.TlsServerName != "vpn.example.com" || e.notify.n.Load() != 1 {
		t.Errorf("updated = %+v", up)
	}
	var sv int
	e.st.R.QueryRow(`SELECT spec_version FROM inbound WHERE id = ?`, in.Id).Scan(&sv)
	if sv != 2 {
		t.Errorf("spec_version %d", sv)
	}
	e.resetNotify()
	same := must(e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, PortOverride: new(uint32(4443))}))).Msg.Inbound
	if same.Port != 4443 || e.notify.n.Load() != 0 {
		t.Error("an unchanged update must be silent")
	}
	cleared := must(e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, PortOverride: new(uint32(0))}))).Msg.Inbound
	if cleared.Port != 443 {
		t.Errorf("cleared override: %+v", cleared)
	}
	off := must(e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, Enabled: new(false)}))).Msg.Inbound
	if off.State != adminv1.InboundState_INBOUND_STATE_DISABLED {
		t.Errorf("disabled state = %v", off.State)
	}
	on := must(e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, Enabled: new(true)}))).Msg.Inbound
	if on.State != adminv1.InboundState_INBOUND_STATE_PENDING {
		t.Errorf("re-enabled state = %v", on.State)
	}
	_, err = e.s.UpdateInbound(ctx, req(&adminv1.UpdateInboundRequest{InboundId: "inb_nope"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.DeleteInbound(ctx, req(&adminv1.DeleteInboundRequest{InboundId: "inb_nope"}))
	wantCode(t, err, connect.CodeNotFound)
}

func TestSubscriptionView(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx
	r := e.user("sub", f.group, func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 1000; m.TermDays = 10 })
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	e.sql(`INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, 'nod_de1', 'hysteria2', ?, 100, 300)`,
		r.User.Id, e.clock.Unix()/3600*3600)
	e.sql(`UPDATE user SET used_bytes = 400 WHERE id = ?`, r.User.Id)

	v := must(e.s.Subscription(ctx, token))
	if v.Status != StatusActive || v.UserName != "sub" || v.Total != 1000 || v.Up != 100 || v.Down != 300 ||
		v.Expires.Unix() != e.clock.AddDate(0, 0, 10).Unix() || len(v.Lines) != 1 {
		t.Fatalf("view = %+v", v)
	}
	u := must(url.Parse(v.Lines[0]))
	if u.Scheme != "hysteria2" || u.Hostname() != "de1.example.com" || u.Port() != "443" || u.Query().Get("obfs") != "salamander" || u.Query().Get("sni") != "de1.example.com" {
		t.Errorf("line = %s", v.Lines[0])
	}
	// The secret in the URI is what the node verifies: sha256(secret) == the verifier in desired state.
	secret := u.User.Username()
	sum := sha256.Sum256([]byte(secret))
	ins := must(e.s.Desired(ctx, f.nodeID))
	if len(ins[0].Creds) != 1 || !strings.Contains(string(ins[0].Creds[0].Data), hex.EncodeToString(sum[:])) {
		t.Errorf("verifier does not match the URI secret: %s", ins[0].Creds[0].Data)
	}
	// The obfs password in the URI is the profile's.
	if pw := u.Query().Get("obfs-password"); len(pw) != 32 {
		t.Errorf("obfs password %q", pw)
	}

	// A failed or disabled inbound, a node that is not active and an unselected node are left out.
	e.sql(`UPDATE inbound SET state = 'failed'`)
	if v := must(e.s.Subscription(ctx, token)); len(v.Lines) != 0 {
		t.Error("failed inbound published")
	}
	e.sql(`UPDATE inbound SET state = 'active'`)
	e.sql(`UPDATE node SET state = 'pending'`)
	if v := must(e.s.Subscription(ctx, token)); len(v.Lines) != 0 {
		t.Error("pending node published")
	}
	e.sql(`UPDATE node SET state = 'active'`)
	if v := must(e.s.Subscription(ctx, token)); len(v.Lines) != 1 {
		t.Error("restored state must publish again")
	}
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	must(e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: r.User.Id, Nodes: &adminv1.NodeSelection{NodeIds: []string{"nod_nl1"}}})))
	if v := must(e.s.Subscription(ctx, token)); len(v.Lines) != 0 {
		t.Error("a node outside the selection was published")
	}

	// Status per clock: expired and limited users get headers data and no lines.
	must(e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: r.User.Id, Nodes: &adminv1.NodeSelection{All: true}})))
	e.clock = e.clock.AddDate(0, 0, 11)
	if v := must(e.s.Subscription(ctx, token)); v.Status != StatusExpired || len(v.Lines) != 0 {
		t.Errorf("expired view = %+v", v)
	}
	e.clock = e.clock.AddDate(0, 0, -11)
	must(e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: r.User.Id, QuotaBytes: new(uint64(400))})))
	if v := must(e.s.Subscription(ctx, token)); v.Status != StatusLimited || len(v.Lines) != 0 || v.Down != 300 {
		t.Errorf("limited view = %+v", v)
	}
	must(e.s.SetUsersEnabled(ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{r.User.Id}, Enabled: false})))
	if v := must(e.s.Subscription(ctx, token)); v.Status != StatusDisabled || len(v.Lines) != 0 {
		t.Errorf("disabled view = %+v", v)
	}
	if _, err := e.s.Subscription(ctx, "nope"); err != ErrUnknownToken {
		t.Errorf("unknown token: %v", err)
	}
}

func TestSubscriptionSelfSignedNeedsPin(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	e.node("nod_ip", "ip1", "203.0.113.10", "active")
	p := e.profile("self", `{"tls_mode":"self_signed","obfs":{"type":"none"}}`)
	e.inbound(p.Id, "nod_ip")
	g := e.group("g", p.Id)
	r := e.user("u", g, nil)
	token := r.SubscriptionUrl[strings.LastIndex(r.SubscriptionUrl, "/")+1:]
	if v := must(e.s.Subscription(ctx, token)); len(v.Lines) != 0 {
		t.Fatalf("published before the node reported its certificate: %v", v.Lines)
	}
	pin := strings.Repeat("ab", 32)
	e.sql(`UPDATE inbound SET cert_pin_sha256 = ?, state = 'active'`, pin)
	v := must(e.s.Subscription(ctx, token))
	if len(v.Lines) != 1 || !strings.Contains(v.Lines[0], "?insecure=1&pinSHA256="+pin+"#") || strings.Contains(v.Lines[0], "sni=") {
		t.Errorf("lines = %v", v.Lines)
	}
}

func TestSubscriptionLinkNeedsBase(t *testing.T) {
	e := newEnv(t)
	e.s.cfg.SubscriptionBaseURL = ""
	g := e.group("g")
	_, err := e.s.CreateUser(e.ctx, req(&adminv1.CreateUserRequest{Name: "u", GroupId: g}))
	wantMsg(t, err, "failed_precondition: sub_address_missing")
}

func TestListenOverlap(t *testing.T) {
	l := func(port, from, to uint16) plugin.Listen { return plugin.Listen{Port: port, HopFrom: from, HopTo: to} }
	for _, c := range []struct {
		name string
		a, b plugin.Listen
		want bool
	}{
		{"same port", l(443, 0, 0), l(443, 0, 0), true},
		{"different ports", l(443, 0, 0), l(8443, 0, 0), false},
		{"port inside a hop range", l(25000, 0, 0), l(8443, 20000, 30000), true},
		{"the gap between a port and its own hop range is free", l(15000, 0, 0), l(8443, 20000, 30000), false},
		{"a port below another hop range", l(443, 0, 0), l(8443, 20000, 30000), false},
		{"overlapping hop ranges", l(443, 20000, 30000), l(8443, 25000, 35000), true},
		{"disjoint hop ranges", l(443, 20000, 30000), l(8443, 30001, 35000), false},
		{"a hop range that covers the other port", l(443, 8000, 9000), l(8443, 0, 0), true},
	} {
		if got := listenOverlap(c.a, c.b); got != c.want {
			t.Errorf("%s: listenOverlap = %v, want %v", c.name, got, c.want)
		}
		if got := listenOverlap(c.b, c.a); got != c.want {
			t.Errorf("%s (swapped): listenOverlap = %v, want %v", c.name, got, c.want)
		}
	}
}

// User, group and profile names are 1-64 characters, as the admin's form counts them: a Cyrillic letter is one, not two.
func TestNamesCountCharactersNotBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{strings.Repeat("я", 64), true},
		{strings.Repeat("я", 40), true},
		{strings.Repeat("я", 65), false},
		{strings.Repeat("x", 65), false},
		{"Алиса\x01", false},
		{"bad\xffutf8", false},
		{"  ", false},
	} {
		if _, err := cleanName("user", c.name); (err == nil) != c.ok {
			t.Errorf("cleanName(%d runes): err=%v, want ok=%v", len([]rune(c.name)), err, c.ok)
		}
	}
}
