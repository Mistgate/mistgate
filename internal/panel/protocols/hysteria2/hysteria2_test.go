package hysteria2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

func settings(t *testing.T, mutate func(*Settings)) json.RawMessage {
	t.Helper()
	raw, err := New().DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	s.Obfs.Password = "gawrgura-pw" // fixed for goldens
	if mutate != nil {
		mutate(&s)
	}
	out, _ := json.Marshal(s)
	return out
}

func TestSchemaAndDefaults(t *testing.T) {
	p := New()
	var schema map[string]any
	if err := json.Unmarshal(p.SettingsSchema(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	secrets, err := protocols.FlaggedPointers(p.SettingsSchema(), "x-secret")
	if err != nil || len(secrets) != 1 || secrets[0] != "/obfs/password" {
		t.Fatalf("x-secret pointers = %v, %v", secrets, err)
	}
	crit, _ := protocols.FlaggedPointers(p.SettingsSchema(), "x-critical")
	want := []string{"/hop", "/obfs/password", "/obfs/type", "/port", "/sni", "/tls_mode"}
	if strings.Join(crit, ",") != strings.Join(want, ",") {
		t.Errorf("x-critical = %v, want %v", crit, want)
	}
	def, err := p.DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	if errs := p.Validate(def); len(errs) != 0 {
		t.Errorf("defaults do not validate: %v", errs)
	}
	def2, _ := p.DefaultSettings()
	if string(def) == string(def2) {
		t.Error("two DefaultSettings share a generated secret")
	}
	var s Settings
	json.Unmarshal(def, &s)
	if len(s.Obfs.Password) != 32 {
		t.Errorf("generated password length %d", len(s.Obfs.Password))
	}
	// Every property the schema declares exists in Settings and vice versa.
	props := schema["properties"].(map[string]any)
	var asMap map[string]any
	json.Unmarshal(def, &asMap)
	for k := range asMap {
		if _, ok := props[k]; !ok {
			t.Errorf("settings key %q missing from schema", k)
		}
	}
	for k := range props {
		if _, ok := asMap[k]; !ok {
			t.Errorf("schema property %q missing from defaults", k)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Settings)
		pointer string // expected first error pointer, "" = valid
	}{
		{"valid", nil, ""},
		{"port zero", func(s *Settings) { s.Port = 0 }, "/port"},
		{"port high", func(s *Settings) { s.Port = 70000 }, "/port"},
		{"hop ok", func(s *Settings) { s.Hop = Hop{20000, 39999} }, ""},
		{"hop from 1024 ok", func(s *Settings) { s.Hop = Hop{1024, 21023} }, ""},
		{"hop below 1024", func(s *Settings) { s.Hop = Hop{1000, 2000} }, "/hop/from"},
		{"hop covers ssh 22", func(s *Settings) { s.Hop = Hop{20, 30} }, "/hop/from"},
		{"hop too wide", func(s *Settings) { s.Hop = Hop{20000, 50000} }, "/hop/to"},
		{"hop 20001 ports", func(s *Settings) { s.Hop = Hop{20000, 40000} }, "/hop/to"},
		{"hop to above 65535", func(s *Settings) { s.Hop = Hop{60000, 70000} }, "/hop/to"},
		{"hop reversed", func(s *Settings) { s.Hop = Hop{50000, 20000} }, "/hop/to"},
		{"hop half set", func(s *Settings) { s.Hop = Hop{20000, 0} }, "/hop/to"},
		{"hop contains port", func(s *Settings) { s.Port = 443; s.Hop = Hop{400, 500} }, "/hop/from"},
		{"acme_ip unsupported", func(s *Settings) { s.TLSMode = "acme_ip" }, "/tls_mode"},
		{"bad tls", func(s *Settings) { s.TLSMode = "x" }, "/tls_mode"},
		{"sni ip with acme", func(s *Settings) { s.SNI = "203.0.113.10" }, "/sni"},
		{"sni ip self-signed", func(s *Settings) { s.TLSMode = "self_signed"; s.SNI = "203.0.113.10" }, ""},
		{"sni garbage", func(s *Settings) { s.SNI = "a b" }, "/sni"},
		{"obfs short pw", func(s *Settings) { s.Obfs.Password = "x" }, "/obfs/password"},
		{"obfs none no pw", func(s *Settings) { s.Obfs = Obfs{Type: "none"} }, ""},
		{"obfs bad type", func(s *Settings) { s.Obfs.Type = "xor" }, "/obfs/type"},
		{"gecko ok", func(s *Settings) { s.Obfs.Type = "gecko" }, ""},
		{"masq none ok", func(s *Settings) { s.Masquerade.Type = "none" }, ""},
		{"masq proxy not yet", func(s *Settings) { s.Masquerade.Type = "proxy" }, "/masquerade/type"},
		{"bbr", func(s *Settings) { s.BBRProfile = "x" }, "/bbr_profile"},
		{"up negative", func(s *Settings) { s.UpMbps = -1 }, "/up_mbps"},
		{"down ok", func(s *Settings) { s.DownMbps = 300 }, ""},
		{"warp ok", func(s *Settings) { s.Egress = "warp" }, ""},
		{"egress garbage", func(s *Settings) { s.Egress = "tor" }, "/egress"},
	}
	for _, c := range cases {
		errs := New().Validate(settings(t, c.mutate))
		switch {
		case c.pointer == "" && len(errs) != 0:
			t.Errorf("%s: unexpected errors %v", c.name, errs)
		case c.pointer != "" && (len(errs) == 0 || errs[0].Pointer != c.pointer):
			t.Errorf("%s: errors %v, want first pointer %s", c.name, errs, c.pointer)
		}
	}
	if errs := New().Validate(json.RawMessage(`{"port":"443"}`)); len(errs) != 1 || errs[0].Pointer != "/port" {
		t.Errorf("type error: %v", errs)
	}
	if errs := New().Validate(json.RawMessage(`{"nope":1}`)); len(errs) != 1 || errs[0].Code != "invalid_json" {
		t.Errorf("unknown field: %v", errs)
	}
	if errs := New().Validate(json.RawMessage(`{"obfs":{"type":5}}`)); len(errs) != 1 || errs[0].Pointer != "/obfs/type" {
		t.Errorf("nested type error: %v", errs)
	}
}

func TestSummary(t *testing.T) {
	p := New()
	if got := p.Summary(settings(t, nil)); got != "UDP 443 · Salamander · Let's Encrypt" {
		t.Errorf("summary = %q", got)
	}
	got := p.Summary(settings(t, func(s *Settings) {
		s.Port = 8443
		s.Hop = Hop{20000, 40000}
		s.Obfs = Obfs{Type: "none"}
		s.TLSMode = "self_signed"
	}))
	if got != "UDP 8443 · hop 20000-40000 · No obfuscation · Self-signed" {
		t.Errorf("summary = %q", got)
	}
	if p.Summary(json.RawMessage(`not json`)) != "" {
		t.Error("summary of garbage must be empty")
	}
}

func TestBuildInbound(t *testing.T) {
	p := New()
	node := protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"}
	spec, err := p.BuildInbound(protocols.InboundInput{
		Profile:     protocols.ProfileView{ID: "prf_1", Settings: settings(t, func(s *Settings) { s.Hop = Hop{20000, 40000} })},
		Node:        node,
		SpecVersion: 7,
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Protocol != "hysteria2" || spec.ProfileID != "prf_1" || spec.Version != 7 || !spec.Enabled || spec.Egress != "direct" {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Listen != (plugin.Listen{Network: "udp", Port: 443, HopFrom: 20000, HopTo: 40000}) {
		t.Errorf("listen = %+v", spec.Listen)
	}
	if spec.TLS != (plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "de1.example.com"}) {
		t.Errorf("tls = %+v", spec.TLS)
	}
	wantSettings := `{"obfs":{"type":"salamander","password":"gawrgura-pw"},"masquerade":{"type":"decoy"},` +
		`"ignore_client_bandwidth":true,"bbr_profile":"standard","up_mbps":0,"down_mbps":0,"udp":true}`
	if string(spec.Settings) != wantSettings {
		t.Errorf("node settings:\n got %s\nwant %s", spec.Settings, wantSettings)
	}

	// Overrides: port and server name.
	spec, err = p.BuildInbound(protocols.InboundInput{
		Profile:               protocols.ProfileView{ID: "prf_1", Settings: settings(t, func(s *Settings) { s.SNI = "profile.example.com" })},
		Node:                  node,
		PortOverride:          8443,
		TLSServerNameOverride: "override.example.com",
	})
	if err != nil || spec.Listen.Port != 8443 || spec.TLS.ServerName != "override.example.com" {
		t.Errorf("overrides: %+v, %v", spec, err)
	}
	// Profile sni beats the node address.
	spec, _ = p.BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) { s.SNI = "profile.example.com" })}, Node: node})
	if spec.TLS.ServerName != "profile.example.com" {
		t.Errorf("sni = %q", spec.TLS.ServerName)
	}
	// Obfs none drops the password; masquerade none goes through as is.
	spec, _ = p.BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) {
			s.Obfs.Type = "none"
			s.Masquerade.Type = "none"
		})}, Node: node})
	if !strings.Contains(string(spec.Settings), `"obfs":{"type":"none"}`) ||
		!strings.Contains(string(spec.Settings), `"masquerade":{"type":"none"}`) {
		t.Errorf("settings = %s", spec.Settings)
	}

	// Errors: ACME needs a host name; port override inside the hop range; invalid settings.
	// The refusals are typed, so that the panel says them in the admin's words (access: acme_needs_domain, port_in_hop,
	// sni_invalid) while Error() stays the sentence a node page shows.
	ipNode := protocols.NodeView{Name: "x", Address: "203.0.113.10"}
	_, err = p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, nil)}, Node: ipNode})
	var needs *protocols.NeedsDomainError
	if !errors.As(err, &needs) || needs.Address != "203.0.113.10" || !strings.Contains(err.Error(), "host name is required") {
		t.Errorf("acme_domain on an IP node without SNI: %v", err)
	}
	_, err = p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) { s.Hop = Hop{20000, 40000} })}, Node: node, PortOverride: 30000})
	if hop := (*protocols.PortInHopError)(nil); !errors.As(err, &hop) || hop.From != 20000 || hop.To != 40000 {
		t.Errorf("port override inside the hop range: %v", err)
	}
	_, err = p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, nil)}, Node: ipNode, TLSServerNameOverride: "203.0.113.10"})
	if name := (*protocols.ServerNameError)(nil); !errors.As(err, &name) || !name.DomainOnly || name.Name != "203.0.113.10" {
		t.Errorf("an IP as the Let's Encrypt name: %v", err)
	}
	_, err = p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) { s.TLSMode = "self_signed" })}, Node: ipNode, TLSServerNameOverride: "not a name"})
	if name := (*protocols.ServerNameError)(nil); !errors.As(err, &name) || name.DomainOnly {
		t.Errorf("a broken self-signed name: %v", err)
	}
	spec, err = p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) { s.TLSMode = "self_signed" })}, Node: ipNode})
	if err != nil || spec.TLS != (plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "203.0.113.10"}) {
		t.Errorf("self-signed on IP node: %+v, %v", spec.TLS, err)
	}
	if _, err := p.BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) { s.Hop = Hop{20000, 40000} })}, Node: node, PortOverride: 30000}); err == nil {
		t.Error("port override inside hop range must fail")
	}
	if _, err := p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, func(s *Settings) { s.Port = 0 })}, Node: node}); err == nil {
		t.Error("invalid settings must fail")
	}
	if _, err := p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings(t, nil)}, Node: node, TLSServerNameOverride: "1.2.3.4"}); err == nil {
		t.Error("IP override with acme_domain must fail")
	}
}

