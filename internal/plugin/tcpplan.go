package plugin

import (
	"cmp"
	"encoding/json"
	"slices"
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
