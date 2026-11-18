package access

import (
	"context"
	"errors"
	"maps"
	"slices"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/nodeshare"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (s *Service) SetNodeSharing(ctx context.Context, req *connect.Request[adminv1.SetNodeSharingRequest]) (*connect.Response[adminv1.SetNodeSharingResponse], error) {
	m := req.Msg
	choice, err := nodeshare.Valid(m.Mode, m.GroupIds)
	if err != nil {
		return nil, err
	}
	node, err := s.st.Node(ctx, m.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("node")
	} else if err != nil {
		return nil, s.internal("node", err)
	}
	if node.State == "retired" {
		return nil, coded(connect.CodeFailedPrecondition, "node_retired")
	}
	a := s.st.Access()
	groups, err := a.Groups(ctx)
	if err != nil {
		return nil, s.internal("list groups", err)
	}
	audience, err := a.NodeAudience(ctx, m.NodeId)
	if err != nil {
		return nil, s.internal("node audience", err)
	}
	for _, id := range choice.GroupIDs {
		if !slices.ContainsFunc(groups, func(g store.AccessGroup) bool { return g.ID == id }) {
			return nil, notFound("group")
		}
	}
	// what each group does after the change; ALL and NONE name no group
	resp := &adminv1.SetNodeSharingResponse{Mode: m.Mode}
	for _, g := range groups {
		was, will := g.GetsNode(m.NodeId), choice.Mode == store.ShareAll || slices.Contains(choice.GroupIDs, g.ID)
		people := uint32(audience[g.ID])
		switch {
		case will:
			resp.GroupIds = append(resp.GroupIds, g.ID)
			resp.PeopleReceiving += people
			if !was {
				resp.PeopleGain += people
			}
		case was:
			resp.PeopleLose += people
		}
	}
	if m.DryRun {
		return connect.NewResponse(resp), nil
	}
	changed := int(resp.PeopleGain + resp.PeopleLose)
	reservation, err := s.reserveTokenUserChanges(ctx, adminv1connect.GroupServiceSetNodeSharingProcedure, changed)
	if err != nil {
		return nil, err
	}
	defer reservation.finish()
	switch err := a.SetNodeShare(ctx, m.NodeId, choice, s.now()); {
	case errors.Is(err, store.ErrNodeRetired):
		return nil, coded(connect.CodeFailedPrecondition, "node_retired")
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("node")
	case err != nil:
		return nil, s.internal("set node sharing", err)
	}
	reservation.markTouched(changed)
	if changed > 0 {
		s.notify.StateChanged() // the people of the groups gained or lost the node
	}
	audit := map[string]any{"node": node.ID, "name": node.Name, "mode": choice.Mode}
	if len(choice.GroupIDs) > 0 {
		audit["groups"] = slices.Sorted(slices.Values(choice.GroupIDs))
	}
	s.audit(ctx, actor(ctx), "node_sharing_set", audit)
	return connect.NewResponse(resp), nil
}

// checkHeldNodes refuses a group update that would switch on a node the owner holds back, for a group that was not given it
// (FAILED_PRECONDITION "node_held"): the update replaces the whole disabled set, so a form loaded before the node was made would
// otherwise hand it out by saving. Switching a held node off for a group that has it is fine.
func (s *Service) checkHeldNodes(ctx context.Context, before store.AccessGroup, disabled []string) error {
	shares, err := s.st.NodeShares(ctx)
	if err != nil {
		return s.internal("node shares", err)
	}
	for _, id := range slices.Sorted(maps.Keys(shares)) {
		if sh := shares[id]; sh.Hold && !sh.Retired && !before.GetsNode(id) && store.GroupGetsNode(disabled, id) {
			return coded(connect.CodeFailedPrecondition, "node_held", "node", id)
		}
	}
	return nil
}
