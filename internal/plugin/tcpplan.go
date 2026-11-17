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
// The panel and the agent must say the same here, so everything that asks "what answers on TCP 443" asks this.
func TCPPortOwners(specs []InboundSpec) map[uint16]TCPOwner {
	sorted := slices.Clone(specs)
	slices.SortStableFunc(sorted, func(a, b InboundSpec) int { return cmp.Compare(a.ID, b.ID) })
	owners := make(map[uint16]TCPOwner)
	for _, s := range sorted {
		if _, held := owners[s.Listen.Port]; !held && s.Listen.Network == "tcp" && s.Listen.Port != 0 {
			owners[s.Listen.Port] = TCPOwner{ID: s.ID, Protocol: s.Protocol, Site: tcpOwnSite(s)}
		}
	}
	for _, s := range sorted {
		if port := hy2SitePort(s); port != 0 {
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
		var j struct {
			Security string `json:"security"`
			Reality  struct {
				Target string `json:"target"`
			} `json:"reality"`
		}
		if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
			return false
		}
		return j.Security == "tls" || (j.Security == "reality" || j.Security == "") && j.Reality.Target == "self"
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

// hy2SitePort is the TCP port of a Hysteria2 inbound's HTTPS site, 0 = none (the node's own rule,
// internal/node/hysteria2/settings.go).
func hy2SitePort(s InboundSpec) uint16 {
	if s.Protocol != "hysteria2" || s.TLS.Mode == 0 {
		return 0
	}
	var j struct {
		Masquerade struct {
			TCPPort *int `json:"tcp_port"`
		} `json:"masquerade"`
	}
	if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
		return 0
	}
	if j.Masquerade.TCPPort != nil {
		return uint16(min(max(*j.Masquerade.TCPPort, 0), 65535))
	}
	return 443
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
		plan[port] = p
	}
	return plan
}

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
		var j struct {
			Security string `json:"security"`
			Reality  struct {
				ServerNames []string `json:"server_names"`
			} `json:"reality"`
			CDN struct {
				Host string `json:"host"`
			} `json:"cdn"`
		}
		if len(s.Settings) > 0 && json.Unmarshal(s.Settings, &j) != nil {
			return r, false
		}
		if j.Security == "tls" {
			names = []string{s.TLS.ServerName, j.CDN.Host}
		} else {
			names, r.Reality = j.Reality.ServerNames, true
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