func TestIssueCredential(t *testing.T) {
	p := New()
	a, err := p.IssueCredential(protocols.IssueInput{UserID: "usr_1", DeviceID: "dev_1"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.IssueCredential(protocols.IssueInput{})
	if a.Secret == b.Secret || len(a.Secret) != 43 {
		t.Errorf("secrets: %q %q", a.Secret, b.Secret)
	}
	sum := sha256.Sum256([]byte(a.Secret))
	if want := `{"auth_sha256":"` + hex.EncodeToString(sum[:]) + `"}`; string(a.NodeData) != want {
		t.Errorf("node data = %s", a.NodeData)
	}
	if strings.Contains(string(a.NodeData), a.Secret) {
		t.Error("node data contains the raw secret")
	}
}

func TestClients(t *testing.T) {
	cs := New().Clients()
	if len(cs) != 2 || cs[0].Client != plugin.ClientHapp || cs[1].Client != plugin.ClientMihomo || cs[1].Formats[0] != plugin.FormatMihomo {
		t.Fatalf("clients = %+v", cs)
	}
	if !protocols.AllowedForApps(New(), plugin.ClientHapp) || protocols.AllowedForApps(New(), plugin.ClientAmnezia) {
		t.Error("Happ must be allowed, Amnezia not")
	}
}

func inbound(mode plugin.TLSMode, sni, pin string) protocols.InboundView {
	return protocols.InboundView{
		ID:            "inb_1",
		Node:          protocols.NodeView{ID: "nod_1", Name: "de2", Address: "de2.example.com"},
		Port:          443,
		TLSServerName: sni,
		TLSMode:       mode,
		CertPinSHA256: pin,
	}
}

func TestRenderGolden(t *testing.T) {
	p := New()
	render := func(in protocols.RenderInput) string {
		t.Helper()
		f, ok := p.Render(in)
		if !ok {
			t.Fatal("Render refused")
		}
		if f.Format != plugin.FormatURIList || strings.ContainsAny(string(f.Data), "\r\n") {
			t.Fatalf("bad fragment %+v", f)
		}
		return string(f.Data)
	}
	secret := "tok_en-123"

	got := render(protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, nil), Inbound: inbound(plugin.TLSAcmeDomain, "de2.example.com", ""), Secret: secret})
	want := "hysteria2://tok_en-123@de2.example.com:443/?obfs=salamander&obfs-password=gawrgura-pw&sni=de2.example.com#de2%20%C2%B7%20hy2%20%C2%B7%20443"
	if got != want {
		t.Errorf("acme URI:\n got %s\nwant %s", got, want)
	}

	pin := "AB:CD:" + strings.Repeat("EF", 30)
	got = render(protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, func(s *Settings) { s.Obfs = Obfs{Type: "none"} }),
		Inbound: inbound(plugin.TLSSelfSigned, "de2.example.com", pin), Secret: secret})
	want = "hysteria2://tok_en-123@de2.example.com:443/?insecure=1&pinSHA256=abcd" + strings.Repeat("ef", 30) + "&sni=de2.example.com#de2%20%C2%B7%20hy2%20%C2%B7%20443"
	if got != want {
		t.Errorf("self-signed URI:\n got %s\nwant %s", got, want)
	}

	// Self-signed without a reported pin yet: no pinSHA256 (the caller decides whether to publish it).
	got = render(protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, func(s *Settings) { s.Obfs = Obfs{Type: "none"} }),
		Inbound: inbound(plugin.TLSSelfSigned, "", ""), Secret: secret})
	if strings.Contains(got, "pinSHA256") || strings.Contains(got, "insecure") || strings.Contains(got, "sni=") || strings.Contains(got, "obfs") {
		t.Errorf("minimal URI = %s", got)
	}

	// IPv6 host, IP server name (no sni), escaping in password and secret.
	v6 := inbound(plugin.TLSSelfSigned, "2001:db8::1", "")
	v6.Node.Address = "2001:db8::1"
	got = render(protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, func(s *Settings) { s.Obfs.Password = "p@ss word&x=1" }),
		Inbound: v6, Secret: "a:b@c"})
	if !strings.HasPrefix(got, "hysteria2://a%3Ab%40c@[2001:db8::1]:443/?obfs=salamander&obfs-password=p%40ss+word%26x%3D1#") || strings.Contains(got, "sni=") {
		t.Errorf("ipv6 URI = %s", got)
	}

	// Masked preview: secrets as literal bullets.
	got = render(protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, nil), Inbound: inbound(plugin.TLSAcmeDomain, "de2.example.com", ""), Secret: secret, MaskSecrets: true})
	if want := "hysteria2://••••@de2.example.com:443/?obfs=salamander&obfs-password=••••&sni=de2.example.com#"; !strings.HasPrefix(got, want) || strings.Contains(got, "gawrgura") || strings.Contains(got, secret) {
		t.Errorf("masked URI = %s", got)
	}

	// The URI parses the way clients parse it.
	u, err := url.Parse(render(protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, nil), Inbound: inbound(plugin.TLSAcmeDomain, "de2.example.com", ""), Secret: secret}))
	if err != nil || u.Scheme != "hysteria2" || u.User.Username() != secret || u.Hostname() != "de2.example.com" || u.Port() != "443" ||
		u.Query().Get("obfs") != "salamander" || u.Query().Get("obfs-password") != "gawrgura-pw" || u.Query().Get("sni") != "de2.example.com" ||
		u.Fragment != "de2 · hy2 · 443" {
		t.Errorf("parsed = %+v, %v", u, err)
	}

	// Unsupported formats are refused.
	if _, ok := p.Render(protocols.RenderInput{Format: plugin.FormatAWGConf, Settings: settings(t, nil)}); ok {
		t.Error("awg-conf must be refused")
	}
}

// Egress "warp" is a first-class spec field and shows in the summary; the node side decides whether it can run it.
func TestBuildInboundWarpEgress(t *testing.T) {
	p := New()
	raw := settings(t, func(s *Settings) { s.Egress = "warp" })
	spec, err := p.BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{ID: "prf_1", Settings: raw},
		Node:    protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"}, SpecVersion: 1, Enabled: true,
	})
	if err != nil || spec.Egress != "warp" {
		t.Fatalf("spec = %+v, err %v", spec, err)
	}
	if got := p.Summary(raw); !strings.HasSuffix(got, " · WARP") {
		t.Errorf("summary = %q", got)
	}
}
