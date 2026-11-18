package access

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A person in several groups (step 2: the server only; the groups come through the store, there is no API for them yet).
// Profile P on node N comes from a group of the person that holds P and gets N, the same group for both; what each group of
// the person hands out is added up. The proof is the person with two groups against two clones with one group each.

// extraGroups gives the person extra groups the way the store does it, and returns the person as stored.
func (e *env) extraGroups(userID string, add []string, remove ...string) {
	e.t.Helper()
	a := e.st.Access()
	u := must(a.User(e.ctx, userID))
	if err := a.UpdateUser(e.ctx, u, false, store.AccessGroupChange{Add: add, Remove: remove}); err != nil {
		e.t.Fatal(err)
	}
}

// pairWorld: nodes de1, nl1 and own1. Profiles hy (both groups hold it), fam (family alone) and home (alice-home alone), all
// Hysteria2 on their own ports; hy is on every node, fam and home on de1 and own1.
//
//	family     = {hy, fam}, own1 held back (disabled for it)
//	alice-home = {hy, home}
type pairWorld struct {
	*env
	family, home string
}

func newPairWorld(t *testing.T) *pairWorld {
	e := newEnv(t)
	for _, n := range []string{"de1", "nl1", "own1"} {
		e.node("nod_"+n, n, n+".example.com", "active")
	}
	hy := e.profile("hy 443", "")
	fam := e.profile("fam 8443", `{"port":8443}`)
	home := e.profile("home 9443", `{"port":9443}`)
	for _, n := range []string{"nod_de1", "nod_nl1", "nod_own1"} {
		e.inbound(hy.Id, n)
	}
	for _, n := range []string{"nod_de1", "nod_own1"} {
		e.inbound(fam.Id, n)
		e.inbound(home.Id, n)
	}
	w := &pairWorld{env: e, family: e.group("family", hy.Id, fam.Id), home: e.group("alice-home", hy.Id, home.Id)}
	must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: w.family, DisabledNodeIds: &adminv1.NodeIds{Values: []string{"nod_own1"}}})))
	return w
}

// served is what a person is shown in a format: "node/profile" of every server, sorted.
func (w *pairWorld) served(userID string, format plugin.ClientFormat) []string {
	w.t.Helper()
	v := must(w.s.SubscriptionWith(w.ctx, w.tokenOf(userID), SubOptions{Format: format, NoTouch: true, NoPageData: true}))
	var out []string
	for _, s := range v.Servers {
		out = append(out, s.Node+"/"+s.Profile)
	}
	slices.Sort(out)
	return out
}

