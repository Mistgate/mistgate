package fleet

import (
	"context"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/connectiondiag"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// capLinks: the agent sends connection counters (StatsBatch.links, agent.proto LinkCounters).
const capLinks = "links/1"

// GetNodeConnections: the connection counters of the node's inbounds over the last hours (agent.proto LinkCounters). A read; no
// address is stored or returned.
func (s nodeService) GetNodeConnections(ctx context.Context, req *connect.Request[adminv1.GetNodeConnectionsRequest]) (*connect.Response[adminv1.GetNodeConnectionsResponse], error) {
	f := s.f
	now := f.now().UTC()
	since := now.Add(-time.Duration(store.LinkWindowHours(req.Msg.Hours)) * time.Hour)
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "get node", err)
	}
	resp := &adminv1.GetNodeConnectionsResponse{SinceUnix: since.Unix(), NowUnix: now.Unix()}
	if !capabilityPresent(n.AgentCaps, capLinks) {
		return connect.NewResponse(resp), nil
	}
	resp.Supported = true
	inbounds, err := f.st.FleetInbounds(ctx, n.ID, false)
	if err != nil {
		return nil, internalErr(f.log.Error, "node inbounds", err)
	}
	sums, err := f.st.NodeLinkReports(ctx, n.ID, since, req.Msg.ByHour)
	if err != nil {
		return nil, internalErr(f.log.Error, "node connection counters", err)
	}
	byInbound := make(map[string]store.NodeLinkReport, len(sums))
	for _, r := range sums {
		byInbound[r.Totals.InboundID] = r
	}
	for _, in := range inbounds {
		report := byInbound[in.ID]
		r := report.Totals
		msg := &adminv1.InboundConnections{
			InboundId: in.ID, ProfileId: in.ProfileID, ProfileName: in.ProfileName, Protocol: in.Protocol,
			Connects: clamp32(r.Connects), Rejects: clamp32(r.Rejects), Drops: clamp32(r.Drops), Stalls: clamp32(r.Stalls),
			OnlineS: r.OnlineS, UnknownKeys: clamp32(r.UnknownKeys), Hellos: clamp32(r.Hellos), Handshakes: clamp32(r.Handshakes),
			KeysStalled: clamp32(r.KeysStalled), KeysTried: clamp32(r.KeysTried),
			ClientCloses: clamp32(r.ClientCloses), Freezes: clamp32(r.Freezes), Quiet: clamp32(r.Quiet),
			Accepted: clamp32(r.Accepted), HellosForeign: clamp32(r.HellosForeign),
			RttMs:        connectiondiag.AverageRTTMS(r.RttMSSum, r.RttSamples),
			LossPermille: connectiondiag.LossPermille(r.PktsLost, r.PktsSent), RttSamples: clamp32(r.RttSamples), PktsSent: r.PktsSent,
			Unsupported: connectiondiag.UnsupportedCountersForNode(in.Protocol, n.AgentCaps),
		}
		for _, h := range report.Hours {
			msg.Hours = append(msg.Hours, &adminv1.LinkHour{HourStartUnix: h.HourStart, Connects: clamp32(h.Connects), Rejects: clamp32(h.Rejects),
				Drops: clamp32(h.Drops), Stalls: clamp32(h.Stalls), OnlineS: h.OnlineS, UnknownKeys: clamp32(h.UnknownKeys),
				Hellos: clamp32(h.Hellos), Handshakes: clamp32(h.Handshakes), ClientCloses: clamp32(h.ClientCloses),
				Freezes: clamp32(h.Freezes), Quiet: clamp32(h.Quiet), Accepted: clamp32(h.Accepted), HellosForeign: clamp32(h.HellosForeign),
				RttMs: connectiondiag.AverageRTTMS(h.RttMSSum, h.RttSamples), LossPermille: connectiondiag.LossPermille(h.PktsLost, h.PktsSent),
				RttSamples: clamp32(h.RttSamples), PktsSent: h.PktsSent})
		}
		for _, c := range report.Clients {
			msg.Clients = append(msg.Clients, &adminv1.ClientConnections{Client: c.Client, Platform: c.Platform, Devices: clamp32(c.Devices),
				Connects: clamp32(c.Connects), Rejects: clamp32(c.Rejects), Drops: clamp32(c.Drops), Stalls: clamp32(c.Stalls), OnlineS: c.OnlineS,
				Freezes: clamp32(c.Freezes), Quiet: clamp32(c.Quiet), RttMs: connectiondiag.AverageRTTMS(c.RttMSSum, c.RttSamples),
				LossPermille: connectiondiag.LossPermille(c.PktsLost, c.PktsSent)})
		}
		if in.Protocol == "awg" {
			if h := awgStatusMsg(in); h != nil {
				msg.Awg = &adminv1.AwgPackets{UdpRxPackets: h.GetUdpRxPackets(), ReportedUnix: h.GetReportedUnix(),
					Backend: h.GetBackend(), PeersOnline: h.GetPeersOnline()}
			}
		}
		resp.Inbounds = append(resp.Inbounds, msg)
	}
	return connect.NewResponse(resp), nil
}

