package access

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/nodeshare"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Who a node is handed out to: the node-side call (SetNodeSharing), the rule that decides what a group
// receives, and what a held node does to subscriptions, keys, proxies, counters and the nodes the agents are told to serve.

const (
	shareAll    = adminv1.NodeShareMode_NODE_SHARE_MODE_ALL
	shareListed = adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED
	shareNone   = adminv1.NodeShareMode_NODE_SHARE_MODE_NONE
)

func (e *env) setShare(node string, mode adminv1.NodeShareMode, groups ...string) *adminv1.SetNodeSharingResponse {
	e.t.Helper()
	return must(e.s.SetNodeSharing(e.ctx, req(&adminv1.SetNodeSharingRequest{NodeId: node, Mode: mode, GroupIds: groups}))).Msg
}

// shareOf is the mode of a node and the names of the groups that receive it, by name.
func (e *env) shareOf(node string) (adminv1.NodeShareMode, []string) {
	e.t.Helper()
	shares, err := e.st.NodeShares(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, g := range must(e.s.ListGroups(e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups {
		if !slices.Contains(g.DisabledNodeIds, node) {
			names = append(names, g.Name)
		}
	}
	slices.Sort(names)
	return nodeshare.Mode(shares[node].Mode()), names
}

// holdNode makes a node one the owner holds back, as the second node of a panel is made.
func (e *env) holdNode(id string) {
	e.t.Helper()
	e.sql(`UPDATE node SET hold_new_groups = 1 WHERE id = ?`, id)
	e.sql(`INSERT OR IGNORE INTO user_group_disabled_node (group_id, node_id) SELECT id, ? FROM user_group`, id)
}

// TestNodeShareRule is the table of the rule: the mode a node is set to, a group's own switch for it, and a group made later.
func TestNodeShareRule(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(e *env, family, friends string)
		mode  adminv1.NodeShareMode
		want  []string // groups that receive the node, new group "later" included
	}{
		{"a node nobody changed goes to every group", func(*env, string, string) {}, shareAll, []string{"family", "friends", "later"}},
		{"all", func(e *env, f, g string) { e.setShare("nod_nl1", shareAll) }, shareAll, []string{"family", "friends", "later"}},
		{"all, friends switched it off", func(e *env, f, g string) {
			e.setShare("nod_nl1", shareAll)
			must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: g, DisabledNodeIds: &adminv1.NodeIds{Values: []string{"nod_nl1"}}})))
		}, shareAll, []string{"family", "later"}},
		{"listed: only the named group, a later one gets nothing", func(e *env, f, g string) { e.setShare("nod_nl1", shareListed, f) }, shareListed, []string{"family"}},
		{"none", func(e *env, f, g string) { e.setShare("nod_nl1", shareNone) }, shareNone, nil},
		{"all wipes the switches of the groups", func(e *env, f, g string) {
			e.setShare("nod_nl1", shareNone)
			e.setShare("nod_nl1", shareAll)
		}, shareAll, []string{"family", "friends", "later"}},
		{"listed after none", func(e *env, f, g string) {
			e.setShare("nod_nl1", shareNone)
			e.setShare("nod_nl1", shareListed, g)
		}, shareListed, []string{"friends"}},
		{"none after listed", func(e *env, f, g string) {
			e.setShare("nod_nl1", shareListed, f, g)
			e.setShare("nod_nl1", shareNone)
		}, shareNone, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.node("nod_de1", "de1", "de1.example.com", "active")
			e.node("nod_nl1", "nl1", "nl1.example.com", "active")
			family, friends := e.group("family"), e.group("friends")
			c.setup(e, family, friends)
			e.group("later") // a group made after the node was handed out
			mode, got := e.shareOf("nod_nl1")
			if mode != c.mode || !slices.Equal(got, c.want) {
				t.Fatalf("mode %v, groups %v; want %v, %v", mode, got, c.mode, c.want)
			}
			// the groups' own lists say the same, and de1, which nobody touched, is on every group
			for _, g := range must(e.s.ListGroups(e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups {
				if on := !slices.Contains(g.DisabledNodeIds, "nod_nl1"); on != slices.Contains(c.want, g.Name) {
					t.Errorf("group %s: nl1 on = %v", g.Name, on)
				}
				if slices.Contains(g.DisabledNodeIds, "nod_de1") {
					t.Errorf("group %s lost de1", g.Name)
				}
			}
		})
	}
}

