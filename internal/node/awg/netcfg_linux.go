//go:build linux

package awg

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"

	"github.com/mistgate/mistgate/internal/plugin"
)

// Both backends share the rtnetlink part: the address, MTU and link state of the interface (the kernel module's
// link is created with LinkAdd, the userspace TUN by tun.CreateTUN; everything after that is identical).

func toIPNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}

// configureLink gives the interface the node's address of the client subnet and brings it up. A failing IPv6
// address does not take the IPv4 tunnel down (a host with IPv6 disabled): it is returned as a warning.
func configureLink(name string, t plugin.Tunnel) (warn error, err error) {
	l, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("link %s: %w", name, err)
	}
	if err := netlink.AddrAdd(l, &netlink.Addr{IPNet: toIPNet(t.AddrV4)}); err != nil && !errors.Is(err, syscall.EEXIST) {
		return nil, fmt.Errorf("add %s to %s: %w", t.AddrV4, name, err)
	}
	if t.AddrV6.IsValid() {
		a := &netlink.Addr{IPNet: toIPNet(t.AddrV6), Flags: syscall.IFA_F_NODAD}
		if err := netlink.AddrAdd(l, a); err != nil && !errors.Is(err, syscall.EEXIST) {
			warn = fmt.Errorf("add %s to %s: %w", t.AddrV6, name, err)
		}
	}
	if err := netlink.LinkSetMTU(l, int(t.MTU)); err != nil {
		return warn, fmt.Errorf("set mtu of %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(l); err != nil {
		return warn, fmt.Errorf("set %s up: %w", name, err)
	}
	return warn, nil
}

func setLinkMTU(name string, mtu int) error {
	l, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	return netlink.LinkSetMTU(l, mtu)
}

// linkIsUp: the link exists and IFF_UP is set.
func linkIsUp(name string) bool {
	l, err := netlink.LinkByName(name)
	if err != nil {
		return false
	}
	return l.Attrs().Flags&net.FlagUp != 0
}

// deleteLink removes a link of ours; a missing link is fine. Only names with our prefix are ever touched.
func deleteLink(name string) error {
	if !strings.HasPrefix(name, ifacePrefix) {
		return fmt.Errorf("refusing to delete %q: not one of our interfaces", name)
	}
	l, err := netlink.LinkByName(name)
	if err != nil {
		var nf netlink.LinkNotFoundError
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	return netlink.LinkDel(l)
}

// portListening reports whether a UDP socket is bound to the port (any address), from /proc/net/udp{,6}.
func portListening(port uint16) bool {
	want := fmt.Sprintf(":%04X", port)
	for _, f := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) > 1 && strings.HasSuffix(fields[1], want) {
				return true
			}
		}
	}
	return false
}
