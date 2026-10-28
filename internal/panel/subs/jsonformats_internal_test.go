package subs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
)

// The Xray JSON array and the sing-box profile, assembled from the plugins' own golden outbounds (one source of truth for the
// outbound text). Golden documents are stored indented; the handler writes them compact.

var (
	xrayFragments = []string{
		"../protocols/hysteria2/testdata/xray_salamander.json",
		"../protocols/vless/testdata/xray_reality_xhttp.json",
		"../protocols/vless/testdata/xray_tls_direct_ws.json",
	}
	singboxFragments = []string{
		"../protocols/hysteria2/testdata/singbox_salamander.json",
		"../protocols/vless/testdata/singbox_reality_tcp.json",
		"../protocols/vless/testdata/singbox_tls_direct_ws.json",
	}
	jsonNames = []string{"\U0001F1E9\U0001F1EA DE · Hysteria2", "\U0001F1E9\U0001F1EA DE · VLESS", "\U0001F1F3\U0001F1F1 NL · VLESS WS"}
)

func fixtures(t *testing.T, rels []string) []string {
	t.Helper()
	out := make([]string, len(rels))
	for i, rel := range rels {
		out[i] = fixture(t, rel)
	}
	return out
}

// docPresets are the DNS presets the documents are written for: nil is "no dns module" and the empty preset one no client can carry.
var docPresets = []struct {
	name   string
	preset *dns.Preset
}{
	{"standard", &dns.Preset{Name: "std", Transport: dns.KindPlain, Servers: []dns.Server{{Variant: "cloudflare/standard"}, {Variant: "google/standard"}}}},
	{"ru_split", ruSplit()},
	{"ru_split_off", func() *dns.Preset { p := ruSplit(); p.SplitDirect = false; return p }()},
	{"cn_split", cnSplit()},
	{"doh_adblock", &dns.Preset{Name: "doh", Transport: dns.KindDoH, Servers: []dns.Server{{Variant: "adguard/default"}}}},
	{"no_usable_server", &dns.Preset{Name: "none", Transport: dns.KindPlain}},
	{"no_dns", nil},
}

// goldenJSON compares an indented rendering of a compact document with testdata/<name> (rewrite with -update).
func goldenJSON(t *testing.T, name string, doc []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, doc, "", "  "); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", name, err, doc)
	}
	buf.WriteByte('\n')
	golden(t, name, buf.String())
}

var testNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func noteSamples(now time.Time) map[string]string {
	return map[string]string{
		"refusal": stateNote(access.SubView{Status: access.StatusActive, Refusal: access.RefusalDeviceLimit, DeviceLimit: 3, DevicesUsed: 3}, "en", now),
		"ended":   stateNote(access.SubView{Status: access.StatusExpired, Expires: now.Add(-24 * time.Hour)}, "en", now),
		"none":    noServersNote("en"),
	}
}

func decodeArray(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var a []map[string]any
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatalf("not an array of objects: %v\n%s", err, b)
	}
	return a
}

func decodeObject(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not an object: %v\n%s", err, b)
	}
	return m
}

func tagsOf(t *testing.T, list any) []string {
	t.Helper()
	var tags []string
	for _, o := range list.([]any) {
		tags = append(tags, o.(map[string]any)["tag"].(string))
	}
	return tags
}

// ---- Xray JSON ----

