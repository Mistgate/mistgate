package access

import (
	"encoding/json"
	"testing"
	"time"
)

type subscriptionOnline struct{ users map[string]string }

func (s subscriptionOnline) OnlineUsers() map[string]string { return s.users }

func TestSubscriptionReportsCapacityUtilization(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.inbound(f.profile, "nod_nl1")
	if _, err := e.st.W.Exec(`UPDATE node SET bandwidth_mbps = 100 WHERE id = ?`, f.nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE node SET bandwidth_mbps = 50 WHERE id = 'nod_nl1'`); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		id     string
		rx, tx int64
	}{
		{id: f.nodeID, rx: 80_000_000},
		{id: "nod_nl1", rx: 20_000_000, tx: 30_000_000},
	} {
		e.sql(`UPDATE node SET last_seen_at = ? WHERE id = ?`, e.clock.Unix(), sample.id)
		e.sql(`INSERT INTO node_live (node_id, session, sample_at, rx_bps, tx_bps, users, live_json)
			VALUES (?, 1, ?, ?, ?, '{}', '{}')`, sample.id, e.clock.Add(-time.Second).Unix(), sample.rx, sample.tx)
	}
	e.s.online = subscriptionOnline{users: map[string]string{}}
	created := e.user("alice", f.group, nil).User
	users, err := json.Marshal(map[string]int64{created.Id: e.clock.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	e.sql(`UPDATE node_live SET users = ? WHERE node_id = ?`, string(users), f.nodeID)
	user := must(e.st.Access().User(e.ctx, created.Id))
	data, err := e.st.Access().SubscriptionData(e.ctx, user.ID, user.GroupID, user.PeriodStart, true, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	foundOnline := false
	for _, live := range data.LiveNodes {
		if live.NodeID == f.nodeID {
			foundOnline = live.UserOnline
		}
	}
	if !foundOnline {
		t.Fatalf("subscription live rows did not find the user key: %+v", data.LiveNodes)
	}

	view, err := e.s.subView(e.ctx, user, false, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(view.Servers))
	}
	got := make(map[string]int, len(view.Servers))
	for _, server := range view.Servers {
		if server.LoadPercent == nil {
			t.Errorf("%s has no capacity utilization", server.NodeID)
			continue
		}
		got[server.NodeID] = *server.LoadPercent
	}
	if got[f.nodeID] != 80 || got["nod_nl1"] != 60 {
		t.Errorf("capacity utilization = %v, want de1=80 nl1=60", got)
	}
	for _, node := range view.Nodes {
		if !node.Online {
			t.Errorf("node row %q is offline despite a fresh last_seen_at", node.ID)
		}
	}
	var hasOnlineDevice bool
	for _, device := range view.Devices {
		hasOnlineDevice = hasOnlineDevice || device.Online
	}
	if !hasOnlineDevice {
		t.Error("implicit device did not use the user-online flag from node_live.users")
	}
}
