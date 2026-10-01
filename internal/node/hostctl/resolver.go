package hostctl

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Pure helpers for the resolver fix. They are not behind the Linux build tag so they are tested everywhere.

const (
	// ResolverModeResolved configures systemd-resolved through a drop-in; ResolverModeResolvConf rewrites
	// /etc/resolv.conf (backup kept).
	ResolverModeResolved   = "resolved"
	ResolverModeResolvConf = "resolv_conf"

	resolverMarker = "# Managed by mistgate-node."
	maxResolvers   = 3 // glibc reads at most 3 nameserver lines
)

// cleanResolvers validates and deduplicates resolver addresses. Entries are bare IPs or host:port (host must
// be an IP: neither resolv.conf nor resolved.conf can take a name here). resolv.conf has no port syntax, so in
// that mode only port 53 survives. Loopback is allowed (a local cache is a legitimate choice),
// unspecified and multicast are not. At most three are kept, in order.
func cleanResolvers(servers []string, resolvConf bool) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	var skipped []string
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		var addr netip.Addr
		port := uint16(53)
		if ap, err := netip.ParseAddrPort(s); err == nil {
			addr, port = ap.Addr(), ap.Port()
		} else if a, err := netip.ParseAddr(s); err == nil {
			addr = a
		} else {
			skipped = append(skipped, s)
			continue
		}
		addr = addr.Unmap()
		if addr.IsUnspecified() || addr.IsMulticast() || port == 0 || (resolvConf && port != 53) {
			skipped = append(skipped, s)
			continue
		}
		v := addr.String()
		if port != 53 {
			if addr.Is6() {
				v = "[" + v + "]:" + fmt.Sprint(port)
			} else {
				v += ":" + fmt.Sprint(port)
			}
		}
		if !seen[v] && len(out) < maxResolvers {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		if len(skipped) > 0 {
			return nil, fmt.Errorf("no usable resolver address in %q", strings.Join(skipped, ","))
		}
		return nil, errors.New("no resolver address")
	}
	return out, nil
}

// parseNameservers returns the nameserver addresses of a resolv.conf in order.
func parseNameservers(conf string) []string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}

// parseResolvectlDNS extracts the server addresses of `resolvectl dns` output
// ("Global: 1.1.1.1", "Link 2 (eth0): 203.0.113.1 fe80::1%eth0").
func parseResolvectlDNS(out string) []string {
	var res []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		var list string
		if i := strings.Index(line, "): "); i >= 0 {
			list = line[i+3:]
		} else if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Global:"); ok {
			list = rest
		} else {
			continue
		}
		for _, f := range strings.Fields(list) {
			if a, _, _ := strings.Cut(f, "%"); !seen[a] && looksLikeAddr(a) {
				seen[a] = true
				res = append(res, a)
			}
		}
	}
	return res
}

func looksLikeAddr(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	_, err := netip.ParseAddrPort(s)
	return err == nil
}

func renderResolvConf(servers []string) string {
	var b strings.Builder
	b.WriteString(resolverMarker + "\n")
	for _, s := range servers {
		b.WriteString("nameserver " + s + "\n")
	}
	return b.String()
}

// renderResolvedDropIn makes the given servers the only global ones and routes every name to them
// (Domains=~.) so the DHCP-provided resolver of the link stops being asked.
func renderResolvedDropIn(servers []string) string {
	return resolverMarker + "\n[Resolve]\nDNS=" + strings.Join(servers, " ") + "\nFallbackDNS=\nDomains=~.\n"
}