func TestXrayJSONGolden(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	for _, c := range docPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := xrayProfile{lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "xray_"+c.name+".json", b)
			checkXrayDoc(t, b, len(lines), c.preset)
		})
	}
	for name, note := range noteSamples(testNow) {
		t.Run("placeholder_"+name, func(t *testing.T) {
			// A note, or (name "none") no usable line, gives the one entry.
			p := xrayProfile{lines: lines, names: jsonNames, preset: docPresets[0].preset, note: note}
			if name == "none" {
				p = xrayProfile{lines: []string{"not json"}, none: note}
			}
			b, err := p.build()
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "xray_placeholder_"+name+".json", b)
			a := decodeArray(t, b)
			if len(a) != 1 || a[0]["remarks"] != note || a[0]["dns"] != nil {
				t.Fatalf("placeholder = %v", a)
			}
			ob := a[0]["outbounds"].([]any)
			if len(ob) != 1 || ob[0].(map[string]any)["protocol"] != "socks" || ob[0].(map[string]any)["tag"] != "proxy" {
				t.Errorf("outbounds = %v", ob)
			}
			if !strings.Contains(string(b), `"address":"127.0.0.1","port":1`) {
				t.Errorf("the placeholder does not point at 127.0.0.1:1:\n%s", b)
			}
		})
	}
}

// checkXrayDoc is what every document of the Xray format has to look like, whatever the preset.
func checkXrayDoc(t *testing.T, b []byte, servers int, preset *dns.Preset) {
	t.Helper()
	a := decodeArray(t, b)
	if len(a) != servers {
		t.Fatalf("%d configs, want %d", len(a), servers)
	}
	var d *dns.XrayDNS
	if preset != nil {
		if x, err := preset.XrayDNS(); err == nil {
			d = &x
		}
	}
	for i, c := range a {
		if c["remarks"] != jsonNames[i] {
			t.Errorf("config %d: remarks %q", i, c["remarks"])
		}
		for _, banned := range []string{"api", "stats", "policy", "metrics", "observatory"} {
			if _, ok := c[banned]; ok {
				t.Errorf("config %d has %s", i, banned)
			}
		}
		in := c["inbounds"].([]any)
		if len(in) != 1 {
			t.Fatalf("config %d: inbounds %v", i, in)
		}
		sk := in[0].(map[string]any)
		if sk["protocol"] != "socks" || sk["listen"] != "127.0.0.1" || sk["port"] != float64(10808) || sk["tag"] != "socks" {
			t.Errorf("config %d: inbound %v", i, sk)
		}
		out := c["outbounds"].([]any)
		tags := tagsOf(t, out)
		want := []string{"proxy", "direct"}
		if d != nil {
			want = append(want, "dns-out")
		}
		if !slices.Equal(tags, want) {
			t.Errorf("config %d: outbound tags %v, want %v", i, tags, want)
		}
		_, hasDNS := c["dns"]
		if hasDNS != (d != nil) {
			t.Errorf("config %d: dns section present %v, want %v", i, hasDNS, d != nil)
		}
		rules := c["routing"].(map[string]any)["rules"].([]any)
		has53 := slices.ContainsFunc(rules, func(r any) bool {
			return r.(map[string]any)["port"] == "53" && r.(map[string]any)["outboundTag"] == "dns-out"
		})
		if has53 != (d != nil) {
			t.Errorf("config %d: port 53 rule present %v", i, has53)
		}
		last := rules[len(rules)-1].(map[string]any)
		if last["outboundTag"] != "proxy" || last["network"] != "tcp,udp" {
			t.Errorf("config %d: last rule %v", i, last)
		}
		if d != nil && len(d.DirectGeo) > 0 {
			// geosite joins the direct domain rule (after the suffixes), geoip is an ip rule of its own right after it
			var domains, ips []any
			for _, r := range rules {
				if x := r.(map[string]any)["domain"]; x != nil {
					domains = x.([]any)
				}
				if x := r.(map[string]any)["ip"]; x != nil && r.(map[string]any)["outboundTag"] == "direct" && slices.Contains(x.([]any), any(d.DirectGeo[0].IP)) {
					ips = x.([]any)
				}
			}
			if len(domains) != len(d.Direct)+len(d.DirectGeo) || domains[len(domains)-1] != d.DirectGeo[len(d.DirectGeo)-1].Site || len(ips) != len(d.DirectGeo) || ips[0] != d.DirectGeo[0].IP {
				t.Errorf("config %d: direct domains %v ips %v, want %v %v", i, domains, ips, d.Direct, d.DirectGeo)
			}
		} else if d != nil && len(d.Direct) > 0 {
			var domains []any
			for _, r := range rules {
				if x := r.(map[string]any)["domain"]; x != nil {
					domains = x.([]any)
				}
			}
			if len(domains) != len(d.Direct) || domains[0] != "domain:"+d.Direct[0] {
				t.Errorf("config %d: direct domains %v, want %v", i, domains, d.Direct)
			}
		} else if strings.Contains(string(b), `"domain:`) && !strings.Contains(string(b), `"domains"`) {
			t.Errorf("config %d: a direct domain rule without a split", i)
		}
	}
}

