package hostctl

import (
	"net/netip"
	"strings"
	"testing"
)

func tun(port uint16, sub string, v6 string) Tunnel {
	p := netip.MustParsePrefix(sub)
	t := Tunnel{Iface: TunnelIface(port), Subnet4: p.Masked(), Addr4: p.Addr().Next(), UDPPort: port}
	if p.Addr().Is4() && p.Addr() != p.Masked().Addr() {
		t.Addr4 = p.Addr()
	}
	if v6 != "" {
		q := netip.MustParsePrefix(v6)
		t.Subnet6, t.Addr6 = q.Masked(), q.Masked().Addr().Next()
	}
	return t
}

func TestRenderTunnelsGolden(t *testing.T) {
	a := tun(51842, "10.66.4.0/22", "fd66:66:0:1::/64")
	b := tun(40001, "10.66.8.0/22", "")
	b.ViaWarp = true
	got, err := RenderTunnels([]Tunnel{a, b})
	if err != nil {
		t.Fatal(err)
	}
	want := `add table inet mistgate_awg
delete table inet mistgate_awg
table inet mistgate_awg {
	counter udp_40001 {
	}
	counter udp_51842 {
	}
	chain input {
		type filter hook input priority filter; policy accept;
		udp dport 40001 counter name udp_40001
		udp dport 51842 counter name udp_51842
		iifname "mgawg40001" ip daddr 10.66.8.1 icmp type echo-request accept
		iifname "mgawg51842" ip daddr 10.66.4.1 icmp type echo-request accept
		iifname "mgawg51842" ip6 daddr fd66:66:0:1::1 icmpv6 type echo-request accept
		iifname "mgawg*" drop
	}
	chain isolate {
		type filter hook forward priority filter; policy accept;
		iifname "mgawg*" oifname "mgawg*" drop
	}
	chain clamp {
		type filter hook forward priority mangle; policy accept;
		oifname "mgawg*" tcp flags syn tcp option maxseg size set rt mtu
		iifname "mgawg*" tcp flags syn tcp option maxseg size set rt mtu
	}
	chain post {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr 10.66.4.0/22 oifname != "mgawg*" masquerade
		ip6 saddr fd66:66:0:1::/64 oifname != "mgawg*" masquerade
	}
}
`
	if got != want {
		t.Fatalf("ruleset mismatch:\n%s", got)
	}
}

func TestRenderTunnelsOrderAndEmpty(t *testing.T) {
	a, b := tun(51842, "10.66.4.0/22", ""), tun(40001, "10.66.8.0/22", "")
	x, _ := RenderTunnels([]Tunnel{a, b})
	y, _ := RenderTunnels([]Tunnel{b, a})
	if x != y {
		t.Error("the script depends on the order of the tunnels")
	}
	got, err := RenderTunnels(nil)
	if err != nil || got != "add table inet mistgate_awg\ndelete table inet mistgate_awg\n" {
		t.Fatalf("empty = %q, %v", got, err)
	}
	// Every tunnel through WARP: no postrouting chain at all.
	a.ViaWarp, b.ViaWarp = true, true
	if s, _ := RenderTunnels([]Tunnel{a, b}); strings.Contains(s, "masquerade") || strings.Contains(s, "chain post") {
		t.Errorf("a WARP-only node masquerades here:\n%s", s)
	}
}

func TestRenderTunnelsRejects(t *testing.T) {
	ok := tun(51842, "10.66.4.0/22", "")
	bad := map[string]func(*Tunnel){
		"injection iface": func(x *Tunnel) { x.Iface = "mgawg1\"; flush ruleset; #" },
		"foreign iface":   func(x *Tunnel) { x.Iface = "eth0" },
		"long iface":      func(x *Tunnel) { x.Iface = "mgawg123456789012" },
		"v6 as subnet4":   func(x *Tunnel) { x.Subnet4 = netip.MustParsePrefix("fd00::/64") },
		"addr outside":    func(x *Tunnel) { x.Addr4 = netip.MustParseAddr("10.99.0.1") },
		"no port":         func(x *Tunnel) { x.UDPPort = 0 },
		"v6 addr only":    func(x *Tunnel) { x.Addr6 = netip.MustParseAddr("fd00::1") },
	}
	for name, mut := range bad {
		x := ok
		mut(&x)
		if s, err := RenderTunnels([]Tunnel{x}); err == nil {
			t.Errorf("%s accepted:\n%s", name, s)
		}
	}
	dup := tun(51842, "10.66.8.0/22", "")
	if _, err := RenderTunnels([]Tunnel{ok, dup}); err == nil {
		t.Error("two tunnels on one port accepted")
	}
	overlap := tun(40001, "10.66.5.0/24", "")
	if _, err := RenderTunnels([]Tunnel{ok, overlap}); err == nil {
		t.Error("overlapping client subnets accepted")
	}
}

func TestTunnelsEqual(t *testing.T) {
	a, b := tun(51842, "10.66.4.0/22", ""), tun(40001, "10.66.8.0/22", "")
	if !TunnelsEqual([]Tunnel{a, b}, []Tunnel{b, a}) {
		t.Error("order matters")
	}
	c := a
	c.ViaWarp = true
	if TunnelsEqual([]Tunnel{a}, []Tunnel{c}) {
		t.Error("ViaWarp is part of the rules")
	}
	if !TunnelsEqual(nil, nil) || TunnelsEqual([]Tunnel{a}, nil) {
		t.Error("empty")
	}
}
