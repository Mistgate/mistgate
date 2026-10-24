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
	"github.com/mistgate/mistgate/internal/panel/connectiondiag"
	"google.golang.org/protobuf/encoding/protojson"
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
	return connectLinksWithCaps(a, instance, capLinks)
}

func connectLinksWithCaps(a *agent, instance string, capabilities ...string) *conn {
	a.e.t.Helper()
	c := a.open()
	h := hello(instance, 0, "")
	h.GetHello().Capabilities = capabilities
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
	// Use the production protocol names in this test so it also covers per-protocol unsupported counters.
	e.exec(`UPDATE profile SET protocol = CASE id WHEN 'prf_1' THEN 'hysteria2' WHEN 'prf_2' THEN 'awg' END WHERE id IN ('prf_1', 'prf_2')`)
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_vless', 'vless', 'VLESS', '{}', 1, unixepoch(), unixepoch())`)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES ('inb_vless', 'prf_vless', ?, 1, unixepoch(), unixepoch())`, a.nodeID)
	e.exec(`UPDATE device_credential SET protocol = CASE protocol WHEN 'fakehy' THEN 'hysteria2' WHEN 'fakewg' THEN 'awg' END WHERE protocol IN ('fakehy', 'fakewg')`)
	e.exec(`UPDATE device SET platform = 'android', client = 'Happ/3.4.2' WHERE id IN ('dev_alice', 'dev_erin')`)
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
	health, err := protojson.Marshal(&agentv1.AwgHealth{Backend: "userspace", UdpRxPackets: 12345, PeersOnline: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetInboundAwgHealth(e.ctx, a.nodeID, ids.i2, string(health), e.f.now()); err != nil {
		t.Fatal(err)
	}

	resp, err := node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: a.nodeID}))
	if err != nil || !resp.Msg.Supported || len(resp.Msg.Inbounds) != 3 {
		t.Fatalf("GetNodeConnections = %+v, %v", resp, err)
	}
	if got := resp.Msg.NowUnix - resp.Msg.SinceUnix; got != 24*3600 {
		t.Errorf("default window = %d s, want 24 h", got)
	}
	hy, wg := resp.Msg.Inbounds[0], resp.Msg.Inbounds[1]
	if hy.InboundId != ids.i1 || hy.ProfileId != "prf_1" || hy.ProfileName != "p1" || hy.Protocol != "hysteria2" ||
		hy.Connects != 4 || hy.Drops != 1 || hy.Stalls != 3 || hy.OnlineS != 610 || hy.UnknownKeys != 4 || hy.Hellos != 20 || hy.Handshakes != 9 ||
		hy.KeysStalled != 1 || hy.KeysTried != 2 {
		t.Errorf("hysteria inbound = %+v", hy)
	}
	if !equalStrings(hy.Unsupported, connectiondiag.UnsupportedCountersForNode("hysteria2", []string{capLinks})) ||
		len(hy.Clients) != 1 || hy.Clients[0].Client != "Happ/3.4.2" || hy.Clients[0].Platform != "android" ||
		hy.Clients[0].Devices != 2 || hy.Clients[0].Connects != 4 {
		t.Errorf("Hysteria2 clients/unsupported = %+v / %v", hy.Clients, hy.Unsupported)
	}
	if wg.InboundId != ids.i2 || wg.Protocol != "awg" || wg.Rejects != 5 || wg.Connects != 0 || wg.KeysTried != 0 ||
		!equalStrings(wg.Unsupported, connectiondiag.UnsupportedCounters("awg")) || wg.Awg == nil || wg.Awg.UdpRxPackets != 12345 ||
		wg.Awg.Backend != "userspace" || wg.Awg.PeersOnline != 2 {
		t.Errorf("wg inbound = %+v", wg)
	}
	if len(wg.Clients) != 1 || wg.Clients[0].Devices != 1 || wg.Clients[0].Rejects != 5 {
		t.Errorf("AWG clients = %+v", wg.Clients)
	}
	vless := resp.Msg.Inbounds[2]
	if vless.InboundId != "inb_vless" || vless.Protocol != "vless" || vless.OnlineS != 0 ||
		!equalStrings(vless.Unsupported, connectiondiag.UnsupportedCounters("vless")) {
		t.Errorf("VLESS unsupported = %+v", vless)
	}
	byHour, err := node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: a.nodeID, ByHour: true}))
	if err != nil || len(byHour.Msg.Inbounds) != 3 || len(byHour.Msg.Inbounds[0].Hours) == 0 {
		t.Fatalf("GetNodeConnections by_hour = %+v, %v", byHour, err)
	}
	for i, h := range byHour.Msg.Inbounds[0].Hours {
		if h.Connects+h.Rejects+h.Drops+h.Stalls+h.UnknownKeys+h.Hellos+h.Handshakes == 0 || (i > 0 && byHour.Msg.Inbounds[0].Hours[i-1].HourStartUnix >= h.HourStartUnix) {
			t.Errorf("hour row is zero or not ascending: %+v", byHour.Msg.Inbounds[0].Hours)
			break
		}
	}
	for hours, want := range map[uint32]int64{1: 3600, 0: 24 * 3600, 48: 48 * 3600, 168: 168 * 3600, 5000: 168 * 3600} {
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
	fleet, err := node.GetFleetConnections(e.ctx, connect.NewRequest(&adminv1.GetFleetConnectionsRequest{Hours: 168}))
	if err != nil || len(fleet.Msg.Nodes) != 2 || fleet.Msg.Nodes[0].NodeName != "nodea" || !fleet.Msg.Nodes[0].Supported ||
		len(fleet.Msg.Nodes[0].Inbounds) != 3 || fleet.Msg.Nodes[0].Inbounds[1].Awg == nil ||
		fleet.Msg.Nodes[0].Inbounds[1].Awg.GetUdpRxPackets() != 12345 || len(fleet.Msg.Nodes[0].Inbounds[1].Clients) != 0 ||
		len(fleet.Msg.Nodes[0].Inbounds[1].Hours) != 0 || fleet.Msg.Nodes[1].Supported || len(fleet.Msg.Nodes[1].Inbounds) != 0 {
		t.Errorf("GetFleetConnections = %+v, %v", fleet, err)
	}
	if _, err = node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: "nod_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an unknown node: %v, want NotFound", err)
	}
}

