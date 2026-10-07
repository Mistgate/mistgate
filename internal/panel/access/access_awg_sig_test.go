package access

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Per-device signatures, the signature-only generator, the preview's warnings and score, and the server-side
// normalisation of AWG profiles (the awg package tests the logic; here it is wired through the service).

// confValue is the value of a key of a .conf.
func confValue(conf, key string) string {
	for _, l := range strings.Split(conf, "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok && k == key {
			return v
		}
	}
	return ""
}

func (e *env) profileSettings(id string) awg.Settings {
	e.t.Helper()
	raw := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: id}))).Msg.SettingsJson
	var s awg.Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) updateProfile(id, settings string) *adminv1.UpdateProfileResponse {
	e.t.Helper()
	ver := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: id}))).Msg.Profile.Version
	return must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: id, ExpectedVersion: ver, SettingsJson: &settings}))).Msg
}

// Every way a device's config is rendered carries the device's own I1, the same at every render: the admin's create
// and read, a rotation, and the Mihomo subscription of the user's implicit device. The profile's I1 stays the template.
func TestAWGPerDeviceSignatureOnEveryPath(t *testing.T) {
	f := newAWGFixture(t) // a default profile: 3.1, DNS, per-device signatures on
	e := f.e
	tmpl := e.profileSettings(f.profile)
	if !tmpl.Obfuscation.PerDeviceSignature || tmpl.Obfuscation.SignatureSeed == "" || tmpl.Obfuscation.I1 == "" {
		t.Fatalf("a new profile: %+v", tmpl.Obfuscation)
	}
	// A device's chain draws its name from a pool of 128, so two devices (or a device and the template) can carry the
	// same packet; awg's TestDeviceSignature pins the derivation itself. Here: every path carries a chain, and not all
	// of them are the template (checked once the Mihomo one is known).
	a, b := f.add(f.user, "a"), f.add(f.user, "b")
	ia, ib := confValue(a.Configs[0].Conf, "I1"), confValue(b.Configs[0].Conf, "I1")
	if ia == "" || ib == "" {
		t.Fatalf("I1 of device a %.40q, b %.40q: a chain expected", ia, ib)
	}

	// reading a device again, and rotating its keys, give the same signature (the config on the user's phone
	// matches the page that shows it again)
	read := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: a.Device.Id}))).Msg.Configs[0]
	if confValue(read.Conf, "I1") != ia || read.Conf != a.Configs[0].Conf {
		t.Error("a device's config changed between two reads")
	}
	rot := must(e.s.RotateDeviceKeys(e.ctx, req(&adminv1.RotateDeviceKeysRequest{DeviceId: a.Device.Id}))).Msg.Configs[0]
	if confValue(rot.Conf, "I1") != ia || rot.Conf == a.Configs[0].Conf {
		t.Error("a rotation must keep the signature and change the keys")
	}
	// the vpn:// key carries the same config
	if !strings.HasPrefix(read.VpnKey, "vpn://") || read.VpnKey != a.Configs[0].VpnKey {
		t.Error("the vpn:// key of a device changed between reads")
	}

	// the subscription (Mihomo) of the user's implicit device: its own chain, stable, and in the YAML
	token := e.tokenOf(f.user)
	yamlOf := func() string {
		v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: plugin.FormatMihomo}))
		if len(v.Lines) != 1 {
			t.Fatalf("mihomo lines = %d", len(v.Lines))
		}
		return v.Lines[0]
	}
	m1 := yamlOf()
	if m1 != yamlOf() {
		t.Error("the Mihomo proxy of a device changed between fetches")
	}
	var proxies []map[string]any
	if err := yaml.Unmarshal([]byte(m1), &proxies); err != nil || len(proxies) != 1 {
		t.Fatalf("mihomo yaml: %v\n%s", err, m1)
	}
	mi1, _ := proxies[0]["amnezia-wg-option"].(map[string]any)["i1"].(string)
	if mi1 == "" {
		t.Error("the implicit device's Mihomo proxy carries no I1")
	}
	if tI1 := tmpl.Obfuscation.I1; ia == tI1 && ib == tI1 && mi1 == tI1 {
		t.Errorf("three devices all carry the template %.40q: per-device signatures are not applied", tI1)
	}

	// off: every device carries the profile's own packets, as an old profile always did
	e.updateProfile(f.profile, `{"obfuscation":{"per_device_signature":false}}`)
	now := e.profileSettings(f.profile)
	for _, dev := range []string{a.Device.Id, b.Device.Id} {
		c := must(e.s.GetDeviceConfigs(e.ctx, req(&adminv1.GetDeviceConfigsRequest{DeviceId: dev}))).Msg.Configs[0]
		if confValue(c.Conf, "I1") != now.Obfuscation.I1 {
			t.Errorf("per-device off: device %s does not carry the profile's I1", dev)
		}
	}
	// a client-only change: nothing for the node, no device stale, no critical field
	r := e.updateProfile(f.profile, `{"obfuscation":{"per_device_signature":true,"domain":"example.com"}}`)
	if r.Impact.DevicesNeedReissue != 0 || len(r.Impact.CriticalFields) != 0 || r.Impact.InboundsRestarted != 0 {
		t.Errorf("per-device switch and domain are client-only: %+v", r.Impact)
	}
}

