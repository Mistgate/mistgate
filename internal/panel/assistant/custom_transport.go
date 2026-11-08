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
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	local := ip.IsLoopback() || ip.IsPrivate()
	if local && !allowLocal {
		return false
	}
	return !httpOnly || local
}
