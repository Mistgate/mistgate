package hysteria2

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// notRendered is what a golden file of a row the format cannot carry holds.
const notRendered = "(not rendered)\n"

// goldenJSON compares the pretty-printed fragment (or the not-rendered marker) with testdata/<name>.
func goldenJSON(t *testing.T, name string, f plugin.Fragment, ok bool) {
	t.Helper()
	got := notRendered
	if ok {
		var buf bytes.Buffer
		if err := json.Indent(&buf, f.Data, "", "  "); err != nil {
			t.Fatalf("%s: fragment is not JSON: %v\n%s", name, err, f.Data)
		}
		got = buf.String() + "\n"
	}
	golden(t, name, got)
}

func jsonIn(t *testing.T, format plugin.ClientFormat, iv protocols.InboundView, mut func(*Settings)) protocols.RenderInput {
	return protocols.RenderInput{Format: format, Settings: settings(t, mut), Inbound: iv, Secret: "tok-DEVICE_secret_0123456789abcdefghijklmn"}
}

// jsonRows are the Hysteria2 rows of the compatibility matrix, one input each.
func jsonRows(t *testing.T, format plugin.ClientFormat) []struct {
	file string
	in   protocols.RenderInput
	want map[plugin.ClientFormat]bool // which formats carry the row
} {
	pin := strings.Repeat("ab", 32)
	hop := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	hop.HopFrom, hop.HopTo = 20000, 30000
	both := map[plugin.ClientFormat]bool{plugin.FormatXrayJSON: true, plugin.FormatSingbox: true}
	xrayOnly := map[plugin.ClientFormat]bool{plugin.FormatXrayJSON: true}
	none := map[plugin.ClientFormat]bool{}
	return []struct {
		file string
		in   protocols.RenderInput
		want map[plugin.ClientFormat]bool
	}{
		{"salamander", jsonIn(t, format, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil), both},
		{"noobfs", jsonIn(t, format, inbound(plugin.TLSAcmeDomain, "", ""), func(s *Settings) { s.Obfs = Obfs{Type: "none"} }), both},
		{"pinned", jsonIn(t, format, inbound(plugin.TLSSelfSigned, "203.0.113.10", strings.ToUpper(pin)), nil), xrayOnly},
		{"pinned_noobfs", jsonIn(t, format, inbound(plugin.TLSSelfSigned, "de2.example.com", pin), func(s *Settings) { s.Obfs = Obfs{Type: "none"} }), xrayOnly},
		{"gecko", jsonIn(t, format, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), func(s *Settings) { s.Obfs.Type = "gecko" }), none},
		{"hop", jsonIn(t, format, hop, nil), both},
		{"acme_ip", jsonIn(t, format, inbound(plugin.TLSAcmeIP, "203.0.113.10", ""), nil), both},
	}
}

// Every Hysteria2 row of the matrix in both formats: the golden file, the "ok" the matrix gives, and a JSON object
// without a tag that the cores accept.
func TestRenderJSONGolden(t *testing.T) {
	p := New()
	for _, format := range []plugin.ClientFormat{plugin.FormatXrayJSON, plugin.FormatSingbox} {
		prefix := map[plugin.ClientFormat]string{plugin.FormatXrayJSON: "xray_", plugin.FormatSingbox: "singbox_"}[format]
		for _, row := range jsonRows(t, format) {
			t.Run(prefix+row.file, func(t *testing.T) {
				f, ok := p.Render(row.in)
				if ok != row.want[format] {
					t.Fatalf("rendered = %v, want %v", ok, row.want[format])
				}
				if ok && f.Format != format {
					t.Errorf("fragment format %q", f.Format)
				}
				goldenJSON(t, prefix+row.file+".json", f, ok)
				if !ok {
					return
				}
				var o map[string]any
				if err := json.Unmarshal(f.Data, &o); err != nil {
					t.Fatalf("not one JSON object: %v\n%s", err, f.Data)
				}
				if _, has := o["tag"]; has {
					t.Errorf("the fragment carries a tag: %s", f.Data)
				}
				checkWithCores(t, format, f.Data)
			})
		}
	}
}

// The panel never writes allowInsecure/insecure, and a Gecko or pin-less self-signed server is never rendered.
func TestRenderJSONNeverInsecure(t *testing.T) {
	p := New()
	for _, format := range []plugin.ClientFormat{plugin.FormatXrayJSON, plugin.FormatSingbox} {
		for _, row := range jsonRows(t, format) {
			if f, ok := p.Render(row.in); ok && strings.Contains(strings.ToLower(string(f.Data)), "insecure") {
				t.Errorf("%s %s: %s", format, row.file, f.Data)
			}
		}
		for _, pin := range []string{"", strings.Repeat("a", 63), strings.Repeat("g", 64), "aa\nbb"} {
			in := jsonIn(t, format, inbound(plugin.TLSSelfSigned, "203.0.113.10", pin), nil)
			if _, ok := p.Render(in); ok {
				t.Errorf("%s: self-signed node with pin %q must not be rendered", format, pin)
			}
		}
		for _, host := range []string{"evil.example/?x=", "a b.example", "h@evil.example", "h.example\nx", "h.example#f", `h.example","x":"`} {
			iv := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
			iv.Node.Address = host
			if _, ok := p.Render(jsonIn(t, format, iv, nil)); ok {
				t.Errorf("%s: host %q must be refused", format, host)
			}
		}
	}
}

