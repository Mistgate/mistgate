package awg

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
	"github.com/mistgate/mistgate/internal/plugin"
)

// jsonTags lists the json names of a struct's fields.
func jsonTags(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		out = append(out, strings.Split(t.Field(i).Tag.Get("json"), ",")[0])
	}
	return out
}

func TestSchemaMatchesSettings(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
			Enum       []string                  `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(New().SettingsSchema(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	var top, ob []string
	for k := range schema.Properties {
		top = append(top, k)
	}
	for k := range schema.Properties["obfuscation"].Properties {
		ob = append(ob, k)
	}
	want := jsonTags(reflect.TypeOf(Settings{}))
	slices.Sort(top)
	slices.Sort(want)
	if !slices.Equal(top, want) {
		t.Errorf("schema properties %v != Settings fields %v", top, want)
	}
	wantOb := jsonTags(reflect.TypeOf(Obfuscation{}))
	slices.Sort(ob)
	slices.Sort(wantOb)
	if !slices.Equal(ob, wantOb) {
		t.Errorf("schema obfuscation properties %v != Obfuscation fields %v", ob, wantOb)
	}

	// the preset enum is the mimicry list, in order
	var presets []string
	for _, p := range mimicry.Presets() {
		presets = append(presets, p.ID)
	}
	var enum struct {
		Properties struct {
			Obfuscation struct {
				Properties struct {
					Preset struct {
						Enum   []string          `json:"enum"`
						Labels map[string]string `json:"x-enum-labels"`
					} `json:"preset"`
				} `json:"properties"`
			} `json:"obfuscation"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(New().SettingsSchema(), &enum)
	e := enum.Properties.Obfuscation.Properties.Preset
	if !slices.Equal(e.Enum, presets) {
		t.Errorf("preset enum %v != %v", e.Enum, presets)
	}
	for _, p := range mimicry.Presets() {
		if e.Labels[p.ID] != p.Label {
			t.Errorf("label of %s: %q != %q", p.ID, e.Labels[p.ID], p.Label)
		}
	}

	secrets, err := protocols.FlaggedPointers(New().SettingsSchema(), "x-secret")
	if err != nil || !slices.Equal(secrets, []string{"/obfuscation/header_protection_key"}) {
		t.Errorf("x-secret = %v, %v", secrets, err)
	}
	critical, _ := protocols.FlaggedPointers(New().SettingsSchema(), "x-critical")
	wantCritical := []string{
		"/port", "/subnet4", "/subnet6", "/version",
		"/obfuscation/h1", "/obfuscation/h2", "/obfuscation/h3", "/obfuscation/h4",
		"/obfuscation/header_protection_key", "/obfuscation/random_trailers",
		"/obfuscation/s1", "/obfuscation/s2", "/obfuscation/s3", "/obfuscation/s4",
	}
	slices.Sort(wantCritical)
	if !slices.Equal(critical, wantCritical) {
		t.Errorf("x-critical = %v\nwant %v", critical, wantCritical)
	}
	// What the client alone cares about must not be critical.
	for _, ptr := range []string{"/obfuscation/jc", "/obfuscation/i1", "/obfuscation/content_padding_addition", "/obfuscation/persistent_keepalive", "/mtu", "/egress",
		"/obfuscation/domain", "/obfuscation/per_device_signature", "/obfuscation/signature_seed", "/obfuscation/preset"} {
		if slices.Contains(critical, ptr) {
			t.Errorf("%s must not be critical", ptr)
		}
	}
}

