package awg

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/vpnkey"
	"github.com/mistgate/mistgate/internal/plugin"
)

func render(t *testing.T, s Settings, f plugin.ClientFormat) plugin.Fragment {
	t.Helper()
	frag, ok := New().Render(renderInput(t, s, f))
	if !ok {
		t.Fatalf("Render(%s) refused", f)
	}
	if frag.Format != f || frag.Name != "de1 · AWG "+s.Version {
		t.Errorf("fragment %s %q", frag.Format, frag.Name)
	}
	return frag
}

func TestConfGolden(t *testing.T) {
	golden(t, "awg31.conf", render(t, fixture31(), plugin.FormatAWGConf).Data)
	golden(t, "awg20.conf", render(t, fixture20(), plugin.FormatAWGConf).Data)
}

// A config may only carry keys the client versions of that protocol version understand: an unknown key makes
// the AmneziaWG apps reject the whole file.
func TestConfKeysPerVersion(t *testing.T) {
	v20 := []string{"Address", "DNS", "MTU", "PrivateKey", "Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4", "I1", "I2", "I3", "I4", "I5", "PublicKey", "PresharedKey", "AllowedIPs", "Endpoint", "PersistentKeepalive"}
	v31 := append(slices.Clone(v20), "HeaderProtectionKey", "ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime", "KeepaliveTimeout", "MaxHandshakeAttempts", "RandomTrailers", "DisableCookies")
	for name, c := range map[string]struct {
		s    Settings
		keys []string
	}{"3.1": {fixture31(), v31}, "2.0": {fixture20(), v20}} {
		conf := string(render(t, c.s, plugin.FormatAWGConf).Data)
		for _, l := range strings.Split(conf, "\n") {
			if k, _, ok := strings.Cut(l, " = "); ok && !slices.Contains(c.keys, k) {
				t.Errorf("%s: key %q is not allowed in a %s config", name, k, name)
			}
		}
		if strings.Contains(conf, "#") {
			t.Errorf("%s: a # starts a comment in a .conf", name)
		}
	}
	// RandomTrailers is written only when on; DisableCookies is always the client's on.
	s := fixture31()
	s.Obfuscation.RandomTrailers = false
	conf := string(render(t, s, plugin.FormatAWGConf).Data)
	if strings.Contains(conf, "RandomTrailers") || !strings.Contains(conf, "DisableCookies = on") {
		t.Errorf("trailers off:\n%s", conf)
	}
}

func TestConfDetails(t *testing.T) {
	conf := string(render(t, fixture31(), plugin.FormatAWGConf).Data)
	for _, want := range []string{
		"Address = 10.66.4.5/32, fd66:66:0:1::5/128\n", "DNS = 1.1.1.1, 8.8.8.8\n", "MTU = 1280\n",
		"AllowedIPs = 0.0.0.0/0, ::/0\n", "Endpoint = 203.0.113.10:51842\n", "PersistentKeepalive = 25-35\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in\n%s", want, conf)
		}
	}
	// IPv6 node address: bracketed endpoint; IPv4-only profile: one address
	in := renderInput(t, func() Settings { s := fixture31(); s.Subnet6 = ""; return s }(), plugin.FormatAWGConf)
	in.NodeAddr = "2001:db8::1"
	frag, ok := New().Render(in)
	if !ok || !strings.Contains(string(frag.Data), "Endpoint = [2001:db8::1]:51842\n") || !strings.Contains(string(frag.Data), "Address = 10.66.4.5/32\n") {
		t.Errorf("v6 endpoint / v4-only: %v\n%s", ok, frag.Data)
	}
	// the port of the inbound (an override) wins over the profile's
	in = renderInput(t, fixture31(), plugin.FormatAWGConf)
	in.Inbound.Port = 4443
	frag, _ = New().Render(in)
	if !strings.Contains(string(frag.Data), "Endpoint = 203.0.113.10:4443\n") {
		t.Errorf("port override:\n%s", frag.Data)
	}
}

