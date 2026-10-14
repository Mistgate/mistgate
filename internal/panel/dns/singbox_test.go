package dns

import "testing"

func TestSingboxDNSGolden(t *testing.T) {
	for _, pp := range planPresets {
		t.Run(pp.name, func(t *testing.T) {
			d, err := pp.p.SingboxDNS("Mistgate")
			if pp.name == "empty" {
				if err == nil {
					t.Fatal("empty preset: want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "singbox_dns_"+pp.name+".json", map[string]any{
				"dns": d.Section, "direct": d.Direct, "notes": d.Notes,
			})
		})
	}
}

func TestSingboxDNSShape(t *testing.T) {
	d, err := presetNamed("russia_split").SingboxDNS("G")
	if err != nil {
		t.Fatal(err)
	}
	s := d.Section
	if s.Final != "remote" || s.Strategy != "ipv4_only" || len(s.Servers) != 3 || len(s.Rules) != 1 {
		t.Fatalf("section = %+v", s)
	}
	if s.Servers[0].Detour != "G" || s.Servers[1].Detour != "" || s.Servers[1].Tag != "split-1" || s.Servers[2].Type != "local" {
		t.Errorf("servers = %+v", s.Servers)
	}
	if r := s.Rules[0]; r.Server != "split-1" || r.Action != "route" || len(r.DomainSuffix) != 3 {
		t.Errorf("rule = %+v", r)
	}
	// no group: no detour
	d, _ = presetNamed("standard").SingboxDNS("")
	if d.Section.Servers[0].Detour != "" {
		t.Errorf("detour = %q", d.Section.Servers[0].Detour)
	}
}
