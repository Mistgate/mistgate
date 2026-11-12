package awg

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The Mistgate format is what the first-party client reads from sync: the same clientConfig as the .conf and the Mihomo proxy,
// as JSON. Its goldens are pretty-printed for review; the wire form is compact.

func mistgateRender(t *testing.T, s Settings, device string, caps ...string) (map[string]any, []byte) {
	t.Helper()
	in := renderInput(t, s, plugin.FormatMistgate)
	in.DeviceID, in.Capabilities = device, caps
	frag, ok := New().Render(in)
	if !ok || frag.Format != plugin.FormatMistgate {
		t.Fatalf("Render(mistgate, %s) refused", s.Version)
	}
	var doc map[string]any
	if err := json.Unmarshal(frag.Data, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, frag.Data)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, frag.Data, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	return doc, pretty.Bytes()
}

func TestMistgateGolden(t *testing.T) {
	v4only := fixture31()
	v4only.Subnet6 = ""
	for _, c := range []struct {
		file string
		s    Settings
	}{
		{"mistgate_awg20.json", fixture20()},
		{"mistgate_awg31.json", fixture31()},
		{"mistgate_awg31_v4only.json", v4only},
		{"mistgate_awg31_perdevice.json", perDevice31("dns")},
	} {
		_, pretty := mistgateRender(t, c.s, "dev_1", "awg=2.0,3.1")
		golden(t, c.file, pretty)
	}
}

// Every field the .conf and the Mihomo proxy of the profile carry is in the settings, under the names the engines take; the
// strings of 3.1 only when the profile has them, the IPv6 address only when the profile has IPv6.
func TestMistgateCarriesWhatTheOtherRendersCarry(t *testing.T) {
	doc, _ := mistgateRender(t, fixture31(), "dev_1", "awg=3.1")
	for _, k := range []string{"version", "endpoint", "public_key", "private_key", "preshared_key", "address_v4", "address_v6", "allowed_ips", "dns", "mtu", "keepalive_s", "params"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	params := doc["params"].(map[string]any)
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4", "i1",
		"header_protection_key", "content_padding_addition", "rekey_after_time", "rekey_timeout", "reject_after_time",
		"keepalive_timeout", "max_handshake_attempts", "random_trailers", "disable_cookies"} {
		if _, ok := params[k]; !ok {
			t.Errorf("params has no %q", k)
		}
	}
	if doc["version"] != "3.1" || doc["keepalive_s"] != float64(25) || doc["address_v4"] == "" || params["h2"] != "2" {
		t.Errorf("3.1: %v", doc)
	}
	if ep := doc["endpoint"].(map[string]any); ep["host"] != "203.0.113.10" || ep["port"] != float64(51842) {
		t.Errorf("endpoint %v", ep)
	}
	if allowed := doc["allowed_ips"].([]any); len(allowed) != 2 || allowed[1] != "::/0" {
		t.Errorf("allowed_ips with IPv6: %v", allowed)
	}

	v20, _ := mistgateRender(t, fixture20(), "dev_1", "awg=2.0")
	if _, ok := v20["address_v6"]; ok {
		t.Error("a profile without IPv6 gave an IPv6 address")
	}
	// Both ranges even so: a dual-stack client must not send IPv6 around the tunnel.
	if allowed := v20["allowed_ips"].([]any); len(allowed) != 2 || allowed[0] != "0.0.0.0/0" || allowed[1] != "::/0" {
		t.Errorf("allowed_ips without IPv6: %v", allowed)
	}
	p20 := v20["params"].(map[string]any)
	for _, k := range []string{"header_protection_key", "content_padding_addition", "rekey_after_time", "random_trailers", "disable_cookies"} {
		if _, ok := p20[k]; ok {
			t.Errorf("a 2.0 profile carries the 3.1 key %q", k)
		}
	}
	if p20["h1"] != "100000-200000" {
		t.Errorf("a range of H is kept as a string: %v", p20["h1"])
	}

	// A 3.1 field the profile leaves empty is absent, as in the conf; a profile without a keepalive has none.
	s := fixture31()
	s.Obfuscation.RekeyTimeout, s.Obfuscation.PersistentKeepalive = "", ""
	doc, _ = mistgateRender(t, s, "dev_1", "awg=3.1")
	if _, ok := doc["params"].(map[string]any)["rekey_timeout"]; ok {
		t.Error("an empty field is present")
	}
	if _, ok := doc["keepalive_s"]; ok {
		t.Error("a profile without a keepalive has one")
	}
}

