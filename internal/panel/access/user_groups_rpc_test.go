package access

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The API of the extra groups (step 3): UpdateUser's add_group_ids and remove_group_ids, the impact with nodes, the counters
// and the audit. The world is pairWorld of multi_group_test.go: family = {hy, fam} without own1, alice-home = {hy, home}.

func (w *pairWorld) update(userID string, mut func(*adminv1.UpdateUserRequest)) (*adminv1.UpdateUserResponse, error) {
	r := &adminv1.UpdateUserRequest{UserId: userID}
	mut(r)
	out, err := w.s.UpdateUser(w.ctx, req(r))
	if err != nil {
		return nil, err
	}
	return out.Msg, nil
}

func profileNamesOf(ps []*adminv1.ImpactProfile) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	slices.Sort(out)
	return out
}

func TestAddingAGroupSaysWhatThePersonGetsAndLoses(t *testing.T) {
	w := newPairWorld(t)
	alice := w.user("alice", w.family, nil).User

	// the dry run: alice-home brings the home profile and the node own1 (family holds it back); nothing is written
	w.resetNotify()
	dry := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds, r.DryRun = []string{w.home}, true }))
	if dry.Impact == nil || dry.Impact.Users != 1 || len(dry.Impact.Lost) != 0 || !slices.Equal(profileNamesOf(dry.Impact.Gained), []string{"home 9443"}) ||
		!slices.Equal(dry.Impact.GainedNodeIds, []string{"nod_own1"}) || len(dry.Impact.LostNodeIds) != 0 {
		t.Fatalf("dry run impact = %+v", dry.Impact)
	}
	if len(dry.User.ExtraGroupIds) != 0 || w.notify.n.Load() != 0 || len(w.audited("user_update")) != 0 {
		t.Error("the dry run wrote something or told the nodes")
	}

	// the real change answers with the same impact, says it to the nodes, lists the group and writes the audit row
	real := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{w.home} }))
	if real.Impact == nil || !slices.Equal(real.Impact.GainedNodeIds, []string{"nod_own1"}) {
		t.Errorf("impact of the real change = %+v", real.Impact)
	}
	u := real.User
	if u.GroupId != w.family || !slices.Equal(u.ExtraGroupIds, []string{w.home}) || !slices.Equal(u.ExtraGroupNames, []string{"alice-home"}) || len(u.ExtraGroupColors) != 1 {
		t.Errorf("the person after the change: main %s, extra %v %v %v", u.GroupId, u.ExtraGroupIds, u.ExtraGroupNames, u.ExtraGroupColors)
	}
	if w.notify.n.Load() == 0 {
		t.Error("the nodes were not told")
	}
	rows := w.audited("user_update")
	if len(rows) != 1 || !strings.Contains(rows[0].Params, `"groups_added":"`+w.home+`"`) || !strings.Contains(rows[0].Params, `"groups_removed":""`) ||
		!strings.Contains(rows[0].Params, `"fields":"groups"`) {
		t.Errorf("audit = %+v", rows)
	}
	if got := must(w.s.GetUser(w.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg.User; !slices.Equal(got.ExtraGroupIds, []string{w.home}) {
		t.Errorf("GetUser extra groups = %v", got.ExtraGroupIds)
	}
	if l := must(w.s.ListUsers(w.ctx, req(&adminv1.ListUsersRequest{Query: "alice"}))).Msg; len(l.Users) != 1 || !slices.Equal(l.Users[0].ExtraGroupNames, []string{"alice-home"}) {
		t.Errorf("the list does not carry the chip names: %+v", l.Users)
	}

	// a repeat does nothing: no impact, no news for the nodes, no second audit row
	w.resetNotify()
	again := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{w.home} }))
	if again.Impact != nil || w.notify.n.Load() != 0 || len(w.audited("user_update")) != 2 {
		t.Errorf("a repeat: impact %v, notified %d, audit rows %d", again.Impact, w.notify.n.Load(), len(w.audited("user_update")))
	}
	if rows := w.audited("user_update"); strings.Contains(rows[0].Params, "groups_added") {
		t.Errorf("a repeat names a change: %s", rows[0].Params)
	}

	// the removal mirrors the add
	gone := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.RemoveGroupIds, r.DryRun = []string{w.home}, true }))
	if gone.Impact == nil || len(gone.Impact.Gained) != 0 || !slices.Equal(profileNamesOf(gone.Impact.Lost), []string{"home 9443"}) || !slices.Equal(gone.Impact.LostNodeIds, []string{"nod_own1"}) {
		t.Fatalf("removal impact = %+v", gone.Impact)
	}
	left := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.RemoveGroupIds = []string{w.home} }))
	if len(left.User.ExtraGroupIds) != 0 || left.User.GroupId != w.family {
		t.Errorf("after the removal: %+v", left.User)
	}
	// removing a group the person is not in is a repeat
	if r := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.RemoveGroupIds = []string{w.home} })); r.Impact != nil {
		t.Errorf("removing what is not there: impact %+v", r.Impact)
	}
}

