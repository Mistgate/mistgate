package vless

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

var updateGolden = flag.Bool("update", false, "rewrite VLESS golden files")

func vlessInbound() protocols.InboundView {
	return protocols.InboundView{
		ID: "inb_1", Node: protocols.NodeView{ID: "nod_de1", Name: "de1", Address: "de1.example.com"}, Port: 443,
	}
}

func testPublicKey(t *testing.T) json.RawMessage {
	t.Helper()
	private := make([]byte, 32)
	for i := range private {
		private[i] = byte(i + 1)
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(struct {
		PublicKey string `json:"public_key"`
	}{PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	return public
}

func renderInput(t *testing.T, format plugin.ClientFormat, transport string, mutate func(*Settings)) protocols.RenderInput {
	t.Helper()
	settings := validSettings(t, func(s *Settings) {
		s.Transport = transport
		if mutate != nil {
			mutate(s)
		}
	})
	return protocols.RenderInput{
		Format: format, Settings: settings, Inbound: vlessInbound(), InboundPublic: testPublicKey(t),
		DeviceID: "dev_123", Secret: "8a3b5c2d-1e4f-4a6b-8c0d-123456789abc",
	}
}

func renderGolden(t *testing.T, file, got string) {
	t.Helper()
	path := filepath.Join("testdata", file)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
		t.Errorf("%s differs:\n got:\n%s\nwant:\n%s", file, got, want)
	}
}

// goldenCases are the combinations a client can get: each transport with defaults, XHTTP with every optional field set
// (two names and ids, so the pick by device shows), and a node whose name is hostile.
var goldenCases = []struct {
	file      string
	transport string
	mutate    func(*Settings)
	nodeName  string
}{
	{file: "tcp_reality", transport: "tcp"},
	{file: "xhttp_reality", transport: "xhttp"},
	{file: "xhttp_full", transport: "xhttp", mutate: func(s *Settings) {
		s.Reality.ServerNames = []string{"first.example.org", "second.example.org"}
		s.Reality.ShortIDs = []string{"11111111", "ab"}
		s.Reality.Fingerprint = "firefox"
		s.XHTTP.Host = "cover.example.org"
		s.XHTTP.Mode = "packet-up"
		s.XHTTP.XPaddingBytes = "120-360"
	}},
	{file: "hostile_name", transport: "xhttp", nodeName: "x\"\n  - type: evil #c: {a}"},
}

func goldenInput(t *testing.T, format plugin.ClientFormat, tc int) protocols.RenderInput {
	c := goldenCases[tc]
	in := renderInput(t, format, c.transport, c.mutate)
	if c.nodeName != "" {
		in.Inbound.Node.Name = c.nodeName
		in.DisplayName = c.nodeName + " · vless · 443" // a subscription names the proxy; the YAML must hold it as one scalar
	}
	return in
}

func TestRenderURIGolden(t *testing.T) {
	for i, tc := range goldenCases {
		t.Run(tc.file, func(t *testing.T) {
			frag, ok := New().Render(goldenInput(t, plugin.FormatURIList, i))
			if !ok || frag.Format != plugin.FormatURIList || strings.ContainsAny(string(frag.Data), "\r\n") {
				t.Fatalf("URI render refused or multiline: %+v", frag)
			}
			renderGolden(t, "uri_"+tc.file+".txt", string(frag.Data)+"\n")
			u, err := url.Parse(string(frag.Data))
			wantName := "de1"
			if tc.nodeName != "" {
				wantName = tc.nodeName
			}
			if err != nil || u.Scheme != "vless" || u.User.Username() != "8a3b5c2d-1e4f-4a6b-8c0d-123456789abc" ||
				u.Hostname() != "de1.example.com" || u.Port() != "443" || u.Query().Get("security") != "reality" || u.Query().Get("sid") == "" || u.Query().Get("sni") == "" ||
				u.Fragment != wantName+" · vless · 443" || frag.Name != wantName+" · vless · 443" {
				t.Errorf("parsed VLESS URI = %+v, %v", u, err)
			}
			if tc.transport == "tcp" && (u.Query().Get("flow") != "xtls-rprx-vision" || u.Query().Get("type") != "tcp" || u.Query().Has("path") || u.Query().Has("mode") || u.Query().Has("extra")) {
				t.Errorf("TCP query = %v", u.Query())
			}
			if tc.file == "xhttp_reality" && (u.Query().Get("mode") != "auto" || u.Query().Get("path") != "/q8x2kd7w" || u.Query().Has("extra") || u.Query().Has("host") || u.Query().Has("flow")) {
				t.Errorf("XHTTP query = %v", u.Query())
			}
		})
	}
}

func TestRenderMihomoGolden(t *testing.T) {
	for i, tc := range goldenCases {
		t.Run(tc.file, func(t *testing.T) {
			frag, ok := New().Render(goldenInput(t, plugin.FormatMihomo, i))
			if !ok || frag.Format != plugin.FormatMihomo {
				t.Fatalf("Mihomo render refused: %+v", frag)
			}
			renderGolden(t, "mihomo_"+tc.file+".yaml", string(frag.Data))
			var proxies []map[string]any
			if err := yaml.Unmarshal(frag.Data, &proxies); err != nil || len(proxies) != 1 {
				t.Fatalf("Mihomo output is not one proxy: %v %v", proxies, err)
			}
			proxy := proxies[0]
			if proxy["type"] != "vless" || proxy["server"] != "de1.example.com" || proxy["port"] != 443 || proxy["tls"] != true ||
				proxy["skip-cert-verify"] != false || proxy["packet-encoding"] != "xudp" || proxy["udp"] != true {
				t.Errorf("Mihomo proxy = %#v", proxy)
			}
			if _, ok := proxy["reality-opts"].(map[string]any); !ok {
				t.Errorf("Mihomo reality options = %#v", proxy["reality-opts"])
			}
			if tc.transport == "xhttp" {
				if _, ok := proxy["xhttp-opts"].(map[string]any); !ok || proxy["network"] != "xhttp" || proxy["flow"] != nil {
					t.Errorf("Mihomo XHTTP options = %#v", proxy)
				}
			} else if proxy["network"] != "tcp" || proxy["flow"] != "xtls-rprx-vision" || proxy["xhttp-opts"] != nil {
				t.Errorf("Mihomo TCP fields = %#v", proxy)
			}
		})
	}
}

func TestRenderSelectionAndNonDefaults(t *testing.T) {
	in := renderInput(t, plugin.FormatURIList, "xhttp", func(s *Settings) {
		s.Reality.ServerNames = []string{"first.example.org", "second.example.org"}
		s.Reality.ShortIDs = []string{"11111111", "22222222"}
		s.XHTTP.Host = "cover.example.org"
		s.XHTTP.XPaddingBytes = "120-360"
	})
	frag, ok := New().Render(in)
	if !ok {
		t.Fatal("render refused")
	}
	u, err := url.Parse(string(frag.Data))
	if err != nil {
		t.Fatal(err)
	}
	idx := int(fnv32(in.DeviceID) % 2)
	var s Settings
	if err := json.Unmarshal(in.Settings, &s); err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("sni") != s.Reality.ServerNames[idx] || u.Query().Get("sid") != s.Reality.ShortIDs[idx] ||
		u.Query().Get("host") != "cover.example.org" || u.Query().Get("extra") != `{"xPaddingBytes":"120-360"}` {
		t.Errorf("selected query = %v, index %d", u.Query(), idx)
	}
	keys := make([]string, 0, len(u.Query()))
	for key := range u.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"encryption", "extra", "fp", "host", "mode", "path", "pbk", "security", "sid", "sni", "spx", "type"}) {
		t.Errorf("query keys not sorted alphabetically: %v", keys)
	}
	if u.Query().Get("spx") != spx(in.DeviceID) {
		t.Errorf("spx = %q, want %q", u.Query().Get("spx"), spx(in.DeviceID))
	}
}

