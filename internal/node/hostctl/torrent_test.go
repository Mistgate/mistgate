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
		`iifname { "mgawg51820", "mgawg51821" } oifname != { "mgawg51820", "mgawg51821" } meta l4proto { tcp, udp } queue num 4242 bypass`,
		`oifname { "mgawg51820", "mgawg51821" } iifname != { "mgawg51820", "mgawg51821" } meta l4proto { tcp, udp } queue num 4242 bypass`,
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules do not contain %q:\n%s", want, rules)
		}
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