// clamp32 fits a window sum into a uint32 field (168 hours of uint32 batches can in theory pass it).
func clamp32(n uint64) uint32 { return uint32(min(n, math.MaxUint32)) }

// GetFleetConnections returns the counters of every non-retired node, with one metadata read and one link_hour read.
func (s nodeService) GetFleetConnections(ctx context.Context, req *connect.Request[adminv1.GetFleetConnectionsRequest]) (*connect.Response[adminv1.GetFleetConnectionsResponse], error) {
	f := s.f
	now := f.now().UTC()
	since := now.Add(-time.Duration(store.LinkWindowHours(req.Msg.Hours)) * time.Hour)
	nodes, err := f.st.FleetConnectionNodes(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "fleet connection nodes", err)
	}
	sums, err := f.st.FleetNodeLinkSums(ctx, since)
	if err != nil {
		return nil, internalErr(f.log.Error, "fleet connection counters", err)
	}
	byNode := make(map[string]map[string]store.LinkInboundSums, len(nodes))
	for _, sum := range sums {
		if byNode[sum.NodeID] == nil {
			byNode[sum.NodeID] = map[string]store.LinkInboundSums{}
		}
		byNode[sum.NodeID][sum.InboundID] = sum.LinkInboundSums
	}
	resp := &adminv1.GetFleetConnectionsResponse{SinceUnix: since.Unix(), NowUnix: now.Unix(), Nodes: []*adminv1.NodeConnections{}}
	for _, n := range nodes {
		node := &adminv1.NodeConnections{NodeId: n.ID, NodeName: n.Name, CountryCode: n.CountryCode,
			Supported: capabilityPresent(n.AgentCaps, capLinks), Inbounds: []*adminv1.InboundConnections{}}
		if node.Supported {
			for _, in := range n.Inbounds {
				r := byNode[n.ID][in.ID]
				msg := &adminv1.InboundConnections{
					InboundId: in.ID, ProfileId: in.ProfileID, ProfileName: in.ProfileName, Protocol: in.Protocol,
					Connects: clamp32(r.Connects), Rejects: clamp32(r.Rejects), Drops: clamp32(r.Drops), Stalls: clamp32(r.Stalls),
					OnlineS: r.OnlineS, UnknownKeys: clamp32(r.UnknownKeys), Hellos: clamp32(r.Hellos), Handshakes: clamp32(r.Handshakes),
					KeysStalled: clamp32(r.KeysStalled), KeysTried: clamp32(r.KeysTried),
					ClientCloses: clamp32(r.ClientCloses), Freezes: clamp32(r.Freezes), Quiet: clamp32(r.Quiet),
					Accepted: clamp32(r.Accepted), HellosForeign: clamp32(r.HellosForeign),
					RttMs:        connectiondiag.AverageRTTMS(r.RttMSSum, r.RttSamples),
					LossPermille: connectiondiag.LossPermille(r.PktsLost, r.PktsSent), RttSamples: clamp32(r.RttSamples), PktsSent: r.PktsSent,
					Unsupported: connectiondiag.UnsupportedCountersForNode(in.Protocol, n.AgentCaps),
				}
				if in.Protocol == "awg" {
					if h := awgStatusMsg(in); h != nil {
						msg.Awg = &adminv1.AwgPackets{UdpRxPackets: h.GetUdpRxPackets(), ReportedUnix: h.GetReportedUnix(),
							Backend: h.GetBackend(), PeersOnline: h.GetPeersOnline()}
					}
				}
				node.Inbounds = append(node.Inbounds, msg)
			}
		}
		resp.Nodes = append(resp.Nodes, node)
	}
	return connect.NewResponse(resp), nil
}
