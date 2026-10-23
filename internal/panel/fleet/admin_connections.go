package fleet

import (
	"context"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
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
	sums, err := f.st.NodeLinkSums(ctx, n.ID, since)
	if err != nil {
		return nil, internalErr(f.log.Error, "node connection counters", err)
	}
	byInbound := make(map[string]store.LinkInboundSums, len(sums))
	for _, r := range sums {
		byInbound[r.InboundID] = r
	}
	for _, in := range inbounds {
		r := byInbound[in.ID]
		resp.Inbounds = append(resp.Inbounds, &adminv1.InboundConnections{
			InboundId: in.ID, ProfileId: in.ProfileID, ProfileName: in.ProfileName, Protocol: in.Protocol,
			Connects: clamp32(r.Connects), Rejects: clamp32(r.Rejects), Drops: clamp32(r.Drops), Stalls: clamp32(r.Stalls),
			OnlineS: r.OnlineS, UnknownKeys: clamp32(r.UnknownKeys), Hellos: clamp32(r.Hellos), Handshakes: clamp32(r.Handshakes),
			KeysStalled: clamp32(r.KeysStalled), KeysTried: clamp32(r.KeysTried),
		})
	}
	return connect.NewResponse(resp), nil
}

// clamp32 fits a window sum into a uint32 field (48 hours of uint32 batches can in theory pass it).
func clamp32(n uint64) uint32 { return uint32(min(n, math.MaxUint32)) }
