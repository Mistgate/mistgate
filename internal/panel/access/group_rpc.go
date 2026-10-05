package access

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// groupProtos converts groups to their API form with what each gives right now (Group.happ_nodes / amnezia_nodes).
func (s *Service) groupProtos(ctx context.Context, gs ...store.AccessGroup) ([]*adminv1.Group, error) {
	full, err := s.st.Access().InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	out := make([]*adminv1.Group, len(gs))
	for i, g := range gs {
		happ, amnezia := s.reachOf(g, full)
		out[i] = &adminv1.Group{Id: g.ID, Name: g.Name, ProfileIds: g.ProfileIDs, UserCount: uint32(g.UserCount), DnsPresetId: g.DNSPresetID,
			HappNodes: happ, AmneziaNodes: amnezia, Color: g.Color}
	}
	return out, nil
}

// reachOf counts the nodes where the group's profiles give something now, by the kind of app that takes them: the
// subscription's rule (liveInbound) for a user with every node and both apps.
func (s *Service) reachOf(g store.AccessGroup, full []store.AccessInboundFull) (happ, amnezia uint32) {
	hn, an := map[string]bool{}, map[string]bool{}
	for _, f := range full {
		if !liveInbound(f) || !slices.Contains(g.ProfileIDs, f.Profile.ID) {
			continue
		}
		proto, ok := s.reg.Get(f.Profile.Protocol)
		if !ok {
			continue
		}
		h, a, _ := clientApps(proto)
		hn[f.Node.ID] = hn[f.Node.ID] || h
		an[f.Node.ID] = an[f.Node.ID] || a
	}
	for _, on := range hn {
		if on {
			happ++
		}
	}
	for _, on := range an {
		if on {
			amnezia++
		}
	}
	return happ, amnezia
}

// impactOf says what moving people from the profile set `from` to `to` does: profiles lost and gained (in name
// order), with the live AmneziaVPN keys per profile among them (awg). Nil when the sets hold the same profiles; NOT_FOUND
// for a profile in `to` that does not exist.
func (s *Service) impactOf(ctx context.Context, from, to []string, users int, awg map[string]int) (*adminv1.AccessImpact, error) {
	profs, err := s.st.Access().Profiles(ctx)
	if err != nil {
		return nil, s.internal("profiles", err)
	}
	known := map[string]bool{}
	imp := &adminv1.AccessImpact{Users: uint32(users)}
	for _, p := range profs {
		known[p.ID] = true
		was, will := slices.Contains(from, p.ID), slices.Contains(to, p.ID)
		if was == will {
			continue
		}
		ip := &adminv1.ImpactProfile{Id: p.ID, Name: p.Name, Protocol: p.Protocol, AwgDevices: uint32(awg[p.ID])}
		if was {
			imp.Lost = append(imp.Lost, ip)
		} else {
			imp.Gained = append(imp.Gained, ip)
		}
	}
	for _, id := range to {
		if !known[id] {
			return nil, notFound("profile")
		}
	}
	if len(imp.Lost) == 0 && len(imp.Gained) == 0 {
		return nil, nil
	}
	return imp, nil
}

func (s *Service) ListGroups(ctx context.Context, _ *connect.Request[adminv1.ListGroupsRequest]) (*connect.Response[adminv1.ListGroupsResponse], error) {
	gs, err := s.st.Access().Groups(ctx)
	if err != nil {
		return nil, s.internal("list groups", err)
	}
	out, err := s.groupProtos(ctx, gs...)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ListGroupsResponse{Groups: out}), nil
}

func groupErr(s *Service, op string, err error) error {
	switch {
	case errors.Is(err, store.ErrAccessExists):
		return coded(connect.CodeAlreadyExists, "name_taken")
	case errors.Is(err, store.ErrNotFound):
		return notFound("group or profile")
	}
	return s.internal(op, err)
}

func (s *Service) CreateGroup(ctx context.Context, req *connect.Request[adminv1.CreateGroupRequest]) (*connect.Response[adminv1.CreateGroupResponse], error) {
	name, err := cleanName("group", req.Msg.Name)
	if err != nil {
		return nil, err
	}
	if err := s.checkDNSPreset(ctx, req.Msg.DnsPresetId); err != nil {
		return nil, err
	}
	if err := checkGroupColor(req.Msg.Color); err != nil {
		return nil, err
	}
	g := store.AccessGroup{ID: store.NewID("grp_"), Name: name, ProfileIDs: slices.Compact(slices.Sorted(slices.Values(req.Msg.ProfileIds))),
		CreatedAt: s.now(), DNSPresetID: req.Msg.DnsPresetId, Color: req.Msg.Color}
	a := s.st.Access()
	if err := a.CreateGroup(ctx, g); err != nil {
		return nil, groupErr(s, "create group", err)
	}
	got, err := a.Group(ctx, g.ID)
	if err != nil {
		return nil, s.internal("get group", err)
	}
	s.audit(ctx, actor(ctx), "group_create", map[string]any{"group": got.ID, "name": got.Name})
	out, err := s.groupProtos(ctx, got)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CreateGroupResponse{Group: out[0]}), nil
}

