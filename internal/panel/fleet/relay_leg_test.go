package fleet

import (
	"testing"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The leg family on the wire (agent.proto RelayForward.to_family): an IPv6 leg sends the exit's IPv6 entry and says so, and only
// an agent with relay-forward/2 gets such a forward at all.
func TestRelayLegIPv6SendsTheOtherFamily(t *testing.T) {
	x := newRelayEnv(t)
	x.relay("rly_a", 8443, true)
	x.exec(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = ?`, x.exit.nodeID)
	x.exec(`UPDATE node SET agent_caps = 'relay-forward/1 relay-forward/2' WHERE id = ?`, x.entry.nodeID)
	leg := store.RelayLegIPv6
	x.setRelay("rly_a", store.RelayPatch{Leg: &leg})

	_, m, ds := connectCaps(x.entry, "new6", agentcap.RelayForward, agentcap.RelayForwardV6)
	if ds.Relays == nil || len(ds.Relays.Items) != 1 {
		t.Fatalf("forwards = %v", ds.Relays)
	}
	it := ds.Relays.Items[0]
	if it.ToHost != "2001:db8::5" || it.ToFamily != store.RelayFamily6 || it.ToPort != 4443 || it.Network != "udp" {
		t.Fatalf("an IPv6 forward = %+v", it)
	}
	if len(m.relays) != 1 || m.relays[0].ToFamily != store.RelayFamily6 || m.relays[0].ToHost != "2001:db8::5" {
		t.Fatalf("the agent holds %+v", m.relays)
	}

	// An entry that cannot carry the other family is not sent the forward: the relay is off in that way. Only relay-forward/1.
	_, m2, ds2 := connectCaps(x.entry, "old6", agentcap.RelayForward)
	if ds2.Relays != nil && len(ds2.Relays.Items) != 0 {
		t.Fatalf("an agent with only relay-forward/1 was sent %+v", ds2.Relays)
	}
	if len(m2.relays) != 0 {
		t.Fatalf("an agent with only relay-forward/1 holds %+v", m2.relays)
	}
}

// The leg check probes what the entry would forward to: the exit's IPv6 entry for an IPv6 leg.
func TestRelayCheckTargetsTheLegFamily(t *testing.T) {
	x := newCheckEnv(t, "entry", "exit", "s1")
	x.exec(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = ?`, x.exit.nodeID)
	x.exec(`UPDATE node SET agent_caps = agent_caps || ' relay-forward/2' WHERE id = ?`, x.entry.nodeID)
	x.relay("rly_u", 8443, true)
	leg := store.RelayLegIPv6
	x.setRelay("rly_u", store.RelayPatch{Leg: &leg})
	c, err := x.f.CheckRelay(x.ctx, "rly_u", store.RelayStageLeg)
	if err != nil {
		t.Fatal(err)
	}
	if c.To != "[2001:db8::5]:4443" {
		t.Fatalf("the leg was probed at %q, want the exit's IPv6 entry on its server's port", c.To)
	}
}
