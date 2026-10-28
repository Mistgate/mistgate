package dns

import (
	"net/netip"
	"net/url"
	"slices"
)

// The dns block of an Xray JSON config. The subscription assembler (internal/panel/subs) puts Section under "dns"
// and builds the routing rules from the rest; nothing here knows the rest of the config.
//
// What an Xray client does with a preset (schema read from xray-core infra/conf dns.go, v1.260327.0):
//
//   - servers: every main endpoint, in order, as a fallback list: Xray asks the next one when a query fails. Plain
//     addresses on any port, DoH as its URL. No DoT. IPv6 resolver addresses are not used (queryStrategy UseIPv4).
//   - a split rule becomes servers with "domains" (domain:<suffix>) and skipFallback: its resolver answers only its
//     suffixes and is never asked for anything else. Only with SplitDirect on, like Mihomo and Happ.
//   - hosts: the DoH host names -> an IPv4 address, so that the name needs no lookup of its own.
//   - tag: the inbound tag of the resolver's own traffic, so that routing rules (inboundTag) can send the main
//     resolvers through the proxy and the split resolvers direct.

// XrayDNSTag is the "tag" of the dns block: routing rules match the resolvers' traffic with it.
const XrayDNSTag = "dns-internal"

// XrayDNS is the dns block of one preset and what the assembler needs around it.
type XrayDNS struct {
	Section XraySection
	// Direct are the suffix bodies (lower-case punycode, no leading dot) whose traffic goes direct; they are exactly
	// the suffixes of the split servers.
	Direct []string
	// DirectGeo are the lists (geo.go) whose sites and addresses go direct: "geosite:cn" joins the domain rule, "geoip:cn" is
	// a rule of its own (an ip rule; the config's domainStrategy AsIs keeps it to connections made to an address).
	DirectGeo []XrayGeo
	// DirectIPs are the addresses of the split resolvers: the routing rule for XrayDNSTag sends them direct (the
	// main resolvers' traffic goes through the proxy).
	DirectIPs []string
	// Notes (English) say what of the preset an Xray config cannot carry.
	Notes []string
}

// XrayGeo is one country list as Xray names it.
type XrayGeo struct{ Site, IP string }

// XraySection is the JSON of the "dns" key.
type XraySection struct {
	Tag           string            `json:"tag"`
	QueryStrategy string            `json:"queryStrategy"`
	Hosts         map[string]string `json:"hosts,omitempty"`
	Servers       []XrayServer      `json:"servers"`
}

// XrayServer is one entry of dns.servers (the object form).
type XrayServer struct {
	Address      string   `json:"address"`
	Port         int      `json:"port,omitempty"`
	Domains      []string `json:"domains,omitempty"`
	SkipFallback bool     `json:"skipFallback,omitempty"`
}

// XrayDNS renders the preset for an Xray config. An error means no main server can be expressed; the caller leaves
// the dns block out.
func (p Preset) XrayDNS() (XrayDNS, error) {
	main, split, notes, err := p.ClientPlan(ClientXray)
	if err != nil {
		return XrayDNS{}, err
	}
	out := XrayDNS{Notes: notes, Section: XraySection{Tag: XrayDNSTag, QueryStrategy: "UseIPv4"}}
	for _, e := range main {
		out.Section.Servers = append(out.Section.Servers, out.server(e, nil, false))
	}
	for _, sp := range split {
		if len(sp.Suffixes) == 0 && len(sp.Geo) == 0 {
			continue
		}
		domains := make([]string, len(sp.Suffixes))
		for i, b := range sp.Suffixes {
			domains[i] = "domain:" + b
		}
		for _, code := range sp.Geo {
			g := XrayGeo{Site: "geosite:" + geoLists[code].Site, IP: "geoip:" + geoLists[code].IP}
			domains = append(domains, g.Site)
			out.DirectGeo = append(out.DirectGeo, g)
		}
		for _, e := range sp.Endpoints {
			out.Section.Servers = append(out.Section.Servers, out.server(e, domains, true))
			out.DirectIPs = appendUniq(out.DirectIPs, endpointIP(e))
		}
		out.Direct = append(out.Direct, sp.Suffixes...)
	}
	return out, nil
}

// server writes an endpoint as an entry and records the host pin of a DoH host name.
func (o *XrayDNS) server(e Endpoint, domains []string, skipFallback bool) XrayServer {
	s := XrayServer{Domains: domains, SkipFallback: skipFallback}
	if e.Kind == KindDoH {
		s.Address = e.Address
		if u, err := url.Parse(e.Address); err == nil {
			if _, err := netip.ParseAddr(u.Hostname()); err != nil {
				if o.Section.Hosts == nil {
					o.Section.Hosts = map[string]string{}
				}
				if _, ok := o.Section.Hosts[u.Hostname()]; !ok { // same host, same pin: the first endpoint's
					o.Section.Hosts[u.Hostname()] = endpointIP(e)
				}
			}
		}
		return s
	}
	addr, port := plainAddr(e.Address)
	s.Address = addr
	if port != 53 {
		s.Port = port
	}
	return s
}

// plainAddr splits a plain endpoint's "ip" / "ip:port" (port 53 when absent).
func plainAddr(a string) (string, int) {
	if ap, err := netip.ParseAddrPort(a); err == nil {
		return ap.Addr().String(), int(ap.Port())
	}
	return a, 53
}

// endpointIP is the address a client dials for an endpoint: the plain address itself, else the first IPv4 address
// of the DoH/DoT host.
func endpointIP(e Endpoint) string {
	if e.Kind == KindPlain {
		a, _ := plainAddr(e.Address)
		return a
	}
	if i := slices.IndexFunc(e.IPs, isIPv4); i >= 0 {
		return e.IPs[i]
	}
	return ""
}
