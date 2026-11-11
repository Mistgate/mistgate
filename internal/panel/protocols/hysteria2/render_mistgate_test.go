package hysteria2

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The Mistgate format of Hysteria2: the settings the first-party client reads from sync. The goldens are pretty-printed for
// review; the wire form is compact.

func mistgateIn(t *testing.T, iv protocols.InboundView, mut func(*Settings), caps ...string) protocols.RenderInput {
	t.Helper()
	return protocols.RenderInput{Format: plugin.FormatMistgate, Capabilities: caps, Settings: settings(t, mut), Inbound: iv,
		Secret: "tok-DEVICE_secret_0123456789abcdefghijklmn"}
}

func mistgatePretty(t *testing.T, in protocols.RenderInput) (map[string]any, string) {
	t.Helper()
	f, ok := New().Render(in)
	if !ok || f.Format != plugin.FormatMistgate {
		t.Fatal("Render refused")
	}
	var doc map[string]any
	if err := json.Unmarshal(f.Data, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, f.Data)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, f.Data, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	return doc, pretty.String()
}

// Salamander, a hop range, a pinned self-signed node, Gecko (with and without the capability) and a node known by address only.
func TestRenderMistgateGolden(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	hop := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	hop.HopFrom, hop.HopTo = 20000, 30000
	for _, c := range []struct {
		file string
		in   protocols.RenderInput
	}{
		{"mistgate_salamander.json", mistgateIn(t, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)},
		{"mistgate_hop.json", mistgateIn(t, hop, nil)},
		{"mistgate_pinned.json", mistgateIn(t, inbound(plugin.TLSSelfSigned, "de1.example.com", strings.ToUpper(pin)), nil)},
		{"mistgate_gecko_hop.json", mistgateIn(t, hop, func(s *Settings) { s.Obfs.Type = "gecko" }, "v=1", "gecko=1")},
		{"mistgate_ip_only.json", mistgateIn(t, func() protocols.InboundView {
			iv := inbound(plugin.TLSSelfSigned, "", pin)
			iv.Node.Address = "203.0.113.10"
			return iv
		}(), func(s *Settings) { s.Obfs = Obfs{Type: "none"} })},
	} {
		_, pretty := mistgatePretty(t, c.in)
		golden(t, c.file, pretty)
	}
}

func TestRenderMistgateFields(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	hop := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	hop.HopFrom, hop.HopTo = 20000, 30000

	doc, _ := mistgatePretty(t, mistgateIn(t, hop, nil))
	if doc["server"] != "de2.example.com" || doc["port"] != float64(443) || doc["ports"] != "20000-30000" || doc["hop_interval_s"] != float64(hopIntervalSeconds) ||
		doc["auth"] != "tok-DEVICE_secret_0123456789abcdefghijklmn" || doc["tls"] != "ca" || doc["sni"] != "de1.example.com" {
		t.Errorf("hop node: %v", doc)
	}
	if _, ok := doc["pin_sha256"]; ok {
		t.Error("a node with a certificate authority has a pin")
	}
	if obfs := doc["obfs"].(map[string]any); obfs["type"] != "salamander" || obfs["password"] != "gawrgura-pw" {
		t.Errorf("obfs: %v", obfs)
	}
	if alpn := doc["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("alpn: %v", alpn)
	}

	// A node without a hop range has no ports and no interval.
	doc, _ = mistgatePretty(t, mistgateIn(t, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil))
	if _, ok := doc["ports"]; ok {
		t.Error("ports without a hop range")
	}
	if _, ok := doc["hop_interval_s"]; ok {
		t.Error("hop interval without a hop range")
	}

	// A self-signed node: the pin (lower case) and "pin"; the client verifies by the pin alone.
	doc, _ = mistgatePretty(t, mistgateIn(t, inbound(plugin.TLSSelfSigned, "de1.example.com", strings.ToUpper(pin)), nil))
	if doc["tls"] != "pin" || doc["pin_sha256"] != pin {
		t.Errorf("pinned node: %v", doc)
	}
	// ... and one that has not reported its certificate yet has no settings: a client could not verify it.
	if _, ok := New().Render(mistgateIn(t, inbound(plugin.TLSSelfSigned, "de1.example.com", ""), nil)); ok {
		t.Error("a self-signed node without a pin was rendered")
	}

	// A node known by address only has no SNI.
	iv := inbound(plugin.TLSSelfSigned, "203.0.113.10", pin)
	iv.Node.Address = "203.0.113.10"
	doc, _ = mistgatePretty(t, mistgateIn(t, iv, nil))
	if _, ok := doc["sni"]; ok {
		t.Errorf("an address-only node has an SNI: %v", doc["sni"])
	}
	if doc["server"] != "203.0.113.10" {
		t.Errorf("server %v", doc["server"])
	}

	// No obfuscation: no obfs object.
	doc, _ = mistgatePretty(t, mistgateIn(t, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), func(s *Settings) { s.Obfs = Obfs{Type: "none"} }))
	if _, ok := doc["obfs"]; ok {
		t.Error("obfs for none")
	}
}