// Signature mode: only preset, domain and I1-I5; nothing critical; the domain goes into the packets.
func TestAWGGenerateModes(t *testing.T) {
	e := newEnv(t)
	gen := func(m *adminv1.GenerateObfuscationRequest) *adminv1.GenerateObfuscationResponse {
		t.Helper()
		return must(e.s.GenerateObfuscation(e.ctx, req(m))).Msg
	}
	keysOf := func(js string) map[string]json.RawMessage {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(js), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	sig := gen(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "sip", Domain: "Example.COM.", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE})
	m := keysOf(sig.ObfuscationJson)
	if len(m) != 7 || len(sig.Warnings) != 0 {
		t.Errorf("signature keys %v, warnings %v", m, sig.Warnings)
	}
	var ob awg.Obfuscation
	if err := json.Unmarshal([]byte(sig.ObfuscationJson), &ob); err != nil || ob.Preset != "sip" || ob.Domain != "example.com" || ob.I1 == "" ||
		!strings.Contains(ob.I1, "6578616d706c652e636f6d") { // "example.com"
		t.Errorf("signature: %+v %v", ob, err)
	}
	if a, b := gen(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "dns", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE}).ObfuscationJson,
		gen(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "dns", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE}).ObfuscationJson; a == b {
		t.Error("two signatures are identical")
	}
	// the MTU is not looked at in signature mode (nothing depends on it)
	gen(&adminv1.GenerateObfuscationRequest{Version: "2.0", Preset: "dns", Mtu: 9000, Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE})

	// full mode (unspecified and explicit): everything but the owner's per_device_signature, with a seed
	for _, mode := range []adminv1.GenerateMode{adminv1.GenerateMode_GENERATE_MODE_UNSPECIFIED, adminv1.GenerateMode_GENERATE_MODE_FULL} {
		full := gen(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "quic", Mtu: 1280, Domain: "cdn.example.net", Mode: mode})
		m := keysOf(full.ObfuscationJson)
		if _, has := m["per_device_signature"]; has {
			t.Error("the generator must leave per_device_signature to the owner")
		}
		for _, k := range []string{"signature_seed", "domain", "header_protection_key", "s4", "h1", "i1", "persistent_keepalive", "content_padding_addition"} {
			if _, ok := m[k]; !ok {
				t.Errorf("full mode lacks %s", k)
			}
		}
		var ob awg.Obfuscation
		_ = json.Unmarshal([]byte(full.ObfuscationJson), &ob)
		if len(ob.SignatureSeed) != 32 || ob.Domain != "cdn.example.net" {
			t.Errorf("seed %q domain %q", ob.SignatureSeed, ob.Domain)
		}
		for _, w := range full.Warnings {
			if w.Code == "preset_port" {
				t.Error("generate-time warnings must not judge a port the request does not carry")
			}
		}
	}
	// warnings carry their params: the MTU that fits
	hi := gen(&adminv1.GenerateObfuscationRequest{Version: "3.1", Preset: "stun", Mtu: 1420})
	var found bool
	for _, w := range hi.Warnings {
		if w.Code == "mtu_headroom" {
			found = w.Params["suggested_mtu"] == "1408" && w.Pointer == "/mtu" && w.Message != ""
		}
	}
	if !found {
		t.Errorf("warnings at MTU 1420: %v", hi.Warnings)
	}

	// refusals
	for name, r := range map[string]*adminv1.GenerateObfuscationRequest{
		"bad domain":                          {Version: "3.1", Preset: "dns", Domain: "no_dot"},
		"bad domain in signature":             {Version: "3.1", Preset: "dns", Domain: "a b.com", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE},
		"bad domain for a preset without one": {Version: "3.1", Preset: "stun", Domain: "localhost"},
		"custom has no generator":             {Version: "3.1", Preset: "custom", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE},
		"unknown preset":                      {Version: "3.1", Preset: "tls", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE},
		"unknown version":                     {Version: "1.0", Preset: "dns", Mode: adminv1.GenerateMode_GENERATE_MODE_SIGNATURE},
	} {
		if _, err := e.s.GenerateObfuscation(e.ctx, req(r)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}

	// the preset list says what the editor needs to know
	byID := map[string]*adminv1.MimicryPreset{}
	list := must(e.s.ListMimicryPresets(e.ctx, req(&adminv1.ListMimicryPresetsRequest{}))).Msg
	for _, p := range list.Presets {
		byID[p.Id] = p
	}
	if len(list.Domains) < 50 { // the pool the domain field suggests from
		t.Errorf("domains = %d", len(list.Domains))
	}
	for id, want := range map[string]struct {
		domain, varies bool
		ports          []uint32
	}{
		"quic": {true, true, []uint32{443}}, "curl_quic": {true, true, []uint32{443}}, "dns": {true, true, []uint32{53}}, "sip": {true, true, []uint32{5060}},
		"ntp": {false, true, []uint32{123}}, "ssdp": {false, true, []uint32{1900}}, "rtp": {false, true, nil},
		"stun": {false, false, nil}, "webrtc": {false, false, nil}, "dtls": {false, false, nil}, "custom": {false, false, nil},
	} {
		p := byID[id]
		if p == nil || p.UsesDomain != want.domain || p.VariesPerDevice != want.varies || len(p.NaturalPorts) != len(want.ports) || (len(want.ports) == 1 && p.NaturalPorts[0] != want.ports[0]) {
			t.Errorf("%s: %+v, want %+v", id, p, want)
		}
	}
}