func TestPickDNS(t *testing.T) {
	cases := []struct {
		in       []string
		want     [2]string
		fallback bool
	}{
		{[]string{"1.1.1.1", "8.8.8.8"}, [2]string{"1.1.1.1", "8.8.8.8"}, false},
		{[]string{"9.9.9.9"}, [2]string{"9.9.9.9", "9.9.9.9"}, false},
		{[]string{"2606:4700::1111", "https://1.1.1.1/dns-query", "77.88.8.8", "77.88.8.1", "8.8.4.4"}, [2]string{"77.88.8.8", "77.88.8.1"}, false},
		{[]string{"2606:4700::1111", "dns.example.com"}, [2]string{"1.1.1.1", "8.8.8.8"}, true},
		{nil, [2]string{"1.1.1.1", "8.8.8.8"}, true},
	}
	for _, c := range cases {
		got, fb := PickDNS(c.in)
		if got != c.want || fb != c.fallback {
			t.Errorf("PickDNS(%v) = %v, %v; want %v, %v", c.in, got, fb, c.want, c.fallback)
		}
	}
	in := renderInput(t, fixture31(), plugin.FormatAWGConf)
	in.DNS = nil
	frag, _ := New().Render(in)
	if !strings.Contains(string(frag.Data), "DNS = 1.1.1.1, 8.8.8.8\n") {
		t.Error("no DNS: the built-in pair is missing")
	}
}

// ---- vpn:// ----

func decodeKey(t *testing.T, frag plugin.Fragment) (doc map[string]any, last map[string]any, js []byte) {
	t.Helper()
	js, err := vpnkey.Decode(string(frag.Data))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(js, &doc); err != nil {
		t.Fatal(err)
	}
	c := doc["containers"].([]any)[0].(map[string]any)["awg"].(map[string]any)
	lc, ok := c["last_config"].(string)
	if !ok {
		t.Fatalf("last_config is %T, it must be a string holding JSON", c["last_config"])
	}
	if err := json.Unmarshal([]byte(lc), &last); err != nil {
		t.Fatal(err)
	}
	return doc, last, js
}

