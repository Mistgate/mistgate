package vless

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// selfSettings are the settings of a profile whose target is the node's own site: the defaults, with the short ids and the
// path a saved profile has.
func selfSettings(t *testing.T, mutate func(*Settings)) json.RawMessage {
	t.Helper()
	raw, err := New().DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
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

func TestTargetKindSchemaAndDefault(t *testing.T) {
	var schema struct {
		Properties struct {
			Reality struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"reality"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(New().SettingsSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	kind := schema.Properties.Reality.Properties["target_kind"]
	if kind == nil || kind["default"] != "self" || kind["x-widget"] != "segmented" || kind["x-critical"] != true {
		t.Fatalf("reality.target_kind = %+v", kind)
	}
	if enum, _ := kind["enum"].([]any); len(enum) != 2 || enum[0] != "self" || enum[1] != "site" {
		t.Fatalf("reality.target_kind enum = %v", kind["enum"])
	}
	labels, _ := kind["x-enum-labels"].(map[string]any)
	if labels["self"] != "This node's site" || labels["site"] != "Another site" {
		t.Fatalf("reality.target_kind labels = %v", labels)
	}
	// the segment comes first; the fields of the other choice follow it
	if kind["x-order"].(float64) >= schema.Properties.Reality.Properties["target"]["x-order"].(float64) {
		t.Error("the target kind must precede the target")
	}
	// a new profile asks for the node's own site, and a valid one needs no target at all
	if errs := New().Validate(selfSettings(t, nil)); len(errs) != 0 {
		t.Fatalf("defaults with ids and path do not validate: %+v", errs)
	}
}

// Profiles made before the node could serve its own site have a target and no target_kind: they read as another site, so
// what they send to nodes does not change by a byte.
func TestDocumentWithoutTargetKindReadsAsSite(t *testing.T) {
	legacy := func(t *testing.T) json.RawMessage {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(validSettings(t, nil), &doc); err != nil {
			t.Fatal(err)
		}
		delete(doc["reality"].(map[string]any), "target_kind")
		out, _ := json.Marshal(doc)
		if strings.Contains(string(out), "target_kind") {
			t.Fatal("the fixture still names a kind")
		}
		return out
	}
	p := New()
	if errs := p.Validate(legacy(t)); len(errs) != 0 {
		t.Fatalf("a v0.1.34 profile no longer validates: %+v", errs)
	}
	built := func(settings json.RawMessage) plugin.InboundSpec {
		spec, err := p.BuildInbound(protocols.InboundInput{
			Profile: protocols.ProfileView{ID: "prf_1", Settings: settings}, Node: protocols.NodeView{Address: "de1.example.com"},
			SpecVersion: 1, Enabled: true, PluginState: bytes32(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return spec
	}
	old, explicit := built(legacy(t)), built(validSettings(t, nil)) // validSettings says site outright
	if string(old.Settings) != string(explicit.Settings) || old.TLS != (plugin.TLS{}) || explicit.TLS != (plugin.TLS{}) {
		t.Fatalf("a profile without a kind builds differently from the same profile with kind site:\n%s\n%s", old.Settings, explicit.Settings)
	}
	var node NodeSettings
	if err := json.Unmarshal(old.Settings, &node); err != nil || node.Reality.Target != "www.example.org:443" || len(node.Reality.ServerNames) != 1 {
		t.Fatalf("legacy node settings = %+v, %v", node.Reality, err)
	}

	// no target and no kind: the default
	var bare map[string]any
	_ = json.Unmarshal(selfSettings(t, nil), &bare)
	delete(bare["reality"].(map[string]any), "target_kind")
	raw, _ := json.Marshal(bare)
	if spec := built(raw); spec.TLS.Mode != plugin.TLSAcmeDomain {
		t.Fatalf("no target and no kind built %+v, want the node's own site", spec.TLS)
	}
}

func TestNormalizeTargetWithoutKind(t *testing.T) {
	p := New()
	fresh, err := p.DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(input string, old json.RawMessage) (json.RawMessage, json.RawMessage) {
		t.Helper()
		base := old
		if base == nil {
			base = fresh
		}
		merged, err := protocols.ResolveInput(json.RawMessage(input), base, fresh, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := p.NormalizeSettings(protocols.NormalizeInput{Input: json.RawMessage(input), Old: old, Merged: merged, Save: true})
		if err != nil {
			t.Fatal(err)
		}
		return merged, out
	}
	kindOf := func(raw json.RawMessage) string {
		var s Settings
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s.Reality.TargetKind
	}

	// create
	for _, tc := range []struct{ name, input, want string }{
		{"a script that gives a target only", `{"reality":{"target":"www.example.org:443"}}`, "site"},
		{"a target with its kind", `{"reality":{"target":"www.example.org:443","target_kind":"self"}}`, "self"},
		{"no target", `{}`, "self"},
		{"an empty target", `{"reality":{"target":" "}}`, "self"},
		{"the other site by name", `{"reality":{"target_kind":"site","target":"www.example.org:443"}}`, "site"},
	} {
		if _, out := resolve(tc.input, nil); kindOf(out) != tc.want {
			t.Errorf("create, %s: kind = %q, want %q", tc.name, kindOf(out), tc.want)
		}
	}

	// update
	stored := selfSettings(t, nil) // explicit self, as saved
	if _, out := resolve(`{"reality":{"target":"www.example.org:443"}}`, stored); kindOf(out) != "self" {
		t.Errorf("update of a self profile that is sent a target: kind = %q (the target is kept, unused, while the kind is self)", kindOf(out))
	}
	site := validSettings(t, nil)
	if merged, out := resolve(`{"port":8443}`, site); string(out) != string(merged) || kindOf(out) != "site" {
		t.Errorf("update of a site profile changed the document: kind %q", kindOf(out))
	}
	var legacyDoc map[string]any
	_ = json.Unmarshal(site, &legacyDoc)
	delete(legacyDoc["reality"].(map[string]any), "target_kind")
	legacy, _ := json.Marshal(legacyDoc)
	if _, out := resolve(`{"port":8443}`, legacy); kindOf(out) != "site" {
		t.Errorf("update of a profile stored without a kind: kind = %q, want site", kindOf(out))
	}
}

func TestTargetFieldErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		name, target, code string
	}{
		{"empty", "", "required"},
		{"blank", "   ", "required"},
		{"no port", "www.example.org", "format"},
		{"no host", ":443", "format"},
		{"bad host", "bad host:443", "host"},
		{"letters in the port", "www.example.org:https", "port_range"},
		{"port zero", "www.example.org:0", "port_range"},
		{"port too high", "www.example.org:65536", "port_range"},
	} {
		errs := New().Validate(validSettings(t, func(s *Settings) { s.Reality.Target = tc.target }))
		if len(errs) != 1 || errs[0].Pointer != "/reality/target" || errs[0].Code != tc.code || errs[0].Message == "" {
			t.Errorf("%s: errors = %+v, want one %s", tc.name, errs, tc.code)
		}
	}
	for name, mutate := range map[string]func(*Settings){
		"unknown kind":                func(s *Settings) { s.Reality.TargetKind = "cdn" },
		"unknown kind with no target": func(s *Settings) { s.Reality.TargetKind = "cdn"; s.Reality.Target = "" },
	} {
		errs := New().Validate(selfSettings(t, mutate))
		if len(errs) != 1 || errs[0].Pointer != "/reality/target_kind" || errs[0].Code != "invalid_enum" {
			t.Errorf("%s: errors = %+v", name, errs)
		}
	}
	// the node's own site reads neither a target nor names, so a profile that switched kinds keeps its old values quietly
	left := selfSettings(t, func(s *Settings) {
		s.Reality.Target = "not a target"
		s.Reality.ServerNames = []string{"*.example.org", "*.example.org"}
	})
	if errs := New().Validate(left); len(errs) != 0 {
		t.Errorf("a self profile with leftovers: %+v", errs)
	}
}

func TestBuildInboundForTheNodesOwnSite(t *testing.T) {
	p := New()
	build := func(node protocols.NodeView, override string, mutate func(*Settings)) (plugin.InboundSpec, error) {
		return p.BuildInbound(protocols.InboundInput{
			Profile: protocols.ProfileView{ID: "prf_1", Settings: selfSettings(t, mutate)}, Node: node,
			TLSServerNameOverride: override, SpecVersion: 2, Enabled: true, PluginState: bytes32(),
		})
	}
	nodeNamed := protocols.NodeView{ID: "nod_1", Name: "node-a", Address: "Node-A.Example.com"}

	spec, err := build(nodeNamed, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var node NodeSettings
	if err := json.Unmarshal(spec.Settings, &node); err != nil {
		t.Fatal(err)
	}
	if spec.TLS != (plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "node-a.example.com"}) || node.Reality.Target != "self" ||
		len(node.Reality.ServerNames) != 1 || node.Reality.ServerNames[0] != "node-a.example.com" || node.Security != "reality" ||
		spec.Listen != (plugin.Listen{Network: "tcp", Port: 443}) {
		t.Fatalf("spec = %+v, node settings = %+v", spec, node)
	}

	// The inbound's own name wins over the node's address, and leftovers of the other kind do not reach the node.
	spec, err = build(nodeNamed, "Files.Example.com.", func(s *Settings) {
		s.Reality.Target, s.Reality.ServerNames = "www.example.org:443", []string{"www.example.org"}
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(spec.Settings, &node)
	if spec.TLS.ServerName != "files.example.com" || node.Reality.Target != "self" || len(node.Reality.ServerNames) != 1 || node.Reality.ServerNames[0] != "files.example.com" {
		t.Fatalf("with an SNI: spec = %+v, node settings = %+v", spec.TLS, node.Reality)
	}

	// An IP node, or a bare label, has no name for Let's Encrypt: the refusal the UI words as reality_needs_domain.
	for _, address := range []string{"203.0.113.10", "2001:db8::1", "[2001:db8::1]", "node-a", ""} {
		_, err := build(protocols.NodeView{Name: "node-a", Address: address}, "", nil)
		var needs *protocols.NeedsDomainError
		if !errors.As(err, &needs) || !needs.Reality || needs.Address != address {
			t.Errorf("address %q: error = %v, want NeedsDomainError for REALITY", address, err)
		}
	}
	// An SNI that is an IP, or has no dot, is refused as a name that Let's Encrypt cannot serve.
	for _, name := range []string{"203.0.113.10", "node-a", "bad name.example.com"} {
		_, err := build(nodeNamed, name, nil)
		var bad *protocols.ServerNameError
		if !errors.As(err, &bad) || !bad.DomainOnly {
			t.Errorf("SNI %q: error = %v, want ServerNameError (domain only)", name, err)
		}
	}
	// ... but the node's own site is not asked of a profile that names another site.
	if _, err := build(protocols.NodeView{Address: "203.0.113.10"}, "", func(s *Settings) {
		s.Reality.TargetKind, s.Reality.Target, s.Reality.ServerNames = "site", "www.example.org:443", []string{"www.example.org"}
	}); err != nil {
		t.Errorf("an IP node with another site as the target: %v", err)
	}
}

func selfRenderInput(t *testing.T, format plugin.ClientFormat) protocols.RenderInput {
	t.Helper()
	in := protocols.RenderInput{
		Format: format, Settings: selfSettings(t, nil), Inbound: vlessInbound(), InboundPublic: testPublicKey(t),
		DeviceID: "dev_123", Secret: "8a3b5c2d-1e4f-4a6b-8c0d-123456789abc",
	}
	in.Inbound.TLSServerName = "de1.example.com" // the spec's name, as access fills it in
	in.Inbound.TLSMode = plugin.TLSAcmeDomain
	return in
}

func TestRenderForTheNodesOwnSite(t *testing.T) {
	frag, ok := New().Render(selfRenderInput(t, plugin.FormatURIList))
	if !ok {
		t.Fatal("URI render refused")
	}
	renderGolden(t, "uri_self_reality.txt", string(frag.Data)+"\n")
	u, err := url.Parse(string(frag.Data))
	if err != nil || u.Query().Get("sni") != "de1.example.com" || u.Query().Get("security") != "reality" || u.Hostname() != "de1.example.com" {
		t.Fatalf("self URI = %v, %v", u, err)
	}

	mihomo, ok := New().Render(selfRenderInput(t, plugin.FormatMihomo))
	if !ok {
		t.Fatal("Mihomo render refused")
	}
	renderGolden(t, "mihomo_self_reality.yaml", string(mihomo.Data))
	var proxies []map[string]any
	if err := yaml.Unmarshal(mihomo.Data, &proxies); err != nil || len(proxies) != 1 || proxies[0]["servername"] != "de1.example.com" {
		t.Fatalf("self Mihomo proxy = %v, %v", proxies, err)
	}

	// Without the name the inbound was built with there is nothing to send: a link would carry the wrong SNI.
	in := selfRenderInput(t, plugin.FormatURIList)
	in.Inbound.TLSServerName = ""
	if _, ok := New().Render(in); ok {
		t.Error("a self inbound without its server name rendered")
	}
}
