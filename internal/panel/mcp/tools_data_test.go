//go:build !js

package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Fakes of the three data reads. de1 has the interesting shape: an inbound the panel dropped that the node still reports active, one that was
// wanted and never confirmed, one the panel has no record of, and an error text carrying an address and a token that must not come out.

func (w *world) GetNodeRuntime(_ context.Context, r *connect.Request[adminv1.GetNodeRuntimeRequest]) (*connect.Response[adminv1.GetNodeRuntimeResponse], error) {
	w.mu.Lock()
	w.runtimeReqs = append(w.runtimeReqs, r.Msg.GetNodeId())
	w.mu.Unlock()
	de1 := &adminv1.NodeRuntime{
		NodeId: nodeA, NodeName: "de1", State: "active", Connected: true,
		DesiredRevision: 9, DesiredHash: "aaaaaaaaaaaa", AppliedRevision: 8, AppliedHash: "bbbbbbbbbbbb", Drift: true, DriftSinceUnix: 1699990000,
		LastApplyUnix: 1699999000, LastApplyStatus: "applied", LastApplyMs: 420,
		Inbounds: []*adminv1.InboundRuntime{
			{InboundId: "inb_1", ProfileName: "Fast", Protocol: "hysteria2", Enabled: true, SpecVersion: 3, AppliedSpecHash: "0123456789ab", AppliedState: "active", LiveState: "active", CertNotAfterUnix: 1710000000},
			{InboundId: "inb_old", ProfileName: "Old", Protocol: "hysteria2", Enabled: false, SpecVersion: 1, AppliedSpecHash: "ffffffffffff", AppliedState: "active", LiveState: "active"},
			{InboundId: "inb_new", ProfileName: "New", Protocol: "awg", Enabled: true, SpecVersion: 1, AppliedState: "pending",
				LastError: "bind " + canaryAddr + ":443 failed " + canaryTK},
		},
		UnknownRunningInboundIds: []string{"inb_ghost"},
	}
	nl1 := &adminv1.NodeRuntime{NodeId: nodeB, NodeName: "nl1", State: "active", Connected: true, DesiredRevision: 4, DesiredHash: "cccccccccccc",
		AppliedRevision: 4, AppliedHash: "cccccccccccc", InSync: true, LastApplyUnix: 1699999500, LastApplyStatus: "in_sync", Inbounds: []*adminv1.InboundRuntime{}}
	out := []*adminv1.NodeRuntime{de1, nl1}
	if id := r.Msg.GetNodeId(); id != "" {
		out = slices.DeleteFunc(out, func(n *adminv1.NodeRuntime) bool { return n.NodeId != id })
	}
	return connect.NewResponse(&adminv1.GetNodeRuntimeResponse{NowUnix: 1700000000, Nodes: out}), nil
}

func (w *world) GetPathLoss(_ context.Context, r *connect.Request[adminv1.GetPathLossRequest]) (*connect.Response[adminv1.GetPathLossResponse], error) {
	w.mu.Lock()
	w.pathLossHours = append(w.pathLossHours, r.Msg.GetHours())
	w.mu.Unlock()
	if w.pathLossRows != nil {
		return connect.NewResponse(&adminv1.GetPathLossResponse{NowUnix: insNow, Rows: w.pathLossRows}), nil
	}
	return connect.NewResponse(&adminv1.GetPathLossResponse{SinceUnix: 1699913600, NowUnix: 1700000000,
		Rows: []*adminv1.PathLossRow{
			{FromNodeId: nodeB, FromName: "nl1", ToNodeId: nodeA, ToName: "de1", Ports: "443,8443", Sent: 300, Got: 150, LossPct: 50, OtherName: "fi1", OtherGot: 299, CheckedUnix: 1699999000},
			{FromNodeId: "nod_c", FromName: "be", ToNodeId: nodeA, ToName: "de1", Ports: "443", Sent: 100, Got: 0, LossPct: 100, OtherName: "fi1", OtherGot: 99, OpenPorts: "8443", CheckedUnix: 1699998000},
		},
		Targets: []*adminv1.PathLossTarget{{NodeId: nodeA, Name: "de1", LossySenders: 2, WorstLossPct: 100}}}), nil
}

