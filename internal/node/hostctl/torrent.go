package hostctl

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

const (
	// NftTorrentTable is Mistgate's independent, fail-open NFQUEUE ruleset.
	NftTorrentTable = "mistgate_torrentguard"
	torrentQueueNum = 4242
)

// TorrentDetection describes one positively identified BitTorrent flow. Source
// and destination retain the direction of the packet that matched the
// signature; TunnelIP identifies the address on the AWG side of that packet.
type TorrentDetection struct {
	SourceIP        netip.Addr
	SourcePort      uint16
	DestinationIP   netip.Addr
	DestinationPort uint16
	L4Protocol      string
	Signature       torrentguard.Protocol
	TunnelIface     string
	TunnelIP        netip.Addr
}

// TorrentGuardHost owns the optional Linux userspace flow inspector.
type TorrentGuardHost interface {
	// SetTorrentGuard inspects forwarded TCP/UDP traffic crossing exactly the
	// supplied active AWG interfaces. Passing no interfaces removes Mistgate's
	// queue table and stops its runtime. Queue failures are fail-open.
	SetTorrentGuard(ctx context.Context, ifaces []string, onAttempt func(TorrentDetection)) error
}

// RenderTorrentGuard returns an atomic replacement for Mistgate's own nft
// table. Queue rules match the exact active AWG interface set in either
// direction, exclude traffic that enters and leaves through AWG interfaces,
// and use queue bypass so traffic continues when no userspace listener exists.
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
	fmt.Fprintf(&b, "\t\tiifname { %s } oifname != { %s } meta l4proto { tcp, udp } queue num %d bypass\n", names.String(), names.String(), torrentQueueNum)
	fmt.Fprintf(&b, "\t\toifname { %s } iifname != { %s } meta l4proto { tcp, udp } queue num %d bypass\n", names.String(), names.String(), torrentQueueNum)
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
