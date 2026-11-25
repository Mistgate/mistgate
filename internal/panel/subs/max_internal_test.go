package subs

import (
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// A preset with BlockMax and the Russian split in the three formats that carry rules: the Max domains are rejected before the
// ".ru direct" rule, or max.ru would go direct by its ending.

func ruMax(blockMax bool) *dns.Preset {
	return &dns.Preset{Name: "ru", Transport: dns.KindPlain, SplitDirect: true, BlockMax: blockMax,
		Servers: []dns.Server{{Variant: "cloudflare/standard"}, {Variant: "google/standard"}},
		Split:   []dns.SplitRule{{Suffixes: []string{".ru"}, Servers: []dns.Server{{Variant: "yandex/basic"}}}}}
}

func TestMihomoProfileBlockMax(t *testing.T) {
	build := func(p *dns.Preset) profileDoc {
		b, err := mihomoProfile{title: "Example VPN", lines: []string{fixture(t, hy2Line)}, names: []string{"de1"}, preset: p}.build()
		if err != nil {
			t.Fatal(err)
		}
		return parseProfile(t, b)
	}
	d := build(ruMax(true))
	if want := []string{"DOMAIN-SUFFIX,max.ru,REJECT", "DOMAIN-SUFFIX,oneme.ru,REJECT", "DOMAIN-SUFFIX,ru,DIRECT", "MATCH,Example VPN"}; !slices.Equal(d.Rules, want) {
		t.Errorf("rules = %v", d.Rules)
	}
	if d := build(ruMax(false)); !slices.Equal(d.Rules, []string{"DOMAIN-SUFFIX,ru,DIRECT", "MATCH,Example VPN"}) {
		t.Errorf("flag off: %v", d.Rules)
	}
}

func TestXrayJSONBlockMax(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	b, err := xrayProfile{lines: lines, names: jsonNames, preset: ruMax(true)}.build()
	if err != nil {
		t.Fatal(err)
	}
	for i, cfg := range decodeArray(t, b) {
		block, direct := -1, -1
		for j, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
			m := r.(map[string]any)
			if m["outboundTag"] == "block" {
				block = j
				if d := m["domain"].([]any); len(d) != 2 || d[0] != "domain:max.ru" || d[1] != "domain:oneme.ru" {
					t.Errorf("config %d: block domains %v", i, d)
				}
			}
			if d, ok := m["domain"].([]any); ok && m["outboundTag"] == "direct" && len(d) > 0 && d[0] == "domain:ru" {
				direct = j
			}
		}
		if block < 0 || direct < 0 || block > direct {
			t.Errorf("config %d: block rule at %d, direct .ru at %d", i, block, direct)
		}
	}
	b, _ = xrayProfile{lines: lines, names: jsonNames, preset: ruMax(false)}.build()
	if strings.Contains(string(b), "oneme.ru") {
		t.Error("flag off: the document names oneme.ru")
	}
}

func TestSingboxBlockMax(t *testing.T) {
	lines := fixtures(t, singboxFragments)
	b, err := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: ruMax(true)}.build()
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeObject(t, b)
	if f := doc["dns"].(map[string]any)["rules"].([]any)[0].(map[string]any); f["action"] != "predefined" || f["rcode"] != "NXDOMAIN" || f["domain_suffix"].([]any)[0] != "max.ru" {
		t.Errorf("first dns rule = %v", f)
	}
	var seq []string // sniff, hijack-dns, ip_is_private, then reject Max, then .ru direct
	for _, r := range doc["route"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		switch {
		case m["ip_is_private"] != nil:
			seq = append(seq, "private")
		case m["action"] == "reject":
			seq = append(seq, "reject")
			if s := m["domain_suffix"].([]any); len(s) != 2 || s[0] != "max.ru" || s[1] != "oneme.ru" {
				t.Errorf("reject rule = %v", m)
			}
		default:
			seq = append(seq, m["action"].(string))
		}
	}
	if want := []string{"sniff", "hijack-dns", "private", "reject", "route"}; !slices.Equal(seq, want) {
		t.Errorf("route rules = %v, want %v", seq, want)
	}
	b, _ = singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: ruMax(false)}.build()
	if strings.Contains(string(b), "oneme.ru") {
		t.Error("flag off: the document names oneme.ru")
	}
}
