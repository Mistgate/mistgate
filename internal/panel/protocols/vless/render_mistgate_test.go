package vless

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The Mistgate format of VLESS: the explicit settings the first-party client reads from sync. The goldens are pretty-printed for
// review; the wire form is compact.

type mistgateCase struct {
	file string
	in   protocols.RenderInput
}

func mistgateCases(t *testing.T, format plugin.ClientFormat) []mistgateCase {
	t.Helper()
	cases := []mistgateCase{
		{"tcp_reality", renderInput(t, format, "tcp", nil)},
		{"xhttp_reality", renderInput(t, format, "xhttp", nil)},
		{"xhttp_full", renderInput(t, format, "xhttp", func(s *Settings) {
			s.Reality.ServerNames = []string{"first.example.org", "second.example.org"}
			s.Reality.ShortIDs = []string{"11111111", "ab"}
			s.Reality.Fingerprint = "firefox"
			s.XHTTP.Host = "cover.example.org"
			s.XHTTP.Mode = "packet-up"
			s.XHTTP.XPaddingBytes = "120-360"
		})},
		{"tls_direct_xhttp", tlsRenderInput(t, format, "xhttp", "")},
		{"tls_cdn_xhttp", tlsRenderInput(t, format, "xhttp", "cdn.example.org")},
		{"tls_direct_ws", tlsRenderInput(t, format, "ws", "")},
		{"tls_cdn_ws", tlsRenderInput(t, format, "ws", "cdn.example.org")},
	}
	for i := range cases {
		cases[i].in.Capabilities = []string{"v=1", "hysteria2=2", "vless=1"} // the client announces what it can use
	}
	return cases
}

func mistgateDoc(t *testing.T, in protocols.RenderInput) (map[string]any, string) {
	t.Helper()
	f, ok := New().Render(in)
	if !ok || f.Format != plugin.FormatMistgate {
		t.Fatal("Render refused")
	}
	var doc map[string]any
	if err := json.Unmarshal(f.Data, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, f.Data)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, f.Data, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	return doc, pretty.String()
}

func TestRenderMistgateGolden(t *testing.T) {
	for _, c := range mistgateCases(t, plugin.FormatMistgate) {
		_, pretty := mistgateDoc(t, c.in)
		renderGolden(t, "mistgate_"+c.file+".json", pretty)
	}
}

func TestRenderMistgateFields(t *testing.T) {
	cases := map[string]protocols.RenderInput{}
	for _, c := range mistgateCases(t, plugin.FormatMistgate) {
		cases[c.file] = c.in
	}
	has := func(doc map[string]any, keys ...string) string {
		var present []string
		for _, k := range keys {
			if _, ok := doc[k]; ok {
				present = append(present, k)
			}
		}
		return strings.Join(present, ",")
	}

	tcp, _ := mistgateDoc(t, cases["tcp_reality"])
	if tcp["transport"] != "raw" || tcp["flow"] != "xtls-rprx-vision" || tcp["security"] != "reality" || tcp["server"] != "de1.example.com" || tcp["port"] != float64(443) ||
		tcp["uuid"] != "8a3b5c2d-1e4f-4a6b-8c0d-123456789abc" || tcp["sni"] != "www.example.org" || tcp["fingerprint"] != "chrome" {
		t.Errorf("raw+reality: %v", tcp)
	}
	if got := has(tcp, "public_key", "short_id", "spider_x"); got != "public_key,short_id,spider_x" {
		t.Errorf("reality fields: %q", got)
	}
	if got := has(tcp, "path", "host", "mode", "x_padding_bytes", "alpn"); got != "" {
		t.Errorf("raw+reality carries transport or TLS fields: %q", got)
	}

	xh, _ := mistgateDoc(t, cases["xhttp_reality"])
	if xh["transport"] != "xhttp" || xh["path"] != "/q8x2kd7w" || xh["mode"] != "auto" || xh["x_padding_bytes"] != "100-1000" {
		t.Errorf("xhttp: %v", xh)
	}
	if got := has(xh, "flow", "host", "alpn"); got != "" {
		t.Errorf("xhttp+reality: %q", got)
	}

	full, _ := mistgateDoc(t, cases["xhttp_full"])
	if full["host"] != "cover.example.org" || full["mode"] != "packet-up" || full["x_padding_bytes"] != "120-360" || full["fingerprint"] != "firefox" {
		t.Errorf("xhttp full: %v", full)
	}

	direct, _ := mistgateDoc(t, cases["tls_direct_xhttp"])
	if direct["security"] != "tls" || direct["sni"] != "origin.example.com" || direct["host"] != "origin.example.com" || direct["server"] != "de1.example.com" {
		t.Errorf("tls direct: %v", direct)
	}
	if alpn, _ := direct["alpn"].([]any); len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Errorf("tls alpn: %v", direct["alpn"])
	}
	if got := has(direct, "public_key", "short_id", "spider_x", "flow"); got != "" {
		t.Errorf("tls carries REALITY fields: %q", got)
	}

	cdn, _ := mistgateDoc(t, cases["tls_cdn_xhttp"])
	if cdn["server"] != "cdn.example.org" || cdn["sni"] != "cdn.example.org" || cdn["host"] != "cdn.example.org" || cdn["port"] != float64(443) || cdn["mode"] != "packet-up" {
		t.Errorf("tls cdn: %v", cdn)
	}

	ws, _ := mistgateDoc(t, cases["tls_direct_ws"])
	if ws["transport"] != "ws" || ws["path"] != "/ws" || ws["alpn"].([]any)[0] != "http/1.1" {
		t.Errorf("ws: %v", ws)
	}
	if got := has(ws, "mode", "x_padding_bytes", "host"); got != "" {
		t.Errorf("ws without a host carries %q", got)
	}
	if wsCDN, _ := mistgateDoc(t, cases["tls_cdn_ws"]); wsCDN["host"] != "cdn.example.org" || wsCDN["server"] != "cdn.example.org" {
		t.Errorf("ws cdn: %v", wsCDN)
	}
}

