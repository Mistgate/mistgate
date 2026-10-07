package access

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

// What only the user page shows (the DNS of each node, the presets they name, the stale keys) is read for the page, not for
// every fetch of an app: a fetch of the link is the most frequent request the panel gets. The Mihomo format still needs the
// DNS of a node for its AmneziaWG proxies. The node DNS tables are gone here, so a read of them is logged.
func TestAppFetchDoesNotReadThePageData(t *testing.T) {
	m, uid, token := awgOnBothNodes(t)
	e := m.e
	e.offer("nod_de1", "dns_builtin_adblock", "dns_builtin_adblock", "dns_builtin_standard")
	if err := e.s.SetPageDNS(e.ctx, uid, PageDNSChoice{NodeID: "nod_de1", PresetID: "dns_builtin_standard"}); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	e.s.log = slog.New(slog.NewTextHandler(&logged, nil))
	e.sql(`DROP TABLE node_dns_option`)
	read := func(opt SubOptions) (SubView, bool) {
		t.Helper()
		logged.Reset()
		v := must(e.s.SubscriptionWith(e.ctx, token, opt))
		return v, strings.Contains(logged.String(), "subscription DNS state")
	}

	v, read1 := read(SubOptions{NoPageData: true})
	if read1 || len(v.Nodes) != 2 || len(v.Lines) == 0 || v.Nodes[0].DNS != nil || len(v.DNSPresets) != 0 || v.DNSLink != "" {
		t.Errorf("an app's fetch: read the DNS data %v, %d nodes, %d lines, dns %+v, presets %d, link %q", read1, len(v.Nodes), len(v.Lines), v.Nodes[0].DNS, len(v.DNSPresets), v.DNSLink)
	}
	if _, read2 := read(SubOptions{Format: plugin.FormatMihomo, NoPageData: true}); !read2 {
		t.Error("the Mihomo profile does not read the DNS of a node for its AmneziaWG proxies")
	}
	if _, read3 := read(SubOptions{}); !read3 {
		t.Error("the page's view does not read the DNS data")
	}
}
