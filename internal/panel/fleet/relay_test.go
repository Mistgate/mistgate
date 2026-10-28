package fleet

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The relays of the desired state (agent.proto "RELAY FORWARDS"): the entry node is sent the forwards of its switched-on
// relays when its agent lists "relay-forward/1" and the exit's server is usable; the hash of a node without forwards is what it
// was before they existed; what the entry reports is kept; the exit and the entry going away takes the forward with it.

// relayEnv is two enrolled nodes, "entry" and "exit", and two servers on the exit that listen on udp 4443 and 5443.
type relayEnv struct {
	*env
	entry, exit *agent
}

const (
	relayInbound  = "inb_exit"
	relayInbound2 = "inb_exit2"
)

func newRelayEnv(t *testing.T) *relayEnv {
	t.Helper()
	e := newEnv(t)
	x := &relayEnv{env: e, entry: e.enroll("entry"), exit: e.enroll("exit")}
	now := time.Now().Unix()
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_hy', 'fakehy', 'hy', '{"port":4443}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at) VALUES (?, 'prf_hy', ?, 1, 'active', ?, ?)`, relayInbound, x.exit.nodeID, now, now)
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_hy2', 'fakehy', 'hy2', '{"port":5443}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at) VALUES (?, 'prf_hy2', ?, 1, 'active', ?, ?)`, relayInbound2, x.exit.nodeID, now, now)
	e.exec(`UPDATE node SET state = 'active' WHERE id IN (?, ?)`, x.entry.nodeID, x.exit.nodeID)
	e.run()
	return x
}

// relay stores a relay of the entry to the exit's server, switched on or off.
func (x *relayEnv) relay(id string, port uint16, on bool) { x.relayOf(id, relayInbound, port, on) }

func (x *relayEnv) relayOf(id, inbound string, port uint16, on bool) {
	x.t.Helper()
	if err := x.st.CreateRelay(x.ctx, store.Relay{ID: id, InboundID: inbound, ViaNodeID: x.entry.nodeID, Network: "udp", Port: port}, time.Now()); err != nil {
		x.t.Fatal(err)
	}
	if on {
		x.setRelay(id, store.RelayPatch{Enabled: &on})
	}
}

func (x *relayEnv) setRelay(id string, p store.RelayPatch) {
	x.t.Helper()
	if err := x.st.UpdateRelay(x.ctx, id, p, time.Now()); err != nil {
		x.t.Fatal(err)
	}
	x.f.StateChanged()
}

func forward(id string, port uint16) plugin.RelayForward {
	return plugin.RelayForward{ID: id, Network: "udp", Port: port, ToHost: "exit.example.com", ToPort: 4443}
}

// next waits for the next desired state of a connection, applies it to the model and acknowledges it.
func (c *conn) next(m *model) *agentv1.DesiredState {
	c.t.Helper()
	ds := c.desired()
	base := m.rev
	if !m.apply(ds) {
		c.t.Fatalf("delta on revision %d does not apply to %d", ds.BaseRevision, base)
	}
	if m.hash() != ds.StateHash {
		c.t.Fatalf("the agent computes %s, the panel announced %s", m.hash(), ds.StateHash)
	}
	c.send(0, applied(ds, m.hash()))
	return ds
}

// A node without the capability never gets forwards, and its hash is the hash of a state that has none; one with it does.
func TestRelaysReachOnlyAgentsThatListTheCapability(t *testing.T) {
	x := newRelayEnv(t)
	x.relay("rly_a", 8443, true)
	noForwards := statehash.State(nil)

	_, m, ds := connectFull(x.entry, "old")
	if ds.Relays != nil || len(m.relays) != 0 {
		t.Fatalf("an agent without relay-forward/1 was sent forwards: %v", ds.Relays)
	}
	if ds.StateHash != noForwards {
		t.Fatalf("a node without forwards hashes %s, want %s (the hash it had before forwards existed)", ds.StateHash, noForwards)
	}

	_, m, ds = connectCaps(x.entry, "new", "doctor/1", plugin.CapRelayForward)
	want := &agentv1.RelayForwards{Items: []*agentv1.RelayForward{{RelayId: "rly_a", Network: "udp", Port: 8443, ToHost: "exit.example.com", ToPort: 4443}}}
	if !proto.Equal(ds.Relays, want) {
		t.Fatalf("forwards on the wire = %v, want %v", ds.Relays, want)
	}
	if len(ds.Inbounds) != 0 {
		t.Errorf("the entry was sent inbounds: %v", ds.Inbounds)
	}
	if got := statehash.StateRelays(nil, nil, []plugin.RelayForward{forward("rly_a", 8443)}); ds.StateHash != got || got == noForwards {
		t.Errorf("hash with a forward = %s, want %s (and not %s)", ds.StateHash, got, noForwards)
	}
	if len(m.relays) != 1 || m.relays[0] != forward("rly_a", 8443) {
		t.Errorf("the agent holds %v", m.relays)
	}
}