// PreviewProfile returns the warnings (with params) and the score of a valid AWG profile, nothing for the rest.
func TestAWGPreviewAdviceAndScore(t *testing.T) {
	e := newEnv(t)
	r := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg"}))).Msg
	sc := r.ObfuscationScore
	if len(r.Errors) != 0 || sc == nil || sc.Tier != "header_protection" || sc.Base != 95 || sc.Value < 85 || sc.Value > 100 {
		t.Fatalf("default preview: %+v %+v", r.Errors, sc)
	}
	sum := int32(sc.Base)
	var codes []string
	for _, it := range sc.Items {
		sum += it.Delta
		codes = append(codes, it.Code)
		if it.Code == "" || it.Pointer == "" {
			t.Errorf("item %+v", it)
		}
	}
	if int32(sc.Value) != min(max(sum, 0), 100) {
		t.Errorf("the value %d is not base + items (%d): %v", sc.Value, sum, codes)
	}
	// DNS on a random port is the one remark of a new profile
	if len(r.Warnings) != 1 || r.Warnings[0].Code != "preset_port" || r.Warnings[0].Pointer != "/port" || r.Warnings[0].Params["ports"] != "53" || r.Warnings[0].Message == "" {
		t.Errorf("default warnings: %+v", r.Warnings)
	}

	// each remark of the new set, as the editor will get it
	for name, c := range map[string]struct {
		settings, code string
		params         map[string]string
	}{
		"mtu":       {`{"mtu":1420}`, "mtu_headroom", map[string]string{"suggested_mtu": "", "s4": ""}},
		"keepalive": {`{"obfuscation":{"persistent_keepalive":"25-40"}}`, "keepalive_over_nat", map[string]string{"max": "40"}},
		"2.0":       {`{"version":"2.0","obfuscation":{"preset":"stun","h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`, "h_default_v20", nil},
	} {
		p := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg", SettingsJson: c.settings}))).Msg
		var got *adminv1.FieldError
		for _, w := range p.Warnings {
			if w.Code == c.code {
				got = w
			}
		}
		if got == nil || got.Message == "" {
			t.Errorf("%s: no %s in %+v", name, c.code, p.Warnings)
			continue
		}
		for k, v := range c.params {
			if _, ok := got.Params[k]; !ok || v != "" && got.Params[k] != v {
				t.Errorf("%s: params %v", name, got.Params)
			}
		}
	}
	// the preview of a 2.0 form is the form: plain headers stay (and cost 25), they are not replaced
	p := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg", SettingsJson: `{"version":"2.0","obfuscation":{"preset":"stun","h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`}))).Msg
	if p.ObfuscationScore == nil || p.ObfuscationScore.Tier != "cps" {
		t.Errorf("2.0 plain: %+v", p.ObfuscationScore)
	}

	// errors: no remarks, no score; another protocol: none either
	bad := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "awg", SettingsJson: `{"port":0}`}))).Msg
	if len(bad.Errors) == 0 || bad.ObfuscationScore != nil || len(bad.Warnings) != 0 {
		t.Errorf("bad preview: %+v", bad)
	}
	hy := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "hysteria2"}))).Msg
	if hy.ObfuscationScore != nil || len(hy.Warnings) != 0 {
		t.Errorf("hysteria2 preview: %+v %v", hy.ObfuscationScore, hy.Warnings)
	}
}

// A profile saved through the API never carries the schema's defaults as its live config.
func TestAWGProfilesAreNormalisedOnSave(t *testing.T) {
	e := newEnv(t)
	// 2.0 without a block: a generated 2.0 block, valid, with a seed
	p := e.awgProfile("v20", `{"version":"2.0"}`)
	s := e.profileSettings(p.Id)
	o := s.Obfuscation
	if s.Version != "2.0" || o.H1 == "1" || o.RandomTrailers || o.ContentPaddingAddition != "" || o.SignatureSeed == "" || !o.PerDeviceSignature || o.PersistentKeepalive != "25" {
		t.Errorf("2.0 without a block: %+v", s)
	}
	// 2.0 with an explicit block, even with the plain headers 1, 2, 3, 4 (a pasted .conf): the owner's, kept as written
	p2 := e.awgProfile("v20 form", `{"version":"2.0","obfuscation":{"h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`)
	if o := e.profileSettings(p2.Id).Obfuscation; o.H1 != "1" || o.H2 != "2" || o.H3 != "3" || o.H4 != "4" {
		t.Errorf("an explicit block was regenerated: %+v", o)
	}
	// the version moves with no block: a generated one for the new version, and the usual critical impact
	d := e.awgProfile("v31", "")
	node := "nod_de1"
	e.node(node, "de1", "de1.example.com", "active")
	e.inbound(d.Id, node)
	g := e.group("g", d.Id)
	u := e.user("alice", g, amnOnly()).User
	must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u.Id, ProfileId: d.Id, Platform: "ios"})))
	before := e.profileSettings(d.Id).Obfuscation
	r := e.updateProfile(d.Id, `{"version":"2.0"}`)
	after := e.profileSettings(d.Id)
	if after.Version != "2.0" || after.Obfuscation.H1 == "1" || after.Obfuscation.RandomTrailers || r.Impact.DevicesNeedReissue != 1 || len(r.Impact.CriticalFields) == 0 {
		t.Errorf("3.1 -> 2.0: %+v impact %+v", after.Obfuscation, r.Impact)
	}
	if after.Obfuscation.SignatureSeed != before.SignatureSeed || after.Obfuscation.Preset != before.Preset || !after.Obfuscation.PerDeviceSignature {
		t.Errorf("the owner's preset, seed and per-device choice survive a move: %+v -> %+v", before, after.Obfuscation)
	}
	// an update that keeps the version leaves the block alone, whatever it holds
	r = e.updateProfile(d.Id, `{"mtu":1300}`)
	if now := e.profileSettings(d.Id).Obfuscation; now != after.Obfuscation || len(r.Impact.CriticalFields) != 0 {
		t.Errorf("an unrelated update rewrote the block")
	}
	// a seed is made when per-device signatures are switched on without one
	e.updateProfile(d.Id, `{"obfuscation":{"per_device_signature":false,"signature_seed":""}}`)
	e.updateProfile(d.Id, `{"obfuscation":{"per_device_signature":true}}`)
	if o := e.profileSettings(d.Id).Obfuscation; len(o.SignatureSeed) != 32 {
		t.Errorf("seed %q", o.SignatureSeed)
	}
	// a domain comes back in its normal form; a bad one is a field error on /obfuscation/domain
	e.updateProfile(d.Id, `{"obfuscation":{"domain":"Example.COM."}}`)
	if o := e.profileSettings(d.Id).Obfuscation; o.Domain != "example.com" {
		t.Errorf("domain %q", o.Domain)
	}
	bad := `{"obfuscation":{"domain":"nope"}}`
	_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: d.Id, ExpectedVersion: must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: d.Id}))).Msg.Profile.Version, SettingsJson: &bad}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "domain") {
		t.Errorf("bad domain: %v", err)
	}
}
