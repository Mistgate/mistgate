package hostctl

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/node/torrentguard"
	torrentlinux "github.com/mistgate/mistgate/internal/node/torrentguard/linux"
)

const (
	// NftTorrentTable is Mistgate's independent, fail-open NFQUEUE ruleset.
	NftTorrentTable = "mistgate_torrentguard"
	torrentQueueNum = 4242
	// torrentTCPPackets is how many packets of a client's TCP connection are queued: the SYN, the ACK and the first
	// data, with room for a retransmitted SYN or a handshake split in two.
	torrentTCPPackets = 6
)

// TorrentDetection describes one BitTorrent request an AWG client sent. TunnelIP is
// the client's tunnel address, used only to find the user; the destination is only a port, never an address.
type TorrentDetection struct {
	L4Protocol  string
	Signature   torrentguard.Protocol
	Evidence    torrentguard.Evidence
	DstPort     uint16
	TunnelIface string
	TunnelIP    netip.Addr
}

// TorrentGuardHost owns the optional Linux userspace flow inspector.
type TorrentGuardHost interface {
	// SetTorrentGuard inspects the start of the TCP/UDP flows clients open
	// through exactly the supplied active AWG interfaces. Passing no interfaces
	// removes Mistgate's queue table and stops its runtime. Queue failures are fail-open.
	SetTorrentGuard(ctx context.Context, ifaces []string, onAttempt func(TorrentDetection)) error
}

// RenderTorrentGuard returns an atomic replacement for Mistgate's own nft
// table. Only what a client sends out through the exact active AWG interface
// set is queued, and only the start of a flow: the first packets of a TCP
// connection the client opened, and UDP datagrams of a conntrack entry that has
// seen no reply yet. Traffic that enters and leaves through AWG interfaces is
// excluded, and queue bypass keeps traffic flowing without a userspace listener.
//
// A block is enforced in the kernel: the runtime repeats the packet with
// BlockMark, the first rule turns it into the connection's ct mark and drops
// it, and the second drops every later packet of that connection in both
// directions. `ct original packets` makes nft switch conntrack accounting on
// (the kernel does it when such a rule is loaded); a connection older than that
// has no counter and stays queued, which is correct, only not cheaper.
func RenderTorrentGuard(ifaces []string) (string, error) {
	sorted := append([]string(nil), ifaces...)
	sort.Strings(sorted)
	for i, iface := range sorted {
		if err := validateTorrentIface(iface); err != nil {
			return "", err
		}
		if i > 0 && sorted[i-1] == iface {
			return "", fmt.Errorf("duplicate torrent guard interface %q", iface)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\n", NftTorrentTable, NftTorrentTable)
	if len(sorted) == 0 {
		return b.String(), nil
	}
	var names strings.Builder
	for i, iface := range sorted {
		if i > 0 {
			names.WriteString(", ")
		}
		fmt.Fprintf(&names, "%q", iface)
	}
	fmt.Fprintf(&b, "table inet %s {\n", NftTorrentTable)
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\tmeta mark 0x%08x ct mark set 0x%08x drop\n", torrentlinux.BlockMark, torrentlinux.BlockMark)
	fmt.Fprintf(&b, "\t\tct mark 0x%08x drop\n", torrentlinux.BlockMark)
	fmt.Fprintf(&b, "\t\tiifname { %s } oifname != { %s } meta l4proto tcp ct direction original ct original packets <= %d queue num %d bypass\n", names.String(), names.String(), torrentTCPPackets, torrentQueueNum)
	fmt.Fprintf(&b, "\t\tiifname { %s } oifname != { %s } meta l4proto udp ct state new queue num %d bypass\n", names.String(), names.String(), torrentQueueNum)
	b.WriteString("\t}\n}\n")
	return b.String(), nil
}

func validateTorrentIface(iface string) error {
	if len(iface) > 15 || !strings.HasPrefix(iface, TunnelIfacePrefix) {
		return fmt.Errorf("torrent guard interface %q is not an AWG interface", iface)
	}
	portText := strings.TrimPrefix(iface, TunnelIfacePrefix)
	if portText == "" {
		return fmt.Errorf("torrent guard interface %q is not an AWG interface", iface)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 || TunnelIface(uint16(port)) != iface {
		return fmt.Errorf("torrent guard interface %q is not an AWG interface", iface)
	}
	return nil
}
