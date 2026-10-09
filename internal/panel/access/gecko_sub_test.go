package access

import (
	"strings"
	"testing"
)

// A connection that only the Mihomo apps can use (Gecko) is still a server of the person: the URI list leaves it out, the
// view's nodes (the page's servers and their DNS) do not.
func TestSubViewListsNodesOnlyMihomoAppsCanUse(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	gecko := e.profile("gecko", `{"obfs":{"type":"gecko"},"port":8444}`)
	e.inbound(gecko.Id, "nod_nl1")
	e.inbound(gecko.Id, "nod_de1")
	u := e.user("both", e.group("all", f.profile, gecko.Id), nil).User

	v := must(e.s.Subscription(e.ctx, e.tokenOf(u.Id)))
	if len(v.Lines) != 1 || !strings.Contains(v.Lines[0], "de1.example.com") {
		t.Fatalf("the URI list holds %q, want the one salamander server of de1", v.Lines)
	}
	conns := map[string][]SubConn{}
	for _, n := range v.Nodes {
		conns[n.ID] = n.Conns
	}
	if len(conns) != 2 {
		t.Fatalf("nodes %v, want de1 and nl1", conns)
	}
	if c := conns["nod_nl1"]; len(c) != 1 || c[0].Way != "link" || !c[0].MihomoOnly {
		t.Errorf("nl1 (gecko only): %+v", c)
	}
	if c := conns["nod_de1"]; len(c) != 2 || !c[0].MihomoOnly || c[1].MihomoOnly || c[1].Server != 0 {
		t.Errorf("de1 (one link for every app, one for Mihomo apps): %+v", c)
	}
}
