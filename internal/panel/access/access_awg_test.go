package access

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// AmneziaWG devices: IPAM, limit, rotation, revocation, staleness, the lazy implicit device
// and the client networks. The hysteria2 behaviour stays under the older tests of this package.

// amnOnly makes a user of the Amnezia app alone (no implicit hysteria2 device), after the optional edits.
func amnOnly(edits ...func(*adminv1.CreateUserRequest)) func(*adminv1.CreateUserRequest) {
	return func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Amnezia: true}
		for _, f := range edits {
			f(m)
		}
	}
}

func (e *env) awgProfile(name, settings string) *adminv1.ProfileSummary {
	e.t.Helper()
	return must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "awg", Name: name, SettingsJson: settings}))).Msg.Profile
}

type awgFixture struct {
	e       *env
	nodeID  string
	profile string
	inbound string
	group   string
	user    string // alice, Amnezia app on
}

func newAWGFixture(t *testing.T) *awgFixture {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.awgProfile("awg31", "")
	in := e.inbound(p.Id, "nod_de1")
	g := e.group("default", p.Id)
	u := e.user("alice", g, amnOnly()).User
	return &awgFixture{e: e, nodeID: "nod_de1", profile: p.Id, inbound: in.Id, group: g, user: u.Id}
}

func (f *awgFixture) add(user, label string) *adminv1.CreateAwgDeviceResponse {
	f.e.t.Helper()
	return must(f.e.s.CreateAwgDevice(f.e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: user, ProfileId: f.profile, Platform: "ios", Label: label}))).Msg
}

func (f *awgFixture) addErr(user string) error {
	_, err := f.e.s.CreateAwgDevice(f.e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: user, ProfileId: f.profile, Platform: "ios"}))
	return err
}

func (f *awgFixture) desired() []plugin.UserCred {
	f.e.t.Helper()
	for _, in := range must(f.e.s.Desired(f.e.ctx, f.nodeID)) {
		if in.Spec.Protocol == "awg" && in.Spec.ProfileID == f.profile {
			return in.Creds
		}
	}
	return nil
}

func (f *awgFixture) user_(id string) *adminv1.GetUserResponse {
	return must(f.e.s.GetUser(f.e.ctx, req(&adminv1.GetUserRequest{UserId: id}))).Msg
}

func pubOf(t *testing.T, data json.RawMessage) string {
	t.Helper()
	var nd awg.NodeData
	if err := json.Unmarshal(data, &nd); err != nil || nd.PublicKey == "" {
		t.Fatalf("node data %s: %v", data, err)
	}
	return nd.PublicKey
}

