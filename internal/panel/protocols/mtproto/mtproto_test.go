package mtproto

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestValidate(t *testing.T) {
	p := New()
	for _, raw := range []string{
		`{"port":443}`,
		`{"port":8443}`,
		`{"port":443,"fake_tls":{"kind":"own"}}`,
		`{"port":443,"fake_tls":{"kind":"own","domain":""}}`,
		`{"port":443,"fake_tls":{"kind":"site","domain":"www.google.com"}}`,
		`{"port":443,"fake_tls":{"domain":"www.example.org"}}`, // a domain without a kind is another site
	} {
		if errs := p.Validate(json.RawMessage(raw)); len(errs) != 0 {
			t.Fatalf("%s: %+v", raw, errs)
		}
	}
	for raw, pointer := range map[string]string{
		`{"port":0}`:     "/port",
		`{"port":70000}`: "/port",
		`{"port":"x"}`:   "/port",
		`{}`:             "/port",
		`{"port":443,"fake_tls":{"kind":"other"}}`:                                  "/fake_tls/kind",
		`{"port":443,"fake_tls":{"kind":"site"}}`:                                   "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"203.0.113.9"}}`:            "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"2001:db8::1"}}`:            "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"www.google.com:443"}}`:     "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"https://www.google.com"}}`: "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"www.google.com/path"}}`:    "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"localhost"}}`:              "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"bad_name.example.com"}}`:   "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"-a.example.com"}}`:         "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"a..example.com"}}`:         "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"site","domain":"例え.example.com"}}`:         "/fake_tls/domain",
		`{"port":443,"fake_tls":{"kind":"own","unknown":1}}`:                        "/fake_tls",
	} {
		errs := p.Validate(json.RawMessage(raw))
		if len(errs) == 0 {
			t.Fatalf("%s accepted", raw)
		}
		if errs[0].Pointer != pointer && pointer != "/fake_tls" {
			t.Errorf("%s: pointer %q, want %q", raw, errs[0].Pointer, pointer)
		}
	}
}

func build(t *testing.T, settings, address, override string) (plugin.InboundSpec, NodeSettings, error) {
	t.Helper()
	spec, err := New().BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: json.RawMessage(settings)},
		Node:    protocols.NodeView{Address: address}, TLSServerNameOverride: override,
	})
	var ns NodeSettings
	if err == nil {
		if err := json.Unmarshal(spec.Settings, &ns); err != nil {
			t.Fatalf("settings %s: %v", spec.Settings, err)
		}
	}
	return spec, ns, err
}

func TestBuildInbound(t *testing.T) {
	spec, ns, err := build(t, `{"port":443}`, "de1.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Listen.Network != "tcp" || spec.Listen.Port != 443 || spec.Egress != "direct" {
		t.Fatalf("spec %+v", spec)
	}
	// own name: the node's host name, answered by the node's own site
	if len(ns.Domains) != 1 || ns.Domains[0] != "de1.example.com" || ns.Fallback != FallbackDecoy || spec.TLS.ServerName != "de1.example.com" {
		t.Fatalf("own: %s, TLS name %q", spec.Settings, spec.TLS.ServerName)
	}
	if string(spec.Settings) != `{"domains":["de1.example.com"],"fallback":"decoy"}` {
		t.Errorf("the node settings are %s", spec.Settings)
	}
	// a name set on the inbound wins over the node address, and makes an IP-only node usable
	if _, ns, err := build(t, `{"port":443,"fake_tls":{"kind":"own"}}`, "203.0.113.9", "Tg.Example.com."); err != nil || ns.Domains[0] != "tg.example.com" || ns.Fallback != FallbackDecoy {
		t.Errorf("inbound name: %+v, %v", ns, err)
	}
	// another site: the node with only an IP can serve, and strangers are passed on to the site
	for _, address := range []string{"203.0.113.9", "de1.example.com", "2001:db8::9"} {
		spec, ns, err := build(t, `{"port":443,"fake_tls":{"kind":"site","domain":"WWW.Google.com"}}`, address, "ignored.example.com")
		if err != nil || len(ns.Domains) != 1 || ns.Domains[0] != "www.google.com" || ns.Fallback != FallbackSite || spec.TLS.ServerName != "www.google.com" {
			t.Errorf("site on %s: %s, %v", address, spec.Settings, err)
		}
	}
	// own name on a node with only an IP address (or a bare label): refused with the reason the UI words
	for _, address := range []string{"203.0.113.9", "2001:db8::9", "[2001:db8::9]", "de1", ""} {
		_, _, err := build(t, `{"port":443}`, address, "")
		var needs *protocols.NeedsDomainError
		if !errors.As(err, &needs) || !needs.MTProto || needs.Address != address {
			t.Errorf("address %q: %v, want NeedsDomainError for MTProto", address, err)
		}
	}
	var name *protocols.ServerNameError
	if _, _, err := build(t, `{"port":443}`, "de1.example.com", "203.0.113.9"); !errors.As(err, &name) {
		t.Errorf("an IP as the inbound name: %v", err)
	}
	spec, _, err = build(t, `{"port":443}`, "de1.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	spec2, err := New().BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: json.RawMessage(`{"port":443}`)}, Node: protocols.NodeView{Address: "de1.example.com"}, PortOverride: 9443,
	})
	if err != nil || spec2.Listen.Port != 9443 {
		t.Fatalf("override %+v, %v", spec2, err)
	}
}

