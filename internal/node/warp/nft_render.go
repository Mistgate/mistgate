package warp

import (
	"fmt"
	"net/netip"
	"strings"
)

// renderNft returns the script that atomically replaces the WARP nft table. `add` + `delete` first makes it work
// whether or not the table exists, and nft applies a whole -f file as one transaction (same style as hostctl).
//
//   - post: masquerade on the tunnel device. WARP accepts only its assigned address as source, so every forwarded
//     client (and every bound local socket) leaves with that address.
//   - clamp: MSS clamp to the route MTU for forwarded TCP through the device.
//   - rsv (only when reserved is set and the kernel backend runs): stamps the three "reserved" bytes of the account
//     into every WireGuard message towards the endpoint, after encryption (the userspace backend does
//     the same in its Bind). UDP header is 8 bytes: message type at bit 64, reserved bytes 1..3 at bit 72.
func renderNft(table, iface string, reserved []byte, ep netip.AddrPort) string {
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\n", table, table)
	fmt.Fprintf(&b, "table inet %s {\n", table)
	fmt.Fprintf(&b, "\tchain post {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n\t\toifname %q masquerade\n\t}\n", iface)
	fmt.Fprintf(&b, "\tchain clamp {\n\t\ttype filter hook forward priority mangle; policy accept;\n\t\toifname %q tcp flags syn tcp option maxseg size set rt mtu\n\t}\n", iface)
	if len(reserved) == 3 && ep.IsValid() {
		fam := "ip"
		if ep.Addr().Is6() {
			fam = "ip6"
		}
		fmt.Fprintf(&b, "\tchain rsv {\n\t\ttype filter hook output priority mangle; policy accept;\n\t\t%s daddr %s udp dport %d @th,64,8 >= 1 @th,64,8 <= 4 @th,72,24 set 0x%02x%02x%02x\n\t}\n",
			fam, ep.Addr(), ep.Port(), reserved[0], reserved[1], reserved[2])
	}
	b.WriteString("}\n")
	return b.String()
}
