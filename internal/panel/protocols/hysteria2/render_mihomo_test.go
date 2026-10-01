package hysteria2

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

func mihomoIn(t *testing.T, iv protocols.InboundView, mut func(*Settings)) protocols.RenderInput {
	return protocols.RenderInput{Format: plugin.FormatMihomo, Settings: settings(t, mut), Inbound: iv, Secret: "tok-DEVICE_secret_0123456789abcdefghijklmn"}
}

// The Mihomo proxy of Hysteria2: salamander, gecko with a hop range, a pinned self-signed node, no obfuscation.
func TestRenderMihomoGolden(t *testing.T) {
	p := New()
	pin := strings.Repeat("ab", 32)
	hop := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	hop.HopFrom, hop.HopTo = 20000, 30000
	for _, c := range []struct {
		file string
		in   protocols.RenderInput
	}{
		{"mihomo_salamander.yaml", mihomoIn(t, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)},
		{"mihomo_gecko_hop.yaml", mihomoIn(t, hop, func(s *Settings) { s.Obfs.Type = "gecko" })},
		{"mihomo_pinned.yaml", mihomoIn(t, inbound(plugin.TLSSelfSigned, "203.0.113.10", strings.ToUpper(pin)), nil)},
		{"mihomo_noobfs.yaml", mihomoIn(t, inbound(plugin.TLSAcmeDomain, "", ""), func(s *Settings) { s.Obfs = Obfs{Type: "none"} })},
	} {
		f, ok := p.Render(c.in)
		if !ok || f.Format != plugin.FormatMihomo {
			t.Fatalf("%s: Render refused", c.file)
		}
		golden(t, c.file, string(f.Data))
		var back []map[string]any
		if err := yaml.Unmarshal(f.Data, &back); err != nil || len(back) != 1 || back[0]["type"] != "hysteria2" {
			t.Errorf("%s does not parse as one proxy: %v %v", c.file, back, err)
		}
	}
}

// The proxy is a YAML list of one element and a node or user name cannot break out of its scalar.
func TestRenderMihomoHostileNames(t *testing.T) {
	p := New()
	for _, name := range []string{
		"x\"\n  - name: evil", "- name: evil", "</script>", "null", "yes", "{a: b}", "emoji \U0001F1E9\U0001F1EA", "a: b #c", "'q'", "tab\there", " ", "",
	} {
		in := mihomoIn(t, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)
		in.DisplayName = name
		f, ok := p.Render(in)
		if !ok {
			t.Fatalf("%q refused", name)
		}
		var back []map[string]any
		if err := yaml.Unmarshal(f.Data, &back); err != nil || len(back) != 1 {
			t.Fatalf("%q: %v %v\n%s", name, back, err, f.Data)
		}
		want := name
		if name == "" {
			want = "de2 · hy2 · 443"
		}
		if back[0]["name"] != want {
			t.Errorf("name %q came back as %q", name, back[0]["name"])
		}
	}
	// Hostile hosts and a self-signed node without a (valid) pin give no proxy.
	for _, host := range []string{"evil.example/?x=", "a b.example", "h@evil.example", "h.example\nx", "h.example#f"} {
		iv := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
		iv.Node.Address = host
		if _, ok := p.Render(mihomoIn(t, iv, nil)); ok {
			t.Errorf("host %q must be refused", host)
		}
	}
	for _, pin := range []string{"", strings.Repeat("a", 63), strings.Repeat("g", 64), "aa\nbb"} {
		if _, ok := p.Render(mihomoIn(t, inbound(plugin.TLSSelfSigned, "203.0.113.10", pin), nil)); ok {
			t.Errorf("self-signed node with pin %q must give no proxy", pin)
		}
	}
}

// A masked preview carries no secret.
func TestRenderMihomoMasked(t *testing.T) {
	in := mihomoIn(t, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)
	in.MaskSecrets = true
	f, ok := New().Render(in)
	if !ok || strings.Contains(string(f.Data), "DEVICE_secret") || strings.Contains(string(f.Data), "gawrgura-pw") || !strings.Contains(string(f.Data), protocols.MaskedSecret) {
		t.Errorf("masked render: %v\n%s", ok, f.Data)
	}
}
