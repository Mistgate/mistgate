package fleet

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The bridge of the reverse relays in the desired state (agent.proto "REVERSE RELAY"): the new fields reach only agents that list
// "relay-reverse/1", only for a relay that stands in the reverse way between two such agents; the hash of everything else is the
// hash it was; "none any more" is an empty message, "nothing changed" is no message.

const bridgePath = "/assets/a1"

// reverseRelay stores a TCP relay of the entry to the exit's server, switched on, and sets the way it stands in.
func (x *relayEnv) reverseRelay(id string, inbound string, port uint16, active string) {
	x.t.Helper()
	if err := x.st.CreateRelay(x.ctx, store.Relay{ID: id, InboundID: inbound, ViaNodeID: x.entry.nodeID, Network: "tcp", Port: port, Mode: store.RelayModeAuto}, time.Now()); err != nil {
		x.t.Fatal(err)
	}
	on := true
	x.setRelay(id, store.RelayPatch{Enabled: &on})
	x.setActive(id, active)
}

func (x *relayEnv) setActive(id, active string) {
	x.t.Helper()
	if err := x.st.SetRelayActiveMode(x.ctx, id, active, "leg_failed", time.Now()); err != nil {
		x.t.Fatal(err)
	}
	x.f.StateChanged()
}

func (x *relayEnv) givePath(port uint16) {
	x.t.Helper()
	if _, err := x.st.EnsureNodeBridgePath(x.ctx, x.entry.nodeID, bridgePath); err != nil {
		x.t.Fatal(err)
	}
	if err := x.st.SetNodeBridgePort(x.ctx, x.entry.nodeID, port); err != nil {
		x.t.Fatal(err)
	}
	x.f.StateChanged()
}

func (x *relayEnv) plainForward(id string, port uint16) plugin.RelayForward {
	return plugin.RelayForward{ID: id, Network: "tcp", Port: port, ToHost: "exit.example.com", ToPort: 4443}
}

// An agent that does not list the capability is sent none of it, whatever the way the relay stands in, and its hash is the hash of a
// state without them; the plain forward is what the agent with relay-forward/1 alone has always been sent.
func TestBridgeFieldsReachOnlyAgentsThatListTheCapability(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(0)
	x.reverseRelay("rly_a", relayInbound, 8443, store.RelayModeReverse)

	// Both ends list relay-forward/1 only: a reverse way is the DNAT forward it was, and nothing of the bridge is sent.
	_, em, eds := connectCaps(x.exit, "exit-old")
	if eds.BridgeServe != nil || eds.BridgeDials != nil || eds.Relays != nil || len(eds.Inbounds) != 2 || em.hash() != eds.StateHash {
		t.Fatalf("an exit without the capability: serve %v dials %v relays %v", eds.BridgeServe, eds.BridgeDials, eds.Relays)
	}
	_, m, ds := connectCaps(x.entry, "entry-old", plugin.CapRelayForward)
	plain := x.plainForward("rly_a", 8443)
	if ds.BridgeServe != nil || ds.BridgeDials != nil || len(ds.Relays.GetItems()) != 1 || ds.Relays.Items[0].Mode != "" || ds.Relays.Items[0].ExitNodeId != "" {
		t.Fatalf("an entry without relay-reverse/1 was sent the bridge: %v", ds)
	}
	if want := statehash.StateRelays(nil, nil, []plugin.RelayForward{plain}); ds.StateHash != want || m.hash() != want {
		t.Fatalf("hash %s, want the one of a plain forward %s", ds.StateHash, want)
	}

	// The entry lists both, the exit does not: the pair cannot do it, the relay stays a plain forward and no bridge is served.
	_, m, ds = connectCaps(x.entry, "entry-new", plugin.CapRelayForward, plugin.CapRelayReverse)
	if ds.BridgeServe != nil || ds.Relays.Items[0].Mode != "" || m.hash() != statehash.StateRelays(nil, nil, []plugin.RelayForward{plain}) {
		t.Fatalf("a pair with an old exit: %v", ds)
	}
}

