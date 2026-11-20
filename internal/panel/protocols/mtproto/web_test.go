package mtproto

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestValidateMode(t *testing.T) {
	p := New()
	for _, raw := range []string{
		`{"port":443,"mode":"fake_tls"}`,
		`{"port":8443,"mode":"fake_tls","fake_tls":{"kind":"site","domain":"www.google.com"}}`,
		`{"port":443,"mode":"web"}`,
		`{"port":443,"mode":"web","fake_tls":{"kind":"own"}}`,
		`{"port":443,"mode":"web","fake_tls":{"kind":"own","domain":""}}`,
	} {
		if errs := p.Validate(json.RawMessage(raw)); len(errs) != 0 {
			t.Errorf("%s: %+v", raw, errs)
		}
	}
	for raw, pointer := range map[string]string{
		`{"port":443,"mode":"proxy"}`: "/mode",
		`{"port":443,"mode":"Web"}`:   "/mode",
		`{"port":8443,"mode":"web"}`:  "/port",
		`{"port":0,"mode":"web"}`:     "/port",
		`{"port":443,"mode":"web","fake_tls":{"kind":"site","domain":"www.google.com"}}`: "/fake_tls/kind",
		`{"port":443,"mode":"web","fake_tls":{"domain":"www.google.com"}}`:               "/fake_tls/kind", // a domain without a kind is another site
	} {
		errs := p.Validate(json.RawMessage(raw))
		if len(errs) == 0 || errs[0].Pointer != pointer {
			t.Errorf("%s: %+v, want an error at %s", raw, errs, pointer)
		}
	}
}

func TestBuildWeb(t *testing.T) {
	web := `{"port":443,"mode":"web"}`

	// the node's own name, answered by the node's own site, marked web
	spec, ns, err := build(t, web, "de1.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(spec.Settings) != `{"domains":["de1.example.com"],"fallback":"decoy","mode":"web"}` || ns.Mode != "web" || spec.TLS.ServerName != "de1.example.com" || spec.Listen.Port != 443 {
		t.Fatalf("web spec %+v %s", spec, spec.Settings)
	}
	spec.Protocol = ID
	if !plugin.MTProtoWeb(spec) {
		t.Error("the built web inbound is not read as web")
	}
	// a name set on the inbound makes an IP-only node usable; a node without any name is refused as before
	if _, ns, err := build(t, web, "203.0.113.9", "Tg.Example.com."); err != nil || ns.Domains[0] != "tg.example.com" || ns.Mode != "web" {
		t.Errorf("a name set on the inbound: %+v, %v", ns, err)
	}
	for _, address := range []string{"203.0.113.9", "2001:db8::9", "de1", ""} {
		_, _, err := build(t, web, address, "")
		var needs *protocols.NeedsDomainError
		if !errors.As(err, &needs) || !needs.MTProto {
			t.Errorf("address %q: %v, want NeedsDomainError for MTProto", address, err)
		}
	}
	// the port override is 0 or 443: the Web Proxy is https
	for override, ok := range map[uint16]bool{0: true, 443: true, 80: false, 8443: false, 9443: false} {
		spec, err := New().BuildInbound(protocols.InboundInput{
			Profile: protocols.ProfileView{Settings: json.RawMessage(web)}, Node: protocols.NodeView{Address: "de1.example.com"}, PortOverride: override,
		})
		if (err == nil) != ok || (ok && spec.Listen.Port != 443) {
			t.Errorf("override %d: %+v, %v", override, spec.Listen, err)
		}
	}
	// a profile that Validate refuses is not built
	if _, _, err := build(t, `{"port":443,"mode":"web","fake_tls":{"kind":"site","domain":"www.google.com"}}`, "de1.example.com", ""); err == nil {
		t.Error("another site accepted for the Web Proxy")
	}
	// Fake TLS, written either way, keeps the settings bytes it always had
	for _, settings := range []string{`{"port":443}`, `{"port":443,"mode":"fake_tls"}`} {
		spec, _, err := build(t, settings, "de1.example.com", "")
		spec.Protocol = ID
		if err != nil || string(spec.Settings) != `{"domains":["de1.example.com"],"fallback":"decoy"}` || plugin.MTProtoWeb(spec) {
			t.Errorf("%s: %s, %v", settings, spec.Settings, err)
		}
	}
	if got := New().Summary(json.RawMessage(web)); got != "TCP 443 · Web Proxy" {
		t.Errorf("summary %q", got)
	}
}

