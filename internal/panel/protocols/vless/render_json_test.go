package vless

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
	renderGolden(t, name, got)
}

type jsonRow struct {
	file string
	in   func(*testing.T, plugin.ClientFormat) protocols.RenderInput
	xray bool // Xray carries the row
	sing bool // sing-box carries the row
}

func realityRow(transport string, mutate func(*Settings)) func(*testing.T, plugin.ClientFormat) protocols.RenderInput {
	return func(t *testing.T, f plugin.ClientFormat) protocols.RenderInput {
		return renderInput(t, f, transport, mutate)
	}
}

func tlsRow(transport, cdnHost string) func(*testing.T, plugin.ClientFormat) protocols.RenderInput {
	return func(t *testing.T, f plugin.ClientFormat) protocols.RenderInput {
		return tlsRenderInput(t, f, transport, cdnHost)
	}
}

// jsonRows are the VLESS rows of the compatibility matrix.
var jsonRows = []jsonRow{
	{"reality_tcp", realityRow("tcp", nil), true, true},
	{"reality_xhttp", realityRow("xhttp", nil), true, false},
	{"reality_xhttp_full", realityRow("xhttp", func(s *Settings) {
		s.Reality.ServerNames = []string{"first.example.org", "second.example.org"}
		s.Reality.ShortIDs = []string{"11111111", "ab"}
		s.Reality.Fingerprint = "firefox"
		s.XHTTP.Host = "cover.example.org"
		s.XHTTP.Mode = "packet-up"
		s.XHTTP.XPaddingBytes = "120-360"
	}), true, false},
	{"reality_self", func(t *testing.T, f plugin.ClientFormat) protocols.RenderInput { return selfRenderInput(t, f) }, true, false},
	{"tls_direct_xhttp", tlsRow("xhttp", ""), true, false},
	{"tls_cdn_xhttp", tlsRow("xhttp", "cdn.example.org"), true, false},
	{"tls_direct_ws", tlsRow("ws", ""), true, true},
	{"tls_cdn_ws", tlsRow("ws", "cdn.example.org"), true, true},
}

var jsonFormats = []struct {
	format plugin.ClientFormat
	prefix string
}{{plugin.FormatXrayJSON, "xray_"}, {plugin.FormatSingbox, "singbox_"}}

// Every VLESS row of the matrix in both formats: the golden file, the "ok" the matrix gives, a JSON object without a
// tag, and the real cores' verdict (Xray's config builder in process, sing-box binaries when present).
func TestRenderJSONGolden(t *testing.T) {
	for _, jf := range jsonFormats {
		for _, row := range jsonRows {
			t.Run(jf.prefix+row.file, func(t *testing.T) {
				f, ok := New().Render(row.in(t, jf.format))
				want := row.xray
				if jf.format == plugin.FormatSingbox {
					want = row.sing
				}
				if ok != want {
					t.Fatalf("rendered = %v, want %v", ok, want)
				}
				goldenJSON(t, jf.prefix+row.file+".json", f, ok)
				if !ok {
					return
				}
				if f.Format != jf.format {
					t.Errorf("fragment format %q", f.Format)
				}
				var o map[string]any
				if err := json.Unmarshal(f.Data, &o); err != nil {
					t.Fatalf("not one JSON object: %v\n%s", err, f.Data)
				}
				if _, has := o["tag"]; has {
					t.Errorf("the fragment carries a tag: %s", f.Data)
				}
				checkWithCores(t, jf.format, f.Data)
			})
		}
	}
}

// The XHTTP fields: the padding is written only when it differs from the default, as the direct xPaddingBytes key.
func TestRenderXrayXHTTPFields(t *testing.T) {
	get := func(row string) map[string]any {
		for _, r := range jsonRows {
			if r.file != row {
				continue
			}
			f, ok := New().Render(r.in(t, plugin.FormatXrayJSON))
			if !ok {
				t.Fatalf("%s refused", row)
			}
			var o struct {
				StreamSettings struct {
					Network       string         `json:"network"`
					Security      string         `json:"security"`
					XHTTPSettings map[string]any `json:"xhttpSettings"`
				} `json:"streamSettings"`
				Settings struct {
					Vnext []struct {
						Users []map[string]any `json:"users"`
					} `json:"vnext"`
				} `json:"settings"`
			}
			if err := json.Unmarshal(f.Data, &o); err != nil || o.StreamSettings.Network != "xhttp" {
				t.Fatalf("%s: %v %s", row, err, f.Data)
			}
			if u := o.Settings.Vnext[0].Users[0]; u["flow"] != nil || u["encryption"] != "none" {
				t.Errorf("%s: XHTTP user %v must have no flow", row, u)
			}
			return o.StreamSettings.XHTTPSettings
		}
		t.Fatalf("no row %s", row)
		return nil
	}
	if x := get("reality_xhttp"); x["xPaddingBytes"] != nil || x["extra"] != nil || x["mode"] != "auto" || x["path"] != "/q8x2kd7w" || x["host"] != nil {
		t.Errorf("default XHTTP = %v", x)
	}
	if x := get("reality_xhttp_full"); x["xPaddingBytes"] != "120-360" || x["extra"] != nil || x["mode"] != "packet-up" || x["host"] != "cover.example.org" {
		t.Errorf("full XHTTP = %v", x)
	}
	if x := get("tls_cdn_xhttp"); x["mode"] != "packet-up" || x["host"] != "cdn.example.org" {
		t.Errorf("CDN XHTTP = %v", x)
	}
}

