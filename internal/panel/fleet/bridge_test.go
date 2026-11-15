package fleet

import (
	"context"
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The bridge of the reverse relays in the desired state (agent.proto "REVERSE RELAY"): the new fields reach only agents that list
// "relay-reverse/1", only for a relay that stands in the reverse way between two such agents; the hash of everything else is the
// hash it was; "none any more" is an empty message, "nothing changed" is no message.

const bridgePath = "/assets/a1"

// bridgePinTTLForTest is a little over the 15 minutes the panel trusts the entry's report of its pin.
const bridgePinTTLForTest = 16 * time.Minute

const bridgePin = "abababababababababababababababababababababababababababababababab"

// reverseRelay stores a TCP relay of the entry to the exit's server, switched on, and sets the way it stands in.
func (x *relayEnv) reverseRelay(id string, inbound string, port uint16, active string) {
	x.t.Helper()
	x.ensureTCPExitInbound()
	if err := x.st.CreateRelay(x.ctx, store.Relay{ID: id, InboundID: relayInboundTCP, ViaNodeID: x.entry.nodeID, Network: "tcp", Port: port, Mode: store.RelayModeAuto}, time.Now()); err != nil {
		x.t.Fatal(err)
	}
	on := true
	x.setRelay(id, store.RelayPatch{Enabled: &on})
	x.setActive(id, active)
	x.entryReportsPin()
}

func (x *relayEnv) ensureTCPExitInbound() {
	x.t.Helper()
	if _, err := x.st.FleetInbound(x.ctx, relayInboundTCP); err == nil {
		return
	}
	now := time.Now().Unix()
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_vless_tcp', 'vless', 'tcp exit', '{"port":4443,"security":"tls"}', 1, ?, ?)`, now, now)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at)
		VALUES (?, 'prf_vless_tcp', ?, 1, 'active', ?, ?)`, relayInboundTCP, x.exit.nodeID, now, now)
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
	x.entryReportsPin()
	x.f.StateChanged()
}

// entryReportsPin plays the entry's agent: the pin of the bridge site it runs, with the report of every relay through it (the
// panel keeps it there, with the report it came in; the process that builds the exit's state need not be the one that heard it).
func (x *relayEnv) entryReportsPin() {
	x.t.Helper()
	node, err := x.st.Node(x.ctx, x.entry.nodeID)
	if err != nil || node.BridgePort == 0 {
		return
	}
	links, err := x.st.RelayLinks(x.ctx, store.RelayFilter{ViaNodeID: x.entry.nodeID})
	if err != nil {
		x.t.Fatal(err)
	}
	for _, l := range links {
		if err := x.st.SetRelayBridgeSide(x.ctx, l.ID, "entry", &store.RelayBridgeSideCheck{Connected: true, Conns: 2,
			SinceUnix: x.f.now().Unix() - 60, AtUnix: x.f.now().Unix(), SitePin: bridgePin, SitePort: node.BridgePort}); err != nil {
			x.t.Fatal(err)
		}
	}
}

func (x *relayEnv) entryTLSite() {
	x.t.Helper()
	now := time.Now().Unix()
	x.exec(`UPDATE node SET address = 'entry.example.com' WHERE id = ?`, x.entry.nodeID)
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_entry_tls', 'vless', 'entry tls', '{"port":443,"security":"tls"}', 1, ?, ?)`, now, now)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at)
		VALUES ('inb_entry_tls', 'prf_entry_tls', ?, 1, 'active', ?, ?)`, x.entry.nodeID, now, now)
}

func (x *relayEnv) entrySelfSignedSite() {
	x.t.Helper()
	now := time.Now().Unix()
	x.exec(`UPDATE node SET address = 'entry.example.com' WHERE id = ?`, x.entry.nodeID)
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_entry_self', 'vless', 'entry self signed', '{"port":443,"security":"reality","reality":{"target":"self","server_names":["entry.example.com"]}}', 1, ?, ?)`, now, now)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at)
		VALUES ('inb_entry_self', 'prf_entry_self', ?, 1, 'active', ?, ?)`, x.entry.nodeID, now, now)
}

func assertAgentBridgeValid(t *testing.T, ds *agentv1.DesiredState, roots *x509.CertPool) {
	t.Helper()
	if ds.BridgeServe != nil && (ds.BridgeServe.Path != "" || len(ds.BridgeServe.ExitNodeIds) != 0) {
		entry := nodebridge.NewEntry()
		if err := entry.SetServe(nodebridge.ServeConfig{EntryNodeID: "nod_msk1", Path: ds.BridgeServe.Path,
			ExitNodeIDs: ds.BridgeServe.ExitNodeIds, Roots: roots}); err != nil {
			t.Fatalf("the node's BridgeServe validator rejected panel state %v: %v", ds.BridgeServe, err)
		}
	}
	if ds.BridgeDials == nil {
		return
	}
	dials := make([]nodebridge.DialSpec, 0, len(ds.BridgeDials.Items))
	for _, item := range ds.BridgeDials.Items {
		dial := plugin.BridgeDial{EntryNodeID: item.EntryNodeId, Host: item.Host, Port: uint16(item.Port), SNI: item.Sni,
			PinSHA256: item.PinSha256, Path: item.Path, Pool: item.Pool, PanelVia: item.PanelVia}
		for _, target := range item.Targets {
			dial.Targets = append(dial.Targets, plugin.BridgeTarget{RelayID: target.RelayId, Network: target.Network, Port: uint16(target.Port)})
		}
		dials = append(dials, nodebridge.DialSpec{Dial: dial})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dialer := nodebridge.NewDialer(nil)
	dialer.Sync(ctx, dials)
	defer dialer.Close()
	for _, state := range dialer.States() {
		if state.Error != "" {
			t.Fatalf("the node's BridgeDial validator rejected panel state %v: %s", ds.BridgeDials, state.Error)
		}
	}
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
	_, em, eds := connectCaps(x.exit, "exit-old", plugin.CapRelayForward, capVLESSTLS)
	if eds.BridgeServe != nil || eds.BridgeDials != nil || eds.Relays != nil || em.hash() != eds.StateHash {
		t.Fatalf("an exit without the capability: serve %v dials %v relays %v", eds.BridgeServe, eds.BridgeDials, eds.Relays)
	}
	_, m, ds := connectCaps(x.entry, "entry-old", plugin.CapRelayForward, capVLESSTLS)
	plain := x.plainForward("rly_a", 8443)
	if ds.BridgeServe != nil || ds.BridgeDials != nil || len(ds.Relays.GetItems()) != 1 || ds.Relays.Items[0].Mode != "" || ds.Relays.Items[0].ExitNodeId != "" {
		t.Fatalf("an entry without relay-reverse/1 was sent the bridge: %v", ds)
	}
	if want := statehash.StateRelays(nil, nil, []plugin.RelayForward{plain}); ds.StateHash != want || m.hash() != want {
		t.Fatalf("hash %s, want the one of a plain forward %s", ds.StateHash, want)
	}

	// The entry lists both, the exit does not: the pair cannot do it, the relay stays a plain forward and no bridge is served.
	_, m, ds = connectCaps(x.exit, "exit-old-again", plugin.CapRelayForward, capVLESSTLS)
	_, m, ds = connectCaps(x.entry, "entry-new", plugin.CapRelayForward, plugin.CapRelayReverse, capVLESSTLS)
	if ds.BridgeServe != nil || ds.Relays.Items[0].Mode != "" || m.hash() != statehash.StateRelays(nil, nil, []plugin.RelayForward{plain}) {
		t.Fatalf("a pair with an old exit: %v", ds)
	}
}

func TestReversePairIsSentTheBridge(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	ec, em, eds := connectCaps(x.exit, "exit", plugin.CapRelayReverse, capVLESSTLS)
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
	exitNode, err := x.st.Node(x.ctx, x.exit.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, desiredDials, err := x.f.bridgeDesired(x.ctx, exitNode, exitNode.AgentCaps)
	if err != nil || len(desiredDials) != 1 || desiredDials[0].PinSHA256 != bridgePin || desiredDials[0].Port != 8443 ||
		len(desiredDials[0].Targets) != 1 || desiredDials[0].Targets[0].RelayID != "rly_a" {
		t.Fatalf("direct exit bridge desired state = %+v, err %v", desiredDials, err)
	}
	x.f.StateChanged()
	d := ec.next(em)
	wantDials := &agentv1.BridgeDials{Items: []*agentv1.BridgeDial{{EntryNodeId: x.entry.nodeID, Host: "entry.example.com", Port: 8443, Path: bridgePath, Pool: 2,
		PinSha256: bridgePin, Targets: []*agentv1.BridgeTarget{{RelayId: "rly_a", Network: "tcp", Port: 4443}}}}}
	if !proto.Equal(d.BridgeDials, wantDials) || d.BridgeServe != nil || d.Relays != nil || len(d.Inbounds) != 0 {
		t.Fatalf("the exit's delta: %v", d)
	}
	assertAgentBridgeValid(t, d, x.pool)

	// A bridge on 443 of the site has port 0 on the entry and 443 in the dial.
	x.entryTLSite()
	x.givePath(0)
	x.f.StateChanged()
	if d = ec.next(em); d.BridgeDials == nil || len(d.BridgeDials.Items) != 1 || d.BridgeDials.Items[0].Port != 443 || d.BridgeDials.Items[0].Sni != "entry.example.com" || d.BridgeDials.Items[0].PinSha256 != "" {
		t.Fatalf("the default port of the bridge: %v", d.BridgeDials)
	}
	assertAgentBridgeValid(t, d, x.pool)
	x.exec(`UPDATE inbound SET cert_pin_sha256 = ? WHERE id = 'inb_entry_tls'`, bridgePin)
	x.f.StateChanged()
	if d = ec.next(em); d.BridgeDials == nil || d.BridgeDials.Items[0].PinSha256 != bridgePin || d.BridgeDials.Items[0].Sni != "entry.example.com" {
		t.Fatalf("the panel's known site certificate pin was not used: %v", d.BridgeDials)
	}
	assertAgentBridgeValid(t, d, x.pool)
}

func TestBridgeDoesNotUseSystemRootsForAnUnpinnedSelfSignedSite(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(0)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	x.entrySelfSignedSite()
	connectCaps(x.exit, "exit", plugin.CapRelayReverse, capVLESSTLS)
	connectCaps(x.entry, "entry", plugin.CapRelayForward, plugin.CapRelayReverse, capVLESSTLS)
	exitNode, err := x.st.Node(x.ctx, x.exit.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, dials, err := x.f.bridgeDesired(x.ctx, exitNode, exitNode.AgentCaps)
	if err != nil || len(dials) != 0 {
		t.Fatalf("unpinned self-signed site was used for a bridge dial: %+v, err %v", dials, err)
	}
}

func TestBridgeAllowsTwoExitsAndUsesFourConnectionsForBusyRelay(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	x.ensureTCPExitInbound()
	second := x.enroll("other-exit")
	now := time.Now().Unix()
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_vless_tcp2', 'vless', 'second tcp exit', '{"port":4444,"security":"tls"}', 1, ?, ?)`, now, now)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at)
		VALUES ('inb_second_tcp', 'prf_vless_tcp2', ?, 1, 'active', ?, ?)`, second.nodeID, now, now)
	exitConn, exitModel, _ := connectCaps(x.exit, "exit-one", plugin.CapRelayReverse, capVLESSTLS)
	secondConn, secondModel, _ := connectCaps(second, "exit-two", plugin.CapRelayReverse, capVLESSTLS)
	entryConn, entryModel, _ := connectCaps(x.entry, "entry", plugin.CapRelayForward, plugin.CapRelayReverse, capVLESSTLS)
	create := func(id, inbound, exitID string, port uint16) {
		t.Helper()
		if err := x.st.CreateRelay(x.ctx, store.Relay{ID: id, InboundID: inbound, ViaNodeID: x.entry.nodeID,
			Network: "tcp", Port: port, Mode: store.RelayModeAuto}, time.Now()); err != nil {
			t.Fatal(err)
		}
		on := true
		if err := x.st.UpdateRelay(x.ctx, id, store.RelayPatch{Enabled: &on}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := x.st.SetRelayActiveMode(x.ctx, id, store.RelayModeReverse, "leg_failed", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	create("rly_a", relayInboundTCP, x.exit.nodeID, 7443)
	create("rly_b", "inb_second_tcp", second.nodeID, 7444)
	counted := time.Now().Unix()
	if err := x.st.SetRelayCounters(x.ctx, x.entry.nodeID, []store.RelayCounterUpdate{{RelayID: "rly_a", Network: "tcp", Port: 7443,
		RelayCounters: store.RelayCounters{Bytes: 100_000, CountedUnix: counted - 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := x.st.SetRelayCounters(x.ctx, x.entry.nodeID, []store.RelayCounterUpdate{{RelayID: "rly_a", Network: "tcp", Port: 7443,
		RelayCounters: store.RelayCounters{Bytes: 1_600_000, CountedUnix: counted}}}); err != nil {
		t.Fatal(err)
	}
	x.entryReportsPin()
	// the entry's agent reports (the pool is chosen there: a forward that carried 1.5 MB in a second is busy)
	entryConn.bridges = []*agentv1.BridgeState{
		{PeerNodeId: x.exit.nodeID, Conns: 2, SinceUnix: time.Now().Unix() - 60, SiteCertPinSha256: bridgePin},
		{PeerNodeId: second.nodeID, Conns: 2, SinceUnix: time.Now().Unix() - 60, SiteCertPinSha256: bridgePin}}
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, entryConn.bridges, time.Now())
	x.f.StateChanged()
	entryState := entryConn.next(entryModel)
	wantExits := []string{x.exit.nodeID, second.nodeID}
	if wantExits[0] > wantExits[1] {
		wantExits[0], wantExits[1] = wantExits[1], wantExits[0]
	}
	if entryState.BridgeServe == nil || len(entryState.BridgeServe.ExitNodeIds) != len(wantExits) ||
		entryState.BridgeServe.ExitNodeIds[0] != wantExits[0] || entryState.BridgeServe.ExitNodeIds[1] != wantExits[1] {
		t.Fatalf("entry allowed exits = %v", entryState.BridgeServe)
	}
	assertAgentBridgeValid(t, entryState, x.pool)
	firstState := exitConn.next(exitModel)
	secondState := secondConn.next(secondModel)
	if firstState.BridgeDials == nil || len(firstState.BridgeDials.Items) != 1 || firstState.BridgeDials.Items[0].Pool != 4 ||
		len(firstState.BridgeDials.Items[0].Targets) != 1 || firstState.BridgeDials.Items[0].Targets[0].RelayId != "rly_a" {
		t.Fatalf("busy exit's bridge dials = %v", firstState.BridgeDials)
	}
	if secondState.BridgeDials == nil || len(secondState.BridgeDials.Items) != 1 || secondState.BridgeDials.Items[0].Pool != 2 ||
		len(secondState.BridgeDials.Items[0].Targets) != 1 || secondState.BridgeDials.Items[0].Targets[0].RelayId != "rly_b" {
		t.Fatalf("other exit's bridge dials = %v", secondState.BridgeDials)
	}
	assertAgentBridgeValid(t, firstState, x.pool)
	assertAgentBridgeValid(t, secondState, x.pool)
}

func TestBridgeReportsMergeAndKeepThePinWithTheReportItCameIn(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	now := x.f.now()
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, []*agentv1.BridgeState{{PeerNodeId: x.exit.nodeID, Conns: 1,
		SinceUnix: now.Unix() - 20, RttMs: 27, SiteCertPinSha256: bridgePin}}, now)
	x.f.recordBridgeReports(x.ctx, x.exit.nodeID, []*agentv1.BridgeState{{PeerNodeId: x.entry.nodeID, Conns: 1,
		SinceUnix: now.Unix() - 20, RttMs: 31}}, now)
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, []*agentv1.BridgeState{{PeerNodeId: x.exit.nodeID, Conns: 1,
		SinceUnix: now.Unix() - 20, RttMs: 27, SiteCertPinSha256: bridgePin}}, now.Add(time.Second))
	link, err := x.st.RelayLink(x.ctx, "rly_a")
	if err != nil {
		t.Fatal(err)
	}
	bridge, ok := link.Bridge()
	stage := link.BridgeStage(now.Add(time.Second))
	if !ok || bridge.Entry == nil || bridge.Exit == nil || bridge.Entry.RTTMS != 27 || bridge.Exit.RTTMS != 31 ||
		!bridge.Connected || bridge.Conns != 1 || stage.Status != "ok" || link.BridgeSitePin(8443, now.Add(time.Second)) != bridgePin {
		t.Fatalf("reports were not merged: bridge=%+v stage=%+v pin=%q", bridge, stage, link.BridgeSitePin(8443, now.Add(time.Second)))
	}
	// The pin is not a secret and the process that builds the exit's state is not the one that heard the entry, so it is kept with
	// the report; the path of the bridge is a secret and goes nowhere near the check.
	if strings.Contains(link.CheckJSON, bridgePath) {
		t.Fatalf("the secret path of the bridge entered relay.check_json: %s", link.CheckJSON)
	}
	// A pin for another port is not this port's, and one the entry has not repeated for 15 minutes is not trusted.
	if link.BridgeSitePin(9443, now.Add(time.Second)) != "" || link.BridgeSitePin(8443, now.Add(time.Second+bridgePinTTLForTest)) != "" {
		t.Fatalf("a pin answered for another port or after it went stale: %+v", bridge.Entry)
	}
}

// "None any more" is an empty message, "nothing changed" is no message at all, on both sides of the bridge.
func TestBridgeEmptyIsNotAbsent(t *testing.T) {
	x := newRelayEnv(t)
	x.givePath(8443)
	x.reverseRelay("rly_a", relayInbound, 7443, store.RelayModeReverse)
	ec, em, eds := connectCaps(x.exit, "exit", plugin.CapRelayReverse, capVLESSTLS)
	exitHash := eds.StateHash // the exit with its TCP server and no bridge
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

	// The relay goes back to the plain way: the list loses its mode and the exit is told that its list is empty (present and empty).
	// The entry keeps the bridge site up (the relay may still need it: it is auto), so it is told nothing of it.
	x.setActive("rly_a", store.RelayModeDNAT)
	d = c.next(m)
	if d.BridgeServe != nil || d.BridgeDials != nil || d.Relays == nil || d.Relays.Items[0].Mode != "" || m.bridgeServe == nil || m.bridgeServe.IsZero() {
		t.Fatalf("the entry went on serving: serve %v relays %v model %+v", d.BridgeServe, d.Relays, m.bridgeServe)
	}
	// Only the owner's dnat stops it: present and empty, and the hash of the entry is the one without a bridge.
	dnat := store.RelayModeDNAT
	x.setRelay("rly_a", store.RelayPatch{Mode: &dnat})
	d = c.next(m)
	if d.BridgeServe == nil || !proto.Equal(d.BridgeServe, &agentv1.BridgeServe{}) || d.BridgeDials != nil {
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

func TestEmptyBridgeSnapshotsDoNotCreateBridgeStateForPlainRelay(t *testing.T) {
	x := newRelayEnv(t)
	x.reverseRelay("rly_a", relayInboundTCP, 7443, store.RelayModeDNAT)
	now := x.f.now()
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, nil, now)
	x.f.recordBridgeReports(x.ctx, x.exit.nodeID, nil, now)
	link, err := x.st.RelayLink(x.ctx, "rly_a")
	if err != nil {
		t.Fatal(err)
	}
	bridge, ok := link.Bridge()
	if ok || strings.Contains(link.CheckJSON, `"bridge"`) || strings.Contains(link.CheckJSON, `"bridge_stage"`) {
		t.Fatalf("an empty snapshot created bridge state on a DNAT relay: bridge=%+v check=%s", bridge, link.CheckJSON)
	}
}

// A relay that is not carried through the bridge whatever its way says: UDP servers, an entry that has no bridge path yet, a relay
// that is off and a server that is not usable.
func TestReverseWayWithoutWhatItNeedsIsPlain(t *testing.T) {
	x := newRelayEnv(t)
	connectCaps(x.exit, "exit", plugin.CapRelayReverse, capVLESSTLS)
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
	if d.BridgeServe != nil || len(d.Relays.Items) != 1 || d.Relays.Items[0].RelayId != "rly_u" { // the site stays up for a relay that is off but may be switched on
		t.Fatalf("a relay switched off: %v", d)
	}
	on := true
	x.setRelay("rly_a", store.RelayPatch{Enabled: &on})
	c.next(m)
	x.exec(`UPDATE inbound SET state = 'failed' WHERE id = ?`, relayInboundTCP)
	x.f.StateChanged()
	if d = c.next(m); d.BridgeServe == nil || d.BridgeServe.Path != "" || len(d.Relays.Items) != 1 {
		t.Fatalf("a failed server: %v", d)
	}
}

// The admin's node card shows the port of the bridge site and why the entry could not use port 443.
func TestNodeMessageCarriesTheBridgePort(t *testing.T) {
	x := newRelayEnv(t)
	port := func() map[string]*adminv1.Node {
		resp, err := nodeService{x.f}.ListNodes(x.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*adminv1.Node{}
		for _, n := range resp.Msg.Nodes {
			out[n.Id] = n
		}
		return out
	}
	if n := port()[x.entry.nodeID]; n.BridgePort != 0 || n.BridgePortReason != "" || n.BridgePortProfileId != "" || n.LinkViaNodeId != "" {
		t.Fatalf("a node without a bridge: %+v", n)
	}
	now := time.Now().Unix()
	x.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
		VALUES ('prf_entry_external', 'vless', 'external reality', '{"port":443,"security":"reality","reality":{"target":"site.example.net:443","server_names":["site.example.net"]}}', 1, ?, ?)`, now, now)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at)
		VALUES ('inb_entry_external', 'prf_entry_external', ?, 1, 'active', ?, ?)`, x.entry.nodeID, now, now)
	x.givePath(8443)
	if n := port()[x.entry.nodeID]; n.BridgePort != 8443 || n.BridgePortReason != "reality_external_target" ||
		n.BridgePortProfileId != "prf_entry_external" || n.LinkViaNodeId != "" {
		t.Fatalf("a node with a bridge on 8443: %+v", n)
	}
	if n := port()[x.exit.nodeID]; n.BridgePort != 0 {
		t.Fatalf("the other node: %+v", n)
	}
}