// TestSetNodeSharingCountsPeople: the answer says how many people gain and lose the node, a dry run writes nothing, and a
// person who picked other nodes is not counted.
func TestSetNodeSharingCountsPeople(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	family, friends := e.group("family"), e.group("friends")
	e.user("alice", family, nil)
	e.user("bob", family, nil)
	e.user("carol", friends, nil)
	e.user("dave", friends, func(m *adminv1.CreateUserRequest) { m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_de1"}} }) // not on nl1
	e.setShare("nod_nl1", shareNone)
	e.resetNotify()

	dry := must(e.s.SetNodeSharing(e.ctx, req(&adminv1.SetNodeSharingRequest{NodeId: "nod_nl1", Mode: shareListed, GroupIds: []string{family, friends}, DryRun: true}))).Msg
	if dry.PeopleGain != 3 || dry.PeopleLose != 0 || dry.Mode != shareListed || dry.PeopleReceiving != 3 || len(dry.GroupIds) != 2 {
		t.Fatalf("dry run: %+v", dry)
	}
	if mode, got := e.shareOf("nod_nl1"); mode != shareNone || len(got) != 0 || e.notify.n.Load() != 0 {
		t.Fatalf("a dry run changed something: %v %v notified %d", mode, got, e.notify.n.Load())
	}

	got := e.setShare("nod_nl1", shareListed, family, friends)
	if got.PeopleGain != 3 || got.PeopleLose != 0 || e.notify.n.Load() == 0 {
		t.Fatalf("applied: gain %d lose %d notified %d", got.PeopleGain, got.PeopleLose, e.notify.n.Load())
	}
	// friends only: family's two people lose it, carol keeps it
	got = e.setShare("nod_nl1", shareListed, friends)
	if got.PeopleGain != 0 || got.PeopleLose != 2 {
		t.Fatalf("narrowed: gain %d lose %d", got.PeopleGain, got.PeopleLose)
	}
	e.resetNotify()
	if again := e.setShare("nod_nl1", shareListed, friends); again.PeopleGain+again.PeopleLose != 0 || e.notify.n.Load() != 0 {
		t.Fatalf("the same share again: %+v notified %d", again, e.notify.n.Load())
	}
}

func TestSetNodeSharingRefusals(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_old", "old", "old.example.com", "retired")
	family := e.group("family")
	set := func(node string, mode adminv1.NodeShareMode, groups ...string) error {
		_, err := e.s.SetNodeSharing(e.ctx, req(&adminv1.SetNodeSharingRequest{NodeId: node, Mode: mode, GroupIds: groups}))
		return err
	}
	wantCode(t, set("nod_de1", adminv1.NodeShareMode_NODE_SHARE_MODE_UNSPECIFIED), connect.CodeInvalidArgument)
	wantCode(t, set("nod_de1", shareAll, family), connect.CodeInvalidArgument)
	wantCode(t, set("nod_de1", shareNone, family), connect.CodeInvalidArgument)
	wantCode(t, set("nod_de1", shareListed), connect.CodeInvalidArgument)
	wantCode(t, set("nod_de1", shareListed, "grp_nobody"), connect.CodeNotFound)
	wantCode(t, set("nod_nobody", shareAll), connect.CodeNotFound)
	wantMsg(t, set("nod_old", shareAll), "failed_precondition: node_retired")
}

// A group page cannot hand out a held node by saving: the form may have been loaded before the node was made.
func TestGroupUpdateCannotSwitchOnAHeldNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	family, friends := e.group("family"), e.group("friends")
	e.setShare("nod_nl1", shareListed, family)
	update := func(group string, disabled ...string) error {
		_, err := e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: group, DisabledNodeIds: &adminv1.NodeIds{Values: disabled}}))
		return err
	}
	// friends was not given nl1: a list without it would switch it on
	wantMsg(t, update(friends), "failed_precondition: node_held: node=nod_nl1")
	wantMsg(t, update(friends, "nod_de1"), "failed_precondition: node_held: node=nod_nl1")
	if _, got := e.shareOf("nod_nl1"); !slices.Equal(got, []string{"family"}) {
		t.Fatalf("a refused update changed the share: %v", got)
	}
	// a dry run is refused the same way, so a plan never promises it
	_, err := e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: friends, DisabledNodeIds: &adminv1.NodeIds{}, DryRun: true}))
	wantCode(t, err, connect.CodeFailedPrecondition)
	// a list that keeps it off is fine, and so is switching a node off for the group that has it
	if err := update(friends, "nod_nl1"); err != nil {
		t.Fatal(err)
	}
	if err := update(family, "nod_nl1"); err != nil {
		t.Fatal(err)
	}
	if mode, got := e.shareOf("nod_nl1"); mode != shareNone || len(got) != 0 {
		t.Fatalf("after family switched it off: %v %v", mode, got)
	}
	// a node that is not held is as before
	if err := update(friends, "nod_nl1", "nod_de1"); err != nil {
		t.Fatal(err)
	}
	if err := update(friends, "nod_nl1"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateGroupDoesNotGetAHeldNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.node("nod_old", "old", "old.example.com", "retired")
	e.holdNode("nod_nl1")
	e.sql(`UPDATE node SET hold_new_groups = 1 WHERE id = 'nod_old'`)
	g := must(e.s.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: "family"}))).Msg.Group
	if !slices.Equal(g.DisabledNodeIds, []string{"nod_nl1"}) {
		t.Fatalf("a new group's disabled nodes: %v (de1 is not held, a retired node is not worth a row)", g.DisabledNodeIds)
	}
}

