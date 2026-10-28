package access

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Relays (relay.proto): the entry node fi1 forwards a port to a server of the exit node de1.

type relayFix struct {
	*env
	base   *adminv1.ProfileSummary // hysteria2 on udp 443
	server *adminv1.Inbound        // the profile on de1
}

// newRelayFix has de1 with a server, fi1 whose agent lists relay-forward/1, and nl1 with a second server.
func newRelayFix(t *testing.T) *relayFix {
	t.Helper()
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_fi1", "fi1", "fi1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_fi1'`)
	p := e.profile("files", "")
	return &relayFix{env: e, base: p, server: e.inbound(p.Id, "nod_de1")}
}

func (f *relayFix) create(inbound, via string, port uint32) (*adminv1.Relay, error) {
	r, err := f.s.CreateRelay(f.ctx, req(&adminv1.CreateRelayRequest{InboundId: inbound, ViaNodeId: via, Port: port}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Relay, nil
}

func (f *relayFix) update(m *adminv1.UpdateRelayRequest) (*adminv1.Relay, error) {
	r, err := f.s.UpdateRelay(f.ctx, req(m))
	if err != nil {
		return nil, err
	}
	return r.Msg.Relay, nil
}

func (f *relayFix) audited(action string) []store.AuditRow {
	f.t.Helper()
	return must(f.st.ListAuditQuery(f.ctx, store.AuditQuery{Actions: []string{action}, Limit: 50}))
}

func allowRelays(context.Context, store.RelayLink) error { return nil }

func TestCreateRelayDefaultsAndLists(t *testing.T) {
	f := newRelayFix(t)
	f.resetNotify()
	r := must(f.create(f.server.Id, "nod_fi1", 0))
	if r.Port != 443 || r.ExitPort != 443 || r.Network != "udp" || r.Enabled || r.State != adminv1.InboundState_INBOUND_STATE_PENDING ||
		r.ViaNodeName != "fi1" || r.ExitNodeName != "de1" || r.ProfileName != "files" || r.Protocol != "hysteria2" ||
		!r.ExitLive || !r.AgentSupported || !strings.HasPrefix(r.Id, "rly_") || r.InboundId != f.server.Id {
		t.Fatalf("a new relay: %+v", r)
	}
	if f.notify.n.Load() != 0 {
		t.Error("a relay that is off changes no desired state, but the nodes were told")
	}
	rows := f.audited("relay.create")
	if len(rows) != 1 || !strings.Contains(rows[0].Params, `"via":"fi1"`) || !strings.Contains(rows[0].Params, `"exit":"de1"`) ||
		!strings.Contains(rows[0].Params, `"port":443`) || strings.Contains(rows[0].Params, "192.0.2") {
		t.Fatalf("audit: %+v", rows)
	}
	for node, want := range map[string]int{"": 1, "nod_fi1": 1, "nod_de1": 1, "nod_nl1": 0} {
		got := must(f.s.ListRelays(f.ctx, req(&adminv1.ListRelaysRequest{NodeId: node}))).Msg.Relays
		if len(got) != want {
			t.Errorf("ListRelays(%q) = %d relays, want %d", node, len(got), want)
		}
	}
	// A second time through the same entry, the same node as entry, an unknown server or node, a port the agent refuses, and
	// an agent that cannot forward.
	wantMsg(t, errOf2(f.create(f.server.Id, "nod_fi1", 0)), "already_exists: relay_exists: exit=de1&node=fi1")
	wantCode(t, errOf2(f.create(f.server.Id, "nod_de1", 0)), connect.CodeInvalidArgument)
	wantCode(t, errOf2(f.create("inb_nope", "nod_fi1", 0)), connect.CodeNotFound)
	wantCode(t, errOf2(f.create(f.server.Id, "nod_nope", 0)), connect.CodeNotFound)
	wantCode(t, errOf2(f.create(f.server.Id, "", 0)), connect.CodeInvalidArgument)
	wantCode(t, errOf2(f.create(f.server.Id, "nod_nl1", 80)), connect.CodeInvalidArgument)
	wantCode(t, errOf2(f.create(f.server.Id, "nod_nl1", 70000)), connect.CodeInvalidArgument)
	wantMsg(t, errOf2(f.create(f.server.Id, "nod_nl1", 0)), "failed_precondition: agent_old: node=nl1")
	// A node that has not connected yet has no capabilities on record: not refused.
	f.node("nod_new", "new1", "new1.example.com", "pending")
	if _, err := f.create(f.server.Id, "nod_new", 0); err != nil {
		t.Errorf("a node that never connected: %v", err)
	}
	f.sql(`UPDATE node SET state = 'retired' WHERE id = 'nod_nl1'`)
	wantMsg(t, errOf2(f.create(f.server.Id, "nod_nl1", 0)), "failed_precondition: node_retired")
}

func errOf2(_ *adminv1.Relay, err error) error { return err }

// The port rules run both ways: a relay cannot take a port a profile or a relay of the entry holds (hop range included),
// and a profile cannot take the port a relay forwards.
func TestRelayPortsAndProfilesKeepApart(t *testing.T) {
	f := newRelayFix(t)
	// fi1 serves a Hysteria2 profile on udp 443 itself, and hops over 20000-30000.
	own := f.profile("own", `{"hop":{"from":20000,"to":30000}}`)
	f.inbound(own.Id, "nod_fi1")

	// The exit's port is taken on the entry: the default is another one.
	r := must(f.create(f.server.Id, "nod_fi1", 0))
	if r.Port == 443 || r.ExitPort != 443 || r.Port < 1024 || (r.Port >= 20000 && r.Port <= 30000) {
		t.Fatalf("default port %d on an entry whose 443 and hop range are taken", r.Port)
	}
	// An explicit port: the profile's, a port in its hop range, and a free one.
	wantCode(t, errOf2(f.create(f.server.Id, "nod_fi1", 443)), connect.CodeAlreadyExists)
	// (a second server of the exit, so that the relay itself is not a duplicate)
	p2 := f.profile("second", `{"port":5443}`)
	in2 := f.inbound(p2.Id, "nod_de1")
	err := errOf2(f.create(in2.Id, "nod_fi1", 443))
	wantCode(t, err, connect.CodeAlreadyExists)
	if !strings.Contains(err.Error(), "port_taken") || !strings.Contains(err.Error(), "profile=own") || !strings.Contains(err.Error(), "free=") {
		t.Errorf("the refusal does not say what holds the port: %v", err)
	}
	wantCode(t, errOf2(f.create(in2.Id, "nod_fi1", 25000)), connect.CodeAlreadyExists)
	// The port of the first relay is taken for the second.
	err = errOf2(f.create(in2.Id, "nod_fi1", uint32(r.Port)))
	wantCode(t, err, connect.CodeAlreadyExists)
	if !strings.Contains(err.Error(), "relay+to+de1") {
		t.Errorf("a relay holding a port is not named: %v", err)
	}
	r2 := must(f.create(in2.Id, "nod_fi1", 7443))
	if r2.Port != 7443 || r2.ExitPort != 5443 {
		t.Fatalf("explicit port: %+v", r2)
	}

	// A new profile on the entry cannot take a forwarded port, nor hop over it, switched on or off.
	for port, why := range map[string]string{`{"port":7443}`: "port_taken", `{"port":10000,"hop":{"from":7000,"to":7500}}`: "hop_taken"} {
		clash := f.profile("clash"+port[len(port)-6:], port)
		err := errOf3(f.s.CreateInbound(f.ctx, req(&adminv1.CreateInboundRequest{ProfileId: clash.Id, NodeId: "nod_fi1"})))
		wantCode(t, err, connect.CodeAlreadyExists)
		if !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v", port, err)
		}
	}
	// ... and its port override may not either.
	free := f.profile("free", `{"port":6443}`)
	freeIn := f.inbound(free.Id, "nod_fi1")
	taken := uint32(7443)
	wantCode(t, errOf3(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: freeIn.Id, PortOverride: &taken}))), connect.CodeAlreadyExists)

	// Moving a relay: onto a profile's port or a relay's is refused; onto its own port or a free one is not.
	for _, port := range []uint32{443, 25000, 7443} {
		wantCode(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &port})), connect.CodeAlreadyExists)
	}
	own1, free1 := uint32(r.Port), uint32(9443)
	if got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &own1})); got.Port != r.Port {
		t.Errorf("a relay moved onto its own port: %+v", got)
	}
	if got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &free1})); got.Port != 9443 {
		t.Errorf("a relay moved onto a free port: %+v", got)
	}
	bad := uint32(80)
	wantCode(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &bad})), connect.CodeInvalidArgument)
	// The exit node holds the port of its own server, but it is not a neighbour on the entry: a relay to fi1's server from
	// de1 may use 443 there.
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_de1'`)
	back := must(f.create(freeIn.Id, "nod_de1", 0))
	if back.Port != 6443 {
		t.Errorf("fi1's server through de1 (which already serves 443/udp): %+v", back)
	}
}