func TestAWGDeviceCreateAndDesiredState(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.resetNotify()
	r := f.add(f.user, "")
	if r.Device.AwgProfileId != f.profile || r.Device.AwgProfileName != "awg31" || r.Device.AwgVersion != "3.1" ||
		r.Device.Model != "ios 1" || r.Device.Platform != "ios" || r.Device.Stale || r.Device.Address == "" ||
		r.Device.LastHandshakeUnix != 0 || len(r.Device.Protocols) != 1 || r.Device.Protocols[0] != "awg" {
		t.Errorf("device = %+v", r.Device)
	}
	if !strings.HasPrefix(r.Device.Address, "10.66.4.2, fd66:66:0:1::2") {
		t.Errorf("first address = %q", r.Device.Address)
	}
	if r.User.DevicesUsed != 1 {
		t.Errorf("devices used = %d", r.User.DevicesUsed)
	}
	if e.notify.n.Load() == 0 {
		t.Error("the nodes were not told")
	}
	if len(r.Configs) != 1 {
		t.Fatalf("configs = %d", len(r.Configs))
	}
	c := r.Configs[0]
	if c.NodeId != "nod_de1" || c.NodeName != "de1" || c.AwgVersion != "3.1" || c.ProfileName != "awg31" || c.ConfFilename != "mistgate-de1.conf" ||
		c.Stale || !strings.HasPrefix(c.VpnKey, "vpn://") || !strings.Contains(c.Conf, "[Interface]") || !strings.Contains(c.Conf, "Endpoint = de1.example.com:") ||
		!strings.Contains(c.Conf, "MTU = 1280") {
		t.Errorf("config = %+v", c)
	}
	if len(c.MinClients) == 0 || c.MinClients[0].App != "AmneziaVPN" || c.MinClients[0].MinVersion != "5.0.1.5" {
		t.Errorf("min clients = %v", c.MinClients)
	}
	for _, w := range c.Warnings {
		if w == "dns_fallback" {
			t.Errorf("the default DNS preset carries two IPv4 servers, got %v", c.Warnings)
		}
	}
	if !contains(c.Warnings, "amnezia_desktop_mtu") {
		t.Errorf("warnings = %v", c.Warnings)
	}

	// The node gets the peer's public key, preshared key and addresses, never the private key.
	creds := f.desired()
	if len(creds) != 1 || creds[0].UserID != f.user || creds[0].DeviceID != r.Device.Id {
		t.Fatalf("desired creds = %+v", creds)
	}
	var nd awg.NodeData
	if err := json.Unmarshal(creds[0].Data, &nd); err != nil || len(nd.AllowedIPs) != 2 || nd.AllowedIPs[0] != "10.66.4.2/32" || nd.PSK == "" {
		t.Errorf("node data = %s (%v)", creds[0].Data, err)
	}
	if strings.Contains(string(creds[0].Data), "priv") {
		t.Error("the private key reached the node data")
	}
	var privKey string
	for _, line := range strings.Split(c.Conf, "\n") {
		if k, ok := strings.CutPrefix(strings.TrimSpace(line), "PrivateKey = "); ok {
			privKey = k
		}
	}
	if privKey == "" || strings.Contains(string(creds[0].Data), privKey) {
		t.Errorf("private key %q", privKey)
	}
	// The spec: tunnel, port and the server key of the inbound (only for the node).
	for _, in := range must(e.s.Desired(e.ctx, f.nodeID)) {
		if in.Spec.Tunnel.IsZero() || in.Spec.Tunnel.MTU != 1280 || in.Spec.Listen.Network != "udp" || in.Spec.Listen.Port < 10000 ||
			!strings.Contains(string(in.Spec.Settings), `"private_key"`) {
			t.Errorf("spec = %+v", in.Spec)
		}
	}

	// The audit row names the action and the device and carries no key (the rows before it are the fixture's setup).
	rows := must(e.st.ListAudit(e.ctx, "", 0, 10))
	if len(rows) == 0 || rows[0].Action != "device_create" || !strings.Contains(rows[0].Params, r.Device.Id) || strings.Contains(rows[0].Params, privKey) {
		t.Errorf("audit = %+v", rows)
	}
	// What the admin page lists.
	got := f.user_(f.user)
	if len(got.Devices) != 1 || got.Devices[0].Id != r.Device.Id || got.Devices[0].AwgProfileName != "awg31" {
		t.Errorf("devices = %+v", got.Devices)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestAWGDeviceCreateRejections(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	msgOf := func(err error) string { return strings.SplitN(err.Error(), ":", 2)[1] }
	precondition := func(err error, want string) {
		t.Helper()
		wantCode(t, err, connect.CodeFailedPrecondition)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want %q", err, want)
		}
	}

	// A user who is disabled, or has the Amnezia toggle off.
	d := e.user("dave", f.group, amnOnly()).User
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{d.Id}, Enabled: false})))
	precondition(f.addErr(d.Id), "user_disabled")
	h := e.user("hank", f.group, func(m *adminv1.CreateUserRequest) { m.Apps = &adminv1.AppToggles{Happ: true} }).User
	precondition(f.addErr(h.Id), "app_disabled")

	// A profile outside the user's group, a profile without a usable inbound, an old agent.
	other := e.awgProfile("other", "")
	_, err := e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: f.user, ProfileId: other.Id, Platform: "ios"}))
	precondition(err, "profile_not_in_group")
	g2 := e.group("g2", f.profile, other.Id)
	u2 := e.user("bob", g2, amnOnly()).User
	_, err = e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u2.Id, ProfileId: other.Id, Platform: "ios"}))
	precondition(err, "no_inbound")
	e.s.caps = fakeCaps{"nod_de1": false}
	precondition(f.addErr(f.user), "agent too old")
	e.s.caps = fakeCaps{"nod_other": false} // a node never seen: not too old
	f.add(f.user, "ok")
	e.s.caps = nil

	// Bad input and unknown things.
	for name, r := range map[string]*adminv1.CreateAwgDeviceRequest{
		"platform":   {UserId: f.user, ProfileId: f.profile, Platform: "palm"},
		"long label": {UserId: f.user, ProfileId: f.profile, Platform: "ios", Label: strings.Repeat("x", 41)},
		"newline":    {UserId: f.user, ProfileId: f.profile, Platform: "ios", Label: "a\nb"},
		"no profile": {UserId: f.user, Platform: "ios"},
	} {
		if _, err := e.s.CreateAwgDevice(e.ctx, req(r)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	hy := e.profile("hy", "")
	e.group("hy-group", hy.Id)
	_, err = e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: f.user, ProfileId: hy.Id, Platform: "ios"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: "usr_nope", ProfileId: f.profile, Platform: "ios"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: f.user, ProfileId: "prf_nope", Platform: "ios"}))
	wantCode(t, err, connect.CodeNotFound)
	_ = msgOf
	// Nothing was stored by the rejected calls: one device (the "ok" one).
	if n := must(e.st.Access().DeviceCounts(e.ctx, []string{f.user}))[f.user]; n != 1 {
		t.Errorf("devices = %d", n)
	}
}

type fakeCaps map[string]bool

func (c fakeCaps) AgentCapability(nodeID, _ string) (bool, bool) {
	has, known := c[nodeID]
	return known, has
}

func TestAWGDeviceLimit(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	u := e.user("limited", f.group, amnOnly(func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })).User
	f.add(u.Id, "")
	f.add(u.Id, "")
	err := f.addErr(u.Id)
	wantCode(t, err, connect.CodeFailedPrecondition)
	if !strings.Contains(err.Error(), "device_limit: 2/2") {
		t.Errorf("message = %v", err)
	}
	// A revoked device gives its slot back.
	devs := f.user_(u.Id).Devices
	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: devs[0].Id})))
	f.add(u.Id, "again")
	if got := f.user_(u.Id).User.DevicesUsed; got != 2 {
		t.Errorf("devices used = %d", got)
	}
}

func TestAWGConcurrentAdds(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	// One user, limit 5, twelve racing additions: exactly five win.
	u := e.user("racer", f.group, amnOnly(func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 5 })).User
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := f.addErr(u.Id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case connect.CodeOf(err) == connect.CodeFailedPrecondition && strings.Contains(err.Error(), "device_limit"):
				limited++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 5 || limited != 7 {
		t.Errorf("ok=%d limited=%d", ok, limited)
	}

	// Many users on one profile: every tunnel address and every public key is distinct.
	var users []string
	for i := range 6 {
		users = append(users, e.user(fmt.Sprintf("u%d", i), f.group, amnOnly(func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 10 })).User.Id)
	}
	for range 4 {
		for _, id := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := f.addErr(id); err != nil {
					t.Errorf("add: %v", err)
				}
			}()
		}
	}
	wg.Wait()
	seenAddr, seenKey := map[string]bool{}, map[string]bool{}
	for _, c := range f.desired() {
		var nd awg.NodeData
		if err := json.Unmarshal(c.Data, &nd); err != nil {
			t.Fatal(err)
		}
		if seenAddr[nd.AllowedIPs[0]] || seenKey[nd.PublicKey] {
			t.Errorf("duplicate %v / %s", nd.AllowedIPs, nd.PublicKey)
		}
		seenAddr[nd.AllowedIPs[0]], seenKey[nd.PublicKey] = true, true
	}
	if len(seenAddr) != 5+24 {
		t.Errorf("%d peers, want 29", len(seenAddr))
	}
}

