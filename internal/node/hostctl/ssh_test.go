package hostctl

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderSSHGuard(t *testing.T) {
	got, err := RenderRuleset([]Hop{{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443}}, []uint16{22, 2222})
	if err != nil {
		t.Fatal(err)
	}
	want := `add table inet mistgate_node
delete table inet mistgate_node
table inet mistgate_node {
	set ssh_v4 {
		type ipv4_addr; flags dynamic,timeout; timeout 10m; size 65536
	}
	set ssh_v6 {
		type ipv6_addr; flags dynamic,timeout; timeout 10m; size 65536
	}
	chain ssh {
		type filter hook input priority filter; policy accept;
		iifname "lo" accept
		tcp dport { 22, 2222 } ct state new add @ssh_v4 { ip saddr limit rate over 6/minute burst 10 packets } drop comment "ssh:new-rate"
		tcp dport { 22, 2222 } ct state new add @ssh_v6 { ip6 saddr & ffff:ffff:ffff:ffff:: limit rate over 6/minute burst 10 packets } drop comment "ssh:new-rate"
	}
	chain hop {
		type nat hook prerouting priority dstnat; policy accept;
		udp dport 20000-29999 redirect to :443 comment "hop:inb_a"
	}
}
`
	if got != want {
		t.Fatalf("ruleset mismatch:\n%s", got)
	}
	// The guard only ever matches NEW connections (established sessions are never touched), never bans,
	// and never drops loopback.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "drop") && !strings.Contains(line, "ct state new") {
			t.Errorf("a drop that is not limited to new connections: %q", line)
		}
	}
	if strings.Contains(got, "established") || strings.Contains(got, "reject") {
		t.Error("the guard must not touch established connections")
	}

	// Guard without hops: no hop chain. Nothing at all: the cleanup script.
	only, err := RenderRuleset(nil, []uint16{22})
	if err != nil || strings.Contains(only, "chain hop") || !strings.Contains(only, "chain ssh") {
		t.Fatalf("ssh only: %v\n%s", err, only)
	}
	none, _ := RenderRuleset(nil, nil)
	if none != "add table inet mistgate_node\ndelete table inet mistgate_node\n" {
		t.Fatalf("empty: %q", none)
	}
	if _, err := RenderRuleset(nil, []uint16{0}); err == nil {
		t.Error("port 0 accepted")
	}
}

func TestValidateHop(t *testing.T) {
	ok := Hop{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443}
	if err := ValidateHop(ok, []uint16{22, 2222}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mut      func(*Hop)
		reserved []uint16
	}{
		"whole space":   {func(h *Hop) { h.From, h.To = 1, 65535 }, nil},
		"from 22":       {func(h *Hop) { h.From, h.To = 22, 500 }, nil},
		"from 1023":     {func(h *Hop) { h.From, h.To = 1023, 2000 }, nil},
		"too wide":      {func(h *Hop) { h.From, h.To = 30000, 50000 }, nil}, // 20001 ports
		"sshd port":     {func(h *Hop) { h.From, h.To = 2000, 3000 }, []uint16{2222}},
		"other inbound": {func(h *Hop) { h.From, h.To = 20000, 20010 }, []uint16{20005}},
	} {
		h := ok
		tc.mut(&h)
		if err := ValidateHop(h, tc.reserved); err == nil {
			t.Errorf("%s: accepted %+v", name, h)
		}
	}
	// Exactly the limits are fine; the inbound's own port may sit inside its range.
	for _, h := range []Hop{
		{InboundID: "a", Network: "udp", From: MinHopPort, To: MinHopPort + MaxHopSpan - 1, Port: 443},
		{InboundID: "a", Network: "udp", From: 65535 - MaxHopSpan + 1, To: 65535, Port: 443},
		{InboundID: "a", Network: "udp", From: 30000, To: 30100, Port: 30050},
	} {
		if err := ValidateHop(h, []uint16{30050}); err != nil {
			t.Errorf("%+v: %v", h, err)
		}
	}
	// RenderRuleset refuses them too (defence in depth), with the ssh ports reserved.
	if _, err := RenderRuleset([]Hop{{InboundID: "a", Network: "udp", From: 2000, To: 3000, Port: 443}}, []uint16{2222}); err == nil {
		t.Error("RenderRuleset installed a hop over the sshd port")
	}
}

func TestSSHPortParsers(t *testing.T) {
	if got := normalizePorts(portsFrom(sshdTPortRe, "addressfamily any\nport 2200\nPort 22\nport 22\nport 99999\nlistenaddress 0.0.0.0\n")); !reflect.DeepEqual(got, []uint16{22, 2200}) {
		t.Errorf("sshd -T: %v", got)
	}
	conf := "# Port 1111\n  Port 2022 # high port\nPortable 5\nport 2023\nMatch User x\n"
	if got := normalizePorts(portsFrom(sshdConfPortRe, conf)); !reflect.DeepEqual(got, []uint16{2022, 2023}) {
		t.Errorf("sshd_config: %v", got)
	}
	sock := "Listen=[::]:2222 (Stream)\nListen=0.0.0.0:2222 (Stream)\nListen=/run/x.sock (Stream)\n"
	if got := normalizePorts(portsFrom(socketListenRe, sock)); !reflect.DeepEqual(got, []uint16{2222}) {
		t.Errorf("ssh.socket: %v", got)
	}
}
