//go:build linux

package hostctl

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// SyncOwnedIPv6 keeps the panel's addresses on the uplink (ipv6addr.go has the rules).
func (h *linuxHost) SyncOwnedIPv6(ctx context.Context, wanted []netip.Addr) (OwnedIPv6Result, error) {
	return h.v6.Sync(ctx, wanted)
}

// CleanupOwnedIPv6 removes every address of ipv6-owned.json.
func (h *linuxHost) CleanupOwnedIPv6(ctx context.Context) error { return h.v6.Cleanup(ctx) }

// CleanupOwnedIPv6 is the host-less form for `mistgate-node cleanup-net --ipv6-addresses`: it removes the addresses that
// the file in stateDir names, and only those.
func CleanupOwnedIPv6(ctx context.Context, stateDir string) error {
	return newIPv6Owner(rtnetlinkV6{}, stateDir).Cleanup(ctx)
}

// rtnetlinkV6 is v6Netlink on the kernel of this process's network namespace.
type rtnetlinkV6 struct{}

func (rtnetlinkV6) DefaultIface() (string, error) {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return "", err
	}
	best, bestPrio := "", 0
	for _, r := range routes {
		if r.Type != unix.RTN_UNICAST || r.LinkIndex == 0 {
			continue
		}
		if r.Dst != nil { // older kernels or library versions list ::/0 with a destination, newer ones with none
			if ones, _ := r.Dst.Mask.Size(); ones != 0 || !r.Dst.IP.IsUnspecified() {
				continue
			}
		}
		l, err := netlink.LinkByIndex(r.LinkIndex)
		if err != nil || NotUplink(l.Attrs().Name) {
			continue
		}
		if best == "" || r.Priority < bestPrio {
			best, bestPrio = l.Attrs().Name, r.Priority
		}
	}
	return best, nil
}

func (rtnetlinkV6) Addrs(iface string) ([]v6Addr, error) {
	l, err := netlink.LinkByName(iface)
	if err != nil {
		return nil, err
	}
	list, err := netlink.AddrList(l, netlink.FAMILY_V6)
	if err != nil {
		return nil, err
	}
	out := make([]v6Addr, 0, len(list))
	for _, a := range list {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok || a.IPNet == nil {
			continue
		}
		bits, _ := a.Mask.Size()
		out = append(out, v6Addr{Addr: ip.Unmap(), Bits: bits, Flags: a.Flags})
	}
	return out, nil
}

func v6Own(addr netip.Addr) *netlink.Addr {
	return &netlink.Addr{
		IPNet:       &net.IPNet{IP: addr.AsSlice(), Mask: net.CIDRMask(128, 128)},
		Flags:       unix.IFA_F_NOPREFIXROUTE,
		PreferedLft: 0, ValidLft: math.MaxUint32, // deprecated, never expires
	}
}

func (rtnetlinkV6) Add(iface string, addr netip.Addr, replace bool) error {
	l, err := netlink.LinkByName(iface)
	if err != nil {
		return err
	}
	if replace {
		return netlink.AddrReplace(l, v6Own(addr))
	}
	return netlink.AddrAdd(l, v6Own(addr))
}

func (rtnetlinkV6) Del(iface string, addr netip.Addr) error {
	l, err := netlink.LinkByName(iface)
	if err != nil {
		var nf netlink.LinkNotFoundError
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	// The kernel matches the prefix length too: ours is always /128.
	if err := netlink.AddrDel(l, v6Own(addr)); err != nil && !errors.Is(err, syscall.EADDRNOTAVAIL) {
		return err
	}
	return nil
}
