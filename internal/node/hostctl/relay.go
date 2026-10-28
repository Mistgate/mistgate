package hostctl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
)

// The host side of the relay forwards (agent.proto "RELAY FORWARDS"). A forward passes the encrypted traffic of one port
// on to a server on another node, unchanged: the kernel rewrites the destination (DNAT) and the source (masquerade), so
// the node never decrypts anything. Everything lives in ONE nft table of its own, "inet mistgate_relay", replaced as a
// whole like the hop and tunnel tables:
//
//   - local4: the node's own IPv4 addresses (OwnIPv4s), re-rendered when they change;
//   - pre: DNAT of packets addressed to local4 on <network> dport <port> to the exit's address and port. A rule on the port
//     alone would also catch the traffic of clients that only pass through the node (an AmneziaWG tunnel) to other hosts;
//   - post: masquerade of what was DNATed, so the exit's replies come back through this node;
//   - pass: one named counter per forward (bytes and packets, for the doctor) and an accept.
//
// An accept in this table cannot override a "policy drop" in another table's forward chain (Docker, UFW): that case is
// detected and the table is not installed at all (ErrForwardDrop) instead of hijacking a port into a black hole.

const (
	// NftRelayTable is the table of the relay forwards. The doctor treats it as ours.
	NftRelayTable = "mistgate_relay"
	// MaxForwards bounds the forwards of one node.
	MaxForwards = 64
	// MinForwardPort is the lowest port a forward may listen on, besides 443: well-known ports, sshd 22 and ACME 80 stay out of reach.
	MinForwardPort = 1024
)

// ErrForwardDrop is returned by SetRelays when a FORWARD chain of the host drops forwarded packets by default (UFW's
// default, Docker): nothing is installed.
var ErrForwardDrop = errors.New("forward_drop")

// Forward is one forwarded port after the exit's name has been resolved.
type Forward struct {
	ID      string // the panel's relay id
	Network string // "tcp" | "udp"
	Port    uint16 // listened on here
	ToIP    netip.Addr
	ToPort  uint16
}

// NetPort is a port of one network.
type NetPort struct {
	Network string
	Port    uint16
}

// ForwardGuard is what ValidateForward needs to know about the node and this package cannot: the caller fills it in.
type ForwardGuard struct {
	SSHPorts []uint16  // sshd, any network
	Ports    []NetPort // listeners of the node's own inbounds
	Hops     []Hop     // port-hop ranges of the node's own inbounds
	// IsSelf says whether an address is one of the node's own; nil = no address is.
	IsSelf func(netip.Addr) bool
}

// RelayHost is the part of the host that the relay forwards need beyond Host. The Linux host implements it, the stub
// does not (so the capability is not announced).
type RelayHost interface {
	// RelaySupported says whether this host can forward (nft is installed).
	RelaySupported() bool
	// SetRelays atomically replaces the relay table with the rules of fs (no forwards = the table is deleted) and turns IPv4
	// forwarding on. A call with the set that is already installed does nothing. Before anything is installed the host
	// looks for a FORWARD chain that drops: then it installs nothing, removes a table it had, and returns ErrForwardDrop.
	SetRelays(ctx context.Context, fs []Forward) error
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// PublicIPv4 says whether a is a public unicast IPv4 address: not private, CGNAT, loopback, link-local (which holds the
// cloud metadata address), multicast, "this network" or reserved. Documentation ranges pass: they are what tests use.
func PublicIPv4(a netip.Addr) bool {
	a = a.Unmap()
	if !a.Is4() || !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) {
		return false
	}
	return a.As4()[0] != 0 && a.As4()[0] < 240
}

// check is what makes a Forward renderable at all.
func (f Forward) check() error {
	switch {
	case !idRe.MatchString(f.ID):
		return fmt.Errorf("forward %q: bad id", f.ID)
	case f.Network != "tcp" && f.Network != "udp":
		return fmt.Errorf("forward %s: network %q", f.ID, f.Network)
	case f.Port != 443 && f.Port < MinForwardPort:
		return fmt.Errorf("forward %s: port %d is below %d (well-known ports, sshd 22 and ACME 80 stay out of reach; 443 is the one exception)", f.ID, f.Port, MinForwardPort)
	case f.ToPort == 0:
		return fmt.Errorf("forward %s: no target port", f.ID)
	case !PublicIPv4(f.ToIP):
		return fmt.Errorf("forward %s: %s is not a public IPv4 address", f.ID, f.ToIP)
	}
	return nil
}

// ValidateForward is the one place that says whether a forward may be installed, like ValidateHop. accepted are the
// forwards already accepted in this round (the caller visits them in id order, so the earlier id wins a clash).
func ValidateForward(f Forward, accepted []Forward, g ForwardGuard) error {
	if err := f.check(); err != nil {
		return err
	}
	if len(accepted) >= MaxForwards {
		return fmt.Errorf("forward %s: a node takes at most %d forwards", f.ID, MaxForwards)
	}
	for _, o := range accepted {
		if o.Network == f.Network && o.Port == f.Port {
			return fmt.Errorf("forward %s: %s port %d is already forwarded by %s", f.ID, f.Network, f.Port, o.ID)
		}
	}
	for _, p := range g.SSHPorts {
		if p == f.Port {
			return fmt.Errorf("forward %s: port %d is the sshd port of this node", f.ID, f.Port)
		}
	}
	for _, p := range g.Ports {
		if p.Network == f.Network && p.Port == f.Port {
			return fmt.Errorf("forward %s: %s port %d is used by an inbound of this node", f.ID, f.Network, f.Port)
		}
	}
	for _, h := range g.Hops {
		if h.Network == f.Network && f.Port >= h.From && f.Port <= h.To {
			return fmt.Errorf("forward %s: %s port %d is inside the hop range %d-%d of inbound %s", f.ID, f.Network, f.Port, h.From, h.To, h.InboundID)
		}
	}
	if g.IsSelf != nil && g.IsSelf(f.ToIP.Unmap()) {
		return fmt.Errorf("forward %s: %s is an address of this node", f.ID, f.ToIP)
	}
	return nil
}