// Another set of nodes with the same profiles is a change too: the pair is what counts.
func TestANodeAloneIsAnImpact(t *testing.T) {
	w := newPairWorld(t)
	// a group with the profile family holds, but that gets own1
	open := w.group("open", must(w.st.Access().Group(w.ctx, w.family)).ProfileIDs...)
	alice := w.user("alice", w.family, nil).User
	r := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds, r.DryRun = []string{open}, true }))
	if r.Impact == nil || len(r.Impact.Gained) != 0 || len(r.Impact.Lost) != 0 || !slices.Equal(r.Impact.GainedNodeIds, []string{"nod_own1"}) {
		t.Fatalf("impact of a group with the same profiles on another node = %+v", r.Impact)
	}
}

func TestMakingAnExtraGroupTheMainOne(t *testing.T) {
	w := newPairWorld(t)
	alice := w.user("alice", w.family, nil).User
	w.extraGroups(alice.Id, []string{w.home})

	// B becomes the main group and A stays as an extra group: nothing is lost, the set of groups is the same
	swap := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) {
		r.GroupId, r.AddGroupIds, r.DryRun = &w.home, []string{w.family}, true
	}))
	if swap.Impact != nil {
		t.Errorf("a swap changes nothing: impact %+v", swap.Impact)
	}
	real := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.GroupId, r.AddGroupIds = &w.home, []string{w.family} })).User
	if real.GroupId != w.home || !slices.Equal(real.ExtraGroupIds, []string{w.family}) {
		t.Errorf("after the swap: main %s, extra %v", real.GroupId, real.ExtraGroupIds)
	}
	// without the old main group in add_group_ids the old main group is left, and what only it gave is lost
	lost := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) {
		r.GroupId, r.RemoveGroupIds, r.DryRun = &w.family, []string{w.home}, true
	}))
	if lost.Impact == nil || !slices.Equal(profileNamesOf(lost.Impact.Lost), []string{"home 9443"}) {
		t.Errorf("leaving alice-home: impact %+v", lost.Impact)
	}
	// a group that is the main one now is skipped when it is added
	got := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{w.home} })).User
	if !slices.Equal(got.ExtraGroupIds, []string{w.family}) {
		t.Errorf("adding the main group: extra %v", got.ExtraGroupIds)
	}
	// the group of the request leaves the extra ones even when the request does not say so
	moved := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.GroupId = &w.family })).User
	if moved.GroupId != w.family || len(moved.ExtraGroupIds) != 0 {
		t.Errorf("moving to an extra group: main %s, extra %v", moved.GroupId, moved.ExtraGroupIds)
	}
}

