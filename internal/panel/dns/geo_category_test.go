package dns

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A rule may name one site category alone, geosite:<name>: only the form of the name is checked, it is lower-cased, a rule of
// categories alone is complete, and a name with no geoip pair still gives a rule.
func TestGeositeCategoryRule(t *testing.T) {
	srv := []Server{{Variant: "yandex/basic"}}
	main := []Server{{Variant: "cloudflare/standard"}}
	clean := func(geo ...string) ([]SplitRule, error) {
		_, _, _, sp, err := Clean("p", "", main, []SplitRule{{Geo: geo, Servers: srv}})
		return sp, err
	}

	sp, err := clean(" GeoSite:Category-Media-RU", "geosite:category-media-ru", "geosite:telegram", "cn", "geosite:geolocation-!cn", "geosite:google@cn", "geosite:0abc")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"geosite:category-media-ru", "geosite:telegram", "cn", "geosite:geolocation-!cn", "geosite:google@cn", "geosite:0abc"}
	if !slices.Equal(sp[0].Geo, want) {
		t.Errorf("geo = %v, want %v", sp[0].Geo, want)
	}
	if b, _ := json.Marshal(sp[0]); string(b) != `{"suffixes":[],"geo":["geosite:category-media-ru","geosite:telegram","cn","geosite:geolocation-!cn","geosite:google@cn","geosite:0abc"],"servers":[{"variant":"yandex/basic"}]}` {
		t.Errorf("stored as %s", b)
	}

	for _, name := range []string{
		"geosite:", "geosite:-x", "geosite:!x", "geosite:@x", "geosite:a_b", "geosite:a.b", "geosite:a b", "geosite:a,b", "geosite:a/b",
		"geosite:ä", "geosite:" + strings.Repeat("a", 65), "geosite:geosite:x", "telegram", "geoip:ru", "geosite",
	} {
		if _, err := clean(name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
	if _, err := clean("geosite:" + strings.Repeat("a", 64)); err != nil {
		t.Errorf("a 64-character name: %v", err)
	}
	if _, err := clean("geosite:Bad Name"); err == nil || !strings.Contains(err.Error(), "invalid site category") {
		t.Errorf("error = %v", err)
	}
	var many []string
	for i := 0; i <= MaxGeoPerRule; i++ {
		many = append(many, "geosite:a"+strconv.Itoa(i))
	}
	if _, err := clean(many...); err == nil {
		t.Error("more categories than a rule may carry were accepted")
	}
}

func categoryPreset() Preset {
	return Preset{
		ID: "dns_x", Name: "x", Transport: KindPlain, SplitDirect: true, Servers: variantServers("cloudflare/standard"),
		Split: []SplitRule{{Suffixes: []string{}, Geo: []string{"geosite:category-media-ru", "geosite:telegram"}, Servers: variantServers("yandex/basic")}},
	}
}

// Categories alone in the four formats: the site list only, never an address list.
func TestGeositeCategoryRender(t *testing.T) {
	p := categoryPreset()

	m := mustDNS(t, p, "G")
	if len(m.Providers) != 2 || m.Providers[0].Behavior != "domain" || m.Providers[1].Behavior != "domain" ||
		m.Providers[0].URL != "https://github.com/MetaCubeX/meta-rules-dat/raw/meta/geo/geosite/category-media-ru.mrs" ||
		m.Providers[1].Name != "geosite-telegram" || m.Providers[1].Rule() != "RULE-SET,geosite-telegram,DIRECT" {
		t.Errorf("providers = %+v", m.Providers)
	}
	if len(m.Policy) != 2 || m.Policy[0].Domain != "rule-set:geosite-category-media-ru" {
		t.Errorf("policy = %+v", m.Policy)
	}
	goldenJSON(t, "mihomo_dns_site_category.json", map[string]any{"policy": m.Policy, "providers": m.Providers, "rules": rulesOf(m.Providers), "notes": m.Notes})

	x, err := p.XrayDNS()
	if err != nil {
		t.Fatal(err)
	}
	if len(x.DirectGeo) != 2 || x.DirectGeo[0] != (XrayGeo{Site: "geosite:category-media-ru"}) || x.DirectGeo[1].IP != "" {
		t.Errorf("xray geo = %+v", x.DirectGeo)
	}
	goldenJSON(t, "xray_dns_site_category.json", map[string]any{"dns": x.Section, "direct": x.Direct, "directGeo": x.DirectGeo, "directIPs": x.DirectIPs, "notes": x.Notes})

	sb, err := p.SingboxDNS("Mistgate")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sb.DirectSets, []string{"geosite-category-media-ru", "geosite-telegram"}) || len(sb.RuleSets) != 2 ||
		sb.RuleSets[0].URL != "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-media-ru.srs" {
		t.Errorf("sing-box = %v %+v", sb.DirectSets, sb.RuleSets)
	}
	goldenJSON(t, "singbox_dns_site_category.json", map[string]any{"dns": sb.Section, "direct": sb.Direct, "ruleSets": sb.RuleSets, "directSets": sb.DirectSets, "notes": sb.Notes})

	h, err := HappRouting(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); !strings.Contains(got, `"DirectSites":["geosite:category-media-ru","geosite:telegram"],"DirectIp":[],`) {
		t.Errorf("happ: %s", got)
	}
}

// A country and the category of the same name are one list: the first rule that takes it keeps it.
func TestGeositeCategoryAndCountryClaimOnce(t *testing.T) {
	p := Preset{Name: "two", Transport: KindPlain, SplitDirect: true, Servers: variantServers("cloudflare/standard"), Split: []SplitRule{
		{Geo: []string{"geosite:cn"}, Servers: variantServers("alidns/standard")},
		{Geo: []string{"cn", "geosite:telegram"}, Servers: variantServers("dnspod/standard")},
		{Geo: []string{"geosite:telegram", "ir"}, Servers: variantServers("dnspod/standard")},
		{Geo: []string{"ir", "geosite:category-ir"}, Servers: variantServers("dnspod/standard")},
	}}
	_, split, notes, err := p.ClientPlan(ClientMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(split[0].Geo, []string{"geosite:cn"}) || !slices.Equal(split[1].Geo, []string{"geosite:telegram"}) ||
		!slices.Equal(split[2].Geo, []string{"ir"}) || len(split[3].Geo) != 0 {
		t.Errorf("split = %+v", split)
	}
	if !slices.Contains(notes, `country list "cn" skipped: its geosite list is used by an earlier rule`) || len(notes) != 1 {
		t.Errorf("notes = %v", notes)
	}
}