// heldFixture: de1 and nl1 run hysteria2, nl1 also an MTProto proxy; alice and bob are in family. nl1 is handed to nobody.
func TestHeldNodeReachesNobody(t *testing.T) {
	f := newTGFix(t)
	alice := f.person("alice", true)
	bob := f.person("bob", true)
	nodes := func(u *adminv1.User) []string {
		var out []string
		for _, l := range f.links(u).Links {
			out = append(out, l.NodeName)
		}
		slices.Sort(out)
		return out
	}
	f.inbound(f.hy.Id, "nod_nl1")
	subNodes := func(token string) []string {
		var out []string
		for _, s := range must(f.s.Subscription(f.ctx, token)).Servers {
			if !slices.Contains(out, s.Node) {
				out = append(out, s.Node)
			}
		}
		slices.Sort(out)
		return out
	}
	if got := subNodes(f.tokens["alice"]); !slices.Equal(got, []string{"de1", "nl1"}) || !slices.Equal(nodes(alice), []string{"de1", "nl1"}) {
		t.Fatalf("before: subscription %v, proxies %v", got, nodes(alice))
	}
	if g := must(f.s.ListGroups(f.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups[0]; g.HappNodes != 2 || g.TelegramNodes != 2 {
		t.Fatalf("reach before: %d happ, %d telegram", g.HappNodes, g.TelegramNodes)
	}

	f.setShare("nod_nl1", shareNone)

	if got := subNodes(f.tokens["alice"]); !slices.Equal(got, []string{"de1"}) {
		t.Errorf("subscription of a held node: %v", got)
	}
	if got := nodes(alice); !slices.Equal(got, []string{"de1"}) {
		t.Errorf("Telegram proxies of a held node: %v", got)
	}
	if keys := f.mtprotoKeys("nod_nl1"); len(keys) != 0 {
		t.Errorf("the held node is told to accept %d proxy keys", len(keys))
	}
	hy := func(node string) map[string]bool {
		out := map[string]bool{}
		for _, in := range must(f.s.Desired(f.ctx, node)) {
			for _, c := range in.Creds {
				out[c.UserID] = true
			}
		}
		return out
	}
	if got := hy("nod_nl1"); len(got) != 0 {
		t.Errorf("the held node is told to serve %v", got)
	}
	if got := hy("nod_de1"); !got[alice.Id] || !got[bob.Id] {
		t.Errorf("de1 serves %v", got)
	}
	g := must(f.s.ListGroups(f.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups[0]
	if g.HappNodes != 1 || g.TelegramNodes != 1 {
		t.Errorf("reach of the group: %d happ, %d telegram", g.HappNodes, g.TelegramNodes)
	}
	// the person's own page: no protocol is offered on the held node
	for _, na := range must(f.s.GetUser(f.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg.NodeAccess {
		if na.NodeName == "nl1" && len(na.Protocols) != 0 {
			t.Errorf("the page offers %v on the held node", na.Protocols)
		}
		if na.NodeName == "de1" && len(na.Protocols) == 0 {
			t.Error("the page lost de1")
		}
	}

	// handed to one group, it comes back for its people only
	other := f.env.group("friends", f.tg.Id, f.hy.Id)
	carol := must(f.s.CreateUser(f.ctx, req(&adminv1.CreateUserRequest{Name: "carol", GroupId: other, Apps: &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true}}))).Msg.User
	f.setShare("nod_nl1", shareListed, f.group)
	if got := nodes(alice); !slices.Equal(got, []string{"de1", "nl1"}) {
		t.Errorf("after the node was given to alice's group: %v", got)
	}
	if got := nodes(carol); !slices.Equal(got, []string{"de1"}) {
		t.Errorf("a group that was not given it: %v", got)
	}
	if got := f.mtprotoKeys("nod_nl1"); !strings.Contains(strings.Join(mapKeys(got), ","), alice.Id) || len(got) != 2 {
		t.Errorf("proxy keys on nl1: %v", got)
	}
}

func mapKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A person's AmneziaWG key is made of the nodes the group receives: a held node is not in it, and comes into it when given.
func TestHeldNodeIsNotInAnAWGKey(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.inbound(f.profile, "nod_nl1")
	d := f.add(f.user, "phone").Device
	if got := len(f.deviceConfs(d.Id)); got != 2 {
		t.Fatalf("configs before: %d, want one per node", got)
	}
	e.setShare("nod_nl1", shareNone)
	if got := len(f.deviceConfs(d.Id)); got != 1 {
		t.Errorf("configs with nl1 held: %d, want 1", got)
	}
	e.setShare("nod_nl1", shareListed, f.group)
	if got := len(f.deviceConfs(d.Id)); got != 2 {
		t.Errorf("configs with nl1 given to the group: %d, want 2", got)
	}
}

// Asked with the node's current mode and groups, a dry run reads who gets the node now.
func TestSetNodeSharingDryRunReadsTheNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	family, friends := e.group("family"), e.group("friends")
	e.user("alice", family, nil)
	e.user("bob", friends, nil)
	e.setShare("nod_nl1", shareListed, family)
	r := must(e.s.SetNodeSharing(e.ctx, req(&adminv1.SetNodeSharingRequest{NodeId: "nod_nl1", Mode: shareListed, GroupIds: []string{family}, DryRun: true}))).Msg
	if r.Mode != shareListed || !slices.Equal(r.GroupIds, []string{family}) || r.PeopleReceiving != 1 || r.PeopleGain != 0 || r.PeopleLose != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestNodeShareChoiceStore(t *testing.T) {
	// the choice a job keeps round-trips; anything unreadable is the automatic one
	c := store.NodeShareChoice{Mode: store.ShareListed, GroupIDs: []string{"grp_a"}}
	if got := store.ParseNodeShareChoice(c.JSON()); got.Mode != c.Mode || !slices.Equal(got.GroupIDs, c.GroupIDs) {
		t.Fatalf("round trip: %+v", got)
	}
	if got := store.ParseNodeShareChoice("{nope"); got.Mode != "" {
		t.Fatalf("unreadable: %+v", got)
	}
}

// Every change of a share is audited with the node, the mode and the groups, and nothing else; a dry run writes nothing.
func TestSetNodeSharingIsAudited(t *testing.T) {
	e := newEnv(t)
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	family := e.group("family")
	must(e.s.SetNodeSharing(e.ctx, req(&adminv1.SetNodeSharingRequest{NodeId: "nod_nl1", Mode: shareNone, DryRun: true})))
	if n := e.count(`SELECT count(*) FROM audit WHERE action = 'node_sharing_set'`); n != 0 {
		t.Fatalf("a dry run was audited: %d rows", n)
	}
	e.setShare("nod_nl1", shareListed, family)
	e.setShare("nod_nl1", shareNone)
	var params string
	if err := e.st.R.QueryRow(`SELECT params FROM audit WHERE action = 'node_sharing_set' ORDER BY id LIMIT 1`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if want := `{"groups":["` + family + `"],"mode":"listed","name":"nl1","node":"nod_nl1"}`; params != want {
		t.Fatalf("audit params %s, want %s", params, want)
	}
	if n := e.count(`SELECT count(*) FROM audit WHERE action = 'node_sharing_set'`); n != 2 {
		t.Fatalf("audit rows: %d, want 2", n)
	}
}
