package hostctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderHops(t *testing.T) {
	got, err := RenderHops([]Hop{
		{InboundID: "inb_b", Network: "udp", From: 30000, To: 31000, Port: 8443},
		{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `add table inet mistgate_node
delete table inet mistgate_node
table inet mistgate_node {
	chain hop {
		type nat hook prerouting priority dstnat; policy accept;
		udp dport 20000-29999 redirect to :443 comment "hop:inb_a"
		udp dport 30000-31000 redirect to :8443 comment "hop:inb_b"
	}
}
`
	if got != want {
		t.Fatalf("ruleset mismatch:\n%s", got)
	}
}

func TestRenderHopsEmptyIsCleanup(t *testing.T) {
	got, err := RenderHops(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "add table inet mistgate_node\ndelete table inet mistgate_node\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderHopsRejects(t *testing.T) {
	ok := Hop{InboundID: "inb_a", Network: "udp", From: 20000, To: 21000, Port: 443}
	for name, hops := range map[string][]Hop{
		"injection id":  {{InboundID: "x\"; flush ruleset; #", Network: "udp", From: 1, To: 2, Port: 3}},
		"newline id":    {{InboundID: "a\nb", Network: "udp", From: 1, To: 2, Port: 3}},
		"bad network":   {{InboundID: "a", Network: "sctp", From: 1, To: 2, Port: 3}},
		"inverted":      {{InboundID: "a", Network: "udp", From: 9, To: 2, Port: 3}},
		"zero port":     {{InboundID: "a", Network: "udp", From: 1, To: 2}},
		"zero from":     {{InboundID: "a", Network: "udp", To: 2, Port: 3}},
		"overlap":       {ok, {InboundID: "inb_b", Network: "udp", From: 21000, To: 22000, Port: 444}},
		"overlap order": {{InboundID: "inb_b", Network: "udp", From: 21000, To: 22000, Port: 444}, ok},
	} {
		if s, err := RenderHops(hops); err == nil {
			t.Errorf("%s: accepted:\n%s", name, s)
		}
	}
	// Same range on another network is fine.
	if _, err := RenderHops([]Hop{ok, {InboundID: "inb_t", Network: "tcp", From: 20000, To: 21000, Port: 443}}); err != nil {
		t.Errorf("udp/tcp same range: %v", err)
	}
}

func TestSysctlBaselineBody(t *testing.T) {
	if strings.Contains(sysctlFileBody, "cubic") || !strings.Contains(sysctlFileBody, "bbr") ||
		!strings.Contains(sysctlFileBody, "net.core.rmem_max = 16777216") ||
		!strings.Contains(sysctlFileBody, "net.core.wmem_max = 16777216") {
		t.Error("sysctl baseline must be fq + bbr with 16 MiB UDP buffers")
	}
}

func TestProcParsers(t *testing.T) {
	a, ok := parseCPUStat("cpu  100 0 50 800 40 5 5 0 0 0\ncpu0 1 1 1 1 1 1 1 1\n")
	if !ok || a.total != 1000 || a.idle != 840 || a.softirq != 5 {
		t.Fatalf("a=%+v ok=%v", a, ok)
	}
	b, _ := parseCPUStat("cpu  200 0 100 1600 80 10 50 0 0 0\n")
	busy, soft := cpuPct(a, b)
	// delta total 2000-1000... b.total = 2040, dt = 1040; idle delta = 840 -> busy 200/1040
	if busy < 19 || busy > 19.5 || soft < 4.3 || soft > 4.4 {
		t.Fatalf("busy=%v soft=%v", busy, soft)
	}
	if b, s := cpuPct(b, a); b != 0 || s != 0 { // counters went backwards
		t.Fatal("expected zeros on wrap")
	}

	total, avail := parseMeminfo("MemTotal:        1000 kB\nMemFree: 1 kB\nMemAvailable:     250 kB\n")
	if total != 1000*1024 || avail != 250*1024 {
		t.Fatalf("mem %d %d", total, avail)
	}

	dev := "Inter-|   Receive\n face |bytes packets\n  lo: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\neth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n"
	if rx, tx, ok := parseNetDev(dev, "eth0"); !ok || rx != 1000 || tx != 2000 {
		t.Fatalf("netdev %d %d %v", rx, tx, ok)
	}
	if _, _, ok := parseNetDev(dev, "wlan0"); ok {
		t.Fatal("unknown iface")
	}
	route := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nlo\t0000007F\t00000000\t0001\t0\t0\t0\t000000FF\neth0\t00000000\t0100A8C0\t0003\t0\t0\t100\t00000000\n"
	route6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo\n" +
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001   mgwarp\n" +
		"20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001     ens3\n" +
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003     ens3\n"
	if got := defaultIface(route, route6); got != "eth0" {
		t.Fatalf("iface %q", got)
	}
	// An IPv6-only node: the IPv6 default route's interface, not lo's unreachable default or WARP's own table.
	if got := defaultIface("Iface\tDestination\n", route6); got != "ens3" {
		t.Fatalf("IPv6-only iface %q", got)
	}
	if defaultIface("Iface\tDestination\n", "") != "" {
		t.Fatal("no default route")
	}
	// the counters the bandwidth test reads: the default route's interface and its byte counters, from a /proc tree
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"net/route": route, "net/ipv6_route": "", "net/dev": dev} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if iface, rx, tx, ok := NICCountersAt(root); !ok || iface != "eth0" || rx != 1000 || tx != 2000 {
		t.Fatalf("NICCountersAt = %q %d %d %v", iface, rx, tx, ok)
	}
	if _, _, _, ok := NICCountersAt(t.TempDir()); ok {
		t.Fatal("an empty /proc has no main interface")
	}
	if parseBtime("cpu 1\nbtime 1700000000\n") != 1700000000 {
		t.Fatal("btime")
	}
	if osPrettyName("NAME=x\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n") != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatal("os-release")
	}
}