func errOf3[T any](_ *T, err error) error { return err }

// A relay is switched on only through the gate; the real gate admits nothing until the panel can check the path.
func TestRelaySwitchGate(t *testing.T) {
	f := newRelayFix(t)
	r := must(f.create(f.server.Id, "nod_fi1", 0))
	f.resetNotify()
	on, off := true, false
	err := errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	wantMsg(t, err, "failed_precondition: relay_unchecked")
	if got := must(f.s.ListRelays(f.ctx, req(&adminv1.ListRelaysRequest{}))).Msg.Relays[0]; got.Enabled {
		t.Fatal("the gate refused, but the relay is on")
	}
	if f.notify.n.Load() != 0 || len(f.audited("relay.update")) != 0 {
		t.Error("a refused switch was announced or audited")
	}

	f.s.SetRelayGateForTest(allowRelays)
	got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if !got.Enabled || got.State != adminv1.InboundState_INBOUND_STATE_PENDING {
		t.Fatalf("switched on: %+v", got)
	}
	if f.notify.n.Load() == 0 {
		t.Error("the nodes were not told that a forward appeared")
	}
	if rows := f.audited("relay.update"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"enabled":true`) {
		t.Fatalf("audit: %+v", rows)
	}
	// Switching on twice changes nothing; switching off passes whatever the gate says.
	f.resetNotify()
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if f.notify.n.Load() != 0 || len(f.audited("relay.update")) != 1 {
		t.Error("a switch to the same value was announced or audited")
	}
	f.s.SetRelayGateForTest(nil)
	if got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &off})); got.Enabled {
		t.Fatalf("switched off: %+v", got)
	}
	wantCode(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: "rly_nope", Enabled: &off})), connect.CodeNotFound)

	// An entry whose agent lost the capability (an older build) is not switched on.
	f.s.SetRelayGateForTest(allowRelays)
	f.sql(`UPDATE node SET agent_caps = '' WHERE id = 'nod_fi1'`)
	wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})), "failed_precondition: agent_old: node=fi1")
}

func TestDeleteRelayAndTheServerGoingAway(t *testing.T) {
	f := newRelayFix(t)
	r := must(f.create(f.server.Id, "nod_fi1", 0))
	f.resetNotify()
	must(f.s.DeleteRelay(f.ctx, req(&adminv1.DeleteRelayRequest{Id: r.Id})))
	if rows := f.audited("relay.delete"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"reason":"admin"`) || !strings.Contains(rows[0].Params, r.Id) {
		t.Fatalf("audit: %+v", rows)
	}
	if f.notify.n.Load() == 0 {
		t.Error("the nodes were not told that a forward went")
	}
	_, err := f.s.DeleteRelay(f.ctx, req(&adminv1.DeleteRelayRequest{Id: r.Id}))
	wantCode(t, err, connect.CodeNotFound)

	// Removing the server takes its relays with it, and says so in the audit log.
	r = must(f.create(f.server.Id, "nod_fi1", 0))
	must(f.s.DeleteInbound(f.ctx, req(&adminv1.DeleteInboundRequest{InboundId: f.server.Id})))
	if got := must(f.s.ListRelays(f.ctx, req(&adminv1.ListRelaysRequest{}))).Msg.Relays; len(got) != 0 {
		t.Fatalf("relays left with their server gone: %+v", got)
	}
	rows := f.audited("relay.delete")
	if len(rows) != 2 || !strings.Contains(rows[0].Params, `"reason":"inbound_removed"`) || !strings.Contains(rows[0].Params, r.Id) {
		t.Fatalf("audit: %+v", rows)
	}
}

