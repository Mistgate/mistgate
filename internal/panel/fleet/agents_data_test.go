package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Stats batches produce hourly host rows: the host load of two batches of one hour is one row, mean and peak.
func TestStatsBatchesFeedHostHours(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "host-hours")
	now := time.Now().Unix()
	for _, batch := range []struct {
		seq uint64
		cpu float32
	}{{seq: 1, cpu: 10}, {seq: 2, cpu: 30}} {
		b := statsBatch(now-10, now, nil, []*agentv1.Session{{CredId: "crd_alice_hy", InboundId: ids.i1, ConnectedAtUnix: now - 60}})
		b.GetStats().Host = &agentv1.HostMetrics{CpuPct: batch.cpu, SoftirqPct: 4, Load1: 0.5, RamUsedBytes: 512, RamTotalBytes: 1024, NetRxBps: 100 * batch.seq, NetTxBps: 50}
		c.send(batch.seq, b)
	}
	node := nodeService{e.f}
	waitFor(t, func() bool {
		r, err := node.GetNodeHostHistory(e.ctx, connect.NewRequest(&adminv1.GetNodeHostHistoryRequest{NodeId: a.nodeID, Hours: 2}))
		return err == nil && len(r.Msg.Hours) > 0 && r.Msg.Hours[len(r.Msg.Hours)-1].Samples == 2
	})
	r, err := node.GetNodeHostHistory(e.ctx, connect.NewRequest(&adminv1.GetNodeHostHistoryRequest{NodeId: a.nodeID, Hours: 2}))
	if err != nil {
		t.Fatal(err)
	}
	h := r.Msg.Hours[len(r.Msg.Hours)-1]
	if r.Msg.NodeName != "de1" || h.CpuAvgPct != 20 || h.CpuMaxPct != 30 || h.SoftirqMaxPct != 4 || h.RamUsedMaxBytes != 512 ||
		h.RxMaxBps != 200 || h.RxAvgBps != 150 || h.SessionsMax != 1 {
		t.Errorf("host hour = %+v (node %q)", h, r.Msg.NodeName)
	}
	if _, err := node.GetNodeHostHistory(e.ctx, connect.NewRequest(&adminv1.GetNodeHostHistoryRequest{NodeId: "nod_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
	if d, err := node.GetNodeHostHistory(e.ctx, connect.NewRequest(&adminv1.GetNodeHostHistoryRequest{NodeId: a.nodeID, Hours: 9999})); err != nil ||
		d.Msg.NowUnix-d.Msg.SinceUnix < 719*3600 || d.Msg.NowUnix-d.Msg.SinceUnix > 720*3600 {
		t.Errorf("hours 9999 window = %+v, %v; want 720 hours", d, err)
	}
}

// GetNodeRuntime: a node that applied the desired state is in sync with the time of the apply; an inbound the node runs that the
// panel has no row for shows as unknown; a drifting or foreign state is not in sync.
func TestGetNodeRuntimeDesiredAgainstApplied(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, ds := connectFull(a, "runtime")
	node := nodeService{e.f}
	get := func(id string) *adminv1.NodeRuntime {
		t.Helper()
		r, err := node.GetNodeRuntime(e.ctx, connect.NewRequest(&adminv1.GetNodeRuntimeRequest{NodeId: id}))
		if err != nil || len(r.Msg.Nodes) != 1 {
			t.Fatalf("GetNodeRuntime(%q) = %+v, %v", id, r, err)
		}
		return r.Msg.Nodes[0]
	}
	waitFor(t, func() bool { return get(a.nodeID).LastApplyUnix != 0 })
	n := get(a.nodeID)
	if !n.InSync || n.Drift || n.DesiredHash == "" || n.DesiredHash != n.AppliedHash || len(n.DesiredHash) > 12 || n.AppliedRevision != ds.Revision ||
		n.DesiredRevision != ds.Revision || n.LastApplyStatus != "applied" || n.NodeName != "nodea" || !n.Connected || len(n.Inbounds) != 2 {
		t.Fatalf("runtime = %+v", n)
	}
	for _, in := range n.Inbounds {
		if in.InboundId != ids.i1 && in.InboundId != ids.i2 || !in.Enabled || in.ProfileName == "" || in.SpecVersion != 1 {
			t.Errorf("inbound = %+v", in)
		}
	}

	// A stats batch that lists a running inbound the panel never heard of.
	b := statsBatch(time.Now().Unix()-10, time.Now().Unix(), nil, nil)
	b.GetStats().Health = []*agentv1.InboundHealth{
		{InboundId: ids.i1, State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING},
		{InboundId: "inb_ghost", State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING},
	}
	c.send(1, b)
	waitFor(t, func() bool { return len(get(a.nodeID).UnknownRunningInboundIds) == 1 })
	n = get(a.nodeID)
	if n.UnknownRunningInboundIds[0] != "inb_ghost" {
		t.Errorf("unknown = %v", n.UnknownRunningInboundIds)
	}
	for _, in := range n.Inbounds {
		if in.InboundId == ids.i1 && in.LiveState != "active" {
			t.Errorf("live state of inb_1 = %q", in.LiveState)
		}
	}

	// Another hash applied for the sent revision: a drift, so not in sync (the panel resends once, a second time it stays drift).
	e.exec(`UPDATE node SET applied_hash = 'ffffffffffffffffffff' WHERE id = ?`, a.nodeID)
	if n := get(a.nodeID); n.InSync || n.AppliedHash != "ffffffffffff" {
		t.Errorf("a different applied hash is in sync: %+v", n)
	}

	// Every node: both live nodes, by name; a retired node is gone.
	e.exec(`UPDATE node SET state = 'retired' WHERE id = ?`, ids.nodeB)
	all, err := node.GetNodeRuntime(e.ctx, connect.NewRequest(&adminv1.GetNodeRuntimeRequest{}))
	if err != nil || len(all.Msg.Nodes) != 1 || all.Msg.Nodes[0].NodeId != a.nodeID {
		t.Errorf("all nodes = %+v, %v", all, err)
	}
	if _, err := node.GetNodeRuntime(e.ctx, connect.NewRequest(&adminv1.GetNodeRuntimeRequest{NodeId: ids.nodeB})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("retired node: %v", err)
	}
	if _, err := node.GetNodeRuntime(e.ctx, connect.NewRequest(&adminv1.GetNodeRuntimeRequest{NodeId: "nod_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
}

// GetPathLoss lists the stored lossy paths by node name, the worst first, and says per target how many senders lose packets.
func TestGetPathLossRowsAndTargets(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	now := time.Now().UTC()
	e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_c', 'nodec', 'c.example.com', 'active', ?)`, now.Unix())
	// nodeb and nodec both lose packets to nodea; the check by nodec is too old for a 24-hour window but not for 72.
	err := e.st.UpdateUDPPathLosses(e.ctx, a.nodeID, []string{ids.nodeB, "nod_c"}, []store.UDPPathLoss{
		{SenderID: ids.nodeB, TargetID: a.nodeID, Ports: "443,8443", Sent: 200, Got: 100, OtherSenderID: "nod_c", OtherGot: 199, CheckedAt: now.Add(-time.Hour)},
		{SenderID: "nod_c", TargetID: a.nodeID, Ports: "443", Sent: 100, Got: 0, OtherSenderID: ids.nodeB, OtherGot: 99, OpenPorts: "8443", CheckedAt: now.Add(-48 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	node := nodeService{e.f}
	r, err := node.GetPathLoss(e.ctx, connect.NewRequest(&adminv1.GetPathLossRequest{}))
	if err != nil || len(r.Msg.Rows) != 1 || len(r.Msg.Targets) != 1 {
		t.Fatalf("default window = %+v, %v", r, err)
	}
	row := r.Msg.Rows[0]
	if row.FromName != "nodeb" || row.ToName != "nodea" || row.Ports != "443,8443" || row.Sent != 200 || row.Got != 100 || row.LossPct != 50 ||
		row.OtherName != "nodec" || row.OtherGot != 199 || row.CheckedUnix != now.Add(-time.Hour).Unix() {
		t.Errorf("row = %+v", row)
	}
	if r.Msg.NowUnix-r.Msg.SinceUnix != 24*3600 {
		t.Errorf("default window = %d s", r.Msg.NowUnix-r.Msg.SinceUnix)
	}
	r, err = node.GetPathLoss(e.ctx, connect.NewRequest(&adminv1.GetPathLossRequest{Hours: 72}))
	if err != nil || len(r.Msg.Rows) != 2 || r.Msg.Rows[0].LossPct != 100 || r.Msg.Rows[0].OpenPorts != "8443" {
		t.Fatalf("72 h = %+v, %v", r, err)
	}
	if tg := r.Msg.Targets; len(tg) != 1 || tg[0].Name != "nodea" || tg[0].LossySenders != 2 || tg[0].WorstLossPct != 100 || tg[0].NodeId != a.nodeID {
		t.Errorf("targets = %+v", tg)
	}
	for _, row := range r.Msg.Rows {
		for _, s := range []string{row.FromName, row.ToName, row.OtherName, row.Ports} {
			if s == "a.example.com" || s == "b.example.com" || s == "c.example.com" {
				t.Errorf("an address in the row: %+v", row)
			}
		}
	}
}
