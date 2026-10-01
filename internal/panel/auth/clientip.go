package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ProxyTrust says which peers are reverse proxies whose X-Forwarded-For / Forwarded
// headers may be believed. The zero value trusts nobody: the client address is the TCP
// peer, whatever the headers say.
type ProxyTrust struct{ prefixes []netip.Prefix }

// NewProxyTrust trusts the given networks.
func NewProxyTrust(p []netip.Prefix) ProxyTrust { return ProxyTrust{prefixes: p} }

// ParseProxies parses --trusted-proxy values: CIDRs ("10.0.0.0/8") or single addresses.
func ParseProxies(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not a CIDR or IP address", s)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

func (t ProxyTrust) trusts(a netip.Addr) bool {
	for _, p := range t.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the address of the client behind remoteAddr ("host:port" as in
// http.Request.RemoteAddr). Headers are read only when the peer itself is a trusted
// proxy. Then the chain is walked from the right (the proxy nearest to us appended
// last) and the first address that is not itself a trusted proxy is the client, so
// values the client put at the left of the header are never believed.
// X-Forwarded-For and Forwarded are both understood; if a request carries both and they
// name different clients, neither is believed and the proxy's own address is returned
// (one shared bucket for that request, instead of letting a spoofed header pick one).
// An unparseable chain falls back to the peer as well.
func (t ProxyTrust) ClientIP(remoteAddr string, h http.Header) netip.Addr {
	peer := parseNode(remoteAddr)
	if !peer.IsValid() || !t.trusts(peer) {
		return peer
	}
	var fromXFF, fromFwd netip.Addr
	xff, fwd := h.Values("X-Forwarded-For"), h.Values("Forwarded")
	if len(xff) > 0 {
		fromXFF = t.rightmostUntrusted(splitList(xff))
		if !fromXFF.IsValid() {
			return peer
		}
	}
	if len(fwd) > 0 {
		fromFwd = t.rightmostUntrusted(forwardedFor(fwd))
		if !fromFwd.IsValid() {
			return peer
		}
	}
	switch {
	case fromXFF.IsValid() && fromFwd.IsValid():
		if fromXFF != fromFwd {
			return peer
		}
		return fromXFF
	case fromXFF.IsValid():
		return fromXFF
	case fromFwd.IsValid():
		return fromFwd
	}
	return peer
}

// rightmostUntrusted walks nodes right to left and returns the first that is not a
// trusted proxy. If all are trusted (an internal client) the leftmost is the client.
// Any node that is not an address invalidates the whole chain.
func (t ProxyTrust) rightmostUntrusted(nodes []string) netip.Addr {
	var last netip.Addr
	for i := len(nodes) - 1; i >= 0; i-- {
		a := parseNode(nodes[i])
		if !a.IsValid() {
			return netip.Addr{}
		}
		if !t.trusts(a) {
			return a
		}
		last = a
	}
	return last
}

func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

// forwardedFor extracts the for= node of every element of Forwarded headers (RFC 7239).
// An element without for=, or with an obfuscated or unknown node, yields "" (invalid).
func forwardedFor(values []string) []string {
	var out []string
	for _, el := range splitList(values) {
		node := ""
		for _, pair := range strings.Split(el, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && strings.EqualFold(k, "for") {
				node = strings.Trim(v, `"`)
			}
		}
		out = append(out, node)
	}
	return out
}

// parseNode parses "1.2.3.4", "1.2.3.4:80", "2001:db8::1", "[2001:db8::1]" and
// "[2001:db8::1]:80" into an address (IPv4-mapped IPv6 unmapped, zone dropped).
func parseNode(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone("")
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return a.Unmap().WithZone("")
		}
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return a.Unmap().WithZone("")
		}
	}
	return netip.Addr{}
}

// SourceKey is the rate-limit and ceremony-cap key for a client: the address for IPv4,
// the /64 for IPv6 (a single subscriber is handed a whole /64, so per-address limits
// are free to evade).
func SourceKey(a netip.Addr) string {
	switch {
	case !a.IsValid():
		return "unknown"
	case a.Is4() || a.Is4In6():
		return a.Unmap().String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}
