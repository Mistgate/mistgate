package fleet

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// linksBatch is a stats batch that carries connection counters (agent.proto LinkCounters) and nothing else.
func linksBatch(links ...*agentv1.LinkCounters) *agentv1.ConnectRequest {
	now := time.Now().Unix()
	m := statsBatch(now-10, now, nil, nil)
	m.GetStats().Links = links
	return m
}

// connectLinks connects an agent that lists links/1 and confirms the full state.
func connectLinks(a *agent, instance string) *conn {
	a.e.t.Helper()
	c := a.open()
	h := hello(instance, 0, "")
	h.GetHello().Capabilities = []string{capLinks}
	c.send(0, h)
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	m := newModel()
	m.apply(ds)
	c.send(0, applied(ds, m.hash()))
	return c
}

// The counters of a batch land in the hour bucket under the traffic's filter, and GetNodeConnections reads them back; a node
// whose agent does not list links/1 says so instead of showing zeros.
func TestStatsLinksAreStoredAndRead(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c := connectLinks(a, "inst1")
	node := nodeService{e.f}

	c.send(1, linksBatch(
		&agentv1.LinkCounters{CredId: "crd_alice_hy", InboundId: ids.i1, Connects: 2, Drops: 1, OnlineS: 600},
		&agentv1.LinkCounters{CredId: "crd_erin_hy", InboundId: ids.i1, Connects: 1, Stalls: 3},
		&agentv1.LinkCounters{InboundId: ids.i1, UnknownKeys: 4, Hellos: 20, Handshakes: 9},
		&agentv1.LinkCounters{CredId: "crd_alice_wg", InboundId: ids.i1, Connects: 9}, // a wg key on a hysteria inbound
		&agentv1.LinkCounters{CredId: "crd_alice_hy", InboundId: ids.i3, Connects: 9}, // an inbound of another node
		&agentv1.LinkCounters{CredId: "crd_nobody", InboundId: ids.i1, Connects: 9},   // unknown key
		&agentv1.LinkCounters{InboundId: ids.i3, Hellos: 9},                           // the inbound row of another node
		&agentv1.LinkCounters{CredId: "crd_alice_wg", InboundId: ids.i2, Rejects: 5}))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 1 })
	c.send(2, linksBatch(&agentv1.LinkCounters{CredId: "crd_alice_hy", InboundId: ids.i1, Connects: 1, OnlineS: 10}))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 2 })
	if n := e.count(`SELECT count(*) FROM link_hour`); n != 4 {
		t.Fatalf("link_hour rows = %d, want 4 (two keys and the inbound row of i1, one key of i2)", n)
	}
	if n := e.count(`SELECT count(*) FROM link_hour WHERE inbound_id = ? OR node_id <> ?`, ids.i3, a.nodeID); n != 0 {
		t.Errorf("%d rows of the other node's inbound or the wrong node", n)
	}

	resp, err := node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: a.nodeID}))
	if err != nil || !resp.Msg.Supported || len(resp.Msg.Inbounds) != 2 {
		t.Fatalf("GetNodeConnections = %+v, %v", resp, err)
	}
	if got := resp.Msg.NowUnix - resp.Msg.SinceUnix; got != 24*3600 {
		t.Errorf("default window = %d s, want 24 h", got)
	}
	hy, wg := resp.Msg.Inbounds[0], resp.Msg.Inbounds[1]
	if hy.InboundId != ids.i1 || hy.ProfileId != "prf_1" || hy.ProfileName != "p1" || hy.Protocol != "fakehy" ||
		hy.Connects != 4 || hy.Drops != 1 || hy.Stalls != 3 || hy.OnlineS != 610 || hy.UnknownKeys != 4 || hy.Hellos != 20 || hy.Handshakes != 9 ||
		hy.KeysStalled != 1 || hy.KeysTried != 2 {
		t.Errorf("hysteria inbound = %+v", hy)
	}
	if wg.InboundId != ids.i2 || wg.Protocol != "fakewg" || wg.Rejects != 5 || wg.Connects != 0 || wg.KeysTried != 0 {
		t.Errorf("wg inbound = %+v", wg)
	}
	for hours, want := range map[uint32]int64{1: 3600, 0: 24 * 3600, 48: 48 * 3600, 5000: 48 * 3600} {
		r, err := node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: a.nodeID, Hours: hours}))
		if err != nil || r.Msg.NowUnix-r.Msg.SinceUnix != want {
			t.Errorf("hours %d: window %v, %v, want %d s", hours, r, err, want)
		}
	}

	// node B never said links/1: not supported, and no inbounds
	resp, err = node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: ids.nodeB}))
	if err != nil || resp.Msg.Supported || len(resp.Msg.Inbounds) != 0 || resp.Msg.NowUnix == 0 {
		t.Errorf("a node without links/1 = %+v, %v", resp, err)
	}
	if _, err = node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: "nod_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an unknown node: %v, want NotFound", err)
	}
}

// guardLinks keeps at most maxStatsDeltas rows; the traffic limits do not touch the counters.
func TestGuardStatsLimitsLinkRows(t *testing.T) {
	f := &Fleet{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	links := make([]*agentv1.LinkCounters, maxStatsDeltas+3)
	for i := range links {
		links[i] = &agentv1.LinkCounters{CredId: fmt.Sprintf("crd_%d", i), InboundId: "inb_cap", Connects: 1}
	}
	g := f.guardStats(&agentv1.StatsBatch{IntervalStartUnix: 1_000, IntervalEndUnix: 1_010, Links: links})
	if len(g.links) != maxStatsDeltas || g.rejected != 0 || g.links[maxStatsDeltas-1].CredID != fmt.Sprintf("crd_%d", maxStatsDeltas-1) {
		t.Fatalf("%d rows in, %d kept, rejected %d; want the first %d kept", len(links), len(g.links), g.rejected, maxStatsDeltas)
	}
	// a batch whose traffic is refused still gets its counters in
	deltas := []*agentv1.TrafficDelta{{CredId: "crd_a", InboundId: "inb_cap", BytesUp: 1 << 60}}
	g = f.guardStats(&agentv1.StatsBatch{IntervalStartUnix: 1_000, IntervalEndUnix: 1_010, Traffic: deltas, Links: links[:2]})
	if g.rejected != 1 || len(g.links) != 2 {
		t.Fatalf("a refused delta: rejected %d, links %d, want 1 and 2", g.rejected, len(g.links))
	}
}