func TestTheRefusalsOfTheGroupLists(t *testing.T) {
	w := newPairWorld(t)
	alice := w.user("alice", w.family, nil).User
	both := func(r *adminv1.UpdateUserRequest) {
		r.AddGroupIds, r.RemoveGroupIds = []string{w.home}, []string{w.home}
	}
	_, err := w.update(alice.Id, both)
	wantCoded(t, err, connect.CodeInvalidArgument, "group_in_both", map[string]string{"group": w.home})
	_, err = w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.RemoveGroupIds = []string{w.family} })
	wantCoded(t, err, connect.CodeInvalidArgument, "main_group", map[string]string{"group": w.family})
	// the main group is the one after the request
	_, err = w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.GroupId, r.RemoveGroupIds = &w.home, []string{w.home} })
	wantCoded(t, err, connect.CodeInvalidArgument, "main_group", nil)
	_, err = w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{"grp_nope"} })
	wantCode(t, err, connect.CodeNotFound)
	_, err = w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds, r.DryRun = []string{"grp_nope"}, true })
	wantCode(t, err, connect.CodeNotFound)
	_, err = w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{""} })
	wantCode(t, err, connect.CodeInvalidArgument)

	// the most a person is in is maxUserGroups, the main one counted
	var ids []string
	for i := range maxUserGroups - 1 {
		ids = append(ids, w.group("g"+string(rune('a'+i))))
	}
	got := must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = ids })).User
	if len(got.ExtraGroupIds) != maxUserGroups-1 {
		t.Fatalf("%d extra groups, want %d", len(got.ExtraGroupIds), maxUserGroups-1)
	}
	_, err = w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{w.home} })
	wantCoded(t, err, connect.CodeInvalidArgument, "too_many_groups", nil)
	// a refused change wrote nothing, and a swap of one for another still fits
	if got := must(w.s.GetUser(w.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg.User; len(got.ExtraGroupIds) != maxUserGroups-1 {
		t.Errorf("the refused add changed the groups: %v", got.ExtraGroupIds)
	}
	must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds, r.RemoveGroupIds = []string{w.home}, ids[:1] }))
}

func TestTheCountersOfAGroupHaveBothKindsOfPeople(t *testing.T) {
	w := newPairWorld(t)
	alice := w.user("alice", w.family, nil).User
	bob := w.user("bob", w.home, nil).User
	must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{w.home} }))
	groups := map[string]*adminv1.Group{}
	for _, g := range must(w.s.ListGroups(w.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups {
		groups[g.Id] = g
	}
	if g := groups[w.home]; g.UserCount != 1 || g.ExtraUserCount != 1 {
		t.Errorf("alice-home: main %d, extra %d, want 1 and 1", g.UserCount, g.ExtraUserCount)
	}
	if g := groups[w.family]; g.UserCount != 1 || g.ExtraUserCount != 0 {
		t.Errorf("family: main %d, extra %d, want 1 and 0", g.UserCount, g.ExtraUserCount)
	}
	// a group with only extra members is deleted without a target; they leave it and keep their main group
	extra := w.group("friends")
	must(w.update(bob.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{extra} }))
	w.resetNotify()
	must(w.s.DeleteGroup(w.ctx, req(&adminv1.DeleteGroupRequest{GroupId: extra})))
	if got := must(w.s.GetUser(w.ctx, req(&adminv1.GetUserRequest{UserId: bob.Id}))).Msg.User; got.GroupId != w.home || len(got.ExtraGroupIds) != 0 {
		t.Errorf("bob after the group went: %+v", got)
	}
	if w.notify.n.Load() == 0 {
		t.Error("the nodes were not told that the extra members lost the group")
	}
	if rows := w.audited("group_delete"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"extra_left":1`) {
		t.Errorf("audit = %+v", rows)
	}
	// the main members still need a target
	_, err := w.s.DeleteGroup(w.ctx, req(&adminv1.DeleteGroupRequest{GroupId: w.home}))
	wantCode(t, err, connect.CodeFailedPrecondition)
}

// The changes of two requests at once do not undo each other: each names its own group.
func TestTwoChangesOfGroupsDoNotUndoEachOther(t *testing.T) {
	w := newPairWorld(t)
	third := w.group("friends")
	alice := w.user("alice", w.family, nil).User
	must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{w.home} }))
	must(w.update(alice.Id, func(r *adminv1.UpdateUserRequest) { r.AddGroupIds = []string{third} }))
	got := must(w.s.GetUser(w.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg.User
	want := []string{w.home, third}
	slices.Sort(want)
	if g := slices.Sorted(slices.Values(got.ExtraGroupIds)); !slices.Equal(g, want) {
		t.Errorf("extra groups = %v, want %v", g, want)
	}
	// the chips are ordered by name
	if !slices.Equal(got.ExtraGroupNames, []string{"alice-home", "friends"}) {
		t.Errorf("names = %v", got.ExtraGroupNames)
	}
}
