package access

import (
	"fmt"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
)

// A static AmneziaWG device (a key with no app) gets a config to the node's IPv6 entry beside its direct one, as the link
// devices get the "· IPv6" twin: a key and a tunnel address of its own (one key on two endpoints makes the peer's endpoint jump),
// issued when the configs are first looked at while the entry is on. The direct config and key never change.
func TestStaticAWGDeviceGetsAConfigToTheIPv6Entry(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.sql(`UPDATE user SET device_limit = 1 WHERE id = ?`, f.user)
	device := f.add(f.user, "alice").Device
	configs := func() []*adminv1.DeviceConfig {
		return must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: device.Id}))).Msg.Configs
	}
	pathKeys := func(live bool) int {
		q := `SELECT count(*) FROM device_credential WHERE device_id = ? AND path = 'ipv6'`
		if live {
			q += ` AND revoked_at IS NULL`
		}
		return e.count(q, device.Id)
	}

	// No entry: the direct config alone, no key, nothing new for the node.
	before := configs()
	if len(before) != 1 || before[0].Ipv6 || pathKeys(false) != 0 {
		t.Fatalf("no entry: configs %+v, IPv6 keys %d; want the direct config alone", before, pathKeys(false))
	}
	peersBefore := len(f.desired())

	e.sql(`UPDATE node SET ipv6_entry = ? WHERE id = ?`, entryAddr, f.nodeID)
	e.resetNotify()
	after := configs()
	if len(after) != 2 || after[0].Ipv6 || !after[1].Ipv6 || after[1].ViaNodeId != "" || after[1].ViaCountry != "" {
		t.Fatalf("entry on: configs %+v; want the direct config, then the IPv6 one", after)
	}
	if after[0].Conf != before[0].Conf || after[0].VpnKey != before[0].VpnKey || after[0].ConfFilename != before[0].ConfFilename {
		t.Error("the direct config changed when the IPv6 entry came")
	}
	v6 := after[1]
	if !strings.Contains(v6.Conf, "Endpoint = "+entryBracket+":") || strings.Contains(after[0].Conf, entryAddr) {
		t.Errorf("endpoints: direct %q, IPv6 %q; want the IPv6 one at %s in brackets", after[0].Conf, v6.Conf, entryBracket)
	}
	if !strings.HasSuffix(v6.ConfFilename, "-ipv6.conf") || v6.ConfFilename == after[0].ConfFilename || strings.ContainsAny(v6.ConfFilename, "/\\") {
		t.Errorf("file name %q", v6.ConfFilename)
	}
	if line := func(conf, key string) string {
		for _, l := range strings.Split(conf, "\n") {
			if k, v, ok := strings.Cut(l, " = "); ok && k == key {
				return v
			}
		}
		return ""
	}; line(v6.Conf, "PrivateKey") == "" || line(v6.Conf, "PrivateKey") == line(after[0].Conf, "PrivateKey") || line(v6.Conf, "Address") == line(after[0].Conf, "Address") {
		t.Errorf("the IPv6 config shares the direct key or tunnel address:\n%s\n%s", after[0].Conf, v6.Conf)
	}
	if decoded, err := vpnkey.Decode(v6.VpnKey); err != nil || !strings.Contains(string(decoded), entryAddr) {
		t.Errorf("the vpn:// key of the IPv6 config does not carry the entry: %v, %s", err, decoded)
	}
	if got := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND kind = 'key' AND revoked_at IS NULL`, f.user); got != 1 {
		t.Errorf("devices = %d, want one: the path key is no device", got)
	}
	if pathKeys(true) != 1 || e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0 AND credential_id IN (SELECT id FROM device_credential WHERE device_id = ?)`, device.Id) != 2 {
		t.Errorf("want one live IPv6 key and two live peers on the device")
	}
	if e.notify.n.Load() == 0 || len(f.desired()) != peersBefore+1 {
		t.Errorf("the node must hear about one new peer: notified %d, peers %d -> %d", e.notify.n.Load(), peersBefore, len(f.desired()))
	}

	// A second look changes nothing; the sweep leaves a key whose entry is on.
	again := configs()
	if len(again) != 2 || again[1].Conf != v6.Conf || again[0].Conf != after[0].Conf || pathKeys(false) != 1 {
		t.Errorf("a second look changed the configs or issued another key")
	}
	if n, err := e.st.RevokeOrphanPathCreds(e.ctx, e.clock); err != nil || n != 0 {
		t.Errorf("sweep with the entry on = %d, %v", n, err)
	}

	// The entry off: the IPv6 config goes, its key waits for the sweep, and a new entry gets a fresh key.
	e.sql(`UPDATE node SET ipv6_entry = '' WHERE id = ?`, f.nodeID)
	if got := configs(); len(got) != 1 || got[0].Ipv6 {
		t.Fatalf("entry off: configs %+v; want the direct one", got)
	}
	if n, err := e.st.RevokeOrphanPathCreds(e.ctx, e.clock); err != nil || n != 1 || pathKeys(true) != 0 {
		t.Fatalf("sweep with the entry off = %d, %v; live IPv6 keys %d", n, err, pathKeys(true))
	}
	e.sql(`UPDATE node SET ipv6_entry = ? WHERE id = ?`, entryAddr, f.nodeID)
	if got := configs(); len(got) != 2 || got[1].Conf == v6.Conf {
		t.Fatalf("entry back on: configs %+v; want an IPv6 config with a fresh key", got)
	}

	// A rotation takes the IPv6 key with the direct one; removal takes them all.
	rotated := must(e.s.RotateDeviceKeys(e.ctx, req(&adminv1.RotateDeviceKeysRequest{DeviceId: device.Id}))).Msg.Configs
	if len(rotated) != 2 || !rotated[1].Ipv6 || rotated[1].Conf == v6.Conf || rotated[0].Conf == after[0].Conf {
		t.Fatalf("after a rotation: %+v", rotated)
	}
	must(e.s.RevokeDevice(e.ctx, req(&adminv1.RevokeDeviceRequest{DeviceId: device.Id})))
	if got := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, device.Id); got != 0 {
		t.Errorf("live keys of a removed device = %d, want none", got)
	}
}

