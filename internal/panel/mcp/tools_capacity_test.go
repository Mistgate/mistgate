//go:build !js

package mcp

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
)

// Fakes of the two OpsService reads. de1 is overloaded with a target named in its recommendation; the recommendation's free
// params carry an address and a token that must not come out.

func (w *world) GetFleetLoad(_ context.Context, r *connect.Request[opsv1.GetFleetLoadRequest]) (*connect.Response[opsv1.GetFleetLoadResponse], error) {
	w.mu.Lock()
	w.loadHours = append(w.loadHours, r.Msg.GetHours())
	w.mu.Unlock()
	hours := r.Msg.GetHours()
	if hours == 0 {
		hours = 168
	}
	f := func(v float32) *float32 { return &v }
	u := func(v uint32) *uint32 { return &v }
	de1 := &opsv1.NodeLoad{NodeId: nodeA, NodeName: "de1", Connected: true, Cpus: 2, Status: "overloaded", Bottleneck: "cpu", HoursWithData: 168, ExtHours: 100,
		BusyHourUnix: 1699992000, BusyHourCpuPct: 86, PeopleBusyHour: 120, PeoplePeak: 140, CpuPeakP95Pct: 99, StealP95Pct: f(3), RamPeakPct: f(60),
		Fit: "ok", CpuPctPerHundredPeople: f(55), MbpsPerPerson: f(1.2), HeadroomPeople: u(0),
		Recommendations: []*opsv1.LoadRecommendation{
			{Code: "cpu_busy", Severity: "crit", Params: map[string]string{"cpu_pct": "86", "target": "nl1", "target_headroom": "80"}},
			{Code: "steal_high", Severity: "warn", Params: map[string]string{"steal_pct": "12", "note": "seen at " + canaryAddr + " " + canaryTK}},
		}}
	nl1 := &opsv1.NodeLoad{NodeId: nodeB, NodeName: "nl1", Connected: true, Status: "ok", HoursWithData: 168, BusyHourCpuPct: 20, Fit: "flat"}
	return connect.NewResponse(&opsv1.GetFleetLoadResponse{NowUnix: 1700000000, Hours: hours, Nodes: []*opsv1.NodeLoad{de1, nl1},
		Summary: &opsv1.FleetLoadSummary{Ok: 1, Overloaded: 1, HeadroomPeople: 80, HeadroomNodes: 2,
			MostLoaded: []*opsv1.LoadNodeRef{{NodeId: nodeA, NodeName: "de1", Status: "overloaded", BusyHourCpuPct: 86, HeadroomPeople: u(0)}},
			MostFree:   []*opsv1.LoadNodeRef{{NodeId: nodeB, NodeName: "nl1", Status: "ok", BusyHourCpuPct: 20, HeadroomPeople: u(80)}}}}), nil
}

func (w *world) GetPanelResources(context.Context, *connect.Request[opsv1.GetPanelResourcesRequest]) (*connect.Response[opsv1.GetPanelResourcesResponse], error) {
	return connect.NewResponse(&opsv1.GetPanelResourcesResponse{NowUnix: 1700000000,
		Process:         &opsv1.PanelProcess{CpuPct: 1.5, RssBytes: 90 << 20, GoTotalBytes: 60 << 20, GoHeapBytes: 30 << 20, Goroutines: 400, Threads: 12, OpenFds: 80, FdLimit: 65536, UptimeS: 3600},
		Host:            &opsv1.PanelHost{Cpus: 2, CpuBusyPct: 8, RamUsedBytes: 700 << 20, RamTotalBytes: 2 << 30, DiskUsedBytes: 5 << 30, DiskTotalBytes: 20 << 30},
		Database:        &opsv1.PanelDatabase{FileBytes: 64 << 20, WalBytes: 4 << 20, PageCount: 16384, FreelistCount: 100, PageSize: 4096, MeasuredUnix: 1699999990},
		AgentsConnected: 2, Nodes: 3, Rpc: &opsv1.PanelRpcLoad{WindowS: 300, Requests: 420, ServerErrors: 1, ClientErrors: 3, P95Ms: 38}}), nil
}

// metricsOf is GetNode's gauges; with withResources, de1's come from an agent that reports the resource fields.
func (w *world) metricsOf(nodeID string) *adminv1.NodeMetrics {
	m := &adminv1.NodeMetrics{CpuPct: 12, Load1: 0.5, RamUsedBytes: 1 << 30, RamTotalBytes: 2 << 30}
	w.mu.Lock()
	with := w.withResources
	w.mu.Unlock()
	if with && nodeID == nodeA {
		m.StealPct, m.IowaitPct, m.SwapUsedBytes, m.SwapTotalBytes, m.ConntrackCount, m.ConntrackMax = 7.5, 1, 1<<20, 1<<30, 900, 1000
		m.TcpSockets, m.UdpSockets, m.NicSpeedMbps = 120, 9, 1000
		m.Agent = &adminv1.ProcessUsage{CpuPct: 4.5, RssBytes: 80 << 20, GoTotalBytes: 50 << 20, GoHeapBytes: 20 << 20, Goroutines: 300, Threads: 10, OpenFds: 150, FdLimit: 65536}
	}
	return m
}

