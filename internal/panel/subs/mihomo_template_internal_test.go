package subs

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// The Mihomo profile built around an owner's template: where the servers go, what the panel adds, what it leaves alone, and when a
// person gets the built-in profile instead.

func templateText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hostileNames are what a node, a profile or a brand can be called: whatever they are, they are values of the document.
var hostileNames = []string{
	"x\"\n  - name: evil", "- name: evil", "a: b", "a #b", "{a: b}", "[a, b]", "a, b", "&anchor", "*alias", "!tag", "|", ">", "'q'", "\"q\"", "50%", "@at",
	"`tick`", "line one\nline two\n", "tab\there", "yes", "null", "~", "0x1F", "1e3", "<<", "---", "...", "\U0001F1E9\U0001F1EA de1 \U0001F600", "DIRECT", "reject",
	"Auto", "Proxy", "same", "same",
}

func templated(t *testing.T, tpl string, p mihomoProfile) (string, bool) {
	t.Helper()
	b, used, err := p.render("tpl", tpl)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), used
}

func rootKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, b)
	}
	var keys []string
	for i := 0; i+1 < len(n.Content[0].Content); i += 2 {
		keys = append(keys, n.Content[0].Content[i].Value)
	}
	return keys
}

type templatedProfile struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	DNS   map[string]any `yaml:"dns"`
	Rules []string       `yaml:"rules"`
}

func parseTemplated(t *testing.T, b string) templatedProfile {
	t.Helper()
	var d templatedProfile
	if err := yaml.Unmarshal([]byte(b), &d); err != nil {
		t.Fatalf("not a profile: %v\n%s", err, b)
	}
	return d
}

func twoServers(t *testing.T) mihomoProfile {
	return mihomoProfile{
		title:  "Example VPN",
		lines:  []string{fixture(t, hy2Line), fixture(t, awgLine)},
		names:  []string{"\U0001F1E9\U0001F1EA de1 · hy2", "\U0001F1F3\U0001F1F1 nl1 · awg"},
		preset: ruSplit(), autoSelect: true,
	}
}

// A router's template and a PC's: the servers come first, Auto last, the dns the template lacks is the preset's resolvers.
func TestMihomoTemplateGolden(t *testing.T) {
	for _, tc := range []struct{ name, in, out string }{
		{"router", "mihomo_template_router.yaml", "mihomo_template_router.out.yaml"},
		{"pc", "mihomo_template_pc.yaml", "mihomo_template_pc.out.yaml"},
	} {
		got, used := templated(t, templateText(t, tc.in), twoServers(t))
		if !used {
			t.Fatalf("%s: the built-in profile was sent", tc.name)
		}
		golden(t, tc.out, got)
	}

	routerOut, _ := templated(t, templateText(t, "mihomo_template_router.yaml"), twoServers(t))
	router := parseTemplated(t, routerOut)
	if len(router.Proxies) != 2 || router.Proxies[0]["name"] != "\U0001F1E9\U0001F1EA de1 · hy2" || router.Proxies[1]["type"] != "wireguard" {
		t.Errorf("router proxies: %v", router.Proxies)
	}
	last := router.Groups[len(router.Groups)-1]
	if last.Name != "Auto" || last.Type != "url-test" || len(last.Proxies) != 2 {
		t.Errorf("router groups: %+v", router.Groups)
	}
	// the dns the template lacks: the resolvers of the preset, respect-rules on, no group fragment, no policy, no provider
	if router.DNS["respect-rules"] != true || router.DNS["enhanced-mode"] != "fake-ip" || router.DNS["nameserver-policy"] != nil || router.DNS["proxy-server-nameserver"] == nil {
		t.Errorf("router dns: %v", router.DNS)
	}
	for _, ns := range router.DNS["nameserver"].([]any) {
		if strings.Contains(ns.(string), "#") {
			t.Errorf("a group fragment in %q", ns)
		}
	}
	// the rules are the template's: nothing of the preset (ru suffixes) was added
	if !slices.Equal(router.Rules, []string{"RULE-SET,ads,REJECT", "DOMAIN-SUFFIX,example.com,DIRECT", "GEOIP,LAN,DIRECT,no-resolve", "MATCH,Proxy"}) {
		t.Errorf("router rules: %v", router.Rules)
	}
	// the template's own dns is not touched
	pc, _ := templated(t, templateText(t, "mihomo_template_pc.yaml"), twoServers(t))
	d := parseTemplated(t, pc)
	if d.DNS["respect-rules"] != nil || len(d.DNS["nameserver"].([]any)) != 1 || d.DNS["nameserver"].([]any)[0] != "9.9.9.9" {
		t.Errorf("pc dns: %v", d.DNS)
	}
	if got := []string{d.Proxies[0]["name"].(string), d.Proxies[1]["name"].(string), d.Proxies[2]["name"].(string)}; got[2] != "office-socks" {
		t.Errorf("pc proxies: %v", got)
	}
	if k := rootKeys(t, []byte(pc)); strings.Join(k, " ") != "mixed-port mode proxy-providers proxies proxy-groups dns rules" {
		t.Errorf("pc keys: %v", k)
	}
}

