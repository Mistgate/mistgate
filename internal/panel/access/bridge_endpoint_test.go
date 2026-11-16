package access

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestEnsureBridgeEndpointUsesAnEntryTLSsite(t *testing.T) {
	f := newRelayFix(t)
	profile := f.profile("entry site", "")
	f.inbound(profile.Id, "nod_fi1")
	f.resetNotify()

	node, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.BridgePort != 0 || !strings.HasPrefix(node.BridgePath, "/.well-known/mistgate/bridge/") || len(node.BridgePath) > 240 || strings.Contains(node.BridgePath, "..") {
		t.Fatalf("bridge endpoint = port %d path %q", node.BridgePort, node.BridgePath)
	}
	if f.notify.n.Load() != 1 {
		t.Fatalf("new endpoint StateChanged calls = %d, want 1", f.notify.n.Load())
	}
	if _, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil); err != nil {
		t.Fatal(err)
	}
	if f.notify.n.Load() != 1 {
		t.Fatalf("unchanged endpoint StateChanged calls = %d, want 1 total", f.notify.n.Load())
	}
}

func TestEnsureBridgeEndpointDoesNotUseAHiddenHysteriaSite(t *testing.T) {
	f := newRelayFix(t)
	hy := f.profile("entry Hysteria site", "")
	f.inbound(hy.Id, "nod_fi1")
	vless := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "external reality",
		SettingsJson: `{"port":443,"transport":"tcp","security":"reality","reality":{"target_kind":"site","target":"site.example.net:443","server_names":["site.example.net"],"short_ids":["aabbccdd"],"fingerprint":"chrome"},"xhttp":{"mode":"auto","x_padding_bytes":"100-1000"},"egress":"direct"}`}))).Msg.Profile
	f.inbound(vless.Id, "nod_fi1")

	node, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.BridgePort == 0 || node.BridgePort == 443 {
		t.Fatalf("bridge used an HTTPS site that VLESS takes over: port %d", node.BridgePort)
	}
}

func TestEnsureBridgeEndpointFindsASeparatePortAroundOtherListeners(t *testing.T) {
	f := newRelayFix(t)
	profile := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "external reality",
		SettingsJson: `{"port":443,"transport":"tcp","security":"reality","reality":{"target_kind":"site","target":"site.example.net:443","server_names":["site.example.net"],"short_ids":["aabbccdd"],"fingerprint":"chrome"},"xhttp":{"mode":"auto","x_padding_bytes":"100-1000"},"egress":"direct"}`}))).Msg.Profile
	f.inbound(profile.Id, "nod_fi1")
	if err := f.st.CreateRelay(f.ctx, store.Relay{ID: "rly_taken", InboundID: f.server.Id, ViaNodeID: "nod_fi1", Network: "tcp", Port: 4443}, f.clock); err != nil {
		t.Fatal(err)
	}
	planned := plugin.Listen{Network: "tcp", Port: 8443}
	node, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", &planned)
	if err != nil {
		t.Fatal(err)
	}
	if node.BridgePort == 0 || node.BridgePort == 443 || node.BridgePort == 8443 || node.BridgePort == 4443 {
		t.Fatalf("separate bridge port %d overlaps the REALITY site, planned relay, or existing relay", node.BridgePort)
	}
	others, _, err := f.s.nodeInbounds(f.ctx, "nod_fi1", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range others {
		if other.protocol == "bridge" {
			continue
		}
		if listenOverlap(plugin.Listen{Network: "tcp", Port: node.BridgePort}, other.l) {
			t.Fatalf("separate bridge port %d overlaps %s at %+v", node.BridgePort, other.profile, other.l)
		}
	}
}

func TestMovingBridgeEndpointInvalidatesReverseReach(t *testing.T) {
	f := newRelayFix(t)
	profile := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "external reality",
		SettingsJson: `{"port":443,"transport":"tcp","security":"reality","reality":{"target_kind":"site","target":"site.example.net:443","server_names":["site.example.net"],"short_ids":["aabbccdd"],"fingerprint":"chrome"},"xhttp":{"mode":"auto","x_padding_bytes":"100-1000"},"egress":"direct"}`}))).Msg.Profile
	f.inbound(profile.Id, "nod_fi1")
	if err := f.st.CreateRelay(f.ctx, store.Relay{ID: "rly_a", InboundID: f.server.Id, ViaNodeID: "nod_fi1", Network: "tcp", Port: 7443}, f.clock); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetRelayCheck(f.ctx, "rly_a", store.RelayCheck{At: f.clock.Unix(), Network: "tcp", Port: 7443,
		Leg: store.RelayStage{Status: "fail", At: f.clock.Unix()}, ReverseReach: store.RelayStage{Status: "ok", At: f.clock.Unix()}}); err != nil {
		t.Fatal(err)
	}
	entry, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil)
	if err != nil {
		t.Fatal(err)
	}
	link, err := f.st.RelayLink(f.ctx, "rly_a")
	if err != nil {
		t.Fatal(err)
	}
	check, ok := link.Check()
	if !ok || entry.BridgeEndpointPort() == 443 {
		t.Fatalf("the entry's 443 is the REALITY site, so the bridge needs a port of its own: %d", entry.BridgeEndpointPort())
	}
	if check.ReverseReachOK(f.clock, entry.BridgeEndpointPort()) {
		t.Fatalf("a probe made before the bridge had this endpoint (no port: the first version's 443) passed for %d: %+v", entry.BridgeEndpointPort(), check.ReverseReach)
	}
	check.ReverseReach.Port = entry.BridgeEndpointPort()
	if !check.ReverseReachOK(f.clock, entry.BridgeEndpointPort()) || check.ReverseReachOK(f.clock, 443) {
		t.Fatalf("a probe carries the port it was made for, and passes for that port only: %+v", check.ReverseReach)
	}
}
