package plugin

import (
	"cmp"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
)

// TCPOwner is what holds one TCP port of a node and what a visitor of the port meets there.
type TCPOwner struct {
	ID       string // the inbound that holds the port
	Protocol string
	// Site: a stranger gets the node's own HTTPS site (VLESS TLS and REALITY "self", TrustTunnel, the decoy of MTProto, a
	// Hysteria2 site). REALITY with another site and the site fallback of MTProto show someone else's.
	Site bool
}

// TCPPortOwners is the plan of the TCP ports of a node from its enabled inbounds: who holds each port, as the agent
// settles it. The lowest id of the TCP inbounds on a port holds it. A Hysteria2 site (settings.masquerade.tcp_port, 443
// when omitted) holds a port only when no TCP inbound does: it gives the port up to any of them (the agent's TCPClaims).
// A port that services share by SNI (TCPSharePlan) answers strangers with its default route. The panel and the agent must say the
// same here, so everything that asks "what answers on TCP 443" asks this.
func TCPPortOwners(specs []InboundSpec) map[uint16]TCPOwner {
	sorted := slices.Clone(specs)
	slices.SortStableFunc(sorted, func(a, b InboundSpec) int { return cmp.Compare(a.ID, b.ID) })
	owners := make(map[uint16]TCPOwner)
	for _, s := range sorted {
		if _, held := owners[s.Listen.Port]; !held && s.Listen.Network == "tcp" && s.Listen.Port != 0 {
			owners[s.Listen.Port] = TCPOwner{ID: s.ID, Protocol: s.Protocol, Site: tcpOwnSite(s)}
		}
	}
	for port, p := range TCPSharePlan(sorted) {
		if !p.Front() {
			continue
		}
		for _, r := range p.Routes {
			if r.ID == p.Default {
				owners[port] = TCPOwner{ID: r.ID, Protocol: r.Protocol, Site: true}
			}
		}
	}
	for _, s := range sorted {
		if port := Hy2SitePort(s); port != 0 {
			if _, held := owners[port]; !held {
				owners[port] = TCPOwner{ID: s.ID, Protocol: s.Protocol, Site: true}
			}
		}
	}
	return owners
}

// tcpOwnSite says whether a TCP inbound answers strangers with the node's own site.
func tcpOwnSite(s InboundSpec) bool {
	switch s.Protocol {
	case "trusttunnel":
		return true
	case "vless":
		v, ok := VLESSOf(s)
		return ok && v.OwnSite()
	case "mtproto":
		var j struct {
			Fallback string `json:"fallback"`
		}
		if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
			return false
		}
		return j.Fallback == "" || j.Fallback == "decoy"
	}
	return false
}

// TCPRoute is one service on a TCP port that services share by the SNI of the first ClientHello.
type TCPRoute struct {
	ID       string
	Protocol string
	// Names are the exact SNI names the service gets: lower case, no trailing dot, ASCII, never an IP address, and none that
	// a service with a lower id holds.
	Names []string
	Site  bool // a stranger gets the node's own site from it (TCPOwner.Site)
	// Reality: Xray runs REALITY on its connections (a TCP keepalive is not set on them, as when Xray listens alone).
	Reality bool
}

// TCPShadow says whose name an inbound asked for and did not get.
type TCPShadow struct{ Name, By string }

// TCPPortShare is one TCP port with two services or more, each of which can be told apart by name.
type TCPPortShare struct {
	// Routes are the services that hold their names, by id. Default is the id of the first of them that serves the node's own
	// site: it gets what no route claims (no name, an unknown name, not TLS, a broken hello); "" when none does.
	Routes  []TCPRoute
	Default string
	// Shadowed are the services that asked for a name a service with a lower id holds: they get no route and do not run.
	Shadowed map[string]TCPShadow
}

// Front says the port is shared: two routes or more and a default one. Otherwise the port keeps its old rule: the lowest id
// listens, the others fail to bind (Shadowed ones are reported the same way, by name).
func (p TCPPortShare) Front() bool { return len(p.Routes) >= 2 && p.Default != "" }

// TCPSharePlan decides which TCP ports of a node several services share by SNI (the agent's front, internal/node/front). It
// takes the inbounds that are to run (a caller that checks a layout before it is enabled passes the disabled ones too) and
// returns the ports where two of them or more listen. A port where one inbound cannot take part (another protocol than
// VLESS, TrustTunnel and MTProto, or no name to be told apart by) is not in the result: it keeps the old rule. A Hysteria2
// site is no route: it gives its port up to any TCP inbound (TCPPortOwners).
func TCPSharePlan(specs []InboundSpec) map[uint16]TCPPortShare {
	sorted := slices.Clone(specs)
	slices.SortStableFunc(sorted, func(a, b InboundSpec) int { return cmp.Compare(a.ID, b.ID) })
	byPort := make(map[uint16][]TCPRoute)
	broken := make(map[uint16]bool)
	for _, s := range sorted {
		if s.Listen.Network != "tcp" || s.Listen.Port == 0 {
			continue
		}
		r, ok := tcpRoute(s)
		byPort[s.Listen.Port] = append(byPort[s.Listen.Port], r)
		broken[s.Listen.Port] = broken[s.Listen.Port] || !ok
	}
	plan := make(map[uint16]TCPPortShare)
	for port, routes := range byPort {
		if len(routes) < 2 || broken[port] {
			continue
		}
		plan[port] = shareOf(routes)
	}
	return plan
}

