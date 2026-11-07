//go:build !js

package mcp

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestNodesGetBatch(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	all := decode[NodesGetV](t, mustOK(t, s, "nodes_get", map[string]any{}))
	if len(all.Nodes) != 2 || all.Nodes[0].Name != "de1" || all.Nodes[1].Name != "nl1" {
		t.Fatalf("all nodes: %+v", all.Nodes)
	}
	some := decode[NodesGetV](t, mustOK(t, s, "nodes_get", map[string]any{"nodes": []string{"nl1", nodeB, "nl1"}}))
	if len(some.Nodes) != 1 || some.Nodes[0].ID != nodeB {
		t.Fatalf("a duplicate reference is one node: %+v", some.Nodes)
	}
	// one ListNodes for the whole call, not one per node
	if n := len(e.w.calls("/mistgate.admin.v1.NodeService/ListNodes")); n != 2 {
		t.Errorf("ListNodes calls: %d, want 2 (one per nodes_get)", n)
	}
	for _, c := range []map[string]any{{"nodes": []string{"nope"}}, {"nodes": []string{""}}} {
		mustFail(t, s, "nodes_get", c)
	}
	out := mustOK(t, s, "nodes_get", map[string]any{})
	noCanary(t, "nodes_get", out)
}

func TestFleetWideReads(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)

	ports := decode[FleetPortsV](t, mustOK(t, s, "node_ports", map[string]any{}))
	if len(ports.Nodes) != 2 || ports.Nodes[0].NodeName != "de1" || len(ports.Nodes[0].Ports) != 1 || ports.Nodes[0].Ports[0].Port != 443 ||
		len(ports.Nodes[0].TCP) != 2 || ports.Nodes[0].UDPFilteredUnix == 0 {
		t.Fatalf("ports: %+v", ports)
	}
	if len(e.w.checkPortsReq) != 0 {
		t.Error("the fleet-wide read contacted a node")
	}
	mustFail(t, s, "node_ports", map[string]any{"check": true})

	metrics := decode[FleetMetricsV](t, mustOK(t, s, "node_metrics", map[string]any{}))
	if len(metrics.Nodes) != 2 || metrics.Nodes[0].Name != "de1" || metrics.Nodes[0].CPUPct != 12 || metrics.Nodes[0].RAMTotal != 2<<30 || metrics.Nodes[0].OnlineTotal != 15 {
		t.Fatalf("metrics: %+v", metrics)
	}
	warp := decode[FleetWarpV](t, mustOK(t, s, "warp_status", map[string]any{}))
	if len(warp.Nodes) != 2 || !warp.Nodes[0].HasAccount || warp.Nodes[0].Colo != "DE" || warp.Nodes[1].HasAccount {
		t.Fatalf("warp: %+v", warp)
	}
	// ListNodes once per call: three fleet reads, three listings
	if n := len(e.w.calls("/mistgate.admin.v1.NodeService/ListNodes")); n != 3 {
		t.Errorf("ListNodes calls: %d", n)
	}
	for _, tool := range []string{"node_ports", "node_metrics", "warp_status"} {
		out := mustOK(t, s, tool, map[string]any{})
		noCanary(t, tool, out)
		if len(out) > maxResultBytes {
			t.Errorf("%s: %d bytes", tool, len(out))
		}
	}
	// a node still answers the old way
	one := decode[NodePortsV](t, mustOK(t, s, "node_ports", map[string]any{"node": "de1"}))
	if one.NodeName != "de1" || len(one.Ports) != 1 || len(one.Ports[0].Inbounds) != 1 {
		t.Fatalf("one node: %+v", one)
	}
}

func TestLiveNodesDropsRetired(t *testing.T) {
	got := liveNodes([]*adminv1.Node{{Id: "a", Status: adminv1.NodeStatus_NODE_STATUS_ONLINE}, {Id: "b", Status: adminv1.NodeStatus_NODE_STATUS_RETIRED}})
	if len(got) != 1 || got[0].Id != "a" {
		t.Fatalf("%+v", got)
	}
	if _, err := resolveNode([]*adminv1.Node{{Id: "a", Name: "x"}, {Id: "b", Name: "x"}}, "x"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("ambiguous name: %v", err)
	}
}