// REALITY needs uTLS in sing-box; the fingerprint and the Vision flow are the profile's.
func TestRenderSingboxRealityNeedsUTLS(t *testing.T) {
	f, ok := New().Render(realityRow("tcp", func(s *Settings) { s.Reality.Fingerprint = "safari" })(t, plugin.FormatSingbox))
	if !ok {
		t.Fatal("refused")
	}
	var o struct {
		Flow string `json:"flow"`
		TLS  struct {
			UTLS struct {
				Enabled     bool
				Fingerprint string
			} `json:"utls"`
			Reality struct{ Enabled bool } `json:"reality"`
		} `json:"tls"`
	}
	if err := json.Unmarshal(f.Data, &o); err != nil || !o.TLS.UTLS.Enabled || o.TLS.UTLS.Fingerprint != "safari" || !o.TLS.Reality.Enabled || o.Flow != "xtls-rprx-vision" {
		t.Errorf("sing-box REALITY = %+v, %v\n%s", o, err, f.Data)
	}
}

// A hostile address, key or settings document gives no outbound in either format.
func TestRenderJSONRefusesHostileInput(t *testing.T) {
	for _, jf := range jsonFormats {
		for _, address := range []string{"evil.example/?x=", "a b.example", "h@evil.example", "h.example#f", "h.example\nx", "de1.example.com:443", `h.example","x":"`} {
			in := renderInput(t, jf.format, "tcp", nil)
			in.Inbound.Node.Address = address
			if _, ok := New().Render(in); ok {
				t.Errorf("%s accepted non-host address %q", jf.format, address)
			}
		}
		for _, key := range []string{"", "bad key", "!", strings.Repeat("a", 44)} {
			in := renderInput(t, jf.format, "tcp", nil)
			in.InboundPublic = json.RawMessage(`{"public_key":` + fmtQuote(key) + `}`)
			if _, ok := New().Render(in); ok {
				t.Errorf("%s accepted public key %q", jf.format, key)
			}
		}
		in := renderInput(t, jf.format, "tcp", nil)
		in.Settings = json.RawMessage(`{"port":`)
		if _, ok := New().Render(in); ok {
			t.Errorf("%s accepted broken settings", jf.format)
		}
	}
}

// A masked preview carries no UUID in either format.
func TestRenderJSONMasked(t *testing.T) {
	for _, jf := range jsonFormats {
		in := renderInput(t, jf.format, "tcp", nil)
		in.MaskSecrets = true
		f, ok := New().Render(in)
		if !ok || strings.Contains(string(f.Data), "8a3b5c2d") || strings.Count(string(f.Data), protocols.MaskedSecret) != 1 {
			t.Errorf("%s masked render: %v\n%s", jf.format, ok, f.Data)
		}
		goldenJSON(t, jf.prefix+"masked.json", f, ok)
	}
}

// Names never enter the object, so a hostile node or display name cannot change a byte of it, and it is the name the
// assembler gets back in Fragment.Name. An assembler that sets the name as the tag keeps it byte for byte.
func TestRenderJSONHostileNames(t *testing.T) {
	names := []string{
		"x\"}, {\"protocol\":\"freedom\",\"tag\":\"evil", `\`, `\"`, "line\nbreak", "</script>", "\U0001F1E9\U0001F1EA emoji",
		"rtl ‮abc‬ ‏", "line sep", "null", "{a: b}", "tab\there", "- name: evil", "a\x00b",
	}
	for _, jf := range jsonFormats {
		base, ok := New().Render(renderInput(t, jf.format, "tcp", nil))
		if !ok {
			t.Fatal("base refused")
		}
		for _, name := range names {
			in := renderInput(t, jf.format, "tcp", nil)
			in.DisplayName = name
			in.Inbound.Node.Name = name
			f, ok := New().Render(in)
			if !ok || !bytes.Equal(f.Data, base.Data) || f.Name != name {
				t.Fatalf("%s %q: ok=%v name=%q\n%s", jf.format, name, ok, f.Name, f.Data)
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
			if err := json.Unmarshal(out, &back); err != nil || len(back) != 2 || back[0]["tag"] != name {
				t.Errorf("%s %q: tag came back %q (%v) in %s", jf.format, name, back[0]["tag"], err, out)
			}
		}
		// The fallback name when the assembler passes none is the one the other formats use.
		if f, ok := New().Render(renderInput(t, jf.format, "tcp", nil)); !ok || f.Name != "de1 · vless · 443" {
			t.Errorf("%s fallback name %q", jf.format, f.Name)
		}
	}
}