func TestStatsLinks2QualityIsStoredAndRead(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	e.exec(`UPDATE profile SET protocol = 'hysteria2' WHERE id = 'prf_1'`)
	e.exec(`UPDATE device_credential SET protocol = 'hysteria2' WHERE id = 'crd_alice_hy'`)
	e.exec(`UPDATE device SET platform = 'android', client = 'Happ/3.4.2' WHERE id = 'dev_alice'`)
	c := connectLinksWithCaps(a, "inst2", capLinks, connectiondiag.Links2Capability)
	c.send(1, linksBatch(
		&agentv1.LinkCounters{CredId: "crd_alice_hy", InboundId: ids.i1, Connects: 3, ClientCloses: 1, Freezes: 2, Quiet: 3,
			RttMsSum: 1500, RttSamples: 3, PktsSent: 1000, PktsLost: 10},
		&agentv1.LinkCounters{InboundId: ids.i1, Hellos: 30, Handshakes: 20, Accepted: 9, HellosForeign: 4},
	))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 1 })

	node := nodeService{e.f}
	resp, err := node.GetNodeConnections(e.ctx, connect.NewRequest(&adminv1.GetNodeConnectionsRequest{NodeId: a.nodeID, ByHour: true}))
	if err != nil || !resp.Msg.Supported || len(resp.Msg.Inbounds) != 2 {
		t.Fatalf("GetNodeConnections with links/2 = %+v, %v", resp, err)
	}
	hy := resp.Msg.Inbounds[0]
	if len(hy.Unsupported) != 0 || hy.ClientCloses != 1 || hy.Freezes != 2 || hy.Quiet != 3 || hy.Accepted != 9 || hy.HellosForeign != 4 ||
		hy.RttMs != 500 || hy.LossPermille != 10 || hy.RttSamples != 3 || hy.PktsSent != 1000 || len(hy.Hours) != 1 ||
		hy.Hours[0].ClientCloses != 1 || hy.Hours[0].Freezes != 2 || hy.Hours[0].Accepted != 9 || hy.Hours[0].HellosForeign != 4 ||
		hy.Hours[0].RttMs != 500 || hy.Hours[0].LossPermille != 10 || hy.Hours[0].RttSamples != 3 || hy.Hours[0].PktsSent != 1000 ||
		len(hy.Clients) != 1 || hy.Clients[0].Freezes != 2 || hy.Clients[0].Quiet != 3 || hy.Clients[0].RttMs != 500 || hy.Clients[0].LossPermille != 10 {
		t.Errorf("Hysteria2 links/2 result = %+v", hy)
	}

	fleet, err := node.GetFleetConnections(e.ctx, connect.NewRequest(&adminv1.GetFleetConnectionsRequest{}))
	if err != nil || len(fleet.Msg.Nodes) == 0 || fleet.Msg.Nodes[0].Inbounds[0].Accepted != 9 || fleet.Msg.Nodes[0].Inbounds[0].RttMs != 500 ||
		fleet.Msg.Nodes[0].Inbounds[0].LossPermille != 10 || len(fleet.Msg.Nodes[0].Inbounds[0].Unsupported) != 0 {
		t.Errorf("GetFleetConnections links/2 = %+v, %v", fleet, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guardLinks keeps at most maxStatsDeltas rows; the traffic limits do not touch the counters.
func TestGuardStatsLimitsLinkRows(t *testing.T) {
	f := &Fleet{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	links := make([]*agentv1.LinkCounters, maxStatsDeltas+3)
	for i := range links {
		links[i] = &agentv1.LinkCounters{CredId: fmt.Sprintf("crd_%d", i), InboundId: "inb_cap", Connects: 1,
			ClientCloses: 2, Freezes: 3, Quiet: 4, Accepted: 5, HellosForeign: 6, RttMsSum: 7, RttSamples: 8, PktsSent: 9, PktsLost: 10}
	}
	g := f.guardStats(&agentv1.StatsBatch{IntervalStartUnix: 1_000, IntervalEndUnix: 1_010, Links: links})
	if len(g.links) != maxStatsDeltas || g.rejected != 0 || g.links[maxStatsDeltas-1].CredID != fmt.Sprintf("crd_%d", maxStatsDeltas-1) ||
		g.links[0].ClientCloses != 2 || g.links[0].Freezes != 3 || g.links[0].Quiet != 4 || g.links[0].Accepted != 5 ||
		g.links[0].HellosForeign != 6 || g.links[0].RttMSSum != 7 || g.links[0].RttSamples != 8 || g.links[0].PktsSent != 9 || g.links[0].PktsLost != 10 {
		t.Fatalf("%d rows in, %d kept, rejected %d; want the first %d kept", len(links), len(g.links), g.rejected, maxStatsDeltas)
	}
	// a batch whose traffic is refused still gets its counters in
	deltas := []*agentv1.TrafficDelta{{CredId: "crd_a", InboundId: "inb_cap", BytesUp: 1 << 60}}
	g = f.guardStats(&agentv1.StatsBatch{IntervalStartUnix: 1_000, IntervalEndUnix: 1_010, Traffic: deltas, Links: links[:2]})
	if g.rejected != 1 || len(g.links) != 2 {
		t.Fatalf("a refused delta: rejected %d, links %d, want 1 and 2", g.rejected, len(g.links))
	}
}
