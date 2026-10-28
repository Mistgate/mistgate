package dns

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func rowOf(t *testing.T, servers, split string) store.DNSPreset {
	t.Helper()
	return store.DNSPreset{ID: "dns_x", Name: "x", ServersJSON: servers, SplitJSON: split, Transport: "plain"}
}

// chinaSplit is the shape of the built-in "China: Chinese sites direct" (migration 00083), with the catalog resolved by the code.
func chinaSplit() Preset {
	return Preset{
		ID: "dns_builtin_cn_split", Name: "cn", Builtin: true, Transport: KindPlain, SplitDirect: true,
		Servers: variantServers("cloudflare/standard", "google/standard"),
		Split:   []SplitRule{{Suffixes: []string{}, Geo: []string{"cn"}, Servers: variantServers("alidns/standard", "dnspod/standard")}},
	}
}

func TestSplitRuleNeedsSuffixesOrGeo(t *testing.T) {
	srv := []Server{{Variant: "alidns/standard"}}
	main := []Server{{Variant: "cloudflare/standard"}}
	clean := func(r SplitRule) ([]SplitRule, error) {
		_, _, _, sp, err := Clean("p", "", main, []SplitRule{r})
		return sp, err
	}

	// geo alone is a rule; the code is lower-cased and a repeat dropped
	sp, err := clean(SplitRule{Geo: []string{" CN", "cn"}, Servers: srv})
	if err != nil {
		t.Fatal(err)
	}
	if len(sp) != 1 || !slices.Equal(sp[0].Geo, []string{"cn"}) || sp[0].Suffixes == nil || len(sp[0].Suffixes) != 0 {
		t.Errorf("geo-only rule = %+v", sp)
	}
	if b, _ := json.Marshal(sp[0]); string(b) != `{"suffixes":[],"geo":["cn"],"servers":[{"variant":"alidns/standard"}]}` {
		t.Errorf("stored as %s", b)
	}

	// geo and suffixes together
	sp, err = clean(SplitRule{Suffixes: []string{".CN", "example.cn"}, Geo: []string{"cn"}, Servers: srv})
	if err != nil || !slices.Equal(sp[0].Suffixes, []string{".cn", "example.cn"}) || !slices.Equal(sp[0].Geo, []string{"cn"}) {
		t.Errorf("geo + suffixes = %+v %v", sp, err)
	}

	// a rule with a suffix only is stored without the geo key, as before
	sp, err = clean(SplitRule{Suffixes: []string{".ru"}, Servers: srv})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(sp[0]); string(b) != `{"suffixes":[".ru"],"servers":[{"variant":"alidns/standard"}]}` {
		t.Errorf("suffix-only rule stored as %s", b)
	}

	// an unknown code, an empty code, and neither suffixes nor geo are errors
	for _, r := range []SplitRule{
		{Geo: []string{"xx"}, Servers: srv},
		{Suffixes: []string{".ru"}, Geo: []string{"cn", "zz"}, Servers: srv},
		{Geo: []string{""}, Servers: srv},
		{Servers: srv},
	} {
		if _, err := clean(r); err == nil {
			t.Errorf("rule %+v was accepted", r)
		}
	}
	if _, err := clean(SplitRule{Geo: []string{"xx"}, Servers: srv}); err == nil || !strings.Contains(err.Error(), "unknown country list") {
		t.Errorf("unknown code: %v", err)
	}
}

// The known lists are the single place for names: cn and ir, with the geosite names the data files use.
func TestGeoLists(t *testing.T) {
	if got := GeoCodes(); !slices.Equal(got, []string{"cn", "ir"}) {
		t.Errorf("codes = %v", got)
	}
	if geositeTag("ir") != "geosite-category-ir" || geoipTag("ir") != "geoip-ir" || geositeTag("cn") != "geosite-cn" || geoipTag("cn") != "geoip-cn" {
		t.Error("tags")
	}
}

