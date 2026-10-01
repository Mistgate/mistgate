package access

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// What a group gives and what a person gets: the reach of a group by app, the access of a user, the impact of
// a group change, a group deleted with its people moved, and every device of the user page in the admin card.

// mixed: de1 runs a hysteria2 and an AmneziaWG profile, nl1 the hysteria2 one only, "wait" is enrolled but not
// installed. "all" holds both profiles, "happ" the hysteria2 one, "later" a profile that runs nowhere yet.
type mixed struct {
	*awgFixture
	hy2, later              string
	all, happOnly, laterGrp string
	hy2de1                  string
}

func newMixed(t *testing.T) *mixed {
	f := newAWGFixture(t) // de1, the AWG profile "awg31" on it, group "default", Amnezia-only alice
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.node("nod_wait", "wait", "wait.example.com", "pending")
	hy2 := e.profile("hy2 443", "")
	in := e.inbound(hy2.Id, "nod_de1")
	e.inbound(hy2.Id, "nod_nl1")
	later := e.profile("later", `{"port":8443}`)
	e.inbound(later.Id, "nod_wait")
	return &mixed{awgFixture: f, hy2: hy2.Id, later: later.Id, hy2de1: in.Id,
		all: e.group("all", hy2.Id, f.profile), happOnly: e.group("happ", hy2.Id), laterGrp: e.group("later", later.Id)}
}

func (m *mixed) groups() map[string]*adminv1.Group {
	out := map[string]*adminv1.Group{}
	for _, g := range must(m.e.s.ListGroups(m.e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups {
		out[g.Id] = g
	}
	return out
}

func TestGroupReach(t *testing.T) {
	m := newMixed(t)
	gs := m.groups()
	for id, want := range map[string][2]uint32{m.all: {2, 1}, m.happOnly: {2, 0}, m.laterGrp: {0, 0}, m.group: {0, 1}} {
		if g := gs[id]; g.HappNodes != want[0] || g.AmneziaNodes != want[1] {
			t.Errorf("group %s reach = %d/%d, want %v", g.Name, g.HappNodes, g.AmneziaNodes, want)
		}
	}
	// A failed or switched-off inbound gives nothing: de1 drops out of the subscription's reach.
	m.e.sql(`UPDATE inbound SET state = 'failed' WHERE id = ?`, m.hy2de1)
	must(m.e.s.UpdateInbound(m.e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: m.inbound, Enabled: new(false)})))
	if g := m.groups()[m.all]; g.HappNodes != 1 || g.AmneziaNodes != 0 {
		t.Errorf("after the failure: %d/%d", g.HappNodes, g.AmneziaNodes)
	}
	// Create and update answer with the reach too.
	c := must(m.e.s.CreateGroup(m.e.ctx, req(&adminv1.CreateGroupRequest{Name: "new", ProfileIds: []string{m.hy2}}))).Msg.Group
	if c.HappNodes != 1 {
		t.Errorf("created group reach = %d", c.HappNodes)
	}
}

func TestUserAccess(t *testing.T) {
	m := newMixed(t)
	e := m.e
	both := e.user("both", m.all, nil).User
	amn := e.user("amn", m.all, amnOnly()).User
	nothing := e.user("nothing", m.laterGrp, nil).User
	nl1 := e.user("nl1 only", m.all, func(r *adminv1.CreateUserRequest) { r.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_nl1"}} }).User
	happNoAwg := e.user("happ group", m.happOnly, amnOnly()).User
	want := map[string][2]bool{both.Id: {true, true}, amn.Id: {false, true}, nothing.Id: {false, false}, nl1.Id: {true, false}, happNoAwg.Id: {false, false}}
	if both.AccessHapp != true || both.AccessAmnezia != true {
		t.Errorf("CreateUser answer: %v/%v", both.AccessHapp, both.AccessAmnezia)
	}
	for _, u := range must(e.s.ListUsers(e.ctx, req(&adminv1.ListUsersRequest{}))).Msg.Users {
		w, ok := want[u.Id]
		if ok && (u.AccessHapp != w[0] || u.AccessAmnezia != w[1]) {
			t.Errorf("%s: access %v/%v, want %v", u.Name, u.AccessHapp, u.AccessAmnezia, w)
		}
	}
	// The same answer as the subscription itself.
	v := must(e.s.Subscription(e.ctx, e.tokenOf(nl1.Id)))
	if !v.AccessHapp || v.AccessAmnezia {
		t.Errorf("subscription of nl1 only: %v/%v", v.AccessHapp, v.AccessAmnezia)
	}
	// A disabled user keeps the flags (the page shows the status instead); the detail carries them too.
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{both.Id}, Enabled: false})))
	if g := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: both.Id}))).Msg.User; !g.AccessHapp || !g.AccessAmnezia {
		t.Errorf("disabled user access: %v/%v", g.AccessHapp, g.AccessAmnezia)
	}
}

