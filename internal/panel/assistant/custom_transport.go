//go:build !js

package assistant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

var (
	providerCGNAT          = netip.MustParsePrefix("100.64.0.0/10")
	providerIPv4Compatible = netip.MustParsePrefix("::/96")
)

// customProviderHTTP validates every resolved address at connection time, so DNS
// changes cannot turn a previously accepted hostname into a metadata endpoint.
func customProviderHTTP(base string, allowLocal bool) (providerHTTP, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return providerHTTP{}, errors.New("assistant provider: invalid endpoint")
	}
	if u.Scheme == "http" && !allowLocal {
		return providerHTTP{}, errors.New("assistant provider: HTTP requires local access")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, u.Hostname()) {
			return nil, errors.New("assistant provider: invalid host")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("assistant provider: host resolution failed")
		}
		// Refuse the whole answer when it mixes permitted and forbidden addresses.
		for _, ip := range ips {
			if !allowedProviderIP(ip, u.Scheme == "http", allowLocal) {
				return nil, errors.New("assistant provider: forbidden address")
			}
		}
		var dialErr error
		for _, ip := range ips {
			var conn net.Conn
			conn, dialErr = (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
		}
		return nil, dialErr
	}
	client := &http.Client{Transport: cappedRoundTripper{base: transport}, Timeout: providerTimeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !strings.EqualFold(req.URL.Host, u.Host) || req.URL.Scheme != u.Scheme {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return providerHTTP{base: u, client: client}, nil
}

func (s *Service) adapterForRow(p adminv1.AssistantProvider, row providerRow) adapter {
	if !isCustomProvider(p) {
		return s.providers[p]
	}
	if row.customURL == "" {
		return nil
	}
	h, err := customProviderHTTP(row.customURL, row.allowLocal)
	if err != nil {
		return nil
	}
	return &openAIAdapter{kind: p, http: h, defaultModel: row.model}
}

func allowedProviderIP(ip netip.Addr, httpOnly, allowLocal bool) bool {
	ip = unwrapProviderIP(ip)
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	local := ip.IsLoopback() || ip.IsPrivate() || providerCGNAT.Contains(ip)
	if local {
		return allowLocal
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	return !httpOnly && !providerSpecialAddress(ip)
}

func unwrapProviderIP(ip netip.Addr) netip.Addr {
	if !ip.IsValid() {
		return ip
	}
	if ip.IsUnspecified() || ip.IsLoopback() {
		return ip
	}
	if ip.Is4In6() {
		return ip.Unmap()
	}
	if ip.Is6() {
		if v4, ok := embeddedIPv4(ip, providerIPv4Compatible); ok {
			return v4
		}
		if v4, ok := embeddedIPv4(ip, netip.MustParsePrefix("64:ff9b::/96")); ok {
			return v4
		}
		if ip.Is6() && netip.MustParsePrefix("2002::/16").Contains(ip) {
			a := ip.As16()
			return netip.AddrFrom4([4]byte{a[2], a[3], a[4], a[5]})
		}
	}
	return ip
}

func embeddedIPv4(ip netip.Addr, prefix netip.Prefix) (netip.Addr, bool) {
	if !prefix.Contains(ip) || prefix.Bits() != 96 {
		return netip.Addr{}, false
	}
	a := ip.As16()
	return netip.AddrFrom4([4]byte{a[12], a[13], a[14], a[15]}), true
}

func providerSpecialAddress(ip netip.Addr) bool {
	var blocked []netip.Prefix
	if ip.Is4() {
		blocked = []netip.Prefix{
			netip.MustParsePrefix("0.0.0.0/8"),
			netip.MustParsePrefix("100.64.0.0/10"),
			netip.MustParsePrefix("192.0.0.0/24"),
			netip.MustParsePrefix("192.0.2.0/24"),
			netip.MustParsePrefix("192.88.99.0/24"),
			netip.MustParsePrefix("198.18.0.0/15"),
			netip.MustParsePrefix("198.51.100.0/24"),
			netip.MustParsePrefix("203.0.113.0/24"),
			netip.MustParsePrefix("240.0.0.0/4"),
		}
	} else {
		blocked = []netip.Prefix{
			netip.MustParsePrefix("64:ff9b:1::/48"),
			netip.MustParsePrefix("100::/64"),
			netip.MustParsePrefix("2001::/23"),
			netip.MustParsePrefix("2001:db8::/32"),
			netip.MustParsePrefix("3fff::/20"),
			netip.MustParsePrefix("fec0::/10"),
		}
	}
	for _, prefix := range blocked {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
