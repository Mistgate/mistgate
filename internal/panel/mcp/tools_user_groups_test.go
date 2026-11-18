//go:build !js

package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The extra groups of a person in the MCP tools: user_update (add_group_ids, remove_group_ids), users_search, groups_list,
// group_delete and the access report.

func TestUserUpdatePlansTheExtraGroups(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	e.w.groups["grp_3"] = &adminv1.Group{Id: "grp_3", Name: "friends"}
	e.w.userImpact = &adminv1.AccessImpact{Users: 1,
		Gained:        []*adminv1.ImpactProfile{{Id: "prf_9", Name: "Home", Protocol: "awg"}},
		Lost:          []*adminv1.ImpactProfile{{Id: "prf_2", Name: "Slow", Protocol: "vless"}},
		GainedNodeIds: []string{nodeA}, LostNodeIds: []string{nodeB}}

	p := planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "add_group_ids": []string{"grp_3"}, "remove_group_ids": []string{"grp_1"}})
	for key, want := range map[string]string{"groups_added": "friends", "groups_removed": "family", "profiles_gained": "Home", "profiles_lost": "Slow",
		"nodes_gained": "de1", "nodes_lost": "nl1"} {
		if f := factOf(t, p, key); f.Value != want || !f.Untrusted {
			t.Errorf("fact %s = %+v, want the untrusted %q", key, f, want)
		}
	}
	if len(e.w.userDryReq) != 1 || !e.w.userDryReq[0].GetDryRun() || !slices.Equal(e.w.userDryReq[0].GetAddGroupIds(), []string{"grp_3"}) {
		t.Fatalf("the plan asked the panel to check the change: %+v", e.w.userDryReq)
	}
	if len(e.w.updateReq) != 0 {
		t.Fatal("the plan changed the person")
	}
	applyOf(t, s, "user_update", p.ConfirmToken)
	got := e.w.updateReq[len(e.w.updateReq)-1]
	if got.DryRun || got.GroupId != nil || !slices.Equal(got.AddGroupIds, []string{"grp_3"}) || !slices.Equal(got.RemoveGroupIds, []string{"grp_1"}) {
		t.Errorf("the applied request: %+v", got)
	}

	// B as the main group with A kept: the plan says the group and the extra one
	e.w.userImpact = nil
	p = planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "group_id": "grp_3", "add_group_ids": []string{"grp_1", "grp_1"}})
	if f := factOf(t, p, "groups_added"); f.Value != "family" {
		t.Errorf("groups_added = %+v", f)
	}
	for _, f := range p.Facts {
		if strings.HasPrefix(f.Key, "profiles_") || strings.HasPrefix(f.Key, "nodes_") {
			t.Errorf("a change with no impact has the fact %+v", f)
		}
	}
	applyOf(t, s, "user_update", p.ConfirmToken)
	if got := e.w.updateReq[len(e.w.updateReq)-1]; got.GetGroupId() != "grp_3" || !slices.Equal(got.AddGroupIds, []string{"grp_1"}) {
		t.Errorf("the applied request: %+v", got)
	}

	// refusals: a group that is not there, too many ids, nothing at all
	if out := mustFail(t, s, "user_update_plan", map[string]any{"user_id": "usr_alice", "add_group_ids": []string{"grp_nope"}}); !strings.Contains(out, "no group with id") {
		t.Errorf("unknown group: %s", out)
	}
	if out := mustFail(t, s, "user_update_plan", map[string]any{"user_id": "usr_alice", "add_group_ids": []string{"bad id"}}); out == "" {
		t.Error("an id with a space was accepted")
	}
	if out := mustFail(t, s, "user_update_plan", map[string]any{"user_id": "usr_alice", "add_group_ids": []string{}}); !strings.Contains(out, "nothing to change") {
		t.Errorf("empty lists: %s", out)
	}
}