func inbound(host, name string, port uint16) protocols.InboundView {
	return protocols.InboundView{Port: port, Node: protocols.NodeView{Address: host, Name: "de1"}, TLSServerName: name}
}

func TestIssueAndRender(t *testing.T) {
	p := New()
	settings := json.RawMessage(`{"port":443}`)
	issued, err := p.IssueCredential(protocols.IssueInput{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.Secret) != 2*keyLen {
		t.Fatalf("secret %q", issued.Secret)
	}
	var data struct{ Key string }
	if err := json.Unmarshal(issued.NodeData, &data); err != nil {
		t.Fatal(err)
	}
	if raw, err := hex.DecodeString(data.Key); err != nil || len(raw) != keyLen || data.Key != issued.Secret {
		t.Fatalf("key %q: %v", data.Key, err)
	}
	got, ok := p.Render(protocols.RenderInput{
		Format: plugin.FormatTelegramLink, Settings: settings, Inbound: inbound("vpn.example.com", "de1.example.com", 443), Secret: issued.Secret,
	})
	if !ok {
		t.Fatal("not rendered")
	}
	u, err := url.Parse(string(got.Data))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "tg" || u.Host != "proxy" {
		t.Fatalf("link %q", got.Data)
	}
	// the domain after the key is the inbound's name, not the address the client connects to
	if want := "ee" + issued.Secret + hex.EncodeToString([]byte("de1.example.com")); u.Query().Get("server") != "vpn.example.com" || u.Query().Get("port") != "443" || u.Query().Get("secret") != want {
		t.Fatalf("query %v", u.Query())
	}
	if got.Name != "de1 - Telegram - 443" {
		t.Fatalf("name %q", got.Name)
	}
	// another site: the link carries that site
	got, _ = p.Render(protocols.RenderInput{Format: plugin.FormatTelegramLink, Inbound: inbound("203.0.113.9", "www.google.com", 443), Secret: issued.Secret})
	if u, _ := url.Parse(string(got.Data)); u == nil || u.Query().Get("secret") != "ee"+issued.Secret+hex.EncodeToString([]byte("www.google.com")) || u.Query().Get("server") != "203.0.113.9" {
		t.Errorf("site link %q", got.Data)
	}
	// no name (the build was refused), a bad key or no key: no link
	for name, in := range map[string]protocols.RenderInput{
		"no name":  {Format: plugin.FormatTelegramLink, Inbound: inbound("vpn.example.com", "", 443), Secret: issued.Secret},
		"bad key":  {Format: plugin.FormatTelegramLink, Inbound: inbound("vpn.example.com", "de1.example.com", 443), Secret: "ee" + issued.Secret},
		"no key":   {Format: plugin.FormatTelegramLink, Inbound: inbound("vpn.example.com", "de1.example.com", 443)},
		"no host":  {Format: plugin.FormatTelegramLink, Inbound: inbound("", "de1.example.com", 443), Secret: issued.Secret},
		"bad host": {Format: plugin.FormatTelegramLink, Inbound: inbound("a b", "de1.example.com", 443), Secret: issued.Secret},
	} {
		if _, ok := p.Render(in); ok {
			t.Errorf("%s: rendered", name)
		}
	}
	// No subscription format carries the link: a tg:// line in the base64 list of Happ would be a bogus server.
	for _, f := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
		if _, ok := p.Render(protocols.RenderInput{
			Format: f, Settings: settings, Secret: issued.Secret, Inbound: inbound("vpn.example.com", "de1.example.com", 443),
		}); ok {
			t.Fatalf("rendered the subscription format %s", f)
		}
	}
}

func TestSummaryUsesTheMiddleDot(t *testing.T) {
	if got := New().Summary(json.RawMessage(`{"port":443}`)); got != "TCP 443 · Fake TLS" {
		t.Fatalf("summary %q", got)
	}
	if got := New().Summary(json.RawMessage(`{"port":8443,"fake_tls":{"kind":"site","domain":"WWW.google.com"}}`)); got != "TCP 8443 · Fake TLS · www.google.com" {
		t.Fatalf("summary of another site %q", got)
	}
}

func TestDefaultSettingsAreValid(t *testing.T) {
	p := New()
	raw, err := p.DefaultSettings()
	if err != nil || len(p.Validate(raw)) != 0 {
		t.Fatalf("defaults %s: %v %v", raw, err, p.Validate(raw))
	}
	if string(raw) != `{"port":443,"fake_tls":{"kind":"own","domain":""}}` {
		t.Errorf("defaults %s", raw)
	}
	if !json.Valid([]byte(settingsSchema)) {
		t.Error("the schema is not JSON")
	}
}
