package hostctl

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// The host side of the L3 tunnel protocols (AmneziaWG). The engine builds the
// interface, its address and MTU; everything around it belongs to the agent and lives in ONE nft table of its own,
// "inet mistgate_awg", that is replaced as a whole, exactly like the hop table:
//
//   - masquerade of the client subnets (not for a tunnel whose egress is WARP: the WARP table masquerades to the
//     WARP address, two source-NAT rules for one flow would race by priority);
//   - an MSS clamp to the route MTU towards and from a tunnel interface;
//   - client isolation: a forwarded packet may not leave through another (or the same) tunnel interface, so client
//     A never reaches client B, not even across profiles (found on a test stand);
//   - an input policy for packets that arrive on a tunnel interface and are for the node itself: only an echo
//     request to the tunnel's own address is answered, everything else is dropped. A client must not reach sshd, the
//     decoy site or a management port through the tunnel, nor the node address of another profile;
//   - with "IPv6 for clients" off (Tunnel.RejectV6): IPv6 from a tunnel towards the uplink is rejected with ICMPv6
//     admin-prohibited (chain nov6), not dropped, so apps move to IPv4 immediately;
//   - one named counter per UDP port, AwgHealth.udp_rx_packets: 0 = the provider or a firewall, > 0 without a
//     handshake = the obfuscation parameters.

const (
	// NftTunnelTable is the table of the tunnel rules. The doctor treats it as ours.
	NftTunnelTable = "mistgate_awg"
	// NftWarpTable is the table of the WARP manager (internal/node/warp.NftTable, a test in cmd/mistgate-node keeps
	// the two equal); the doctor treats it as ours too, and the two tables are the only ones besides NftTable.
	NftWarpTable = "mistgate_warp"
	// TunnelIfacePrefix is the name prefix of every interface the AWG engine creates (awg.IfaceName, a test in
	// internal/node/awg keeps the two equal). Cleanup deletes links that carry it.
	TunnelIfacePrefix = "mgawg"
	// WarpIface is the WARP tunnel device (warp.DefaultIface, same test).
	WarpIface = "mgwarp"
)

// TunnelIface is the interface name of a tunnel inbound on a UDP port.
func TunnelIface(port uint16) string { return fmt.Sprintf("%s%d", TunnelIfacePrefix, port) }

// Tunnel is one tunnel interface as the firewall sees it.
type Tunnel struct {
	Iface            string // mgawg<port>
	Subnet4, Subnet6 netip.Prefix
	Addr4, Addr6     netip.Addr // the node's own addresses inside the subnets (.1)
	UDPPort          uint16
	// ViaWarp: the egress of this tunnel is WARP. No masquerade here, the WARP table does it.
	ViaWarp bool
	// RejectV6: "IPv6 for clients" is off on this node. IPv6 forwarded from this tunnel to anything but a tunnel
	// interface is answered with ICMPv6 admin-prohibited, so the app falls back to IPv4 at once. The client keeps its
	// IPv6 address and ::/0 route (without them its real IPv6 would leave outside the tunnel). Not rendered for a ViaWarp
	// tunnel (the exit is Cloudflare's, not this node's uplink) or one without an IPv6 subnet.
	RejectV6 bool
}

// TunnelHost is the part of the host that the L3 protocols need beyond Host. The Linux host implements it, the stub
// accepts and does nothing; a caller type-asserts (`th, ok := host.(TunnelHost)`).
type TunnelHost interface {
	// SetTunnels atomically replaces the tunnel table with the rules of ts (no tunnels = the table is deleted) and
	// turns forwarding on: net.ipv4.ip_forward, and IPv6 forwarding when a tunnel has an IPv6 subnet. A call with the
	// set that is already installed does nothing (the counters keep counting).
	SetTunnels(ctx context.Context, ts []Tunnel) error
	// TunnelCounters returns, per UDP port of an installed tunnel, the datagrams received since the tunnel was first
	// installed by this process (a replacement of the table does not reset them).
	TunnelCounters(ctx context.Context) (map[uint16]uint64, error)
}

// ValidateTunnel checks one tunnel before it reaches nft. The strings that get into the script are interface names and
// address literals rendered by the netip package; the name is additionally restricted.
func ValidateTunnel(t Tunnel) error {
	switch {
	case !strings.HasPrefix(t.Iface, TunnelIfacePrefix) || len(t.Iface) > 15 || !ifaceNameOK(t.Iface):
		return fmt.Errorf("tunnel %q: interface name must be %s<port>", t.Iface, TunnelIfacePrefix)
	case !t.Subnet4.IsValid() || !t.Subnet4.Addr().Is4() || !t.Addr4.IsValid() || !t.Addr4.Is4() || !t.Subnet4.Contains(t.Addr4):
		return fmt.Errorf("tunnel %s: need an IPv4 subnet and the node address inside it", t.Iface)
	case t.Subnet6.IsValid() && (!t.Subnet6.Addr().Is6() || !t.Addr6.IsValid() || !t.Addr6.Is6() || !t.Subnet6.Contains(t.Addr6)):
		return fmt.Errorf("tunnel %s: bad IPv6 subnet or node address", t.Iface)
	case !t.Subnet6.IsValid() && t.Addr6.IsValid():
		return fmt.Errorf("tunnel %s: IPv6 node address without a subnet", t.Iface)
	case t.UDPPort == 0:
		return fmt.Errorf("tunnel %s: no UDP port", t.Iface)
	}
	return nil
}

