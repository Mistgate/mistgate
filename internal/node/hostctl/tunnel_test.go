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
	got, err := RenderTunnels([]Tunnel{a, b}, []PublishedPort{
		{Network: "tcp", Port: 8443},
		{Network: "udp", Port: 3478},
	})
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
		iifname "mgawg*" ip daddr { 10.66.8.1, 10.66.4.1 } drop
		iifname "mgawg*" ip6 daddr { fd66:66:0:1::1 } drop
		iifname "mgawg*" tcp dport { 443, 8443 } accept
		iifname "mgawg*" udp dport 3478 accept
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

func TestRenderTunnelsAllowsHTTPSBeforeLocalDrop(t *testing.T) {
	rules, err := RenderTunnels([]Tunnel{tun(51842, "10.66.4.0/22", "fd66:66:0:1::/64")}, []PublishedPort{{Network: "tcp", Port: 8443}})
	if err != nil {
		t.Fatal(err)
	}
	allow := `iifname "mgawg*" tcp dport { 443, 8443 } accept`
	udpAllow := `iifname "mgawg*" udp dport {`
	drop := `iifname "mgawg*" drop`
	own4 := `iifname "mgawg*" ip daddr { 10.66.4.1 } drop`
	own6 := `iifname "mgawg*" ip6 daddr { fd66:66:0:1::1 } drop`
	allowAt, dropAt := strings.Index(rules, allow), strings.Index(rules, drop)
	own4At, own6At := strings.Index(rules, own4), strings.Index(rules, own6)
	if allowAt < 0 {
		t.Fatalf("published TCP access to the node is not allowed:\n%s", rules)
	}
	if strings.Contains(rules, udpAllow) {
		t.Fatalf("an empty UDP allowlist was rendered:\n%s", rules)
	}
	// the tunnel's own address stays closed: published ports are open only at the host's other addresses
	if own4At < 0 || own4At > allowAt || own6At < 0 || own6At > allowAt {
		t.Fatalf("the IPv4 and IPv6 tunnel addresses must be dropped before published ports are allowed:\n%s", rules)
	}
	if dropAt < 0 || allowAt > dropAt {
		t.Fatalf("published-port allow must precede the local-service drop:\n%s", rules)
	}
	for _, forbidden := range []string{`tcp dport 22 accept`, `tcp dport 18089 accept`, `tcp dport 18443 accept`} {
		if strings.Contains(rules, forbidden) {
			t.Fatalf("management port escaped the local-service drop: %s\n%s", forbidden, rules)
		}
	}
	if !strings.Contains(rules, `tcp dport { 443, 8443 } accept`) {
		t.Fatalf("TCP 443 was not retained with the configured TCP listener:\n%s", rules)
	}
}