// Gecko is given to a client that announced gecko=1 and to nobody else.
func TestRenderMistgateGeckoByCapability(t *testing.T) {
	iv := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	gecko := func(s *Settings) { s.Obfs.Type = "gecko" }
	for _, c := range []struct {
		caps []string
		ok   bool
	}{
		{[]string{"v=1", "hysteria2=2", "gecko=1", "awg=2.0"}, true},
		{[]string{"gecko=1,2"}, true},
		{[]string{"v=1", "hysteria2=2"}, false},
		{[]string{"gecko=2"}, false},
		{nil, false},
	} {
		f, ok := New().Render(mistgateIn(t, iv, gecko, c.caps...))
		if ok != c.ok {
			t.Errorf("caps %v: rendered = %v, want %v", c.caps, ok, c.ok)
		}
		if ok {
			var doc struct {
				Obfs struct{ Type string }
			}
			if err := json.Unmarshal(f.Data, &doc); err != nil || doc.Obfs.Type != "gecko" {
				t.Errorf("caps %v: obfs %v (%v)", c.caps, doc.Obfs, err)
			}
		}
	}
	// Salamander needs no capability.
	if _, ok := New().Render(mistgateIn(t, iv, nil)); !ok {
		t.Error("salamander refused without capabilities")
	}
}

// The settings are made of the node's address and the credential, never its name, and a host that is not a plain one is refused.
func TestRenderMistgateNamesNothingAndRefusesAHostileHost(t *testing.T) {
	iv := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	iv.Node.Name = "node-de2-secret-name"
	_, pretty := mistgatePretty(t, mistgateIn(t, iv, nil))
	if strings.Contains(pretty, "node-de2-secret-name") {
		t.Errorf("the node's name is in the settings:\n%s", pretty)
	}
	for _, host := range []string{"de1.example.com/evil", "de1.example.com:443", "a b", "", "x\"y"} {
		iv.Node.Address = host
		if _, ok := New().Render(mistgateIn(t, iv, nil)); ok {
			t.Errorf("host %q was rendered", host)
		}
	}
}

// An IPv6 server is a bare address, with or without the brackets the node's address may carry: the port is a field of its own.
func TestRenderMistgateIPv6ServerHasNoBrackets(t *testing.T) {
	for _, addr := range []string{"2001:db8::5", "[2001:db8::5]"} {
		iv := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
		iv.Node.Address = addr
		doc, _ := mistgatePretty(t, mistgateIn(t, iv, nil))
		if doc["server"] != "2001:db8::5" || doc["port"] != float64(443) {
			t.Errorf("address %q: server %v port %v", addr, doc["server"], doc["port"])
		}
	}
}
