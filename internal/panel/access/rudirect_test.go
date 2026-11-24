package access

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
)

// A "Russian services direct" key is the device's property: its .conf has the long AllowedIPs and no IPv6 address, a rotation keeps
// it, an ordinary key next to it is as before; the key goes stale when the list it holds is not the panel's, the ordinary one never.
func TestRUDirectKey(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	add := func(route, label string) *adminv1.CreateAwgDeviceResponse {
		t.Helper()
		return must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Label: label, Route: route}))).Msg
	}
	direct, plain := add(awg.RouteRUDirect, "direct"), add("", "plain")
	if direct.Device.AwgRoute != awg.RouteRUDirect || plain.Device.AwgRoute != "" {
		t.Fatalf("routes: %q %q", direct.Device.AwgRoute, plain.Device.AwgRoute)
	}
	isDirect := func(conf string) bool {
		return len(strings.Split(confValue(conf, "AllowedIPs"), ", ")) == len(awg.RUDirectAllowedIPs()) && !strings.Contains(confValue(conf, "Address"), ":")
	}
	for _, c := range direct.Configs {
		if !isDirect(c.Conf) || !strings.HasPrefix(c.VpnKey, "vpn://") {
			t.Errorf("direct key of %s: AllowedIPs %d entries, Address %q", c.NodeId, len(strings.Split(confValue(c.Conf, "AllowedIPs"), ", ")), confValue(c.Conf, "Address"))
		}
	}
	for _, c := range plain.Configs {
		if confValue(c.Conf, "AllowedIPs") != "0.0.0.0/0, ::/0" {
			t.Errorf("plain key changed: %q", confValue(c.Conf, "AllowedIPs"))
		}
	}
	if _, err := e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: uid, ProfileId: m.profile, Platform: "ios", Route: "everything"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown route: %v", err)
	}

	routeStale := func() map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, d := range must(e.s.Subscription(e.ctx, token)).Devices {
			if d.AWG != nil {
				out[d.ID] = d.AWG.RouteStale
			}
		}
		return out
	}
	if got := routeStale(); got[direct.Device.Id] || got[plain.Device.Id] {
		t.Fatalf("fresh keys are stale: %v", got)
	}
	// the panel's list moves on: the key holds an older version
	dev := must(e.st.Access().AWGDevice(e.ctx, direct.Device.Id))
	old := map[string]string{ruSigKey: "2020-01-01"}
	for k, v := range dev.DNSSig {
		if k != ruSigKey {
			old[k] = v
		}
	}
	if err := e.st.Access().RecordDeviceConfig(e.ctx, dev.CredID, nil, old); err != nil {
		t.Fatal(err)
	}
	if got := routeStale(); !got[direct.Device.Id] || got[plain.Device.Id] {
		t.Fatalf("the old list: stale %v, want only the direct key", got)
	}
	// the person fetches it again: the new list, not stale; a rotation keeps the route
	_, cfgs, err := e.s.DeviceConfigs(e.ctx, "user:"+uid, uid, direct.Device.Id)
	if err != nil || !isDirect(cfgs[0].Conf) {
		t.Fatalf("fetch: %v", err)
	}
	if got := routeStale(); got[direct.Device.Id] {
		t.Fatalf("after the fetch: %v", got)
	}
	_, cfgs, err = e.s.RotateDevice(e.ctx, "user:"+uid, uid, direct.Device.Id)
	if err != nil || !isDirect(cfgs[0].Conf) {
		t.Fatalf("rotate: %v", err)
	}
	if got := routeStale(); got[direct.Device.Id] {
		t.Fatalf("after the rotation: %v", got)
	}
}