// The first rule that names a code wins, like the first rule that names a suffix.
func TestClientPlanGeoFirstRuleWins(t *testing.T) {
	p := Preset{Name: "two", Transport: KindPlain, SplitDirect: true, Servers: variantServers("cloudflare/standard"), Split: []SplitRule{
		{Suffixes: []string{"a.example"}, Geo: []string{"cn"}, Servers: variantServers("alidns/standard")},
		{Geo: []string{"cn", "ir"}, Servers: variantServers("dnspod/standard")},
		{Geo: []string{"ir"}, Servers: variantServers("dnspod/standard")}, // every code claimed
	}}
	for _, c := range []Client{ClientXray, ClientSingbox, ClientMihomo} {
		_, split, _, err := p.ClientPlan(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(split) != 3 || !slices.Equal(split[0].Geo, []string{"cn"}) || !slices.Equal(split[1].Geo, []string{"ir"}) || len(split[2].Geo) != 0 {
			t.Errorf("%s: split = %+v", c, split)
		}
	}
	// SplitDirect off: the geo rule is ignored like any other
	p.SplitDirect = false
	_, split, notes, err := p.ClientPlan(ClientXray)
	if err != nil || len(split) != 0 || len(notes) == 0 {
		t.Errorf("split off: %v %v %v", split, notes, err)
	}
}

// What the stored JSON of a preset with no geo looks like is not touched by the new field: the stock Russian split reads and
// writes the exact text migration 00011 stored.
func TestSplitJSONWithoutGeoIsUnchanged(t *testing.T) {
	const stock = `[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"servers":[{"variant":"yandex/basic"}]}]`
	p, err := fromRow(rowOf(t, `[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]`, stock))
	if err != nil {
		t.Fatal(err)
	}
	row, err := toRow(p)
	if err != nil {
		t.Fatal(err)
	}
	if row.SplitJSON != stock {
		t.Errorf("split rewritten as %s", row.SplitJSON)
	}
	// and the new built-in's text reads back and writes back the same
	const cn = `[{"suffixes":[],"geo":["cn"],"servers":[{"variant":"alidns/standard"},{"variant":"dnspod/standard"}]}]`
	p, err = fromRow(rowOf(t, `[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]`, cn))
	if err != nil {
		t.Fatal(err)
	}
	if row, err = toRow(p); err != nil || row.SplitJSON != cn {
		t.Errorf("cn split rewritten as %s (%v)", row.SplitJSON, err)
	}
}

func TestGeoRenderMihomo(t *testing.T) {
	d := mustDNS(t, chinaSplit(), "G")
	// the main resolvers are Cloudflare and Google through the group; the Chinese ones are asked directly
	eq(t, "nameserver", d.Nameserver, []string{"1.1.1.1#G", "8.8.8.8#G", "1.0.0.1#G", "8.8.4.4#G"})
	if len(d.Policy) != 1 || d.Policy[0].Domain != "rule-set:geosite-cn" {
		t.Fatalf("policy = %+v", d.Policy)
	}
	eq(t, "policy servers", d.Policy[0].Servers, []string{"223.5.5.5", "119.29.29.29", "223.6.6.6"})
	if len(d.Direct) != 0 {
		t.Errorf("direct = %v", d.Direct)
	}
	goldenJSON(t, "mihomo_dns_china_geo.json", map[string]any{"policy": d.Policy, "providers": d.Providers, "rules": rulesOf(d.Providers), "notes": d.Notes})
}

func rulesOf(ps []MihomoProvider) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Rule())
	}
	return out
}

func TestGeoRenderXray(t *testing.T) {
	d, err := chinaSplit().XrayDNS()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Direct) != 0 || len(d.DirectGeo) != 1 || d.DirectGeo[0] != (XrayGeo{Site: "geosite:cn", IP: "geoip:cn"}) {
		t.Errorf("direct = %v geo = %v", d.Direct, d.DirectGeo)
	}
	last := d.Section.Servers[len(d.Section.Servers)-1]
	if !last.SkipFallback || !slices.Equal(last.Domains, []string{"geosite:cn"}) {
		t.Errorf("split server = %+v", last)
	}
	goldenJSON(t, "xray_dns_china_geo.json", map[string]any{
		"dns": d.Section, "direct": d.Direct, "directGeo": d.DirectGeo, "directIPs": d.DirectIPs, "notes": d.Notes,
	})

	// next to suffixes, the code joins the same server's domains
	p := chinaSplit()
	p.Split[0].Suffixes = []string{".cn"}
	d, err = p.XrayDNS()
	if err != nil {
		t.Fatal(err)
	}
	last = d.Section.Servers[len(d.Section.Servers)-1]
	if !slices.Equal(last.Domains, []string{"domain:cn", "geosite:cn"}) {
		t.Errorf("domains = %v", last.Domains)
	}
}

