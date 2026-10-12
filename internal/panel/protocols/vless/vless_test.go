package vless

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

func validSettings(t *testing.T, mutate func(*Settings)) json.RawMessage {
	t.Helper()
	raw, err := New().DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	s.Reality.Target = "www.example.org:443"
	s.Reality.ServerNames = []string{"www.example.org"}
	s.Reality.ShortIDs = []string{"6ba85179", "0f1e2d3c4b5a6978"}
	s.XHTTP.Path = "/q8x2kd7w"
	if mutate != nil {
		mutate(&s)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSchemaAndDefaults(t *testing.T) {
	p := New()
	var schema map[string]any
	if err := json.Unmarshal(p.SettingsSchema(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	props := schema["properties"].(map[string]any)
	for _, id := range []string{"port", "transport", "reality", "xhttp", "egress"} {
		if _, ok := props[id]; !ok {
			t.Errorf("schema is missing %q", id)
		}
	}
	for _, id := range []string{"security", "tls_mode", "cdn", "ws", "sni"} {
		if _, ok := props[id]; ok {
			t.Errorf("R3 field %q was exposed in R4", id)
		}
	}
	defs, err := p.DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var got Settings
	if err := json.Unmarshal(defs, &got); err != nil {
		t.Fatal(err)
	}
	if got.Port != 443 || got.Transport != "xhttp" || got.Security != "reality" || got.Reality.Target != "" ||
		got.Reality.Fingerprint != "chrome" || got.XHTTP.Mode != "auto" || got.XHTTP.XPaddingBytes != defaultXPaddingBytes || got.Egress != "direct" {
		t.Errorf("defaults = %+v", got)
	}
	if len(got.Reality.ShortIDs) != 0 || got.XHTTP.Path != "" {
		t.Errorf("per-profile values must not be defaults: %+v", got)
	}

	// A profile made from the defaults gets its own short IDs and path, a different set each time; a document that
	// has them is returned byte for byte.
	made := func() Settings {
		in := protocols.NormalizeInput{Merged: defs, Save: true}
		out, err := p.NormalizeSettings(in)
		if err != nil {
			t.Fatal(err)
		}
		var s Settings
		if err := json.Unmarshal(out, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := made(), made()
	if len(a.Reality.ShortIDs) != 2 || len(a.Reality.ShortIDs[0]) != 8 || len(a.Reality.ShortIDs[1]) != 16 {
		t.Errorf("generated short IDs = %v", a.Reality.ShortIDs)
	}
	for _, id := range a.Reality.ShortIDs {
		if _, err := hex.DecodeString(id); err != nil {
			t.Errorf("short ID %q is not hex", id)
		}
	}
	if !regexp.MustCompile(`^/[A-Za-z0-9_-]{10}$`).MatchString(a.XHTTP.Path) {
		t.Errorf("generated path = %q", a.XHTTP.Path)
	}
	if a.XHTTP.Path == b.XHTTP.Path || a.Reality.ShortIDs[0] == b.Reality.ShortIDs[0] {
		t.Errorf("two profiles share generated values: %+v %+v", a, b)
	}
	full := validSettings(t, nil)
	if out, err := p.NormalizeSettings(protocols.NormalizeInput{Merged: full}); err != nil || string(out) != string(full) {
		t.Errorf("complete settings changed: %s, %v", out, err)
	}
	broken := json.RawMessage(`{"port":"x"}`)
	if out, err := p.NormalizeSettings(protocols.NormalizeInput{Merged: broken}); err != nil || string(out) != string(broken) {
		t.Errorf("unreadable settings changed: %s, %v", out, err)
	}
	// only the missing half is drawn
	half := validSettings(t, func(s *Settings) { s.XHTTP.Path = "" })
	out, err := p.NormalizeSettings(protocols.NormalizeInput{Merged: half})
	if err != nil {
		t.Fatal(err)
	}
	var kept Settings
	if err := json.Unmarshal(out, &kept); err != nil || kept.XHTTP.Path == "" || kept.Reality.ShortIDs[0] != "6ba85179" {
		t.Errorf("half-filled settings = %s, %v", out, err)
	}

	crit, err := protocols.FlaggedPointers(p.SettingsSchema(), "x-critical")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/port", "/reality/server_names", "/reality/short_ids", "/reality/target", "/transport", "/xhttp/host", "/xhttp/path", "/xhttp/x_padding_bytes"}
	if strings.Join(crit, ",") != strings.Join(want, ",") {
		t.Errorf("x-critical = %v, want %v", crit, want)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		want    string
		message string
	}{
		{name: "valid"},
		{name: "port zero", mutate: func(s *Settings) { s.Port = 0 }, want: "/port"},
		{name: "port high", mutate: func(s *Settings) { s.Port = 65536 }, want: "/port"},
		{name: "port 80", mutate: func(s *Settings) { s.Port = 80 }, want: "/port", message: "reserved"},
		{name: "tcp reality", mutate: func(s *Settings) { s.Transport = "tcp" }},
		{name: "target missing", mutate: func(s *Settings) { s.Reality.Target = "" }, want: "/reality/target"},
		{name: "target without port", mutate: func(s *Settings) { s.Reality.Target = "www.example.org" }, want: "/reality/target"},
		{name: "target bad host", mutate: func(s *Settings) { s.Reality.Target = "bad host:443" }, want: "/reality/target"},
		{name: "target port zero", mutate: func(s *Settings) { s.Reality.Target = "www.example.org:0" }, want: "/reality/target"},
		{name: "target port high", mutate: func(s *Settings) { s.Reality.Target = "www.example.org:65536" }, want: "/reality/target"},
		{name: "target ipv6", mutate: func(s *Settings) { s.Reality.Target = "[2001:db8::1]:443" }},
		{name: "target name defaults server names", mutate: func(s *Settings) { s.Reality.ServerNames = nil }},
		{name: "server names nine", mutate: func(s *Settings) {
			s.Reality.ServerNames = []string{"a.example", "b.example", "c.example", "d.example", "e.example", "f.example", "g.example", "h.example", "i.example"}
		}, want: "/reality/server_names"},
		{name: "server name wildcard", mutate: func(s *Settings) { s.Reality.ServerNames = []string{"*.example.org"} }, want: "/reality/server_names"},
		{name: "server name ip", mutate: func(s *Settings) { s.Reality.ServerNames = []string{"203.0.113.10"} }, want: "/reality/server_names"},
		{name: "server names duplicate", mutate: func(s *Settings) { s.Reality.ServerNames = []string{"www.example.org", "WWW.EXAMPLE.ORG"} }, want: "/reality/server_names"},
		{name: "short IDs empty", mutate: func(s *Settings) { s.Reality.ShortIDs = nil }, want: "/reality/short_ids"},
		{name: "short IDs nine", mutate: func(s *Settings) { s.Reality.ShortIDs = []string{"00", "02", "04", "06", "08", "0a", "0c", "0e", "10"} }, want: "/reality/short_ids"},
		{name: "short ID odd", mutate: func(s *Settings) { s.Reality.ShortIDs = []string{"abc"} }, want: "/reality/short_ids"},
		{name: "short ID too long", mutate: func(s *Settings) { s.Reality.ShortIDs = []string{strings.Repeat("a", 18)} }, want: "/reality/short_ids"},
		{name: "short ID not hex", mutate: func(s *Settings) { s.Reality.ShortIDs = []string{"zz"} }, want: "/reality/short_ids"},
		{name: "short IDs duplicate after padding", mutate: func(s *Settings) { s.Reality.ShortIDs = []string{"01", "0100"} }, want: "/reality/short_ids"},
		{name: "empty short ID accepted", mutate: func(s *Settings) { s.Reality.ShortIDs = []string{""} }},
		{name: "fingerprint bad", mutate: func(s *Settings) { s.Reality.Fingerprint = "curl" }, want: "/reality/fingerprint"},
		{name: "xhttp path missing", mutate: func(s *Settings) { s.XHTTP.Path = "" }, want: "/xhttp/path"},
		{name: "xhttp path characters", mutate: func(s *Settings) { s.XHTTP.Path = "/p?x=1" }, want: "/xhttp/path"},
		{name: "xhttp mode bad", mutate: func(s *Settings) { s.XHTTP.Mode = "packet" }, want: "/xhttp/mode"},
		{name: "padding syntax", mutate: func(s *Settings) { s.XHTTP.XPaddingBytes = "100/1000" }, want: "/xhttp/x_padding_bytes"},
		{name: "padding low bound", mutate: func(s *Settings) { s.XHTTP.XPaddingBytes = "0-1" }, want: "/xhttp/x_padding_bytes"},
		{name: "padding reversed", mutate: func(s *Settings) { s.XHTTP.XPaddingBytes = "2-1" }, want: "/xhttp/x_padding_bytes"},
		{name: "padding upper bound", mutate: func(s *Settings) { s.XHTTP.XPaddingBytes = "1-4097" }, want: "/xhttp/x_padding_bytes"},
		{name: "padding maximum accepted", mutate: func(s *Settings) { s.XHTTP.XPaddingBytes = "1-4096" }},
		{name: "xhttp path 65 characters", mutate: func(s *Settings) { s.XHTTP.Path = "/" + strings.Repeat("a", 64) }, want: "/xhttp/path"},
		{name: "xhttp path 64 characters", mutate: func(s *Settings) { s.XHTTP.Path = "/" + strings.Repeat("a", 63) }},
		{name: "xhttp host bad", mutate: func(s *Settings) { s.XHTTP.Host = "a b" }, want: "/xhttp/host"},
		{name: "xhttp host set", mutate: func(s *Settings) { s.XHTTP.Host = "cover.example.org" }},
		{name: "tcp ignores xhttp fields", mutate: func(s *Settings) { s.Transport = "tcp"; s.XHTTP.Path = "" }},
		{name: "ip target needs names", mutate: func(s *Settings) { s.Reality.Target = "203.0.113.10:443"; s.Reality.ServerNames = nil }, want: "/reality/server_names"},
		{name: "ip target with names", mutate: func(s *Settings) { s.Reality.Target = "203.0.113.10:443" }},
		{name: "bad target reports once", mutate: func(s *Settings) { s.Reality.Target = ""; s.Reality.ServerNames = nil }, want: "/reality/target"},
		{name: "transport ws", mutate: func(s *Settings) { s.Transport = "ws" }, want: "/transport", message: "unsupported"},
		{name: "transport unknown", mutate: func(s *Settings) { s.Transport = "grpc" }, want: "/transport", message: "invalid_enum"},
		{name: "security tls", mutate: func(s *Settings) { s.Security = "tls" }, want: "/security", message: "unsupported"},
		{name: "security empty", mutate: func(s *Settings) { s.Security = "" }, want: "/security", message: "invalid_enum"},
		{name: "egress invalid", mutate: func(s *Settings) { s.Egress = "exit" }, want: "/egress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := New().Validate(validSettings(t, tt.mutate))
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %+v", errs)
				}
				return
			}
			if len(errs) == 0 || errs[0].Pointer != tt.want || (tt.message != "" && errs[0].Code != tt.message) {
				t.Fatalf("errors = %+v, want %s (%s)", errs, tt.want, tt.message)
			}
		})
	}
	for _, tc := range []struct {
		name, pointer string
		raw           func() json.RawMessage
	}{
		{"CDN block", "/cdn", func() json.RawMessage {
			return json.RawMessage(strings.TrimSuffix(string(validSettings(t, nil)), "}") + `,"cdn":{"host":"cdn.example.org"}}`)
		}},
		{"certificate mode", "/tls_mode", func() json.RawMessage {
			return json.RawMessage(strings.TrimSuffix(string(validSettings(t, nil)), "}") + `,"tls_mode":"self_signed"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := New().Validate(tc.raw())
			if len(errs) != 1 || errs[0].Pointer != tc.pointer || errs[0].Code != "unsupported" {
				t.Fatalf("errors = %+v", errs)
			}
		})
	}
	for name, raw := range map[string]string{"not an object": `[]`, "two documents": `{} {}`, "wrong type": `{"port":"443"}`} {
		if errs := New().Validate(json.RawMessage(raw)); len(errs) != 1 {
			t.Errorf("%s: errors = %+v", name, errs)
		}
	}
}

// An empty server_names list follows the target: nothing derived is stored, so a changed target cannot leave a stale name.
func TestEmptyServerNamesFollowTheTarget(t *testing.T) {
	for _, target := range []string{"www.example.org:443", "www.example.net:8443"} {
		settings := validSettings(t, func(s *Settings) { s.Reality.Target = target; s.Reality.ServerNames = nil })
		if errs := New().Validate(settings); len(errs) != 0 {
			t.Fatalf("%s: %+v", target, errs)
		}
		spec, err := New().BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings}, PluginState: bytes32()})
		if err != nil {
			t.Fatal(err)
		}
		var node NodeSettings
		if err := json.Unmarshal(spec.Settings, &node); err != nil {
			t.Fatal(err)
		}
		host, _, _ := strings.Cut(target, ":")
		if len(node.Reality.ServerNames) != 1 || node.Reality.ServerNames[0] != host {
			t.Errorf("%s: server names = %v", target, node.Reality.ServerNames)
		}
	}
}

func bytes32() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

func TestInitBuildAndCredential(t *testing.T) {
	p := New()
	init, err := p.InitInbound(protocols.InboundInitInput{InboundID: "inb_1"})
	if err != nil || len(init.State) != 32 {
		t.Fatalf("InitInbound state length %d, err %v", len(init.State), err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(init.State)
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(init.Public, &public); err != nil {
		t.Fatal(err)
	}
	decodedPublic, err := base64.RawURLEncoding.DecodeString(public.PublicKey)
	if err != nil || string(decodedPublic) != string(priv.PublicKey().Bytes()) {
		t.Fatalf("public key does not match private key: %v", err)
	}
	settings := validSettings(t, nil)
	spec, err := p.BuildInbound(protocols.InboundInput{
		Profile:     protocols.ProfileView{ID: "prf_1", Settings: settings},
		Node:        protocols.NodeView{ID: "nod_1", Name: "de1", Address: "de1.example.com"},
		SpecVersion: 3, Enabled: true, PluginState: init.State, PluginPublic: init.Public,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Protocol != ID || spec.ProfileID != "prf_1" || spec.Version != 3 || !spec.Enabled || spec.Egress != "direct" ||
		spec.Listen != (plugin.Listen{Network: "tcp", Port: 443}) || spec.TLS != (plugin.TLS{}) {
		t.Errorf("spec = %+v", spec)
	}
	var node NodeSettings
	if err := json.Unmarshal(spec.Settings, &node); err != nil {
		t.Fatal(err)
	}
	if node.Transport != "xhttp" || node.Security != "reality" || node.Flow != "" || node.Reality.Target != "www.example.org:443" ||
		len(node.Reality.ServerNames) != 1 || node.Reality.ServerNames[0] != "www.example.org" || node.Reality.PrivateKey != base64.RawURLEncoding.EncodeToString(init.State) ||
		node.XHTTP.Path != "/q8x2kd7w" || node.XHTTP.Mode != "auto" || node.XHTTP.XPaddingBytes != defaultXPaddingBytes || node.WS != (NodeWebSocket{}) {
		t.Errorf("node settings = %+v", node)
	}
	tcp, err := p.BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: validSettings(t, func(s *Settings) { s.Transport = "tcp" })},
		Node:    protocols.NodeView{Address: "de1.example.com"}, PluginState: init.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tcp.Settings, &node); err != nil || node.Flow != "xtls-rprx-vision" {
		t.Fatalf("tcp node flow = %q, err %v", node.Flow, err)
	}
	if _, err := p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings}, PluginState: init.State, PortOverride: 80}); err == nil {
		t.Error("port override 80 must fail")
	}
	if _, err := p.BuildInbound(protocols.InboundInput{Profile: protocols.ProfileView{Settings: settings}}); err == nil {
		t.Error("missing inbound key must fail")
	}

	a, err := p.IssueCredential(protocols.IssueInput{UserID: "usr_1", DeviceID: "dev_1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.IssueCredential(protocols.IssueInput{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Secret == b.Secret || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a.Secret) {
		t.Errorf("credentials are not unique UUID v4 values: %q %q", a.Secret, b.Secret)
	}
	wantData := fmt.Sprintf(`{"id":%q}`, a.Secret)
	if string(a.NodeData) != wantData || strings.Contains(string(a.NodeData), "private") {
		t.Errorf("node data = %s", a.NodeData)
	}
}

func TestClients(t *testing.T) {
	clients := New().Clients()
	if len(clients) != 2 || clients[0].Client != plugin.ClientHapp || clients[0].Formats[0] != plugin.FormatURIList ||
		clients[1].Client != plugin.ClientMihomo || clients[1].Formats[0] != plugin.FormatMihomo {
		t.Fatalf("clients = %+v", clients)
	}
	if !protocols.AllowedForApps(New(), plugin.ClientHapp) || !protocols.AllowedForApps(New(), plugin.ClientMihomo) || protocols.AllowedForApps(New(), plugin.ClientAmnezia) {
		t.Error("VLESS must follow the Happ/Mihomo link-app toggle")
	}
}