func (f *awgFixture) idxOf(deviceID string) int {
	f.e.t.Helper()
	var idx int
	err := f.e.st.R.QueryRow(`SELECT ap.idx FROM awg_peer ap JOIN device_credential c ON c.id = ap.credential_id
		WHERE c.device_id = ? AND ap.released_at = 0`, deviceID).Scan(&idx)
	if err != nil {
		f.e.t.Fatalf("idx of %s: %v", deviceID, err)
	}
	return idx
}

func TestAWGRevokeQuarantineAndReuse(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	a, b := f.add(f.user, "a").Device, f.add(f.user, "b").Device
	if f.idxOf(a.Id) != 2 || f.idxOf(b.Id) != 3 {
		t.Fatalf("idx = %d, %d", f.idxOf(a.Id), f.idxOf(b.Id))
	}
	e.resetNotify()
	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: a.Id})))
	if e.notify.n.Load() == 0 {
		t.Error("revoking did not notify the nodes")
	}
	if creds := f.desired(); len(creds) != 1 || creds[0].DeviceID != b.Id {
		t.Errorf("desired creds after revoke = %+v", creds)
	}
	// The released address stays out of circulation for 24 hours.
	c := f.add(f.user, "c").Device
	if f.idxOf(c.Id) != 4 {
		t.Errorf("new device reused a quarantined address: idx %d", f.idxOf(c.Id))
	}
	e.clock = e.clock.Add(25 * time.Hour)
	d := f.add(f.user, "d").Device
	if f.idxOf(d.Id) != 2 {
		t.Errorf("after the quarantine the smallest free index is 2, got %d", f.idxOf(d.Id))
	}
	// A revoked device is gone from the lists and its configs cannot be fetched.
	_, err := e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: a.Id}))
	wantCode(t, err, connect.CodeNotFound)
	for _, dv := range f.user_(f.user).Devices {
		if dv.Id == a.Id {
			t.Error("revoked device still listed")
		}
	}
}

func TestAWGSubnetFull(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.awgProfile("tiny", `{"subnet4":"10.77.0.0/24","subnet6":""}`)
	e.inbound(p.Id, "nod_de1")
	g := e.group("g", p.Id)
	var fillers []string
	for i := range 3 {
		fillers = append(fillers, e.user(fmt.Sprintf("fill%d", i), g, amnOnly(func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 100 })).User.Id)
	}
	u := e.user("last", g, amnOnly()).User
	max, err := awg.MaxPeerIndex(json.RawMessage(`{"version":"3.1","port":10001,"mtu":1280,"egress":"direct","subnet4":"10.77.0.0/24","subnet6":"","obfuscation":{}}`))
	if err != nil || max != 254 {
		t.Fatalf("max idx = %d (%v)", max, err)
	}
	a := e.st.Access()
	for i := range max - 1 { // indexes 2..254 = 253 peers, filled at store level
		id := fmt.Sprintf("dev_fill%d", i)
		_, err := a.AddAWGDevice(e.ctx, store.AWGDeviceAdd{Device: store.AccessDevice{ID: id, UserID: fillers[i/100]}, ProfileID: p.Id, MaxIdx: max, Limit: 100}, e.clock,
			func(idx int) (store.AccessCred, string, error) {
				return store.AccessCred{ID: fmt.Sprintf("crd_fill%d", idx), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, fmt.Sprintf("pub%d", idx), nil
			})
		if err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	_, err = e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u.Id, ProfileId: p.Id, Platform: "ios"}))
	wantCode(t, err, connect.CodeFailedPrecondition)
	if !strings.Contains(err.Error(), "subnet_full") {
		t.Errorf("message = %v", err)
	}
}

