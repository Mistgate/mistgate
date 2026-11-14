package access

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A key on a path through an entry node (device_credential.path) belongs to a device that already has its direct key:
// it is no device and no second key. The device lists, the Mihomo proxy and the configs come from the direct key, and
// the secret of the path key is never opened for them (it is garbage here, so opening it would fail the render). The
// path key is a peer of the profile and goes to its nodes with the others.
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
		e.sql(`INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at)
			VALUES (?, ?, ?, 'awg', ?, 'relay:nod_msk1', x'01', '{"public_key":"msk1"}', 0, ?)`, credID, deviceID, f.user, f.profile, e.clock.Unix())
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
