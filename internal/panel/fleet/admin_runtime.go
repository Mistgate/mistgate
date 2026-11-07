//go:build !js

package fleet

import (
	"context"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The reads behind the MCP tools node_runtime, node_metrics (with hours) and path_loss_matrix. Each is a plain read of rows
// the panel already keeps; none contacts a node, and none returns an address, a certificate pin or a key.

const (
	runtimeHashLen  = 12
	runtimeErrorLen = 160
	hostDefaultHrs  = 24
	pathDefaultHrs  = 24
)

func headHash(h string) string { return store.Clip(h, runtimeHashLen) }

// GetNodeRuntime puts the panel's desired state of a node next to what the node reported it applies.
func (s nodeService) GetNodeRuntime(ctx context.Context, req *connect.Request[adminv1.GetNodeRuntimeRequest]) (*connect.Response[adminv1.GetNodeRuntimeResponse], error) {
	f := s.f
	now := f.now().UTC()
	var nodes []store.NodeRow
	if id := req.Msg.NodeId; id != "" {
		n, err := f.st.Node(ctx, id)
		if errors.Is(err, store.ErrNotFound) || err == nil && n.State == "retired" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		if err != nil {
			return nil, internalErr(f.log.Error, "get node", err)
		}
		nodes = []store.NodeRow{n}
	} else {
		all, err := f.st.Nodes(ctx, false)
		if err != nil {
			return nil, internalErr(f.log.Error, "list nodes for runtime", err)
		}
		nodes = all
	}
	inbounds, err := f.st.NodeRuntimeInbounds(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, internalErr(f.log.Error, "node runtime inbounds", err)
	}
	metas, err := f.st.NodeApplyMetas(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, internalErr(f.log.Error, "node apply metadata", err)
	}
	lives, err := f.st.NodeLive(ctx, now, req.Msg.NodeId, true)
	if err != nil {
		return nil, internalErr(f.log.Error, "node live for runtime", err)
	}
	liveBy := make(map[string]store.NodeLiveRow, len(lives))
	for _, l := range lives {
		liveBy[l.NodeID] = l
	}
	inboundsBy := map[string][]store.RuntimeInbound{}
	for _, in := range inbounds {
		inboundsBy[in.NodeID] = append(inboundsBy[in.NodeID], in)
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	resp := &adminv1.GetNodeRuntimeResponse{NowUnix: now.Unix(), Nodes: []*adminv1.NodeRuntime{}}
	for _, n := range nodes {
		if n.State == "retired" {
			continue
		}
		live := liveBy[n.ID]
		meta := metas[n.ID]
		drift := live.Drift || !meta.DriftSince.IsZero()
		out := &adminv1.NodeRuntime{
			NodeId: n.ID, NodeName: n.Name, State: n.State, Connected: live.Connected,
			DesiredRevision: n.DesiredRevision, DesiredHash: headHash(n.DesiredHash),
			AppliedRevision: n.AppliedRevision, AppliedHash: headHash(n.AppliedHash),
			InSync: n.DesiredHash != "" && n.DesiredHash == n.AppliedHash && !drift,
			Drift:  drift, DriftSinceUnix: fleetUnix(meta.DriftSince),
			LastApplyUnix: fleetUnix(meta.AppliedAt), LastApplyStatus: meta.Status, LastApplyMs: meta.DurationMS,
		}
		liveState := map[string]string{}
		if live.Connected {
			for _, h := range decodeLiveView(live).Health {
				liveState[h.InboundID] = h.RunState
			}
		}
		known := map[string]bool{}
		for _, in := range inboundsBy[n.ID] {
			known[in.ID] = true
			out.Inbounds = append(out.Inbounds, &adminv1.InboundRuntime{
				InboundId: in.ID, ProfileName: in.ProfileName, Protocol: in.Protocol, Enabled: in.Enabled, SpecVersion: in.SpecVersion,
				AppliedSpecHash: headHash(in.AppliedSpecHash), AppliedState: in.State, LastError: store.Clip(logscrub.Addrs(in.LastError), runtimeErrorLen),
				CertNotAfterUnix: fleetUnix(in.CertNotAfter), LiveState: liveState[in.ID],
			})
		}
		for id, st := range liveState {
			if !known[id] && st != "" {
				out.UnknownRunningInboundIds = append(out.UnknownRunningInboundIds, store.Clip(id, 64))
			}
		}
		sort.Strings(out.UnknownRunningInboundIds)
		resp.Nodes = append(resp.Nodes, out)
	}
	return connect.NewResponse(resp), nil
}

// GetNodeHostHistory returns a node's hourly host load.
func (s nodeService) GetNodeHostHistory(ctx context.Context, req *connect.Request[adminv1.GetNodeHostHistoryRequest]) (*connect.Response[adminv1.GetNodeHostHistoryResponse], error) {
	f := s.f
	now := f.now().UTC()
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "get node", err)
	}
	hours := int(req.Msg.Hours)
	if hours == 0 {
		hours = hostDefaultHrs
	}
	hours = min(hours, store.HostMaxHours)
	rows, err := f.st.NodeHostHours(ctx, n.ID, now, hours)
	if err != nil {
		return nil, internalErr(f.log.Error, "node host hours", err)
	}
	resp := &adminv1.GetNodeHostHistoryResponse{NodeId: n.ID, NodeName: n.Name, NowUnix: now.Unix(),
		SinceUnix: now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour).Unix(), Hours: []*adminv1.HostHour{}}
	for _, h := range rows {
		resp.Hours = append(resp.Hours, &adminv1.HostHour{
			HourStartUnix: h.HourStart, Samples: h.Samples, CpuAvgPct: float32(h.CPUAvg), CpuMaxPct: float32(h.CPUMax),
			SoftirqMaxPct: float32(h.SoftirqMax), Load1Max: float32(h.LoadMax), RamUsedMaxBytes: h.RAMUsedMax, RamTotalBytes: h.RAMTotal,
			RxAvgBps: h.RxAvg, RxMaxBps: h.RxMax, TxAvgBps: h.TxAvg, TxMaxBps: h.TxMax, SessionsMax: h.SessionsMax,
			SessionsAvg: float32(h.SessionsAvg), ExtSamples: h.ExtSamples, StealAvgPct: float32(h.StealAvg), StealMaxPct: float32(h.StealMax),
			IowaitMaxPct: float32(h.IOWaitMax), SwapUsedMaxBytes: h.SwapUsedMax, ConntrackMax: h.ConntrackMax, ConntrackLimit: h.ConntrackLimit,
			TcpSocketsMax: h.TCPSocketsMax, NicSpeedMbps: h.NICSpeedMbps, AgentCpuAvgPct: float32(h.AgentCPUAvg), AgentCpuMaxPct: float32(h.AgentCPUMax),
			AgentRssMaxBytes: h.AgentRSSMax,
		})
	}
	return connect.NewResponse(resp), nil
}