// The root of a result has the keys of the template, and the panel's three only.
func TestMihomoTemplateRootKeys(t *testing.T) {
	router, _ := templated(t, templateText(t, "mihomo_template_router.yaml"), twoServers(t))
	want := "mixed-port redir-port tproxy-port allow-lan bind-address mode log-level external-controller x-check proxy-groups rule-providers rules proxies dns"
	if got := strings.Join(rootKeys(t, []byte(router)), " "); got != want {
		t.Errorf("keys:\n got %s\nwant %s", got, want)
	}
}

// Names are hostile input: whatever they are, they are values of proxies, and the template's groups and rules are untouched.
func TestMihomoTemplateHostileNames(t *testing.T) {
	for _, tplFile := range []string{"mihomo_template_router.yaml", "mihomo_template_pc.yaml"} {
		tpl := templateText(t, tplFile)
		base, _ := templated(t, tpl, mihomoProfile{title: "T", lines: []string{fixture(t, hy2Line)}, names: []string{"de1"}, preset: ruSplit(), autoSelect: true})
		baseline := parseTemplated(t, base)
		var lines []string
		for range hostileNames {
			lines = append(lines, fixture(t, hy2Line))
		}
		for _, title := range []string{"Brand, Inc", "a#b&c=d", "x\ny", "MATCH,DIRECT"} {
			out, used := templated(t, tpl, mihomoProfile{title: title, lines: lines, names: hostileNames, preset: ruSplit(), autoSelect: true})
			if !used {
				t.Fatalf("%s: not templated", tplFile)
			}
			d := parseTemplated(t, out)
			static := len(baseline.Proxies) - 1 // the template's own proxies
			if len(d.Proxies) != len(hostileNames)+static {
				t.Fatalf("%s, title %q: %d proxies, want %d\n%s", tplFile, title, len(d.Proxies), len(hostileNames)+static, out)
			}
			if !slices.Equal(d.Rules, baseline.Rules) || strings.Join(rootKeys(t, []byte(out)), " ") != strings.Join(rootKeys(t, []byte(base)), " ") {
				t.Errorf("%s, title %q: the structure moved: rules %v keys %v", tplFile, title, d.Rules, rootKeys(t, []byte(out)))
			}
			if len(d.Groups) != len(baseline.Groups) {
				t.Errorf("%s: %d groups, want %d", tplFile, len(d.Groups), len(baseline.Groups))
			}
			seen := map[string]bool{}
			for i, p := range d.Proxies {
				n, ok := p["name"].(string)
				if !ok || n == "" || seen[n] {
					t.Errorf("proxy %d has the name %q (twice or not text)", i, p["name"])
				}
				seen[n] = true
				if i < len(hostileNames) && (p["type"] != "hysteria2" || len(p) != 10) {
					t.Errorf("proxy %d changed shape: %v", i, p)
				}
			}
			for _, core := range []string{"DIRECT", "REJECT", "direct", "reject"} {
				if seen[core] {
					t.Errorf("a server is named %q", core)
				}
			}
			// the names of the template stay as they are, and the servers that have them come after with a suffix
			for _, g := range d.Groups {
				if g.Name == "Auto" && len(g.Proxies) != len(hostileNames) {
					t.Errorf("Auto lists %d servers, want %d", len(g.Proxies), len(hostileNames))
				}
			}
			names := map[string]bool{}
			for _, g := range d.Groups {
				names[g.Name] = true
			}
			if !names["Auto"] || (strings.Contains(tplFile, "router") && !names["Proxy"]) {
				t.Errorf("groups %v", names)
			}
		}
	}
}