func TestSecretRoundTripThroughTheFramework(t *testing.T) {
	p := New()
	def, _ := p.DefaultSettings()
	secrets, _ := protocols.FlaggedPointers(p.SettingsSchema(), "x-secret")
	pub, sec, err := protocols.SplitSecrets(def, secrets)
	if err != nil || len(sec) != 1 || strings.Contains(string(pub), sec["/obfuscation/header_protection_key"]) {
		t.Fatalf("split: secrets %v, %v", sec, err)
	}
	back, err := protocols.MergeSecrets(pub, sec)
	if err != nil || p.Validate(back) != nil {
		t.Fatalf("merge: %v %v", err, p.Validate(back))
	}
	// A 2.0 edit sent over defaults: the framework regenerates the empty secret, and the profile stays valid.
	in := raw(t, map[string]any{"version": "2.0", "obfuscation": map[string]any{
		"header_protection_key": "", "random_trailers": false, "content_padding_addition": "", "rekey_after_time": "",
		"rekey_timeout": "", "reject_after_time": "", "keepalive_timeout": "", "max_handshake_attempts": "",
		"persistent_keepalive": "25", "s1": 40, "s2": 60, "s3": 25, "s4": 18,
		"h1": "10-20", "h2": "30-40", "h3": "50-60", "h4": "70-80",
	}})
	merged, err := protocols.ResolveInput(in, def, def, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if e := p.Validate(merged); len(e) > 0 {
		t.Errorf("2.0 over defaults: %+v", e)
	}
}

func TestSummary(t *testing.T) {
	s := fixture31()
	if got := New().Summary(raw(t, s)); got != "UDP 51842 · AWG 3.1 · WebRTC" {
		t.Errorf("summary = %q", got)
	}
	s = fixture20()
	s.Egress = "warp"
	if got := New().Summary(raw(t, s)); got != "UDP 43210 · AWG 2.0 · DNS · WARP" {
		t.Errorf("summary = %q", got)
	}
	s.Obfuscation.Preset = "custom"
	if got := New().Summary(raw(t, s)); got != "UDP 43210 · AWG 2.0 · WARP" {
		t.Errorf("summary = %q", got)
	}
	if New().Summary([]byte("{")) != "" {
		t.Error("summary of garbage")
	}
}

func TestInitInbound(t *testing.T) {
	p := New()
	a, err := p.InitInbound(protocols.InboundInitInput{InboundID: "inb_1"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.InitInbound(protocols.InboundInitInput{InboundID: "inb_2"})
	if string(a.State) == string(b.State) {
		t.Error("two inbounds share a server key")
	}
	var pub struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(a.Public, &pub); err != nil {
		t.Fatal(err)
	}
	want, err := publicKeyOf(string(a.State))
	if err != nil || pub.PublicKey != want {
		t.Errorf("public key %q does not match the private key (%q, %v)", pub.PublicKey, want, err)
	}
	if strings.Contains(string(a.Public), string(a.State)) {
		t.Error("the private key leaked into the public part")
	}
	raw, _ := base64.StdEncoding.DecodeString(string(a.State))
	if raw[0]&7 != 0 || raw[31]&0xC0 != 0x40 {
		t.Error("private key is not clamped")
	}
}

func TestBuildInbound(t *testing.T) {
	p := New()
	priv := key32(0x20)
	in := protocols.InboundInput{
		Profile:     protocols.ProfileView{ID: "prf_1", Version: 3, Settings: raw(t, fixture31())},
		Node:        protocols.NodeView{ID: "nod_1", Name: "de1", Address: "203.0.113.10"},
		SpecVersion: 7, Enabled: true, InboundID: "inb_1", PluginState: []byte(priv),
	}
	spec, err := p.BuildInbound(in)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Protocol != "awg" || spec.ProfileID != "prf_1" || spec.Version != 7 || !spec.Enabled || spec.Egress != "direct" {
		t.Errorf("spec: %+v", spec)
	}
	if spec.Listen != (plugin.Listen{Network: "udp", Port: 51842}) || spec.TLS != (plugin.TLS{}) {
		t.Errorf("listen/tls: %+v %+v", spec.Listen, spec.TLS)
	}
	if spec.Tunnel.AddrV4.String() != "10.66.4.1/22" || spec.Tunnel.AddrV6.String() != "fd66:66:0:1::1/64" || spec.Tunnel.MTU != 1280 {
		t.Errorf("tunnel: %+v", spec.Tunnel)
	}
	golden(t, "node_settings_31.json", append([]byte(spec.Settings), '\n'))
	var ns map[string]any
	_ = json.Unmarshal(spec.Settings, &ns)
	for _, k := range []string{"i1", "i2", "preset", "persistent_keepalive", "port", "mtu", "egress", "subnet4", "subnet6", "domain", "per_device_signature", "signature_seed"} {
		if _, ok := ns[k]; ok {
			t.Errorf("node settings carry %q", k)
		}
	}
	if obf, _ := ns["obfuscation"].(map[string]any); obf != nil {
		for _, k := range []string{"i1", "preset", "persistent_keepalive", "domain", "per_device_signature", "signature_seed"} {
			if _, ok := obf[k]; ok {
				t.Errorf("node obfuscation carries %q", k)
			}
		}
	}
	obf, _ := ns["obfuscation"].(map[string]any)
	if ns["private_key"] != priv || obf == nil || obf["header_protection_key"] == nil || obf["h1"] == nil {
		t.Errorf("node settings: %v", ns)
	}
	if _, flat := ns["h1"]; flat {
		t.Errorf("node settings are flat, the node reads them under obfuscation: %v", ns)
	}

	// deterministic bytes: they feed the state hash
	again, _ := p.BuildInbound(in)
	if string(again.Settings) != string(spec.Settings) {
		t.Error("settings bytes are not stable")
	}
	// the client-only fields never move the node: the bytes of a profile with per-device signatures, a domain and a
	// seed are those of the same profile without them (no restart, no new state hash)
	ps := perDevice31("quic")
	ps.Obfuscation.I1, ps.Obfuscation.Preset = fixtureI1, fixture31().Obfuscation.Preset
	in2 := in
	in2.Profile.Settings = raw(t, ps)
	if withIt, err := p.BuildInbound(in2); err != nil || string(withIt.Settings) != string(spec.Settings) {
		t.Errorf("client-only fields reached the node: %v\n%s\n%s", err, withIt.Settings, spec.Settings)
	}

	// a port override moves the listener only; egress and IPv4-only profiles pass through
	in.PortOverride = 4443
	s := fixture31()
	s.Egress, s.Subnet6 = "warp", ""
	in.Profile.Settings = raw(t, s)
	spec, err = p.BuildInbound(in)
	if err != nil || spec.Listen.Port != 4443 || spec.Egress != "warp" || spec.Tunnel.AddrV6.IsValid() {
		t.Errorf("override: %+v %v", spec, err)
	}

	// 2.0: the node never gets a 3.x key, even one the profile stores
	s = fixture20()
	s.Obfuscation.HeaderProtectionKey = key32(9)
	in.Profile.Settings = raw(t, s)
	in.PortOverride = 0
	spec, err = p.BuildInbound(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "node_settings_20.json", append([]byte(spec.Settings), '\n'))
	if strings.Contains(string(spec.Settings), "header_protection_key") || strings.Contains(string(spec.Settings), "random_trailers") {
		t.Errorf("2.0 node settings: %s", spec.Settings)
	}

	// refusals
	in.PluginState = nil
	if _, err := p.BuildInbound(in); err == nil {
		t.Error("built without a server key")
	}
	in.PluginState = []byte("short")
	if _, err := p.BuildInbound(in); err == nil {
		t.Error("built with a damaged server key")
	}
	in.PluginState = []byte(priv)
	in.Profile.Settings = []byte(`{"version":"3.1"}`)
	if _, err := p.BuildInbound(in); err == nil {
		t.Error("built from invalid settings")
	}
}

func TestIssueCredential(t *testing.T) {
	p := New()
	settings := raw(t, fixture31())
	iss, err := p.IssueCredential(protocols.IssueInput{UserID: "usr_1", DeviceID: "dev_1", ProfileID: "prf_1", Settings: settings, PeerIndex: 5})
	if err != nil {
		t.Fatal(err)
	}
	var sec peerSecret
	if err := json.Unmarshal([]byte(iss.Secret), &sec); err != nil {
		t.Fatal(err)
	}
	var nd NodeData
	if err := json.Unmarshal(iss.NodeData, &nd); err != nil {
		t.Fatal(err)
	}
	pub, err := publicKeyOf(sec.Priv)
	if err != nil || nd.PublicKey != pub {
		t.Errorf("node gets %q, the private key gives %q (%v)", nd.PublicKey, pub, err)
	}
	if sec.IP4 != "10.66.4.5" || sec.IP6 != "fd66:66:0:1::5" || nd.PSK != sec.PSK || !validKey(sec.PSK) {
		t.Errorf("secret %+v", sec)
	}
	if !slices.Equal(nd.AllowedIPs, []string{"10.66.4.5/32", "fd66:66:0:1::5/128"}) {
		t.Errorf("allowed ips %v", nd.AllowedIPs)
	}
	if strings.Contains(string(iss.NodeData), sec.Priv) {
		t.Error("the private key reached the node data")
	}
	if !strings.HasPrefix(string(iss.NodeData), `{"public_key":`) || !strings.Contains(string(iss.NodeData), `"allowed_ips":["10.66.4.5/32"`) {
		t.Errorf("node data field order: %s", iss.NodeData)
	}

	// IPv4-only profile; the last peer of a /22 and one past it
	s := fixture31()
	s.Subnet6 = ""
	iss, err = p.IssueCredential(protocols.IssueInput{ProfileID: "prf_1", Settings: raw(t, s), PeerIndex: 1022})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(iss.NodeData, &nd)
	if !slices.Equal(nd.AllowedIPs, []string{"10.66.7.254/32"}) {
		t.Errorf("last peer of the /22: %v", nd.AllowedIPs)
	}
	if n, err := MaxPeerIndex(raw(t, s)); err != nil || n != 1022 {
		t.Errorf("MaxPeerIndex = %d, %v", n, err)
	}
	for _, idx := range []int{0, 1, 1023, -4} {
		if _, err := p.IssueCredential(protocols.IssueInput{ProfileID: "prf_1", Settings: raw(t, s), PeerIndex: idx}); err == nil {
			t.Errorf("peer index %d accepted", idx)
		}
	}
	// hysteria2-style call (no profile): refused, not a half credential
	if _, err := p.IssueCredential(protocols.IssueInput{UserID: "u", DeviceID: "d"}); err == nil {
		t.Error("issued without a profile")
	}

	// two credentials never share keys
	a, _ := p.IssueCredential(protocols.IssueInput{ProfileID: "p", Settings: settings, PeerIndex: 2})
	b, _ := p.IssueCredential(protocols.IssueInput{ProfileID: "p", Settings: settings, PeerIndex: 3})
	if a.Secret == b.Secret || string(a.NodeData) == string(b.NodeData) {
		t.Error("two peers share keys")
	}
}

func TestPeerAddrs(t *testing.T) {
	v4, v6, err := PeerAddrs(raw(t, fixture31()), 300)
	if err != nil || v4.String() != "10.66.5.44" || v6.String() != "fd66:66:0:1::12c" {
		t.Errorf("idx 300: %v %v %v", v4, v6, err)
	}
	if _, _, err := PeerAddrs([]byte("{"), 2); err == nil {
		t.Error("garbage accepted")
	}
}

func TestPerDeviceClientsAndMinClients(t *testing.T) {
	p := New()
	if !protocols.IsPerDevice(p) || p.ID() != "awg" {
		t.Error("awg must be per-device")
	}
	var amnezia, mihomo, happ bool
	for _, c := range p.Clients() {
		switch c.Client {
		case plugin.ClientAmnezia:
			amnezia = slices.Equal(c.Formats, []plugin.ClientFormat{plugin.FormatAmneziaVPN, plugin.FormatAWGConf})
		case plugin.ClientMihomo:
			mihomo = slices.Equal(c.Formats, []plugin.ClientFormat{plugin.FormatMihomo})
		case plugin.ClientHapp:
			happ = true
		}
	}
	if !amnezia || !mihomo || happ {
		t.Errorf("clients: amnezia %v mihomo %v happ %v", amnezia, mihomo, happ)
	}
	if !protocols.AllowedForApps(p, plugin.ClientAmnezia) || protocols.AllowedForApps(p, plugin.ClientHapp) {
		t.Error("AllowedForApps: Happ must not get AWG")
	}

	min := func(s Settings) map[string]string {
		m := map[string]string{}
		for _, r := range protocols.MinClientsOf(p, raw(t, s)) {
			m[r.App] = r.Min
		}
		return m
	}
	m31 := min(fixture31())
	for app, v := range map[string]string{"AmneziaVPN": "5.0.1.5", "AmneziaWG Android": "v3.1.20260814", "AmneziaWG Windows": "3.1.0", "AmneziaWG Apple": "v3.1.3", "Mihomo": "v1.19.30"} {
		if m31[app] != v {
			t.Errorf("3.1 %s = %q, want %q", app, m31[app], v)
		}
	}
	m20 := min(fixture20())
	if m20["AmneziaVPN"] != "4.8.12.9" || m20["Mihomo"] != "v1.19.14" || m20["AmneziaWG"] == "" {
		t.Errorf("2.0: %v", m20)
	}
	// the returned slice is a copy: a caller cannot edit the table
	r := protocols.MinClientsOf(p, raw(t, fixture31()))
	r[0].Min = "0"
	if protocols.MinClientsOf(p, raw(t, fixture31()))[0].Min == "0" {
		t.Error("MinClients hands out its table")
	}
	if got := protocols.MinClientsOf(p, []byte("{")); len(got) != len(minClients31) {
		t.Error("unparsable settings must give the strict list")
	}
}

// Whatever the generator makes, the node accepts: the bytes BuildInbound sends parse and pass the node's own
// validator (the agent runs it again in Apply), for both versions at the MTUs the form offers. A random value that
// the node refuses would fail every inbound of a new profile.
func TestGeneratedProfilesAreAcceptedByTheNode(t *testing.T) {
	p := New()
	for _, version := range []string{Version31, Version20} {
		for _, mtu := range []int{1200, 1280, 1400, 1408, 1420} {
			for seed := uint64(0); seed < 80; seed++ {
				o, err := GenerateObfuscationSeeded(version, "stun", mtu, seed, "")
				if err != nil {
					t.Fatal(err)
				}
				s := fixture31()
				s.Version, s.MTU, s.Obfuscation = version, mtu, o
				spec, err := p.BuildInbound(protocols.InboundInput{
					Profile: protocols.ProfileView{Settings: raw(t, s)}, Node: protocols.NodeView{Address: "203.0.113.10"},
					Enabled: true, SpecVersion: 1, PluginState: []byte(key32(0x20)),
				})
				if err != nil {
					t.Fatalf("%s mtu %d seed %d: %v", version, mtu, seed, err)
				}
				ns, err := awgcfg.ParseSettings(spec.Settings)
				if err != nil {
					t.Fatalf("%s mtu %d seed %d: the node cannot read %s: %v", version, mtu, seed, spec.Settings, err)
				}
				if r := awgcfg.Validate(ns, awgcfg.Options{MTU: mtu}); !r.OK() {
					t.Fatalf("%s mtu %d seed %d: the node refuses %s: %v", version, mtu, seed, spec.Settings, r.Err())
				}
			}
		}
	}
}
