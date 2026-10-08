package hostctl

import (
	"context"
	"fmt"
	"strings"
)

const NftUDPCheckTable = "mistgate_udpcheck"

// Count holds the packet and byte counters nft observed for one UDP port.
type Count struct {
	Packets uint64
	Bytes   uint64
}

// UDPCounter is implemented by a host that can count tagged UDP datagrams. One count at a time; the caller keeps
// which tag is armed.
type UDPCounter interface {
	CountUDP(tag [8]byte, ports []uint16) error
	TakeUDPCount() (map[uint16]Count, error)
}

// UDPCountCleaner is implemented by hosts that can remove a leftover UDP check table.
type UDPCountCleaner interface {
	CleanupUDPCount(context.Context) error
}

// RenderUDPCount returns an atomic nft script that replaces the UDP check table.
func RenderUDPCount(tag [8]byte, ports []uint16) (string, error) {
	if len(ports) == 0 || len(ports) > 8 {
		return "", fmt.Errorf("UDP count needs 1-8 ports")
	}
	seen := make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		if port == 0 {
			return "", fmt.Errorf("UDP count port must not be zero")
		}
		if _, ok := seen[port]; ok {
			return "", fmt.Errorf("UDP count port %d is duplicated", port)
		}
		seen[port] = struct{}{}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "add table %s %s\ndelete table %s %s\n", nftFamily, NftUDPCheckTable, nftFamily, NftUDPCheckTable)
	fmt.Fprintf(&b, "table %s %s {\n", nftFamily, NftUDPCheckTable)
	for _, port := range ports {
		fmt.Fprintf(&b, "\tcounter p%d {\n\t}\n", port)
	}
	b.WriteString("\tchain pre {\n\t\ttype filter hook prerouting priority -500; policy accept;\n")
	for _, port := range ports {
		fmt.Fprintf(&b, "\t\tudp dport %d @th,64,64 0x%x counter name \"p%d\" drop\n", port, tag, port)
	}
	b.WriteString("\t}\n}\n")
	return b.String(), nil
}