func TestVpnKeyGolden(t *testing.T) {
	for name, s := range map[string]Settings{"31": fixture31(), "20": fixture20()} {
		frag := render(t, s, plugin.FormatAmneziaVPN)
		if !strings.HasPrefix(string(frag.Data), "vpn://") || strings.ContainsAny(string(frag.Data)[6:], "=+/ \n") {
			t.Fatalf("not a vpn:// key: %.30s", frag.Data)
		}
		_, _, js := decodeKey(t, frag)
		golden(t, "awg"+name+".vpn.json", append(js, '\n'))
		// The stored key is input for the decoder test below. Its bytes depend on the zlib implementation of the
		// Go version, so they are never compared, only rewritten on -update.
		if *update {
			if err := os.WriteFile("testdata/awg"+name+".vpn.key", append(frag.Data, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestVpnKeyStoredKeysDecode(t *testing.T) {
	for _, name := range []string{"31", "20"} {
		key, err := os.ReadFile("testdata/awg" + name + ".vpn.key")
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile("testdata/awg" + name + ".vpn.json")
		if err != nil {
			t.Fatal(err)
		}
		got, err := vpnkey.Decode(string(key))
		if err != nil || string(got)+"\n" != string(want) {
			t.Errorf("stored key %s no longer decodes to its JSON: %v", name, err)
		}
	}
}

func TestVpnKeyShape(t *testing.T) {
	doc, last, _ := decodeKey(t, render(t, fixture31(), plugin.FormatAmneziaVPN))
	for _, k := range []string{"userName", "password", "port"} {
		if _, ok := doc[k]; ok {
			t.Errorf("top-level %q makes the client treat the key as an administrator's", k)
		}
	}
	if doc["format_version"] != float64(1) || doc["hostName"] != "203.0.113.10" || doc["defaultContainer"] != "amnezia-awg2" ||
		doc["dns1"] != "1.1.1.1" || doc["dns2"] != "8.8.8.8" || doc["description"] != "de1 · AWG 3.1" {
		t.Errorf("document: %v", doc)
	}
	awg := doc["containers"].([]any)[0].(map[string]any)
	if awg["container"] != "amnezia-awg2" {
		t.Errorf("container %v", awg["container"])
	}
	blk := awg["awg"].(map[string]any)
	if blk["port"] != "51842" || blk["transport_proto"] != "udp" || blk["protocol_version"] != "3.1" {
		t.Errorf("awg block: %v", blk)
	}
	if last["port"] != float64(51842) {
		t.Errorf("last_config port is %T %v, it must be a number", last["port"], last["port"])
	}
	if last["client_ip"] != "10.66.4.5" || last["hostName"] != "203.0.113.10" || last["mtu"] != "1280" || last["clientId"] != last["client_pub_key"] {
		t.Errorf("last_config: %v", last)
	}
	if !slices.Equal(toStrings(last["allowed_ips"]), []string{"0.0.0.0/0", "::/0"}) {
		t.Errorf("allowed_ips %v", last["allowed_ips"])
	}
	for k, v := range last {
		if _, isStr := v.(string); !isStr && k != "port" && k != "allowed_ips" {
			t.Errorf("last_config[%s] is %T: every value except port and allowed_ips is a string", k, v)
		}
	}
	if last["RandomTrailers"] != "on" || last["DisableCookies"] != "on" || last["HeaderProtectionKey"] != key32(0x40) || last["persistent_keep_alive"] != "25-35" {
		t.Errorf("3.1 keys: %v", last)
	}
	// the text of config is the .conf, for display and export
	if last["config"] != string(render(t, fixture31(), plugin.FormatAWGConf).Data) {
		t.Error("config differs from the .conf")
	}
	// the client public key is the one the private key gives (the node gets the same from IssueCredential)
	if pub, err := publicKeyOf(last["client_priv_key"].(string)); err != nil || pub != last["client_pub_key"] {
		t.Errorf("client_pub_key %v, derived %v (%v)", last["client_pub_key"], pub, err)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestVpnKey20HasNoV3Keys(t *testing.T) {
	_, last, _ := decodeKey(t, render(t, fixture20(), plugin.FormatAmneziaVPN))
	for _, k := range []string{"HeaderProtectionKey", "ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime", "KeepaliveTimeout", "MaxHandshakeAttempts", "RandomTrailers", "DisableCookies"} {
		if _, ok := last[k]; ok {
			t.Errorf("2.0 key carries %s: AmneziaVPN would take it for 3.x", k)
		}
	}
	if last["persistent_keep_alive"] != "25" || last["S3"] != "25" || last["H1"] != "100000-200000" {
		t.Errorf("2.0 last_config: %v", last)
	}
	doc, _, _ := decodeKey(t, render(t, fixture20(), plugin.FormatAmneziaVPN))
	if doc["containers"].([]any)[0].(map[string]any)["awg"].(map[string]any)["protocol_version"] != "2" {
		t.Error("2.0 protocol_version must be \"2\"")
	}
}

// Both generators write the same keys as a real client does: testdata/qt_sample.key was made by Qt 6.11.2.
func TestVpnKeyHasTheKeysOfTheQtSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/qt_sample.key")
	if err != nil {
		t.Fatal(err)
	}
	js, err := vpnkey.Decode(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var qt map[string]any
	_ = json.Unmarshal(js, &qt)
	qtLast := map[string]any{}
	_ = json.Unmarshal([]byte(qt["containers"].([]any)[0].(map[string]any)["awg"].(map[string]any)["last_config"].(string)), &qtLast)

	// the sample has a single I packet and every 3.x key: fixture31 with one I and disable_cookies irrelevant
	doc, last, _ := decodeKey(t, render(t, fixture31(), plugin.FormatAmneziaVPN))
	if a, b := keys(qt), keys(doc); !slices.Equal(a, b) {
		t.Errorf("top-level keys: Qt %v, ours %v", a, b)
	}
	qtBlk := qt["containers"].([]any)[0].(map[string]any)["awg"].(map[string]any)
	ourBlk := doc["containers"].([]any)[0].(map[string]any)["awg"].(map[string]any)
	if a, b := keys(qtBlk), keys(ourBlk); !slices.Equal(a, b) {
		t.Errorf("awg block keys: Qt %v, ours %v", a, b)
	}
	if a, b := keys(qtLast), keys(last); !slices.Equal(a, b) {
		t.Errorf("last_config keys: Qt %v, ours %v", a, b)
	}
	for k, v := range qtLast { // same JSON types too
		if ov, ok := last[k]; ok && jsonType(v) != jsonType(ov) {
			t.Errorf("last_config[%s]: Qt %s, ours %s", k, jsonType(v), jsonType(ov))
		}
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func jsonType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case []any:
		return "array"
	}
	return "other"
}

// ---- Mihomo ----

func decodeProxy(t *testing.T, frag plugin.Fragment) map[string]any {
	t.Helper()
	var list []map[string]any
	if err := yaml.Unmarshal(frag.Data, &list); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, frag.Data)
	}
	if len(list) != 1 {
		t.Fatalf("a fragment is ONE proxy, got %d:\n%s", len(list), frag.Data)
	}
	return list[0]
}

func TestMihomoGolden(t *testing.T) {
	golden(t, "awg31.mihomo.yaml", render(t, fixture31(), plugin.FormatMihomo).Data)
	golden(t, "awg20.mihomo.yaml", render(t, fixture20(), plugin.FormatMihomo).Data)
}

func TestMihomoProxy(t *testing.T) {
	p := decodeProxy(t, render(t, fixture31(), plugin.FormatMihomo))
	if p["type"] != "wireguard" || p["server"] != "203.0.113.10" || p["port"] != 51842 || p["ip"] != "10.66.4.5/32" || p["ipv6"] != "fd66:66:0:1::5/128" ||
		p["mtu"] != 1280 || p["udp"] != true || p["persistent-keepalive"] != 25 { // the lower end of 25-35
		t.Errorf("proxy: %v", p)
	}
	o := p["amnezia-wg-option"].(map[string]any)
	for k, want := range map[string]any{
		"version": 3, "jc": 6, "jmin": 10, "jmax": 50, "s1": 24, "s4": 24, "h1": "1", "h4": "4",
		"header-protection-key": key32(0x40), "random-trailers": true, "disable-cookies": true,
		"content-padding-addition": "2-10", "rekey-after-time": "100-120", "max-handshake-attempts": "15-20", "i1": fixtureI1,
	} {
		if o[k] != want {
			t.Errorf("amnezia-wg-option[%s] = %#v, want %#v", k, o[k], want)
		}
	}
	if _, ok := o["i2"]; ok {
		t.Error("an empty I packet must be left out")
	}

	q := decodeProxy(t, render(t, fixture20(), plugin.FormatMihomo))
	o = q["amnezia-wg-option"].(map[string]any)
	for _, k := range []string{"version", "header-protection-key", "random-trailers", "disable-cookies", "content-padding-addition", "rekey-after-time"} {
		if _, ok := o[k]; ok {
			t.Errorf("a 2.0 proxy has %s: the kernel of Mihomo would switch to the v3 device", k)
		}
	}
	if o["s3"] != 25 || o["h1"] != "100000-200000" || q["persistent-keepalive"] != 25 {
		t.Errorf("2.0 proxy: %v %v", o, q["persistent-keepalive"])
	}
	if _, ok := q["ipv6"]; ok {
		t.Error("IPv4-only profile has an ipv6 address")
	}
	if !slices.Equal(toStrings(q["allowed-ips"]), []string{"0.0.0.0/0"}) {
		t.Errorf("allowed-ips %v", q["allowed-ips"])
	}
}

// Node and user names are hostile input: whatever they hold, the fragment stays one proxy with that exact name.
func TestMihomoHostileNames(t *testing.T) {
	for _, name := range []string{
		"x\n- name: injected\n  type: http", "a: b", "- dash", "#comment", "</script><script>alert(1)</script>",
		"emoji 🇩🇪 · ноль", "\"quoted\" 'single'", "{flow: [a, b]}", "*anchor &ref !tag", "0o17", "null", "yes", "1e3", "\t", "%YAML 1.2\n---",
	} {
		in := renderInput(t, fixture31(), plugin.FormatMihomo)
		in.DisplayName = name
		frag, ok := New().Render(in)
		if !ok {
			t.Fatalf("%q refused", name)
		}
		p := decodeProxy(t, frag)
		if p["name"] != name {
			t.Errorf("name %q came back as %q", name, p["name"])
		}
		if p["type"] != "wireguard" {
			t.Errorf("%q changed the type to %v", name, p["type"])
		}
	}
}

// ---- hostile hosts, masks, formats ----

func TestRenderRefusesHostileHostsAndBrokenInput(t *testing.T) {
	for _, h := range []string{"1.2.3.4\nEndpoint = 6.6.6.6:1", "a b.example.com", "host:1", "[2001:db8::1]", "fe80::1%eth0", "-x.example.com", "", "a..b"} {
		for _, f := range []plugin.ClientFormat{plugin.FormatAWGConf, plugin.FormatAmneziaVPN, plugin.FormatMihomo} {
			in := renderInput(t, fixture31(), f)
			in.NodeAddr = h
			in.Inbound.Node.Address = h
			if _, ok := New().Render(in); ok {
				t.Errorf("host %q rendered as %s", h, f)
			}
		}
	}
	good := func() protocols.RenderInput { return renderInput(t, fixture31(), plugin.FormatAWGConf) }
	for name, mut := range map[string]func(*protocols.RenderInput){
		"no peer":      func(in *protocols.RenderInput) { in.Peer, in.Secret = nil, "" },
		"garbage peer": func(in *protocols.RenderInput) { in.Peer = []byte("{") },
		"short key": func(in *protocols.RenderInput) {
			in.Peer = []byte(`{"priv":"AAAA","psk":"` + key32(1) + `","ip4":"10.66.4.5"}`)
		},
		"v6 as ip4": func(in *protocols.RenderInput) {
			in.Peer = []byte(`{"priv":"` + key32(1) + `","psk":"` + key32(1) + `","ip4":"fd66::5"}`)
		},
		"no server key":   func(in *protocols.RenderInput) { in.InboundPublic = []byte(`{}`) },
		"invalid profile": func(in *protocols.RenderInput) { in.Settings = []byte(`{"version":"3.1"}`) },
	} {
		in := good()
		mut(&in)
		if _, ok := New().Render(in); ok {
			t.Errorf("%s: rendered", name)
		}
	}
	// the secret may come through Secret instead of Peer
	in := good()
	in.Secret, in.Peer = string(in.Peer), nil
	if _, ok := New().Render(in); !ok {
		t.Error("Secret is not read when Peer is empty")
	}
	// formats that cannot carry AWG
	for _, f := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatXrayJSON, plugin.FormatSingbox, ""} {
		in := good()
		in.Format = f
		if _, ok := New().Render(in); ok {
			t.Errorf("format %q rendered", f)
		}
	}
}

func TestMaskedPreview(t *testing.T) {
	// A profile preview has no device and no inbound: the editor still gets a .conf, without any key material.
	in := protocols.RenderInput{Format: plugin.FormatAWGConf, Settings: raw(t, fixture31()), MaskSecrets: true}
	frag, ok := New().Render(in)
	if !ok {
		t.Fatal("preview refused")
	}
	conf := string(frag.Data)
	for _, want := range []string{"PrivateKey = ••••\n", "PresharedKey = ••••\n", "HeaderProtectionKey = ••••\n", "PublicKey = ••••\n", "Address = 10.66.4.2/32, fd66:66:0:1::2/128\n", "Endpoint = 203.0.113.10:51842\n", "DNS = 1.1.1.1, 8.8.8.8\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in\n%s", want, conf)
		}
	}
	if strings.Contains(conf, key32(0x40)) {
		t.Error("the header protection key is in the preview")
	}
	// with a real device the preview masks the real keys
	in = renderInput(t, fixture31(), plugin.FormatAWGConf)
	in.MaskSecrets = true
	frag, _ = New().Render(in)
	for _, secret := range []string{key32(0x01), key32(0x80), key32(0x40)} {
		if strings.Contains(string(frag.Data), secret) {
			t.Errorf("masked preview contains %s", secret)
		}
	}
	if !strings.Contains(string(frag.Data), "PublicKey = "+key32(0xc0)) { // the server key is public
		t.Error("the server public key must stay visible")
	}
	// Mihomo may be masked, a vpn:// key may not
	in.Format = plugin.FormatMihomo
	frag, ok = New().Render(in)
	if !ok || strings.Contains(string(frag.Data), key32(0x01)) || !strings.Contains(string(frag.Data), "••••") {
		t.Errorf("masked mihomo: %v\n%s", ok, frag.Data)
	}
	in.Format = plugin.FormatAmneziaVPN
	if _, ok := New().Render(in); ok {
		t.Error("a masked vpn:// key would import as a broken one")
	}
}

// ---- per-device signatures ----

// perDevice31 is the 3.1 fixture with per-device signatures on: a preset whose chain differs per device, a seed and a
// domain (so that the pool does not enter the goldens), and the old fixed I1 as the profile's template.
func perDevice31(preset string) Settings {
	s := fixture31()
	o := &s.Obfuscation
	o.Preset, o.Domain, o.PerDeviceSignature, o.SignatureSeed = preset, "example.com", true, "00112233445566778899aabbccddeeff"
	return s
}

func renderFor(t *testing.T, s Settings, f plugin.ClientFormat, device string) plugin.Fragment {
	t.Helper()
	in := renderInput(t, s, f)
	in.DeviceID = device
	frag, ok := New().Render(in)
	if !ok {
		t.Fatalf("Render(%s, %s) refused", f, device)
	}
	return frag
}

// confChain reads I1..I5 out of a .conf.
func confChain(conf string) (out [5]string) {
	for _, l := range strings.Split(conf, "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok && len(k) == 2 && k[0] == 'I' && k[1] >= '1' && k[1] <= '5' {
			out[k[1]-'1'] = v
		}
	}
	return out
}

func TestPerDeviceSignatureInEveryFormat(t *testing.T) {
	s := perDevice31("dns")
	want, ok := deviceSignature(s.Obfuscation, "dev_A")
	if !ok || want[1] == "" || want[2] == "" {
		t.Fatalf("the dns chain has three packets: %v %v", want, ok)
	}
	conf := func(dev string) string { return string(renderFor(t, s, plugin.FormatAWGConf, dev).Data) }

	// the same device gets the same config every time, with its own chain and not the template
	a := conf("dev_A")
	if a != conf("dev_A") || confChain(a) != want || confChain(a)[0] == fixtureI1 {
		t.Errorf("device A: chain %v, want %v", confChain(a), want)
	}
	// another device differs in the signature packets and nowhere else
	b := conf("dev_B")
	if confChain(b) == confChain(a) {
		t.Error("two devices share a chain")
	}
	strip := func(c string) string {
		var keep []string
		for _, l := range strings.Split(c, "\n") {
			if k, _, _ := strings.Cut(l, " = "); !(len(k) == 2 && k[0] == 'I') {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	if strip(a) != strip(b) {
		t.Errorf("devices differ in more than I1-I5:\n%s\n---\n%s", strip(a), strip(b))
	}

	// vpn:// and Mihomo carry the very same chain as the .conf of that device
	_, last, _ := decodeKey(t, renderFor(t, s, plugin.FormatAmneziaVPN, "dev_A"))
	if last["config"] != a {
		t.Error("the config inside the vpn:// key is not the device's .conf")
	}
	for i := range 5 {
		k := fmt.Sprintf("I%d", i+1)
		if got, _ := last[k].(string); got != want[i] {
			t.Errorf("vpn:// %s = %q, want %q", k, got, want[i])
		}
	}
	opt := decodeProxy(t, renderFor(t, s, plugin.FormatMihomo, "dev_A"))["amnezia-wg-option"].(map[string]any)
	optB := decodeProxy(t, renderFor(t, s, plugin.FormatMihomo, "dev_B"))["amnezia-wg-option"].(map[string]any)
	for i := range 5 {
		k := fmt.Sprintf("i%d", i+1)
		if got, _ := opt[k].(string); got != want[i] {
			t.Errorf("mihomo %s = %q, want %q", k, got, want[i])
		}
	}
	if opt["i1"] == optB["i1"] {
		t.Error("mihomo: two devices share I1")
	}

	// the profile preview has no device: it shows the template
	in := protocols.RenderInput{Format: plugin.FormatAWGConf, Settings: raw(t, s), MaskSecrets: true}
	frag, _ := New().Render(in)
	if got := confChain(string(frag.Data)); got[0] != fixtureI1 || got[1] != "" {
		t.Errorf("the preview shows %v, want the template", got)
	}
}

// Off, a custom preset and an old profile keep the profile's own I1-I5 for every device, exactly as before.
func TestPerDeviceSignatureOffKeepsTheTemplate(t *testing.T) {
	for name, mut := range map[string]func(*Settings){
		"off":    func(s *Settings) { s.Obfuscation.PerDeviceSignature = false },
		"custom": func(s *Settings) { s.Obfuscation.Preset = "custom" },
		"legacy": func(s *Settings) {
			s.Obfuscation.PerDeviceSignature, s.Obfuscation.SignatureSeed, s.Obfuscation.Domain = false, "", ""
		},
		"no seed": func(s *Settings) { s.Obfuscation.SignatureSeed = "" }, // the validator refuses it, Render must not guess
	} {
		s := perDevice31("dns")
		mut(&s)
		for _, dev := range []string{"dev_A", "dev_B", ""} {
			in := renderInput(t, s, plugin.FormatAWGConf)
			in.DeviceID = dev
			frag, ok := New().Render(in)
			if name == "no seed" {
				if ok {
					t.Error("a profile that the validator refuses was rendered")
				}
				continue
			}
			if !ok || confChain(string(frag.Data)) != [5]string{fixtureI1} {
				t.Errorf("%s, device %q: %v (%v)", name, dev, confChain(string(frag.Data)), ok)
			}
		}
	}
	// an old profile renders byte for byte as the golden says: the fixture has none of the new fields set
	golden(t, "awg31.conf", render(t, fixture31(), plugin.FormatAWGConf).Data)
}

// A look whose chain is the same bytes for everybody gives the switch nothing to do, as the editor says: a hand edit of
// I1 under it stays on every device instead of being replaced by the generator's output.
func TestPerDeviceSignatureLeavesASamePresetAlone(t *testing.T) {
	for _, preset := range []string{"stun", "webrtc", "dtls"} {
		s := perDevice31(preset)
		for _, dev := range []string{"dev_A", "dev_B"} {
			in := renderInput(t, s, plugin.FormatAWGConf)
			in.DeviceID = dev
			frag, ok := New().Render(in)
			if got := confChain(string(frag.Data)); !ok || got != [5]string{fixtureI1} {
				t.Errorf("%s, device %s: chain %v (%v), want the profile's own", preset, dev, got, ok)
			}
		}
	}
}

func TestPerDeviceGolden(t *testing.T) {
	s := perDevice31("dns")
	golden(t, "awg31.perdevice.conf", renderFor(t, s, plugin.FormatAWGConf, "dev_1").Data)
	golden(t, "awg31.perdevice.mihomo.yaml", renderFor(t, s, plugin.FormatMihomo, "dev_1").Data)
}

// qrMaxBytes is what web/src/sub/qr.ts fits at ECC M (QR version 40): a phone scans the device .conf from the
// screen, so a new profile's default must not outgrow it. quic/curl_quic (I1 of 2406 characters) do, on purpose.
const qrMaxBytes = 2331

func TestDefaultProfileDeviceConfFitsAQR(t *testing.T) {
	def, err := defaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(def, &s); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		conf := renderFor(t, s, plugin.FormatAWGConf, fmt.Sprintf("dev_%d", i)).Data
		if len(conf) > qrMaxBytes {
			t.Fatalf("device %d: .conf of the default %q profile is %d bytes, a QR code holds %d", i, s.Obfuscation.Preset, len(conf), qrMaxBytes)
		}
	}
}