// The spec hash of a Fake TLS inbound is what it was before the Web Proxy existed, so a node keeps the inbound running when the
// panel is updated. The golden value is the hash of the spec the panel built before the mode field was added.
func TestFakeTLSSpecHashIsUnchanged(t *testing.T) {
	spec, _, err := build(t, `{"port":443,"fake_tls":{"kind":"own","domain":""}}`, "de1.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	spec.ID, spec.Protocol, spec.Enabled = "inb_alice", ID, true
	const golden = "54985885304ca2c376809b3d5b23c06860f2ef3b746218fc2d0fbd400036b08f"
	if got := statehash.Spec(spec); got != golden {
		t.Errorf("the spec hash of a Fake TLS inbound is %s, was %s", got, golden)
	}
	web, _, err := build(t, `{"port":443,"mode":"web"}`, "de1.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	web.ID, web.Protocol, web.Enabled = "inb_alice", ID, true
	if statehash.Spec(web) == statehash.Spec(spec) {
		t.Error("the Web Proxy has the hash of the Fake TLS proxy: the agent would not restart it")
	}
}

func TestRenderWeb(t *testing.T) {
	p := New()
	web := json.RawMessage(`{"port":443,"mode":"web"}`)
	issued, err := p.IssueCredential(protocols.IssueInput{Settings: web})
	if err != nil {
		t.Fatal(err)
	}
	in := protocols.RenderInput{
		Format: plugin.FormatTelegramLink, Settings: web, Inbound: inbound("203.0.113.9", "tg.example.com", 443), Secret: issued.Secret,
	}
	got, ok := p.Render(in)
	if !ok {
		t.Fatal("not rendered")
	}
	u, err := url.Parse(string(got.Data))
	if err != nil {
		t.Fatal(err)
	}
	// the server is the inbound's name, not the node's address; the key is the person's, behind dd
	if u.Scheme != "tg" || u.Host != "webproxy" || u.Query().Get("server") != "tg.example.com" || u.Query().Get("port") != "443" ||
		u.Query().Get("secret") != "dd"+issued.Secret || len(u.Query()) != 3 {
		t.Fatalf("link %q", got.Data)
	}
	if got.Name != "de1 - Telegram Web" || got.Format != plugin.FormatTelegramLink {
		t.Errorf("name %q, format %v", got.Name, got.Format)
	}
	in.DisplayName = "alice"
	if got, _ := p.Render(in); got.Name != "alice - Telegram Web" {
		t.Errorf("display name: %q", got.Name)
	}
	// the same key makes the Fake TLS link of the same person
	fake, _ := p.Render(protocols.RenderInput{Format: plugin.FormatTelegramLink, Settings: json.RawMessage(`{"port":443}`), Inbound: inbound("203.0.113.9", "tg.example.com", 443), Secret: issued.Secret})
	if fu, _ := url.Parse(string(fake.Data)); fu == nil || fu.Host != "proxy" || fu.Query().Get("secret") != "ee"+issued.Secret+hex.EncodeToString([]byte("tg.example.com")) {
		t.Errorf("fake tls link %q", fake.Data)
	}
	// the profile editor's preview has no key: the secret is hidden
	prev := in
	prev.Secret, prev.MaskSecrets = "", true
	if got, ok := p.Render(prev); !ok || !strings.HasPrefix(string(got.Data), "tg://webproxy?") || strings.Contains(string(got.Data), issued.Secret) {
		t.Errorf("preview %q, %v", got.Data, ok)
	}
	for name, mod := range map[string]func(*protocols.RenderInput){
		"no name":  func(in *protocols.RenderInput) { in.Inbound.TLSServerName = "" },
		"bad key":  func(in *protocols.RenderInput) { in.Secret = "dd" + issued.Secret },
		"no key":   func(in *protocols.RenderInput) { in.Secret = "" },
		"bad name": func(in *protocols.RenderInput) { in.Inbound.TLSServerName = "a b" },
	} {
		bad := in
		mod(&bad)
		if _, ok := p.Render(bad); ok {
			t.Errorf("%s: rendered", name)
		}
	}
	for _, f := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
		bad := in
		bad.Format = f
		if _, ok := p.Render(bad); ok {
			t.Errorf("rendered the subscription format %s", f)
		}
	}
}
