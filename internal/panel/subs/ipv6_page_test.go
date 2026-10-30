package subs

import (
	"encoding/json"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
)

// The user page lists the IPv6 server as a way of the node, marked ipv6, next to the node's plain server and its relay's: one row
// for the node, three connections, the flag only on the IPv6 one.
func TestPageMarksTheIPv6ConnectionOfANode(t *testing.T) {
	v := access.SubView{
		Servers: []access.SubServer{
			{Node: "de1", CountryCode: "DE", Profile: "Hysteria2"},
			{Node: "de1", CountryCode: "DE", Profile: "Hysteria2", Mark: access.MarkIPv6},
			{Node: "de1", CountryCode: "DE", Profile: "Hysteria2", Mark: access.ViaMark("FI")},
		},
		Nodes: []access.SubNode{{ID: "nod_1", Name: "de1", CountryCode: "DE", Online: true, Conns: []access.SubConn{
			{Way: "link", Exit: "direct", Server: 0},
			{Way: "link", Exit: "direct", Server: 1, IPv6: true},
			{Way: "link", Exit: "direct", Server: 2, Via: "FI", Relay: true},
		}}},
	}
	nodes := pageServers(v, &adminv1.SubscriptionSettings{}, "en")
	if len(nodes) != 1 || len(nodes[0].Connections) != 3 {
		t.Fatalf("nodes: %+v", nodes)
	}
	c := nodes[0].Connections
	if c[0].IPv6 || c[0].Relay || !c[1].IPv6 || c[1].Relay || c[2].IPv6 || !c[2].Relay || c[2].Via != "FI" {
		t.Errorf("connections: %+v", c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), `"ipv6":true`); got != 1 {
		t.Errorf("%d ipv6 flags in %s, want one", got, b)
	}
	// the names the page shows for the apps carry the label
	if names := nodes[0].AppNames; len(names) != 3 || !strings.HasSuffix(names[1], " · IPv6") {
		t.Errorf("app names: %q", names)
	}
}