func TestGroupChangeImpact(t *testing.T) {
	m := newMixed(t)
	e := m.e
	marina := e.user("marina", m.all, nil).User
	must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: marina.Id, ProfileId: m.profile, Platform: "ios"})))
	must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: marina.Id, ProfileId: m.profile, Platform: "android"})))

	// The user card: moving Marina to "happ" costs her the AWG profile and its two keys; nothing is written.
	e.resetNotify()
	dry := must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: marina.Id, GroupId: new(m.happOnly), DryRun: true}))).Msg
	if dry.User.GroupId != m.all || dry.Impact == nil || dry.Impact.Users != 1 || len(dry.Impact.Gained) != 0 || len(dry.Impact.Lost) != 1 ||
		dry.Impact.Lost[0].Id != m.profile || dry.Impact.Lost[0].Name != "awg31" || dry.Impact.Lost[0].Protocol != "awg" || dry.Impact.Lost[0].AwgDevices != 2 {
		t.Fatalf("dry run = %+v", dry)
	}
	if got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: marina.Id}))).Msg.User; got.GroupId != m.all || e.notify.n.Load() != 0 {
		t.Error("the dry run changed the user or told the nodes")
	}
	_, err := e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: marina.Id, GroupId: new("grp_nope"), DryRun: true}))
	wantCode(t, err, connect.CodeNotFound)
	// The same group: no impact. Another field only: no impact either.
	if r := must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: marina.Id, GroupId: new(m.all), DryRun: true}))).Msg; r.Impact != nil {
		t.Errorf("same group impact = %v", r.Impact)
	}
	// The real change answers with the same impact and moves her.
	real := must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: marina.Id, GroupId: new(m.happOnly)}))).Msg
	if real.User.GroupId != m.happOnly || real.Impact == nil || real.Impact.Lost[0].AwgDevices != 2 || e.notify.n.Load() == 0 {
		t.Errorf("real change = %+v", real)
	}

	// The group editor: taking hysteria2 out of "happ" (Marina is there now) costs it for one person, no keys; the
	// later profile is gained.
	e.resetNotify()
	gd := must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: m.happOnly, ProfileIds: &adminv1.ProfileIds{Values: []string{m.later}}, DryRun: true}))).Msg
	if gd.Impact == nil || gd.Impact.Users != 1 || len(gd.Impact.Lost) != 1 || gd.Impact.Lost[0].Id != m.hy2 || gd.Impact.Lost[0].AwgDevices != 0 ||
		len(gd.Impact.Gained) != 1 || gd.Impact.Gained[0].Id != m.later || !slices.Equal(gd.Group.ProfileIds, []string{m.hy2}) {
		t.Fatalf("group dry run = %+v", gd)
	}
	if g := m.groups()[m.happOnly]; !slices.Equal(g.ProfileIds, []string{m.hy2}) || e.notify.n.Load() != 0 {
		t.Error("the group dry run changed the group or told the nodes")
	}
	_, err = e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: m.happOnly, ProfileIds: &adminv1.ProfileIds{Values: []string{"prf_nope"}}, DryRun: true}))
	wantCode(t, err, connect.CodeNotFound)
	// The keys of the group's people count: "all" losing the AWG profile would break nobody's now (Marina left).
	if r := must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: m.all, ProfileIds: &adminv1.ProfileIds{Values: []string{m.hy2}}, DryRun: true}))).Msg; r.Impact.Lost[0].AwgDevices != 0 || r.Impact.Users != 0 {
		t.Errorf("impact on an empty group = %+v", r.Impact)
	}
	// A rename has no impact.
	if r := must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: m.happOnly, Name: new("Happ"), DryRun: true}))).Msg; r.Impact != nil || r.Group.Name != "happ" {
		t.Errorf("rename dry run = %+v", r)
	}
}

