// Package provision contains the panel's SSH provisioning primitives.
package provision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// DefaultSSHPort is used when the owner leaves the SSH port blank.
const DefaultSSHPort = 22

var (
	// ErrUnsafeTarget covers malformed hosts and any address that is not public unicast.
	ErrUnsafeTarget = errors.New("provision: SSH target must resolve only to public unicast addresses")
	// ErrNoAddress means DNS returned no addresses for the target.
	ErrNoAddress = errors.New("provision: SSH target has no usable address")
)

// Target is a normalized SSH host and port. Construct it with NewTarget so DNS
// search suffixes, zones and malformed host names cannot alter where we dial.
type Target struct {
	host string
	port uint16
}

// NewTarget validates and normalizes an SSH hostname or IP address and port.
func NewTarget(host string, port uint32) (Target, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "\x00/\\@?#[]") {
		return Target{}, ErrUnsafeTarget
	}
	if port == 0 {
		port = DefaultSSHPort
	}
	if port > 65535 {
		return Target{}, errors.New("provision: SSH port out of range")
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return Target{}, ErrUnsafeTarget
		}
		return Target{host: addr.Unmap().String(), port: uint16(port)}, nil
	}

	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if len(host) > 253 || !strings.Contains(host, ".") {
		return Target{}, ErrUnsafeTarget
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return Target{}, ErrUnsafeTarget
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return Target{}, ErrUnsafeTarget
			}
		}
	}
	return Target{host: host, port: uint16(port)}, nil
}

// Host returns the normalized SSH hostname or IP address.
func (t Target) Host() string { return t.host }

// Port returns the normalized SSH port.
func (t Target) Port() uint16 { return t.port }

// Address returns the target in host:port form, with IPv6 brackets when needed.
func (t Target) Address() string {
	return net.JoinHostPort(t.host, strconv.Itoa(int(t.port)))
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func (t Target) publicAddresses(ctx context.Context, r resolver) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(t.host); err == nil {
		addr = addr.Unmap()
		if !publicUnicast(addr) {
			return nil, ErrUnsafeTarget
		}
		return []netip.Addr{addr}, nil
	}

	addrs, err := r.LookupNetIP(ctx, "ip", t.host)
	if err != nil {
		return nil, fmt.Errorf("provision: resolve SSH target: %w", err)
	}
	if len(addrs) == 0 {
		return nil, ErrNoAddress
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		if !publicUnicast(addr) {
			return nil, ErrUnsafeTarget
		}
		if !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out, nil
}

var nonPublicPrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"64:ff9b::/96", "64:ff9b:1::/48", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16",
)

var globalIPv6 = netip.MustParsePrefix("2000::/3")

func publicUnicast(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if addr.Is6() && !globalIPv6.Contains(addr) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func mustPrefixes(values ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}
