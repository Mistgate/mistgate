package subsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func tpl(name, text string, groups ...string) *adminv1.MihomoTemplate {
	return &adminv1.MihomoTemplate{Name: name, Yaml: text, GroupIds: groups}
}

func withTemplates(list ...*adminv1.MihomoTemplate) *adminv1.SubscriptionSettings {
	s := Defaults()
	s.MihomoTemplates = list
	return s
}

const smallTemplate = "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n"

// bombText is a billion-laughs document: nine lists of nine references to the one before. Read into nodes it is a few lines; read
// into a value it is 9^9 items.
func bombText(prefix string) string {
	var b strings.Builder
	b.WriteString(prefix + "a: &a [x, x, x, x, x, x, x, x, x]\n")
	prev := "a"
	for _, n := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		fmt.Fprintf(&b, "%s%s: &%s [%s]\n", prefix, n, n, strings.TrimSuffix(strings.Repeat("*"+prev+", ", 9), ", "))
		prev = n
	}
	return b.String()
}

func generated(t *testing.T, kind string) string {
	t.Helper()
	switch kind {
	case "bomb":
		return smallTemplate + bombText("x-")
	case "bomb_x":
		return "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n    x:\n" + bombText("      ")
	case "deep":
		return "x: " + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + "\n" + smallTemplate
	}
	t.Fatalf("unknown gen %q", kind)
	return ""
}

// What a save of one template takes. The table is the contract of the save: the admin's editor words these refusals.
func TestMihomoTemplateCases(t *testing.T) {
	b, err := os.ReadFile("testdata/mihomo_template_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Name    string
			Lines   []string
			AutoOff bool `json:"auto_off"`
			Gen     string
			OK      bool
			Err     string
		}
	}
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) < 40 {
		t.Fatalf("%d cases: the table was cut", len(table.Cases))
	}
	for _, c := range table.Cases {
		text := strings.Join(c.Lines, "\n")
		if len(c.Lines) > 0 {
			text += "\n"
		}
		if c.Gen != "" {
			text = generated(t, c.Gen)
		}
		s := withTemplates(tpl("t", text, "grp_a"))
		if c.AutoOff {
			s.LinkAutoSelect = new(bool)
		}
		err := Validate(s)
		switch {
		case c.OK && err != nil:
			t.Errorf("%s: refused: %v", c.Name, err)
		case !c.OK && !errors.Is(err, ErrInvalid):
			t.Errorf("%s: accepted (err %v)", c.Name, err)
		case !c.OK && !strings.Contains(err.Error(), c.Err):
			t.Errorf("%s: the message lacks %q: %v", c.Name, c.Err, err)
		}
	}
}

// The text of a refusal names the line the YAML parser found.
func TestMihomoTemplateErrorNamesTheLine(t *testing.T) {
	err := Validate(withTemplates(tpl("t", "mixed-port: 1\nproxy-groups: []\nmixed-port: 2\n", "grp_a")))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("%v", err)
	}
}

func TestMihomoTemplateNotUTF8(t *testing.T) {
	if err := Validate(withTemplates(tpl("t", smallTemplate+"# \xff\n"))); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("%v", err)
	}
}

func TestMihomoTemplateListLimits(t *testing.T) {
	// 20 templates are fine; the 21st is not.
	var list []*adminv1.MihomoTemplate
	for i := 0; i < MaxMihomoTemplates; i++ {
		list = append(list, tpl(fmt.Sprintf("t%d", i), smallTemplate, fmt.Sprintf("grp_%d", i)))
	}
	if err := Validate(withTemplates(list...)); err != nil {
		t.Fatalf("%d templates: %v", len(list), err)
	}
	if err := Validate(withTemplates(append(list, tpl("one more", smallTemplate))...)); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d templates: %v", len(list)+1, err)
	}
	// one text: 64 KiB is the most, in bytes (a comment makes the padding valid YAML)
	pad := func(n int) string { return smallTemplate + "#" + strings.Repeat("x", n-len(smallTemplate)-2) + "\n" }
	if text := pad(MaxMihomoTemplateBytes); len(text) != MaxMihomoTemplateBytes {
		t.Fatalf("test text of %d bytes", len(text))
	} else if err := Validate(withTemplates(tpl("t", text))); err != nil {
		t.Errorf("exactly the limit: %v", err)
	} else if err := Validate(withTemplates(tpl("t", text+"#\n"))); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("one comment more: %v", err)
	}
	// all together: 256 KiB; five of 64 KiB are over it, four are exactly it
	full := pad(MaxMihomoTemplateBytes)
	four := []*adminv1.MihomoTemplate{tpl("a", full), tpl("b", full), tpl("c", full), tpl("d", full)}
	if err := Validate(withTemplates(four...)); err != nil {
		t.Errorf("four of 64 KiB: %v", err)
	}
	if err := Validate(withTemplates(append(four, tpl("e", smallTemplate))...)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "in all") {
		t.Errorf("past 256 KiB: %v", err)
	}
}

