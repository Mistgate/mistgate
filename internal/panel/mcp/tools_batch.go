//go:build !js

package mcp

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// Whole-fleet reads and the on-demand check. An agent that wants "how is every node" should make one call, not one per
// node: these resolve the node list once (nodeRef lists on every call) and return compact rows.

// maxFleetNodes caps the nodes one fleet-wide read walks; more are reported as truncated.
const maxFleetNodes = 50

// maxDoctorRefresh is the most nodes a fleet-wide node_doctor refresh will ask at once: the panel runs them in parallel and
// waits up to 30 s for each, so a larger fleet is refreshed node by node.
const maxDoctorRefresh = 24

var (
	// checksWait is how long checks_run_now waits for the scheduled rounds, and checksPoll how often it looks.
	checksWait = 25 * time.Second
	checksPoll = time.Second
)

type nodeOptArg struct {
	Node string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node (one compact row each)"`
}

type nodePortsOptArg struct {
	Node  string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node (compact rows, stored results only)"`
	Check bool   `json:"check,omitempty" jsonschema:"true: run a UDP delivery check now (needs a node); false: show the stored results"`
}

type nodesGetArg struct {
	Nodes []string `json:"nodes,omitempty" jsonschema:"node ids or exact names; empty = every node that is not retired (at most 50)"`
}

type checksRunArg struct {
	Node    string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node"`
	Inbound string `json:"inbound,omitempty" jsonschema:"an inbound id (inbound_id in checks_results): only that inbound's result is returned"`
}

func batchTools() []toolDef {
	return []toolDef{
		readTool("nodes_get", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure),
			"node_get for many nodes in one call: nodes is a list of node ids or exact names, empty = every node that is not retired (at most 50). Each entry is the node_get view (status and reason, host facts, inbounds with their state, up to 10 online users, notes, ipv6); when the answer is too large the online users go first, then whole nodes, and truncated is set. No addresses, keys or certificate pins.",
			func(c *call, in nodesGetArg) (any, error) { return c.nodesGet(in.Nodes) }),

		readToolWithin(checksWait+20*time.Second, "checks_run_now", ProfileOperator,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.HealthServiceRunChecksNowProcedure, adminv1connect.HealthServiceGetChecksProcedure),
			"Run the health checks now instead of waiting for the next round, and return the fresh results in the checks_results shape (only the inbounds that were probed; each cell has its inbound_id). node: one node, empty = every node; inbound: only that inbound's result. "+
				"The panel runs one round per inbound at most every 30 seconds: asking again sooner is refused, then read checks_results instead. A round is the panel's own client connecting through the inbound like a person (it reads nothing from the node). "+
				"It waits up to 25 seconds for the rounds; complete=false means some had not finished, call checks_results a little later.",
			func(c *call, in checksRunArg) (any, error) { return c.checksRunNow(in) }),
	}
}

// listNodes is ListNodes once.
func (c *call) listNodes() ([]*adminv1.Node, error) {
	r, err := c.cl.Node.ListNodes(c.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg.GetNodes(), nil
}

// resolveNode finds a node by id or by exact name in an already listed set.
func resolveNode(all []*adminv1.Node, ref string) (*adminv1.Node, error) {
	if ref == "" || len(ref) > 200 {
		return nil, errors.New("node: give a node id or its exact name")
	}
	var byName []*adminv1.Node
	for _, n := range all {
		if n.GetId() == ref {
			return n, nil
		}
		if n.GetName() == ref {
			byName = append(byName, n)
		}
	}
	switch len(byName) {
	case 0:
		return nil, errors.New("node not found")
	case 1:
		return byName[0], nil
	}
	return nil, errors.New("several nodes have this name: use the node id")
}

// liveNodes drops the retired nodes.
func liveNodes(all []*adminv1.Node) []*adminv1.Node {
	var out []*adminv1.Node
	for _, n := range all {
		if n.GetStatus() != adminv1.NodeStatus_NODE_STATUS_RETIRED {
			out = append(out, n)
		}
	}
	return out
}

// fleetNodes is the nodes a fleet-wide read covers: the live ones, at most maxFleetNodes.
func (c *call) fleetNodes() (nodes []*adminv1.Node, truncated bool, err error) {
	all, err := c.listNodes()
	if err != nil {
		return nil, false, err
	}
	nodes = liveNodes(all)
	if len(nodes) > maxFleetNodes {
		nodes, truncated = nodes[:maxFleetNodes], true
	}
	return nodes, truncated, nil
}

// skippable says a failure of one node must not sink a fleet-wide answer: that node is listed in unavailable instead. A
// refused token or an expired deadline ends the whole call.
func skippable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeResourceExhausted, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return false
	}
	return true
}