func (w *world) GetNodeHostHistory(_ context.Context, r *connect.Request[adminv1.GetNodeHostHistoryRequest]) (*connect.Response[adminv1.GetNodeHostHistoryResponse], error) {
	hours := int(r.Msg.GetHours())
	w.mu.Lock()
	w.hostHours = append(w.hostHours, hours)
	w.mu.Unlock()
	const newest = int64(1700000400) - 1700000400%3600
	resp := &adminv1.GetNodeHostHistoryResponse{NodeId: r.Msg.GetNodeId(), NodeName: "de1", NowUnix: 1700000400, SinceUnix: newest - int64(hours-1)*3600}
	for i := hours - 1; i >= 0; i-- {
		h := &adminv1.HostHour{HourStartUnix: newest - int64(i)*3600, Samples: 360, CpuAvgPct: 20, CpuMaxPct: 40, SoftirqMaxPct: 5, Load1Max: 1.5,
			RamUsedMaxBytes: 1 << 30, RamTotalBytes: 2 << 30, RxAvgBps: 1000, RxMaxBps: 5000, TxAvgBps: 2000, TxMaxBps: 6000, SessionsMax: 7}
		if i == 3 { // the busy hour the peaks must name
			h.CpuMaxPct, h.SessionsMax, h.TxMaxBps = 97, 31, 90000
		}
		resp.Hours = append(resp.Hours, h)
	}
	return connect.NewResponse(resp), nil
}

func TestNodeRuntimeShowsDesiredAgainstAppliedWithoutSecrets(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	if !slices.Contains(toolNames(t, s), "node_runtime") {
		t.Fatal("readonly does not see node_runtime")
	}
	out := mustOK(t, s, "node_runtime", map[string]any{"node": "de1"})
	noCanary(t, "node_runtime", out)
	if strings.Contains(out, "bind") && strings.Contains(out, canaryAddr) {
		t.Errorf("an address came through: %s", out)
	}
	v := decode[NodeRuntimeV](t, out)
	if len(v.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1: %s", len(v.Nodes), out)
	}
	n := v.Nodes[0]
	if n.Name != "de1" || n.InSync || !n.Drift || n.DesiredHash == n.AppliedHash || n.DriftSince != 1699990000 || n.LastApplyStatus != "applied" || len(n.Inbounds) != 3 {
		t.Errorf("node = %+v", n)
	}
	kinds := map[string]string{}
	for _, a := range n.Attention {
		kinds[a.Kind] = a.InboundID
	}
	if kinds["applied_not_desired"] != "inb_old" || kinds["desired_not_applied"] != "inb_new" || kinds["unknown_running"] != "inb_ghost" || len(kinds) != 3 {
		t.Errorf("attention = %+v", n.Attention)
	}
	if got := e.w.runtimeReqs; len(got) != 1 || got[0] != nodeA {
		t.Errorf("GetNodeRuntime asked for %v, want the node id", got)
	}
	// every node
	all := decode[NodeRuntimeV](t, mustOK(t, s, "node_runtime", map[string]any{}))
	if len(all.Nodes) != 2 || !all.Nodes[1].InSync || len(all.Nodes[1].Attention) != 0 {
		t.Errorf("all nodes = %+v", all.Nodes)
	}
	mustFail(t, s, "node_runtime", map[string]any{"node": "nope"})
}

func TestPathLossMatrixNamesNodesAndSummarisesTargets(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	out := mustOK(t, s, "path_loss_matrix", map[string]any{"hours": 48})
	noCanary(t, "path_loss_matrix", out)
	v := decode[PathLossV](t, out)
	if len(v.Rows) != 2 || v.Rows[0].From != "nl1" || v.Rows[0].To != "de1" || v.Rows[0].LossPct != 50 || v.Rows[0].Other != "fi1" ||
		v.Rows[1].OpenPorts != "8443" || len(v.Targets) != 1 || v.Targets[0].Target != "de1" || v.Targets[0].LossySenders != 2 {
		t.Errorf("result = %+v", v)
	}
	if got := e.w.pathLossHours; len(got) != 1 || got[0] != 48 {
		t.Errorf("hours sent = %v", got)
	}
	mustOK(t, s, "path_loss_matrix", map[string]any{"hours": 5000})
	if got := e.w.pathLossHours; got[1] != 720 {
		t.Errorf("hours 5000 sent as %d, want 720", got[1])
	}
}