func TestReversePairIsSentTheBridge(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	ec, em, eds := connectCaps(x.exit, "exit", plugin.CapRelayReverse)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	_, m, ds := connectCaps(x.entry, "entry", plugin.CapRelayForward, plugin.CapRelayReverse)

	// The entry: the forward carries the way and the exit, the serve setting says where the exit calls and whom it accepts.
	wantRelays := &agentv1.RelayForwards{Items: []*agentv1.RelayForward{{RelayId: "rly_a", Network: "tcp", Port: 7443, ToHost: "exit.example.com", ToPort: 4443,
		Mode: "reverse", ExitNodeId: x.exit.nodeID}}}
	wantServe := &agentv1.BridgeServe{Port: 8443, Path: bridgePath, ExitNodeIds: []string{x.exit.nodeID}}
	if !proto.Equal(ds.Relays, wantRelays) || !proto.Equal(ds.BridgeServe, wantServe) || ds.BridgeDials != nil {
		t.Fatalf("the entry: relays %v serve %v dials %v", ds.Relays, ds.BridgeServe, ds.BridgeDials)
	}
	if want := statehash.StateBridge(nil, nil, []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 7443, ToHost: "exit.example.com", ToPort: 4443, Mode: "reverse", ExitNodeID: x.exit.nodeID}},
		&plugin.BridgeServe{Port: 8443, Path: bridgePath, ExitNodeIDs: []string{x.exit.nodeID}}, nil); ds.StateHash != want || m.hash() != want {
		t.Fatalf("the entry's hash %s, want %s", ds.StateHash, want)
	}

	// The exit learns of the entry when the entry's agent has listed the capability: the dials are a delta, the server's own port is
	// the target, and the entry's name and pin are not yet the panel's to give (an agent refuses a dial with neither).
	if eds.BridgeDials != nil || eds.BridgeServe != nil {
		t.Fatalf("the exit was sent a bridge before the entry could take it: %v", eds)
	}
	x.f.StateChanged()
	d := ec.next(em)
	wantDials := &agentv1.BridgeDials{Items: []*agentv1.BridgeDial{{EntryNodeId: x.entry.nodeID, Host: "entry.example.com", Port: 8443, Path: bridgePath,
		Targets: []*agentv1.BridgeTarget{{RelayId: "rly_a", Network: "tcp", Port: 4443}}}}}
	if !proto.Equal(d.BridgeDials, wantDials) || d.BridgeServe != nil || d.Relays != nil || len(d.Inbounds) != 0 {
		t.Fatalf("the exit's delta: %v", d)
	}

	// A bridge on 443 of the site has port 0 on the entry and 443 in the dial.
	x.givePath(0)
	if d = ec.next(em); d.BridgeDials.Items[0].Port != 443 {
		t.Fatalf("the default port of the bridge: %v", d.BridgeDials)
	}
}

