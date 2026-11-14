package access

import (
	"bytes"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The AmneziaWG key of a path through an entry node (device_credential.path = 'relay:<entry>'): a person gets one key for each
// way to a server, issued when the twin is first given to them. The direct server is always rendered with the direct key and a
// twin only with the key of its own entry; one key on two paths breaks the tunnel (the lab of the relay plan).

var awgPrivateKeyLine = regexp.MustCompile(`private-key: "([^"]+)"`)
var awgTunnelAddressLine = regexp.MustCompile(`(?m)^\s*ip: "?([^"\r\n]+)"?`)

func awgPrivateKey(t *testing.T, uri string) string {
	t.Helper()
	m := awgPrivateKeyLine.FindStringSubmatch(uri)
	if m == nil {
		t.Fatalf("no private key in the AmneziaWG proxy:\n%s", uri)
	}
	return m[1]
}

func awgTunnelAddress(t *testing.T, uri string) string {
	t.Helper()
	m := awgTunnelAddressLine.FindStringSubmatch(uri)
	if m == nil {
		t.Fatalf("no tunnel address in the AmneziaWG proxy:\n%s", uri)
	}
	return m[1]
}

// awgRelayFix is the exit de1 with an AmneziaWG server, and the entries fi1 (FI) and nl1 (NL) that relay it.
func awgRelayFix(t *testing.T) (f *relayFix, inbound string, group string) {
	t.Helper()
	f = newTwinFix(t)
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_nl1'`)
	p := f.awgProfile("awg31", "")
	inbound = f.inbound(p.Id, "nod_de1").Id
	for _, via := range []struct {
		node string
		port uint32
	}{{"nod_fi1", 8443}, {"nod_nl1", 9443}} {
		f.switchOn(must(f.create(inbound, via.node, via.port)).Id)
	}
	return f, inbound, f.group("g", p.Id)
}

func (f *relayFix) mihomoAWG(token string) SubView {
	f.t.Helper()
	return must(f.s.SubscriptionWith(f.ctx, token, SubOptions{Format: plugin.FormatMihomo, NoPageData: true}))
}

func TestAWGRelayServersEachGetTheirOwnKey(t *testing.T) {
	f, _, group := awgRelayFix(t)
	alice := f.user("alice", group, nil).User
	token := f.tokenOf(alice.Id)

	v := f.mihomoAWG(token)
	if len(v.Servers) != 3 {
		t.Fatalf("servers = %d, want the direct one and a twin through each entry", len(v.Servers))
	}
	// The direct server comes first; the two relays were made in the same second, so their order follows their random ids.
	wantPaths := []string{"direct", "relay:nod_fi1", "relay:nod_nl1"}
	want := map[string][2]string{"direct": {"de1.example.com", ""}, "relay:nod_fi1": {"fi1.example.com", "port: 8443"}, "relay:nod_nl1": {"nl1.example.com", "port: 9443"}}
	keys := map[string]bool{}
	var direct string
	for i, srv := range v.Servers {
		w, ok := want[srv.PathKey]
		if !ok || (i == 0) != (srv.PathKey == "direct") || !strings.Contains(srv.URI, `server: "`+w[0]+`"`) || !strings.Contains(srv.URI, w[1]) {
			t.Errorf("server %d = %q, want %q:\n%s", i, srv.PathKey, w[0], srv.URI)
		}
		delete(want, srv.PathKey)
		key := awgPrivateKey(t, srv.URI)
		if keys[key] {
			t.Errorf("server %d (%s) shares its private key with another path", i, srv.PathKey)
		}
		keys[key] = true
		if i == 0 {
			direct = key
		}
	}

	// The keys are the ones stored for each path: the direct server carries the direct key, never a path key.
	for _, path := range wantPaths[1:] {
		if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND path = ? AND revoked_at IS NULL`, alice.Id, path); n != 1 {
			t.Errorf("live keys of %s = %d, want one", path, n)
		}
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND path = '' AND revoked_at IS NULL`, alice.Id); n != 1 {
		t.Errorf("live direct keys = %d, want one", n)
	}
	if n := f.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0 AND credential_id IN (SELECT id FROM device_credential WHERE user_id = ? AND revoked_at IS NULL)`, alice.Id); n != 3 {
		t.Errorf("live peers = %d, want three", n)
	}

	// A second fetch changes nothing: the same keys, no new credential.
	again := f.mihomoAWG(token)
	if len(again.Servers) != 3 || awgPrivateKey(t, again.Servers[0].URI) != direct {
		t.Fatalf("a second fetch changed the servers: %d, direct key kept %v", len(again.Servers), awgPrivateKey(t, again.Servers[0].URI) == direct)
	}
	for i := range again.Servers {
		if again.Servers[i].URI != v.Servers[i].URI {
			t.Errorf("server %d changed between two fetches", i)
		}
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, alice.Id); n != 3 {
		t.Errorf("credentials after the second fetch = %d, want three", n)
	}
}

func TestAWGDirectIPv6AndRelayPathsUseDistinctPeersOnOneDevice(t *testing.T) {
	f, _, group := awgRelayFix(t)
	f.entry("nod_de1", entryAddr)
	f.sql(`UPDATE relay SET enabled = 0 WHERE via_node_id = 'nod_nl1'`)
	alice := f.user("alice", group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 }).User

	v := f.mihomoAWG(f.tokenOf(alice.Id))
	wantPaths := []string{"direct", "ipv6", "relay:nod_fi1"}
	wantHosts := []string{"de1.example.com", entryAddr, "fi1.example.com"}
	if len(v.Servers) != len(wantPaths) {
		t.Fatalf("servers = %+v, want direct, IPv6 and relay paths", v.Servers)
	}
	keys, addresses := map[string]bool{}, map[string]bool{}
	for i, server := range v.Servers {
		if server.PathKey != wantPaths[i] || !strings.Contains(server.URI, `server: "`+wantHosts[i]+`"`) {
			t.Errorf("server %d = %+v; want path %q at %q", i, server, wantPaths[i], wantHosts[i])
		}
		key, address := awgPrivateKey(t, server.URI), awgTunnelAddress(t, server.URI)
		if keys[key] {
			t.Errorf("server %d reuses another path's private key", i)
		}
		if addresses[address] {
			t.Errorf("server %d reuses another path's tunnel address %q", i, address)
		}
		keys[key], addresses[address] = true, true
	}
	if got := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, alice.Id); got != 3 {
		t.Errorf("AWG credentials = %d, want three on the device", got)
	}
	if got := f.count(`SELECT count(*) FROM device WHERE user_id = ? AND kind = 'link' AND revoked_at IS NULL`, alice.Id); got != 1 {
		t.Errorf("live link devices = %d, want one device-limit slot", got)
	}
}

// A person who has the "· IPv6" twin today with the direct key keeps the twin after the update, now with a key of its own on
// the next fetch; turning the entry off hides the twin at once and the sweep takes the key, and it comes back with a new key.
func TestAWGIPv6TwinKeyFollowsTheEntry(t *testing.T) {
	f, _, group := awgRelayFix(t)
	f.sql(`UPDATE relay SET enabled = 0`)
	alice := f.user("alice", group, nil).User
	token := f.tokenOf(alice.Id)
	ipv6Keys := func(live bool) int {
		q := `SELECT count(*) FROM device_credential WHERE user_id = ? AND path = 'ipv6'`
		if live {
			q += ` AND revoked_at IS NULL`
		}
		return f.count(q, alice.Id)
	}

	v := f.mihomoAWG(token)
	if len(v.Servers) != 1 || ipv6Keys(false) != 0 {
		t.Fatalf("entry off: servers %+v, IPv6 keys %d; want the direct server and no key", v.Servers, ipv6Keys(false))
	}
	direct := awgPrivateKey(t, v.Servers[0].URI)

	f.entry("nod_de1", entryAddr)
	v = f.mihomoAWG(token)
	if len(v.Servers) != 2 || v.Servers[1].PathKey != "ipv6" || awgPrivateKey(t, v.Servers[0].URI) != direct {
		t.Fatalf("entry on: servers %+v; want the same direct key and an IPv6 twin", v.Servers)
	}
	first := awgPrivateKey(t, v.Servers[1].URI)
	if first == direct || ipv6Keys(true) != 1 {
		t.Fatalf("IPv6 key equals the direct key (%v) or %d live keys", first == direct, ipv6Keys(true))
	}

	f.entry("nod_de1", "")
	if v = f.mihomoAWG(token); len(v.Servers) != 1 || v.Servers[0].PathKey != "direct" {
		t.Fatalf("entry off again: servers %+v; want the direct one", v.Servers)
	}
	if ipv6Keys(true) != 1 {
		t.Fatal("the key must wait for the sweep, not vanish with the twin")
	}
	if n, err := f.st.RevokeOrphanPathCreds(f.ctx, time.Now()); err != nil || n != 1 || ipv6Keys(true) != 0 {
		t.Fatalf("sweep with the entry off = %d, %v; live IPv6 keys %d", n, err, ipv6Keys(true))
	}
	f.entry("nod_de1", entryAddr)
	v = f.mihomoAWG(token)
	if len(v.Servers) != 2 || awgPrivateKey(t, v.Servers[1].URI) == first || awgPrivateKey(t, v.Servers[1].URI) == direct {
		t.Fatalf("entry back on: servers %+v; want a twin with a fresh key", v.Servers)
	}
}

// The profile network is full: the direct server stays, there is no IPv6 twin and the log says why.
func TestAWGIPv6TwinNetworkFullKeepsTheDirectServer(t *testing.T) {
	f := newTwinFix(t)
	f.entry("nod_de1", entryAddr)
	p := f.awgProfile("tiny", `{"subnet4":"10.77.0.0/24","subnet6":""}`)
	f.inbound(p.Id, "nod_de1")
	g := f.group("g", p.Id)
	var fillers []string
	for i := range 3 {
		fillers = append(fillers, f.user(fmt.Sprintf("filler%d", i), g, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 100 }).User.Id)
	}
	alice := f.user("alice", g, nil).User
	const maxIdx = 254 // the peers 2..254 of a /24; one address is left for the direct key
	for i := range maxIdx - 2 {
		if _, err := f.st.Access().AddAWGDevice(f.ctx, store.AWGDeviceAdd{Device: store.AccessDevice{ID: fmt.Sprintf("dev_fill%d", i), UserID: fillers[i/90]},
			ProfileID: p.Id, MaxIdx: maxIdx, Limit: 100}, f.clock, func(idx int) (store.AccessCred, string, error) {
			return store.AccessCred{ID: fmt.Sprintf("crd_fill%d", idx), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, fmt.Sprintf("pub%d", idx), nil
		}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	var logged bytes.Buffer
	f.s.log = slog.New(slog.NewTextHandler(&logged, nil))

	v := f.mihomoAWG(f.tokenOf(alice.Id))
	if len(v.Servers) != 1 || v.Servers[0].PathKey != "direct" || v.Servers[0].Mark != "" {
		t.Fatalf("servers = %+v, want only the direct one", v.Servers)
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND path = '' AND revoked_at IS NULL`, alice.Id); n != 1 {
		t.Errorf("direct keys = %d, want one", n)
	}
	if !strings.Contains(logged.String(), "awg_path_network_full") {
		t.Errorf("no coded log line for the full network: %s", logged.String())
	}
}