func TestMihomoTemplateNamesAndGroups(t *testing.T) {
	long := strings.Repeat("я", MaxMihomoTemplateName+1)
	bad := map[string][]*adminv1.MihomoTemplate{
		"no name":                  {tpl("", smallTemplate)},
		"a name too long":          {tpl(long, smallTemplate)},
		"a control character":      {tpl("a\nb", smallTemplate)},
		"the same name":            {tpl("Router", smallTemplate), tpl("Router", smallTemplate)},
		"the same name, any case":  {tpl("Router", smallTemplate), tpl("rOUTER", smallTemplate)},
		"a group in two templates": {tpl("a", smallTemplate, "grp_a", "grp_b"), tpl("b", smallTemplate, "grp_c", "grp_b")},
		"a group twice":            {tpl("a", smallTemplate, "grp_a", "grp_a")},
		"a malformed group id":     {tpl("a", smallTemplate, "grp a")},
		"an empty group id":        {tpl("a", smallTemplate, "")},
		"a missing template":       {nil},
	}
	var many []string
	for i := 0; i <= maxHeaderGroups; i++ {
		many = append(many, fmt.Sprintf("grp_%d", i))
	}
	bad["more than 100 groups"] = []*adminv1.MihomoTemplate{tpl("a", smallTemplate, many...)}
	for name, list := range bad {
		if err := Validate(withTemplates(list...)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted (err %v)", name, err)
		}
	}
	good := []*adminv1.MihomoTemplate{
		tpl("Маршрутизатор", smallTemplate, "grp_a", "grp_b"), tpl(strings.Repeat("я", MaxMihomoTemplateName), smallTemplate, "grp_c"),
		tpl("no groups", smallTemplate), // serves nobody; a deleted group can leave it so
	}
	if err := Validate(withTemplates(good...)); err != nil {
		t.Errorf("%v", err)
	}
}

// The switch of automatic selection is in the same document as the templates, so a save that turns it off refuses a template that
// refers to Auto, and the other way round.
func TestMihomoTemplateAutoAndTheSwitch(t *testing.T) {
	refers := tpl("t", "proxy-groups:\n  - name: Proxy\n    type: select\n    proxies: [Auto, DIRECT]\n", "grp_a")
	on := withTemplates(refers)
	if err := Validate(on); err != nil {
		t.Fatalf("switch on: %v", err)
	}
	off := withTemplates(refers)
	off.LinkAutoSelect = new(bool)
	if err := Validate(off); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Auto") {
		t.Errorf("switch off: %v", err)
	}
}

func TestNormalizeTrimsTheTemplateName(t *testing.T) {
	s := withTemplates(tpl("  Router \n", smallTemplate+"# kept as it is   \n"))
	Normalize(s)
	if s.MihomoTemplates[0].Name != "Router" || !strings.HasSuffix(s.MihomoTemplates[0].Yaml, "# kept as it is   \n") {
		t.Errorf("%+v", s.MihomoTemplates[0])
	}
}

func TestMihomoTemplateFor(t *testing.T) {
	s := withTemplates(tpl("router", smallTemplate, "grp_a", "grp_b"), tpl("pc", smallTemplate, "grp_c"), tpl("nobody", smallTemplate))
	for group, want := range map[string]string{"grp_a": "router", "grp_b": "router", "grp_c": "pc", "grp_x": "", "": ""} {
		got := MihomoTemplateFor(s, group)
		if (got == nil) != (want == "") || (got != nil && got.Name != want) {
			t.Errorf("group %q: %v, want %q", group, got, want)
		}
	}
	if MihomoTemplateFor(nil, "grp_a") != nil || MihomoTemplateFor(Defaults(), "grp_a") != nil {
		t.Error("a template out of nothing")
	}
}