func TestAWGRotate(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	d := f.add(f.user, "phone")
	before := f.desired()
	if len(before) != 1 {
		t.Fatalf("creds = %d", len(before))
	}
	oldPub, oldConf := pubOf(t, before[0].Data), d.Configs[0].Conf
	e.resetNotify()
	r := must(e.s.RotateDeviceKeys(e.ctx, req(&adminv1.RotateDeviceKeysRequest{DeviceId: d.Device.Id}))).Msg
	if e.notify.n.Load() == 0 {
		t.Error("rotating did not notify the nodes")
	}
	if r.Device.Address != d.Device.Address || r.Device.Id != d.Device.Id || r.Device.Model != "phone" || r.Device.Stale {
		t.Errorf("device after rotation = %+v", r.Device)
	}
	after := f.desired()
	if len(after) != 1 || after[0].CredID == before[0].CredID || pubOf(t, after[0].Data) == oldPub {
		t.Fatalf("desired after rotation = %+v", after)
	}
	if r.Configs[0].Conf == oldConf || !strings.Contains(r.Configs[0].Conf, "Address = 10.66.4.2/32") {
		t.Error("the new config must differ and keep the address")
	}
	// The old address is the same row index: a new device must not take it, and the old credential is revoked.
	if f.idxOf(d.Device.Id) != 2 {
		t.Errorf("idx = %d", f.idxOf(d.Device.Id))
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE idx = 2 AND released_at > 0`); n != 1 {
		t.Errorf("released rows of idx 2 = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, d.Device.Id); n != 1 {
		t.Errorf("live credentials = %d", n)
	}
	if other := f.add(f.user, "tablet").Device; f.idxOf(other.Id) != 3 {
		t.Errorf("idx of the next device = %d", f.idxOf(other.Id))
	}
	// Rotating a device that is gone, or one that belongs to somebody else through the owner API.
	_, err := e.s.RotateDeviceKeys(e.ctx, req(&adminv1.RotateDeviceKeysRequest{DeviceId: "dev_nope"}))
	wantCode(t, err, connect.CodeNotFound)
	_, _, err = e.s.RotateDevice(e.ctx, "user:x", "usr_someone_else", d.Device.Id)
	wantCode(t, err, connect.CodeNotFound)
	rows := must(e.st.ListAudit(e.ctx, "", 0, 10))
	if !auditHas(rows, "device_rotate") {
		t.Errorf("audit = %+v", rows)
	}
}

func auditHas(rows []store.AuditRow, action string) bool {
	for _, r := range rows {
		if r.Action == action {
			return true
		}
	}
	return false
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestAWGStaleAndImpact(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	bob := e.user("bob", f.group, amnOnly()).User
	d1 := f.add(f.user, "a").Device
	d2 := f.add(bob.Id, "b").Device
	p := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: f.profile}))).Msg.Profile
	upd := func(settings string, dry bool) *adminv1.UpdateProfileResponse {
		t.Helper()
		ver := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: f.profile}))).Msg.Profile.Version
		return must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: f.profile, ExpectedVersion: ver, SettingsJson: &settings, DryRun: dry}))).Msg
	}
	stale := func(user string) bool { return f.user_(user).Devices[0].Stale }
	_ = p

	// A client-side, non-critical change (junk count, keepalive) leaves the configs valid.
	r := upd(`{"obfuscation":{"jc":7,"persistent_keepalive":"20-30"}}`, false)
	if r.Impact.DevicesNeedReissue != 0 || len(r.Impact.CriticalFields) != 0 || stale(f.user) {
		t.Errorf("non-critical: impact %+v stale %v", r.Impact, stale(f.user))
	}
	// A dry run of a critical change reports the devices and changes nothing. (S1 must differ from the generated one.)
	s1 := 30
	if e.profileSettings(f.profile).Obfuscation.S1 == s1 {
		s1 = 31
	}
	crit := fmt.Sprintf(`{"obfuscation":{"s1":%d}}`, s1)
	dry := upd(crit, true)
	if dry.Impact.DevicesNeedReissue != 2 || len(dry.Impact.CriticalFields) != 1 || dry.Impact.CriticalFields[0] != "/obfuscation/s1" {
		t.Errorf("dry impact = %+v", dry.Impact)
	}
	if !contains(dry.Impact.AffectedUserNames, "alice") || !contains(dry.Impact.AffectedUserNames, "bob") {
		t.Errorf("affected = %v", dry.Impact.AffectedUserNames)
	}
	if stale(f.user) {
		t.Error("a dry run made a device stale")
	}
	// The real change: both devices are stale, the admin page says so.
	upd(crit, false)
	if !stale(f.user) || !stale(bob.Id) {
		t.Error("devices are not stale after an x-critical change")
	}
	// The user gets a new config: the badge goes away (only for that device), and the config says it replaces one.
	r2 := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: d1.Id}))).Msg
	if !r2.Configs[0].Stale || r2.Device.Stale || stale(f.user) || !stale(bob.Id) {
		t.Errorf("after fetching: config.stale=%v device.stale=%v user=%v bob=%v", r2.Configs[0].Stale, r2.Device.Stale, stale(f.user), stale(bob.Id))
	}
	if again := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: d1.Id}))).Msg; again.Configs[0].Stale {
		t.Error("a second fetch still reports a stale config")
	}
	// A new device is created at the current epoch; a rotated one too.
	if d3 := f.add(f.user, "c"); d3.Device.Stale || d3.Configs[0].Stale {
		t.Error("a fresh device is stale")
	}
	rot := must(e.s.RotateDeviceKeys(e.ctx, req(&adminv1.RotateDeviceKeysRequest{DeviceId: d2.Id}))).Msg
	if rot.Device.Stale {
		t.Error("a rotated device is stale")
	}
	// The new config carries the new obfuscation.
	if !strings.Contains(r2.Configs[0].Conf, fmt.Sprintf("S1 = %d", s1)) {
		t.Errorf("config does not carry the changed S1:\n%s", r2.Configs[0].Conf)
	}
	// Adding a node to the profile does not make anyone stale.
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.inbound(f.profile, "nod_nl1")
	if stale(f.user) {
		t.Error("a new node made a device stale")
	}
	cfgs := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: d1.Id}))).Msg.Configs
	if len(cfgs) != 2 || cfgs[0].NodeName != "de1" || cfgs[1].NodeName != "nl1" {
		t.Errorf("one config per node expected: %+v", cfgs)
	}
	// A port override on one inbound changes the endpoint of every issued config of the profile.
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: cfgs[1].InboundId, PortOverride: new(uint32(12345))})))
	if !stale(f.user) {
		t.Error("a port override did not mark the devices stale")
	}
	// The second node gets its own server key: public keys differ between nodes, the address and keys of the device do not.
	if cfgs[0].Conf == cfgs[1].Conf || !strings.Contains(cfgs[1].Conf, "nl1.example.com") {
		t.Error("configs of two nodes must differ by endpoint and server key")
	}
}

func TestAWGProfileImpactWithoutDevices(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	// S1 must differ from the generated one (the four S are equal and random in 12..32), or nothing changes
	s1 := 31
	if e.profileSettings(f.profile).Obfuscation.S1 == s1 {
		s1 = 30
	}
	s := fmt.Sprintf(`{"obfuscation":{"s1":%d}}`, s1)
	r := must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: f.profile, ExpectedVersion: 1, SettingsJson: &s, DryRun: true}))).Msg
	if r.Impact.DevicesNeedReissue != 0 || len(r.Impact.CriticalFields) != 1 || r.Impact.InboundsRestarted != 1 {
		t.Errorf("impact = %+v", r.Impact)
	}
}

func TestAWGClientNetworks(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	nets := func(id string) (string, string) {
		var s struct{ Subnet4, Subnet6 string }
		_ = json.Unmarshal([]byte(must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: id}))).Msg.SettingsJson), &s)
		return s.Subnet4, s.Subnet6
	}
	// Each new profile takes the first free slot of the panel.
	p1, p2, p3 := e.awgProfile("a", ""), e.awgProfile("b", ""), e.awgProfile("c", "")
	if a, b := nets(p1.Id); a != "10.66.4.0/22" || b != "fd66:66:0:1::/64" {
		t.Errorf("first profile: %s %s", a, b)
	}
	if a, b := nets(p2.Id); a != "10.66.8.0/22" || b != "fd66:66:0:2::/64" {
		t.Errorf("second profile: %s %s", a, b)
	}
	// A freed slot is taken again (first free, not next).
	must(e.s.DeleteProfile(e.ctx, req(&adminv1.DeleteProfileRequest{ProfileId: p2.Id})))
	if a, _ := nets(e.awgProfile("d", "").Id); a != "10.66.8.0/22" {
		t.Errorf("reused slot: %s", a)
	}
	// A named network that overlaps another profile is rejected at the field.
	_, err := e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "x", SettingsJson: `{"subnet4":"10.66.6.0/24"}`}))
	wantCode(t, err, connect.CodeInvalidArgument)
	if !strings.Contains(err.Error(), "/subnet4") {
		t.Errorf("error = %v", err)
	}
	// A named network is kept, the other one is taken from a free slot.
	q := e.awgProfile("y", `{"subnet4":"192.168.50.0/24"}`)
	if a, b := nets(q.Id); a != "192.168.50.0/24" || b != "fd66:66:0:4::/64" {
		t.Errorf("named v4: %s %s", a, b)
	}
	z := e.awgProfile("z", `{"subnet6":""}`)
	if _, b := nets(z.Id); b != "" {
		t.Errorf("an explicit empty IPv6 network must stay empty: %q", b)
	}
	// Before anything is deployed the network can change (but not onto another profile's); after the first inbound it cannot.
	up := func(id, settings string) error {
		ver := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: id}))).Msg.Profile.Version
		_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: id, ExpectedVersion: ver, SettingsJson: &settings}))
		return err
	}
	if err := up(p1.Id, `{"subnet4":"10.66.4.0/23"}`); err != nil {
		t.Errorf("free network change: %v", err)
	}
	if err := up(p1.Id, `{"subnet4":"192.168.50.0/24"}`); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("overlap on update: %v", err)
	}
	e.inbound(p3.Id, "nod_de1")
	err = up(p3.Id, `{"subnet4":"172.20.0.0/22"}`)
	wantCode(t, err, connect.CodeInvalidArgument)
	if !strings.Contains(err.Error(), "/subnet4") {
		t.Errorf("immutable error = %v", err)
	}
	if err := up(p3.Id, `{"mtu":1300}`); err != nil {
		t.Errorf("unrelated change on a deployed profile: %v", err)
	}
}

func TestAWGPreviewChoosesFreeClientNetworkWhenUnset(t *testing.T) {
	e := newEnv(t)
	e.awgProfile("first", "")

	preview := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg", SettingsJson: `{}`}))).Msg
	if len(preview.Errors) != 0 {
		t.Fatalf("preview errors: %+v", preview.Errors)
	}
	if !strings.Contains(preview.ClientPreview, "10.66.8.") || !strings.Contains(preview.ClientPreview, "fd66:66:0:2::") {
		t.Fatalf("preview did not use the next free client network: %s", preview.ClientPreview)
	}
}

func TestAWGClientNetworksFixedAfterDevices(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	d := f.add(f.user, "")
	e.clock = e.clock.Add(time.Minute)
	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: d.Device.Id})))
	must(e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: f.inbound})))
	s := `{"subnet4":"172.20.0.0/22"}`
	_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: f.profile, ExpectedVersion: 1, SettingsJson: &s}))
	wantCode(t, err, connect.CodeInvalidArgument) // a released address is still in quarantine: the network stays
}

func TestAWGDesiredStateJoinsByProfile(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	ob20 := must(e.s.GenerateObfuscation(e.ctx, req(&adminv1.GenerateObfuscationRequest{Version: "2.0", Preset: "dns"}))).Msg.ObfuscationJson
	p2 := e.awgProfile("awg20", `{"version":"2.0","obfuscation":`+ob20+`}`)
	if p2.Summary == "" || !strings.Contains(p2.Summary, "AWG 2.0") {
		t.Fatalf("summary = %q", p2.Summary)
	}
	in2 := e.inbound(p2.Id, "nod_de1")
	g := e.group("both", f.profile, p2.Id)
	u := e.user("carol", g, amnOnly()).User
	d1 := f.add(u.Id, "v31")
	d2 := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u.Id, ProfileId: p2.Id, Platform: "android", Label: "v20"}))).Msg
	if d2.Device.AwgVersion != "2.0" || d1.Device.AwgVersion != "3.1" {
		t.Fatalf("versions %s %s", d1.Device.AwgVersion, d2.Device.AwgVersion)
	}
	if strings.Contains(d2.Configs[0].Conf, "HeaderProtectionKey") || strings.Contains(d2.Configs[0].Conf, "RandomTrailers") || strings.Contains(d2.Configs[0].Conf, "DisableCookies") {
		t.Errorf("a 2.0 config must not carry 3.x keys:\n%s", d2.Configs[0].Conf)
	}
	byInbound := map[string][]string{}
	for _, in := range must(e.s.Desired(e.ctx, f.nodeID)) {
		for _, c := range in.Creds {
			if c.UserID == u.Id {
				byInbound[in.Spec.ProfileID] = append(byInbound[in.Spec.ProfileID], c.DeviceID)
			}
		}
	}
	if len(byInbound[f.profile]) != 1 || byInbound[f.profile][0] != d1.Device.Id ||
		len(byInbound[p2.Id]) != 1 || byInbound[p2.Id][0] != d2.Device.Id {
		t.Errorf("credentials per inbound: %v (inbounds %s, %s)", byInbound, f.inbound, in2.Id)
	}
}

func TestAWGInboundKeyMaterial(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	in2 := e.inbound(f.profile, "nod_nl1")
	var enc1, enc2 []byte
	var pub1, pub2 string
	for id, dst := range map[string]struct {
		enc *[]byte
		pub *string
	}{f.inbound: {&enc1, &pub1}, in2.Id: {&enc2, &pub2}} {
		if err := e.st.R.QueryRow(`SELECT plugin_state_enc, plugin_public_json FROM inbound WHERE id = ?`, id).Scan(dst.enc, dst.pub); err != nil {
			t.Fatal(err)
		}
	}
	if len(enc1) == 0 || len(enc2) == 0 || string(enc1) == string(enc2) || pub1 == pub2 || !strings.Contains(pub1, `"public_key"`) {
		t.Fatalf("key material: %d %d %q %q", len(enc1), len(enc2), pub1, pub2)
	}
	priv, err := e.s.vault.Open(enc1, f.inbound)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pub1, string(priv)) {
		t.Error("the private key is in the public column")
	}
	for _, in := range must(e.s.Desired(e.ctx, f.nodeID)) {
		var ns awg.NodeSettings
		if err := json.Unmarshal(in.Spec.Settings, &ns); err != nil || ns.PrivateKey != string(priv) {
			t.Errorf("node settings = %s (%v)", in.Spec.Settings, err)
		}
	}
	// The key material of one inbound must not open under another inbound's id.
	if _, err := e.s.vault.Open(enc1, in2.Id); err == nil {
		t.Error("the sealed key opened under a foreign id")
	}
	// hysteria2 inbounds carry none.
	hy := e.profile("hy", "")
	e.inbound(hy.Id, "nod_de1")
	if n := e.count(`SELECT count(*) FROM inbound WHERE plugin_state_enc IS NOT NULL`); n != 2 {
		t.Errorf("inbounds with key material = %d", n)
	}
	// Every inbound of the profile reports its state from the stored AwgHealth.
	e.sql(`UPDATE inbound SET awg_health_json = ?, awg_health_at = ? WHERE id = ?`,
		`{"backend":"userspace","backendVersion":"amneziawg-go v3.1.20260828","ifaceUp":true,"peers":3,"peersHandshaken":2,"peersOnline":1}`, e.clock.Unix(), f.inbound)
	got := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: f.profile}))).Msg.Inbounds
	var seen int
	for _, in := range got {
		if in.Id == f.inbound {
			seen++
			if in.Awg == nil || in.Awg.Backend != "userspace" || in.Awg.Peers != 3 || in.Awg.PeersOnline != 1 || !in.Awg.IfaceUp || in.Awg.ReportedUnix != e.clock.Unix() {
				t.Errorf("awg status = %+v", in.Awg)
			}
		} else if in.Awg != nil {
			t.Errorf("an inbound that never reported has a status: %+v", in.Awg)
		}
	}
	if seen != 1 {
		t.Errorf("inbounds = %+v", got)
	}
}

func TestAWGImplicitDeviceIsLazy(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	// A user of the Amnezia app alone owns no device until something needs one.
	if got := f.user_(f.user); len(got.Devices) != 0 || got.User.DevicesUsed != 0 {
		t.Fatalf("a new Amnezia-only user has devices: %+v", got.Devices)
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ?`, f.user); n != 0 {
		t.Errorf("empty implicit device rows = %d", n)
	}
	// The default format needs no AWG credential and does not create the device.
	token := e.tokenOf(f.user)
	v := must(e.s.Subscription(e.ctx, token))
	if len(v.Lines) != 0 || v.DevicesUsed != 0 || !v.AccessAmnezia || v.AccessHapp || len(v.AWGProfiles) != 1 || v.AWGProfiles[0].Name != "awg31" || v.AWGProfiles[0].Version != "3.1" {
		t.Errorf("uri view = %+v", v)
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ?`, f.user); n != 0 {
		t.Errorf("a URI-list fetch created a device: %d", n)
	}
	// The first Mihomo fetch issues the credential of every usable AWG profile on the implicit device.
	e.resetNotify()
	name := func(s SubServer) string { return "srv " + s.Node + " / " + s.Profile }
	m := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: plugin.FormatMihomo, Name: name}))
	if len(m.Lines) != 1 || m.Format != plugin.FormatMihomo || !strings.Contains(m.Lines[0], "srv de1 / awg31") || !strings.Contains(m.Lines[0], "amnezia-wg-option") ||
		m.Servers[0].Protocol != "awg" || m.Servers[0].ProfileID != f.profile || m.DevicesUsed != 0 {
		t.Fatalf("mihomo view = %+v", m)
	}
	if e.notify.n.Load() == 0 {
		t.Error("new credentials did not notify the nodes")
	}
	if creds := f.desired(); len(creds) != 1 {
		t.Errorf("desired creds = %d", len(creds))
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND hwid_hash IS NULL`, f.user); n != 1 {
		t.Errorf("implicit devices = %d", n)
	}
	if got := f.user_(f.user); got.User.DevicesUsed != 1 || got.Devices[0].AwgProfileId != "" {
		t.Errorf("after the first Mihomo fetch: used %d, devices %+v", got.User.DevicesUsed, got.Devices)
	}
	// The second fetch reuses it: same address, same key, nothing new for the nodes.
	e.resetNotify()
	m2 := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: plugin.FormatMihomo, Name: name}))
	if m2.Lines[0] != m.Lines[0] || e.notify.n.Load() != 0 {
		t.Error("the second Mihomo fetch changed the proxy or notified")
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND revoked_at IS NULL`, f.user); n != 1 {
		t.Errorf("live credentials = %d", n)
	}
	// An explicit device is a second peer: the limit counts both, and the page lists the AWG one with its facts.
	d := f.add(f.user, "phone")
	v = must(e.s.Subscription(e.ctx, token))
	if v.DevicesUsed != 2 || len(v.Devices) != 2 {
		t.Fatalf("devices = %+v", v.Devices)
	}
	var awgDev *SubDevice
	for i := range v.Devices {
		if v.Devices[i].ID == d.Device.Id {
			awgDev = &v.Devices[i]
		}
	}
	if awgDev == nil || awgDev.AWG == nil || awgDev.AWG.ProfileName != "awg31" || awgDev.AWG.Version != "3.1" || awgDev.App != "amnezia" || awgDev.Model != "phone" ||
		awgDev.AWG.Stale || awgDev.AWG.Address == "" || len(awgDev.AWG.MinClients) == 0 {
		t.Errorf("awg device on the page = %+v", awgDev)
	}
}

// tokenOf returns a user's subscription token (the admin link, decrypted).
func (e *env) tokenOf(userID string) string {
	e.t.Helper()
	l := must(e.s.GetSubscriptionLink(e.ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: userID}))).Msg.Url
	return l[strings.LastIndex(l, "/")+1:]
}

func TestAWGLegacyEmptyImplicitDevice(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	// An Amnezia-only user of an older panel has an empty implicit device: it does not count, and Mihomo reuses it.
	e.sql(`INSERT INTO device (id, user_id, platform, model, os_version, first_seen_at, last_seen_at, created_at) VALUES ('dev_legacy', ?, '', '', '', 1, 1, 1)`, f.user)
	if got := f.user_(f.user); got.User.DevicesUsed != 0 || len(got.Devices) != 0 {
		t.Errorf("an empty device counts: %+v", got)
	}
	e.user("eve", f.group, amnOnly(func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 }))
	f.add(f.user, "x")
	m := must(e.s.SubscriptionWith(e.ctx, e.tokenOf(f.user), SubOptions{Format: plugin.FormatMihomo}))
	if len(m.Lines) != 1 {
		t.Errorf("lines = %d", len(m.Lines))
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = 'dev_legacy' AND revoked_at IS NULL`); n != 1 {
		t.Errorf("the legacy device did not get the credential: %d", n)
	}
	// A user of both apps keeps his hysteria2 device exactly as before: one implicit device, hysteria2 only.
	f2 := newFixture(t)
	both := f2.e.user("both", f2.group, nil).User
	got := must(f2.e.s.GetUser(f2.e.ctx, req(&adminv1.GetUserRequest{UserId: both.Id}))).Msg
	if len(got.Devices) != 1 || got.Devices[0].AwgProfileId != "" || len(got.Devices[0].Protocols) != 1 || got.Devices[0].Protocols[0] != "hysteria2" {
		t.Errorf("hysteria2 user devices = %+v", got.Devices)
	}
}