// A twin that is not given to a person is not issued to them: no node of theirs at the entry, or a relay that is switched off.
func TestAWGRelayKeysOnlyWhereTheTwinIsGiven(t *testing.T) {
	f, _, group := awgRelayFix(t)
	bob := f.user("bob", group, func(m *adminv1.CreateUserRequest) {
		m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_de1", "nod_fi1"}}
	}).User
	if v := f.mihomoAWG(f.tokenOf(bob.Id)); len(v.Servers) != 2 || v.Servers[1].PathKey != "relay:nod_fi1" {
		t.Fatalf("bob (de1 and fi1 only) servers = %+v", v.Servers)
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND path = 'relay:nod_nl1'`, bob.Id); n != 0 {
		t.Errorf("a key through an entry that is not his = %d, want none", n)
	}

	f.sql(`UPDATE relay SET enabled = 0 WHERE via_node_id = 'nod_fi1'`)
	carol := f.user("carol", group, nil).User
	if v := f.mihomoAWG(f.tokenOf(carol.Id)); len(v.Servers) != 2 || v.Servers[1].PathKey != "relay:nod_nl1" {
		t.Fatalf("carol, relay through fi1 off: servers = %+v", v.Servers)
	}
	if n := f.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND path = 'relay:nod_fi1'`, carol.Id); n != 0 {
		t.Errorf("a key through a relay that is off = %d, want none", n)
	}
}