// onNodes is what the nodes are told about a person: "node/profile" of every inbound they hold a credential on, sorted; it
// fails the test when an inbound lists one credential twice.
func (w *pairWorld) onNodes(userID string) []string {
	w.t.Helper()
	var out []string
	for _, n := range []string{"nod_de1", "nod_nl1", "nod_own1"} {
		for _, in := range must(w.s.Desired(w.ctx, n)) {
			seen := map[string]bool{}
			for _, c := range in.Creds {
				if seen[c.CredID] {
					w.t.Errorf("inbound %s lists credential %s twice", in.Spec.ID, c.CredID)
				}
				seen[c.CredID] = true
				if c.UserID == userID {
					out = append(out, strings.TrimPrefix(n, "nod_")+"/"+w.profileName(in.Spec.ProfileID))
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

func (w *pairWorld) profileName(id string) string {
	p := must(w.st.Access().Profile(w.ctx, id))
	return p.Name
}

func union(sets ...[]string) []string {
	var out []string
	for _, s := range sets {
		out = append(out, s...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestAPersonInTwoGroupsGetsWhatTwoClonesGet(t *testing.T) {
	w := newPairWorld(t)
	alice := w.user("alice", w.family, nil).User
	clonF := w.user("alice_f", w.family, nil).User
	clonH := w.user("alice_h", w.home, nil).User
	bob := w.user("bob", w.family, nil).User
	bobBefore := map[plugin.ClientFormat][]string{}
	for _, f := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo} {
		v := must(w.s.SubscriptionWith(w.ctx, w.tokenOf(bob.Id), SubOptions{Format: f, NoTouch: true, NoPageData: true}))
		bobBefore[f] = v.Lines
	}
	bobNodesBefore := w.onNodes(bob.Id)
	w.extraGroups(alice.Id, []string{w.home})

	for _, f := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
		got, want := w.served(alice.Id, f), union(w.served(clonF.Id, f), w.served(clonH.Id, f))
		if !slices.Equal(got, want) {
			t.Errorf("%s: alice is shown %v, the union of her clones is %v", f, got, want)
		}
	}
	// the pairs are not a flat union: the profile only family holds, deployed on the node only alice-home gets, is not given
	want := []string{"de1/fam 8443", "de1/home 9443", "de1/hy 443", "nl1/hy 443", "own1/home 9443", "own1/hy 443"}
	if got := w.served(alice.Id, plugin.FormatURIList); !slices.Equal(got, want) {
		t.Errorf("alice is shown %v, want %v", got, want)
	}
	// the nodes are told the same, each credential once even where both groups hold the profile
	if got, want := w.onNodes(alice.Id), union(w.onNodes(clonF.Id), w.onNodes(clonH.Id)); !slices.Equal(got, want) {
		t.Errorf("the nodes know alice as %v, the union of her clones is %v", got, want)
	}
	wantNodes := []string{"de1/fam 8443", "de1/home 9443", "de1/hy 443", "nl1/hy 443", "own1/home 9443", "own1/hy 443"}
	if got := w.onNodes(alice.Id); !slices.Equal(got, wantNodes) {
		t.Errorf("the nodes know alice as %v, want %v", got, wantNodes)
	}
	// whoever has one group is shown, and told to the nodes, what they were before
	for f, before := range bobBefore {
		if after := must(w.s.SubscriptionWith(w.ctx, w.tokenOf(bob.Id), SubOptions{Format: f, NoTouch: true, NoPageData: true})).Lines; !slices.Equal(after, before) {
			t.Errorf("%s: bob's subscription changed when alice got a group:\n%v\n%v", f, before, after)
		}
	}
	if got := w.onNodes(bob.Id); !slices.Equal(got, bobNodesBefore) {
		t.Errorf("the nodes' view of bob changed: %v -> %v", bobNodesBefore, got)
	}
	// the user page: the profiles are the union, the node access follows the pairs
	u := must(w.s.GetUser(w.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg
	var profiles []string
	for _, p := range u.Profiles {
		profiles = append(profiles, p.Name)
	}
	slices.Sort(profiles)
	if !slices.Equal(profiles, []string{"fam 8443", "home 9443", "hy 443"}) {
		t.Errorf("profiles on alice's page = %v", profiles)
	}
	for _, n := range u.NodeAccess {
		if n.NodeName == "own1" && !slices.Equal(n.Protocols, []string{"hysteria2"}) {
			t.Errorf("own1 on alice's page: %+v", n)
		}
	}
	if u.User.GroupId != w.family || u.User.GroupName != "family" {
		t.Errorf("the main group of alice is %s (%s)", u.User.GroupId, u.User.GroupName)
	}
	// the list says what the person's apps reach, with every group of theirs
	listed := must(w.s.ListUsers(w.ctx, req(&adminv1.ListUsersRequest{GroupId: w.home}))).Msg
	var inHome []string
	for _, x := range listed.Users {
		inHome = append(inHome, x.Name)
	}
	if slices.Sort(inHome); !slices.Equal(inHome, []string{"alice", "alice_h"}) {
		t.Errorf("the people of alice-home = %v, want the main and the extra members", inHome)
	}
	// a person keeps what the extra group gave only while they have it
	w.extraGroups(alice.Id, nil, w.home)
	if got, want := w.served(alice.Id, plugin.FormatURIList), w.served(clonF.Id, plugin.FormatURIList); !slices.Equal(got, want) {
		t.Errorf("after leaving alice-home alice is shown %v, want %v", got, want)
	}
	if got, want := w.onNodes(alice.Id), w.onNodes(clonF.Id); !slices.Equal(got, want) {
		t.Errorf("after leaving alice-home the nodes know alice as %v, want %v", got, want)
	}
}

func TestTheScalarsOfAPersonStayWithTheMainGroup(t *testing.T) {
	w := newPairWorld(t)
	must(w.s.UpdateGroup(w.ctx, req(&adminv1.UpdateGroupRequest{GroupId: w.family, Color: new("rose")})))
	must(w.s.UpdateGroup(w.ctx, req(&adminv1.UpdateGroupRequest{GroupId: w.home, Color: new("sky")})))
	alice := w.user("alice", w.family, nil).User
	w.extraGroups(alice.Id, []string{w.home})
	got := must(w.s.GetUser(w.ctx, req(&adminv1.GetUserRequest{UserId: alice.Id}))).Msg.User
	if got.GroupId != w.family || got.GroupName != "family" || got.GroupColor != "rose" {
		t.Errorf("alice's group = %s %s %s: the main group decides the name and the colour", got.GroupId, got.GroupName, got.GroupColor)
	}
}

// The relay twin of a server is given by one group for the server, its node and the entry node.
func TestARelayTwinComesFromOneGroup(t *testing.T) {
	f := newTwinFix(t)
	famP := f.profile("fam", `{"port":8443}`)
	homeP := f.profile("home", `{"port":9443}`)
	famIn := f.inbound(famP.Id, "nod_de1")
	homeIn := f.inbound(homeP.Id, "nod_de1")
	f.switchOn(must(f.create(famIn.Id, "nod_fi1", 7443)).Id)
	f.switchOn(must(f.create(homeIn.Id, "nod_fi1", 7444)).Id)
	family, home := f.group("family", famP.Id), f.group("alice-home", homeP.Id)
	// family gets neither the entry nor... it holds fam; fi1 is held back from it; alice-home gets fi1
	must(f.s.UpdateGroup(f.ctx, req(&adminv1.UpdateGroupRequest{GroupId: family, DisabledNodeIds: &adminv1.NodeIds{Values: []string{"nod_fi1"}}})))
	alice := f.user("alice", family, nil).User
	clonF := f.user("alice_f", family, nil).User
	clonH := f.user("alice_h", home, nil).User
	f.extraGroups(alice.Id, []string{home})
	shown := func(id string) []string {
		var out []string
		for _, s := range f.twinView(f.tokenOf(id), plugin.FormatURIList).Servers {
			out = append(out, s.Profile+"/"+s.Mark)
		}
		slices.Sort(out)
		return out
	}
	if got, want := shown(clonF.Id), []string{"fam/"}; !slices.Equal(got, want) {
		t.Fatalf("a person of family is shown %v, want %v (no twin: family does not get the entry)", got, want)
	}
	if got, want := shown(clonH.Id), []string{"home/", "home/via:FI"}; !slices.Equal(got, want) {
		t.Fatalf("a person of alice-home is shown %v, want %v", got, want)
	}
	// alice: the twin of fam through fi1 is not given (family holds fam, alice-home gets fi1, but not the same group); the twin
	// of home is
	if got, want := shown(alice.Id), union(shown(clonF.Id), shown(clonH.Id)); !slices.Equal(got, want) {
		t.Errorf("alice is shown %v, the union of her clones is %v", got, want)
	}
	if got := shown(alice.Id); slices.Contains(got, "fam/via:FI") {
		t.Errorf("alice has a twin of fam through fi1: %v", got)
	}
}

// An AmneziaWG key follows the profile: it can be made while a group of the person holds the profile, and it goes with it.
func TestAnAWGKeyFollowsTheGroupThatHoldsTheProfile(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	other := e.group("other") // no profile
	u := must(e.st.Access().User(e.ctx, f.user))
	u.GroupID = other
	if err := e.st.Access().UpdateUser(e.ctx, u, false, store.AccessGroupChange{}); err != nil {
		t.Fatal(err)
	}
	// the main group has no profile and the person has no extra group yet: no key
	if err := f.addErr(f.user); err == nil || !strings.Contains(err.Error(), "profile_not_in_group") {
		t.Fatalf("a key of a profile no group holds: %v", err)
	}
	e.extraGroups(f.user, []string{f.group})
	r := f.add(f.user, "")
	if got := f.desired(); len(got) != 1 || got[0].UserID != f.user || got[0].DeviceID != r.Device.Id {
		t.Fatalf("the node does not have the key of the extra group's profile: %+v", got)
	}
	if len(r.Configs) != 1 {
		t.Fatalf("configs = %d", len(r.Configs))
	}
	// the per-profile count of a group takes in its extra members
	counts, err := e.st.Access().AWGDevicesPerProfile(e.ctx, f.group, "")
	if err != nil || counts[f.profile] != 1 {
		t.Errorf("AWGDevicesPerProfile(group) = %v, %v: the extra member's key is not counted", counts, err)
	}
	// the page of the device still renders
	if _, err := e.s.loadDevice(e.ctx, f.user, r.Device.Id); err != nil {
		t.Fatalf("the device of the extra group: %v", err)
	}
	// leaving the group: the profile is no longer given, the device cannot be rendered, and the node loses the key
	e.extraGroups(f.user, nil, f.group)
	if _, err := e.s.loadDevice(e.ctx, f.user, r.Device.Id); err == nil || !strings.Contains(err.Error(), "profile_not_in_group") {
		t.Errorf("loadDevice after leaving the group = %v, want profile_not_in_group", err)
	}
	if got := f.desired(); len(got) != 0 {
		t.Errorf("the node still has the key after the group was left: %+v", got)
	}
	if counts, err = e.st.Access().AWGDevicesPerProfile(e.ctx, f.group, ""); err != nil || counts[f.profile] != 0 {
		t.Errorf("AWGDevicesPerProfile(group) after leaving = %v, %v", counts, err)
	}
}

// The Telegram proxy of a person comes from any of their groups.
func TestTelegramLinksComeFromAnyGroupOfThePerson(t *testing.T) {
	f := newTGFix(t)
	plain := f.env.group("plain", f.hy.Id) // hy only
	alice := f.userBare("alice", plain, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true}
	}).User
	if r := f.links(alice); len(r.Links) != 0 {
		t.Fatalf("telegram links of a group without the proxy: %+v", r.Links)
	}
	f.extraGroups(alice.Id, []string{f.group})
	if r := f.links(alice); len(r.Links) != 2 {
		t.Fatalf("telegram links with the extra group that holds the proxy on two nodes: %+v", r.Links)
	}
	if u := f.user(alice); !u.AccessTelegram {
		t.Error("the list does not say the person reaches Telegram through the extra group")
	}
	f.extraGroups(alice.Id, nil, f.group)
	if r := f.links(alice); len(r.Links) != 0 {
		t.Errorf("telegram links after leaving the group: %+v", r.Links)
	}
}

// The people a node is handed out to are counted once, whatever the number of their groups: a person has the node when one of
// their groups gets it.
func TestSharingCountsAPersonOnce(t *testing.T) {
	w := newPairWorld(t) // family does not get own1, alice-home does
	alice := w.user("alice", w.family, nil).User
	w.user("bob", w.family, nil)
	w.user("carol", w.home, nil)
	w.extraGroups(alice.Id, []string{w.home})
	dry := func(mode adminv1.NodeShareMode, groups ...string) *adminv1.SetNodeSharingResponse {
		return must(w.s.SetNodeSharing(w.ctx, req(&adminv1.SetNodeSharingRequest{NodeId: "nod_own1", Mode: mode, GroupIds: groups, DryRun: true}))).Msg
	}
	// to every group: alice and carol have it already, bob gets it
	if r := dry(shareAll); r.PeopleReceiving != 3 || r.PeopleGain != 1 || r.PeopleLose != 0 {
		t.Errorf("all: receiving %d, gain %d, lose %d; want 3, 1, 0", r.PeopleReceiving, r.PeopleGain, r.PeopleLose)
	}
	// to family only: alice keeps it (through family now), bob gets it, carol loses it
	if r := dry(shareListed, w.family); r.PeopleReceiving != 2 || r.PeopleGain != 1 || r.PeopleLose != 1 {
		t.Errorf("family only: receiving %d, gain %d, lose %d; want 2, 1, 1", r.PeopleReceiving, r.PeopleGain, r.PeopleLose)
	}
	// to nobody: alice and carol lose it
	if r := dry(shareNone); r.PeopleReceiving != 0 || r.PeopleGain != 0 || r.PeopleLose != 2 {
		t.Errorf("none: receiving %d, gain %d, lose %d; want 0, 0, 2", r.PeopleReceiving, r.PeopleGain, r.PeopleLose)
	}
}

// Deleting a group: the people who have it as the main group are the ones that need a place; the ones that have it as an extra
// group just leave it.
func TestDeletingAGroupOfExtraMembers(t *testing.T) {
	w := newPairWorld(t)
	alice := w.user("alice", w.family, nil).User
	w.extraGroups(alice.Id, []string{w.home})
	if g := w.groupOf(w.home); g.UserCount != 0 || g.ExtraUserCount != 1 {
		t.Fatalf("the extra member is not counted: %+v", g)
	}
	// the extra member of alice-home gets and loses what it gives; it can go, nobody has it as the main group
	w.resetNotify()
	must(w.s.DeleteGroup(w.ctx, req(&adminv1.DeleteGroupRequest{GroupId: w.home})))
	if w.notify.n.Load() == 0 {
		t.Error("the nodes were not told that a person lost the group's access")
	}
	if got := must(w.st.Access().User(w.ctx, alice.Id)); got.GroupID != w.family || len(got.ExtraGroupIDs) != 0 {
		t.Errorf("alice after the group went: main %s, extra %v", got.GroupID, got.ExtraGroupIDs)
	}
	// a group with people as the main group is not deleted
	_, err := w.s.DeleteGroup(w.ctx, req(&adminv1.DeleteGroupRequest{GroupId: w.family}))
	wantCode(t, err, connect.CodeFailedPrecondition)
}

func (e *env) groupOf(id string) store.AccessGroup {
	e.t.Helper()
	return must(e.st.Access().Group(e.ctx, id))
}
