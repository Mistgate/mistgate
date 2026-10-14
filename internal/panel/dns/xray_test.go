package dns

import "testing"

// Golden dns block (and the notes and routing helpers) of every shared preset.
func TestXrayDNSGolden(t *testing.T) {
	for _, pp := range planPresets {
		t.Run(pp.name, func(t *testing.T) {
			d, err := pp.p.XrayDNS()
			if pp.name == "empty" {
				if err == nil {
					t.Fatal("empty preset: want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			goldenJSON(t, "xray_dns_"+pp.name+".json", map[string]any{
				"dns": d.Section, "direct": d.Direct, "directIPs": d.DirectIPs, "notes": d.Notes,
			})
		})
	}
}

func TestXrayDNSShape(t *testing.T) {
	d, err := presetNamed("russia_split").XrayDNS()
	if err != nil {
		t.Fatal(err)
	}
	s := d.Section
	if s.Tag != "dns-internal" || s.QueryStrategy != "UseIPv4" {
		t.Errorf("tag/queryStrategy = %q/%q", s.Tag, s.QueryStrategy)
	}
	if len(s.Servers) != 4 {
		t.Fatalf("servers = %+v", s.Servers)
	}
	for _, m := range s.Servers[:2] {
		if m.SkipFallback || len(m.Domains) != 0 {
			t.Errorf("main server %+v must be a plain fallback entry", m)
		}
	}
	for _, sp := range s.Servers[2:] {
		if !sp.SkipFallback || len(sp.Domains) != 3 || sp.Domains[0] != "domain:ru" {
			t.Errorf("split server %+v", sp)
		}
	}
	eq(t, "direct", d.Direct, []string{"ru", "su", "xn--p1ai"})
	eq(t, "directIPs", d.DirectIPs, []string{"77.88.8.8", "77.88.8.1"})
}

// A DoH host name is pinned in hosts, an IP-literal host is not.
func TestXrayDNSHosts(t *testing.T) {
	d, err := presetNamed("custom_port_dot_doh").XrayDNS()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Section.Hosts) != 1 || d.Section.Hosts["dns.adguard-dns.com"] != "94.140.14.14" {
		t.Errorf("hosts = %v", d.Section.Hosts)
	}
}
