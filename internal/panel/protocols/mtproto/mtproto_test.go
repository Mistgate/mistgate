package mtproto

import (
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestValidate(t *testing.T) {
	p := New()
	for _, raw := range []string{
		`{"port":443}`,
		`{"port":8443}`,
	} {
		if errs := p.Validate(json.RawMessage(raw)); len(errs) != 0 {
			t.Fatalf("%s: %+v", raw, errs)
		}
	}
	for _, raw := range []string{
		`{"port":0}`,
		`{"port":70000}`,
		`{"port":"x"}`,
		`{}`,
	} {
		if errs := p.Validate(json.RawMessage(raw)); len(errs) == 0 {
			t.Fatalf("%s accepted", raw)
		}
	}
}

func TestBuildInbound(t *testing.T) {
	p := New()
	spec, err := p.BuildInbound(protocols.InboundInput{
		Profile: protocols.ProfileView{Settings: json.RawMessage(`{"port":443}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Listen.Network != "tcp" || spec.Listen.Port != 443 || spec.Egress != "direct" {
		t.Fatalf("spec %+v", spec)
	}
	var ns NodeSettings
	if err := json.Unmarshal(spec.Settings, &ns); err != nil || ns.SNI != defaultSNI {
		t.Fatalf("settings %s: %v", spec.Settings, err)
	}
	spec, err = p.BuildInbound(protocols.InboundInput{
		Profile:      protocols.ProfileView{Settings: json.RawMessage(`{"port":443}`)},
		PortOverride: 9443,
	})
	if err != nil || spec.Listen.Port != 9443 {
		t.Fatalf("override %+v, %v", spec, err)
	}
}

func TestIssueAndRender(t *testing.T) {
	p := New()
	settings := json.RawMessage(`{"port":443}`)
	issued, err := p.IssueCredential(protocols.IssueInput{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Secret, "ee") || len(issued.Secret) != 2+32+2*len("www.google.com") {
		t.Fatalf("secret %q", issued.Secret)
	}
	var data struct{ Key string }
	if err := json.Unmarshal(issued.NodeData, &data); err != nil {
		t.Fatal(err)
	}
	if raw, err := hex.DecodeString(data.Key); err != nil || len(raw) != keyLen {
		t.Fatalf("key %q: %v", data.Key, err)
	}
	got, ok := p.Render(protocols.RenderInput{
		Format:   plugin.FormatURIList,
		Settings: settings,
		Inbound:  protocols.InboundView{Port: 443, Node: protocols.NodeView{Address: "vpn.example.com", Name: "de1"}},
		Secret:   issued.Secret,
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
	if u.Query().Get("server") != "vpn.example.com" || u.Query().Get("port") != "443" || u.Query().Get("secret") != issued.Secret {
		t.Fatalf("query %v", u.Query())
	}
	if got.Name != "de1 - Telegram - 443" {
		t.Fatalf("name %q", got.Name)
	}
	if _, ok := p.Render(protocols.RenderInput{Format: plugin.FormatMihomo, Settings: settings}); ok {
		t.Fatal("rendered an unsupported format")
	}
}