// Names are hostile input: whatever they hold, the array keeps exactly the servers it was given, each with the name as given.
func TestXrayJSONHostileNames(t *testing.T) {
	lines := make([]string, len(hostileJSONNames))
	for i := range lines {
		lines[i] = fixture(t, xrayFragments[0])
	}
	b, err := xrayProfile{lines: lines, names: hostileJSONNames, preset: ruSplit()}.build()
	if err != nil {
		t.Fatal(err)
	}
	a := decodeArray(t, b)
	if len(a) != len(hostileJSONNames) {
		t.Fatalf("%d configs for %d servers:\n%s", len(a), len(hostileJSONNames), b)
	}
	for i, c := range a {
		want := strings.ToValidUTF8(hostileJSONNames[i], "�")
		if want == "" {
			want = "server " + strconv.Itoa(i+1) // a nameless line gets a numbered default
		}
		if c["remarks"] != want {
			t.Errorf("remarks %d = %q, want %q", i, c["remarks"], want)
		}
		if len(c) != 6 { // remarks, log, dns, inbounds, outbounds, routing: a name added no key
			t.Errorf("config %d has %d keys", i, len(c))
		}
		out := c["outbounds"].([]any)
		if len(out) != 3 || out[0].(map[string]any)["tag"] != "proxy" {
			t.Errorf("config %d: outbounds %v", i, tagsOf(t, out))
		}
	}
	if bytes.ContainsAny(b, "\n ") || bytes.Contains(b, []byte("</script>")) {
		t.Error("a raw newline, U+2028 or </script> reached the document")
	}
}

// hostileJSONNames carry what could break a JSON document or a client that reads it: a quote that closes the string and starts a
// new outbound, a backslash, a newline, markup, emoji, bidirectional marks, line separators, a NUL and invalid UTF-8.
var hostileJSONNames = []string{
	`x","protocol":"freedom","tag":"evil`, `"},{"protocol":"freedom","tag":"evil"}],"routing":{"rules":[`, `\`, `\"`, "line\nbreak", "tab\there",
	"</script><script>alert(1)</script>", "<!--", "&amp;", "\U0001F1E9\U0001F1EA \U0001F600 \U0001F468‍\U0001F469‍\U0001F467", "‮evil‬", "‏x‎",
	"  ", "nul\x00byte", "bad\xffutf8", "direct", "DIRECT", "proxy", "dns-out", "null", "true", "123", "", " ", "same", "same",
}

// A line that is not one outbound is skipped; with none left the person still gets the one entry, never an empty array.
func TestXrayJSONBrokenLines(t *testing.T) {
	good := fixture(t, xrayFragments[0])
	for _, bad := range []string{"", "x", "[]", "null", `{"type":"vless"}`, `{"protocol":5}`, `{"protocol":""}`, "{"} {
		b, err := xrayProfile{lines: []string{bad, good}, names: []string{"bad", "good"}, none: "nothing"}.build()
		if err != nil {
			t.Fatal(err)
		}
		if a := decodeArray(t, b); len(a) != 1 || a[0]["remarks"] != "good" {
			t.Errorf("%q: %v", bad, a)
		}
		b, _ = xrayProfile{lines: []string{bad}, none: "nothing"}.build()
		if a := decodeArray(t, b); len(a) != 1 || a[0]["remarks"] != "nothing" {
			t.Errorf("%q alone: %v", bad, a)
		}
	}
	// A nameless line gets a numbered default; the outbound is the plugin's, with the tag.
	b, _ := xrayProfile{lines: []string{good}}.build()
	a := decodeArray(t, b)
	if a[0]["remarks"] != "server 1" {
		t.Errorf("default name %v", a[0]["remarks"])
	}
	ob := a[0]["outbounds"].([]any)[0].(map[string]any)
	if ob["protocol"] != "hysteria" || ob["tag"] != "proxy" || ob["streamSettings"] == nil {
		t.Errorf("outbound = %v", ob)
	}
}

