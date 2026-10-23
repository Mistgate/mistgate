package access

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
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
	for _, f := range full {
		if seen[f.Node.ID] || !s.usable(f, g, u) {
			continue
		}
		seen[f.Node.ID] = true
		if !slices.Contains(f.Node.AgentCaps, capLinks) {
			resp.NodesWithoutCounters = append(resp.NodesWithoutCounters, f.Node.ID)
		}
	}
	slices.Sort(resp.NodesWithoutCounters)
	rows, err := s.st.UserLinks(ctx, u.ID, since)
	if err != nil {
		return nil, s.internal("connection counters", err)
	}
	for _, r := range rows {
		resp.Rows = append(resp.Rows, &adminv1.DeviceConnection{
			DeviceId: r.DeviceID, NodeId: r.NodeID, NodeName: r.NodeName, InboundId: r.InboundID, ProfileName: r.ProfileName,
			Protocol: r.Protocol, Connects: clamp32(r.Connects), Rejects: clamp32(r.Rejects), Drops: clamp32(r.Drops),
			Stalls: clamp32(r.Stalls), OnlineS: r.OnlineS, LastHourUnix: r.LastHour.Unix(),
		})
	}
	return connect.NewResponse(resp), nil
}

// clamp32 fits a window sum into a uint32 field.
func clamp32(n uint64) uint32 { return uint32(min(n, math.MaxUint32)) }