func TestDeleteGroupMovesItsPeople(t *testing.T) {
	m := newMixed(t)
	e := m.e
	a := e.user("a", m.laterGrp, nil).User
	b := e.user("b", m.laterGrp, nil).User

	_, err := e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: m.laterGrp}))
	wantMsg(t, err, "failed_precondition: group_not_empty: users=2")
	_, err = e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: m.laterGrp, MoveUsersTo: m.laterGrp}))
	wantCode(t, err, connect.CodeInvalidArgument)
	// A target that does not exist moves nobody and deletes nothing.
	_, err = e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: m.laterGrp, MoveUsersTo: "grp_nope"}))
	wantCode(t, err, connect.CodeNotFound)
	if g, ok := m.groups()[m.laterGrp]; !ok || g.UserCount != 2 {
		t.Fatalf("after a refused move: %+v", g)
	}

	e.resetNotify()
	must(e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: m.laterGrp, MoveUsersTo: m.all})))
	gs := m.groups()
	if _, ok := gs[m.laterGrp]; ok || gs[m.all].UserCount != 2 || e.notify.n.Load() == 0 {
		t.Errorf("after the move: groups %v, notified %d", gs, e.notify.n.Load())
	}
	for _, id := range []string{a.Id, b.Id} {
		if u := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: id}))).Msg.User; u.GroupId != m.all || !u.AccessHapp {
			t.Errorf("%s: group %s access %v", u.Name, u.GroupId, u.AccessHapp)
		}
	}
	// An empty group goes without a target, as before.
	empty := e.group("empty")
	must(e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: empty})))
	_, err = e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: empty}))
	wantCode(t, err, connect.CodeNotFound)

	// A delete with a target tells the nodes even when the group read empty: the read comes before the move, and a user
	// created into the group in between is moved with the rest, so its access must follow (it cannot be counted here).
	racy := e.group("racy")
	e.resetNotify()
	must(e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: racy, MoveUsersTo: m.all})))
	if e.notify.n.Load() == 0 {
		t.Error("a delete with a target did not notify the fleet")
	}
}

func TestHysteria2CriticalChangeNamesEveryone(t *testing.T) {
	f := newFixture(t)
	e := f.e
	var names []string
	for _, n := range []string{"anna", "boris", "vera"} {
		u := e.user(n, f.group, nil).User
		names = append(names, n)
		if n == "boris" {
			e.online[u.Id] = f.nodeID
		}
	}
	dry := must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: f.profile, ExpectedVersion: 1, DryRun: true, SettingsJson: new(`{"port":9443,"obfs":{"password":"••••"}}`)}))).Msg
	// Everyone loses the server until the app refreshes the subscription, not only the person online.
	if dry.Impact.UsersOnline != 1 || !slices.Equal(dry.Impact.AffectedUserNames, names) || dry.Impact.InboundsRestarted != 1 {
		t.Errorf("impact = %+v", dry.Impact)
	}
}

func TestFailedInboundNamesItsNode(t *testing.T) {
	f := newFixture(t)
	f.e.sql(`UPDATE inbound SET state = 'failed', last_error = 'bind: address already in use' WHERE id = ?`, f.inbound)
	ps := must(f.e.s.ListProfiles(f.e.ctx, req(&adminv1.ListProfilesRequest{}))).Msg.Profiles
	if len(ps) != 1 || len(ps[0].Warnings) != 1 || ps[0].Warnings[0].Code != "inbound_failed" || ps[0].Warnings[0].Params["node"] != "de1" ||
		ps[0].Warnings[0].Params["error"] != "bind: address already in use" {
		t.Errorf("warnings = %+v", ps[0].Warnings)
	}
}

// The admin card lists every device the user's page lists: the subscription apps' device next to the AmneziaVPN keys.
func TestAdminListsEveryDeviceOfThePage(t *testing.T) {
	m := newMixed(t)
	e := m.e
	u := e.user("both", m.all, nil).User
	must(e.s.CreateAwgDevice(e.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u.Id, ProfileId: m.profile, Platform: "android", Label: "Pixel"})))
	page, _, err := e.s.PreviewSubscription(e.ctx, u.Id)
	if err != nil {
		t.Fatal(err)
	}
	admin := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.Devices
	var pageIDs, adminIDs []string
	for _, d := range page.Devices {
		pageIDs = append(pageIDs, d.ID)
	}
	implicit := 0
	for _, d := range admin {
		adminIDs = append(adminIDs, d.Id)
		if d.AwgProfileId == "" && slices.Contains(d.Protocols, "hysteria2") {
			implicit++
		}
	}
	slices.Sort(pageIDs)
	slices.Sort(adminIDs)
	if len(pageIDs) != 2 || !slices.Equal(pageIDs, adminIDs) || implicit != 1 {
		t.Errorf("page devices %v, admin devices %+v", pageIDs, admin)
	}
}