func TestAWGOwnerScoping(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	bob := e.user("bob", f.group, amnOnly()).User
	d := f.add(f.user, "mine")
	// The page acts as the token's user: a device of somebody else is "not found", whatever the call.
	_, _, err := e.s.DeviceConfigs(e.ctx, "user:"+bob.Id, bob.Id, d.Device.Id)
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.RelabelDevice(e.ctx, bob.Id, d.Device.Id, "stolen")
	wantCode(t, err, connect.CodeNotFound)
	wantCode(t, e.s.RevokeOwnDevice(e.ctx, "user:"+bob.Id, bob.Id, d.Device.Id), connect.CodeNotFound)
	if len(f.desired()) != 1 {
		t.Fatal("someone else's call changed the state")
	}
	// The owner may.
	if _, _, err := e.s.DeviceConfigs(e.ctx, "user:"+f.user, f.user, d.Device.Id); err != nil {
		t.Errorf("own configs: %v", err)
	}
	got := must(e.s.RenameDevice(e.ctx, req(&adminv1.RenameDeviceRequest{DeviceId: d.Device.Id, Label: " new name "}))).Msg.Device
	if got.Model != "new name" {
		t.Errorf("label = %q", got.Model)
	}
	_, err = e.s.RenameDevice(e.ctx, req(&adminv1.RenameDeviceRequest{DeviceId: d.Device.Id, Label: " "}))
	wantCode(t, err, connect.CodeInvalidArgument)
	e.resetNotify()
	if err := e.s.RevokeOwnDevice(e.ctx, "user:"+f.user, f.user, d.Device.Id); err != nil {
		t.Fatal(err)
	}
	if len(f.desired()) != 0 || e.notify.n.Load() == 0 {
		t.Error("revoking by the owner did not remove the peer")
	}
	rows := must(e.st.ListAudit(e.ctx, "", 0, 20))
	if !auditHas(rows, "device_revoke") || !auditHas(rows, "device_configs") {
		t.Errorf("audit = %+v", rows)
	}
	for _, r := range rows {
		if r.Action == "device_revoke" && r.Actor != "user:"+f.user {
			t.Errorf("actor = %q", r.Actor)
		}
	}
}