func TestRenderTunnelsOrderAndEmpty(t *testing.T) {
	a, b := tun(51842, "10.66.4.0/22", ""), tun(40001, "10.66.8.0/22", "")
	portsA := []PublishedPort{{Network: "udp", Port: 9000}, {Network: "tcp", Port: 8443}, {Network: "tcp", Port: 8443}}
	portsB := []PublishedPort{{Network: "tcp", Port: 8443}, {Network: "udp", Port: 9000}}
	x, _ := RenderTunnels([]Tunnel{a, b}, portsA)
	y, _ := RenderTunnels([]Tunnel{b, a}, portsB)
	if x != y {
		t.Error("the script depends on the order of the tunnels")
	}
	got, err := RenderTunnels(nil, nil)
	if err != nil || got != "add table inet mistgate_awg\ndelete table inet mistgate_awg\n" {
		t.Fatalf("empty = %q, %v", got, err)
	}
	withTunnel, err := RenderTunnels([]Tunnel{a}, nil)
	if err != nil || !strings.Contains(withTunnel, `iifname "mgawg*" tcp dport 443 accept`) {
		t.Fatalf("no extra listeners must retain the TCP 443 exception:\n%s (%v)", withTunnel, err)
	}
	// Every tunnel through WARP: no postrouting chain at all.
	a.ViaWarp, b.ViaWarp = true, true
	if s, _ := RenderTunnels([]Tunnel{a, b}, nil); strings.Contains(s, "masquerade") || strings.Contains(s, "chain post") {
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
		if s, err := RenderTunnels([]Tunnel{x}, nil); err == nil {
			t.Errorf("%s accepted:\n%s", name, s)
		}
	}
	for _, badPort := range []PublishedPort{
		{Network: "tcp", Port: 0},
		{Network: "udp", Port: 65536},
		{Network: "sctp", Port: 8443},
	} {
		if s, err := RenderTunnels([]Tunnel{ok}, []PublishedPort{badPort}); err == nil {
			t.Errorf("invalid published port %+v accepted:\n%s", badPort, s)
		}
	}
	dup := tun(51842, "10.66.8.0/22", "")
	if _, err := RenderTunnels([]Tunnel{ok, dup}, nil); err == nil {
		t.Error("two tunnels on one port accepted")
	}
	overlap := tun(40001, "10.66.5.0/24", "")
	if _, err := RenderTunnels([]Tunnel{ok, overlap}, nil); err == nil {
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
	c = a
	c.RejectV6 = true
	if TunnelsEqual([]Tunnel{a}, []Tunnel{c}) {
		t.Error("RejectV6 is part of the rules")
	}
	if !TunnelsEqual(nil, nil) || TunnelsEqual([]Tunnel{a}, nil) {
		t.Error("empty")
	}
}

// "IPv6 for clients" off: a tunnel with an IPv6 subnet that exits directly gets two reject rules for IPv6 towards
// anything that is not a tunnel interface (a TCP reset for TCP, ICMPv6 admin-prohibited for the rest); nothing else in the
// table moves.
func TestRenderTunnelsRejectV6(t *testing.T) {
	a := tun(51842, "10.66.4.0/22", "fd66:66:0:1::/64")
	a.RejectV6 = true
	got, err := RenderTunnels([]Tunnel{a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `	chain nov6 {
		type filter hook forward priority filter; policy accept;
		iifname "mgawg51842" oifname != "mgawg*" meta nfproto ipv6 meta l4proto tcp reject with tcp reset
		iifname "mgawg51842" oifname != "mgawg*" meta nfproto ipv6 reject with icmpx type admin-prohibited
	}
`
	if !strings.Contains(got, want) {
		t.Fatalf("no reject rule:\n%s", got)
	}
	// The client keeps its IPv6 inside the tunnel: the masquerade and the input policy are those of the "on" table.
	a.RejectV6 = false
	on, _ := RenderTunnels([]Tunnel{a}, nil)
	if strings.Contains(on, "nov6") || strings.Replace(got, want, "", 1) != on {
		t.Errorf("the switch changes more than the reject chain:\n%s\n---\n%s", got, on)
	}
	// Only the tunnel that asked gets a rule.
	b := tun(40001, "10.66.8.0/22", "fd66:66:0:2::/64")
	a.RejectV6 = true
	two, _ := RenderTunnels([]Tunnel{a, b}, nil)
	if strings.Count(two, "reject with") != 2 || strings.Contains(two, `iifname "mgawg40001" oifname`) ||
		!strings.Contains(two, `iifname "mgawg51842" oifname != "mgawg*" meta nfproto ipv6 reject`) {
		t.Errorf("two tunnels, one off:\n%s", two)
	}
	// The drop fallback (a kernel without nft reject) is one rule: there is no RST to send.
	dropped, _ := renderTunnels([]Tunnel{a}, v6Drop, nil)
	if strings.Contains(dropped, "tcp reset") || strings.Count(dropped, "meta nfproto ipv6 drop") != 1 {
		t.Errorf("drop fallback:\n%s", dropped)
	}
	// Not for a WARP tunnel (the exit is Cloudflare's), not for one without IPv6.
	w, v4 := a, tun(40002, "10.66.12.0/22", "")
	w.ViaWarp, v4.RejectV6 = true, true
	if s, _ := RenderTunnels([]Tunnel{w, v4}, nil); strings.Contains(s, "reject") || strings.Contains(s, "nov6") {
		t.Errorf("a WARP or IPv4-only tunnel got the rule:\n%s", s)
	}
}