// "None any more" is an empty message, "nothing changed" is no message at all, on both sides of the bridge.
func TestBridgeEmptyIsNotAbsent(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(0)
	ec, em, eds := connectCaps(x.exit, "exit", plugin.CapRelayReverse)
	exitHash := eds.StateHash // the exit with its two servers and no bridge
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	c, m, _ := connectCaps(x.entry, "entry", plugin.CapRelayForward, plugin.CapRelayReverse)
	x.f.StateChanged()
	ec.next(em)
	if len(m.bridgeDials) != 0 || m.bridgeServe == nil || len(em.bridgeDials) != 1 || em.bridgeServe != nil {
		t.Fatalf("held: entry serve %v, exit dials %v", m.bridgeServe, em.bridgeDials)
	}

	// An unrelated change on the entry (a second, plain forward) is a delta with the relay list and nothing of the bridge.
	x.relayOf("rly_b", relayInbound2, 9443, true)
	d := c.next(m)
	if d.Relays == nil || len(d.Relays.Items) != 2 || d.BridgeServe != nil || d.BridgeDials != nil {
		t.Fatalf("an unrelated delta carries the bridge: %v", d)
	}
	ec.quiet(300 * time.Millisecond) // the exit has nothing to learn of it

	// The relay goes back to the plain way: the list loses its mode, the entry is told to stop (present and empty), the exit that its
	// list is empty (present and empty), and the hash of both is the one without a bridge.
	x.setActive("rly_a", store.RelayModeDNAT)
	d = c.next(m)
	if d.BridgeServe == nil || !proto.Equal(d.BridgeServe, &agentv1.BridgeServe{}) || d.BridgeDials != nil || d.Relays == nil || d.Relays.Items[0].Mode != "" {
		t.Fatalf("the entry told to stop: serve %v relays %v", d.BridgeServe, d.Relays)
	}
	if m.bridgeServe == nil || !m.bridgeServe.IsZero() {
		t.Fatalf("the model after an empty serve: %+v", m.bridgeServe)
	}
	if want := statehash.StateRelays(nil, nil, []plugin.RelayForward{x.plainForward("rly_a", 7443), {ID: "rly_b", Network: "udp", Port: 9443, ToHost: "exit.example.com", ToPort: 5443}}); m.hash() != want {
		t.Fatalf("the entry's hash after the way back %s, want %s", m.hash(), want)
	}
	ed := ec.next(em)
	if ed.BridgeDials == nil || len(ed.BridgeDials.Items) != 0 || ed.BridgeServe != nil || len(em.bridgeDials) != 0 || em.hash() != exitHash {
		t.Fatalf("the exit told its list is empty: %v", ed)
	}

	// And nothing more is sent while it stays so.
	x.f.StateChanged()
	c.quiet(300 * time.Millisecond)
	ec.quiet(300 * time.Millisecond)
}

// A relay that is not carried through the bridge whatever its way says: UDP servers, an entry that has no bridge path yet, a relay
// that is off and a server that is not usable.
func TestReverseWayWithoutWhatItNeedsIsPlain(t *testing.T) {
	x := newRelayEnv(t)
	connectCaps(x.exit, "exit", plugin.CapRelayReverse)
	// A UDP server (the default of relayOf) in the reverse way, an entry without a path: the plain forward.
	x.relayOf("rly_u", relayInbound2, 6443, true)
	x.setActive("rly_u", store.RelayModeReverse)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	c, m, ds := connectCaps(x.entry, "entry", plugin.CapRelayForward, plugin.CapRelayReverse)
	if ds.BridgeServe != nil || len(ds.Relays.Items) != 2 || ds.Relays.Items[0].Mode != "" || ds.Relays.Items[1].Mode != "" || len(m.relays) != 2 {
		t.Fatalf("no bridge path on the entry: %v", ds)
	}
	// With the path, the TCP relay is carried and the UDP relay still is not.
	x.givePath(0)
	d := c.next(m)
	if d.BridgeServe == nil || d.BridgeServe.Path != bridgePath || d.Relays.Items[0].RelayId != "rly_a" || d.Relays.Items[0].Mode != "reverse" ||
		d.Relays.Items[1].RelayId != "rly_u" || d.Relays.Items[1].Mode != "" || d.Relays.Items[1].ExitNodeId != "" {
		t.Fatalf("with a path: %v", d)
	}
	// A relay that is off or whose server is not usable is not carried and not served.
	off := false
	x.setRelay("rly_a", store.RelayPatch{Enabled: &off})
	d = c.next(m)
	if d.BridgeServe == nil || d.BridgeServe.Path != "" || len(d.Relays.Items) != 1 || d.Relays.Items[0].RelayId != "rly_u" {
		t.Fatalf("a relay switched off: %v", d)
	}
	on := true
	x.setRelay("rly_a", store.RelayPatch{Enabled: &on})
	c.next(m)
	x.exec(`UPDATE inbound SET state = 'failed' WHERE id = ?`, relayInbound)
	x.f.StateChanged()
	if d = c.next(m); d.BridgeServe == nil || d.BridgeServe.Path != "" || len(d.Relays.Items) != 1 {
		t.Fatalf("a failed server: %v", d)
	}
}
