package dns

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
)

// The dns block of a sing-box profile (sing-box 1.12 and later: the typed server format; the legacy address-string
// servers are gone in 1.14). Docs: https://sing-box.sagernet.org/configuration/dns/ , read 2026-10-14.
//
// What a sing-box client does with a preset:
//
//   - final: one main resolver, "remote", the first endpoint of the plan, through the proxy group (detour). sing-box
//     has one resolver per name, not a fallback list, so the others are dropped with a note.
//   - rules: a split rule becomes {domain_suffix, action route, server split-N}; the resolver of a split rule is the
//     first endpoint of the rule, queried directly (no detour). Only with SplitDirect on.
//   - a DoH/DoT resolver is written as its IPv4 address plus tls.server_name (the host name), so that it needs no
//     lookup of its own. The assembler also needs "local" (the system resolver) for route.default_domain_resolver.
//   - strategy ipv4_only: no AAAA answers, like Mihomo's ipv6: false.

// Tags of the servers in the block that the rest of the profile refers to.
const (
	SingboxRemoteTag = "remote" // dns.final
	SingboxLocalTag  = "local"  // route.default_domain_resolver
)

// SingboxDNS is the dns block of one preset and what the assembler needs around it.
type SingboxDNS struct {
	Section SingboxSection
	// Direct are the suffix bodies (lower-case punycode, no leading dot) whose traffic goes direct; they are exactly
	// the suffixes of the DNS rules.
	Direct []string
	// Notes (English) say what of the preset a sing-box profile cannot carry.
	Notes []string
}

// SingboxSection is the JSON of the "dns" key.
type SingboxSection struct {
	Servers  []SingboxServer  `json:"servers"`
	Rules    []SingboxDNSRule `json:"rules,omitempty"`
	Final    string           `json:"final"`
	Strategy string           `json:"strategy"`
}

// SingboxServer is one entry of dns.servers (types udp, tls, https and local).
type SingboxServer struct {
	Type       string            `json:"type"`
	Tag        string            `json:"tag"`
	Server     string            `json:"server,omitempty"`
	ServerPort int               `json:"server_port,omitempty"` // only off the type's default port
	Path       string            `json:"path,omitempty"`        // DoH, only off /dns-query
	TLS        *SingboxServerTLS `json:"tls,omitempty"`
	Detour     string            `json:"detour,omitempty"`
}

// SingboxServerTLS is the tls block of a DoT/DoH server.
type SingboxServerTLS struct {
	ServerName string `json:"server_name"`
}

// SingboxDNSRule sends the queries of some suffixes to one server.
type SingboxDNSRule struct {
	DomainSuffix []string `json:"domain_suffix"`
	Action       string   `json:"action"`
	Server       string   `json:"server"`
}

// SingboxDNS renders the preset for a sing-box profile. group is the outbound (the selector) the main resolver's
// queries go through ("" = no detour). An error means no main server can be expressed; the caller leaves the dns
// block out.
func (p Preset) SingboxDNS(group string) (SingboxDNS, error) {
	main, split, notes, err := p.ClientPlan(ClientSingbox)
	if err != nil {
		return SingboxDNS{}, err
	}
	out := SingboxDNS{Notes: notes, Section: SingboxSection{Final: SingboxRemoteTag, Strategy: "ipv4_only"}}
	out.Section.Servers = append(out.Section.Servers, singboxServer(main[0], SingboxRemoteTag, group))
	if n := len(main) - 1; n > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d fallback resolver(s) dropped: sing-box takes one main resolver", n))
	}
	for _, sp := range split {
		if len(sp.Suffixes) == 0 {
			continue
		}
		tag := fmt.Sprintf("split-%d", len(out.Section.Rules)+1)
		out.Section.Servers = append(out.Section.Servers, singboxServer(sp.Endpoints[0], tag, ""))
		if n := len(sp.Endpoints) - 1; n > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("split rule %d: %d fallback resolver(s) dropped: sing-box takes one resolver per rule", sp.Rule+1, n))
		}
		out.Section.Rules = append(out.Section.Rules, SingboxDNSRule{DomainSuffix: sp.Suffixes, Action: "route", Server: tag})
		out.Direct = append(out.Direct, sp.Suffixes...)
	}
	out.Section.Servers = append(out.Section.Servers, SingboxServer{Type: "local", Tag: SingboxLocalTag})
	return out, nil
}

// singboxServer writes an endpoint of the sing-box transports as a server entry.
func singboxServer(e Endpoint, tag, detour string) SingboxServer {
	s := SingboxServer{Tag: tag, Server: endpointIP(e), Detour: detour}
	switch e.Kind {
	case KindDoH:
		s.Type = "https"
		u, _ := url.Parse(e.Address)
		s.ServerPort = portOff(u.Port(), 443)
		if u.Path != "" && u.Path != "/dns-query" {
			s.Path = u.Path
		}
		s.TLS = tlsName(u.Hostname())
	case KindDoT:
		s.Type = "tls"
		host, port := e.Address, ""
		if h, p, err := net.SplitHostPort(e.Address); err == nil {
			host, port = h, p
		}
		s.ServerPort = portOff(port, 853)
		s.TLS = tlsName(host)
	default:
		s.Type = "udp"
		_, port := plainAddr(e.Address)
		s.ServerPort = portOff(strconv.Itoa(port), 53)
	}
	return s
}

// portOff is the port when it is set and not the default (the field is left out then).
func portOff(port string, def int) int {
	if n, err := strconv.Atoi(port); err == nil && n != def {
		return n
	}
	return 0
}

// tlsName is the server_name for a host: none for an IP literal (the certificate is checked against the address).
func tlsName(host string) *SingboxServerTLS {
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	return &SingboxServerTLS{ServerName: host}
}
