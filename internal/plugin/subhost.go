package plugin

import (
	"net/netip"
	"slices"
)

// A subscription host (design/adr/0018-panel-behind-nat.md): a node whose TCP 443 hands the raw TLS of the subscription
// names to the panel, which calls it over the bridge. The panel and the agent decide its routes, and whether the node can be
// one, with the same function (SubHostRoutes).

// CapSubHost is the Hello capability of an agent that can be a subscription host.
const CapSubHost = "sub-host/1"

// The ids of the host's routes on the front of TCP 443. Inbound ids never hold "~".
const (
	SubHostRouteID     = "~sub-host"      // the subscription names: to the panel, over the bridge
	SubHostSiteRouteID = "~sub-host-site" // the agent's own site, when no inbound on 443 serves one
)

// SubHostPort is the port the host serves the subscription names on.
const SubHostPort = 443

// SubHostRole is a node's subscription host role (agent.v1.SubHostRole).
type SubHostRole struct {
	Names []string // the subscription names, lower case
	Path  string   // the bridge path the panel calls
	// SiteName is the name of the agent's own site on 443 (ACME), used when no inbound there serves the node's site.
	SiteName string
	// LinkSources are the fleet nodes' addresses: connections from them are agent links, with capacity of their own.
	LinkSources []netip.Prefix
}

// Why a node cannot be the subscription host (SubHostRoutes): stable codes the panel and the agent report.
const (
	SubHostNamesInvalid     = "names_invalid"          // no subscription name a ClientHello can carry
	SubHostRealityForeign   = "reality_foreign_target" // a REALITY inbound on 443 shows someone else's site
	SubHostHysteria2Site    = "hysteria2_site_443"     // a Hysteria2 site holds TCP 443 (no TCP inbound does)
	SubHostPortUnshareable  = "port_443_unshareable"   // an inbound on 443 cannot share the port by name
	SubHostNameTaken        = "name_taken"             // an inbound on 443 asks for a subscription name or the site name
	SubHostSiteNameRequired = "site_name_required"     // nothing on 443 serves the node's site and no site name is given
	SubHostSiteNameConflict = "site_name_conflict"     // the site name is a subscription name
)

// SubHostRoutes returns the extra routes of TCP 443 for role (TCPSharePlan's extra), or why the node cannot be the host with
// these inbounds (the ones to run; the panel checks a change with the inbounds it would leave). An unfit role gets no route:
// the agent installs nothing and its inbounds run as before.
func SubHostRoutes(role SubHostRole, specs []InboundSpec) ([]TCPExtraRoute, string) {
	return SubHostRoutesOn(SubHostPort, role, specs)
}

// SubHostRoutesOn is SubHostRoutes for another port than 443 (tests on loopback).
func SubHostRoutesOn(port uint16, role SubHostRole, specs []InboundSpec) ([]TCPExtraRoute, string) {
	var names []string
	for _, n := range role.Names {
		if n = routeName(n); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, SubHostNamesInvalid
	}
	tcpOn443, ownSite := false, false
	for _, s := range specs {
		if s.Listen.Network != "tcp" || s.Listen.Port != port {
			continue
		}
		tcpOn443 = true
		if _, _, foreign := RealityTarget(s); foreign {
			return nil, SubHostRealityForeign
		}
		r, ok := tcpRoute(s)
		if !ok {
			return nil, SubHostPortUnshareable
		}
		if slices.ContainsFunc(r.Names, func(n string) bool { return slices.Contains(names, n) }) {
			return nil, SubHostNameTaken
		}
		ownSite = ownSite || r.Site
	}
	if !tcpOn443 {
		for _, s := range specs {
			if hy2SitePort(s) == port {
				return nil, SubHostHysteria2Site
			}
		}
	}
	routes := []TCPExtraRoute{{Port: port, Route: TCPRoute{ID: SubHostRouteID, Protocol: "sub-host", Names: names}}}
	if ownSite {
		return routes, ""
	}
	site := routeName(role.SiteName)
	switch {
	case site == "":
		return nil, SubHostSiteNameRequired
	case slices.Contains(names, site):
		return nil, SubHostSiteNameConflict
	}
	for _, s := range specs {
		if r, ok := tcpRoute(s); ok && s.Listen.Network == "tcp" && s.Listen.Port == port && slices.Contains(r.Names, site) {
			return nil, SubHostNameTaken
		}
	}
	return append(routes, TCPExtraRoute{Port: port, Route: TCPRoute{ID: SubHostSiteRouteID, Protocol: "sub-host-site", Names: []string{site}, Site: true}}), ""
}
