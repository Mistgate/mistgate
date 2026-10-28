package dns

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
)

// The dns section of a Mihomo profile and the rules that go with it. The subscription
// assembler (internal/panel/subs) turns this into YAML nodes; nothing here knows YAML.
//
// What a Mihomo client does with a preset:
//
//   - nameserver: the main servers, each with "#<group>" so that the queries travel through the tunnel (a DoH query to
//     1.1.1.1 from a Russian home line is blocked, the same query through a node is not). The "#group" syntax is read
//     from the core's source (config.parseNameServer); the end-to-end test checks the interplay with a real tunnel.
//   - nameserver-policy: the split rules' suffixes -> the split rule's servers, WITHOUT "#group" (they are queried
//     directly), and only when the preset's SplitDirect is on (the same switch as Happ: only then do the split's domains
//     bypass the VPN; off = the split is ignored). The same suffixes become DOMAIN-SUFFIX,<s>,DIRECT rules.
//   - a country list of a split rule (geo.go) is two rule providers (mrs, downloaded through the tunnel group), a
//     nameserver-policy entry "rule-set:geosite-<name>" and two RULE-SET rules to DIRECT, the geoip one with no-resolve
//     (a domain is not looked up to be checked against it). Not GEOSITE,<name>: the core fetches geosite.dat itself at start,
//     with no way to say through what, and from a censored network that does not work.
//   - default-nameserver: plain addresses that resolve the NAMES of the DoH/DoT servers (the bootstrap IPv4 addresses
//     the catalog knows for them).
//   - proxy-server-nameserver: plain main servers (the names of our own nodes are resolved without the tunnel, which
//     does not exist yet when the core connects), "system" when the preset has no plain server.
//
// Some Mihomo-based apps ignore the whole block (they build their own dns, groups and rules); Clash Verge and the like merge it.

// MihomoDNS is the dns section and the direct suffixes of one preset.
type MihomoDNS struct {
	Nameserver            []string
	DefaultNameserver     []string // may be empty: the core's own defaults apply
	ProxyServerNameserver []string // never empty ("system" at worst)
	Policy                []MihomoPolicy
	// Direct are the suffix bodies (lower-case punycode, no leading dot) whose traffic goes DIRECT; they are exactly
	// the suffixes of Policy.
	Direct []string
	// Providers are the rule providers of the country lists, and the RULE-SET rules to DIRECT that go with them, in order.
	Providers []MihomoProvider
	// Notes (English) say what of the preset a Mihomo profile cannot carry.
	Notes []string
}

// MihomoProvider is one rule provider of a country list: a key of rule-providers.
type MihomoProvider struct {
	Name     string // "geosite-cn": the key of rule-providers and the name RULE-SET refers to
	Behavior string // "domain" or "ipcidr"
	URL      string
}

// Rule is the rule line that sends the provider's traffic DIRECT. The address list is matched without a lookup.
func (p MihomoProvider) Rule() string {
	if p.Behavior == "ipcidr" {
		return "RULE-SET," + p.Name + ",DIRECT,no-resolve"
	}
	return "RULE-SET," + p.Name + ",DIRECT"
}

// MihomoProviderInterval is how often a provider is refreshed, in seconds.
const MihomoProviderInterval = 86400

// MihomoPolicy is one nameserver-policy entry: Domain is "+.<suffix>" (the suffix and its subdomains).
type MihomoPolicy struct {
	Domain  string
	Servers []string
}

// maxBootstrap bounds default-nameserver.
const maxBootstrap = 4

// MihomoDNS renders the preset for a Mihomo profile. group is the proxy group the main servers' queries go through
// ("" = no "#group", the queries use the core's own routing). An error means no main server can be expressed; the
// caller leaves the dns section out.
func (p Preset) MihomoDNS(group string) (MihomoDNS, error) {
	main, split, notes, err := p.ClientPlan(ClientMihomo)
	if err != nil {
		return MihomoDNS{}, err
	}
	out := MihomoDNS{Notes: notes}
	boot := slices.Clone(main)
	for _, e := range main {
		out.Nameserver = appendUniq(out.Nameserver, mihomoServer(e, group))
		if e.Kind == KindPlain {
			out.ProxyServerNameserver = appendUniq(out.ProxyServerNameserver, mihomoServer(e, ""))
		}
	}
	for _, sp := range split {
		var servers []string
		for _, e := range sp.Endpoints {
			servers = appendUniq(servers, mihomoServer(e, ""))
			boot = append(boot, e)
		}
		for _, body := range sp.Suffixes {
			out.Direct = append(out.Direct, body)
			out.Policy = append(out.Policy, MihomoPolicy{Domain: "+." + body, Servers: servers})
		}
		for _, code := range sp.Geo {
			site, ip := geositeTag(code), geoipTag(code)
			out.Policy = append(out.Policy, MihomoPolicy{Domain: "rule-set:" + site, Servers: servers})
			out.Providers = append(out.Providers,
				MihomoProvider{Name: site, Behavior: "domain", URL: fmt.Sprintf(mihomoGeositeURL, geoLists[code].Site)},
				MihomoProvider{Name: ip, Behavior: "ipcidr", URL: fmt.Sprintf(mihomoGeoipURL, geoLists[code].IP)})
		}
	}
	if len(out.ProxyServerNameserver) == 0 {
		out.ProxyServerNameserver = []string{"system"}
	}
	out.DefaultNameserver = bootstrapIPs(boot)
	return out, nil
}

// mihomoServer is the nameserver string of an endpoint: plain "ip" / "ip:port" (IPv6 in brackets), DoT as tls://,
// DoH as its URL; "#group" (percent-encoded: the core reads it as a URL fragment) when group is not empty.
func mihomoServer(e Endpoint, group string) string {
	var s string
	switch e.Kind {
	case KindDoH:
		s = e.Address
	case KindDoT:
		s = "tls://" + hostPort(e.Address)
	default:
		s = hostPort(e.Address)
	}
	if group != "" {
		s += "#" + (&url.URL{Fragment: group}).EscapedFragment()
	}
	return s
}

// hostPort brackets a bare IPv6 address ("ip:port" and "[v6]:port" are already fine; a name is left alone).
func hostPort(addr string) string {
	if a, err := netip.ParseAddr(addr); err == nil && a.Is6() {
		return "[" + a.String() + "]"
	}
	return addr
}

// bootstrapIPs collects the IPv4 addresses of the DoH/DoT hosts of the endpoints, at most maxBootstrap (the profile
// switches IPv6 off, so an IPv6 bootstrap address would be of no use).
func bootstrapIPs(eps []Endpoint) []string {
	var out []string
	for _, e := range eps {
		if e.Kind == KindPlain {
			continue
		}
		for _, ip := range e.IPs {
			if a, err := netip.ParseAddr(ip); err == nil && a.Is4() && len(out) < maxBootstrap {
				out = appendUniq(out, a.String())
			}
		}
	}
	return out
}

func appendUniq(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}
