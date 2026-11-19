package fleet

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// Services that share a TCP port by SNI go only to an agent that can (tcp-share/1); an older one holds one service of a port and
// fails the rest on bind, so it is sent the default route alone and the others are withheld, as an awg inbound is from an agent
// without awg/1.
func TestSharedPortIsWithheldFromAnAgentThatCannotShare(t *testing.T) {
	if capTCPShare != "tcp-share/1" { // the agent's own constant (internal/node/agent l3.go) is pinned to the same literal
		t.Fatalf("capability is %q", capTCPShare)
	}
	tcp := func(id, protocol string, port uint16, name, settings string) statehash.Inbound {
		return statehash.Inbound{Spec: plugin.InboundSpec{ID: id, Protocol: protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: port},
			TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: name}, Settings: []byte(settings)}}
	}
	mt := func(id string, port uint16, name string) statehash.Inbound {
		in := tcp(id, "mtproto", port, name, `{"domains":["`+name+`"],"fallback":"decoy"}`)
		in.Spec.TLS = plugin.TLS{ServerName: name}
		return in
	}
	e := newEnv(t)
	node := store.NodeRow{ID: "nod_1", State: "active"}
	allCaps := []string{capVLESS, capVLESSSelf, capMTProto, capTrustTunnel}
	desired := func(in ...statehash.Inbound) {
		e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) { return in, nil }
	}
	build := func(caps ...string) *nodeState {
		t.Helper()
		st, err := e.f.buildState(context.Background(), node, caps)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	held := func(st *nodeState) []string { return st.ids() }

	// three services on 443 (the lowest id that serves the node's own site is the default), one on 8443 of its own
	desired(mt("inb_a", 443, "a.example.com"), tcp("inb_b", "trusttunnel", 443, "b.example.com", `{}`), tcp("inb_c", "trusttunnel", 443, "c.example.com", `{}`),
		tcp("inb_d", "trusttunnel", 8443, "d.example.com", `{}`))

	old := build(allCaps...)
	if got := held(old); !slices.Equal(got, []string{"inb_a", "inb_d"}) || !slices.Equal(old.withheld, []string{"inb_b", "inb_c"}) {
		t.Fatalf("an agent that cannot share holds %v and is withheld %v, want a and d held, b and c withheld", got, old.withheld)
	}
	capable := build(append(allCaps, capTCPShare)...)
	if got := held(capable); !slices.Equal(got, []string{"inb_a", "inb_b", "inb_c", "inb_d"}) || len(capable.withheld) != 0 {
		t.Fatalf("an agent that can share holds %v and is withheld %v, want all held", got, capable.withheld)
	}
	if old.hash == capable.hash {
		t.Fatal("the state of an agent that holds one service and of one that holds three has one hash")
	}

	// a service that lost its name is not run by an agent that shares, and is not sent to one that cannot
	desired(mt("inb_a", 443, "a.example.com"), tcp("inb_b", "trusttunnel", 443, "A.example.com", `{}`), tcp("inb_d", "trusttunnel", 443, "d.example.com", `{}`))
	if got := held(build(allCaps...)); !slices.Equal(got, []string{"inb_a"}) {
		t.Fatalf("an agent that cannot share holds %v, want a alone", got)
	}

	// no service shows the node's own site: the old rule holds for every agent, nothing is withheld for it
	desired(tcp("inb_a", "vless", 443, "", `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`),
		tcp("inb_b", "mtproto", 443, "", `{"domains":["b.example.com"],"fallback":"site"}`))
	if st := build(allCaps...); len(st.withheld) != 0 || len(st.in) != 2 {
		t.Fatalf("a port with no default route: held %v withheld %v", held(st), st.withheld)
	}

	// one service on a port, and a Hysteria2 site, are no share
	desired(tcp("inb_a", "trusttunnel", 443, "a.example.com", `{}`), statehash.Inbound{Spec: plugin.InboundSpec{ID: "inb_h", Protocol: "hysteria2", Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 443}, TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "h.example.com"}, Settings: []byte(`{}`)}})
	if st := build(allCaps...); len(st.withheld) != 0 || len(st.in) != 2 {
		t.Fatalf("one service and a Hysteria2 site: held %v withheld %v", held(st), st.withheld)
	}
}

