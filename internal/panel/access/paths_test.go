package access

import (
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// serverPaths: the direct path first, then the IPv6 entry, then each relay; a server that cannot go to another address, and a node
// with no usable entry, have what they have and nothing else.
func TestServerPathsOrderAndWhatHasNoOtherAddress(t *testing.T) {
	relay := func(via string) store.RelayLink { return store.RelayLink{Relay: store.Relay{ViaNodeID: via}} }
	keys := func(p []serverPath) []string {
		var out []string
		for _, x := range p {
			out = append(out, x.Key)
		}
		return out
	}
	withEntry := store.AccessNode{ID: "nod_de1", Address: "192.0.2.10", IPv6Entry: "2001:db8::5"}
	noEntry := store.AccessNode{ID: "nod_de1", Address: "192.0.2.10"}
	relays := []store.RelayLink{relay("nod_fi1"), relay("nod_nl1")}

	if got := keys(serverPaths(withEntry, plugin.InboundSpec{}, relays)); !slices.Equal(got, []string{"direct", "ipv6", "relay:nod_fi1", "relay:nod_nl1"}) {
		t.Errorf("entry on = %v", got)
	}
	if got := keys(serverPaths(noEntry, plugin.InboundSpec{}, relays)); !slices.Equal(got, []string{"direct", "relay:nod_fi1", "relay:nod_nl1"}) {
		t.Errorf("entry off = %v", got)
	}
	if got := keys(serverPaths(withEntry, plugin.InboundSpec{}, nil)); !slices.Equal(got, []string{"direct", "ipv6"}) {
		t.Errorf("entry on, no relays = %v", got)
	}
	acmeIP := plugin.InboundSpec{TLS: plugin.TLS{Mode: plugin.TLSAcmeIP}} // a certificate for the node's IPv4 address: no other address
	if got := keys(serverPaths(withEntry, acmeIP, relays)); !slices.Equal(got, []string{"direct"}) {
		t.Errorf("a server with no IPv6 twin = %v, want the direct path alone", got)
	}
}