// A server with the name of a proxy or group of the template gets a suffix; the template's name is never changed.
func TestMihomoTemplateServerNameOfTheTemplate(t *testing.T) {
	p := mihomoProfile{title: "T", lines: []string{fixture(t, hy2Line), fixture(t, hy2Line), fixture(t, hy2Line)}, names: []string{"office-socks", "Servers", "Work"}, autoSelect: true}
	out, _ := templated(t, templateText(t, "mihomo_template_pc.yaml"), p)
	d := parseTemplated(t, out)
	var names []string
	for _, px := range d.Proxies {
		names = append(names, px["name"].(string))
	}
	if !slices.Equal(names, []string{"office-socks ·", "Servers ·", "Work ·", "office-socks"}) {
		t.Errorf("proxies %v", names)
	}
	if d.Groups[0].Name != "Servers" || d.Groups[1].Name != "Work" || !slices.Equal(d.Groups[1].Proxies, []string{"office-socks", "DIRECT"}) {
		t.Errorf("the template's groups changed: %+v", d.Groups)
	}
	// Auto lists the servers of the person only, not the template's static proxy
	if auto := d.Groups[len(d.Groups)-1]; auto.Name != "Auto" || !slices.Equal(auto.Proxies, []string{"office-socks ·", "Servers ·", "Work ·"}) {
		t.Errorf("Auto: %+v", auto)
	}
}

func TestMihomoTemplateAutoSelect(t *testing.T) {
	tpl := templateText(t, "mihomo_template_router.yaml")
	for name, tc := range map[string]struct {
		auto  bool
		lines int
		own   bool
		want  []string // the groups of the result
	}{
		"on, two servers":  {true, 2, false, []string{"Proxy", "Fast", "Auto"}},
		"on, one server":   {true, 1, false, []string{"Proxy", "Fast", "Auto"}},
		"off, two servers": {false, 2, false, []string{"Proxy", "Fast"}},
		"on, own Auto":     {true, 2, true, []string{"Proxy", "Fast", "Auto"}},
	} {
		text := tpl
		if tc.own {
			text = strings.Replace(text, "  - name: Fast\n", "  - name: Auto\n    type: select\n    proxies: [DIRECT]\n  - name: Fast\n", 1)
		}
		var lines []string
		for i := 0; i < tc.lines; i++ {
			lines = append(lines, fixture(t, hy2Line))
		}
		out, _ := templated(t, text, mihomoProfile{title: "T", lines: lines, preset: ruSplit(), autoSelect: tc.auto})
		d := parseTemplated(t, out)
		var got []string
		for _, g := range d.Groups {
			got = append(got, g.Name)
		}
		if tc.own {
			want := []string{"Proxy", "Auto", "Fast"}
			if !slices.Equal(got, want) || d.Groups[1].Type != "select" {
				t.Errorf("%s: %v (own Auto must stay the template's)", name, got)
			}
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: groups %v, want %v", name, got, tc.want)
		}
		if tc.auto {
			if auto := d.Groups[len(d.Groups)-1]; len(auto.Proxies) != tc.lines || auto.Type != "url-test" {
				t.Errorf("%s: Auto %+v", name, auto)
			}
		}
	}
}