// shareOf settles the names of the routes of one port, which come by id: the lower id keeps a name, a route that asks for a name
// that is kept already is shadowed and holds none of its names; the first route that serves the node's own site is the default.
func shareOf(routes []TCPRoute) TCPPortShare {
	p := TCPPortShare{Shadowed: map[string]TCPShadow{}}
	holder := map[string]string{}
routes:
	for _, r := range routes {
		for _, n := range r.Names {
			if by, taken := holder[n]; taken {
				p.Shadowed[r.ID] = TCPShadow{Name: n, By: by}
				continue routes
			}
		}
		for _, n := range r.Names {
			holder[n] = r.ID
		}
		if p.Default == "" && r.Site {
			p.Default = r.ID
		}
		p.Routes = append(p.Routes, r)
	}
	if len(p.Shadowed) == 0 {
		p.Shadowed = nil
	}
	return p
}

// The codes of a port that services cannot share (the panel refuses a layout that has one, and words them: admin sentences).
const (
	ShareUnsupported = "share_unsupported" // a service with no name to be told apart by, or one that cannot take part
	ShareNeedsSite   = "share_needs_site"  // none of the services answers a stranger with the node's own site
	SNITaken         = "sni_taken"         // two services ask for the same name
)

// TCPShareProblem is one reason the TCP inbounds of a port cannot share it. It is comparable: the panel judges a change by the
// problems it adds.
type TCPShareProblem struct {
	Code string
	// ID is the inbound it is about: the one with no name (ShareUnsupported), the one that lost the name (SNITaken); "" for
	// ShareNeedsSite.
	ID string
	// Name and By: the name and the inbound that holds it (SNITaken).
	Name, By string
}

// TCPShareProblems says what stops the TCP inbounds of port from sharing it, nothing when they can, or when fewer than two of
// them are there (one service listens alone, whatever it is). It takes the inbounds as TCPSharePlan does, and a caller that checks
// a layout before it is enabled passes the disabled ones too. One service that cannot take part (no name, another protocol)
// is the only problem reported: the others cannot be judged without it.
func TCPShareProblems(specs []InboundSpec, port uint16) []TCPShareProblem {
	sorted := slices.Clone(specs)
	slices.SortStableFunc(sorted, func(a, b InboundSpec) int { return cmp.Compare(a.ID, b.ID) })
	var routes []TCPRoute
	var out []TCPShareProblem
	n := 0
	for _, s := range sorted {
		if s.Listen.Network != "tcp" || s.Listen.Port != port {
			continue
		}
		n++
		if r, ok := tcpRoute(s); ok {
			routes = append(routes, r)
		} else {
			out = append(out, TCPShareProblem{Code: ShareUnsupported, ID: s.ID})
		}
	}
	if n < 2 {
		return nil
	}
	if len(out) > 0 {
		return out
	}
	p := shareOf(routes)
	for _, r := range routes {
		if sh, lost := p.Shadowed[r.ID]; lost {
			out = append(out, TCPShareProblem{Code: SNITaken, ID: r.ID, Name: sh.Name, By: sh.By})
		}
	}
	if p.Default == "" {
		out = append(out, TCPShareProblem{Code: ShareNeedsSite})
	}
	return out
}

// TCPRouteOf is the route a TCP inbound offers a shared port: the names it answers to and whether it serves the node's own site.
// ok is false when it has no name to be told apart by (or is no inbound a port can be shared with).
func TCPRouteOf(s InboundSpec) (TCPRoute, bool) { return tcpRoute(s) }

// tcpRoute is what a TCP inbound offers a shared port, from its spec; ok is false when it has no name to be told apart by.
func tcpRoute(s InboundSpec) (r TCPRoute, ok bool) {
	r = TCPRoute{ID: s.ID, Protocol: s.Protocol, Site: tcpOwnSite(s)}
	var names []string
	switch s.Protocol {
	case "trusttunnel":
		names = []string{s.TLS.ServerName}
	case "mtproto":
		var j struct {
			Domains []string `json:"domains"`
		}
		if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
			return r, false
		}
		names = j.Domains
	case "vless":
		// What the engine reads (internal/node/vless/settings.go): the TLS name and the CDN host of a TLS inbound; the server
		// names of REALITY, which for the node's own site are its TLS name and for another site the names the target is asked for.
		v, ok := VLESSOf(s)
		if !ok {
			return r, false
		}
		if v.TLS() {
			names = []string{s.TLS.ServerName, v.CDNHost}
		} else {
			names, r.Reality = v.ServerNames, true
		}
	default:
		return r, false
	}
	for _, n := range names {
		if n = routeName(n); n != "" && !slices.Contains(r.Names, n) {
			r.Names = append(r.Names, n)
		}
	}
	return r, len(r.Names) > 0
}

// routeName normalizes a name of a route as the front matches it; "" when a ClientHello cannot carry it (an IP address, a
// wildcard, a name that is not ASCII: it would have to be punycode already).
func routeName(n string) string {
	n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
	if n == "" || len(n) > 253 || strings.ContainsAny(n, "*: ") {
		return ""
	}
	for i := 0; i < len(n); i++ {
		if n[i] >= 0x80 {
			return ""
		}
	}
	if _, err := netip.ParseAddr(n); err == nil {
		return ""
	}
	return n
}
