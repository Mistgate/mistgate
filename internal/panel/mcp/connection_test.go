package mcp

import (
	"strings"
	"testing"
)

// user_connection and node_connections: the counters as the panel gave them, with the names of the nodes, the panel's text cleaned,
// the window passed on (0 = the panel's 24, the rest cut to 48), and a node that does not send counters says so.
func TestConnectionToolsContent(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	u := decode[UserConnectionV](t, mustOK(t, s, "user_connection", map[string]any{"user_id": "usr_alice"}))
	if u.NowUnix-u.SinceUnix != 24*3600 || len(u.Rows) != 2 {
		t.Fatalf("view: %+v", u)
	}
	if r := u.Rows[0]; r.DeviceID != "dev_phone" || r.NodeName != "de1" || r.Profile != "Fast" || r.Protocol != "hysteria2" ||
		r.Connects != 4 || r.Rejects != 1 || r.Drops != 2 || r.Stalls != 3 || r.OnlineS != 7200 || r.LastHourUnix != 1699999200 {
		t.Errorf("row: %+v", r)
	}
	if strings.Contains(u.Rows[1].Profile, canaryTK) {
		t.Errorf("a profile name carried a token through: %q", u.Rows[1].Profile)
	}
	if len(u.NodesWithoutCounters) != 2 || u.NodesWithoutCounters[0].NodeID != nodeB || u.NodesWithoutCounters[0].NodeName != "nl1" ||
		u.NodesWithoutCounters[1].NodeID != "nod_gone" || u.NodesWithoutCounters[1].NodeName != "" {
		t.Errorf("nodes without counters: %+v", u.NodesWithoutCounters)
	}
	for hours, want := range map[int]int64{0: 24, 1: 1, 48: 48, 500: 48, -3: 24} {
		v := decode[UserConnectionV](t, mustOK(t, s, "user_connection", map[string]any{"user_id": "usr_alice", "hours": hours}))
		if v.NowUnix-v.SinceUnix != want*3600 {
			t.Errorf("hours %d: window %d h, want %d", hours, (v.NowUnix-v.SinceUnix)/3600, want)
		}
	}
	if out := mustFail(t, s, "user_connection", map[string]any{"user_id": "usr_nobody"}); !strings.Contains(out, "not found") {
		t.Errorf("an unknown user: %s", out)
	}

	n := decode[NodeConnectionsV](t, mustOK(t, s, "node_connections", map[string]any{"node": "de1", "hours": 6}))
	if !n.Supported || n.NodeID != nodeA || n.NodeName != "de1" || n.NowUnix-n.SinceUnix != 6*3600 || len(n.Inbounds) != 2 {
		t.Fatalf("view: %+v", n)
	}
	if i := n.Inbounds[0]; i.InboundID != "inb_1" || i.Profile != "Fast" || i.Connects != 40 || i.Drops != 2 || i.OnlineS != 9000 ||
		i.UnknownKeys != 5 || i.Hellos != 100 || i.Handshakes != 20 || i.KeysTried != 9 {
		t.Errorf("hysteria2 inbound: %+v", i)
	}
	if i := n.Inbounds[1]; i.Stalls != 12 || i.KeysStalled != 4 || strings.Contains(i.Profile, canaryTK) {
		t.Errorf("awg inbound: %+v", i)
	}
	old := decode[NodeConnectionsV](t, mustOK(t, s, "node_connections", map[string]any{"node": nodeB}))
	if old.Supported || len(old.Inbounds) != 0 || old.NodeName != "nl1" {
		t.Errorf("a node without counters: %+v", old)
	}
}