func TestAWGDeleteProfileCascades(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	d := f.add(f.user, "")
	must(e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: f.inbound})))
	must(e.s.DeleteProfile(e.ctx, req(&adminv1.DeleteProfileRequest{ProfileId: f.profile})))
	for _, q := range []string{`SELECT count(*) FROM awg_peer`, `SELECT count(*) FROM device_credential`, `SELECT count(*) FROM device WHERE revoked_at IS NULL`} {
		if n := e.count(q); n != 0 {
			t.Errorf("%s = %d", q, n)
		}
	}
	if got := f.user_(f.user); got.User.DevicesUsed != 0 || len(got.Devices) != 0 {
		t.Errorf("devices after deleting the profile: %+v", got.Devices)
	}
	_ = d
}

func TestAWGUserDeleteFreesEverything(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	f.add(f.user, "")
	must(e.s.DeleteUsers(e.ctx, req(&adminv1.DeleteUsersRequest{UserIds: []string{f.user}})))
	if n := e.count(`SELECT count(*) FROM awg_peer`); n != 0 {
		t.Errorf("peers left: %d", n)
	}
	if len(f.desired()) != 0 {
		t.Error("deleted user still in the desired state")
	}
}

func TestAWGDisabledUserLeavesDesiredState(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	d := f.add(f.user, "")
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{f.user}, Enabled: false})))
	if len(f.desired()) != 0 {
		t.Error("a disabled user's peer is still on the node")
	}
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{f.user}, Enabled: true})))
	if c := f.desired(); len(c) != 1 || c[0].DeviceID != d.Device.Id {
		t.Errorf("after enabling: %+v", c)
	}
	// Switching the Amnezia toggle off takes the peer off the nodes too, and on brings it back.
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: f.user, Apps: &adminv1.AppToggles{Happ: true}})))
	if len(f.desired()) != 0 {
		t.Error("toggle off: peer still on the node")
	}
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: f.user, Apps: &adminv1.AppToggles{Happ: true, Amnezia: true}})))
	if len(f.desired()) != 1 {
		t.Error("toggle on: peer did not come back")
	}
}

