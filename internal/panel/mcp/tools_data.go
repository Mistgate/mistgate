//go:build !js

package mcp

import (
	"errors"
	"sort"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/logscrub"
)

// Read tools that show an agent what the panel stores about a node instead of letting it guess: what a node runs against
// what the panel wants (node_runtime), which UDP paths between nodes lose packets (path_loss_matrix) and, through
// node_metrics, how loaded a host was over the last hours. Names, ids, hashes cut to 12 characters, states and counts only:
// no address, certificate pin or key reaches a result.

func dataTools() []toolDef {
	return []toolDef{
		readTool("node_runtime", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeRuntimeProcedure),
			"What a node runs against what the panel wants it to run, for one node or for every node (node empty). "+
				"Per node: desired_revision/desired_hash (the state the panel computed and sent) against applied_revision/applied_hash (the state the node reports it runs, hashed from what its engines actually run); "+
				"in_sync is true only when the two hashes are equal and non-empty and the node is not drifting; drift/drift_since_unix say the node twice applied a state other than the one sent; "+
				"last_apply_unix/status say when the node last confirmed an apply (applied, partial) or a Hello that already matched (in_sync). "+
				"Per inbound: enabled (true = part of the desired state), spec_version (the panel's), applied_spec_hash (what the node reported), applied_state (active, failed, pending, disabled), live_state (what the node's stats say right now; empty = not listed), last_error and cert_not_after_unix. "+
				"attention lists what does not line up, by kind: applied_not_desired (an inbound the panel no longer wants that the node still reports active: what an orphan looks like, check live_state before saying it still runs), "+
				"desired_not_applied (wanted, never confirmed by the node), failed, and unknown_running (the node lists an inbound id the panel has no record of). "+
				"Do not call an inbound an orphan unless it shows up here. The panel and node do not reveal addresses, certificate pins or keys. This never contacts a node.",
			func(c *call, in nodeOptArg) (any, error) {
				id := ""
				if in.Node != "" {
					var err error
					if id, _, err = c.nodeRef(in.Node); err != nil {
						return nil, err
					}
				}
				r, err := c.cl.Node.GetNodeRuntime(c.ctx, connect.NewRequest(&adminv1.GetNodeRuntimeRequest{NodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				return nodeRuntimeView(r.Msg), nil
			}),

		readTool("path_loss_matrix", ProfileReadonly, procs(adminv1connect.NodeServiceGetPathLossProcedure),
			"The UDP paths between your nodes that lost packets in a port check, so you can tell a bad node from a bad path. "+
				"Each row is a sender (from), the target it sent to (to), the ports of that run, how many packets were sent and got through, loss_pct, the second sender that reached the same target (other) and how many it got through, "+
				"and open_ports when some ports arrived whole while the lost ones got nothing (a filter on the way, not a lossy line). "+
				"targets summarises per target how many senders lose packets to it: one lossy sender means that sender's path, several mean look at the target itself. "+
				"Only paths where another sender got through are listed: a target that nobody reaches is a port problem, see node_ports. "+
				"hours is how far back the checks go (1 to 720, default 24); a path clean at its last check is not listed. Node names only, never addresses.",
			func(c *call, in pathLossArg) (any, error) {
				r, err := c.cl.Node.GetPathLoss(c.ctx, connect.NewRequest(&adminv1.GetPathLossRequest{Hours: hostHours(in.Hours)}))
				if err != nil {
					return nil, apiError(err)
				}
				return pathLossView(r.Msg), nil
			}),
	}
}

type pathLossArg struct {
	Hours int `json:"hours,omitempty" jsonschema:"only paths checked in the last hours, 1 to 720; empty = 24"`
}

type nodeMetricsArg struct {
	Node  string `json:"node,omitempty" jsonschema:"node id or its exact name; empty = every node (current gauges only)"`
	Hours int    `json:"hours,omitempty" jsonschema:"also return the host load per hour for this many hours back, 1 to 720, with the peaks; empty = only the current gauges"`
}

// hostHours clamps the hours argument of the history reads: 0 stays 0 (the server's default), the rest is 1 to 720.
func hostHours(h int) uint32 {
	if h <= 0 {
		return 0
	}
	return uint32(min(h, 720))
}

// ---------------------------------------------------------------------------------------------------------------------
// node_runtime

type inboundRuntimeV struct {
	ID          string `json:"inbound_id"`
	Profile     string `json:"profile"`
	Protocol    string `json:"protocol"`
	Enabled     bool   `json:"enabled"`
	SpecVersion int64  `json:"spec_version"`
	SpecHash    string `json:"applied_spec_hash,omitempty"`
	State       string `json:"applied_state"`
	LiveState   string `json:"live_state,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	CertUntil   int64  `json:"cert_not_after_unix,omitempty"`
}

type attentionV struct {
	Kind      string `json:"kind"`
	InboundID string `json:"inbound_id"`
	Profile   string `json:"profile,omitempty"`
}

type nodeRuntimeV struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	State           string            `json:"state"`
	Connected       bool              `json:"connected"`
	DesiredRevision uint64            `json:"desired_revision"`
	DesiredHash     string            `json:"desired_hash,omitempty"`
	AppliedRevision uint64            `json:"applied_revision"`
	AppliedHash     string            `json:"applied_hash,omitempty"`
	InSync          bool              `json:"in_sync"`
	Drift           bool              `json:"drift,omitempty"`
	DriftSince      int64             `json:"drift_since_unix,omitempty"`
	LastApplyUnix   int64             `json:"last_apply_unix,omitempty"`
	LastApplyStatus string            `json:"last_apply_status,omitempty"`
	LastApplyMS     uint32            `json:"last_apply_ms,omitempty"`
	Attention       []attentionV      `json:"attention,omitempty"`
	Inbounds        []inboundRuntimeV `json:"inbounds"`
}

// NodeRuntimeV is the node_runtime result.
type NodeRuntimeV struct {
	NowUnix   int64          `json:"now_unix"`
	Nodes     []nodeRuntimeV `json:"nodes"`
	Truncated bool           `json:"truncated,omitempty"`
}

func (v *NodeRuntimeV) shrink() bool {
	v.Truncated = true
	if cut(&v.Nodes) {
		return true
	}
	if len(v.Nodes) == 1 {
		return cut(&v.Nodes[0].Inbounds)
	}
	return false
}

func nodeRuntimeView(r *adminv1.GetNodeRuntimeResponse) *NodeRuntimeV {
	v := &NodeRuntimeV{NowUnix: r.GetNowUnix(), Nodes: []nodeRuntimeV{}}
	for _, n := range r.GetNodes() {
		nv := nodeRuntimeV{
			ID: clean(n.GetNodeId(), 80), Name: nm(n.GetNodeName()), State: clean(n.GetState(), 20), Connected: n.GetConnected(),
			DesiredRevision: n.GetDesiredRevision(), DesiredHash: clean(n.GetDesiredHash(), 12),
			AppliedRevision: n.GetAppliedRevision(), AppliedHash: clean(n.GetAppliedHash(), 12), InSync: n.GetInSync(),
			Drift: n.GetDrift(), DriftSince: n.GetDriftSinceUnix(), LastApplyUnix: n.GetLastApplyUnix(),
			LastApplyStatus: clean(n.GetLastApplyStatus(), 20), LastApplyMS: n.GetLastApplyMs(), Inbounds: []inboundRuntimeV{},
		}
		for _, in := range n.GetInbounds() {
			iv := inboundRuntimeV{
				ID: clean(in.GetInboundId(), 80), Profile: nm(in.GetProfileName()), Protocol: clean(in.GetProtocol(), 40),
				Enabled: in.GetEnabled(), SpecVersion: in.GetSpecVersion(), SpecHash: clean(in.GetAppliedSpecHash(), 12),
				State: clean(in.GetAppliedState(), 20), LiveState: clean(in.GetLiveState(), 20),
				LastError: clean(logscrub.Addrs(in.GetLastError()), 160), CertUntil: in.GetCertNotAfterUnix(),
			}
			nv.Inbounds = append(nv.Inbounds, iv)
			switch {
			case !iv.Enabled && iv.SpecHash != "" && (iv.State == "active" || iv.LiveState == "active"):
				nv.Attention = append(nv.Attention, attentionV{Kind: "applied_not_desired", InboundID: iv.ID, Profile: iv.Profile})
			case iv.Enabled && iv.SpecHash == "":
				nv.Attention = append(nv.Attention, attentionV{Kind: "desired_not_applied", InboundID: iv.ID, Profile: iv.Profile})
			case iv.Enabled && (iv.State == "failed" || iv.LiveState == "failed"):
				nv.Attention = append(nv.Attention, attentionV{Kind: "failed", InboundID: iv.ID, Profile: iv.Profile})
			}
		}
		for _, id := range n.GetUnknownRunningInboundIds() {
			nv.Attention = append(nv.Attention, attentionV{Kind: "unknown_running", InboundID: clean(id, 64)})
		}
		v.Nodes = append(v.Nodes, nv)
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// path_loss_matrix

type pathLossRowV struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Ports     string  `json:"ports"`
	Sent      uint32  `json:"sent"`
	Got       uint32  `json:"got"`
	LossPct   float32 `json:"loss_pct"`
	Other     string  `json:"other,omitempty"`
	OtherGot  uint32  `json:"other_got"`
	OpenPorts string  `json:"open_ports,omitempty"`
	AtUnix    int64   `json:"at_unix"`
}

type pathLossTargetV struct {
	Target       string  `json:"target"`
	LossySenders uint32  `json:"lossy_senders"`
	WorstLossPct float32 `json:"worst_loss_pct"`
}

// PathLossV is the path_loss_matrix result.
type PathLossV struct {
	SinceUnix int64             `json:"since_unix"`
	NowUnix   int64             `json:"now_unix"`
	Rows      []pathLossRowV    `json:"rows"`
	Targets   []pathLossTargetV `json:"targets"`
	Truncated bool              `json:"truncated,omitempty"`
}

func (v *PathLossV) shrink() bool {
	v.Truncated = true
	return cut(&v.Rows)
}

func pathLossView(r *adminv1.GetPathLossResponse) *PathLossV {
	v := &PathLossV{SinceUnix: r.GetSinceUnix(), NowUnix: r.GetNowUnix(), Rows: []pathLossRowV{}, Targets: []pathLossTargetV{}}
	for _, row := range r.GetRows() {
		v.Rows = append(v.Rows, pathLossRowV{
			From: nm(row.GetFromName()), To: nm(row.GetToName()), Ports: clean(row.GetPorts(), 80), Sent: row.GetSent(), Got: row.GetGot(),
			LossPct: row.GetLossPct(), Other: nm(row.GetOtherName()), OtherGot: row.GetOtherGot(), OpenPorts: clean(row.GetOpenPorts(), 80),
			AtUnix: row.GetCheckedUnix(),
		})
	}
	for _, t := range r.GetTargets() {
		v.Targets = append(v.Targets, pathLossTargetV{Target: nm(t.GetName()), LossySenders: t.GetLossySenders(), WorstLossPct: t.GetWorstLossPct()})
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// node_metrics with hours

// metricsHourV is one point of the host load series: one hour, or a bucket of several hours when the window is long.
type metricsHourV struct {
	AtUnix     int64   `json:"at_unix"`
	Samples    uint32  `json:"samples"`
	CPUAvg     float32 `json:"cpu_avg_pct"`
	CPUMax     float32 `json:"cpu_max_pct"`
	SoftIRQMax float32 `json:"softirq_max_pct"`
	RAMMax     uint64  `json:"ram_used_max_bytes"`
	RxAvg      uint64  `json:"rx_avg_bps"`
	RxMax      uint64  `json:"rx_max_bps"`
	TxAvg      uint64  `json:"tx_avg_bps"`
	TxMax      uint64  `json:"tx_max_bps"`
	SessionMax uint32  `json:"sessions_max"`
}

// metricsPeakV is the highest value of a gauge over the whole window and the hour it fell in.
type metricsPeakV struct {
	Value  float64 `json:"value"`
	AtUnix int64   `json:"at_unix"`
}

type metricsPeaksV struct {
	CPUPct     metricsPeakV `json:"cpu_pct"`
	SoftIRQPct metricsPeakV `json:"softirq_pct"`
	Load1      metricsPeakV `json:"load1"`
	RAMBytes   metricsPeakV `json:"ram_used_bytes"`
	RxBps      metricsPeakV `json:"rx_bps"`
	TxBps      metricsPeakV `json:"tx_bps"`
	Sessions   metricsPeakV `json:"sessions"`
}

// maxMetricsPoints keeps a 720-hour answer inside the result cap: longer windows are bucketed.
const maxMetricsPoints = 96

func (v *NodeMetricsV) shrink() bool {
	if len(v.Series) > 1 {
		v.Truncated = true
		cutOld(&v.Series)
		return true
	}
	if len(v.TopToday) > 1 {
		return cut(&v.TopToday)
	}
	return false
}

// addHostHistory puts the hourly series and the peaks of a GetNodeHostHistory answer into the node_metrics view.
func (v *NodeMetricsV) addHostHistory(r *adminv1.GetNodeHostHistoryResponse, hours int) {
	v.HistoryHours = hours
	hs := r.GetHours()
	v.Series = []metricsHourV{}
	if len(hs) == 0 {
		return
	}
	var peaks metricsPeaksV
	top := func(p *metricsPeakV, val float64, at int64) {
		if val > p.Value || p.AtUnix == 0 {
			*p = metricsPeakV{Value: val, AtUnix: at}
		}
	}
	for _, h := range hs {
		at := h.GetHourStartUnix()
		top(&peaks.CPUPct, float64(h.GetCpuMaxPct()), at)
		top(&peaks.SoftIRQPct, float64(h.GetSoftirqMaxPct()), at)
		top(&peaks.Load1, float64(h.GetLoad1Max()), at)
		top(&peaks.RAMBytes, float64(h.GetRamUsedMaxBytes()), at)
		top(&peaks.RxBps, float64(h.GetRxMaxBps()), at)
		top(&peaks.TxBps, float64(h.GetTxMaxBps()), at)
		top(&peaks.Sessions, float64(h.GetSessionsMax()), at)
	}
	v.Peaks = &peaks
	step := 1
	if len(hs) > maxMetricsPoints || hours > maxMetricsPoints {
		step = (hours + maxMetricsPoints - 1) / maxMetricsPoints
	}
	v.BucketHours = step
	// Group the hours by bucket of step hours counted back from the newest one.
	type acc struct {
		metricsHourV
		cpuSum, rxSum, txSum float64
	}
	buckets := map[int64]*acc{}
	var order []int64
	newest := hs[len(hs)-1].GetHourStartUnix()
	for _, h := range hs {
		idx := (newest - h.GetHourStartUnix()) / 3600 / int64(step)
		key := newest - idx*int64(step)*3600 - int64(step-1)*3600 // the bucket's first hour
		a := buckets[key]
		if a == nil {
			a = &acc{metricsHourV: metricsHourV{AtUnix: key}}
			buckets[key] = a
			order = append(order, key)
		}
		n := float64(h.GetSamples())
		a.Samples += h.GetSamples()
		a.cpuSum += float64(h.GetCpuAvgPct()) * n
		a.rxSum += float64(h.GetRxAvgBps()) * n
		a.txSum += float64(h.GetTxAvgBps()) * n
		a.CPUMax = max(a.CPUMax, h.GetCpuMaxPct())
		a.SoftIRQMax = max(a.SoftIRQMax, h.GetSoftirqMaxPct())
		a.RAMMax = max(a.RAMMax, h.GetRamUsedMaxBytes())
		a.RxMax = max(a.RxMax, h.GetRxMaxBps())
		a.TxMax = max(a.TxMax, h.GetTxMaxBps())
		a.SessionMax = max(a.SessionMax, h.GetSessionsMax())
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, k := range order {
		a := buckets[k]
		if a.Samples > 0 {
			a.CPUAvg = float32(a.cpuSum / float64(a.Samples))
			a.RxAvg = uint64(a.rxSum / float64(a.Samples))
			a.TxAvg = uint64(a.txSum / float64(a.Samples))
		}
		v.Series = append(v.Series, a.metricsHourV)
	}
}

// nodeMetrics is node_metrics: the current gauges of GetNode and, when hours is set, the hourly host load.
func (c *call) nodeMetrics(in nodeMetricsArg) (any, error) {
	id, _, err := c.nodeRef(in.Node)
	if err != nil {
		return nil, err
	}
	if in.Hours < 0 || in.Hours > 720 {
		return nil, errors.New("hours: 1 to 720")
	}
	r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
	if err != nil {
		return nil, apiError(err)
	}
	v := nodeMetricsView(r.Msg)
	if in.Hours > 0 {
		h, err := c.cl.Node.GetNodeHostHistory(c.ctx, connect.NewRequest(&adminv1.GetNodeHostHistoryRequest{NodeId: id, Hours: uint32(in.Hours)}))
		if err != nil {
			return nil, apiError(err)
		}
		v.addHostHistory(h.Msg, in.Hours)
	}
	return v, nil
}