func (s *Service) UpdateGroup(ctx context.Context, req *connect.Request[adminv1.UpdateGroupRequest]) (*connect.Response[adminv1.UpdateGroupResponse], error) {
	m := req.Msg
	a := s.st.Access()
	var name *string
	if m.Name != nil {
		n, err := cleanName("group", *m.Name)
		if err != nil {
			return nil, err
		}
		name = &n
	}
	var profiles *[]string
	if m.ProfileIds != nil {
		ids := slices.Compact(slices.Sorted(slices.Values(m.ProfileIds.Values)))
		profiles = &ids
	}
	if m.DnsPresetId != nil {
		if err := s.checkDNSPreset(ctx, *m.DnsPresetId); err != nil {
			return nil, err
		}
	}
	if m.Color != nil {
		if err := checkGroupColor(*m.Color); err != nil {
			return nil, err
		}
	}
	before, err := a.Group(ctx, m.GroupId)
	if err != nil {
		return nil, groupErr(s, "get group", err)
	}
	var impact *adminv1.AccessImpact
	if profiles != nil {
		awg := map[string]int{}
		if before.UserCount > 0 {
			if awg, err = a.AWGDevicesPerProfile(ctx, before.ID, ""); err != nil {
				return nil, s.internal("awg devices", err)
			}
		}
		if impact, err = s.impactOf(ctx, before.ProfileIDs, *profiles, before.UserCount, awg); err != nil {
			return nil, err
		}
	}
	got := before
	if !m.DryRun {
		if err := a.UpdateGroup(ctx, m.GroupId, name, profiles, m.DnsPresetId, m.Color); err != nil {
			return nil, groupErr(s, "update group", err)
		}
		if got, err = a.Group(ctx, m.GroupId); err != nil {
			return nil, s.internal("get group", err)
		}
		if !slices.Equal(before.ProfileIDs, got.ProfileIDs) && got.UserCount > 0 {
			s.notify.StateChanged() // the users of the group gained or lost profiles
		}
		s.audit(ctx, actor(ctx), "group_update", map[string]any{"group": got.ID, "name": got.Name})
	}
	out, err := s.groupProtos(ctx, got)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateGroupResponse{Group: out[0], Impact: impact}), nil
}

func (s *Service) DeleteGroup(ctx context.Context, req *connect.Request[adminv1.DeleteGroupRequest]) (*connect.Response[adminv1.DeleteGroupResponse], error) {
	m := req.Msg
	if m.MoveUsersTo != "" && m.MoveUsersTo == m.GroupId {
		return nil, invalid("move the users to another group")
	}
	a := s.st.Access()
	g, err := a.Group(ctx, m.GroupId)
	if err != nil {
		return nil, groupErr(s, "get group", err)
	}
	switch err := a.DeleteGroup(ctx, m.GroupId, m.MoveUsersTo); {
	case errors.Is(err, store.ErrAccessInUse):
		return nil, coded(connect.CodeFailedPrecondition, "group_not_empty", "users", strconv.Itoa(g.UserCount))
	case err != nil:
		return nil, groupErr(s, "delete group", err)
	}
	// The moved users now get the other group's profiles. Not g.UserCount: it was read before the move, and a user
	// created into the group since then was moved too.
	if m.MoveUsersTo != "" {
		s.notify.StateChanged()
	}
	s.audit(ctx, actor(ctx), "group_delete", map[string]any{"group": m.GroupId, "name": g.Name})
	return connect.NewResponse(&adminv1.DeleteGroupResponse{}), nil
}

// checkGroupColor accepts "" (none picked) and a tone of the palette.
func checkGroupColor(c string) error {
	if c != "" && !slices.Contains(store.GroupTones, c) {
		return invalid("group color must be one of %s", strings.Join(store.GroupTones, ", "))
	}
	return nil
}

// checkDNSPreset accepts "" (inherit) and the id of an existing preset.
func (s *Service) checkDNSPreset(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	ok, err := s.st.DNS().Exists(ctx, id)
	if err != nil {
		return s.internal("dns preset", err)
	}
	if !ok {
		return notFound("dns preset")
	}
	return nil
}