// The static key of AmneziaVPN: a config through each entry with its own key and a name that does not tell the panel's node
// name; the device holds one place; a rotation and a removal take the path keys with the direct one.
func TestStaticAWGPathKeysFollowRotationAndRemoval(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.sql(`UPDATE node SET country_code = 'DE' WHERE id = ?`, f.nodeID)
	for _, n := range []struct{ id, name, addr, cc string }{{"nod_fi1", "fi1", "198.51.100.21", "FI"}, {"nod_nl1", "nl1", "198.51.100.22", "NL"}} {
		e.sql(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at) VALUES (?, ?, ?, ?, 'active', 'relay-forward/1', 2)`, n.id, n.name, n.addr, n.cc)
		e.sql(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
			VALUES ('rly_' || ?, ?, ?, 'udp', 8443, 1, 'active', ?, 1, 1)`, n.name, f.inbound, n.id, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`)
	}
	privateKey := func(conf string) string {
		for _, line := range strings.Split(conf, "\n") {
			if k, v, ok := strings.Cut(line, " = "); ok && k == "PrivateKey" {
				return v
			}
		}
		return ""
	}
	keysOf := func(cfgs []DeviceConfig) map[string]string { // entry node id ("" = direct) -> private key
		out := map[string]string{}
		for _, c := range cfgs {
			out[c.ViaNodeID] = privateKey(c.Conf)
		}
		return out
	}

	dev, configs, err := e.s.AddAWGDevice(e.ctx, "", f.user, f.profile, "ios", "alice-phone")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 3 {
		t.Fatalf("configs = %d, want the direct one and one through each entry", len(configs))
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND kind = 'key' AND revoked_at IS NULL`, f.user); n != 1 {
		t.Errorf("devices = %d, want one: the path keys are no devices", n)
	}
	before := keysOf(configs)
	for via, key := range before {
		for other, k := range before {
			if via != other && key == k {
				t.Fatalf("paths %q and %q share a private key", via, other)
			}
		}
	}
	files := map[string]bool{}
	for _, c := range configs {
		if files[c.ConfFilename] {
			t.Errorf("file name %q repeats", c.ConfFilename)
		}
		files[c.ConfFilename] = true
		if c.ViaNodeID == "" {
			continue
		}
		if strings.Contains(c.Server+c.ConfFilename, "fi1") || strings.Contains(c.Server+c.ConfFilename, "nl1") {
			t.Errorf("a config through %s tells the panel's node name: server %q, file %q", c.ViaNodeID, c.Server, c.ConfFilename)
		}
		if !strings.Contains(c.Server, c.ViaCountry) || c.ViaCountry == "" {
			t.Errorf("server %q of the config through %s does not name the entry's country %q", c.Server, c.ViaNodeID, c.ViaCountry)
		}
	}

	// A second look at the configs: the same keys.
	_, cfgs, err := e.s.DeviceConfigs(e.ctx, "", "", dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(cfgs); len(got) != 3 || got[""] != before[""] || got["nod_fi1"] != before["nod_fi1"] || got["nod_nl1"] != before["nod_nl1"] {
		t.Errorf("a second look changed the keys")
	}

	_, rotated, err := e.s.RotateDevice(e.ctx, "", "", dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := keysOf(rotated)
	if len(after) != 3 {
		t.Fatalf("configs after the rotation = %d, want three", len(after))
	}
	for via := range before {
		if after[via] == "" || after[via] == before[via] {
			t.Errorf("path %q kept its private key through a rotation", via)
		}
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND path <> '' AND revoked_at IS NULL`, dev.ID); n != 2 {
		t.Errorf("live path keys after the rotation = %d, want two new ones", n)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND path <> '' AND revoked_at IS NOT NULL`, dev.ID); n != 2 {
		t.Errorf("revoked path keys after the rotation = %d, want the two old ones", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0 AND credential_id IN (SELECT id FROM device_credential WHERE device_id = ?)`, dev.ID); n != 3 {
		t.Errorf("live peers of the device after the rotation = %d, want three", n)
	}

	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: dev.ID})))
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, dev.ID); n != 0 {
		t.Errorf("live keys of a removed device = %d, want none", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0 AND credential_id IN (SELECT id FROM device_credential WHERE device_id = ?)`, dev.ID); n != 0 {
		t.Errorf("live peers of a removed device = %d, want none", n)
	}
}

// The port of a relay of an AmneziaWG server: the natural port of its disguise when the entry has it free, else the exit's own.
func TestCreateRelayAWGPortSkipsATakenNaturalPort(t *testing.T) {
	f := newRelayFix(t)
	profile := f.awgProfile("awg-files", `{"port":1194,"obfuscation":{"preset":"quic"}}`)
	srv := f.inbound(profile.Id, "nod_de1")
	f.inbound(f.base.Id, "nod_fi1") // hysteria2 on the entry holds UDP 443
	r := must(f.create(srv.Id, "nod_fi1", 0))
	if r.Port != 1194 || r.ExitPort != 1194 {
		t.Fatalf("relay port = %d (exit %d), want the exit's 1194: 443 is taken on the entry", r.Port, r.ExitPort)
	}
}