func TestMihomoTemplateDNS(t *testing.T) {
	router := templateText(t, "mihomo_template_router.yaml")
	pc := templateText(t, "mihomo_template_pc.yaml")
	p := mihomoProfile{title: "T", lines: []string{fixture(t, hy2Line)}, preset: ruSplit(), autoSelect: true}
	// a template without dns gets the resolvers of the preset with respect-rules, and nothing else of it
	out, _ := templated(t, router, p)
	d := parseTemplated(t, out)
	if d.DNS["respect-rules"] != true || d.DNS["nameserver-policy"] != nil || strings.Contains(out, "rule-set:") || strings.Contains(out, "DOMAIN-SUFFIX,ru,") {
		t.Errorf("dns: %v\n%s", d.DNS, out)
	}
	// the preset that carries country lists adds no provider and no policy to a template
	cn := p
	cn.preset = cnSplit()
	if out, _ := templated(t, router, cn); strings.Contains(out, "geosite-cn") || strings.Contains(out, "geoip-cn") {
		t.Errorf("the country lists of the preset reached a template:\n%s", out)
	}
	// ads blocking of the preset: no REJECT rule, no provider
	ads := p
	ads.preset = adsDocPresets[0].preset
	if out, _ := templated(t, router, ads); strings.Count(out, "RULE-SET,") != 1 {
		t.Errorf("the ad list of the preset reached a template:\n%s", out)
	}
	// no preset: no dns section
	none := p
	none.preset = nil
	if out, _ := templated(t, router, none); strings.Contains(out, "\ndns:") {
		t.Errorf("a dns out of nothing:\n%s", out)
	}
	// a preset the core cannot use leaves it out
	bad := p
	bad.preset = &dns.Preset{Name: "none"}
	if out, used := templated(t, router, bad); !used || strings.Contains(out, "\ndns:") {
		t.Errorf("a preset without servers: %v\n%s", used, out)
	}
	// a template with its own dns keeps it whatever the preset is
	out, _ = templated(t, pc, p)
	if d := parseTemplated(t, out); len(d.DNS) != 4 || d.DNS["nameserver"].([]any)[0] != "9.9.9.9" {
		t.Errorf("the template's dns changed: %v", d.DNS)
	}
}

// What the owner wrote stays: comments, anchors, merge keys, quoting and the order of the keys.
func TestMihomoTemplateKeepsTheText(t *testing.T) {
	out, _ := templated(t, templateText(t, "mihomo_template_router.yaml"), twoServers(t))
	for _, want := range []string{
		"# Router: transparent proxy for the LAN.", "x-check: &check", "<<: *check", "bind-address: \"*\"", "proxies: [Auto, DIRECT] # the panel's Auto group",
		"filter: \"(?i)de|nl\"", "- GEOIP,LAN,DIRECT,no-resolve",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "!!merge") {
		t.Errorf("a merge key was written with its tag:\n%s", out)
	}
}

// A long line stays one line (the encoder of yaml.v3 does not fold), so that the rules and the filters are as written.
func TestMihomoTemplateDoesNotFoldLongLines(t *testing.T) {
	long := "DOMAIN-SUFFIX," + strings.Repeat("a", 280) + ".example.com,Proxy"
	regex := "(?i)" + strings.Repeat("alpha|", 60) + "omega"
	tpl := "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n    filter: \"" + regex + "\"\nrules:\n  - " + long + "\n  - MATCH,Proxy\n"
	out, _ := templated(t, tpl, mihomoProfile{title: "T", lines: []string{fixture(t, hy2Line)}, autoSelect: true})
	if !strings.Contains(out, "\n  - "+long+"\n") || !strings.Contains(out, "filter: \""+regex+"\"\n") {
		t.Errorf("a long line was folded:\n%s", out)
	}
}

// The stored template is shared by every request: building from it twice gives the same bytes.
func TestMihomoTemplateIsTheSameEachTime(t *testing.T) {
	tpl := templateText(t, "mihomo_template_router.yaml")
	a, _ := templated(t, tpl, twoServers(t))
	b, _ := templated(t, tpl, twoServers(t))
	if a != b {
		t.Errorf("two builds differ:\n%s\n---\n%s", a, b)
	}
}

