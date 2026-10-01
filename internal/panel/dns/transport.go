package dns

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
)

// Which resolvers an app gets. One function, Resolve, turns a preset's servers into the
// endpoints a given client can carry; the Happ routing header uses it now, the Mihomo YAML and AmneziaWG
// renderers call it with their Client and format the result.
//
// The rule: a preset has one preferred transport. A catalog server is offered in that transport if the client
// can carry it and the variant has an endpoint for it, else in the next one down the chain DoH -> DoT -> plain
// (see order for the last-resort step up). A custom server has a fixed kind: it is used as it is when the client
// can carry that kind and dropped (with a note) when not.

// Client is an app that carries the DNS of a subscription.
type Client string

const (
	ClientHapp      Client = "happ"
	ClientMihomo    Client = "mihomo" // the Mihomo core and apps built on it
	ClientAmneziaWG Client = "amneziawg"
)

// Clients lists the clients in display order.
var Clients = []Client{ClientHapp, ClientMihomo, ClientAmneziaWG}

// chain is the fallback order of transports.
var chain = []Kind{KindDoH, KindDoT, KindPlain}

// Transports is what a client can carry. Sources: the client config formats (Mihomo dns:, Amnezia dns1/dns2),
// happ.go (Happ has plain and DoH, no DoT).
func (c Client) Transports() []Kind {
	switch c {
	case ClientHapp:
		return []Kind{KindPlain, KindDoH}
	case ClientMihomo:
		return []Kind{KindPlain, KindDoT, KindDoH}
	}
	return []Kind{KindPlain} // AmneziaWG: a dns1/dns2 pair of IPv4 addresses, nothing else
}

// Supports reports whether the client can carry a transport.
func (c Client) Supports(k Kind) bool { return slices.Contains(c.Transports(), k) }

// IPv6 reports whether IPv6 resolver addresses are usable: Mihomo takes them; Happ's profile and Amnezia's
// key and .conf are IPv4 only.
func (c Client) IPv6() bool { return c == ClientMihomo }

// Endpoint is one resolver as a client gets it.
type Endpoint struct {
	Kind    Kind   // the transport actually used
	Address string // plain: "ip" or "ip:port"; DoH: the URL; DoT: "host" or "host:port"
	// IPs of the DoH/DoT host (Happ and Mihomo want an address next to a host name); empty for plain and for a host
	// the catalog does not know.
	IPs []string
	// Variant is the catalog id it came from, "" for a custom server.
	Variant string
}

// order returns the transports to try for a preferred one: itself, then down the chain (DoH -> DoT -> plain), and
// only as a last resort the ones above it, so a variant that has nothing further down (Mullvad has no plain DNS)
// is still carried instead of dropped.
func order(pref Kind) []Kind {
	i := slices.Index(chain, pref)
	if i < 0 {
		i = len(chain) - 1 // plain
	}
	return append(slices.Clone(chain[i:]), chain[:i]...)
}

// variantEndpoints lists the endpoints of a variant in one transport (none when it has no such endpoint).
func variantEndpoints(v Variant, k Kind, ipv6 bool) []Endpoint {
	switch k {
	case KindPlain:
		if v.NoPlain {
			return nil
		}
		var out []Endpoint
		for _, a := range v.addresses(ipv6) {
			out = append(out, Endpoint{Kind: KindPlain, Address: a, Variant: v.ID})
		}
		return out
	case KindDoT:
		if v.DoTHost != "" {
			return []Endpoint{{Kind: KindDoT, Address: v.dotAddress(), IPs: v.addresses(ipv6), Variant: v.ID}}
		}
	case KindDoH:
		if v.DoHURL != "" {
			return []Endpoint{{Kind: KindDoH, Address: v.DoHURL, IPs: v.addresses(ipv6), Variant: v.ID}}
		}
	}
	return nil
}

// customEndpoint is a custom server as an endpoint, with the bootstrap addresses it can be given.
func customEndpoint(s Server) Endpoint {
	e := Endpoint{Kind: s.Kind, Address: s.Address}
	switch s.Kind {
	case KindDoH:
		if u, err := url.Parse(s.Address); err == nil {
			e.IPs = hostIPs(KindDoH, u.Hostname())
		}
	case KindDoT:
		h := s.Address
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		e.IPs = hostIPs(KindDoT, h)
	}
	return e
}

