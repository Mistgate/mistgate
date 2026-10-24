package mcp

import (
	"strings"
	"testing"
)

// user_connection and node_connections: the counters as the panel gave them, with the names of the nodes, the panel's text cleaned,
// the window passed on (0 = the panel's 24, the rest cut to 168), and a node that does not send counters says so.
func TestConnectionToolsContent(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	u := decode[UserConnectionV](t, mustOK(t, s, "user_connection", map[string]any{"user_id": "usr_alice", "by_hour": true}))
	if u.NowUnix-u.SinceUnix != 24*3600 || len(u.Rows) != 2 {
		t.Fatalf("view: %+v", u)
	}
	if r := u.Rows[0]; r.DeviceID != "dev_phone" || r.NodeName != "de1" || r.Profile != "Fast" || r.Protocol != "hysteria2" ||
		r.Connects != 4 || r.Rejects != 1 || r.Drops != 2 || r.Stalls != 3 || r.OnlineS != 7200 || r.LastHourUnix != 1699999200 ||
		r.Platform != "android" || r.Client != "Happ/3.4.2" || r.Kind != "link" || len(r.Hours) != 2 ||
		r.Hours[0].HourUnix >= r.Hours[1].HourUnix || r.Hours[0].Connects != 2 || r.Hours[1].Drops != 2 {
		t.Errorf("row: %+v", r)
	}
	if strings.Contains(u.Rows[1].Profile, canaryTK) {
		t.Errorf("a profile name carried a token through: %q", u.Rows[1].Profile)
	}
	if len(u.NodesWithoutCounters) != 2 || u.NodesWithoutCounters[0].NodeID != nodeB || u.NodesWithoutCounters[0].NodeName != "nl1" ||
		u.NodesWithoutCounters[1].NodeID != "nod_gone" || u.NodesWithoutCounters[1].NodeName != "" {
		t.Errorf("nodes without counters: %+v", u.NodesWithoutCounters)
	}
	for hours, want := range map[int]int64{0: 24, 1: 1, 48: 48, 168: 168, 500: 168, -3: 24} {
		v := decode[UserConnectionV](t, mustOK(t, s, "user_connection", map[string]any{"user_id": "usr_alice", "hours": hours}))
		if v.NowUnix-v.SinceUnix != want*3600 {
			t.Errorf("hours %d: window %d h, want %d", hours, (v.NowUnix-v.SinceUnix)/3600, want)
		}
	}
	if out := mustFail(t, s, "user_connection", map[string]any{"user_id": "usr_nobody"}); !strings.Contains(out, "not found") {
		t.Errorf("an unknown user: %s", out)
	}

	n := decode[NodeConnectionsV](t, mustOK(t, s, "node_connections", map[string]any{"node": "de1", "hours": 6, "by_hour": true}))
	if !n.Supported || n.NodeID != nodeA || n.NodeName != "de1" || n.NowUnix-n.SinceUnix != 6*3600 || len(n.Inbounds) != 2 {
		t.Fatalf("view: %+v", n)
	}
	if i := n.Inbounds[0]; i.InboundID != "inb_1" || i.Profile != "Fast" || i.Connects != 40 || i.Drops != 2 || i.OnlineS != 9000 ||
		i.UnknownKeys != 5 || i.Hellos != 100 || i.Handshakes != 20 || i.KeysTried != 9 || len(i.Hours) != 2 ||
		i.Hours[0].HourUnix >= i.Hours[1].HourUnix || i.Hours[0].Connects != 10 || i.Hours[1].Drops != 2 || len(i.Clients) != 1 ||
		i.Clients[0].Client != "Happ/3.4.2" || i.Clients[0].Platform != "android" || i.Clients[0].Devices != 2 {
		t.Errorf("hysteria2 inbound: %+v", i)
	}
	if i := n.Inbounds[1]; i.Stalls != 12 || i.KeysStalled != 4 || strings.Contains(i.Profile, canaryTK) ||
		strings.Join(i.Unsupported, ",") != "rejects,drops,hellos,handshakes" || i.AWG == nil || i.AWG.UDPRxPackets != 12345 ||
		i.AWG.Backend != "userspace" || i.AWG.PeersOnline != 3 {
		t.Errorf("awg inbound: %+v", i)
	}
	old := decode[NodeConnectionsV](t, mustOK(t, s, "node_connections", map[string]any{"node": nodeB}))
	if old.Supported || len(old.Inbounds) != 0 || old.NodeName != "nl1" {
		t.Errorf("a node without counters: %+v", old)
	}
	fleetRaw := mustOK(t, s, "fleet_connections", map[string]any{"hours": 168})
	fleet := decode[FleetConnectionsV](t, fleetRaw)
	if fleet.NowUnix-fleet.SinceUnix != 168*3600 || len(fleet.Nodes) != 2 || fleet.Nodes[0].NodeID != nodeA || !fleet.Nodes[0].Supported ||
		len(fleet.Nodes[0].Inbounds) != 2 || fleet.Nodes[0].Inbounds[1].AWG == nil || fleet.Nodes[1].Supported || len(fleet.Nodes[1].Inbounds) != 0 {
		t.Errorf("fleet connections: %+v", fleet)
	}
	if strings.Contains(fleetRaw, `"clients"`) || strings.Contains(fleetRaw, `"hours"`) {
		t.Errorf("fleet view includes per-client or hourly detail: %s", fleetRaw)
	}
}

func TestNodeJournalToolContent(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	warning := decode[NodeJournalV](t, mustOK(t, s, "node_journal", map[string]any{
		"node": "de1", "hours": 6, "min_level": "warning", "source": "awg", "query": "HANDSHAKE", "limit": 5,
	}))
	if warning.NowUnix-warning.SinceUnix != 6*3600 || len(warning.Entries) != 1 {
		t.Fatalf("warning result: %+v", warning)
	}
	if warning.NodesWithoutJournal == nil {
		t.Error("nodes_without_journal is null for a single-node result; want an empty list")
	}
	if entry := warning.Entries[0]; entry.NodeID != nodeA || entry.NodeName != "de1" || entry.ProfileName != "Fast" ||
		entry.Level != "warning" || entry.Source != "awg" || entry.Count != 2 || entry.Attrs["code"] != "stall" {
		t.Errorf("warning entry: %+v", entry)
	}

	errorEntry := decode[NodeJournalV](t, mustOK(t, s, "node_journal", map[string]any{
		"node": nodeA, "min_level": "error", "query": "auth%_failed",
	}))
	if len(errorEntry.Entries) != 1 || errorEntry.Entries[0].Level != "error" || errorEntry.Entries[0].Attrs["detail"] != "auth%_failed" {
		t.Errorf("ERROR/attribute search: %+v", errorEntry)
	}

	all := decode[NodeJournalV](t, mustOK(t, s, "node_journal", map[string]any{"hours": 999, "limit": 1}))
	if all.NowUnix-all.SinceUnix != 168*3600 || len(all.Entries) != 1 || !all.Truncated ||
		len(all.NodesWithoutJournal) != 1 || all.NodesWithoutJournal[0] != nodeB {
		t.Errorf("all-node, capped, truncated result: %+v", all)
	}
	if out := mustFail(t, s, "node_journal", map[string]any{"query": strings.Repeat("x", 101)}); !strings.Contains(out, "query too long") {
		t.Errorf("long query: %s", out)
	}
}
