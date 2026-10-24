package fleet

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// capJournal: the agent sends the saved warning and error journal (StatsBatch.journal, agent.proto JournalEntry).
const capJournal = "journal/1"

// GetNodeJournal reads the saved warning and error journal. Records are privacy-cleaned before storage and carry no addresses.
func (s nodeService) GetNodeJournal(ctx context.Context, req *connect.Request[adminv1.GetNodeJournalRequest]) (*connect.Response[adminv1.GetNodeJournalResponse], error) {
	f := s.f
	now := f.now().UTC()
	hours := store.NodeJournalWindowHours(req.Msg.Hours)
	since := now.Add(-time.Duration(hours) * time.Hour)
	resp := &adminv1.GetNodeJournalResponse{SinceUnix: since.Unix(), NowUnix: now.Unix()}

	var inScope []store.NodeRow
	if req.Msg.NodeId != "" {
		n, err := f.st.Node(ctx, req.Msg.NodeId)
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		if err != nil {
			return nil, internalErr(f.log.Error, "get node", err)
		}
		if n.State != "retired" {
			inScope = append(inScope, n)
		}
	} else {
		nodes, err := f.st.Nodes(ctx, false)
		if err != nil {
			return nil, internalErr(f.log.Error, "list nodes for journal", err)
		}
		inScope = nodes
	}
	for _, n := range inScope {
		if !capabilityPresent(n.AgentCaps, capJournal) {
			resp.NodesWithoutJournal = append(resp.NodesWithoutJournal, n.ID)
		}
	}

	minLevel := int32(2) // UNSPECIFIED and INFO both mean WARNING for this RPC.
	if req.Msg.MinLevel == adminv1.EventSeverity_EVENT_SEVERITY_ERROR {
		minLevel = 3
	}
	limit := 100
	if req.Msg.Limit != 0 {
		limit = int(min(req.Msg.Limit, uint32(200)))
	}
	rows, err := f.st.NodeJournal(ctx, store.NodeJournalFilter{
		NodeID: req.Msg.NodeId, Since: since, Until: now, MinLevel: minLevel, Source: req.Msg.Source,
		InboundID: req.Msg.InboundId, Query: logscrub.ClipRunes(req.Msg.Query, 100), Limit: limit + 1,
	})
	if err != nil {
		return nil, internalErr(f.log.Error, "read node journal", err)
	}
	if len(rows) > limit {
		resp.Truncated = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		resp.Entries = append(resp.Entries, &adminv1.NodeJournalEntry{
			NodeId: row.NodeID, NodeName: row.NodeName, HourStartUnix: row.HourStart,
			Level: adminv1.EventSeverity(row.Level), Source: row.Source, Message: row.Message, InboundId: row.InboundID,
			ProfileName: row.ProfileName, Attrs: row.Attrs, Count: row.Count, FirstUnix: row.FirstAt, LastUnix: row.LastAt,
		})
	}
	return connect.NewResponse(resp), nil
}
