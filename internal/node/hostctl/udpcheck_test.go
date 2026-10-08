package hostctl

import (
	"strings"
	"testing"
)

func TestRenderUDPCount(t *testing.T) {
	tag := [8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	script, err := RenderUDPCount(tag, []uint16{443, 8443})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "table inet "+NftUDPCheckTable+" {") ||
		!strings.Contains(script, "type filter hook prerouting priority -500; policy accept;") {
		t.Fatalf("table or chain is missing:\n%s", script)
	}
	if strings.Count(script, "counter p") != 2 {
		t.Fatalf("counter declarations = %d, want one per port:\n%s", strings.Count(script, "counter p"), script)
	}
	rules := []string{
		`udp dport 443 @th,64,64 0x0123456789abcdef counter name "p443" drop`,
		`udp dport 8443 @th,64,64 0x0123456789abcdef counter name "p8443" drop`,
	}
	last := -1
	for _, rule := range rules {
		at := strings.Index(script, rule)
		if at < 0 || at <= last {
			t.Fatalf("rules are missing or out of order (%q):\n%s", rule, script)
		}
		last = at
	}
}

func TestRenderUDPCountRefusesInvalidPorts(t *testing.T) {
	for name, ports := range map[string][]uint16{
		"empty":     nil,
		"nine":      {1, 2, 3, 4, 5, 6, 7, 8, 9},
		"duplicate": {443, 443},
		"zero port": {0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RenderUDPCount([8]byte{}, ports); err == nil {
				t.Fatal("RenderUDPCount accepted invalid ports")
			}
		})
	}
}