func TestCapacityReport(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	v := decode[CapacityV](t, mustOK(t, s, "capacity_report", map[string]any{}))
	if v.Hours != 168 || len(v.Nodes) != 2 || v.Summary.Overloaded != 1 || v.Summary.HeadroomPeople != 80 || len(v.Summary.MostFree) != 1 || v.Summary.MostFree[0].NodeName != "nl1" {
		t.Fatalf("fleet: %+v", v)
	}
	de1 := v.Nodes[0]
	if de1.NodeName != "de1" || de1.Status != "overloaded" || de1.Bottleneck != "cpu" || de1.HeadroomPeople == nil || *de1.HeadroomPeople != 0 ||
		de1.CPUPer100People == nil || *de1.CPUPer100People != 55 || len(de1.Recommendations) != 2 || de1.Recommendations[0].Params["target"] != "nl1" {
		t.Fatalf("de1: %+v", de1)
	}
	// unknown stays absent, not zero
	if nl1 := v.Nodes[1]; nl1.HeadroomPeople != nil || nl1.StealP95 != nil || nl1.CPUPer100People != nil || nl1.Fit != "flat" {
		t.Fatalf("nl1: %+v", nl1)
	}

	one := decode[CapacityV](t, mustOK(t, s, "capacity_report", map[string]any{"node": "nl1", "hours": 24}))
	if len(one.Nodes) != 1 || one.Nodes[0].NodeName != "nl1" || one.Hours != 24 || one.Summary.OK != 1 {
		t.Fatalf("one node: %+v", one)
	}
	if got := e.w.loadHours; len(got) != 2 || got[0] != 0 || got[1] != 24 {
		t.Fatalf("windows asked: %v", got)
	}
	for _, bad := range []map[string]any{{"hours": 721}, {"hours": -1}, {"node": "nowhere"}} {
		if _, isErr := callTool(t, s, "capacity_report", bad); !isErr {
			t.Errorf("%v accepted", bad)
		}
	}
	out := mustOK(t, s, "capacity_report", map[string]any{})
	noCanary(t, "capacity_report", out)
	if strings.Contains(out, canaryAddr) {
		t.Errorf("an address in the report: %s", out)
	}
}

func TestPanelResourcesTool(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	v := decode[PanelResourcesV](t, mustOK(t, s, "panel_resources", map[string]any{}))
	if v.Process.RSS != 90<<20 || v.Process.Goroutines != 400 || v.Process.FDLimit != 65536 || v.Host.CPUs != 2 || v.Database.FileBytes != 64<<20 ||
		v.Database.WALBytes != 4<<20 || v.AgentsConnected != 2 || v.Nodes != 3 || v.RPC.Requests != 420 || v.RPC.P95Ms != 38 || v.RPC.WindowS != 300 {
		t.Fatalf("panel_resources: %+v", v)
	}
}

// node_metrics carries the resource fields of an agent that sends them, and leaves them out (not zero) for one that does not.
func TestNodeMetricsResources(t *testing.T) {
	e := newTestEnv(t)
	e.w.mu.Lock()
	e.w.withResources = true
	e.w.mu.Unlock()
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	v := decode[NodeMetricsV](t, mustOK(t, s, "node_metrics", map[string]any{"node": "de1"}))
	r := v.Res
	if r == nil || r.StealPct != 7.5 || r.ConntrackFillPct != 90 || r.TCPSockets != 120 || r.NICSpeedMbps != 1000 || r.AgentCPUPct != 4.5 ||
		r.AgentRSS != 80<<20 || r.AgentFDs != 150 || r.AgentFDLimit != 65536 {
		t.Fatalf("resources: %+v", r)
	}
	if old := decode[NodeMetricsV](t, mustOK(t, s, "node_metrics", map[string]any{"node": "nl1"})); old.Res != nil {
		t.Fatalf("an old agent got resources: %+v", old.Res)
	}
	out := mustOK(t, s, "node_metrics", map[string]any{"node": "nl1"})
	if strings.Contains(out, "steal") || strings.Contains(out, "agent_cpu") {
		t.Fatalf("unknown fields written as zero: %s", out)
	}
	fleet := decode[FleetMetricsV](t, mustOK(t, s, "node_metrics", map[string]any{}))
	var de1, nl1 *NodeMetricsRowV
	for i := range fleet.Nodes {
		switch fleet.Nodes[i].Name {
		case "de1":
			de1 = &fleet.Nodes[i]
		case "nl1":
			nl1 = &fleet.Nodes[i]
		}
	}
	if de1 == nil || de1.StealPct == nil || *de1.StealPct != 7.5 || de1.ConntrackFillPct == nil || *de1.ConntrackFillPct != 90 ||
		de1.AgentCPUPct == nil || *de1.AgentCPUPct != 4.5 || de1.AgentRSS == nil || *de1.AgentRSS != 80<<20 {
		t.Fatalf("fleet row de1: %+v", de1)
	}
	if nl1 == nil || nl1.StealPct != nil || nl1.AgentRSS != nil || nl1.ConntrackFillPct != nil {
		t.Fatalf("fleet row nl1: %+v", nl1)
	}
}