// A tag the plugin may have put in the fragment is replaced, not duplicated.
func TestTaggedReplacesTag(t *testing.T) {
	b, ok := tagged([]byte(`{"protocol":"vless","tag":"old","settings":{"a":[1,2]}}`), "protocol", "proxy")
	if !ok || string(b) != `{"protocol":"vless","settings":{"a":[1,2]},"tag":"proxy"}` {
		t.Errorf("%s %v", b, ok)
	}
}

// ---- sing-box ----

func TestSingboxGolden(t *testing.T) {
	lines := fixtures(t, singboxFragments)
	for _, c := range docPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "singbox_"+c.name+".json", b)
			checkSingboxDoc(t, b, jsonNames, c.preset)
		})
	}
	for name, note := range noteSamples(testNow) {
		t.Run("placeholder_"+name, func(t *testing.T) {
			p := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: docPresets[0].preset, note: note}
			if name == "none" {
				p = singboxProfile{title: "Example VPN", lines: []string{"not json"}, none: note}
			}
			b, err := p.build()
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "singbox_placeholder_"+name+".json", b)
			m := decodeObject(t, b)
			out := m["outbounds"].([]any)
			sel, off := out[0].(map[string]any), out[1].(map[string]any)
			if sel["type"] != "selector" || len(sel["outbounds"].([]any)) != 1 || sel["outbounds"].([]any)[0] != note || off["type"] != "socks" ||
				off["tag"] != note || off["server"] != "127.0.0.1" || off["server_port"] != float64(1) || m["dns"] != nil {
				t.Errorf("placeholder = %v", out)
			}
		})
	}
}