// The panel keeps the last snapshot of the shared ports of a node: a change of what is shared at once, a change of the counters alone
// every five minutes, and a snapshot with none removes it.
func TestSharedPortsSnapshotIsStoredWhenItChanges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nodeID, _, _ := e.createEnrollment("de1", "de1.example.com")
	row := func(accepted, tt uint64, unknown ...string) []*agentv1.SharedPort {
		p := &agentv1.SharedPort{Port: 443, DefaultInboundId: "inb_a", Accepted: accepted,
			Routes: []*agentv1.SharedRoute{{InboundId: "inb_b", Delivered: tt}, {InboundId: "inb_a", Delivered: accepted - tt}}}
		for _, n := range unknown {
			p.UnknownNames = append(p.UnknownNames, &agentv1.SharedUnknownName{Name: n, Count: 2})
		}
		return []*agentv1.SharedPort{p, {Port: 0}, nil} // a row with no port is not a port
	}
	now := time.Unix(1_800_000_000, 0)
	read := func() ([]*adminv1.SharedPort, int64) {
		t.Helper()
		ports, at, err := e.f.storedSharedPorts(ctx, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		return ports, at
	}

	e.f.recordSharedPorts(ctx, nodeID, nil, now) // a node that shares nothing costs nothing and stores nothing
	if ports, _ := read(); len(ports) != 0 {
		t.Fatalf("stored %v for a node that shares nothing", ports)
	}
	e.f.recordSharedPorts(ctx, nodeID, row(10, 4, "evil.example.com", "bad name!", "UPPER"), now)
	ports, at := read()
	if len(ports) != 1 || at != now.Unix() || ports[0].Accepted != 10 || ports[0].DefaultInboundId != "inb_a" || len(ports[0].Routes) != 2 {
		t.Fatalf("stored %v at %d", ports, at)
	}
	if got := ports[0].UnknownNames; len(got) != 1 || got[0].Name != "evil.example.com" {
		t.Fatalf("unknown names = %v, want the host name only (a stranger chooses them)", got)
	}
	// only the counters change: not written before five minutes
	e.f.recordSharedPorts(ctx, nodeID, row(50, 20), now.Add(time.Minute))
	if ports, _ := read(); ports[0].Accepted != 10 {
		t.Fatalf("counters written after a minute: %d", ports[0].Accepted)
	}
	e.f.recordSharedPorts(ctx, nodeID, row(90, 40), now.Add(6*time.Minute))
	if ports, at := read(); ports[0].Accepted != 90 || at != now.Add(6*time.Minute).Unix() {
		t.Fatalf("counters not written after six minutes: %v at %d", ports, at)
	}
	// a bind error is news at once
	failed := row(0, 0)
	failed[0].BindError = "listen tcp :443: bind: address already in use"
	e.f.recordSharedPorts(ctx, nodeID, failed, now.Add(7*time.Minute))
	if ports, _ := read(); ports[0].BindError == "" {
		t.Fatal("a bind error was not written at once")
	}
	// the port is not shared any more: the snapshot goes
	e.f.recordSharedPorts(ctx, nodeID, nil, now.Add(8*time.Minute))
	if ports, at := read(); len(ports) != 0 || at != 0 {
		t.Fatalf("the snapshot stayed: %v at %d", ports, at)
	}
}

// The admin sees the snapshot in the node's detail.
func TestNodeDetailCarriesTheSharedPorts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nodeID, _, _ := e.createEnrollment("de1", "de1.example.com")
	now := time.Now()
	e.f.recordSharedPorts(ctx, nodeID, []*agentv1.SharedPort{{Port: 443, DefaultInboundId: "inb_a", Accepted: 3}}, now)
	got, err := nodeService{e.f}.GetNode(ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.SharedPorts) != 1 || got.Msg.SharedPorts[0].Port != 443 || got.Msg.SharedPortsUnix != now.Unix() {
		t.Fatalf("shared ports of the node detail = %v at %d", got.Msg.SharedPorts, got.Msg.SharedPortsUnix)
	}
}
