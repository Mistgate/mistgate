package vless

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// siteProfile is a profile that shows another site (www.example.org).
func siteProfile(t *testing.T) json.RawMessage {
	return selfSettings(t, func(s *Settings) {
		s.Transport = "tcp"
		s.Reality.TargetKind, s.Reality.Target, s.Reality.ServerNames = "site", "www.example.org:443", []string{"www.example.org"}
	})
}

func buildOverride(t *testing.T, settings json.RawMessage, node protocols.NodeView, target string) (plugin.InboundSpec, error) {
	t.Helper()
	return New().BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{ID: "prf_1", Settings: settings}, Node: node, RealityTargetOverride: target,
		SpecVersion: 2, Enabled: true, PluginState: bytes32(),
	})
}

func TestBuildInboundWithATargetOfTheNode(t *testing.T) {
	named := protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"}
	byIP := protocols.NodeView{ID: "nod_2", Name: "nl1", Address: "192.0.2.10"}
	for _, c := range []struct {
		name       string
		settings   json.RawMessage
		node       protocols.NodeView
		target     string
		wantTarget string
	}{
		{"site profile", siteProfile(t), named, "shop.example.com:443", "shop.example.com:443"},
		{"self profile", selfSettings(t, nil), named, "shop.example.com:443", "shop.example.com:443"},
		{"self profile on a node with no domain", selfSettings(t, nil), byIP, " Shop.Example.com.:8443 ", "shop.example.com:8443"},
	} {
		spec, err := buildOverride(t, c.settings, c.node, c.target)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var node NodeSettings
		if err := json.Unmarshal(spec.Settings, &node); err != nil {
			t.Fatal(err)
		}
		const host = "shop.example.com"
		if node.Reality.Target != c.wantTarget || len(node.Reality.ServerNames) != 1 || node.Reality.ServerNames[0] != host {
			t.Errorf("%s: reality = %+v", c.name, node.Reality)
		}
		// the name rides in the TLS block with no certificate: mode 0
		if spec.TLS != (plugin.TLS{ServerName: host}) {
			t.Errorf("%s: tls = %+v, want only the server name", c.name, spec.TLS)
		}
		if _, name, ok := plugin.RealityTarget(spec); !ok || name != host {
			t.Errorf("%s: RealityTarget name %q, ok %v", c.name, name, ok)
		}
		if got := plugin.RealityTargetShown(spec); got != c.wantTarget {
			t.Errorf("%s: shown target %q", c.name, got)
		}
	}
}

func TestTargetOfTheNodeRefusals(t *testing.T) {
	named := protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"}
	tlsProfile := selfSettings(t, func(s *Settings) {
		s.Security, s.Transport, s.TLSMode, s.XHTTP.Path = "tls", "xhttp", "acme_domain", "/q8x2kd7w"
		s.Reality = RealitySettings{}
	})
	for _, c := range []struct {
		name, target, reason string
		settings             json.RawMessage
	}{
		{"an IP address", "198.51.100.7:443", "invalid", siteProfile(t)},
		{"an IPv6 address", "[2001:db8::1]:443", "invalid", siteProfile(t)},
		{"a bare label", "shop:443", "invalid", siteProfile(t)},
		{"no port", "shop.example.com", "invalid", siteProfile(t)},
		{"port 0", "shop.example.com:0", "invalid", siteProfile(t)},
		{"this machine", "site.localhost:443", "invalid", siteProfile(t)},
		{"a wildcard", "*.example.com:443", "invalid", siteProfile(t)},
		{"the node itself at its port", "DE1.example.com.:443", "same_node", siteProfile(t)},
		{"the node itself, self profile", "de1.example.com:443", "same_node", selfSettings(t, nil)},
		{"TLS instead of REALITY", "shop.example.com:443", "unsupported", tlsProfile},
	} {
		_, err := buildOverride(t, c.settings, named, c.target)
		var bad *protocols.RealityTargetError
		if !errors.As(err, &bad) || bad.Reason != c.reason {
			t.Errorf("%s: error = %v, want reason %q", c.name, err, c.reason)
		}
	}
	// the node's own name on another port is a site next to the node: allowed
	if _, err := buildOverride(t, siteProfile(t), named, "de1.example.com:8443"); err != nil {
		t.Errorf("the node's name on another port: %v", err)
	}
}

// A node without a target of its own builds what it built before, whatever the profile's kind.
func TestNoTargetOfTheNodeChangesNothing(t *testing.T) {
	named := protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"}
	site, err := buildOverride(t, siteProfile(t), named, "")
	if err != nil {
		t.Fatal(err)
	}
	if site.TLS != (plugin.TLS{}) {
		t.Errorf("site profile without a target of the node: tls = %+v, want none", site.TLS)
	}
	self, err := buildOverride(t, selfSettings(t, nil), named, "")
	if err != nil {
		t.Fatal(err)
	}
	if self.TLS != (plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "de1.example.com"}) {
		t.Errorf("self profile: tls = %+v", self.TLS)
	}
}

// Every format of a subscription tells the client the site this node shows; a node without a target of its own keeps the profile's names.
func TestSubscriptionsCarryTheTargetOfTheNode(t *testing.T) {
	formats := []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatXrayJSON, plugin.FormatMihomo, plugin.FormatSingbox, plugin.FormatMistgate}
	for _, profile := range []struct {
		name string
		raw  json.RawMessage
		own  string // what the profile gives a node without a target of its own
	}{{"site", siteProfile(t), "www.example.org"}, {"self", selfSettings(t, func(s *Settings) { s.Transport = "tcp" }), "de1.example.com"}} {
		for _, format := range formats {
			in := protocols.RenderInput{
				Format: format, Settings: profile.raw, Inbound: vlessInbound(), InboundPublic: testPublicKey(t),
				DeviceID: "dev_123", Secret: "8a3b5c2d-1e4f-4a6b-8c0d-123456789abc", Capabilities: []string{"vless=1"},
			}
			if profile.name == "self" {
				in.Inbound.TLSServerName = "de1.example.com" // the spec's name, as access fills it in
			}
			plain, ok := New().Render(in)
			if !ok {
				t.Fatalf("%s %v without a target of the node: refused", profile.name, format)
			}
			if !strings.Contains(string(plain.Data), profile.own) || strings.Contains(string(plain.Data), "shop.example.com") {
				t.Errorf("%s %v without a target of the node: %s", profile.name, format, plain.Data)
			}
			in.Inbound.TLSServerName = "shop.example.com" // what access fills in from the spec of a node with a target of its own
			own, ok := New().Render(in)
			if !ok {
				t.Fatalf("%s %v with a target of the node: refused", profile.name, format)
			}
			if !strings.Contains(string(own.Data), "shop.example.com") || (profile.name == "site" && strings.Contains(string(own.Data), "www.example.org")) {
				t.Errorf("%s %v with a target of the node: %s", profile.name, format, own.Data)
			}
		}
	}
}
