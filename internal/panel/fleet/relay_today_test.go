package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestNodeViewsIncludeRelayTodayBytes(t *testing.T) {
	e := newCoreEnv(t)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	e.f.now = func() time.Time { return now }
	for _, row := range [][4]string{
		{"nod_de1", "de1", "de1.example.invalid", "active"},
		{"nod_msk1", "msk1", "msk1.example.invalid", "active"},
	} {
		e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, ?, ?)`, row[0], row[1], row[2], row[3], now.Unix())
	}
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_de1', 'hysteria2', 'de1', '{}', ?, ?)`, now.Unix(), now.Unix())
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_de1', 'prf_de1', 'nod_de1', ?, ?)`, now.Unix(), now.Unix())
	if err := e.st.CreateRelay(e.ctx, store.Relay{ID: "rly_de1", InboundID: "inb_de1", ViaNodeID: "nod_msk1", Network: "udp", Port: 443}, now); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := e.st.UpdateRelay(e.ctx, "rly_de1", store.RelayPatch{Enabled: &on}, now); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []store.RelayCounters{
		{Bytes: 80, CountedUnix: now.Unix() - 60},
		{Bytes: 180, CountedUnix: now.Unix() - 30},
	} {
		if err := e.st.SetRelayCounters(e.ctx, "nod_msk1", []store.RelayCounterUpdate{{RelayID: "rly_de1", Network: "udp", Port: 443, RelayCounters: sample}}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := (nodeService{e.f}).ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range list.Msg.Nodes {
		if n.Id == "nod_msk1" {
			found = true
			if n.RelayTodayBytes != 180 {
				t.Fatalf("entry today bytes = %d, want 180", n.RelayTodayBytes)
			}
		}
	}
	if !found {
		t.Fatal("entry node is absent from the list")
	}
	detail, err := (nodeService{e.f}).GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: "nod_msk1"}))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Msg.Node.RelayTodayBytes != 180 {
		t.Fatalf("entry detail today bytes = %d, want 180", detail.Msg.Node.RelayTodayBytes)
	}
}