func TestFleetViewsFitTheCap(t *testing.T) {
	v := &FleetMetricsV{}
	for i := 0; i < 400; i++ {
		v.Nodes = append(v.Nodes, NodeMetricsRowV{ID: nodeA, Name: strings.Repeat("n", 100), Status: "online"})
	}
	res, _, err := result(v)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	if len(text) > maxResultBytes || !strings.Contains(text, "truncated") {
		t.Errorf("%d bytes, truncated flag missing", len(text))
	}
}

func TestChecksRunNow(t *testing.T) {
	e := newTestEnv(t)
	_, ro := e.token(ProfileReadonly)
	if names := toolNames(t, e.session(ro)); slices.Contains(names, "checks_run_now") {
		t.Error("a readonly token sees checks_run_now")
	}
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)

	before := decode[ChecksV](t, mustOK(t, s, "checks_results", map[string]any{}))
	if before.Rows[0].Cells[0].Stats24h == nil || before.Rows[0].Cells[0].Stats24h.JitterMs != 22 || before.Rows[0].Cells[0].InboundID != "inb_1" {
		t.Fatalf("checks_results: stats and inbound_id: %+v", before.Rows[0].Cells[0])
	}

	v := decode[ChecksRunV](t, mustOK(t, s, "checks_run_now", map[string]any{"node": "de1"}))
	if v.Scheduled != 1 || v.Skipped != 1 || !v.Complete || len(v.Rows) != 1 || v.Rows[0].Cells[0].AtUnix != 1<<40 || v.Rows[0].Cells[0].InboundID != "inb_1" {
		t.Fatalf("run now: %+v", v)
	}
	if len(e.w.runChecksReqs) != 1 || e.w.runChecksReqs[0].NodeId != nodeA {
		t.Errorf("RunChecksNow requests: %+v", e.w.runChecksReqs)
	}
	// an inbound alone: its node is found in the matrix
	v = decode[ChecksRunV](t, mustOK(t, s, "checks_run_now", map[string]any{"inbound": "inb_1"}))
	if !v.Complete || len(v.Rows) != 1 || e.w.runChecksReqs[1].NodeId != nodeA {
		t.Fatalf("by inbound: %+v / %+v", v, e.w.runChecksReqs)
	}
	mustFail(t, s, "checks_run_now", map[string]any{"inbound": "inb_nope"})
	mustFail(t, s, "checks_run_now", map[string]any{"node": "nope"})
	noCanary(t, "checks_run_now", mustOK(t, s, "checks_run_now", map[string]any{}))
}

func TestChecksRunNowRateLimited(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	e.w.checksRunErr = connect.NewError(connect.CodeResourceExhausted, errors.New("checks ran a moment ago: try again in 20 s"))
	out := mustFail(t, s, "checks_run_now", map[string]any{"node": "de1"})
	if !strings.Contains(out, "30 seconds") || !strings.Contains(out, "checks_results") {
		t.Errorf("the refusal is not explained: %s", out)
	}
}

func TestChecksRunNowIncomplete(t *testing.T) {
	oldWait, oldPoll := checksWait, checksPoll
	checksWait, checksPoll = 50*time.Millisecond, 10*time.Millisecond
	defer func() { checksWait, checksPoll = oldWait, oldPoll }()
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	// the panel schedules a round but the cell never turns fresh: the stored matrix comes back, not complete
	e.w.holdChecks = true
	v := decode[ChecksRunV](t, mustOK(t, s, "checks_run_now", map[string]any{"node": "de1"}))
	if v.Complete || len(v.Rows) != 1 || v.Rows[0].Cells[0].Status != "ok" {
		t.Fatalf("incomplete: %+v", v)
	}
}

func TestNodeDoctorRefreshFleet(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	e.w.doctorRun = []*adminv1.NodeDoctor{
		{NodeId: nodeA, NodeName: "de1", HasReport: true, ReceivedUnix: 1 << 40},
		{NodeId: nodeB, NodeName: "nl1", HasReport: true, ReceivedUnix: 1700000000},
	}
	v := decode[struct {
		Fresh          bool     `json:"fresh"`
		Refreshed      int      `json:"refreshed"`
		KeptLastReport []string `json:"kept_last_report"`
		Nodes          []any    `json:"nodes"`
	}](t, mustOK(t, s, "node_doctor", map[string]any{"refresh": true}))
	if !v.Fresh || v.Refreshed != 1 || len(v.KeptLastReport) != 1 || v.KeptLastReport[0] != "nl1" || len(v.Nodes) != 2 {
		t.Fatalf("fleet refresh: %+v", v)
	}
	// the read-only profile has no refresh at all
	_, ro := e.token(ProfileReadonly)
	if out := mustOK(t, e.session(ro), "node_doctor", map[string]any{}); strings.Contains(out, "kept_last_report") {
		t.Errorf("readonly doctor: %s", out)
	}
}