// ---------------------------------------------------------------------------------------------------------------------
// nodes_get

// NodesGetV is the nodes_get result.
type NodesGetV struct {
	Nodes     []*NodeGetV `json:"nodes"`
	Truncated bool        `json:"truncated,omitempty"`
}

func (v *NodesGetV) shrink() bool {
	v.Truncated = true
	dropped := false
	for _, n := range v.Nodes {
		if len(n.OnlineUsers) > 0 {
			n.OnlineUsers, n.Truncated, dropped = nil, true, true
		}
	}
	return dropped || cut(&v.Nodes)
}

func (c *call) nodesGet(refs []string) (any, error) {
	all, err := c.listNodes()
	if err != nil {
		return nil, err
	}
	var picked []*adminv1.Node
	if len(refs) == 0 {
		picked = liveNodes(all)
	} else {
		if len(refs) > maxFleetNodes {
			return nil, errors.New("nodes: at most 50")
		}
		for _, ref := range refs {
			n, err := resolveNode(all, ref)
			if err != nil {
				return nil, err
			}
			if !slices.ContainsFunc(picked, func(p *adminv1.Node) bool { return p.GetId() == n.GetId() }) {
				picked = append(picked, n)
			}
		}
	}
	v := &NodesGetV{Nodes: []*NodeGetV{}}
	for i, n := range picked {
		if i == maxFleetNodes {
			v.Truncated = true
			break
		}
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: n.GetId()}))
		if err != nil {
			return nil, apiError(err)
		}
		v.Nodes = append(v.Nodes, nodeGetView(r.Msg))
	}
	return v, nil
}

// ---------------------------------------------------------------------------------------------------------------------
// node_ports, node_metrics and warp_status without a node

type portBriefV struct {
	Port           uint32 `json:"port"`
	Verdict        string `json:"verdict,omitempty"`
	Sent           uint32 `json:"sent"`
	Got            uint32 `json:"got"`
	Checked        int64  `json:"checked_unix,omitempty"`
	ProtocolOKUnix int64  `json:"protocol_ok_unix,omitempty"`
	Blame          string `json:"blame,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type tcpBriefV struct {
	Port    uint32 `json:"port"`
	Role    string `json:"role,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type nodePortsRowV struct {
	NodeID          string       `json:"node_id"`
	NodeName        string       `json:"node_name"`
	Status          string       `json:"status"`
	UDPFilteredUnix int64        `json:"udp_filtered_unix,omitempty"`
	Ports           []portBriefV `json:"ports"`
	TCP             []tcpBriefV  `json:"tcp,omitempty"`
}

// FleetPortsV is node_ports without a node: the stored port checks of every node, compact.
type FleetPortsV struct {
	Nodes       []nodePortsRowV `json:"nodes"`
	Unavailable []string        `json:"unavailable,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
}

func (v *FleetPortsV) shrink() bool {
	v.Truncated = true
	return cut(&v.Nodes)
}

// NodeMetricsRowV is one node of node_metrics without a node: the gauges, no top users.
type NodeMetricsRowV struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	HasMetrics   bool    `json:"has_metrics"`
	AtUnix       int64   `json:"at_unix,omitempty"`
	CPUPct       float32 `json:"cpu_pct"`
	SoftIRQPct   float32 `json:"softirq_pct"`
	Load1        float32 `json:"load1"`
	RAMUsed      uint64  `json:"ram_used_bytes"`
	RAMTotal     uint64  `json:"ram_total_bytes"`
	DiskUsed     uint64  `json:"disk_used_bytes"`
	DiskTotal    uint64  `json:"disk_total_bytes"`
	NetRxBps     uint64  `json:"net_rx_bps"`
	NetTxBps     uint64  `json:"net_tx_bps"`
	TrafficToday uint64  `json:"traffic_today_bytes"`
	OnlineTotal  uint32  `json:"online_total"`
}

// FleetMetricsV is node_metrics without a node.
type FleetMetricsV struct {
	Nodes       []NodeMetricsRowV `json:"nodes"`
	Unavailable []string          `json:"unavailable,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
}

func (v *FleetMetricsV) shrink() bool {
	v.Truncated = true
	return cut(&v.Nodes)
}