// checkSingboxDoc is what every document of the sing-box format has to look like, whatever the preset.
func checkSingboxDoc(t *testing.T, b []byte, names []string, preset *dns.Preset) {
	t.Helper()
	m := decodeObject(t, b)
	for _, banned := range []string{"experimental.clash_api", "mixed"} {
		if strings.Contains(string(b), banned) {
			t.Errorf("the profile has %s", banned)
		}
	}
	in := m["inbounds"].([]any)
	if len(in) != 1 || in[0].(map[string]any)["type"] != "tun" || in[0].(map[string]any)["auto_route"] != true {
		t.Errorf("inbounds = %v", in)
	}
	out := m["outbounds"].([]any)
	sel := out[0].(map[string]any)
	if sel["type"] != "selector" || sel["default"] != names[0] {
		t.Errorf("selector = %v", sel)
	}
	members := sel["outbounds"].([]any)
	if len(members) != len(names)+1 || members[len(members)-1] != "direct" {
		t.Errorf("selector members = %v", members)
	}
	tags := tagsOf(t, out)
	if tags[len(tags)-1] != "direct" || !slices.Equal(tags[1:len(tags)-1], names) {
		t.Errorf("outbound tags = %v", tags)
	}
	route := m["route"].(map[string]any)
	if route["final"] != sel["tag"] || route["auto_detect_interface"] != true || route["default_domain_resolver"] != "local" {
		t.Errorf("route = %v", route)
	}
	rules := route["rules"].([]any)
	if rules[0].(map[string]any)["action"] != "sniff" || rules[1].(map[string]any)["action"] != "hijack-dns" || rules[2].(map[string]any)["ip_is_private"] != true {
		t.Errorf("route rules = %v", rules)
	}
	if exp := m["experimental"].(map[string]any); exp["cache_file"].(map[string]any)["enabled"] != true || len(exp) != 1 {
		t.Errorf("experimental = %v", exp)
	}
	// The dns block always exists and always has "local", which default_domain_resolver names.
	dnsM := m["dns"].(map[string]any)
	dnsTags := tagsOf(t, dnsM["servers"])
	if !slices.Contains(dnsTags, "local") {
		t.Errorf("dns servers %v have no local", dnsTags)
	}
	var d *dns.SingboxDNS
	if preset != nil {
		if x, err := preset.SingboxDNS(sel["tag"].(string)); err == nil {
			d = &x
		}
	}
	if d == nil {
		if !slices.Equal(dnsTags, []string{"local"}) || dnsM["final"] != "local" {
			t.Errorf("fallback dns = %v", dnsM)
		}
		return
	}
	if dnsM["final"] != "remote" || dnsTags[0] != "remote" || dnsM["servers"].([]any)[0].(map[string]any)["detour"] != sel["tag"] {
		t.Errorf("dns = %v", dnsM)
	}
	direct := len(rules) > 3
	if direct != (len(d.Direct) > 0 || len(d.DirectSets) > 0) {
		t.Errorf("direct rule present %v for the split %v and sets %v", direct, d.Direct, d.DirectSets)
	}
	// the sets a rule names are defined, once each, by a remote binary rule set
	sets := map[string]bool{}
	defs, _ := route["rule_set"].([]any)
	if (len(defs) > 0) != (len(d.DirectSets) > 0) {
		t.Errorf("route.rule_set = %v for the sets %v", defs, d.DirectSets)
	}
	for _, rs := range defs {
		o := rs.(map[string]any)
		if o["type"] != "remote" || o["format"] != "binary" || o["download_detour"] != nil || o["url"] == "" || sets[o["tag"].(string)] {
			t.Errorf("rule set = %v", o)
		}
		sets[o["tag"].(string)] = true
	}
	for _, tag := range d.DirectSets {
		if !sets[tag] {
			t.Errorf("route names the rule set %s, which is not defined (%v)", tag, sets)
		}
	}
}

func TestSingboxHostileNames(t *testing.T) {
	lines := make([]string, len(hostileJSONNames))
	for i := range lines {
		lines[i] = fixture(t, singboxFragments[0])
	}
	for _, title := range []string{"Brand, Inc", `a"b\c`, "50% off", "  ", "x\ny", "direct", "selector", `"},{"type":"direct","tag":"evil"}`} {
		b, err := singboxProfile{title: title, lines: lines, names: hostileJSONNames, preset: ruSplit()}.build()
		if err != nil {
			t.Fatal(err)
		}
		m := decodeObject(t, b)
		out := m["outbounds"].([]any)
		if len(out) != len(hostileJSONNames)+2 {
			t.Fatalf("title %q: %d outbounds for %d servers:\n%s", title, len(out), len(hostileJSONNames), b)
		}
		seen := map[string]bool{}
		for i, o := range out {
			om := o.(map[string]any)
			tag, _ := om["tag"].(string)
			if tag == "" || seen[tag] {
				t.Errorf("title %q: outbound %d has the tag %q (seen %v)", title, i, tag, seen[tag])
			}
			seen[tag] = true
			switch {
			case i == 0:
				if om["type"] != "selector" {
					t.Errorf("title %q: the first outbound is %v", title, om["type"])
				}
			case i == len(out)-1:
				if om["type"] != "direct" || tag != "direct" {
					t.Errorf("title %q: the last outbound is %v", title, om)
				}
			default:
				if om["type"] != "hysteria2" || len(om) != 7 { // type, server, server_port, password, obfs, tls, tag
					t.Errorf("title %q: outbound %d changed shape: %v", title, i, om)
				}
			}
		}
		sel := out[0].(map[string]any)
		if m["route"].(map[string]any)["final"] != sel["tag"] || len(sel["outbounds"].([]any)) != len(hostileJSONNames)+1 {
			t.Errorf("title %q: selector %v", title, sel)
		}
		if bytes.ContainsAny(b, "\n ") || bytes.Contains(b, []byte("</script>")) {
			t.Errorf("title %q: a raw newline, U+2028 or </script> reached the document", title)
		}
	}
}