// Only a switched-on relay whose server is usable is sent; each change is a delta that carries the whole list, the empty
// wrapper clears it, and an unchanged list is not sent again.
func TestRelayForwardsFollowTheSwitchAndTheServer(t *testing.T) {
	x := newRelayEnv(t)
	x.relay("rly_a", 8443, false)
	c, m, ds := connectCaps(x.entry, "new", plugin.CapRelayForward)
	if ds.Relays != nil || ds.StateHash != statehash.State(nil) {
		t.Fatalf("a relay that is off was sent: %v", ds.Relays)
	}

	on := true
	x.setRelay("rly_a", store.RelayPatch{Enabled: &on})
	d := c.next(m)
	if d.BaseRevision == 0 || len(d.Inbounds) != 0 || d.Relays == nil || len(d.Relays.Items) != 1 || len(m.relays) != 1 {
		t.Fatalf("switching on: %v", d)
	}
	x.f.StateChanged()
	c.quiet(300 * time.Millisecond) // nothing changed: nothing sent

	port := uint16(9443)
	x.setRelay("rly_a", store.RelayPatch{Port: &port}) // a move puts it back to pending, and the forward moves
	if d = c.next(m); len(d.Relays.Items) != 1 || d.Relays.Items[0].Port != 9443 || m.relays[0] != forward("rly_a", 9443) {
		t.Fatalf("moving the port: %v", d)
	}
	if err := x.st.RelaysApplied(x.ctx, x.entry.nodeID, []store.RelayApplied{{ID: "rly_a", OK: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	x.f.StateChanged()
	c.quiet(300 * time.Millisecond) // a report changes nothing the agent needs

	// The server fails: the forward goes (the empty wrapper), and comes back when the server does.
	x.exec(`UPDATE inbound SET state = 'failed' WHERE id = ?`, relayInbound)
	x.f.StateChanged()
	if d = c.next(m); d.Relays == nil || len(d.Relays.Items) != 0 || len(m.relays) != 0 || m.hash() != statehash.State(nil) {
		t.Fatalf("a failed server is still forwarded: %v", d)
	}
	x.exec(`UPDATE inbound SET state = 'active' WHERE id = ?`, relayInbound)
	x.f.StateChanged()
	if d = c.next(m); len(d.Relays.Items) != 1 || len(m.relays) != 1 {
		t.Fatalf("a server that recovered is not forwarded: %v", d)
	}
	x.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, relayInbound)
	x.f.StateChanged()
	if d = c.next(m); d.Relays == nil || len(d.Relays.Items) != 0 {
		t.Fatalf("a switched-off server is still forwarded: %v", d)
	}
	x.exec(`UPDATE inbound SET enabled = 1 WHERE id = ?`, relayInbound)
	x.f.StateChanged()
	c.next(m)

	off := false
	x.setRelay("rly_a", store.RelayPatch{Enabled: &off})
	if d = c.next(m); d.Relays == nil || len(d.Relays.Items) != 0 || len(m.relays) != 0 {
		t.Fatalf("switching off: %v", d)
	}
}

// The agent that restarts holding the state, forwards included, is left alone; one that lost them is sent them in full.
func TestRelayForwardsSurviveAReconnect(t *testing.T) {
	x := newRelayEnv(t)
	x.relay("rly_a", 8443, true)
	x.relayOf("rly_b", relayInbound2, 7443, false)
	_, m, _ := connectCaps(x.entry, "first", plugin.CapRelayForward)
	if len(m.relays) != 1 {
		t.Fatalf("held %v", m.relays)
	}
	c := x.entry.open()
	h := hello("second", m.rev, m.hash())
	h.GetHello().Capabilities = []string{plugin.CapRelayForward}
	c.send(0, h)
	c.wait(func(r *agentv1.ConnectResponse) bool { return r.GetHelloAck() != nil })
	c.quiet(300 * time.Millisecond)

	on := true
	x.setRelay("rly_b", store.RelayPatch{Enabled: &on})
	d := c.next(m)
	if len(d.Relays.Items) != 2 || d.Relays.Items[0].RelayId != "rly_a" || d.Relays.Items[1].RelayId != "rly_b" ||
		d.Relays.Items[1].ToPort != 5443 || len(d.Inbounds) != 0 {
		t.Fatalf("the delta after a reconnect is the whole list sorted by id: %v", d)
	}
}

// What the entry reports for its forwards is kept per relay; a refusal keeps its words.
func TestRelayResultsAreStored(t *testing.T) {
	x := newRelayEnv(t)
	x.relay("rly_a", 8443, true)
	c, m, ds := connectCaps(x.entry, "new", plugin.CapRelayForward)
	result := func(rs ...*agentv1.RelayResult) {
		r := applied(ds, m.hash())
		r.GetApplyResult().Relays = rs
		c.send(0, r)
	}
	state := func() store.Relay {
		l, err := x.st.RelayLink(x.ctx, "rly_a")
		if err != nil {
			t.Fatal(err)
		}
		return l.Relay
	}
	result(&agentv1.RelayResult{RelayId: "rly_a", Ok: true, ToIp: "192.0.2.10"})
	within(t, "the forward is active", func() bool { return state().State == "active" })
	if r := state(); r.ToIP != "192.0.2.10" || r.LastError != "" || r.AppliedAt.IsZero() {
		t.Errorf("an installed forward: %+v", r)
	}
	result(&agentv1.RelayResult{RelayId: "rly_a", Ok: false, Error: "forward_drop"}, &agentv1.RelayResult{RelayId: "rly_unknown", Ok: true})
	within(t, "the forward is failed", func() bool { return state().State == "failed" })
	if r := state(); r.LastError != "forward_drop" {
		t.Errorf("a refused forward: %+v", r)
	}
}

// A server that fails on its own node takes the forward off the entry: the exit's report reaches the entry's desired state.
func TestTheExitsReportMovesTheEntry(t *testing.T) {
	x := newRelayEnv(t)
	x.relay("rly_a", 8443, true)
	c, m, _ := connectCaps(x.entry, "new", plugin.CapRelayForward)
	if len(m.relays) != 1 {
		t.Fatalf("held %v", m.relays)
	}
	ec, em, eds := connectFull(x.exit, "exit")
	_ = ec
	r := applied(eds, em.hash())
	r.GetApplyResult().Inbounds = []*agentv1.InboundResult{{InboundId: relayInbound, State: agentv1.InboundRunState_INBOUND_RUN_STATE_FAILED, Error: "bind: address in use"}}
	ec.send(0, r)
	d := c.next(m)
	if d.Relays == nil || len(d.Relays.Items) != 0 || len(m.relays) != 0 {
		t.Fatalf("the entry still forwards to a server that failed: %v", d)
	}
}

// A node leaving the fleet takes its relays, as the entry and as the exit, with an audit row each, and the other end follows.
func TestRetiringANodeDeletesItsRelays(t *testing.T) {
	for _, retire := range []string{"exit", "entry"} {
		t.Run(retire, func(t *testing.T) {
			x := newRelayEnv(t)
			x.relay("rly_a", 8443, true)
			c, m, _ := connectCaps(x.entry, "new", plugin.CapRelayForward)
			node := x.exit
			if retire == "entry" {
				node = x.entry
			}
			if _, err := (nodeService{x.f}).RetireNode(x.ctx, connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: node.nodeID, ConfirmName: retire})); err != nil {
				t.Fatal(err)
			}
			if rs, err := x.st.RelayLinks(x.ctx, store.RelayFilter{}); err != nil || len(rs) != 0 {
				t.Fatalf("relays left: %+v, %v", rs, err)
			}
			rows, err := x.st.ListAuditQuery(x.ctx, store.AuditQuery{Actions: []string{"relay.delete"}, Limit: 10})
			if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Params, `"reason":"node_retired"`) || !strings.Contains(rows[0].Params, `"relay":"rly_a"`) ||
				!strings.Contains(rows[0].Params, `"via":"entry"`) || !strings.Contains(rows[0].Params, `"exit":"exit"`) {
				t.Fatalf("audit rows: %+v, %v", rows, err)
			}
			if retire == "exit" {
				if d := c.next(m); d.Relays == nil || len(d.Relays.Items) != 0 {
					t.Fatalf("the entry keeps forwarding to a retired exit: %v", d)
				}
			}
		})
	}
}