// hostIPs are the addresses of a DoH/DoT host: the host itself when it is an IP literal, else what the catalog
// knows about that host name (so a hand-typed "https://dns.adguard-dns.com/dns-query" still gets a bootstrap IP).
func hostIPs(k Kind, host string) []string {
	if a, err := netip.ParseAddr(host); err == nil {
		return []string{a.String()}
	}
	for _, p := range catalog {
		for _, v := range p.Variants {
			if (k == KindDoH && v.DoHURL != "" && hostEq(v.DoHURL, host)) || (k == KindDoT && v.DoTHost == host) {
				return v.addresses(false)
			}
		}
	}
	return nil
}

func hostEq(rawURL, host string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Hostname() == host
}

// reject says why a client cannot use an endpoint ("" = it can).
func (c Client) reject(e Endpoint) string {
	if e.Kind == KindPlain {
		ap, err := netip.ParseAddrPort(e.Address)
		if err == nil && (c == ClientAmneziaWG || (c == ClientHapp && ap.Port() != 53)) {
			return "port"
		}
		a := ap.Addr()
		if err != nil {
			a, _ = netip.ParseAddr(e.Address)
		}
		if a.Is6() && !c.IPv6() {
			return "IPv6"
		}
		return ""
	}
	if c == ClientHapp && e.Kind == KindDoH && len(e.IPs) == 0 {
		return "no bootstrap IP" // Happ wants an address next to the DoH URL
	}
	return ""
}

// Resolve turns a server list (the main servers of a preset, or one split rule's) into the endpoints a client
// can carry, best first, plus notes (English) on what was dropped. pref is the preset's preferred transport,
// ipv4Only drops IPv6 addresses. The order follows the server list, round-robin: the first endpoint of every
// server, then the second of every server. A plain variant has two addresses, so the first two plain
// endpoints of [Cloudflare, Google] are 1.1.1.1 and 8.8.8.8 (what AmneziaWG's two-address DNS wants).
func Resolve(c Client, pref Kind, servers []Server, ipv4Only bool) (out []Endpoint, notes []string) {
	ipv6 := c.IPv6() && !ipv4Only
	var groups [][]Endpoint
	for _, s := range servers {
		var g []Endpoint
		if s.Variant != "" {
			v, _, ok := LookupVariant(s.Variant)
			if !ok {
				notes = append(notes, fmt.Sprintf("server %s skipped: not in the catalog", s.Variant))
				continue
			}
			for _, k := range order(pref) {
				if !c.Supports(k) {
					continue
				}
				if g = variantEndpoints(v, k, ipv6); len(g) > 0 {
					break
				}
			}
			if len(g) == 0 {
				notes = append(notes, fmt.Sprintf("server %s skipped: no endpoint this client can carry", s.Variant))
				continue
			}
		} else {
			if !c.Supports(s.Kind) {
				notes = append(notes, fmt.Sprintf("server %s skipped: %s not supported", s.Address, kindName(s.Kind)))
				continue
			}
			e := customEndpoint(s)
			if why := c.reject(e); why != "" {
				notes = append(notes, fmt.Sprintf("server %s skipped: %s", s.Address, why))
				continue
			}
			if e.Kind == KindPlain && ipv4Only {
				if a, err := netip.ParseAddr(e.Address); err == nil && a.Is6() {
					notes = append(notes, fmt.Sprintf("server %s skipped: ipv4_only", s.Address))
					continue
				}
			}
			g = []Endpoint{e}
		}
		groups = append(groups, g)
	}
	for i := 0; ; i++ {
		added := false
		for _, g := range groups {
			if i < len(g) {
				out = append(out, g[i])
				added = true
			}
		}
		if !added {
			return out, notes
		}
	}
}

func kindName(k Kind) string {
	switch k {
	case KindDoH:
		return "DoH"
	case KindDoT:
		return "DoT"
	}
	return "plain DNS"
}

// EndpointsFor is Resolve for the main servers of a preset: what the client gets for every domain that is not
// split off.
func (p Preset) EndpointsFor(c Client) ([]Endpoint, []string) {
	return Resolve(c, p.Transport, p.Servers, p.IPv4Only)
}

// SplitEndpointsFor is Resolve for the servers of split rule i of a preset.
func (p Preset) SplitEndpointsFor(c Client, i int) ([]Endpoint, []string) {
	return Resolve(c, p.Transport, p.Split[i].Servers, p.IPv4Only)
}