// What is stored comes back as it was written: through the cache, through protojson, and a document from before the field is read.
func TestMihomoTemplatesAreStoredAndRead(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := NewCache(st, nil)
	if got := c.Get(ctx).MihomoTemplates; len(got) != 0 {
		t.Fatalf("templates out of nothing: %v", got)
	}
	text := "# Router\n" + smallTemplate + "rules:\n  - MATCH,Proxy # all\n"
	in := withTemplates(tpl("  Router  ", text, "grp_a", "grp_b"), tpl("PC", smallTemplate, "grp_c"))
	if _, err := c.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(ctx, st)
	if err != nil || len(out.MihomoTemplates) != 2 {
		t.Fatalf("%v, %v", out, err)
	}
	if e := out.MihomoTemplates[0]; e.Name != "Router" || e.Yaml != text || !slices.Equal(e.GroupIds, []string{"grp_a", "grp_b"}) {
		t.Errorf("first: %+v", e)
	}
	if got := c.Get(ctx).MihomoTemplates; len(got) != 2 || got[1].Name != "PC" {
		t.Errorf("the cache: %v", got)
	}
	if _, err := c.Update(ctx, withTemplates(tpl("bad", "a: [\n"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("a broken template was stored: %v", err)
	}
	if got := c.Get(ctx).MihomoTemplates; len(got) != 2 {
		t.Errorf("a refused save changed what is served: %v", got)
	}
	// a document stored before the field has none, and the other settings are as they were
	old, err := Parse(`{"title":"Example VPN","response_headers":[{"name":"x-a","value":"v"}]}`)
	if err != nil || len(old.MihomoTemplates) != 0 || old.Title != "Example VPN" || len(old.ResponseHeaders) != 1 {
		t.Errorf("an old document: %+v, %v", old, err)
	}
	// and the other way: an older panel (this one before the field) drops the field it does not know, which is what the docs say
	round, err := Parse(`{"mihomo_templates":[{"name":"r","yaml":"a: 1\n","group_ids":["grp_a"]}],"future_field":1}`)
	if err != nil || len(round.MihomoTemplates) != 1 || round.MihomoTemplates[0].Yaml != "a: 1\n" {
		t.Errorf("protojson: %+v, %v", round, err)
	}
}

// ---- the build ----

func serverNode(t *testing.T, name string) MihomoServer {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("name: x\ntype: hysteria2\nserver: example.com\nport: 443\npassword: pw\n"), &n); err != nil {
		t.Fatal(err)
	}
	return MihomoServer{Node: n.Content[0], Name: name}
}

type built struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	DNS map[string]any `yaml:"dns"`
}

func apply(t *testing.T, text string, opt MihomoOptions, names ...string) (string, built) {
	t.Helper()
	var servers []MihomoServer
	for _, n := range names {
		servers = append(servers, serverNode(t, n))
	}
	out, err := ApplyMihomoTemplate(text, servers, opt)
	if err != nil {
		t.Fatal(err)
	}
	var b built
	if err := yaml.Unmarshal(out, &b); err != nil {
		t.Fatalf("the result does not read: %v\n%s", err, out)
	}
	return string(out), b
}

func proxyNames(b built) []string {
	var out []string
	for _, p := range b.Proxies {
		out = append(out, p["name"].(string))
	}
	return out
}

func TestApplyPutsServersFirstAndAutoLast(t *testing.T) {
	text := "proxies:\n  - name: office\n    type: socks5\n    server: example.com\n    port: 1080\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n"
	_, b := apply(t, text, MihomoOptions{AutoSelect: true}, "de1", "nl1")
	if got := proxyNames(b); !slices.Equal(got, []string{"de1", "nl1", "office"}) {
		t.Errorf("proxies %v", got)
	}
	if len(b.Groups) != 2 || b.Groups[0].Name != "Proxy" || b.Groups[1].Name != "Auto" || b.Groups[1].Type != "url-test" ||
		!slices.Equal(b.Groups[1].Proxies, []string{"de1", "nl1"}) {
		t.Errorf("groups %+v", b.Groups)
	}
	// no switch: no Auto; one server: Auto is still there with it (a template refers to it by name)
	if _, b := apply(t, text, MihomoOptions{}, "de1"); len(b.Groups) != 1 {
		t.Errorf("switch off: %+v", b.Groups)
	}
	if _, b := apply(t, text, MihomoOptions{AutoSelect: true}, "de1"); len(b.Groups) != 2 || !slices.Equal(b.Groups[1].Proxies, []string{"de1"}) {
		t.Errorf("one server: %+v", b.Groups)
	}
	// a template with its own Auto keeps it and gets none
	own := "proxy-groups:\n  - name: Auto\n    type: select\n    proxies: [DIRECT]\n"
	if _, b := apply(t, own, MihomoOptions{AutoSelect: true}, "de1"); len(b.Groups) != 1 || b.Groups[0].Type != "select" {
		t.Errorf("own Auto: %+v", b.Groups)
	}
	// no keys at all: both are made
	if _, b := apply(t, "mixed-port: 1\n", MihomoOptions{AutoSelect: true}, "de1"); len(b.Proxies) != 1 || len(b.Groups) != 1 || b.Groups[0].Name != "Auto" {
		t.Errorf("empty template: %+v", b)
	}
}