func TestGeoRenderSingbox(t *testing.T) {
	d, err := chinaSplit().SingboxDNS("Mistgate")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Section.Rules) != 1 || !slices.Equal(d.Section.Rules[0].RuleSet, []string{"geosite-cn"}) || d.Section.Rules[0].Server != "split-1" || len(d.Section.Rules[0].DomainSuffix) != 0 {
		t.Errorf("dns rules = %+v", d.Section.Rules)
	}
	if !slices.Equal(d.DirectSets, []string{"geosite-cn", "geoip-cn"}) || len(d.RuleSets) != 2 {
		t.Errorf("sets = %v %v", d.DirectSets, d.RuleSets)
	}
	goldenJSON(t, "singbox_dns_china_geo.json", map[string]any{
		"dns": d.Section, "direct": d.Direct, "ruleSets": d.RuleSets, "directSets": d.DirectSets, "notes": d.Notes,
	})

	// suffixes and the code are two rules to the same resolver, never one rule (its fields would all have to match)
	p := chinaSplit()
	p.Split[0].Suffixes = []string{".cn"}
	d, err = p.SingboxDNS("")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Section.Rules) != 2 || d.Section.Rules[0].DomainSuffix[0] != "cn" || d.Section.Rules[1].RuleSet[0] != "geosite-cn" || d.Section.Rules[0].Server != d.Section.Rules[1].Server {
		t.Errorf("dns rules = %+v", d.Section.Rules)
	}
}

// Happ: geosite:cn in DirectSites, geoip:cn in DirectIp, the first Chinese resolver as the Domestic DNS.
func TestGeoRenderHapp(t *testing.T) {
	h, err := HappRouting(chinaSplit())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"Name":"DNS","GlobalProxy":"true","RemoteDNSType":"DoU","RemoteDNSDomain":"","RemoteDNSIP":"1.1.1.1",` +
		`"DomesticDNSType":"DoU","DomesticDNSDomain":"","DomesticDNSIP":"223.5.5.5","LastUpdated":"","DnsHosts":{},` +
		`"DirectSites":["geosite:cn"],` +
		`"DirectIp":["geoip:cn"],"ProxySites":[],"ProxyIp":[],"BlockSites":[],"BlockIp":[],"DomainStrategy":"IPIfNonMatch","FakeDNS":"false"}`
	if got := decode(t, h); got != want {
		t.Errorf("happ cn:\n got %s\nwant %s", got, want)
	}
	// SplitDirect off: ignored, nothing direct
	off := chinaSplit()
	off.SplitDirect = false
	h, _ = HappRouting(off)
	if got := decode(t, h); got != goldenStandard {
		t.Errorf("happ cn, split off:\n got %s\nwant %s", got, goldenStandard)
	}
	// suffixes and the code together
	p := chinaSplit()
	p.Split[0].Suffixes = []string{".cn"}
	h, _ = HappRouting(p)
	if got := decode(t, h); !strings.Contains(got, `"DirectSites":["domain:cn","geosite:cn"],"DirectIp":["geoip:cn"]`) {
		t.Errorf("happ cn + suffix: %s", got)
	}
}

// The rest of the catalog: the two Chinese resolvers are plain only, regional, country CN; Yandex's regional one is RU.
func TestChinaCatalog(t *testing.T) {
	for id, ips := range map[string][]string{"alidns/standard": {"223.5.5.5", "223.6.6.6"}, "dnspod/standard": {"119.29.29.29"}} {
		v, _, ok := LookupVariant(id)
		if !ok || v.Category != CategoryRegional || v.Country != "CN" || !slices.Equal(v.IPv4, ips) || v.DoTHost != "" || v.DoHURL != "" || v.NoPlain {
			t.Errorf("%s = %+v %v", id, v, ok)
		}
	}
	if v, _, _ := LookupVariant("yandex/basic"); v.Category != CategoryRegional || v.Country != "RU" {
		t.Errorf("yandex/basic = %+v", v)
	}
	// a preferred DoH preset still carries them: they step down to plain
	p := chinaSplit()
	p.Transport = KindDoH
	if _, split, _, err := p.ClientPlan(ClientMihomo); err != nil || len(split) != 1 || split[0].Endpoints[0].Kind != KindPlain {
		t.Errorf("DoH preference: %v %v", split, err)
	}
	if CategoryOf(chinaSplit()) != CategoryRegional || CountryOf(chinaSplit()) != "CN" {
		t.Error("the built-in is listed under regional / CN")
	}
	if c := (Preset{Servers: variantServers("alidns/standard")}); CategoryOf(c) != CategoryRegional || CountryOf(c) != "CN" {
		t.Error("a custom preset takes the category and country of its first catalog server")
	}
}
