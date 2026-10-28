package hostctl

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func fwd(id, network string, port uint16, to string, toPort uint16) Forward {
	return Forward{ID: id, Network: network, Port: port, ToIP: netip.MustParseAddr(to), ToPort: toPort}
}

func TestRenderRelaysGolden(t *testing.T) {
	// Given out of order: the table is sorted by id.
	// The node's addresses are given out of order and with a repeat: the set is sorted and unique.
	local := []netip.Addr{netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("198.51.100.1")}
	got, err := RenderRelays([]Forward{fwd("rly_b", "udp", 443, "198.51.100.20", 8443), fwd("rly_a", "tcp", 8443, "192.0.2.10", 443)}, local)
	if err != nil {
		t.Fatal(err)
	}
	want := `add table inet mistgate_relay
delete table inet mistgate_relay
table inet mistgate_relay {
	set local4 {
		type ipv4_addr
		elements = { 192.0.2.1, 198.51.100.1 }
	}
	counter tcp_8443 {
	}
	counter udp_443 {
	}
	chain pre {
		type nat hook prerouting priority dstnat; policy accept;
		meta nfproto ipv4 ip daddr @local4 tcp dport 8443 dnat ip to 192.0.2.10:443 comment "relay:rly_a"
		meta nfproto ipv4 ip daddr @local4 udp dport 443 dnat ip to 198.51.100.20:8443 comment "relay:rly_b"
	}
	chain post {
		type nat hook postrouting priority srcnat; policy accept;
		meta nfproto ipv4 ct status dnat ct original proto-dst 8443 ip daddr 192.0.2.10 tcp dport 443 masquerade
		meta nfproto ipv4 ct status dnat ct original proto-dst 443 ip daddr 198.51.100.20 udp dport 8443 masquerade
	}
	chain pass {
		type filter hook forward priority filter; policy accept;
		meta nfproto ipv4 ct status dnat ct original proto-dst 8443 ip daddr 192.0.2.10 tcp dport 443 counter name tcp_8443 accept
		meta nfproto ipv4 ct status dnat ct original proto-dst 443 ip daddr 198.51.100.20 udp dport 8443 counter name udp_443 accept
	}
}
`
	if got != want {
		t.Errorf("rendered table:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderRelaysEmptyDeletesOnly(t *testing.T) {
	got, err := RenderRelays(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "add table inet mistgate_relay\ndelete table inet mistgate_relay\n" {
		t.Errorf("empty set: %q", got)
	}
}

var testLocal = []netip.Addr{netip.MustParseAddr("198.51.100.1")}

// Forwards without any address of the node to match them against are not rendered: the rules would catch nothing or everything.
func TestRenderRelaysNeedsLocalAddresses(t *testing.T) {
	if _, err := RenderRelays([]Forward{fwd("rly_a", "tcp", 8443, "192.0.2.10", 443)}, nil); err == nil {
		t.Error("rendered without addresses")
	}
}

func TestOwnIPv4sHasNoLoopbackNoIPv6(t *testing.T) {
	as, err := OwnIPv4s()
	if err != nil {
		t.Skip(err)
	}
	for _, a := range as {
		if !a.Is4() || a.IsLoopback() {
			t.Errorf("%s is not a routable IPv4 address of the node", a)
		}
	}
}

func TestRenderRelaysRefusesWhatValidationRefuses(t *testing.T) {
	for name, fs := range map[string][]Forward{
		"private target":      {fwd("rly_a", "tcp", 8443, "10.0.0.5", 443)},
		"duplicate port":      {fwd("rly_a", "tcp", 8443, "192.0.2.10", 443), fwd("rly_b", "tcp", 8443, "192.0.2.11", 443)},
		"duplicate id":        {fwd("rly_a", "tcp", 8443, "192.0.2.10", 443), fwd("rly_a", "udp", 8443, "192.0.2.10", 443)},
		"quote in the id":     {fwd(`rly"a`, "tcp", 8443, "192.0.2.10", 443)},
		"newline in the id":   {fwd("rly_a\nflush ruleset", "tcp", 8443, "192.0.2.10", 443)},
		"low port":            {fwd("rly_a", "tcp", 80, "192.0.2.10", 443)},
		"unset target":        {{ID: "rly_a", Network: "tcp", Port: 8443, ToPort: 443}},
		"bad network":         {fwd("rly_a", "sctp", 8443, "192.0.2.10", 443)},
		"more than the limit": manyForwards(MaxForwards + 1),
	} {
		if _, err := RenderRelays(fs, testLocal); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	if _, err := RenderRelays(manyForwards(MaxForwards), testLocal); err != nil {
		t.Errorf("exactly the limit: %v", err)
	}
}

func manyForwards(n int) []Forward {
	fs := make([]Forward, n)
	for i := range fs {
		fs[i] = fwd(fmt.Sprintf("rly_%d", i), "tcp", uint16(2000+i), "192.0.2.10", 443)
	}
	return fs
}

func TestValidateForward(t *testing.T) {
	guard := ForwardGuard{
		SSHPorts: []uint16{22, 2222},
		Ports:    []NetPort{{"udp", 443}, {"tcp", 8080}},
		Hops:     []Hop{{InboundID: "inb_h", Network: "udp", From: 20000, To: 30000, Port: 443}},
		IsSelf:   func(a netip.Addr) bool { return a == netip.MustParseAddr("192.0.2.99") },
	}
	taken := []Forward{fwd("rly_a", "tcp", 8443, "192.0.2.10", 443)}
	for _, tc := range []struct {
		name  string
		f     Forward
		taken []Forward
		want  string // "" = accepted, else a part of the reason
	}{
		{"plain tcp", fwd("rly_x", "tcp", 9443, "192.0.2.10", 443), nil, ""},
		{"443/udp is an inbound here", fwd("rly_x", "udp", 443, "198.51.100.7", 8443), nil, "inbound of this node"},
		{"tcp 443 is allowed", fwd("rly_x", "tcp", 443, "203.0.113.5", 443), nil, ""},
		{"1024 is the lowest", fwd("rly_x", "tcp", 1024, "203.0.113.5", 443), nil, ""},
		{"1023", fwd("rly_x", "tcp", 1023, "203.0.113.5", 443), nil, "below 1024"},
		{"port 80", fwd("rly_x", "tcp", 80, "203.0.113.5", 443), nil, "below 1024"},
		{"port 22", fwd("rly_x", "tcp", 22, "203.0.113.5", 443), nil, "below 1024"},
		{"port 0", fwd("rly_x", "tcp", 0, "203.0.113.5", 443), nil, "below 1024"},
		{"sshd on a high port", fwd("rly_x", "tcp", 2222, "203.0.113.5", 443), nil, "sshd"},
		{"sshd port, other network", fwd("rly_x", "udp", 2222, "203.0.113.5", 443), nil, "sshd"},
		{"port of an inbound, same network", fwd("rly_x", "tcp", 8080, "203.0.113.5", 443), nil, "inbound of this node"},
		{"port of an inbound, other network", fwd("rly_x", "udp", 8080, "203.0.113.5", 443), nil, ""},
		{"inside a hop range", fwd("rly_x", "udp", 25000, "203.0.113.5", 443), nil, "hop range 20000-30000"},
		{"hop range edge", fwd("rly_x", "udp", 30000, "203.0.113.5", 443), nil, "hop range"},
		{"hop range, other network", fwd("rly_x", "tcp", 25000, "203.0.113.5", 443), nil, ""},
		{"duplicate network and port", fwd("rly_x", "tcp", 8443, "203.0.113.5", 443), taken, "already forwarded by rly_a"},
		{"same port, other network", fwd("rly_x", "udp", 8443, "203.0.113.5", 443), taken, ""},
		{"no target port", fwd("rly_x", "tcp", 9443, "203.0.113.5", 0), nil, "no target port"},
		{"own address", fwd("rly_x", "tcp", 9443, "192.0.2.99", 443), nil, "address of this node"},
		{"own address, mapped", Forward{ID: "rly_x", Network: "tcp", Port: 9443, ToIP: netip.MustParseAddr("::ffff:192.0.2.99"), ToPort: 443}, nil, "address of this node"},
		{"RFC 1918 10/8", fwd("rly_x", "tcp", 9443, "10.1.2.3", 443), nil, "not a public IPv4"},
		{"RFC 1918 172.16/12", fwd("rly_x", "tcp", 9443, "172.16.0.1", 443), nil, "not a public IPv4"},
		{"RFC 1918 192.168/16", fwd("rly_x", "tcp", 9443, "192.168.1.1", 443), nil, "not a public IPv4"},
		{"CGNAT", fwd("rly_x", "tcp", 9443, "100.64.0.1", 443), nil, "not a public IPv4"},
		{"loopback", fwd("rly_x", "tcp", 9443, "127.0.0.1", 443), nil, "not a public IPv4"},
		{"cloud metadata", fwd("rly_x", "tcp", 9443, "169.254.169.254", 443), nil, "not a public IPv4"},
		{"unspecified", fwd("rly_x", "tcp", 9443, "0.0.0.0", 443), nil, "not a public IPv4"},
		{"this network", fwd("rly_x", "tcp", 9443, "0.1.2.3", 443), nil, "not a public IPv4"},
		{"multicast", fwd("rly_x", "tcp", 9443, "224.0.0.1", 443), nil, "not a public IPv4"},
		{"reserved", fwd("rly_x", "tcp", 9443, "240.0.0.1", 443), nil, "not a public IPv4"},
		{"broadcast", fwd("rly_x", "tcp", 9443, "255.255.255.255", 443), nil, "not a public IPv4"},
		{"IPv6", fwd("rly_x", "tcp", 9443, "2001:db8::1", 443), nil, "not a public IPv4"},
		{"unset address", Forward{ID: "rly_x", Network: "tcp", Port: 9443, ToPort: 443}, nil, "not a public IPv4"},
		{"bad network", fwd("rly_x", "icmp", 9443, "203.0.113.5", 443), nil, "network"},
		{"bad id", fwd("rly x", "tcp", 9443, "203.0.113.5", 443), nil, "bad id"},
	} {
		err := ValidateForward(tc.f, tc.taken, guard)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && err == nil:
			t.Errorf("%s: accepted", tc.name)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("%s: %q, want a mention of %q", tc.name, err, tc.want)
		}
	}
	// 64 are enough, the 65th is refused.
	full := manyForwards(MaxForwards)
	if err := ValidateForward(fwd("rly_zz", "tcp", 9999, "203.0.113.5", 443), full, guard); err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Errorf("the 65th forward: %v", err)
	}
	if err := ValidateForward(fwd("rly_zz", "tcp", 9999, "203.0.113.5", 443), full[:MaxForwards-1], guard); err != nil {
		t.Errorf("the 64th forward: %v", err)
	}
	// A guard with nothing in it accepts a plain forward (the caller decides what the node uses).
	if err := ValidateForward(fwd("rly_x", "tcp", 22222, "203.0.113.5", 443), nil, ForwardGuard{}); err != nil {
		t.Errorf("empty guard: %v", err)
	}
}

func TestRelayTableIsOurs(t *testing.T) {
	if !OwnNftTable("inet", NftRelayTable) || NftRelayTable != "mistgate_relay" {
		t.Error("the doctor would call the relay table foreign")
	}
	if OwnNftTable("ip", NftRelayTable) {
		t.Error("only the inet table is ours")
	}
}

func TestForwardsEqualIgnoresOrder(t *testing.T) {
	a, b := fwd("rly_a", "tcp", 8443, "192.0.2.10", 443), fwd("rly_b", "udp", 443, "198.51.100.20", 8443)
	if !ForwardsEqual([]Forward{a, b}, []Forward{b, a}) || !ForwardsEqual(nil, []Forward{}) {
		t.Error("the same set in another order differs")
	}
	c := b
	c.ToIP = netip.MustParseAddr("198.51.100.21")
	if ForwardsEqual([]Forward{a, b}, []Forward{a, c}) || ForwardsEqual([]Forward{a}, []Forward{a, b}) {
		t.Error("another address or another size is equal")
	}
}

func TestIsOwnAddr(t *testing.T) {
	if !IsOwnAddr(netip.MustParseAddr("127.0.0.1")) {
		t.Error("loopback is not an address of the host")
	}
	if IsOwnAddr(netip.MustParseAddr("192.0.2.254")) {
		t.Error("a documentation address is not an address of the host")
	}
}