func TestAlertsListFilters(t *testing.T) {
	e := newTestEnv(t)
	e.w.extraAlerts = []*adminv1.Alert{
		{Id: "alr_2", Severity: adminv1.AlertSeverity_ALERT_SEVERITY_WARNING, Kind: adminv1.AlertKind_ALERT_KIND_PORT_LOSSY, NodeId: nodeA, NodeName: "de1"},
		{Id: "alr_3", Severity: adminv1.AlertSeverity_ALERT_SEVERITY_INFO, Kind: adminv1.AlertKind_ALERT_KIND_PORT_LOSSY, NodeId: nodeA, NodeName: "de1"},
	}
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	ids := func(args map[string]any) string {
		v := decode[AlertsV](t, mustOK(t, s, "alerts_list", args))
		var out []string
		for _, a := range v.Active {
			out = append(out, a.ID)
		}
		return strings.Join(out, " ")
	}
	if got := ids(map[string]any{}); got != "alr_1 alr_2 alr_3" {
		t.Errorf("all: %s", got)
	}
	if got := ids(map[string]any{"min_severity": "warning"}); got != "alr_1 alr_2" {
		t.Errorf("warning: %s", got)
	}
	if got := ids(map[string]any{"min_severity": "critical"}); got != "alr_1" {
		t.Errorf("critical: %s", got)
	}
	if got := ids(map[string]any{"kind": "port_lossy"}); got != "alr_2 alr_3" {
		t.Errorf("kind: %s", got)
	}
	if got := ids(map[string]any{"kind": "PORT_LOSSY", "min_severity": "warning"}); got != "alr_2" {
		t.Errorf("kind and severity: %s", got)
	}
}

func TestEventsSearchFamily(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	mustOK(t, s, "events_search", map[string]any{"family": "profiles"})
	mustOK(t, s, "events_search", map[string]any{"family": "agent", "hours": 24})
	got := e.w.eventReqs
	if len(got) < 2 || got[0].Family != "profiles" || got[len(got)-1].Family != "agent" {
		t.Fatalf("family reached the panel as: %+v", got)
	}
}

func TestAuditSearchFiltersGoToThePanel(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	mustOK(t, s, "audit_search", map[string]any{"actor": "tok_x", "action": "mcp_", "hours": 48, "failed_only": true, "limit": 5})
	r := e.w.auditReqs[len(e.w.auditReqs)-1]
	if r.ActorContains != "tok_x" || r.ActionContains != "mcp_" || r.Hours != 48 || !r.FailedOnly || r.PageSize != 5 {
		t.Fatalf("request: %+v", r)
	}
	// nothing is thinned afterwards: the fake returns an entry that does not match, and it is shown
	out := mustOK(t, s, "audit_search", map[string]any{"actor": "nobody", "action": "nothing"})
	if !strings.Contains(out, "mcp_plan") {
		t.Errorf("the page was filtered again after the panel's answer: %s", out)
	}
	mustFail(t, s, "audit_search", map[string]any{"hours": 9000})
}

func TestChecksResultsDaily(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	plain := decode[ChecksV](t, mustOK(t, s, "checks_results", map[string]any{}))
	if len(plain.Rows[0].Cells[0].Daily) != 0 {
		t.Error("daily without daily_days")
	}
	v := decode[ChecksV](t, mustOK(t, s, "checks_results", map[string]any{"daily_days": 7}))
	d := v.Rows[0].Cells[0].Daily
	if len(d) != 1 || d[0].ConnectP95Ms != 2800 || d[0].ResponseP50Ms != 85 || d[0].JitterMs != 25 || d[0].Failed != 5 {
		t.Fatalf("daily: %+v", d)
	}
	if got := e.w.getChecksReqs[len(e.w.getChecksReqs)-1].DailyDays; got != 7 {
		t.Errorf("daily_days reached the panel as %d", got)
	}
}