// Two creates at once: one relay, and the other request is told why.
func TestConcurrentCreateRelay(t *testing.T) {
	f := newRelayFix(t)
	p2 := f.profile("second", `{"port":5443}`)
	in2 := f.inbound(p2.Id, "nod_de1")
	race := func(a, b [3]string, port [2]uint32, want string) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, c := range [][3]string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = f.create(c[0], c[1], port[i])
			}()
		}
		wg.Wait()
		ok, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case connect.CodeOf(err) == connect.CodeAlreadyExists && strings.Contains(err.Error(), want):
				refused++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}
		if ok != 1 || refused != 1 {
			t.Errorf("%d created, %d refused (%v); want one of each", ok, refused, errs)
		}
	}
	// The same server through the same entry, from two panels' worth of requests.
	race([3]string{f.server.Id, "nod_fi1"}, [3]string{f.server.Id, "nod_fi1"}, [2]uint32{8443, 9443}, "relay_exists")
	// Two servers asking for one port of one entry.
	p3 := f.profile("third", `{"port":6443}`)
	in3 := f.inbound(p3.Id, "nod_nl1")
	race([3]string{in2.Id, "nod_fi1"}, [3]string{in3.Id, "nod_fi1"}, [2]uint32{7443, 7443}, "port_taken")
	if got := must(f.s.ListRelays(f.ctx, req(&adminv1.ListRelaysRequest{NodeId: "nod_fi1"}))).Msg.Relays; len(got) != 2 {
		t.Fatalf("relays of fi1: %+v", got)
	}
}