// RenderRelays returns the nft script that atomically replaces the relay table. `add` + `delete` first makes it work
// whether or not the table exists; nft applies a whole -f file as one transaction. No forwards renders the delete only.
// The forwards are sorted by id; each is checked as ValidateForward does without the node's context.
func RenderRelays(fs []Forward, local []netip.Addr) (string, error) {
	sorted := append([]Forward(nil), fs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for i, f := range sorted {
		if err := f.check(); err != nil {
			return "", err
		}
		for _, o := range sorted[:i] {
			if o.ID == f.ID || (o.Network == f.Network && o.Port == f.Port) {
				return "", fmt.Errorf("forwards %s and %s share an id or a %s port", o.ID, f.ID, f.Network)
			}
		}
	}
	if len(sorted) > MaxForwards {
		return "", fmt.Errorf("%d forwards, the limit is %d", len(sorted), MaxForwards)
	}
	if len(sorted) > 0 && len(local) == 0 {
		return "", errors.New("no IPv4 address of this node to match the forwards against")
	}
	addrs := make([]string, 0, len(local))
	for _, a := range normalizeLocal(local) {
		addrs = append(addrs, a.String())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "add table %s %s\ndelete table %s %s\n", nftFamily, NftRelayTable, nftFamily, NftRelayTable)
	if len(sorted) == 0 {
		return b.String(), nil
	}
	fmt.Fprintf(&b, "table %s %s {\n", nftFamily, NftRelayTable)
	fmt.Fprintf(&b, "\tset local4 {\n\t\ttype ipv4_addr\n\t\telements = { %s }\n\t}\n", strings.Join(addrs, ", "))
	for _, f := range sorted {
		fmt.Fprintf(&b, "\tcounter %s_%d {\n\t}\n", f.Network, f.Port)
	}
	b.WriteString("\tchain pre {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, f := range sorted {
		// Only packets addressed to this node (@local4): a rule on the port alone would also catch the traffic of tunnel
		// clients that merely passes through, to any other host on that port, and send it to the exit.
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv4 ip daddr @local4 %s dport %d dnat ip to %s:%d comment \"relay:%s\"\n", f.Network, f.Port, f.ToIP, f.ToPort, f.ID)
	}
	b.WriteString("\t}\n\tchain post {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, f := range sorted {
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv4 ct status dnat ct original proto-dst %d ip daddr %s %s dport %d masquerade\n", f.Port, f.ToIP, f.Network, f.ToPort)
	}
	b.WriteString("\t}\n\tchain pass {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	for _, f := range sorted {
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv4 ct status dnat ct original proto-dst %d ip daddr %s %s dport %d counter name %s_%d accept\n", f.Port, f.ToIP, f.Network, f.ToPort, f.Network, f.Port)
	}
	b.WriteString("\t}\n}\n")
	return b.String(), nil
}

// ForwardsEqual reports whether two sets install the same rules (order does not matter).
func ForwardsEqual(a, b []Forward) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]Forward(nil), a...), append([]Forward(nil), b...)
	sort.Slice(x, func(i, j int) bool { return x[i].ID < x[j].ID })
	sort.Slice(y, func(i, j int) bool { return y[i].ID < y[j].ID })
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// normalizeLocal returns the addresses sorted, without duplicates.
func normalizeLocal(local []netip.Addr) []netip.Addr {
	out := append([]netip.Addr(nil), local...)
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return slices.Compact(out)
}

// OwnIPv4s lists the IPv4 addresses assigned to this host's interfaces, loopback aside (a packet to 127.0.0.1 is the
// node talking to itself, never a person to forward). The relay table matches destinations against it.
func OwnIPv4s() ([]netip.Addr, error) {
	all, err := ownAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range all {
		if a.Is4() && !a.IsLoopback() {
			out = append(out, a)
		}
	}
	return normalizeLocal(out), nil
}

// ownAddrs lists every address assigned to this host's interfaces, IPv4-mapped ones unmapped.
func ownAddrs() ([]netip.Addr, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, x := range as {
		var ip net.IP
		switch v := x.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if a, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, a.Unmap())
		}
	}
	return out, nil
}

// IsOwnAddr says whether a is assigned to one of this host's interfaces. If the list cannot be read it says yes: failing
// closed is safer than pointing a forward at the node itself. A public address that only a cloud's NAT knows is not seen.
func IsOwnAddr(a netip.Addr) bool {
	as, err := ownAddrs()
	if err != nil {
		return true
	}
	return slices.Contains(as, a.Unmap())
}