func ifaceNameOK(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// RenderTunnels returns the nft script that atomically replaces the tunnel table. `add` + `delete` first makes it work
// whether or not the table exists; nft applies a whole -f file as one transaction. No tunnels renders the delete only.
func RenderTunnels(ts []Tunnel) (string, error) {
	sorted := append([]Tunnel(nil), ts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Iface < sorted[j].Iface })
	for i, t := range sorted {
		if err := ValidateTunnel(t); err != nil {
			return "", err
		}
		for _, o := range sorted[:i] {
			if o.Iface == t.Iface || o.UDPPort == t.UDPPort {
				return "", fmt.Errorf("tunnels %s and %s share an interface or a UDP port", o.Iface, t.Iface)
			}
			if o.Subnet4.Overlaps(t.Subnet4) || (o.Subnet6.IsValid() && t.Subnet6.IsValid() && o.Subnet6.Overlaps(t.Subnet6)) {
				return "", fmt.Errorf("tunnels %s and %s have overlapping client subnets", o.Iface, t.Iface)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "add table %s %s\ndelete table %s %s\n", nftFamily, NftTunnelTable, nftFamily, NftTunnelTable)
	if len(sorted) == 0 {
		return b.String(), nil
	}
	wild := fmt.Sprintf("%q", TunnelIfacePrefix+"*")
	fmt.Fprintf(&b, "table %s %s {\n", nftFamily, NftTunnelTable)
	for _, t := range sorted {
		fmt.Fprintf(&b, "\tcounter udp_%d {\n\t}\n", t.UDPPort)
	}

	// Input: count the datagrams at the UDP port (from anywhere), then the policy for packets arriving on a tunnel.
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "\t\tudp dport %d counter name udp_%d\n", t.UDPPort, t.UDPPort)
	}
	for _, t := range sorted {
		fmt.Fprintf(&b, "\t\tiifname %q ip daddr %s icmp type echo-request accept\n", t.Iface, t.Addr4)
		if t.Subnet6.IsValid() {
			fmt.Fprintf(&b, "\t\tiifname %q ip6 daddr %s icmpv6 type echo-request accept\n", t.Iface, t.Addr6)
		}
	}
	fmt.Fprintf(&b, "\t\tiifname %s drop\n\t}\n", wild)

	// Forward: no client-to-client traffic, and the MSS clamp in both directions of a tunnel.
	fmt.Fprintf(&b, "\tchain isolate {\n\t\ttype filter hook forward priority filter; policy accept;\n\t\tiifname %s oifname %s drop\n\t}\n", wild, wild)
	// "IPv6 for clients" off: reject (not drop) what a tunnel sends to the uplink over IPv6. icmpx maps to ICMPv6
	// "communication administratively prohibited" for IPv6 packets.
	var noV6 strings.Builder
	for _, t := range sorted {
		if t.RejectV6 && t.Subnet6.IsValid() && !t.ViaWarp {
			fmt.Fprintf(&noV6, "\t\tiifname %q oifname != %s meta nfproto ipv6 reject with icmpx type admin-prohibited\n", t.Iface, wild)
		}
	}
	if noV6.Len() > 0 {
		fmt.Fprintf(&b, "\tchain nov6 {\n\t\ttype filter hook forward priority filter; policy accept;\n%s\t}\n", noV6.String())
	}
	fmt.Fprintf(&b, "\tchain clamp {\n\t\ttype filter hook forward priority mangle; policy accept;\n"+
		"\t\toifname %s tcp flags syn tcp option maxseg size set rt mtu\n"+
		"\t\tiifname %s tcp flags syn tcp option maxseg size set rt mtu\n\t}\n", wild, wild)

	var nat strings.Builder
	for _, t := range sorted {
		if t.ViaWarp {
			continue
		}
		fmt.Fprintf(&nat, "\t\tip saddr %s oifname != %s masquerade\n", t.Subnet4.Masked(), wild)
		if t.Subnet6.IsValid() {
			fmt.Fprintf(&nat, "\t\tip6 saddr %s oifname != %s masquerade\n", t.Subnet6.Masked(), wild)
		}
	}
	if nat.Len() > 0 {
		fmt.Fprintf(&b, "\tchain post {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n%s\t}\n", nat.String())
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// TunnelsEqual reports whether two tunnel sets install the same rules (order does not matter).
func TunnelsEqual(a, b []Tunnel) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]Tunnel(nil), a...), append([]Tunnel(nil), b...)
	sort.Slice(x, func(i, j int) bool { return x[i].Iface < x[j].Iface })
	sort.Slice(y, func(i, j int) bool { return y[i].Iface < y[j].Iface })
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// NeedsV6 reports whether any tunnel has an IPv6 subnet (IPv6 forwarding is wanted).
func NeedsV6(ts []Tunnel) bool {
	for _, t := range ts {
		if t.Subnet6.IsValid() {
			return true
		}
	}
	return false
}