// The rule that makes a relay usable for a person: the relay is on, the exit's server is usable for them, and the entry is
// one of their nodes. It is the user-free part (Live) that decides what the entry forwards.
func TestRelayUsableRule(t *testing.T) {
	e := newEnv(t)
	link := store.RelayLink{Relay: store.Relay{Enabled: true, ViaNodeID: "nod_fi1"},
		ExitEnabled: true, ExitState: "active", ExitNodeState: "active", ViaNodeState: "active"}
	exit := store.AccessInboundFull{
		Inbound: store.AccessInbound{Enabled: true, State: "active"},
		Profile: store.AccessProfile{ID: "prf_1", Protocol: "hysteria2"},
		Node:    store.AccessNode{ID: "nod_de1", State: "active"},
	}
	group := store.AccessGroup{ProfileIDs: []string{"prf_1"}}
	user := store.AccessUser{NodeIDs: []string{"nod_de1", "nod_fi1"}, AppHapp: true}
	if !e.s.relayUsable(link, exit, group, user) {
		t.Fatal("a relay everything allows is not usable")
	}
	for name, mutate := range map[string]func(*store.RelayLink, *store.AccessInboundFull, *store.AccessGroup, *store.AccessUser){
		"relay off": func(l *store.RelayLink, _ *store.AccessInboundFull, _ *store.AccessGroup, _ *store.AccessUser) {
			l.Enabled = false
		},
		"server off": func(l *store.RelayLink, f *store.AccessInboundFull, _ *store.AccessGroup, _ *store.AccessUser) {
			l.ExitEnabled, f.Inbound.Enabled = false, false
		},
		"server failed": func(l *store.RelayLink, f *store.AccessInboundFull, _ *store.AccessGroup, _ *store.AccessUser) {
			l.ExitState, f.Inbound.State = "failed", "failed"
		},
		"exit node not active": func(l *store.RelayLink, f *store.AccessInboundFull, _ *store.AccessGroup, _ *store.AccessUser) {
			l.ExitNodeState, f.Node.State = "pending", "pending"
		},
		"entry not active": func(l *store.RelayLink, _ *store.AccessInboundFull, _ *store.AccessGroup, _ *store.AccessUser) {
			l.ViaNodeState = "pending"
		},
		"entry not the person's": func(_ *store.RelayLink, _ *store.AccessInboundFull, _ *store.AccessGroup, u *store.AccessUser) {
			u.NodeIDs = []string{"nod_de1"}
		},
		"exit not the person's": func(_ *store.RelayLink, _ *store.AccessInboundFull, _ *store.AccessGroup, u *store.AccessUser) {
			u.NodeIDs = []string{"nod_fi1"}
		},
		"profile not in the group": func(_ *store.RelayLink, _ *store.AccessInboundFull, g *store.AccessGroup, _ *store.AccessUser) {
			g.ProfileIDs = nil
		},
		"app off": func(_ *store.RelayLink, _ *store.AccessInboundFull, _ *store.AccessGroup, u *store.AccessUser) {
			u.AppHapp = false
		},
	} {
		l, f, g, u := link, exit, group, user
		u.NodeIDs = append([]string(nil), user.NodeIDs...)
		mutate(&l, &f, &g, &u)
		if e.s.relayUsable(l, f, g, u) {
			t.Errorf("%s: the relay is usable", name)
		}
	}
	user.AllNodes, user.NodeIDs = true, nil
	if !e.s.relayUsable(link, exit, group, user) {
		t.Error("a person with every node is refused the entry")
	}
}
