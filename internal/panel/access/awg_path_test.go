package access

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A key on a path through an entry node belongs to a device that already has its direct key: it is no device and no second key.
// Without a relay row the path credential is retained in the link snapshot but no path is rendered from it.
func TestAWGPathKeysLeaveViewsAndRendersToTheDirectKey(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	token := e.tokenOf(f.user)
	key := f.add(f.user, "phone").Device
	link := fetchAs(e, token, phone(), plugin.FormatMihomo)
	if len(link.Lines) != 1 {
		t.Fatalf("Mihomo proxies = %d, want the direct key's one", len(link.Lines))
	}
	var linkID string
	for id := range e.devicesOf(f.user) {
		linkID = id
	}
	configs := func() []*adminv1.DeviceConfig {
		return must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: key.Id}))).Msg.Configs
	}
	views := func() (devices []string, awgKeys int, lines []string) {
		for _, d := range f.user_(f.user).Devices {
			devices = append(devices, d.Id)
		}
		return devices, len(must(e.st.Access().AWGDevices(e.ctx, f.user))), fetchAs(e, token, phone(), plugin.FormatMihomo).Lines
	}
	devicesBefore, keysBefore, linesBefore := views()
	configsBefore, desiredBefore := configs(), len(f.desired())

	for i, deviceID := range []string{key.Id, linkID} {
		credID := "crd_path_" + deviceID
		secret := e.s.vault.Seal([]byte(`{"ip4":"10.66.4.20"}`), credID)
		e.sql(`INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
			VALUES (?, ?, ?, 'awg', ?, 'relay:nod_msk1', ?, '{"public_key":"msk1"}', 0, ?)`, credID, deviceID, f.user, f.profile, secret, e.clock.Unix())
		e.sql(`INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES (?, ?, ?, ?, ?)`,
			credID, f.profile, 20+i, "pub_"+credID, e.clock.Unix())
	}

	devicesAfter, keysAfter, linesAfter := views()
	if !reflect.DeepEqual(devicesBefore, devicesAfter) || keysBefore != keysAfter || !reflect.DeepEqual(linesBefore, linesAfter) {
		t.Fatalf("path keys changed the views: devices %v -> %v, keys %d -> %d, Mihomo %q -> %q", devicesBefore, devicesAfter, keysBefore, keysAfter, linesBefore, linesAfter)
	}
	configsAfter := configs()
	if len(configsBefore) != len(configsAfter) {
		t.Fatalf("configs %d -> %d", len(configsBefore), len(configsAfter))
	}
	for i := range configsBefore {
		if !proto.Equal(configsBefore[i], configsAfter[i]) {
			t.Fatalf("path key changed config %d:\n%v\n%v", i, configsBefore[i], configsAfter[i])
		}
	}
	if got := len(f.desired()); got != desiredBefore+2 {
		t.Fatalf("credentials on the node = %d, want the %d direct ones and the two path keys", got, desiredBefore)
	}

	// A new key replaces the direct one and takes the path key of its device along; the link device's path key stays.
	must(e.s.RotateDeviceKeys(e.ctx, req(&adminv1.RotateDeviceKeysRequest{DeviceId: key.Id})))
	if n := e.count(`SELECT count(*) FROM device_credential WHERE id = ? AND revoked_at IS NULL`, "crd_path_"+key.Id); n != 0 {
		t.Errorf("the rotated device's path key is still live")
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE id = ? AND revoked_at IS NULL`, "crd_path_"+linkID); n != 1 {
		t.Errorf("the other device's path key was revoked")
	}
	if got := len(f.desired()); got != desiredBefore+1 {
		t.Errorf("credentials on the node after the rotation = %d, want %d", got, desiredBefore+1)
	}
}

func TestStaticAWGConfigsIncludeASeparateConfigForEachRelay(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.sql(`UPDATE node SET address = '192.0.2.10', country_code = 'DE' WHERE id = ?`, f.nodeID)
	e.sql(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at)
		VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 'relay-forward/1', 2)`)
	e.sql(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at)
		VALUES ('nod_nl1', 'nl1', '192.0.2.11', 'NL', 'active', 'relay-forward/1', 3)`)
	e.sql(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_msk1', ?, 'nod_msk1', 'udp', 8443, 1, 'active', ?, 1, 1)`, f.inbound,
		`{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`)
	e.sql(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_nl1', ?, 'nod_nl1', 'udp', 9443, 1, 'active', ?, 1, 1)`, f.inbound,
		`{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`)
	e.sql(`UPDATE user SET device_limit = 1 WHERE id = ?`, f.user)

	device := f.add(f.user, "alice")
	if device.User.DevicesUsed != 1 {
		t.Fatalf("path key used another device place: %d", device.User.DevicesUsed)
	}
	if len(device.Configs) != 3 {
		t.Fatalf("device configs = %d, want direct and one config through each relay: %+v", len(device.Configs), device.Configs)
	}
	direct := device.Configs[0]
	if direct.ViaNodeId != "" || direct.ViaCountry != "" {
		t.Fatalf("direct config has path metadata: %+v", direct)
	}
	if !strings.Contains(direct.Conf, "Endpoint = 192.0.2.10:") {
		t.Fatalf("direct config endpoint = %q", direct.Conf)
	}
	confValue := func(conf, key string) string {
		t.Helper()
		for _, line := range strings.Split(conf, "\n") {
			if name, value, ok := strings.Cut(line, " = "); ok && name == key {
				return value
			}
		}
		return ""
	}
	for _, want := range []struct {
		via, country, address string
		port                  int
	}{
		{via: "nod_msk1", country: "ZZ", address: "198.51.100.20", port: 8443},
		{via: "nod_nl1", country: "NL", address: "192.0.2.11", port: 9443},
	} {
		var path *adminv1.DeviceConfig
		for i := range device.Configs {
			if device.Configs[i].ViaNodeId == want.via {
				path = device.Configs[i]
				break
			}
		}
		if path == nil || path.ViaCountry != want.country || !strings.Contains(path.Conf, fmt.Sprintf("Endpoint = %s:%d", want.address, want.port)) ||
			path.ConfFilename == direct.ConfFilename || !strings.HasSuffix(path.ConfFilename, "-via-"+strings.ToLower(want.country)+".conf") || strings.ContainsAny(path.ConfFilename, "/\\") {
			t.Fatalf("relay config through %s: %+v", want.via, path)
		}
		pathAddress := strings.Split(confValue(path.Conf, "Address"), ", ")[0]
		if pathAddress == "" || pathAddress == strings.Split(confValue(direct.Conf, "Address"), ", ")[0] ||
			confValue(path.Conf, "PrivateKey") == "" || confValue(path.Conf, "PrivateKey") == confValue(direct.Conf, "PrivateKey") {
			t.Fatalf("path config through %s did not use its own peer: direct address/key=%q/%q, path=%q/%q", want.via,
				confValue(direct.Conf, "Address"), confValue(direct.Conf, "PrivateKey"), confValue(path.Conf, "Address"), confValue(path.Conf, "PrivateKey"))
		}
		decoded, err := vpnkey.Decode(path.VpnKey)
		if err != nil {
			t.Fatal(err)
		}
		var keyDoc struct {
			Containers []struct {
				AWG struct {
					LastConfig string `json:"last_config"`
				} `json:"awg"`
			} `json:"containers"`
		}
		if err := json.Unmarshal(decoded, &keyDoc); err != nil || len(keyDoc.Containers) != 1 {
			t.Fatalf("relay vpn:// document through %s: %v, %s", want.via, err, decoded)
		}
		var last struct {
			HostName   string `json:"hostName"`
			Port       int    `json:"port"`
			ClientIP   string `json:"client_ip"`
			ClientPriv string `json:"client_priv_key"`
		}
		if err := json.Unmarshal([]byte(keyDoc.Containers[0].AWG.LastConfig), &last); err != nil || last.HostName != want.address ||
			last.Port != want.port || last.ClientIP != strings.TrimSuffix(pathAddress, "/32") || last.ClientPriv != confValue(path.Conf, "PrivateKey") {
			t.Fatalf("path vpn:// config through %s = %+v, %v; path address=%q", want.via, last, err, pathAddress)
		}
		if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND path = ? AND revoked_at IS NULL`, device.Device.Id, "relay:"+want.via); n != 1 {
			t.Errorf("live path keys through %s = %d, want one", want.via, n)
		}
	}
}