// With a relay as well, the order is direct, IPv6, then the relay; each has its own key and its own file.
func TestStaticAWGDeviceListsIPv6BeforeRelays(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.sql(`UPDATE node SET ipv6_entry = ? WHERE id = ?`, entryAddr, f.nodeID)
	e.sql(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at) VALUES ('nod_fi1', 'fi1', '198.51.100.21', 'FI', 'active', 'relay-forward/1', 2)`)
	e.sql(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_fi1', ?, 'nod_fi1', 'udp', 8443, 1, 'active', ?, 1, 1)`, f.inbound, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`)

	_, configs, err := e.s.AddAWGDevice(e.ctx, "", f.user, f.profile, "ios", "alice-phone")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 3 || configs[0].IPv6 || configs[0].ViaNodeID != "" || !configs[1].IPv6 || configs[1].ViaNodeID != "" || configs[2].IPv6 || configs[2].ViaNodeID != "nod_fi1" {
		t.Fatalf("configs = %+v; want direct, IPv6, then the one through fi1", configs)
	}
	if !strings.HasSuffix(configs[1].Server, " · IPv6") || !strings.Contains(configs[2].Server, "FI") {
		t.Errorf("names %q, %q", configs[1].Server, configs[2].Server)
	}
	files := map[string]bool{}
	keys := map[string]bool{}
	for _, c := range configs {
		if files[c.ConfFilename] || keys[c.Conf] {
			t.Errorf("a file name or a config repeats: %q", c.ConfFilename)
		}
		files[c.ConfFilename], keys[c.Conf] = true, true
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, f.user); n != 3 {
		t.Errorf("AWG credentials = %d, want three on one device", n)
	}
}

// The profile network is full: the direct config stays and there is no IPv6 config.
func TestStaticAWGDeviceWithoutAFreeAddressKeepsTheDirectConfig(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.awgProfile("tiny", `{"subnet4":"10.77.0.0/24","subnet6":""}`)
	e.inbound(p.Id, "nod_de1")
	alice := e.user("alice", e.group("default", p.Id), amnOnly()).User
	dev := must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: alice.Id, ProfileId: p.Id, Platform: "ios"}))).Msg
	// The entry comes after the device, and no address is left for its key: take every peer address of the /24 (the device's own stays).
	e.sql(`UPDATE node SET ipv6_entry = ? WHERE id = 'nod_de1'`, entryAddr)
	for i := 2; i <= 300; i++ {
		credID := fmt.Sprintf("crd_fill%d", i)
		e.sql(`INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
			VALUES (?, ?, ?, 'awg', ?, ?, x'01', '{}', 0, 1)`, credID, dev.Device.Id, alice.Id, p.Id, fmt.Sprintf("relay:nod_zz%d", i))
		e.sql(`INSERT OR IGNORE INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES (?, ?, ?, ?, 1)`, credID, p.Id, i, "pub"+credID)
	}
	got := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: dev.Device.Id}))).Msg.Configs
	if len(got) != 1 || got[0].Ipv6 {
		t.Fatalf("%d configs, the first with IPv6 %v; want the direct one alone", len(got), got[0].Ipv6)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND path = 'ipv6'`, dev.Device.Id); n != 0 {
		t.Errorf("IPv6 keys = %d, want none", n)
	}
}