// FleetWarpV is warp_status without a node.
type FleetWarpV struct {
	Nodes       []*WarpStatusV `json:"nodes"`
	Unavailable []string       `json:"unavailable,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
}

func (v *FleetWarpV) shrink() bool {
	v.Truncated = true
	return cut(&v.Nodes)
}

func (c *call) fleetPorts() (any, error) {
	nodes, trunc, err := c.fleetNodes()
	if err != nil {
		return nil, err
	}
	v := &FleetPortsV{Nodes: []nodePortsRowV{}, Truncated: trunc}
	for _, n := range nodes {
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: n.GetId()}))
		if err != nil {
			if skippable(c.ctx, err) {
				v.Unavailable = append(v.Unavailable, nm(n.GetName()))
				continue
			}
			return nil, apiError(err)
		}
		row := nodePortsRowV{NodeID: n.GetId(), NodeName: nm(n.GetName()), Status: nodeStatus(r.Msg.GetNode().GetStatus()),
			UDPFilteredUnix: r.Msg.GetUdpFilteredUnix(), Ports: []portBriefV{}}
		for _, p := range r.Msg.GetPortChecks() {
			if p == nil {
				continue
			}
			row.Ports = append(row.Ports, portBriefV{Port: p.GetPort(), Verdict: clean(p.GetVerdict(), 16), Sent: p.GetSent(), Got: p.GetGot(),
				Checked: p.GetCheckedUnix(), ProtocolOKUnix: p.GetProtocolOkUnix(), Blame: clean(p.GetBlame(), 16), Reason: clean(p.GetReason(), 40)})
		}
		for _, t := range r.Msg.GetTcpChecks() {
			if t != nil {
				row.TCP = append(row.TCP, tcpBriefV{Port: t.GetPort(), Role: clean(t.GetRole(), 16), Verdict: clean(t.GetVerdict(), 16), Reason: clean(t.GetReason(), 40)})
			}
		}
		v.Nodes = append(v.Nodes, row)
	}
	return v, nil
}

func (c *call) fleetMetrics() (any, error) {
	nodes, trunc, err := c.fleetNodes()
	if err != nil {
		return nil, err
	}
	v := &FleetMetricsV{Nodes: []NodeMetricsRowV{}, Truncated: trunc}
	for _, n := range nodes {
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: n.GetId()}))
		if err != nil {
			if skippable(c.ctx, err) {
				v.Unavailable = append(v.Unavailable, nm(n.GetName()))
				continue
			}
			return nil, apiError(err)
		}
		m := nodeMetricsView(r.Msg)
		v.Nodes = append(v.Nodes, NodeMetricsRowV{ID: m.ID, Name: m.Name, Status: m.Status, HasMetrics: m.HasMetrics, AtUnix: m.AtUnix, CPUPct: m.CPUPct,
			SoftIRQPct: m.SoftIRQPct, Load1: m.Load1, RAMUsed: m.RAMUsed, RAMTotal: m.RAMTotal, DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal,
			NetRxBps: m.NetRxBps, NetTxBps: m.NetTxBps, TrafficToday: m.TrafficToday, OnlineTotal: sumUsers(r.Msg.GetNode().GetOnline())})
	}
	return v, nil
}

func (c *call) fleetWarp() (any, error) {
	nodes, trunc, err := c.fleetNodes()
	if err != nil {
		return nil, err
	}
	v := &FleetWarpV{Nodes: []*WarpStatusV{}, Truncated: trunc}
	for _, n := range nodes {
		r, err := c.cl.Warp.GetWarp(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.GetWarpRequest{NodeId: n.GetId()}))
		if err != nil {
			if skippable(c.ctx, err) {
				v.Unavailable = append(v.Unavailable, nm(n.GetName()))
				continue
			}
			return nil, apiError(err)
		}
		v.Nodes = append(v.Nodes, warpStatusView(n.GetId(), n.GetName(), r.Msg, c.e.now()))
	}
	return v, nil
}

// ---------------------------------------------------------------------------------------------------------------------
// node_doctor refresh for the whole fleet

// DoctorRefreshV is node_doctor with refresh=true and no node: the reports after the nodes ran their checks, and the nodes
// that kept their last report because they are offline or their agent cannot run the doctor.
type DoctorRefreshV struct {
	*DoctorV
	Refreshed      int      `json:"refreshed"`
	KeptLastReport []string `json:"kept_last_report,omitempty"`
}

func (c *call) doctorFleetRefresh() (any, error) {
	nodes, err := c.listNodes()
	if err != nil {
		return nil, err
	}
	if n := len(liveNodes(nodes)); n > maxDoctorRefresh {
		return nil, errors.New("too many nodes to refresh at once (" + strconv.Itoa(n) + ", at most " + strconv.Itoa(maxDoctorRefresh) + "): refresh them one by one with node")
	}
	started := c.e.now().Unix() - 1
	r, err := c.cl.Health.RunDoctor(c.ctx, connect.NewRequest(&adminv1.RunDoctorRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	v := &DoctorRefreshV{DoctorV: doctorView(r.Msg.GetNodes(), true)}
	for _, d := range r.Msg.GetNodes() {
		if d.GetReceivedUnix() >= started {
			v.Refreshed++
		} else {
			v.KeptLastReport = append(v.KeptLastReport, nm(d.GetNodeName()))
		}
	}
	return v, nil
}

// ---------------------------------------------------------------------------------------------------------------------
// checks_run_now

// ChecksRunV is the checks_run_now result: the checks_results shape for the probed inbounds.
type ChecksRunV struct {
	Scheduled uint32 `json:"scheduled"`
	Skipped   uint32 `json:"skipped"`
	// Complete is false when the wait ended before every scheduled round had a fresh result.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
	*ChecksV
}

func (c *call) checksRunNow(in checksRunArg) (any, error) {
	nodeID := ""
	if in.Node != "" {
		id, _, err := c.nodeRef(in.Node)
		if err != nil {
			return nil, err
		}
		nodeID = id
	}
	if in.Inbound != "" {
		if err := validID("inbound", in.Inbound); err != nil {
			return nil, err
		}
		if nodeID == "" { // the node of that inbound, from the matrix
			r, err := c.cl.Health.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
			if err != nil {
				return nil, apiError(err)
			}
			for _, row := range r.Msg.GetRows() {
				if slices.ContainsFunc(row.GetCells(), func(cell *adminv1.CheckCell) bool { return cell.GetInboundId() == in.Inbound }) {
					nodeID = row.GetNodeId()
				}
			}
			if nodeID == "" {
				return nil, errors.New("inbound not found")
			}
		}
	}
	started := c.e.now().Unix() - 1
	run, err := c.cl.Health.RunChecksNow(c.ctx, connect.NewRequest(&adminv1.RunChecksNowRequest{NodeId: nodeID}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return nil, failure("checks_recent", "the checks of this node ran less than 30 seconds ago (the panel runs one round per inbound at most every 30 s): "+
				"read the results with checks_results, or ask again in a few seconds")
		}
		return nil, apiError(err)
	}
	want := int(run.Msg.GetScheduled())
	if in.Inbound != "" {
		want = min(want, 1)
	}
	deadline := time.Now().Add(checksWait)
	for {
		r, err := c.cl.Health.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{NodeId: nodeID}))
		if err != nil {
			return nil, apiError(err)
		}
		fresh := keepFresh(r.Msg, started, in.Inbound)
		if fresh >= want || want == 0 || !time.Now().Before(deadline) {
			v := &ChecksRunV{Scheduled: run.Msg.GetScheduled(), Skipped: run.Msg.GetSkipped(), Complete: fresh >= want, ChecksV: checksView(r.Msg)}
			if want == 0 {
				v.Note = "nothing could be probed (the node is offline or updating, the inbound is disabled, or its protocol has no client); these are the stored results"
			}
			return v, nil
		}
		select {
		case <-c.ctx.Done():
			return nil, apiError(c.ctx.Err())
		case <-time.After(checksPoll):
		}
	}
}

// keepFresh narrows r to the cells probed since started (and, with inbound, only that inbound's): the others become empty
// cells so the matrix keeps its shape, and rows without a fresh cell are dropped. It returns how many fresh cells there are;
// with none, r is left as it is (the stored results).
func keepFresh(r *adminv1.GetChecksResponse, started int64, inbound string) int {
	isFresh := func(cell *adminv1.CheckCell) bool {
		l := cell.GetLast()
		return cell.GetDeployed() && l != nil && l.GetStatus() != adminv1.CheckStatus_CHECK_STATUS_SKIPPED && l.GetAtUnix() >= started &&
			(inbound == "" || cell.GetInboundId() == inbound)
	}
	fresh := 0
	for _, row := range r.GetRows() {
		for _, cell := range row.GetCells() {
			if isFresh(cell) {
				fresh++
			}
		}
	}
	if fresh == 0 {
		return 0
	}
	var rows []*adminv1.CheckRow
	for _, row := range r.GetRows() {
		touched := false
		for i, cell := range row.GetCells() {
			if isFresh(cell) {
				touched = true
			} else {
				row.Cells[i] = &adminv1.CheckCell{}
			}
		}
		if touched {
			rows = append(rows, row)
		}
	}
	r.Rows = rows
	return fresh
}