func TestNodeMetricsHoursGivesSeriesAndPeaks(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	// without hours: no history call, no series
	plain := decode[NodeMetricsV](t, mustOK(t, s, "node_metrics", map[string]any{"node": "de1"}))
	if len(plain.Series) != 0 || plain.Peaks != nil || len(e.w.hostHours) != 0 {
		t.Errorf("node_metrics without hours returned a history: %+v (calls %v)", plain, e.w.hostHours)
	}
	v := decode[NodeMetricsV](t, mustOK(t, s, "node_metrics", map[string]any{"node": "de1", "hours": 24}))
	if len(v.Series) != 24 || v.BucketHours != 1 || v.Peaks == nil || v.Peaks.CPUPct.Value != 97 || v.Peaks.Sessions.Value != 31 ||
		v.Peaks.TxBps.Value != 90000 || v.Series[0].AtUnix >= v.Series[23].AtUnix || v.Series[23].Samples != 360 {
		t.Errorf("24 h = %+v", v)
	}
	if peak := v.Peaks.CPUPct.AtUnix; peak != v.Series[20].AtUnix {
		t.Errorf("cpu peak at %d, want the busy hour %d", peak, v.Series[20].AtUnix)
	}
	// a month is bucketed to stay small, with the peak kept
	month := mustOK(t, s, "node_metrics", map[string]any{"node": "de1", "hours": 720})
	if len(month) > maxResultBytes {
		t.Errorf("720 h answer is %d bytes", len(month))
	}
	m := decode[NodeMetricsV](t, month)
	if m.BucketHours != 8 || len(m.Series) != 90 || m.Truncated || m.Peaks.CPUPct.Value != 97 {
		t.Errorf("720 h: bucket %d, %d points, truncated %v, peaks %+v", m.BucketHours, len(m.Series), m.Truncated, m.Peaks)
	}
	if _, isErr := callTool(t, s, "node_metrics", map[string]any{"node": "de1", "hours": 721}); !isErr {
		t.Error("hours 721 accepted")
	}
}

// fleet_digest names crash restarts, a crash loop, host reboots and lossy pairs among a node's reasons.
func TestFleetDigestShowsCrashLoopRebootAndPathLoss(t *testing.T) {
	e, iw := newInsightEnv(t)
	crash := func(sig string) map[string]string {
		return map[string]string{"prev_exit": "unclean", "crash": "1", "reason": "restart", "exit_signal": sig}
	}
	iw.events = append([]*adminv1.Event{
		ev(30, insNow-1200, sInfo, "agent_started", nodeC, "fi1", crash("KILL")),
		ev(29, insNow-900, sInfo, "agent_started", nodeC, "fi1", map[string]string{"prev_exit": "unclean", "crash": "1", "oom": "1"}),
		ev(28, insNow-600, sInfo, "agent_started", nodeC, "fi1", crash("SEGV")),
		ev(27, insNow-5*hour, sInfo, "agent_started", nodeD, "msk1", map[string]string{"prev_exit": "unclean", "reason": "boot"}),
		ev(26, insNow-3*hour, sInfo, "agent_started", nodeD, "msk1", crash("KILL")), // one crash is a restart, not a loop
		ev(25, insNow-hour/2, sInfo, "agent_started", nodeA, "de1", map[string]string{"prev_exit": "clean", "reason": "restart"}),
	}, iw.events...)
	iw.pathLossRows = []*adminv1.PathLossRow{
		{FromNodeId: nodeB, FromName: "nl1", ToNodeId: nodeA, ToName: "de1", LossPct: 50, CheckedUnix: insNow - hour},
		{FromNodeId: nodeD, FromName: "msk1", ToNodeId: nodeA, ToName: "de1", LossPct: 100, CheckedUnix: insNow - 40*hour}, // out of the window
	}
	_, secret := e.token(ProfileReadonly)
	out := mustOK(t, e.session(secret), "fleet_digest", map[string]any{"hours": 24})
	noCanary(t, "fleet_digest", out)
	d := decode[DigestV](t, out)
	reasons := map[string]map[string]digestReasonV{}
	for _, n := range d.Problems {
		reasons[n.Name] = map[string]digestReasonV{}
		for _, r := range n.Reasons {
			reasons[n.Name][r.Code] = r
		}
	}
	fi := reasons["fi1"]
	if fi["agent_crash"].Count != 3 || fi["crash_loop"].Count != 1 || !strings.Contains(fi["crash_loop"].Detail, "3 crash restarts") {
		t.Errorf("fi1 reasons = %+v, want 3 crashes and a loop", fi)
	}
	msk := reasons["msk1"]
	if msk["host_reboot"].Count != 1 || msk["agent_crash"].Count != 1 || msk["crash_loop"].Count != 0 || msk["path_loss"].Count != 0 {
		t.Errorf("msk1 reasons = %+v", msk)
	}
	if nl := reasons["nl1"]["path_loss"]; nl.Count != 1 || !strings.Contains(nl.Detail, "to de1") || !strings.Contains(nl.Detail, "50%") {
		t.Errorf("nl1 path_loss = %+v", nl)
	}
	if _, bad := reasons["de1"]["agent_crash"]; bad {
		t.Error("a clean restart counted as a crash")
	}
}
