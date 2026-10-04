package access

import (
	"testing"
	"time"
)

type subscriptionNetworkSample struct {
	rx, tx uint64
	at     time.Time
}

type subscriptionOnline struct {
	users   map[string]string
	samples map[string]subscriptionNetworkSample
}

func (s subscriptionOnline) OnlineUsers() map[string]string { return s.users }

func (s subscriptionOnline) NetworkUsage(nodeID string) (uint64, uint64, time.Time, bool) {
	sample, ok := s.samples[nodeID]
	return sample.rx, sample.tx, sample.at, ok
}

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
	e.s.online = subscriptionOnline{samples: map[string]subscriptionNetworkSample{
		f.nodeID:  {rx: 80_000_000, at: e.clock.Add(-time.Second)},
		"nod_nl1": {rx: 20_000_000, tx: 30_000_000, at: e.clock.Add(-time.Second)},
	}}
	created := e.user("alice", f.group, nil).User
	user := must(e.st.Access().User(e.ctx, created.Id))

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
}