// GetPathLoss lists the lossy paths the UDP port check found, with a summary per target.
func (s nodeService) GetPathLoss(ctx context.Context, req *connect.Request[adminv1.GetPathLossRequest]) (*connect.Response[adminv1.GetPathLossResponse], error) {
	f := s.f
	now := f.now().UTC()
	hours := int(req.Msg.Hours)
	if hours == 0 {
		hours = pathDefaultHrs
	}
	hours = min(hours, store.HostMaxHours)
	since := now.Add(-time.Duration(hours) * time.Hour)
	losses, err := f.st.UDPPathLosses(ctx, since)
	if err != nil {
		return nil, internalErr(f.log.Error, "udp path loss", err)
	}
	resp := &adminv1.GetPathLossResponse{SinceUnix: since.Unix(), NowUnix: now.Unix(), Rows: []*adminv1.PathLossRow{}, Targets: []*adminv1.PathLossTarget{}}
	byTarget := map[string]*adminv1.PathLossTarget{}
	for _, l := range losses {
		pct := float32(0)
		if l.Sent > 0 {
			pct = float32(100 * float64(l.Sent-min(l.Got, l.Sent)) / float64(l.Sent))
		}
		resp.Rows = append(resp.Rows, &adminv1.PathLossRow{
			FromNodeId: l.SenderID, FromName: l.SenderName, ToNodeId: l.TargetID, ToName: l.TargetName, Ports: l.Ports,
			Sent: l.Sent, Got: l.Got, LossPct: pct, OtherName: l.OtherSenderName, OtherGot: l.OtherGot, OpenPorts: l.OpenPorts,
			CheckedUnix: l.CheckedAt.Unix(),
		})
		t := byTarget[l.TargetID]
		if t == nil {
			t = &adminv1.PathLossTarget{NodeId: l.TargetID, Name: l.TargetName}
			byTarget[l.TargetID] = t
			resp.Targets = append(resp.Targets, t)
		}
		t.LossySenders++
		t.WorstLossPct = max(t.WorstLossPct, pct)
	}
	// Worst first: the largest number of lossy senders, then the heaviest loss, then the name.
	sort.SliceStable(resp.Rows, func(i, j int) bool {
		a, b := resp.Rows[i], resp.Rows[j]
		if a.LossPct != b.LossPct {
			return a.LossPct > b.LossPct
		}
		return a.FromName+a.ToName < b.FromName+b.ToName
	})
	sort.SliceStable(resp.Targets, func(i, j int) bool {
		a, b := resp.Targets[i], resp.Targets[j]
		if a.LossySenders != b.LossySenders {
			return a.LossySenders > b.LossySenders
		}
		if a.WorstLossPct != b.WorstLossPct {
			return a.WorstLossPct > b.WorstLossPct
		}
		return a.Name < b.Name
	})
	return connect.NewResponse(resp), nil
}