// The values the node and the hop range put into the object are the numbers and strings the matrix says.
func TestRenderJSONFields(t *testing.T) {
	p := New()
	hop := inbound(plugin.TLSAcmeDomain, "de1.example.com", "")
	hop.HopFrom, hop.HopTo = 20000, 30000
	field := func(format plugin.ClientFormat, iv protocols.InboundView) map[string]any {
		f, ok := p.Render(jsonIn(t, format, iv, nil))
		if !ok {
			t.Fatalf("%s refused", format)
		}
		var o map[string]any
		if err := json.Unmarshal(f.Data, &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	// Xray: one port, no hop key anywhere (udphop moved between cores).
	x := field(plugin.FormatXrayJSON, hop)
	if x["settings"].(map[string]any)["port"] != float64(443) || strings.Contains(strings.ToLower(mustJSON(t, x)), "hop") {
		t.Errorf("Xray hop row: %s", mustJSON(t, x))
	}
	// sing-box: the range in "a:b" form, the interval, no server_port.
	s := field(plugin.FormatSingbox, hop)
	ports, _ := s["server_ports"].([]any)
	if len(ports) != 1 || ports[0] != "20000:30000" || s["hop_interval"] != "30s" || s["server_port"] != nil {
		t.Errorf("sing-box hop row: %s", mustJSON(t, s))
	}
	s = field(plugin.FormatSingbox, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""))
	if s["server_port"] != float64(443) || s["server_ports"] != nil || s["hop_interval"] != nil {
		t.Errorf("sing-box plain row: %s", mustJSON(t, s))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A masked preview carries no secret in either format.
func TestRenderJSONMasked(t *testing.T) {
	p := New()
	for _, c := range []struct {
		format plugin.ClientFormat
		file   string
	}{{plugin.FormatXrayJSON, "xray_masked.json"}, {plugin.FormatSingbox, "singbox_masked.json"}} {
		in := jsonIn(t, c.format, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)
		in.MaskSecrets = true
		f, ok := p.Render(in)
		if !ok || strings.Contains(string(f.Data), "DEVICE_secret") || strings.Contains(string(f.Data), "gawrgura-pw") || strings.Count(string(f.Data), protocols.MaskedSecret) != 2 {
			t.Errorf("%s masked render: %v\n%s", c.format, ok, f.Data)
		}
		goldenJSON(t, c.file, f, ok)
	}
}

// Names never enter the object, so a hostile node or display name cannot change a byte of it, and it is the name
// the assembler gets back in Fragment.Name. An assembler that sets the name as the tag keeps it byte for byte.
func TestRenderJSONHostileNames(t *testing.T) {
	p := New()
	names := []string{
		"x\"}, {\"protocol\":\"freedom\",\"tag\":\"evil", `\`, `\"`, "line\nbreak", "</script>", "\U0001F1E9\U0001F1EA emoji",
		"rtl ‮abc‬ ‏", "line sep", "null", "{a: b}", "tab\there", "- name: evil", "a\x00b",
	}
	for _, format := range []plugin.ClientFormat{plugin.FormatXrayJSON, plugin.FormatSingbox} {
		base, ok := p.Render(jsonIn(t, format, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil))
		if !ok {
			t.Fatal("base refused")
		}
		for _, name := range names {
			in := jsonIn(t, format, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)
			in.DisplayName = name
			in.Inbound.Node.Name = name
			f, ok := p.Render(in)
			if !ok || !bytes.Equal(f.Data, base.Data) || f.Name != name {
				t.Fatalf("%s %q: ok=%v name=%q\n%s", format, name, ok, f.Name, f.Data)
			}
			// What the step-3 assembler does: decode, set the tag, encode, decode again.
			var o map[string]json.RawMessage
			if err := json.Unmarshal(f.Data, &o); err != nil {
				t.Fatal(err)
			}
			tag, _ := json.Marshal(f.Name)
			o["tag"] = tag
			out, err := json.Marshal([]any{o, map[string]string{"tag": "direct"}})
			if err != nil {
				t.Fatal(err)
			}
			var back []map[string]any
			if err := json.Unmarshal(out, &back); err != nil || len(back) != 2 || back[0]["tag"] != strings.ToValidUTF8(name, "�") {
				t.Errorf("%s %q: tag came back %q (%v) in %s", format, name, back[0]["tag"], err, out)
			}
		}
		// The fallback name when the assembler passes none.
		in := jsonIn(t, format, inbound(plugin.TLSAcmeDomain, "de1.example.com", ""), nil)
		if f, ok := p.Render(in); !ok || f.Name != "de2 · hy2 · 443" {
			t.Errorf("%s fallback name %q", format, f.Name)
		}
	}
}