func TestRenderHostileNamesAndAddresses(t *testing.T) {
	for _, name := range []string{"x\"\n  - type: evil", "- name: evil", "</script>", "null", "yes", "{a: b}", "emoji 😀", "a: b #c", "'q'", "tab\there", "", "line\u2028sep"} {
		t.Run("uri_"+name, func(t *testing.T) {
			in := renderInput(t, plugin.FormatURIList, "tcp", nil)
			in.Inbound.Node.Name = name
			frag, ok := New().Render(in)
			if !ok || strings.ContainsAny(string(frag.Data), "\r\n") {
				t.Fatalf("hostile URI name refused or injected a line: %q %s", name, frag.Data)
			}
			u, err := url.Parse(string(frag.Data))
			want := name
			if want == "" {
				want = "de1.example.com"
			}
			want += " · vless · 443"
			if err != nil || u.Fragment != want {
				t.Errorf("URI fragment = %q, %v; want %q", u.Fragment, err, want)
			}
		})
		t.Run("yaml_"+name, func(t *testing.T) {
			in := renderInput(t, plugin.FormatMihomo, "xhttp", nil)
			in.DisplayName = name
			frag, ok := New().Render(in)
			if !ok {
				t.Fatalf("hostile Mihomo name refused: %q", name)
			}
			var proxies []map[string]any
			if err := yaml.Unmarshal(frag.Data, &proxies); err != nil || len(proxies) != 1 {
				t.Fatalf("hostile Mihomo name altered YAML: %v %v\n%s", proxies, err, frag.Data)
			}
			want := name
			if want == "" {
				want = "de1 · vless · 443"
			}
			if proxies[0]["name"] != want {
				t.Errorf("name = %#v, want %q", proxies[0]["name"], want)
			}
		})
	}
	for _, address := range []string{"evil.example/?x=", "a b.example", "h@evil.example", "h.example#f", "h.example\nx", "de1.example.com:443"} {
		in := renderInput(t, plugin.FormatURIList, "tcp", nil)
		in.Inbound.Node.Address = address
		if _, ok := New().Render(in); ok {
			t.Errorf("URI renderer accepted non-host address %q", address)
		}
		in.Format = plugin.FormatMihomo
		if _, ok := New().Render(in); ok {
			t.Errorf("Mihomo renderer accepted non-host address %q", address)
		}
	}
	for _, key := range []string{"", "bad key", "!", strings.Repeat("a", 44)} {
		in := renderInput(t, plugin.FormatURIList, "tcp", nil)
		in.InboundPublic = json.RawMessage(`{"public_key":` + fmtQuote(key) + `}`)
		if _, ok := New().Render(in); ok {
			t.Errorf("renderer accepted public key %q", key)
		}
	}
}

func fmtQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