// The built-in profile is what a person gets when nothing applies: no template, a note instead of servers, no server, a template
// that does not build.
func TestMihomoTemplateFallsBackToTheBuiltInProfile(t *testing.T) {
	tpl := templateText(t, "mihomo_template_router.yaml")
	builtin, err := twoServers(t).build()
	if err != nil {
		t.Fatal(err)
	}
	if got, used := templated(t, "", twoServers(t)); used || got != string(builtin) {
		t.Errorf("no template: %v", used)
	}
	// a note: the placeholder entry, as the built-in profile has it
	note := mihomoProfile{title: "T", lines: []string{placeholderProxy}, names: []string{"Subscription ended"}, preset: ruSplit(), autoSelect: true, note: true}
	want, _ := note.build()
	if got, used := templated(t, tpl, note); used || got != string(want) || !strings.Contains(got, "Subscription ended") || strings.Contains(got, "redir-port") {
		t.Errorf("a note: %v\n%s", used, got)
	}
	// no server at all, and none that can be used
	for name, lines := range map[string][]string{"no lines": nil, "broken lines": {"", "not: a list", "- 1\n- 2"}} {
		p := mihomoProfile{title: "T", lines: lines, preset: ruSplit(), autoSelect: true}
		want, _ := p.build()
		if got, used := templated(t, tpl, p); used || got != string(want) || strings.Contains(got, "redir-port") {
			t.Errorf("%s: %v\n%s", name, used, got)
		}
	}
}

// A template that does not build (a stored text this version cannot read) gives the built-in profile and a log line with the name of
// the template and its code: never its text.
func TestMihomoTemplateUnusableIsLoggedNotServed(t *testing.T) {
	for name, text := range map[string]string{
		"a syntax error":      "proxy-groups: [\n  secret-token-text",
		"two documents":       templateText(t, "mihomo_template_router.yaml") + "---\na: 1\n",
		"proxies of a map":    "proxies:\n  a: secret-token-text\n",
		"a merge at the root": "base: &b\n  a: secret-token-text\n<<: *b\n",
	} {
		var logs bytes.Buffer
		p := mihomoProfile{title: "T", lines: []string{fixture(t, hy2Line)}, preset: ruSplit(), autoSelect: true, log: slog.New(slog.NewTextHandler(&logs, nil))}
		want, _ := mihomoProfile{title: "T", lines: p.lines, preset: ruSplit(), autoSelect: true}.build()
		out, used, err := p.render("Router, v2", text)
		if err != nil || used || string(out) != string(want) {
			t.Errorf("%s: used %v, err %v", name, used, err)
		}
		if l := logs.String(); !strings.Contains(l, "code=mihomo_template_unusable") || !strings.Contains(l, "Router, v2") || strings.Contains(l, "secret-token-text") {
			t.Errorf("%s: log %q", name, l)
		}
	}
}

func BenchmarkMihomoTemplate(b *testing.B) {
	base, err := os.ReadFile(filepath.Join("testdata", "mihomo_template_router.yaml"))
	if err != nil {
		b.Fatal(err)
	}
	// a template of about 14 KB (an author's real ones) and one of 64 KiB: the router template with comment lines and rules added
	grow := func(size int) string {
		var s strings.Builder
		s.Write(base)
		for i := 0; s.Len() < size-200; i++ {
			s.WriteString("  - DOMAIN-SUFFIX,site-" + strings.Repeat("x", 20) + ".example.com,Proxy # rule\n")
		}
		return strings.Replace(s.String(), "\n  - MATCH,Proxy\n", "\n", 1) + "  - MATCH,Proxy\n"
	}
	var lines, names []string
	for i := 0; i < 8; i++ {
		lines = append(lines, fixtureB(b, hy2Line))
		names = append(names, "node "+string(rune('1'+i)))
	}
	for _, size := range []struct {
		name  string
		bytes int
	}{{"14KB", 14 << 10}, {"64KiB", 64 << 10}} {
		text := grow(size.bytes)
		b.Run(size.name, func(b *testing.B) {
			p := mihomoProfile{title: "T", lines: lines, names: names, preset: ruSplit(), autoSelect: true}
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, used, err := p.render("t", text); err != nil || !used {
					b.Fatal(used, err)
				}
			}
		})
	}
}

func fixtureB(b *testing.B, rel string) string {
	b.Helper()
	data, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		b.Fatal(err)
	}
	return string(data)
}
