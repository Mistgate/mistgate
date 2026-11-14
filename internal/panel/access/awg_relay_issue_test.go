package access

import (
	"regexp"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The AmneziaWG key of a path through an entry node (device_credential.path = 'relay:<entry>'): a person gets one key for each
// way to a server, issued when the twin is first given to them. The direct server is always rendered with the direct key and a
// twin only with the key of its own entry; one key on two paths breaks the tunnel (the lab of the relay plan).

var awgPrivateKeyLine = regexp.MustCompile(`private-key: "([^"]+)"`)

func awgPrivateKey(t *testing.T, uri string) string {
	t.Helper()
	m := awgPrivateKeyLine.FindStringSubmatch(uri)
	if m == nil {
		t.Fatalf("no private key in the AmneziaWG proxy:\n%s", uri)
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
	wantPaths := []string{"direct", "relay:nod_fi1", "relay:nod_nl1"}
	wantHosts := []string{"de1.example.com", "fi1.example.com", "nl1.example.com"}
	wantPorts := []string{"", "port: 8443", "port: 9443"}
	keys := map[string]bool{}
	var direct string
	for i, srv := range v.Servers {
		if srv.PathKey != wantPaths[i] || !strings.Contains(srv.URI, `server: "`+wantHosts[i]+`"`) || !strings.Contains(srv.URI, wantPorts[i]) {
			t.Errorf("server %d = %q through %q:\n%s", i, srv.PathKey, wantHosts[i], srv.URI)
		}
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
