package hostctl

import (
	"strings"
	"testing"
)

func TestRenderTorrentGuardScopesExactActiveAWGInterfaces(t *testing.T) {
	rules, err := RenderTorrentGuard([]string{"mgawg51821", "mgawg51820"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rules, "add table inet "+NftTorrentTable+"\ndelete table inet "+NftTorrentTable+"\n") {
		t.Fatalf("rules do not begin with an atomic owned-table reset:\n%s", rules)
	}
	for _, want := range []string{
		"table inet " + NftTorrentTable,
		"chain forward {",
		// a block verdict becomes the connection's ct mark, and the kernel drops the rest of it in both directions
		"meta mark 0x4d475442 ct mark set 0x4d475442 drop\n\t\tct mark 0x4d475442 drop\n",
		// only what clients send out, and only the start of a flow
		`iifname { "mgawg51820", "mgawg51821" } oifname != { "mgawg51820", "mgawg51821" } meta l4proto tcp ct direction original ct original packets <= 6 queue num 4242 bypass`,
		`iifname { "mgawg51820", "mgawg51821" } oifname != { "mgawg51820", "mgawg51821" } meta l4proto udp ct state new queue num 4242 bypass`,
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules do not contain %q:\n%s", want, rules)
		}
	}
	if strings.Contains(rules, "oifname {") || strings.Count(rules, "queue num") != 2 {
		t.Fatalf("traffic towards the clients is queued:\n%s", rules)
	}
	if strings.Contains(rules, `iifname "mgawg*"`) || strings.Contains(rules, `oifname "mgawg*"`) {
		t.Fatalf("rules use a wildcard instead of the active interface set:\n%s", rules)
	}
	if strings.Contains(rules, "mistgate_node") || strings.Contains(rules, "mistgate_awg") {
		t.Fatalf("rules touch another Mistgate table:\n%s", rules)
	}
}

func TestRenderTorrentGuardEmptySetDeletesOnlyItsTable(t *testing.T) {
	rules, err := RenderTorrentGuard(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rules != "add table inet mistgate_torrentguard\ndelete table inet mistgate_torrentguard\n" {
		t.Fatalf("empty rules = %q", rules)
	}
}

func TestRenderTorrentGuardRejectsInvalidInterfacesAndDuplicates(t *testing.T) {
	for _, ifaces := range [][]string{
		{"eth0"},
		{"mgawg"},
		{"mgawg0;drop"},
		{"mgawg0", "mgawg0"},
		{"mgawg65536"},
	} {
		if _, err := RenderTorrentGuard(ifaces); err == nil {
			t.Errorf("RenderTorrentGuard(%q) succeeded", ifaces)
		}
	}
}
