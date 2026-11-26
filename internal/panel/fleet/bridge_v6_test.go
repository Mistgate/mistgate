package fleet

import (
	"testing"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The bridge is one transport per pair, so its hop has one family: a relay that uses it over IPv6 makes the exit dial the entry's
// IPv6 address, and a family pinned back to IPv4 brings the dial back to the entry's own address.
func TestBridgeDialsTheEntryOverTheLegsIPv6(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	_, _, _ = connectCaps(x.exit, "exit", agentcap.RelayReverse, agentcap.VLESSTLS)
	_, _, _ = connectCaps(x.entry, "entry", agentcap.RelayForward, agentcap.RelayReverse)
	x.exec(`UPDATE node SET ipv6_entry = '2001:db8::7' WHERE id = ?`, x.entry.nodeID)
	leg := store.RelayLegIPv6
	x.setRelay("rly_a", store.RelayPatch{Leg: &leg})
	exitNode, err := x.st.Node(x.ctx, x.exit.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, dials, err := x.f.bridgeDesired(x.ctx, exitNode, exitNode.AgentCaps)
	if err != nil || len(dials) != 1 || dials[0].Host != "2001:db8::7" {
		t.Fatalf("the bridge over IPv6 = %+v, err %v", dials, err)
	}
	leg = store.RelayLegIPv4
	x.setRelay("rly_a", store.RelayPatch{Leg: &leg})
	if _, dials, err = x.f.bridgeDesired(x.ctx, exitNode, exitNode.AgentCaps); err != nil || len(dials) != 1 || dials[0].Host != "entry.example.com" {
		t.Fatalf("the bridge pinned back to IPv4 = %+v, err %v", dials, err)
	}
}
