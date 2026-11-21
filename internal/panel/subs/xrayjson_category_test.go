package subs

import (
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// A site category alone (dns.XrayGeo with no IP) joins the direct domain rule and writes no ip rule for it.
func TestXrayRulesCategoryHasNoIPRule(t *testing.T) {
	d := &dns.XrayDNS{
		Direct: []string{"example.com"},
		DirectGeo: []dns.XrayGeo{
			{Site: "geosite:telegram"},
			{Site: "geosite:category-ru", IP: "geoip:ru"},
		},
	}
	var domains []string
	var ipRules [][]string
	for _, r := range xrayRules(d) {
		if r.OutboundTag != "direct" {
			continue
		}
		if len(r.Domain) > 0 {
			domains = r.Domain
		}
		if len(r.IP) > 0 && !slices.Contains(r.IP, "geoip:private") && slices.ContainsFunc(r.IP, func(s string) bool { return s == "geoip:ru" }) {
			ipRules = append(ipRules, r.IP)
		}
	}
	if !slices.Equal(domains, []string{"domain:example.com", "geosite:telegram", "geosite:category-ru"}) {
		t.Errorf("domains = %v", domains)
	}
	if len(ipRules) != 1 || !slices.Equal(ipRules[0], []string{"geoip:ru"}) {
		t.Errorf("ip rules = %v", ipRules)
	}

	for _, r := range xrayRules(&dns.XrayDNS{DirectGeo: []dns.XrayGeo{{Site: "geosite:telegram"}}}) {
		if slices.Contains(r.IP, "") {
			t.Errorf("empty ip entry in %+v", r)
		}
	}
}