func TestAWGService(t *testing.T) {
	e := newEnv(t)
	presets := must(e.s.ListMimicryPresets(e.ctx, req(&adminv1.ListMimicryPresetsRequest{}))).Msg.Presets
	if len(presets) != 11 || presets[0].Id != "quic" || len(presets[0].Versions) != 2 || presets[0].Name == "" {
		t.Errorf("presets = %+v", presets)
	}
	def, err := awgDefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, ver := range []string{"3.1", "2.0"} {
		for _, p := range presets {
			r, err := e.s.GenerateObfuscation(e.ctx, req(&adminv1.GenerateObfuscationRequest{Version: ver, Preset: p.Id, Mtu: 1280}))
			if err != nil {
				t.Fatalf("%s %s: %v", ver, p.Id, err)
			}
			var ob awg.Obfuscation
			if err := json.Unmarshal([]byte(r.Msg.ObfuscationJson), &ob); err != nil {
				t.Fatal(err)
			}
			st := def
			st.Version, st.Obfuscation = ver, ob
			raw, _ := json.Marshal(st)
			if errs := awg.New().Validate(raw); len(errs) > 0 {
				t.Errorf("%s %s: the generated obfuscation does not validate: %+v", ver, p.Id, errs)
			}
			if ver == "3.1" && ob.HeaderProtectionKey == "" || ver == "2.0" && ob.HeaderProtectionKey != "" {
				t.Errorf("%s %s: header protection key %q", ver, p.Id, ob.HeaderProtectionKey)
			}
		}
	}
	for name, m := range map[string]*adminv1.GenerateObfuscationRequest{
		"version": {Version: "4", Preset: "quic"}, "preset": {Version: "3.1", Preset: "nope"}, "mtu low": {Version: "3.1", Preset: "quic", Mtu: 500}, "mtu high": {Version: "3.1", Preset: "quic", Mtu: 9000},
	} {
		if _, err := e.s.GenerateObfuscation(e.ctx, req(m)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Two calls differ: the key and the junk are random.
	a := must(e.s.GenerateObfuscation(e.ctx, req(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "dns"}))).Msg.ObfuscationJson
	b := must(e.s.GenerateObfuscation(e.ctx, req(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "dns"}))).Msg.ObfuscationJson
	if a == b {
		t.Error("two generations are identical")
	}
}

