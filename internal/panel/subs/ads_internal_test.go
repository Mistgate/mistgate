package subs

import (
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// A preset with BlockAds, in the three formats that carry rules (Happ and the dns package have their own tests): the ad list comes
// before every other rule, whatever the split says. The documents without the flag are the existing goldens, byte for byte.

// adsDocPresets: the ad list alone and next to the Chinese country lists (the block has to stay the first rule).
var adsDocPresets = []struct {
	name   string
	preset *dns.Preset
}{
	{"ads", &dns.Preset{Name: "ads", Transport: dns.KindPlain, BlockAds: true, Servers: []dns.Server{{Variant: "cloudflare/standard"}, {Variant: "google/standard"}}}},
	{"cn_ads", func() *dns.Preset { p := cnSplit(); p.BlockAds = true; return p }()},
}

func TestMihomoProfileGoldenAds(t *testing.T) {
	for _, c := range adsDocPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := mihomoProfile{
				title:  "Example VPN",
				lines:  []string{fixture(t, hy2Line), fixture(t, awgLine)},
				names:  []string{"\U0001F1E9\U0001F1EA de1 · hy2", "\U0001F1E9\U0001F1EA de1 · awg"},
				preset: c.preset,
			}.build()
			if err != nil {
				t.Fatal(err)
			}
			golden(t, "mihomo_profile_"+c.name+".yaml", string(b))
			d := parseProfile(t, b)
			if d.Rules[0] != "RULE-SET,geosite-category-ads-all,REJECT" || d.Rules[len(d.Rules)-1] != "MATCH,Example VPN" {
				t.Errorf("rules = %v", d.Rules)
			}
			p := d.RuleProviders["geosite-category-ads-all"]
			if p["behavior"] != "domain" || p["format"] != "mrs" || p["type"] != "http" || p["proxy"] != "Example VPN" || p["interval"] != 86400 ||
				p["url"] != "https://github.com/MetaCubeX/meta-rules-dat/raw/meta/geo/geosite/category-ads-all.mrs" {
				t.Errorf("provider = %v", p)
			}
			if want := map[string]int{"ads": 1, "cn_ads": 3}[c.name]; len(d.RuleProviders) != want {
				t.Errorf("%d rule providers, want %d", len(d.RuleProviders), want)
			}
		})
	}
}

func TestXrayJSONGoldenAds(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	for _, c := range adsDocPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := xrayProfile{lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "xray_"+c.name+".json", b)
			for i, cfg := range decodeArray(t, b) {
				if tags := tagsOf(t, cfg["outbounds"]); !slices.Equal(tags, []string{"proxy", "direct", "dns-out", "block"}) {
					t.Errorf("config %d: outbound tags %v", i, tags)
				}
				// after the resolvers' and the OS's DNS rules, before private and direct
				var at, dnsOut, private int
				for j, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
					m := r.(map[string]any)
					switch {
					case m["outboundTag"] == "block":
						at = j
						if d := m["domain"].([]any); len(d) != 1 || d[0] != "geosite:category-ads-all" {
							t.Errorf("config %d: block domains %v", i, d)
						}
					case m["port"] == "53":
						dnsOut = j
					case m["outboundTag"] == "direct" && m["ip"] != nil && private == 0:
						private = j
					}
				}
				if at == 0 || dnsOut == 0 || private == 0 || !(dnsOut < at && at < private) {
					t.Errorf("config %d: dns-out rule at %d, block at %d, private at %d", i, dnsOut, at, private)
				}
			}
		})
	}
}

func TestSingboxGoldenAds(t *testing.T) {
	lines := fixtures(t, singboxFragments)
	for _, c := range adsDocPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "singbox_"+c.name+".json", b)
			doc := decodeObject(t, b)
			dnsRules := doc["dns"].(map[string]any)["rules"].([]any)
			if f := dnsRules[0].(map[string]any); f["action"] != "predefined" || f["rcode"] != "NXDOMAIN" || f["server"] != nil || f["rule_set"].([]any)[0] != "geosite-category-ads-all" {
				t.Errorf("first dns rule = %v", f)
			}
			route := doc["route"].(map[string]any)
			// the rule set is declared even when no country list is (route.rule_set used to follow the direct sets only)
			var tags []string
			for _, s := range route["rule_set"].([]any) {
				tags = append(tags, s.(map[string]any)["tag"].(string))
			}
			if tags[0] != "geosite-category-ads-all" || len(tags) != map[string]int{"ads": 1, "cn_ads": 3}[c.name] {
				t.Errorf("route.rule_set tags = %v", tags)
			}
			// sniff, hijack-dns, ip_is_private, then the reject, then whatever goes direct
			rules := route["rules"].([]any)
			var seq []string
			for _, r := range rules {
				m := r.(map[string]any)
				switch {
				case m["ip_is_private"] != nil:
					seq = append(seq, "private")
				case m["action"] == "reject":
					seq = append(seq, "reject")
					if m["outbound"] != nil || m["rule_set"].([]any)[0] != "geosite-category-ads-all" {
						t.Errorf("reject rule = %v", m)
					}
				default:
					seq = append(seq, m["action"].(string))
				}
			}
			want := []string{"sniff", "hijack-dns", "private", "reject"}
			if c.name == "cn_ads" {
				want = append(want, "route")
			}
			if !slices.Equal(seq, want) {
				t.Errorf("route rules = %v, want %v", seq, want)
			}
		})
	}
}
