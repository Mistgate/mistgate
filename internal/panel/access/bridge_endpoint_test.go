package access

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
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

// The bridge rides the node's own site on 443, whoever holds the port: TrustTunnel and the decoy of MTProto have one, and the
// Hysteria2 site beside them gave the port up. MTProto with another site (and a Hysteria2 site beside it, which also gave the
// port up) shows no site of the node, so the bridge needs a port of its own; with 443 free it takes it.
func TestEnsureBridgeEndpointRidesTheSiteOfTrustTunnelAndMTProto(t *testing.T) {
	const (
		onSite = "site" // port 0: the bridge is served by the site of 443
		own    = "own"  // a port of its own, neither 0 nor 443
		on443  = "443"  // a listener of its own on 443
	)
	for name, tc := range map[string]struct {
		protocol, settings string
		hy2                bool
		want               string
	}{
		"trusttunnel":                   {"trusttunnel", `{"port":443}`, false, onSite},
		"trusttunnel beside a hy2":      {"trusttunnel", `{"port":443}`, true, onSite},
		"mtproto decoy":                 {"mtproto", `{"port":443}`, false, onSite},
		"mtproto decoy beside a hy2":    {"mtproto", `{"port":443}`, true, onSite},
		"mtproto site beside a hy2":     {"mtproto", `{"port":443,"fake_tls":{"kind":"site","domain":"site.example.net"}}`, true, own},
		"mtproto decoy on another port": {"mtproto", `{"port":8443}`, false, on443},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRelayFix(t)
			if tc.hy2 {
				f.inbound(f.profile("entry Hysteria site", "").Id, "nod_fi1")
			}
			p := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: tc.protocol, Name: name, SettingsJson: tc.settings}))).Msg.Profile
			f.inbound(p.Id, "nod_fi1")
			node, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil)
			if err != nil {
				t.Fatal(err)
			}
			got := own
			switch node.BridgePort {
			case 0:
				got = onSite
			case 443:
				got = on443
			}
			if got != tc.want {
				t.Fatalf("bridge port = %d (%s), want %s", node.BridgePort, got, tc.want)
			}
		})
	}
}

// Services that share 443 by SNI answer a stranger with the default one, which is what the bridge rides: a REALITY of another site
// alone on 443 shows someone else's site and the bridge takes a port of its own, but beside a service with the node's own site the
// bridge goes on that site, at port 0, whichever id is lower.
func TestEnsureBridgeEndpointRidesTheDefaultRouteOfASharedPort(t *testing.T) {
	f := newRelayFix(t)
	f.s.reg = builtin.Registry()
	f.sql(`UPDATE node SET agent_caps = agent_caps || ' tcp-share/1' WHERE id = 'nod_fi1'`)
	other := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "other site", SettingsJson: `{"port":443,"reality":{"target":"www.example.org:443"}}`}))).Msg.Profile
	f.inbound(other.Id, "nod_fi1")
	node, err := f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.BridgePort == 0 {
		t.Fatal("the bridge rides a site that is someone else's")
	}
	tt := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "trusttunnel", Name: "tt", SettingsJson: `{"port":443,"h3":false,"sni":"tt.example.com","tls_mode":"acme_domain","egress":"direct"}`}))).Msg.Profile
	f.inbound(tt.Id, "nod_fi1")
	if node, err = f.s.ensureBridgeEndpoint(f.ctx, "nod_fi1", nil); err != nil {
		t.Fatal(err)
	}
	if node.BridgePort != 0 {
		t.Fatalf("beside a service with the node's own site the bridge port = %d, want 0 (that site)", node.BridgePort)
	}
}