// The address, port, names, ids and keys are the ones the Xray JSON format carries for the same input: one source.
func TestRenderMistgateAgreesWithXray(t *testing.T) {
	mg, xr := mistgateCases(t, plugin.FormatMistgate), mistgateCases(t, plugin.FormatXrayJSON)
	for i, c := range mg {
		doc, _ := mistgateDoc(t, c.in)
		f, ok := New().Render(xr[i].in)
		if !ok {
			t.Fatalf("%s: Xray refused", c.file)
		}
		var x xrayVless
		if err := json.Unmarshal(f.Data, &x); err != nil {
			t.Fatal(err)
		}
		vn, st := x.Settings.Vnext[0], x.StreamSettings
		if doc["server"] != vn.Address || doc["port"] != float64(vn.Port) || doc["uuid"] != vn.Users[0].ID || doc["security"] != st.Security {
			t.Errorf("%s: endpoint %v vs %+v", c.file, doc, vn)
		}
		if r := st.RealitySettings; r != nil {
			if doc["sni"] != r.ServerName || doc["public_key"] != r.PublicKey || doc["short_id"] != r.ShortID || doc["spider_x"] != r.SpiderX || doc["fingerprint"] != r.Fingerprint {
				t.Errorf("%s: reality %v vs %+v", c.file, doc, r)
			}
		}
		if tl := st.TLSSettings; tl != nil && (doc["sni"] != tl.ServerName || doc["fingerprint"] != tl.Fingerprint) {
			t.Errorf("%s: tls %v vs %+v", c.file, doc, tl)
		}
		if x := st.XHTTPSettings; x != nil && (doc["path"] != x.Path || doc["mode"] != x.Mode || doc["host"] != nilIfEmpty(x.Host)) {
			t.Errorf("%s: xhttp %v vs %+v", c.file, doc, x)
		}
		if w := st.WSSettings; w != nil && (doc["path"] != w.Path || doc["host"] != nilIfEmpty(w.Host)) {
			t.Errorf("%s: ws %v vs %+v", c.file, doc, w)
		}
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// A masked secret is masked here too, and a document that no client could use is refused rather than half written.
func TestRenderMistgateMaskedAndRefused(t *testing.T) {
	in := renderInput(t, plugin.FormatMistgate, "xhttp", nil)
	in.Capabilities = []string{"vless=1"}
	in.MaskSecrets = true
	if doc, _ := mistgateDoc(t, in); doc["uuid"] != protocols.MaskedSecret {
		t.Errorf("uuid = %v, want masked", doc["uuid"])
	}
	in = renderInput(t, plugin.FormatMistgate, "xhttp", nil)
	in.Capabilities = []string{"v=1", "hysteria2=2", "vless=1"}
	in.InboundPublic = nil // no REALITY public key yet
	if _, ok := New().Render(in); ok {
		t.Error("rendered without the REALITY public key")
	}
	in = renderInput(t, plugin.FormatMistgate, "xhttp", nil)
	in.Capabilities = []string{"vless=1"}
	in.Inbound.Node.Address = "not a host"
	if _, ok := New().Render(in); ok {
		t.Error("rendered for an address that is neither a host name nor an IP")
	}
}

// Only a client that announced VLESS gets the path: not one that announced nothing, another version or other protocols.
func TestRenderMistgateNeedsTheCapability(t *testing.T) {
	in := renderInput(t, plugin.FormatMistgate, "xhttp", nil)
	for name, caps := range map[string][]string{"none": nil, "v only": {"v=1"}, "other protocols": {"v=1", "hysteria2=2", "awg=2.0"}, "another version": {"vless=2"}, "a prefix": {"vless1=1"}} {
		in.Capabilities = caps
		if _, ok := New().Render(in); ok {
			t.Errorf("%s: rendered for a client that did not announce VLESS", name)
		}
	}
	in.Capabilities = []string{"v=1", "vless=1"}
	if _, ok := New().Render(in); !ok {
		t.Error("refused a client that announced VLESS")
	}
}
