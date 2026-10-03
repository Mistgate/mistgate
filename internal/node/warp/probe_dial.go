package warp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const probeDNSBudget = probeTotalTimeout / 2

var probeCGNAT = netip.MustParsePrefix("100.64.0.0/10")

type probeLookup func(context.Context, string, string) ([]netip.Addr, error)

// probeDialContext resolves with the node's configured resolvers, then dials only through the WARP interface.
// Both DNS and the socket dial inherit the HTTP request context, so a timed-out health check cannot leave work
// running in the background or report a late connection as traffic.
func (m *Manager) probeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	hasV6, iface, allowPrivate := m.ps.hasV6(), m.s.Iface, m.o.AllowPrivate
	m.mu.Unlock()

	var ips []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []netip.Addr{ip}
	} else {
		var servers []string
		if m.o.DNS != nil {
			servers = m.o.DNS()
		}
		ips, err = lookupProbeIPs(ctx, host, servers, nil)
		if err != nil {
			return nil, err
		}
	}

	candidates := probeCandidates(ips, network, hasV6)
	if len(candidates) == 0 {
		return nil, errors.New("warp probe: no address for this WARP account")
	}
	allowed := candidates[:0]
	for _, ip := range candidates {
		if probeAddressAllowed(ip, allowPrivate) {
			allowed = append(allowed, ip)
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("warp probe: destination address is not allowed")
	}
	return dialProbeIPs(ctx, network, port, iface, allowed)
}

// lookupProbeIPs gives DNS at most half of the full probe budget, reserving time for the WARP TCP connection and
// HTTP response. If the parent supplied a shorter deadline (for example in a test), DNS gets at most half of that.
// Resolver retries divide the remaining DNS time across the remaining configured servers.
func lookupProbeIPs(ctx context.Context, host string, servers []string, lookup probeLookup) ([]netip.Addr, error) {
	if lookup == nil {
		lookup = lookupProbeServer
	}
	budget := probeDNSBudget
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining/2 < budget {
			budget = remaining / 2
		}
	}
	if budget <= 0 {
		return nil, context.DeadlineExceeded
	}
	dnsCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if len(servers) == 0 {
		return lookup(dnsCtx, "", host)
	}
	var lastErr error = errors.New("warp probe: no usable resolver")
	valid := make([]string, 0, len(servers))
	for _, server := range servers {
		if addr, ok := probeResolverAddress(server); ok {
			valid = append(valid, addr)
		}
	}
	for i, server := range valid {
		remaining := time.Until(deadlineOf(dnsCtx))
		if remaining <= 0 {
			return nil, dnsCtx.Err()
		}
		attemptBudget := remaining / time.Duration(len(valid)-i)
		if attemptBudget > 3*time.Second {
			attemptBudget = 3 * time.Second
		}
		attemptCtx, attemptCancel := context.WithTimeout(dnsCtx, attemptBudget)
		ips, err := lookup(attemptCtx, server, host)
		attemptCancel()
		if err == nil && len(ips) > 0 {
			return ips, nil
		}
		if err == nil {
			err = errors.New("warp probe: resolver returned no addresses")
		}
		lastErr = err
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, err
		}
		if dnsCtx.Err() != nil {
			return nil, dnsCtx.Err()
		}
	}
	if len(valid) == 0 {
		return nil, lastErr
	}
	return nil, lastErr
}

func deadlineOf(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

func probeResolverAddress(server string) (string, bool) {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		ip, parseErr := netip.ParseAddr(strings.Trim(server, "[]"))
		if parseErr != nil {
			return "", false
		}
		return net.JoinHostPort(ip.String(), "53"), true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return net.JoinHostPort(ip.String(), port), true
}

func lookupProbeServer(ctx context.Context, resolver, host string) ([]netip.Addr, error) {
	if resolver == "" {
		return (&net.Resolver{PreferGo: true}).LookupNetIP(ctx, "ip", host)
	}
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, resolver)
		},
	}
	return r.LookupNetIP(ctx, "ip", strings.TrimSuffix(host, ".")+".")
}

func probeCandidates(ips []netip.Addr, network string, hasV6 bool) []netip.Addr {
	wantV4 := network != "tcp6"
	wantV6 := network != "tcp4" && hasV6
	var v4, v6 netip.Addr
	for _, ip := range ips {
		ip = ip.Unmap().WithZone("")
		if !ip.IsValid() {
			continue
		}
		if ip.Is4() && wantV4 && !v4.IsValid() {
			v4 = ip
		} else if ip.Is6() && wantV6 && !v6.IsValid() {
			v6 = ip
		}
	}
	var out []netip.Addr
	if v4.IsValid() {
		out = append(out, v4)
	}
	if v6.IsValid() {
		out = append(out, v6)
	}
	return out
}

func probeAddressAllowed(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() {
		return false
	}
	if allowPrivate {
		return true
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || probeCGNAT.Contains(ip) {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, raw := range addrs {
		var addr netip.Addr
		switch a := raw.(type) {
		case *net.IPNet:
			addr, _ = netip.ParseAddr(a.IP.String())
		case *net.IPAddr:
			addr, _ = netip.ParseAddr(a.IP.String())
		}
		if addr.IsValid() && addr.Unmap().WithZone("") == ip {
			return false
		}
	}
	return true
}

func dialProbeIPs(ctx context.Context, network, port, iface string, ips []netip.Addr) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("warp probe: no usable address")
	}
	if len(ips) == 1 {
		return dialProbeIP(ctx, network, port, iface, ips[0])
	}

	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan probeDialResult, len(ips))
	for _, ip := range ips {
		ip := ip
		go func() {
			conn, err := dialProbeIP(dialCtx, network, port, iface, ip)
			results <- probeDialResult{conn: conn, err: err}
		}()
	}
	var lastErr error
	for i := 0; i < len(ips); i++ {
		select {
		case result := <-results:
			if result.err == nil {
				cancel()
				go drainProbeDials(results, len(ips)-i-1)
				return result.conn, nil
			}
			lastErr = result.err
		case <-ctx.Done():
			cancel()
			go drainProbeDials(results, len(ips)-i)
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

type probeDialResult struct {
	conn net.Conn
	err  error
}

func drainProbeDials(results <-chan probeDialResult, count int) {
	for i := 0; i < count; i++ {
		result := <-results
		if result.conn != nil {
			_ = result.conn.Close()
		}
	}
}

func dialProbeIP(ctx context.Context, network, port, iface string, ip netip.Addr) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("warp probe: unsupported network %q", network)
	}
	if network == "tcp4" && !ip.Is4() || network == "tcp6" && !ip.Is6() {
		return nil, fmt.Errorf("warp probe: address %s is not valid for %s", ip, network)
	}
	d := &net.Dialer{}
	if err := bindProbeDevice(d, iface); err != nil {
		return nil, err
	}
	return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}