// Each device has its own I1..I5 when the profile says so: the same profile rendered for two devices differs there and nowhere
// else, and a device renders the same chain every time.
func TestMistgatePerDeviceChains(t *testing.T) {
	s := perDevice31("dns")
	a, _ := mistgateRender(t, s, "dev_A", "awg=3.1")
	b, _ := mistgateRender(t, s, "dev_B", "awg=3.1")
	a2, _ := mistgateRender(t, s, "dev_A", "awg=3.1")
	pa, pb, pa2 := a["params"].(map[string]any), b["params"].(map[string]any), a2["params"].(map[string]any)
	differ := 0
	for _, k := range []string{"i1", "i2", "i3", "i4", "i5"} {
		if pa[k] != pb[k] {
			differ++
		}
		if pa[k] != pa2[k] {
			t.Errorf("%s of one device changed between renders", k)
		}
		delete(pa, k)
		delete(pb, k)
	}
	if differ == 0 {
		t.Error("two devices got the same signature chain")
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if !bytes.Equal(ja, jb) {
		t.Errorf("the devices differ beyond I1..I5:\n%s\n%s", ja, jb)
	}
}

// A client gets the AmneziaWG versions it announced and no others; none announced, none given.
func TestMistgateVersionIsGivenByCapability(t *testing.T) {
	for _, c := range []struct {
		version string
		s       Settings
		caps    []string
		ok      bool
	}{
		{"2.0", fixture20(), []string{"awg=2.0,3.1"}, true},
		{"3.1", fixture31(), []string{"v=1; awg=2.0", "awg=2.0,3.1"}, true},
		{"3.1", fixture31(), []string{"awg=2.0"}, false},
		{"3.1", fixture31(), []string{"awg=3"}, false},
		{"2.0", fixture20(), []string{"hysteria2=2", "gecko=1"}, false},
		{"2.0", fixture20(), nil, false},
	} {
		in := renderInput(t, c.s, plugin.FormatMistgate)
		in.Capabilities = c.caps
		if _, ok := New().Render(in); ok != c.ok {
			t.Errorf("%s with %v: rendered = %v, want %v", c.version, c.caps, ok, c.ok)
		}
	}
}

// The settings hold the device's own secrets, the node's public key and the profile's parameters: no node name, no user name, no
// label.
func TestMistgateNamesNothing(t *testing.T) {
	_, pretty := mistgateRender(t, fixture31(), "dev_1", "awg=3.1")
	for _, name := range []string{"de1", "alice", "laptop", "usr_1", "nod_1", "dev_1"} {
		if strings.Contains(string(pretty), name) {
			t.Errorf("the settings name %q", name)
		}
	}
}

// An IPv6 endpoint is a bare address: the host and the port are fields of their own, so no brackets.
func TestMistgateIPv6EndpointHasNoBrackets(t *testing.T) {
	in := renderInput(t, fixture31(), plugin.FormatMistgate)
	in.Capabilities, in.NodeAddr = []string{"awg=3.1"}, "2001:db8::5"
	frag, ok := New().Render(in)
	if !ok {
		t.Fatal("Render refused")
	}
	var doc struct {
		Endpoint struct {
			Host string
			Port int
		}
	}
	if err := json.Unmarshal(frag.Data, &doc); err != nil || doc.Endpoint.Host != "2001:db8::5" || doc.Endpoint.Port != 51842 {
		t.Errorf("endpoint %+v (%v)", doc.Endpoint, err)
	}
}

// An exit that gives the client no IPv6 (RenderInput.NoIPv6Egress): no format has a tunnel IPv6 address, and everything else of
// them, allowed_ips included (both ranges, so the client's IPv6 cannot go around the tunnel), stays what it was.
func TestWithoutIPv6Egress(t *testing.T) {
	render := func(format plugin.ClientFormat, noV6 bool) string {
		t.Helper()
		in := renderInput(t, fixture31(), format)
		in.DeviceID, in.Capabilities, in.NoIPv6Egress = "dev_1", []string{"awg=3.1"}, noV6
		frag, ok := New().Render(in)
		if !ok {
			t.Fatalf("Render(%s) refused", format)
		}
		return string(frag.Data)
	}
	// The .conf: the Address line loses the /128 and nothing else moves.
	with, without := render(plugin.FormatAWGConf, false), render(plugin.FormatAWGConf, true)
	if !strings.Contains(with, "/128") {
		t.Fatalf("the profile has IPv6 and the .conf carries no IPv6 address by default:\n%s", with)
	}
	if strings.Contains(without, "/128") {
		t.Errorf("an IPv6 address for an exit with no IPv6:\n%s", without)
	}
	if !strings.Contains(without, "AllowedIPs = 0.0.0.0/0, ::/0") {
		t.Errorf("AllowedIPs lost ::/0:\n%s", without)
	}
	if a, b := withoutAddress(with), withoutAddress(without); a != b {
		t.Errorf("something else changed in the .conf:\n%s\n%s", a, b)
	}
	// The vpn:// key holds that .conf (last_config.config) and the client address without a mask.
	keyWith, keyWithout := render(plugin.FormatAmneziaVPN, false), render(plugin.FormatAmneziaVPN, true)
	docWith, err := vpnkey.Decode(keyWith)
	if err != nil {
		t.Fatal(err)
	}
	docWithout, err := vpnkey.Decode(keyWithout)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(docWithout), "/128") || !strings.Contains(string(docWith), "/128") {
		t.Errorf("vpn:// key IPv6 address: with=%v without=%v", strings.Contains(string(docWith), "/128"), strings.Contains(string(docWithout), "/128"))
	}
	if !strings.Contains(string(docWithout), "::/0") {
		t.Error("the vpn:// key lost ::/0")
	}
	// Mihomo: no ipv6 key, allowed-ips as before.
	mWith, mWithout := render(plugin.FormatMihomo, false), render(plugin.FormatMihomo, true)
	if !strings.Contains(mWith, "ipv6:") || strings.Contains(mWithout, "ipv6:") {
		t.Errorf("Mihomo ipv6 key: with=%v without=%v", strings.Contains(mWith, "ipv6:"), strings.Contains(mWithout, "ipv6:"))
	}
	if !strings.Contains(mWithout, "::/0") {
		t.Errorf("Mihomo allowed-ips lost ::/0:\n%s", mWithout)
	}
	if a, b := stripLine(mWith, "ipv6:"), mWithout; a != b {
		t.Errorf("something else changed in the Mihomo proxy:\n%s\n%s", a, b)
	}
	// Mistgate: no address_v6, the rest equal, both ranges.
	var docA, docB map[string]any
	if err := json.Unmarshal([]byte(render(plugin.FormatMistgate, false)), &docA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(render(plugin.FormatMistgate, true)), &docB); err != nil {
		t.Fatal(err)
	}
	if _, ok := docA["address_v6"]; !ok {
		t.Fatal("the profile has IPv6 and the settings carry no address_v6 by default")
	}
	if _, ok := docB["address_v6"]; ok {
		t.Error("address_v6 for an exit with no IPv6")
	}
	delete(docA, "address_v6")
	if a, b := mustJSON(t, docA), mustJSON(t, docB); a != b {
		t.Errorf("something else changed:\n%s\n%s", a, b)
	}
	if allowed, _ := docB["allowed_ips"].([]any); len(allowed) != 2 {
		t.Errorf("allowed_ips = %v", allowed)
	}
}

// withoutAddress is a .conf without its Address line.
func withoutAddress(conf string) string { return stripLine(conf, "Address = ") }

func stripLine(text, prefix string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
