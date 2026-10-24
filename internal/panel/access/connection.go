package access

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/connectiondiag"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// capLinks: the agent sends connection counters (StatsBatch.links, agent.proto LinkCounters).
const capLinks = "links/1"

// GetUserConnection: what the nodes saw of a person's connections over the last hours, per device, node and inbound (agent.proto
// LinkCounters). A read; no address is stored or returned. nodes_without_counters are the nodes the person can use whose agent
// does not list links/1.
func (s *Service) GetUserConnection(ctx context.Context, req *connect.Request[adminv1.GetUserConnectionRequest]) (*connect.Response[adminv1.GetUserConnectionResponse], error) {
	a := s.st.Access()
	u, err := a.User(ctx, req.Msg.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("user")
	} else if err != nil {
		return nil, s.internal("get user", err)
	}
	now := s.now().UTC()
	since := now.Add(-time.Duration(store.LinkWindowHours(req.Msg.Hours)) * time.Hour)
	g, err := a.Group(ctx, u.GroupID)
	if err != nil {
		return nil, s.internal("group", err)
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	resp := &adminv1.GetUserConnectionResponse{SinceUnix: since.Unix(), NowUnix: now.Unix()}
	seen := map[string]bool{}
	links2ByNode := map[string]bool{}
	for _, f := range full {
		if seen[f.Node.ID] || !s.usable(f, g, u) {
			continue
		}
		seen[f.Node.ID] = true
		links2ByNode[f.Node.ID] = slices.Contains(f.Node.AgentCaps, connectiondiag.Links2Capability)
		if !slices.Contains(f.Node.AgentCaps, capLinks) {
			resp.NodesWithoutCounters = append(resp.NodesWithoutCounters, f.Node.ID)
		}
	}
	slices.Sort(resp.NodesWithoutCounters)
	rows, err := s.st.UserLinksByHour(ctx, u.ID, since, req.Msg.ByHour)
	if err != nil {
		return nil, s.internal("connection counters", err)
	}
	for _, r := range rows {
		hasLinks2 := connectiondiag.SupportsHysteria2Links2(r.Protocol, links2ByNode[r.NodeID])
		msg := &adminv1.DeviceConnection{
			DeviceId: r.DeviceID, NodeId: r.NodeID, NodeName: r.NodeName, InboundId: r.InboundID, ProfileName: r.ProfileName,
			Protocol: r.Protocol, Connects: clamp32(r.Connects), Rejects: clamp32(r.Rejects), Drops: clamp32(r.Drops),
			Stalls: clamp32(r.Stalls), OnlineS: r.OnlineS, LastHourUnix: r.LastHour.Unix(), Platform: r.Platform,
			Client: r.Client, Kind: store.DeviceKindName(r.Kind), Hours: userLinkHours(r.Hours, hasLinks2),
		}
		if hasLinks2 {
			msg.ClientCloses, msg.Freezes, msg.Quiet = clamp32(r.ClientCloses), clamp32(r.Freezes), clamp32(r.Quiet)
			msg.RttMs = connectiondiag.AverageRTTMS(r.RttMSSum, r.RttSamples)
			msg.LossPermille = connectiondiag.LossPermille(r.PktsLost, r.PktsSent)
			msg.RttSamples, msg.PktsSent = clamp32(r.RttSamples), r.PktsSent
		}
		resp.Rows = append(resp.Rows, msg)
	}
	return connect.NewResponse(resp), nil
}

func userLinkHours(hours []store.LinkHourSums, links2 bool) []*adminv1.LinkHour {
	out := make([]*adminv1.LinkHour, 0, len(hours))
	for _, h := range hours {
		msg := &adminv1.LinkHour{HourStartUnix: h.HourStart, Connects: clamp32(h.Connects), Rejects: clamp32(h.Rejects),
			Drops: clamp32(h.Drops), Stalls: clamp32(h.Stalls), OnlineS: h.OnlineS}
		if links2 {
			msg.ClientCloses, msg.Freezes, msg.Quiet = clamp32(h.ClientCloses), clamp32(h.Freezes), clamp32(h.Quiet)
			msg.Accepted, msg.HellosForeign = clamp32(h.Accepted), clamp32(h.HellosForeign)
			msg.RttMs, msg.LossPermille = connectiondiag.AverageRTTMS(h.RttMSSum, h.RttSamples), connectiondiag.LossPermille(h.PktsLost, h.PktsSent)
			msg.RttSamples, msg.PktsSent = clamp32(h.RttSamples), h.PktsSent
		}
		out = append(out, msg)
	}
	return out
}

// clamp32 fits a window sum into a uint32 field.
func clamp32(n uint64) uint32 { return uint32(min(n, math.MaxUint32)) }
