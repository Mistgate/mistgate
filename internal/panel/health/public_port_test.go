package health

import (
	"net/netip"
	"testing"

	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

func TestCountryAndOwnProbesUsePublicPort(t *testing.T) {
	c := newCountryEnv(t)
	c.node("de1", "provider-a", true)
	c.vlessInboundWith("de1", 19443, func(s *panelvless.Settings) {
		s.ClientPort = 443
		s.Reality.TargetKind, s.Reality.Target = "site", "www.example.org:443"
		s.Reality.ServerNames = []string{"www.example.org"}
	})
	c.names["de1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	country, err := c.s.CountryTargets(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(country) != 1 || len(country[0].Ports) != 1 || country[0].Ports[0] != 443 {
		t.Errorf("country probe uses internal listener instead of public entry: %+v", country)
	}
	own, err := c.s.ProbeTargets(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range own {
		if x.Port != 443 {
			t.Errorf("own probe uses internal listener instead of public entry: %+v", x)
		}
	}
}