func TestAWGPreviewProfile(t *testing.T) {
	e := newEnv(t)
	r := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg"}))).Msg
	if len(r.Errors) != 0 || !strings.Contains(r.ClientPreview, "[Interface]") || !strings.Contains(r.ClientPreview, "••••") ||
		!strings.Contains(r.ClientLabel, ".conf") || !strings.Contains(r.Summary, "AWG 3.1") {
		t.Errorf("preview = %+v", r)
	}
	// The preview has no real secret in it: neither the header protection key nor a key of a device.
	if n := e.count(`SELECT count(*) FROM device_credential`); n != 0 {
		t.Errorf("a preview stored credentials: %d", n)
	}
	bad := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg", SettingsJson: `{"port":0}`}))).Msg
	if len(bad.Errors) == 0 || bad.ClientPreview != "" {
		t.Errorf("bad preview = %+v", bad)
	}
}

func TestAWGHostileNames(t *testing.T) {
	e := newEnv(t)
	hostile := "de1\r\nEvil = 1 </script> \"x\"; - name: y"
	e.node("nod_bad", hostile, "de1.example.com", "active")
	p := e.awgProfile("p", "")
	e.inbound(p.Id, "nod_bad")
	g := e.group("g", p.Id)
	u := e.user("\U0001F600 u\"ser", g, amnOnly()).User
	r := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u.Id, ProfileId: p.Id, Platform: "ios", Label: "\U0001F4F1 my phone"}))).Msg
	c := r.Configs[0]
	if c.ConfFilename != "mistgate-de1-evil-1-script-x-name-y.conf" {
		t.Errorf("filename = %q", c.ConfFilename)
	}
	for _, r := range c.ConfFilename {
		if !(r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			t.Errorf("unsafe rune %q in %q", r, c.ConfFilename)
		}
	}
	if strings.Contains(c.Conf, "Evil = 1") || strings.Contains(c.Conf, "</script>") {
		t.Errorf("the node name reached the .conf:\n%s", c.Conf)
	}
	m := must(e.s.SubscriptionWith(e.ctx, e.tokenOf(u.Id), SubOptions{Format: plugin.FormatMihomo, Name: func(s SubServer) string { return s.Node }}))
	if len(m.Lines) != 1 || strings.Contains(m.Lines[0], "\n- name: y") {
		t.Errorf("mihomo = %q", m.Lines)
	}
	if r.Device.Model != "\U0001F4F1 my phone" {
		t.Errorf("label = %q", r.Device.Model)
	}
}

func TestAWGMihomoFetchRace(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	hy := e.profile("hy2 443", "")
	e.inbound(hy.Id, "nod_de1")
	g := e.group("both", f.profile, hy.Id)
	u := e.user("both", g, nil).User // both apps: the implicit device already holds hysteria2
	token := e.tokenOf(u.Id)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: plugin.FormatMihomo}); err != nil {
				t.Errorf("fetch: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ?`, u.Id); n != 1 {
		t.Errorf("devices = %d, want the one implicit device", n)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, u.Id); n != 1 {
		t.Errorf("awg credentials = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 1 {
		t.Errorf("peers = %d", n)
	}
	// The URI list still carries hysteria2 only, and the user still counts as one device.
	v := must(e.s.Subscription(e.ctx, token))
	if len(v.Lines) != 1 || !strings.HasPrefix(v.Lines[0], "hysteria2://") || v.DevicesUsed != 1 || !v.AccessHapp || !v.AccessAmnezia {
		t.Errorf("uri view = %+v", v)
	}
}