// Names of the template are never changed: a server with the same name gets the suffix, so does one named like the core or Auto.
func TestApplyKeepsTheTemplateNames(t *testing.T) {
	text := "proxies:\n  - name: office\n    type: socks5\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n"
	_, b := apply(t, text, MihomoOptions{AutoSelect: true}, "office", "Proxy", "Auto", "DIRECT", "direct", "de1", "de1")
	want := []string{"office ·", "Proxy ·", "Auto ·", "DIRECT ·", "direct ·", "de1", "de1 ·", "office"}
	if got := proxyNames(b); !slices.Equal(got, want) {
		t.Errorf("proxies %v, want %v", got, want)
	}
	if b.Groups[0].Name != "Proxy" || b.Groups[1].Name != "Auto" {
		t.Errorf("the groups were renamed: %+v", b.Groups)
	}
}

func TestApplyAddsDNSOnlyWhenTheTemplateHasNone(t *testing.T) {
	dns := mapOf(strNode("enable"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, strNode("respect-rules"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	for name, tc := range map[string]struct {
		text    string
		wantOwn bool
	}{
		"none": {smallTemplate, false}, "null": {smallTemplate + "dns:\n", false}, "own": {smallTemplate + "dns:\n  enable: false\n", true},
	} {
		_, b := apply(t, tc.text, MihomoOptions{DNS: dns}, "de1")
		if own := b.DNS["enable"] == false; own != tc.wantOwn || (!tc.wantOwn && b.DNS["respect-rules"] != true) {
			t.Errorf("%s: dns %v", name, b.DNS)
		}
	}
	if _, b := apply(t, smallTemplate, MihomoOptions{}, "de1"); b.DNS != nil {
		t.Errorf("a dns out of nothing: %v", b.DNS)
	}
}

// Comments, anchors, merge keys, flow and block styles stay as the owner wrote them.
func TestApplyKeepsWhatTheOwnerWrote(t *testing.T) {
	text := `# Router, v3
mixed-port: 7890 # the port
x-check: &check
  url: https://www.gstatic.com/generate_204
  interval: 300
proxy-groups:
  # the main one
  - name: Proxy
    type: select
    include-all-proxies: true
  - name: Fast
    <<: *check
    type: url-test
    include-all-proxies: true
x-note: |
  keep
  this
rules: [MATCH,Proxy]
`
	out, b := apply(t, text, MihomoOptions{AutoSelect: true}, "de1")
	for _, want := range []string{"# Router, v3", "mixed-port: 7890 # the port", "&check", "<<: *check", "# the main one", "x-note: |\n  keep\n  this", "rules: [MATCH, Proxy]"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result lost %q:\n%s", want, out)
		}
	}
	if len(b.Groups) != 3 {
		t.Errorf("groups %+v", b.Groups)
	}
	// what the build wrote is a template again (the Auto group it added is the template's own now)
	if err := CheckMihomoTemplate(out, true); err != nil {
		t.Errorf("the output is not a template: %v", err)
	}
}

// The comments that open the file are the first thing of the result, whatever the template starts with: the servers of the person
// come after them (in the template's own `proxies` where it has one, else at the end), never above.
func TestApplyKeepsTheOpeningCommentsFirst(t *testing.T) {
	const groups = "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n"
	const static = "  - {name: office, type: socks5, server: example.com, port: 1080}\n"
	for name, text := range map[string]string{
		"no proxies":            "# Router, v3\n# for the family\n\nmixed-port: 7890\n" + groups,
		"proxies first":         "# Router, v3\n# for the family\n\nproxies:\n" + static + groups,
		"proxies first, no gap": "# Router, v3\n# for the family\nproxies:\n" + static + groups,
		"empty proxies first":   "# Router, v3\n# for the family\n\nproxies: []\n" + groups,
		"null proxies first":    "# Router, v3\n# for the family\n\nproxies:\n" + groups,
		"proxies, then a note":  "# Router, v3\n# for the family\n\nproxies:\n  # the office\n" + static + groups,
	} {
		t.Run(name, func(t *testing.T) {
			out, b := apply(t, text, MihomoOptions{AutoSelect: true}, "de1")
			if !strings.HasPrefix(out, "# Router, v3\n# for the family\n") {
				t.Errorf("the opening comments are not first:\n%s", out)
			}
			if got := proxyNames(b); len(got) == 0 || got[0] != "de1" {
				t.Errorf("the person's server is not first of proxies: %v", got)
			}
		})
	}
}

func TestApplyRefusesWhatItCannotTouchSafely(t *testing.T) {
	for name, text := range map[string]string{
		"a merge key at the root":   "base: &b\n  mixed-port: 1\n<<: *b\n",
		"proxies as an alias":       "list: &p []\nproxies: *p\n",
		"two documents":             smallTemplate + "---\na: 1\n",
		"proxies of the wrong kind": "proxies:\n  a: 1\n",
	} {
		if _, err := ApplyMihomoTemplate(text, []MihomoServer{serverNode(t, "de1")}, MihomoOptions{}); err == nil {
			t.Errorf("%s: built", name)
		}
	}
	if _, err := ApplyMihomoTemplate(smallTemplate, nil, MihomoOptions{}); !errors.Is(err, ErrNoServers) {
		t.Errorf("no servers: %v", err)
	}
}

// The stored document is shared by every request: a build must not change what it was given.
func TestApplyDoesNotChangeTheTemplateText(t *testing.T) {
	text := smallTemplate
	for i := 0; i < 3; i++ {
		out, _ := apply(t, text, MihomoOptions{AutoSelect: true}, "de1")
		if strings.Count(out, "Auto") != 1 || strings.Count(out, "de1") != 2 { // the server, and the Auto group's list
			t.Fatalf("round %d:\n%s", i, out)
		}
	}
}

func TestMihomoTemplateWarnings(t *testing.T) {
	codes := func(text string, auto bool) string {
		var out []string
		for _, w := range MihomoTemplateWarnings(text, auto) {
			s := w.Code
			if w.Arg != "" {
				s += ":" + w.Arg
			}
			out = append(out, s)
		}
		return strings.Join(out, " ")
	}
	for name, tc := range map[string]struct {
		text string
		auto bool
		want string
	}{
		"clean":                         {smallTemplate + "rules:\n  - DOMAIN,example.com,DIRECT\n  - MATCH,Proxy\n", true, ""},
		"no group takes servers":        {"proxy-groups:\n  - name: Proxy\n    type: select\n    proxies: [DIRECT]\n", true, "no_server_group"},
		"Auto takes them":               {"proxy-groups:\n  - name: Proxy\n    type: select\n    proxies: [Auto]\n", true, ""},
		"no groups at all":              {"mixed-port: 1\n", true, "no_server_group"},
		"own dns":                       {smallTemplate + "dns:\n  enable: true\n", true, "own_dns"},
		"null dns is none":              {smallTemplate + "dns:\n", true, ""},
		"own Auto, switch on":           {"proxy-groups:\n  - name: Auto\n    type: url-test\n    include-all-proxies: true\n", true, "own_auto"},
		"own Auto, switch off":          {"proxy-groups:\n  - name: Auto\n    type: url-test\n    include-all-proxies: true\n", false, ""},
		"unknown rule targets":          {smallTemplate + "rules:\n  - DOMAIN,a.example.com,Nowhere\n  - GEOIP,CN,Elsewhere,no-resolve\n  - IP-CIDR,10.0.0.0/8,Nowhere,no-resolve,src\n  - MATCH,Proxy\n", true, "unknown_rule_target:Nowhere unknown_rule_target:Elsewhere"},
		"SUB-RULE and flags":            {smallTemplate + "rules:\n  - SUB-RULE,(NETWORK,tcp),inner\n  - AND,((DOMAIN,a),(NETWORK,UDP)),REJECT\n  - MATCH\n  - RULE-SET,ads,DIRECT,no-resolve\n", true, ""},
		"the core and Auto":             {smallTemplate + "rules:\n  - MATCH,Auto\n  - DOMAIN,b,REJECT-DROP\n", true, ""},
		"Auto in a rule, no Auto":       {smallTemplate + "rules:\n  - MATCH,Auto\n", false, "unknown_rule_target:Auto"},
		"a rules key of the wrong kind": {smallTemplate + "rules: nothing\n", true, ""},
		"a text that does not read":     {"a: [\n", true, ""},
	} {
		if got := codes(tc.text, tc.auto); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
