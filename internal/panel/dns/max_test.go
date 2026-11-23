package dns

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// BlockMax: the apps drop max.ru and oneme.ru before anything else, in the formats that have rules; AmneziaWG keys and a preset
// without the flag are not touched.

func maxPreset() Preset {
	return Preset{Name: "ru", Transport: KindPlain, SplitDirect: true, BlockMax: true,
		Servers: variantServers("cloudflare/standard", "google/standard"),
		Split:   []SplitRule{{Suffixes: []string{".ru"}, Servers: variantServers("yandex/basic")}}}
}

func TestBlockMaxMihomo(t *testing.T) {
	d := mustDNS(t, maxPreset(), "G")
	if !slices.Equal(d.BlockDomains, []string{"max.ru", "oneme.ru"}) || !slices.Equal(d.Direct, []string{"ru"}) {
		t.Fatalf("block %v direct %v", d.BlockDomains, d.Direct)
	}
	p := maxPreset()
	p.BlockMax = false
	if d := mustDNS(t, p, "G"); d.BlockDomains != nil {
		t.Errorf("flag off: %v", d.BlockDomains)
	}
}

func TestBlockMaxXray(t *testing.T) {
	d, err := maxPreset().XrayDNS()
	if err != nil || !slices.Equal(d.Block, []string{"domain:max.ru", "domain:oneme.ru"}) {
		t.Fatalf("%v %v", d.Block, err)
	}
	// next to the ad list
	p := maxPreset()
	p.BlockAds = true
	if d, _ := p.XrayDNS(); !slices.Equal(d.Block, []string{"geosite:category-ads-all", "domain:max.ru", "domain:oneme.ru"}) {
		t.Errorf("with ads: %v", d.Block)
	}
	p.BlockMax = false
	if d, _ := p.XrayDNS(); !slices.Equal(d.Block, []string{"geosite:category-ads-all"}) {
		t.Errorf("flag off: %v", d.Block)
	}
}

func TestBlockMaxSingbox(t *testing.T) {
	d, err := maxPreset().SingboxDNS("Mistgate")
	if err != nil || !slices.Equal(d.BlockDomains, []string{"max.ru", "oneme.ru"}) {
		t.Fatalf("%v %v", d.BlockDomains, err)
	}
	// the first DNS rule: NXDOMAIN from a predefined answer, before the split's route rule
	if r := d.Section.Rules[0]; r.Action != "predefined" || r.Rcode != "NXDOMAIN" || !slices.Equal(r.DomainSuffix, d.BlockDomains) {
		t.Errorf("first dns rule = %+v", r)
	}
	p := maxPreset()
	p.BlockMax = false
	if d, _ := p.SingboxDNS(""); d.BlockDomains != nil || d.Section.Rules[0].Action == "predefined" {
		t.Errorf("flag off: %v %+v", d.BlockDomains, d.Section.Rules)
	}
}

func TestBlockMaxHapp(t *testing.T) {
	h, err := HappRouting(maxPreset())
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, h)
	if !strings.Contains(got, `"DirectSites":["domain:ru"]`) || !strings.Contains(got, `"BlockSites":["domain:max.ru","domain:oneme.ru"]`) {
		t.Errorf("happ: %s", got)
	}
	p := maxPreset()
	p.BlockMax = false
	h, _ = HappRouting(p)
	if got := decode(t, h); !strings.Contains(got, `"BlockSites":[]`) {
		t.Errorf("flag off: %s", got)
	}
}

func TestBlockMaxLeavesAmneziaWGAlone(t *testing.T) {
	on, off := maxPreset(), maxPreset()
	off.BlockMax = false
	a, _ := on.EndpointsFor(ClientAmneziaWG)
	b, _ := off.EndpointsFor(ClientAmneziaWG)
	if len(a) == 0 || !reflect.DeepEqual(a, b) {
		t.Errorf("amneziawg endpoints differ: %v / %v", a, b)
	}
}

// block_max: Create stores it, Update without the field keeps it (a client that predates it), with the field sets it either way, and
// an own preset made without it stays off. Of the built-ins only the two Russian splits that go direct have it on, and the owner can
// switch one off.
func TestBlockMaxService(t *testing.T) {
	e := newEnv(t)
	create := func(name string, on bool) string {
		t.Helper()
		c, err := e.s.CreateDnsPreset(e.ctx, connect.NewRequest(&adminv1.CreateDnsPresetRequest{Name: name, Servers: []*adminv1.DnsServer{plain("9.9.9.9")}, BlockMax: on}))
		if err != nil || c.Msg.Preset.BlockMax != on {
			t.Fatalf("create %s: %v %v", name, c, err)
		}
		return c.Msg.Preset.Id
	}
	id, off := create("Max", true), create("Plain", false)
	update := func(id string, block *bool) *adminv1.DnsPreset {
		t.Helper()
		u, err := e.s.UpdateDnsPreset(e.ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: id, Name: e.list()[id].Name, Servers: []*adminv1.DnsServer{plain("9.9.9.9")}, BlockMax: block}))
		if err != nil {
			t.Fatal(err)
		}
		return u.Msg.Preset
	}
	f, tr := false, true
	if !update(id, nil).BlockMax || update(off, nil).BlockMax {
		t.Error("update without the field changed block_max")
	}
	if update(id, &f).BlockMax || !update(id, &tr).BlockMax || !e.list()[id].BlockMax {
		t.Error("update with the field did not set it")
	}
	if p := builtin(t, "dns_builtin_ru_geo"); !p.BlockMax {
		t.Errorf("fromRow lost block_max: %+v", p)
	}
	for id, b := range e.list() {
		if b.Builtin && b.BlockMax != (id == "dns_builtin_ru_split" || id == "dns_builtin_ru_geo") {
			t.Errorf("built-in %s block_max = %v", id, b.BlockMax)
		}
	}
	if update("dns_builtin_ru_split", &f).BlockMax {
		t.Error("the owner cannot switch a built-in off")
	}
}