func TestSingboxBrokenLines(t *testing.T) {
	good := fixture(t, singboxFragments[0])
	for _, bad := range []string{"", "x", "[]", "null", `{"protocol":"vless"}`, `{"type":5}`, `{"type":""}`, "{"} {
		b, err := singboxProfile{title: "T", lines: []string{bad, good}, names: []string{"bad", "good"}, none: "nothing"}.build()
		if err != nil {
			t.Fatal(err)
		}
		out := decodeObject(t, b)["outbounds"].([]any)
		if tags := tagsOf(t, out); !slices.Equal(tags, []string{"T", "good", "direct"}) {
			t.Errorf("%q: %v", bad, tags)
		}
		b, _ = singboxProfile{title: "T", lines: []string{bad}, none: "nothing"}.build()
		if tags := tagsOf(t, decodeObject(t, b)["outbounds"].([]any)); !slices.Equal(tags, []string{"T", "nothing", "direct"}) {
			t.Errorf("%q alone: %v", bad, tags)
		}
	}
}

// The Xray and sing-box views never carry the credentials of another device: the fragments are the only place they are, and the
// assembler adds none of its own. A document holds each secret it was given, once per server, and nothing else secret-shaped.
func TestDocumentsAddNoSecrets(t *testing.T) {
	xb, _ := xrayProfile{lines: fixtures(t, xrayFragments[:1]), names: jsonNames[:1], preset: ruSplit()}.build()
	sb, _ := singboxProfile{title: "T", lines: fixtures(t, singboxFragments[:1]), names: jsonNames[:1], preset: ruSplit()}.build()
	for name, doc := range map[string][]byte{"xray": xb, "sing-box": sb} {
		if n := strings.Count(string(doc), "gawrgura-pw"); n != 1 {
			t.Errorf("%s: the obfs password appears %d times", name, n)
		}
		for _, banned := range []string{"insecure", "allowInsecure", "clash_api"} {
			if strings.Contains(string(doc), banned) {
				t.Errorf("%s: the document has %q", name, banned)
			}
		}
	}
}

// ---- 50 servers: the size of a document and the cost of writing it ----

func fifty(t testing.TB, rel string) ([]string, []string) {
	b, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatal(err)
	}
	lines, names := make([]string, 50), make([]string, 50)
	for i := range lines {
		lines[i], names[i] = string(b), "server "+strconv.Itoa(i+1)
	}
	return lines, names
}

func BenchmarkXrayJSON50(b *testing.B) {
	lines, names := fifty(b, xrayFragments[1])
	p := xrayProfile{lines: lines, names: names, preset: ruSplit()}
	b.ReportAllocs()
	var n int
	for i := 0; i < b.N; i++ {
		doc, err := p.build()
		if err != nil {
			b.Fatal(err)
		}
		n = len(doc)
	}
	b.ReportMetric(float64(n), "bytes/doc")
}

func BenchmarkSingbox50(b *testing.B) {
	lines, names := fifty(b, singboxFragments[1])
	p := singboxProfile{title: "Example VPN", lines: lines, names: names, preset: ruSplit()}
	b.ReportAllocs()
	var n int
	for i := 0; i < b.N; i++ {
		doc, err := p.build()
		if err != nil {
			b.Fatal(err)
		}
		n = len(doc)
	}
	b.ReportMetric(float64(n), "bytes/doc")
}
