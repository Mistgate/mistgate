package dns

import (
	"slices"
	"strings"
	"testing"
)

// BlockAds: the apps drop the domains of category-ads-all themselves. The goldens are the proof of the shape; the flag off changes
// nothing (the other goldens of the package are the proof of that).

func adsPresets() map[string]Preset {
	std := Preset{Name: "ads", Servers: variantServers("cloudflare/standard", "google/standard"), Transport: KindPlain, BlockAds: true}
	cn := chinaSplit()
	cn.BlockAds = true
	return map[string]Preset{"ads": std, "china_ads": cn}
}

func TestBlockAdsMihomo(t *testing.T) {
	for name, p := range adsPresets() {
		d := mustDNS(t, p, "G")
		if len(d.Block) != 1 || d.Block[0].Name != "geosite-category-ads-all" || d.Block[0].Behavior != "domain" ||
			d.Block[0].URL != "https://github.com/MetaCubeX/meta-rules-dat/raw/meta/geo/geosite/category-ads-all.mrs" {
			t.Fatalf("%s: block = %+v", name, d.Block)
		}
		if got := d.Block[0].RuleTo("REJECT"); got != "RULE-SET,geosite-category-ads-all,REJECT" {
			t.Errorf("%s: rule = %q", name, got)
		}
		goldenJSON(t, "mihomo_dns_"+name+".json", map[string]any{
			"policy": d.Policy, "providers": d.Providers, "rules": rulesOf(d.Providers), "block": d.Block, "blockRules": []string{d.Block[0].RuleTo("REJECT")}, "notes": d.Notes,
		})
	}
	// the block does not depend on the split switch, and the flag off leaves nothing
	p := adsPresets()["china_ads"]
	p.SplitDirect = false
	if d := mustDNS(t, p, "G"); len(d.Block) != 1 || len(d.Providers) != 0 {
		t.Errorf("split off: block %v providers %v", d.Block, d.Providers)
	}
	if d := mustDNS(t, chinaSplit(), "G"); d.Block != nil {
		t.Errorf("flag off: block = %v", d.Block)
	}
}

func TestBlockAdsXray(t *testing.T) {
	for name, p := range adsPresets() {
		d, err := p.XrayDNS()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(d.Block, []string{"geosite:category-ads-all"}) {
			t.Fatalf("%s: block = %v", name, d.Block)
		}
		goldenJSON(t, "xray_dns_"+name+".json", map[string]any{
			"dns": d.Section, "direct": d.Direct, "directGeo": d.DirectGeo, "directIPs": d.DirectIPs, "block": d.Block, "notes": d.Notes,
		})
	}
	p := adsPresets()["china_ads"]
	p.SplitDirect = false
	if d, err := p.XrayDNS(); err != nil || len(d.Block) != 1 {
		t.Errorf("split off: %v %v", d.Block, err)
	}
	if d, _ := chinaSplit().XrayDNS(); d.Block != nil {
		t.Errorf("flag off: block = %v", d.Block)
	}
}

func TestBlockAdsSingbox(t *testing.T) {
	for name, p := range adsPresets() {
		d, err := p.SingboxDNS("Mistgate")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(d.BlockSets, []string{"geosite-category-ads-all"}) || d.RuleSets[0].Tag != "geosite-category-ads-all" ||
			d.RuleSets[0].URL != "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs" {
			t.Fatalf("%s: sets = %v %v", name, d.BlockSets, d.RuleSets)
		}
		// the first DNS rule: NXDOMAIN from a predefined answer (a DNS reject turns into drop after 50 hits in 30 s)
		if r := d.Section.Rules[0]; r.Action != "predefined" || r.Rcode != "NXDOMAIN" || r.Server != "" || !slices.Equal(r.RuleSet, d.BlockSets) {
			t.Errorf("%s: first dns rule = %+v", name, r)
		}
		for _, r := range d.Section.Rules[1:] {
			if r.Action == "predefined" {
				t.Errorf("%s: a second predefined rule: %+v", name, r)
			}
		}
		goldenJSON(t, "singbox_dns_"+name+".json", map[string]any{
			"dns": d.Section, "direct": d.Direct, "ruleSets": d.RuleSets, "directSets": d.DirectSets, "blockSets": d.BlockSets, "notes": d.Notes,
		})
	}
	// the country lists keep their sets after the ad list's
	d, _ := adsPresets()["china_ads"].SingboxDNS("")
	if tags := []string{d.RuleSets[0].Tag, d.RuleSets[1].Tag, d.RuleSets[2].Tag}; !slices.Equal(tags, []string{"geosite-category-ads-all", "geosite-cn", "geoip-cn"}) {
		t.Errorf("rule sets = %v", tags)
	}
	p := adsPresets()["china_ads"]
	p.SplitDirect = false
	if d, err := p.SingboxDNS(""); err != nil || len(d.BlockSets) != 1 || len(d.DirectSets) != 0 {
		t.Errorf("split off: %+v %v", d, err)
	}
	if d, _ := chinaSplit().SingboxDNS(""); d.BlockSets != nil || len(d.RuleSets) != 2 {
		t.Errorf("flag off: %v %v", d.BlockSets, d.RuleSets)
	}
}

func TestBlockAdsHapp(t *testing.T) {
	std := adsPresets()["ads"]
	h, err := HappRouting(std)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"Name":"DNS","GlobalProxy":"true","RemoteDNSType":"DoU","RemoteDNSDomain":"","RemoteDNSIP":"1.1.1.1",` +
		`"DomesticDNSType":"DoU","DomesticDNSDomain":"","DomesticDNSIP":"1.1.1.1","LastUpdated":"","DnsHosts":{},` +
		`"DirectSites":[],"DirectIp":[],"ProxySites":[],"ProxyIp":[],"BlockSites":["geosite:category-ads-all"],"BlockIp":[],` +
		`"DomainStrategy":"IPIfNonMatch","FakeDNS":"false"}`
	if got := decode(t, h); got != want {
		t.Errorf("happ ads:\n got %s\nwant %s", got, want)
	}
	// with a country list the block sits next to the direct lists
	h, _ = HappRouting(adsPresets()["china_ads"])
	if got := decode(t, h); !strings.Contains(got, `"DirectSites":["geosite:cn"],"DirectIp":["geoip:cn"],"ProxySites":[],"ProxyIp":[],"BlockSites":["geosite:category-ads-all"]`) {
		t.Errorf("happ cn + ads: %s", got)
	}
	// the flag off is the plain profile
	std.BlockAds = false
	h, _ = HappRouting(std)
	if got := decode(t, h); got != goldenStandard {
		t.Errorf("flag off:\n got %s\nwant %s", got, goldenStandard)
	}
}