func TestReadsShowTheExtraGroups(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	e.w.groups["grp_3"] = &adminv1.Group{Id: "grp_3", Name: "friends", ExtraUserCount: 2}
	u := e.w.users["usr_alice"].User
	u.GroupId, u.GroupName = "grp_1", "family"
	u.ExtraGroupIds, u.ExtraGroupNames, u.ExtraGroupColors = []string{"grp_3"}, []string{"friends"}, []string{"sky"}

	var found UsersV
	if err := json.Unmarshal([]byte(mustOK(t, s, "users_search", map[string]any{"query": "alice"})), &found); err != nil || len(found.Users) == 0 {
		t.Fatalf("users_search: %v %+v", err, found)
	}
	if got := found.Users[0]; got.Group != "family" || !slices.Equal(got.ExtraGroupIDs, []string{"grp_3"}) || !slices.Equal(got.ExtraGroups, []string{"friends"}) {
		t.Errorf("users_search shows %+v", got)
	}
	var one UserGetV
	if err := json.Unmarshal([]byte(mustOK(t, s, "user_get", map[string]any{"user_id": "usr_alice"})), &one); err != nil || !slices.Equal(one.User.ExtraGroups, []string{"friends"}) {
		t.Errorf("user_get shows %+v (%v)", one.User, err)
	}
	var groups GroupsV
	if err := json.Unmarshal([]byte(mustOK(t, s, "groups_list", nil)), &groups); err != nil {
		t.Fatal(err)
	}
	for _, g := range groups.Groups {
		if g.ID == "grp_3" && g.ExtraUserCount != 2 {
			t.Errorf("groups_list: %+v", g)
		}
	}
	var one3 groupV
	if err := json.Unmarshal([]byte(mustOK(t, s, "group_get", map[string]any{"group_id": "grp_3"})), &one3); err != nil || one3.ExtraUserCount != 2 {
		t.Errorf("group_get: %+v (%v)", one3, err)
	}
}

func TestGroupDeleteLetsExtraMembersLeave(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	e.w.groups["grp_3"] = &adminv1.Group{Id: "grp_3", Name: "friends", ExtraUserCount: 2}
	p := planOf(t, s, "group_delete", map[string]any{"group_id": "grp_3"})
	if f := factOf(t, p, "extra_users"); f.Code != "users" || f.Params["n"] != "2" {
		t.Errorf("extra_users: %+v", f)
	}
	applyOf(t, s, "group_delete", p.ConfirmToken)
	if len(e.w.groupDeleteReq) != 1 || e.w.groupDeleteReq[0].GetMoveUsersTo() != "" {
		t.Errorf("delete requests: %+v", e.w.groupDeleteReq)
	}
	// the main members are still a refusal
	e.w.groups["grp_4"] = &adminv1.Group{Id: "grp_4", Name: "work", UserCount: 1, ExtraUserCount: 1}
	if out := mustFail(t, s, "group_delete_plan", map[string]any{"group_id": "grp_4"}); !strings.Contains(out, "group has users") {
		t.Errorf("a group with main members: %s", out)
	}
}

// The access report counts the people of a group by every group they are in, and puts a server under the groups that give it.
func TestAccessReportCountsExtraMembers(t *testing.T) {
	f := newFx().node("be", "BE", "online").group("family", "prf_vless").group("home", "prf_hy2")
	h := f.inbound("be", "prf_hy2", "hysteria2", 443)
	f.in.Users = append(f.in.Users,
		accUser{ID: "usr_alice", GroupID: "family", GroupIDs: []string{"family", "home"}, AllNodes: true, LastSeen: 1699990000},
		accUser{ID: "usr_bob", GroupID: "home", AllNodes: true, LastSeen: 1699990000})
	f.in.UsersTotal = len(f.in.Users)
	f.in.Totals[h] = hy2Good()
	f.rows("home", h, "be", accRow{Connects: 5, OnlineS: 100})
	r := buildAccess(f.in)

	var cell *accessCellV
	for i := range r.Matrix {
		if r.Matrix[i].Protocol == "hysteria2" {
			cell = &r.Matrix[i]
		}
	}
	if cell == nil || cell.GivenUsers != 2 {
		t.Fatalf("the hysteria2 server is given to both people: %+v", cell)
	}
	if len(cell.Groups) != 1 || cell.Groups[0].Group != "home" || cell.Groups[0].GivenUsers != 2 {
		t.Errorf("the server belongs to the group that gives it, with its main and extra member: %+v", cell.Groups)
	}
	a := &accessCtx{blockCtx: newBlockCtx(f.in), groupsByID: map[string]*accGroup{}}
	for i := range f.in.Groups {
		a.groupsByID[f.in.Groups[i].ID] = &f.in.Groups[i]
	}
	if got := a.givingGroups(&f.in.Users[0], &f.in.Inbounds[0]); !slices.Equal(got, []string{"home"}) {
		t.Errorf("groups giving alice the server = %v", got)
	}
}
