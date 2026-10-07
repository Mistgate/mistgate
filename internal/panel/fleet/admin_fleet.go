package fleet

import (
	"cmp"
	"context"
	"sort"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type fleetService struct{ f *Fleet }

// feedEvents is what the Overview feed shows: warnings and errors, and the few info events that answer "what happened
// to the fleet" (a node went away, came back, joined, left; an update; a user). Engine starts, applied configurations
// and the like stay on the node's own Events tab.
var feedEvents = &store.CodeMatch{
	MinSeverity: 2,
	Codes:       []string{"node_down", "node_blip", "node_recovered", "node_enrolled", "node_retired", "engine_failed"},
	Prefixes:    []string{"update_", "user_"},
}

// Event families of the node's Events tab (ListEventsRequest.family): "profiles" is everything about a profile on the
// node, "agent" the rest. web/src/screens/node/event-model.ts (eventFamily) words the same split.
var profileEvents = store.CodeMatch{
	Codes: []string{"profile_added", "profile_removed", "profiles_restarted", "awg_backend_unavailable", "hop_failed", "hop_rejected",
		"credential_expired"},
	Prefixes: []string{"engine_"},
}

func familyMatch(family string) (*store.CodeMatch, error) {
	switch family {
	case "":
		return nil, nil
	case "profiles":
		m := profileEvents
		return &m, nil
	case "agent":
		m := profileEvents
		m.Not = true
		return &m, nil
	}
	return nil, invalid(`family must be "", "profiles" or "agent"`)
}

func eventMsg(e store.EventRow) *adminv1.Event {
	// the profile joined from the live inbound, else what a panel event recorded when the inbound still existed
	name, protocol := e.ProfileName, e.Protocol
	if name == "" {
		name, protocol = e.Params["profile"], cmp.Or(protocol, e.Params["protocol"])
	}
	return &adminv1.Event{Id: e.ID, TimeUnix: e.Time.Unix(), Severity: adminv1.EventSeverity(e.Severity), Code: e.Code, Params: e.Params,
		NodeId: e.NodeID, NodeName: e.NodeName, UserId: e.UserID, UserName: e.UserName,
		InboundId: e.InboundID, ProfileName: name, Protocol: protocol, Source: e.Source}
}

// series turns hour -> protocol -> value into hourly points from start (inclusive), n points.
func series(start int64, n int, vals map[int64]map[string]uint64) []*adminv1.SeriesPoint {
	out := make([]*adminv1.SeriesPoint, 0, n)
	for i := range n {
		h := start + int64(i)*3600
		p := &adminv1.SeriesPoint{StartUnix: h}
		for proto, v := range vals[h] {
			p.Values = append(p.Values, &adminv1.ProtocolValue{Protocol: proto, Value: v})
		}
		sort.Slice(p.Values, func(a, b int) bool { return p.Values[a].Protocol < p.Values[b].Protocol })
		out = append(out, p)
	}
	return out
}

func (s fleetService) Overview(ctx context.Context, req *connect.Request[adminv1.OverviewRequest]) (*connect.Response[adminv1.OverviewResponse], error) {
	f := s.f
	now := f.now().UTC()
	points := 24
	if req.Msg.Range == adminv1.OverviewRange_OVERVIEW_RANGE_7D {
		points = 168
	}
	cur := now.Unix() - now.Unix()%3600
	start := cur - int64(points-1)*3600 // points >= 24, so the same rows also feed the 24-hour sparks
	limit := int(req.Msg.EventLimit)
	if limit == 0 {
		limit = 6
	}
	limit = min(limit, 200)

	nodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		return nil, internalErr(f.log.Error, "overview nodes", err)
	}
	hours, err := f.st.FleetHours(ctx, start)
	if err != nil {
		return nil, internalErr(f.log.Error, "overview hours", err)
	}
	events, _, err := f.st.Events(ctx, store.EventFilter{Limit: limit, Match: feedEvents})
	if err != nil {
		return nil, internalErr(f.log.Error, "overview events", err)
	}
	enabled, err := f.st.FleetEnabledInbounds(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "overview inbounds", err)
	}

	traffic := map[int64]map[string]uint64{}
	peak := map[int64]map[string]uint64{}
	nodeHour := map[string]map[int64]uint64{}
	for _, r := range hours {
		if traffic[r.Hour] == nil {
			traffic[r.Hour], peak[r.Hour] = map[string]uint64{}, map[string]uint64{}
		}
		traffic[r.Hour][r.Protocol] += r.Up + r.Down
		// The fleet-wide peak of an hour is the sum of per-node peaks (an upper bound: a user seen
		// on two nodes in that hour counts twice); exact needs a distinct-user rollup.
		peak[r.Hour][r.Protocol] += r.PeakUsers
		if nodeHour[r.NodeID] == nil {
			nodeHour[r.NodeID] = map[int64]uint64{}
		}
		nodeHour[r.NodeID][r.Hour] += r.Up + r.Down
	}

	resp := &adminv1.OverviewResponse{NowUnix: now.Unix(), NodesTotal: uint32(len(nodes)), Events: make([]*adminv1.Event, 0, len(events))}
	for _, e := range events {
		resp.Events = append(resp.Events, eventMsg(e))
	}

	type consumer struct {
		userID, nodeID string
		bps            uint64
	}
	var consumers []consumer
	liveUsers := map[string]bool{}
	liveByProto := map[string]map[string]bool{}
	for _, n := range nodes {
		sess := f.session(n.ID)
		view := sessionViewOf(sess)
		st := f.statusOfView(ctx, n, view, inboundsOf(enabled, n.ID), now)
		card := &adminv1.NodeCard{Id: n.ID, Name: n.Name, CountryCode: n.CountryCode, Location: n.Location, Provider: n.Provider,
			Status: st.status, Reason: st.reason, SparkBytes: make([]uint64, 24)}
		for i := range 24 {
			card.SparkBytes[i] = nodeHour[n.ID][cur-int64(23-i)*3600]
		}
		if st.problem() {
			resp.NodesProblem++
		}
		if sess != nil {
			card.Online = protocolCounts(onlineByProtocolView(view))
			if view != nil {
				for _, o := range view.Live.Online {
					liveUsers[o.userID] = true
					if liveByProto[o.protocol] == nil {
						liveByProto[o.protocol] = map[string]bool{}
					}
					liveByProto[o.protocol][o.userID] = true
				}
				for u, bps := range view.Live.UserDown {
					card.DownBps += bps
					consumers = append(consumers, consumer{u, n.ID, bps})
				}
				for _, bps := range view.Live.UserUp {
					card.UpBps += bps
				}
				if m := view.Live.Metrics; m != nil {
					card.HasMetrics, card.CpuPct = true, m.CpuPct
				}
			}
		}
		resp.Nodes = append(resp.Nodes, card)
	}
	resp.UsersOnline = uint32(len(liveUsers))
	if h := f.hooks(); h != nil {
		resp.AlertsActive, resp.AlertsCritical = h.AlertCounts(ctx)
	}

	live := map[string]uint64{}
	for p, u := range liveByProto {
		live[p] = uint64(len(u))
	}
	peak[cur] = live // the last point is the live value
	resp.Traffic = series(start, points, traffic)
	resp.Online = series(start, points, peak)

	sort.Slice(consumers, func(i, j int) bool {
		if consumers[i].bps != consumers[j].bps {
			return consumers[i].bps > consumers[j].bps
		}
		return consumers[i].userID+consumers[i].nodeID < consumers[j].userID+consumers[j].nodeID
	})
	if len(consumers) > 5 {
		consumers = consumers[:5]
	}
	var uids []string
	for _, c := range consumers {
		uids = append(uids, c.userID)
	}
	names, err := f.st.FleetUserNames(ctx, uids)
	if err != nil {
		return nil, internalErr(f.log.Error, "overview consumers", err)
	}
	nodeNames := map[string]string{}
	for _, n := range nodes {
		nodeNames[n.ID] = n.Name
	}
	for _, c := range consumers {
		if c.bps > 0 {
			resp.TopConsumers = append(resp.TopConsumers, &adminv1.TopConsumer{UserId: c.userID, UserName: names[c.userID],
				NodeId: c.nodeID, NodeName: nodeNames[c.nodeID], DownBps: c.bps})
		}
	}
	return connect.NewResponse(resp), nil
}

func (s fleetService) ListEvents(ctx context.Context, req *connect.Request[adminv1.ListEventsRequest]) (*connect.Response[adminv1.ListEventsResponse], error) {
	f := s.f
	m := req.Msg
	limit := int(m.Limit)
	if limit == 0 {
		limit = 50
	}
	limit = min(limit, 200)
	match, err := familyMatch(m.Family)
	if err != nil {
		return nil, err
	}
	rows, more, err := f.st.Events(ctx, store.EventFilter{NodeID: m.NodeId, UserID: m.UserId, MinSeverity: int(m.MinSeverity),
		BeforeID: m.BeforeId, Limit: limit, Match: match})
	if err != nil {
		return nil, internalErr(f.log.Error, "list events", err)
	}
	resp := &adminv1.ListEventsResponse{HasMore: more}
	for _, e := range rows {
		resp.Events = append(resp.Events, eventMsg(e))
	}
	return connect.NewResponse(resp), nil
}
