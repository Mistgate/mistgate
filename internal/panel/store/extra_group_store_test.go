package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// The extra groups of a person (user_extra_group) on a store: the rule of what a person is given, pair by pair, in SQL and in Go,
// and the counters, the filter, the deletion and the invariant that go with it. Run on SQLite (extra_group_test.go) and on D1
// (d1_store_test.go).

var egNow = time.Unix(1_800_000_000, 0).UTC()

// egWorld: nodes de1, nl1 and own1; hy2 and vless profiles on de1 and own1, the home profile on both, hy2 also on nl1.
//
//	family  = {hy, vl}, own1 held back (on its disabled list)
//	friends = {hy}
//	home    = {hy, home}
type egWorld struct {
	t  *testing.T
	st *Store
	a  Access
}

func newEGWorld(t *testing.T, st *Store) *egWorld {
	t.Helper()
	w := &egWorld{t: t, st: st, a: st.Access()}
	for _, n := range []string{"de1", "nl1", "own1"} {
		relayExec(t, st, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, '192.0.2.10', 'active', 1)`, "nod_"+n, n)
	}
	for _, p := range [][2]string{{"hy", "hysteria2"}, {"vl", "vless"}, {"home", "hysteria2"}} {
		relayExec(t, st, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, ?, ?, '{}', 1, 1)`, "prf_"+p[0], p[1], p[0])
	}
	for _, in := range [][2]string{{"hy", "de1"}, {"vl", "de1"}, {"home", "de1"}, {"hy", "nl1"}, {"hy", "own1"}, {"vl", "own1"}, {"home", "own1"}} {
		relayExec(t, st, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, "inb_"+in[1]+"_"+in[0], "prf_"+in[0], "nod_"+in[1])
	}
	w.group("family", []string{"prf_hy", "prf_vl"}, "nod_own1")
	w.group("friends", []string{"prf_hy"})
	w.group("home", []string{"prf_hy", "prf_home"})
	return w
}

func (w *egWorld) group(name string, profiles []string, disabled ...string) {
	w.t.Helper()
	if err := w.a.CreateGroup(context.Background(), AccessGroup{ID: "grp_" + name, Name: name, ProfileIDs: profiles, DisabledNodeIDs: disabled, CreatedAt: egNow}); err != nil {
		w.t.Fatal(err)
	}
}

// user makes a person with a device that holds a Hysteria2 and a VLESS credential, so that every profile of the world has a
// credential to give.
func (w *egWorld) user(name, group string, extras ...string) AccessUser {
	w.t.Helper()
	u := AccessUser{ID: "usr_" + name, Name: name, GroupID: "grp_" + group, Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "month", PeriodStart: egNow, DeviceLimit: 5, SubTokenHash: []byte("tok-" + name), SubTokenEnc: []byte("sealed"), CreatedAt: egNow}
	dev := AccessDevice{ID: "dev_" + name, UserID: u.ID, Implicit: true, CreatedAt: egNow}
	creds := []AccessCred{
		{ID: "crd_" + name + "_hy", DeviceID: dev.ID, UserID: u.ID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: egNow},
		{ID: "crd_" + name + "_vl", DeviceID: dev.ID, UserID: u.ID, Protocol: "vless", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: egNow},
	}
	if err := w.a.CreateUser(context.Background(), u, dev, creds); err != nil {
		w.t.Fatal(err)
	}
	if len(extras) > 0 {
		w.addExtra(u.ID, extras...)
	}
	got, err := w.a.User(context.Background(), u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

// change applies a group change through the same UpdateUser the panel uses, on the person as stored now.
func (w *egWorld) change(userID, mainGroup string, c AccessGroupChange) error {
	w.t.Helper()
	u, err := w.a.User(context.Background(), userID)
	if err != nil {
		w.t.Fatal(err)
	}
	was := u
	if mainGroup != "" {
		u.GroupID = "grp_" + mainGroup
	}
	return w.a.UpdateUser(context.Background(), u, was, false, c)
}

func (w *egWorld) addExtra(userID string, groups ...string) {
	w.t.Helper()
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = "grp_" + g
	}
	if err := w.change(userID, "", AccessGroupChange{Add: ids}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *egWorld) extras(userID string) []string {
	w.t.Helper()
	u, err := w.a.User(context.Background(), userID)
	if err != nil {
		w.t.Fatal(err)
	}
	return u.ExtraGroupIDs
}

// desired is what every node is told: per node, the rows of DesiredCreds.
func (w *egWorld) desired() map[string][]AccessDesiredCred {
	w.t.Helper()
	out := map[string][]AccessDesiredCred{}
	for _, n := range []string{"nod_de1", "nod_nl1", "nod_own1"} {
		rows, err := w.a.DesiredCreds(context.Background(), n)
		if err != nil {
			w.t.Fatal(err)
		}
		out[n] = rows
	}
	return out
}

// pairs are the (inbound, protocol) pairs the person has on a node, sorted; it fails the test when one credential is listed
// twice for the same inbound.
func egPairs(t *testing.T, rows []AccessDesiredCred, userID string) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.UserID != userID {
			continue
		}
		key := r.InboundID + "/" + r.CredID
		if seen[key] {
			t.Errorf("credential %s is listed twice for inbound %s", r.CredID, r.InboundID)
		}
		seen[key] = true
		out = append(out, strings.TrimPrefix(r.InboundID, "inb_")+"/"+r.Protocol)
	}
	sort.Strings(out)
	return out
}

func egRowsOf(rows []AccessDesiredCred, userIDs ...string) []AccessDesiredCred {
	var out []AccessDesiredCred
	for _, r := range rows {
		if slices.Contains(userIDs, r.UserID) {
			out = append(out, r)
		}
	}
	return out
}

func egUnion(sets ...[]string) []string {
	var out []string
	for _, s := range sets {
		out = append(out, s...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// extraGroupStoreProof runs the whole proof on a store opened by open (a fresh one for each part that needs it).
func extraGroupStoreProof(t *testing.T, open func(*testing.T) *Store) {
	ctx := context.Background()

	t.Run("OneGroupIsAsBefore", func(t *testing.T) {
		w := newEGWorld(t, open(t))
		// alice is in family and home; her clones are in one group each; bob and carol have one group
		alice := w.user("alice", "family")
		w.user("alice_f", "family")
		w.user("alice_h", "home")
		w.user("bob", "friends")
		w.user("carol", "friends")
		before := w.desired()
		for node, rows := range before {
			if len(rows) == 0 && node != "nod_nl1" {
				t.Fatalf("no desired creds on %s: the world is empty", node)
			}
		}
		w.addExtra(alice.ID, "home")
		w.addExtra("usr_carol", "home")
		after := w.desired()
		// (a) whoever has one group is told exactly what they were told before: the rows are the same, field by field
		for node := range before {
			for _, one := range []string{"usr_alice_f", "usr_alice_h", "usr_bob"} {
				if got, want := egRowsOf(after[node], one), egRowsOf(before[node], one); !reflect.DeepEqual(got, want) {
					t.Errorf("%s of %s changed when other people got a group:\n got %+v\nwant %+v", node, one, got, want)
				}
			}
		}
		// (b) alice gets the union of what her two clones get, on every node; (г) every credential once
		for node, rows := range after {
			want := egUnion(egPairs(t, rows, "usr_alice_f"), egPairs(t, rows, "usr_alice_h"))
			if got := egPairs(t, rows, alice.ID); !slices.Equal(got, want) {
				t.Errorf("%s: alice = %v, want the union of her clones %v", node, got, want)
			}
		}
		// the held node: family does not get own1, home does, so alice has on own1 exactly what home gives, hy and home
		// (carol: friends gets own1 and holds hy only; home adds the home profile there)
		if got, want := egPairs(t, after["nod_own1"], alice.ID), []string{"own1_hy/hysteria2", "own1_home/hysteria2"}; !slices.Equal(got, egUnion(want)) {
			t.Errorf("alice on own1 = %v, want %v", got, egUnion(want))
		}
		if got := egPairs(t, after["nod_own1"], "usr_alice_f"); len(got) != 0 {
			t.Errorf("a person of family only has %v on a node that family does not get", got)
		}
		// the profile family holds, deployed on a node only the other group gets, is not given through that group
		if got := egPairs(t, after["nod_own1"], alice.ID); slices.Contains(got, "own1_vl/vless") {
			t.Errorf("alice has the vless profile of family on own1: %v", got)
		}
		for node, who := range map[string]map[string]bool{"nod_own1": {"usr_alice": true, "usr_alice_h": true, "usr_bob": true, "usr_carol": true}} {
			for _, id := range []string{"usr_alice", "usr_alice_f", "usr_alice_h", "usr_bob", "usr_carol"} {
				has, err := w.a.HasUserCredentialOnNode(ctx, node, id)
				if err != nil || has != who[id] {
					t.Errorf("HasUserCredentialOnNode(%s, %s) = %v, %v, want %v", node, id, has, err, who[id])
				}
			}
		}
	})

	t.Run("CountersFilterAndAudience", func(t *testing.T) {
		w := newEGWorld(t, open(t))
		alice := w.user("alice", "family", "home")
		w.user("alice_h", "home")
		w.user("bob", "friends")
		w.user("carol", "family", "friends", "home")
		groups, err := w.a.Groups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string][2]int{}
		for _, g := range groups {
			counts[g.ID] = [2]int{g.UserCount, g.ExtraUserCount}
		}
		if want := map[string][2]int{"grp_family": {2, 0}, "grp_friends": {1, 1}, "grp_home": {1, 2}}; !reflect.DeepEqual(counts, want) {
			t.Errorf("user counts (main, extra) = %v, want %v", counts, want)
		}
		if g, err := w.a.Group(ctx, "grp_home"); err != nil || g.UserCount != 1 || g.ExtraUserCount != 2 {
			t.Errorf("Group(home) = %+v, %v", g, err)
		}
		// the person's groups: the main one first, then the others by id
		if got, err := w.a.UserGroups(ctx, mustUser(t, w, "usr_carol")); err != nil || len(got) != 3 || got[0].ID != "grp_family" || got[1].ID != "grp_friends" || got[2].ID != "grp_home" {
			t.Errorf("UserGroups(carol) = %+v, %v", got, err)
		}
		// the filter: members of the group, the main and the extra, each once, sorted by name, the pages joined by the cursor
		page, more, c, err := w.a.ListUsers(ctx, AccessUserQuery{GroupID: "grp_home", Limit: 10, Now: egNow})
		if err != nil || more || c.All != 3 || len(page) != 3 || page[0].Name != "alice" || page[1].Name != "alice_h" || page[2].Name != "carol" {
			t.Errorf("ListUsers(home) = %+v more %v counts %+v, %v", names(page), more, c, err)
		}
		var walked []string
		after := ""
		for {
			p, more, _, err := w.a.ListUsers(ctx, AccessUserQuery{GroupID: "grp_home", Limit: 1, After: after, Now: egNow})
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range p {
				walked = append(walked, u.Name)
				after = u.Name
			}
			if !more {
				break
			}
		}
		if !slices.Equal(walked, []string{"alice", "alice_h", "carol"}) {
			t.Errorf("ListUsers(home) walked page by page = %v", walked)
		}
		// the list carries the extra groups of each person
		if ids := page[0].ExtraGroupIDs; !slices.Equal(ids, []string{"grp_home"}) {
			t.Errorf("alice in the list: extra groups %v", ids)
		}
		// the audience of a node: a person once, with their groups, the main first
		aud, err := w.a.NodeAudience(ctx, "nod_own1")
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string][]string{"usr_alice": {"grp_family", "grp_home"}, "usr_alice_h": {"grp_home"}, "usr_bob": {"grp_friends"},
			"usr_carol": {"grp_family", "grp_friends", "grp_home"}}; !reflect.DeepEqual(aud, want) {
			t.Errorf("NodeAudience(own1) = %v, want %v", aud, want)
		}
		// the people of a profile: alice holds hy through two groups and is listed once
		users, err := w.a.ProfileUsers(ctx, "prf_hy", true, false, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := briefNames(users); !slices.Equal(got, []string{"alice", "alice_h", "bob", "carol"}) {
			t.Errorf("ProfileUsers(hy) = %v", got)
		}
		if users, err = w.a.ProfileUsers(ctx, "prf_home", true, false, false, false); err != nil || !slices.Equal(briefNames(users), []string{"alice", "alice_h", "carol"}) {
			t.Errorf("ProfileUsers(home) = %v, %v", briefNames(users), err)
		}
		// the relay snapshot reads the groups of each person
		ru, err := w.a.RelayReachabilityUsers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range ru {
			if u.ID == alice.ID && !slices.Equal(u.GroupIDs, []string{"grp_family", "grp_home"}) {
				t.Errorf("RelayReachabilityUsers: alice has groups %v", u.GroupIDs)
			}
		}
		audience, err := w.a.RelayAudienceUsers(ctx, []string{"nod_own1"})
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range audience {
			if u.ID == "usr_carol" && !slices.Equal(u.ExtraGroupIDs, []string{"grp_friends", "grp_home"}) {
				t.Errorf("RelayAudienceUsers: carol has extra groups %v", u.ExtraGroupIDs)
			}
		}
		byToken, err := w.a.UserByTokenHash(ctx, []byte("tok-alice"))
		if err != nil || !slices.Equal(byToken.ExtraGroupIDs, []string{"grp_home"}) {
			t.Errorf("UserByTokenHash(alice) = %+v, %v", byToken.ExtraGroupIDs, err)
		}
		data, err := w.a.SubscriptionData(ctx, alice.ID, egNow, true, egNow, nil)
		if err != nil || len(data.Groups) != 2 || data.Groups[0].ID != "grp_family" || data.Groups[1].ID != "grp_home" ||
			!slices.Equal(data.Groups[1].ProfileIDs, []string{"prf_home", "prf_hy"}) || !slices.Equal(data.Groups[0].DisabledNodeIDs, []string{"nod_own1"}) {
			t.Errorf("SubscriptionData groups = %+v, %v", data.Groups, err)
		}
		// someone who is not active reads no groups
		if data, err = w.a.SubscriptionData(ctx, alice.ID, egNow, false, egNow, nil); err != nil || len(data.Groups) != 0 {
			t.Errorf("SubscriptionData of an inactive person has groups %+v, %v", data.Groups, err)
		}
	})

	t.Run("MainGroupIsNeverAnExtraGroup", func(t *testing.T) {
		w := newEGWorld(t, open(t))
		w.user("alice", "family", "home", "friends")
		// the main group is not added as an extra group
		w.addExtra("usr_alice", "family")
		if got := w.extras("usr_alice"); !slices.Equal(got, []string{"grp_friends", "grp_home"}) {
			t.Errorf("after adding the main group: %v", got)
		}
		// adding twice is the same as once; removing what is not there does nothing
		w.addExtra("usr_alice", "home")
		if err := w.change("usr_alice", "", AccessGroupChange{Remove: []string{"grp_family", "grp_nope"}}); err != nil {
			t.Fatal(err)
		}
		if got := w.extras("usr_alice"); !slices.Equal(got, []string{"grp_friends", "grp_home"}) {
			t.Errorf("after a repeat: %v", got)
		}
		// an extra group that becomes the main one leaves the extra groups, the old main one goes (it is not kept)
		if err := w.change("usr_alice", "home", AccessGroupChange{}); err != nil {
			t.Fatal(err)
		}
		u := mustUser(t, w, "usr_alice")
		if u.GroupID != "grp_home" || !slices.Equal(u.ExtraGroupIDs, []string{"grp_friends"}) {
			t.Errorf("after making home the main group: main %s, extra %v", u.GroupID, u.ExtraGroupIDs)
		}
		// the old main one can be asked to stay, in the same change
		if err := w.change("usr_alice", "friends", AccessGroupChange{Add: []string{"grp_home"}}); err != nil {
			t.Fatal(err)
		}
		if u = mustUser(t, w, "usr_alice"); u.GroupID != "grp_friends" || len(u.ExtraGroupIDs) != 1 || u.ExtraGroupIDs[0] != "grp_home" {
			t.Errorf("after swapping: main %s, extra %v", u.GroupID, u.ExtraGroupIDs)
		}
		// a group that does not exist is refused and changes nothing
		if err := w.change("usr_alice", "", AccessGroupChange{Add: []string{"grp_nope"}, Remove: []string{"grp_home"}}); err != ErrNotFound {
			t.Errorf("adding a missing group = %v, want ErrNotFound", err)
		}
		if got := w.extras("usr_alice"); !slices.Equal(got, []string{"grp_home"}) {
			t.Errorf("a refused change was applied in part: %v", got)
		}
		// no row of the table is a main group
		var n int
		if err := w.st.R.QueryRowContext(ctx, `SELECT count(*) FROM user_extra_group x JOIN user u ON u.id = x.user_id AND u.group_id = x.group_id`).Scan(&n); err != nil || n != 0 {
			t.Errorf("rows that hold a main group: %d, %v", n, err)
		}
	})

	t.Run("DeleteGroup", func(t *testing.T) {
		w := newEGWorld(t, open(t))
		w.group("only_extra", []string{"prf_hy"})
		w.user("alice", "family", "only_extra", "friends")
		w.user("bob", "friends", "home")
		w.user("carol", "home")
		// a group nobody has as the main one goes at once; its extra members just leave it
		if err := w.a.DeleteGroup(ctx, "grp_only_extra", ""); err != nil {
			t.Fatalf("deleting a group of extra members: %v", err)
		}
		if got := w.extras("usr_alice"); !slices.Equal(got, []string{"grp_friends"}) {
			t.Errorf("alice after the group went: %v", got)
		}
		// a group that has people as the main group is refused, extra members or not
		if err := w.a.DeleteGroup(ctx, "grp_friends", ""); err != ErrAccessInUse {
			t.Errorf("deleting a group with people = %v, want ErrAccessInUse", err)
		}
		// moving the people: bob has home as an extra group and is moved there; he has it as the main one only. alice, who has
		// friends as an extra group, leaves it without being moved, and stays where she is.
		if err := w.a.DeleteGroup(ctx, "grp_friends", "grp_home"); err != nil {
			t.Fatal(err)
		}
		bob := mustUser(t, w, "usr_bob")
		if bob.GroupID != "grp_home" || len(bob.ExtraGroupIDs) != 0 {
			t.Errorf("bob after the move: main %s, extra %v", bob.GroupID, bob.ExtraGroupIDs)
		}
		if alice := mustUser(t, w, "usr_alice"); alice.GroupID != "grp_family" || len(alice.ExtraGroupIDs) != 0 {
			t.Errorf("alice after friends went: main %s, extra %v", alice.GroupID, alice.ExtraGroupIDs)
		}
		if g, err := w.a.Group(ctx, "grp_home"); err != nil || g.UserCount != 2 || g.ExtraUserCount != 0 {
			t.Errorf("home after the move: %+v, %v", g, err)
		}
		var n int
		if err := w.st.R.QueryRowContext(ctx, `SELECT count(*) FROM user_extra_group x JOIN user u ON u.id = x.user_id AND u.group_id = x.group_id`).Scan(&n); err != nil || n != 0 {
			t.Errorf("rows that hold a main group: %d, %v", n, err)
		}
		// moving to a group that does not exist is refused and moves nobody
		if err := w.a.DeleteGroup(ctx, "grp_family", "grp_nope"); err != ErrNotFound {
			t.Errorf("moving to a missing group = %v, want ErrNotFound", err)
		}
		if got := mustUser(t, w, "usr_alice").GroupID; got != "grp_family" {
			t.Errorf("alice was moved by a refused delete: %s", got)
		}
	})

	t.Run("MatchesTheRuleInGo", func(t *testing.T) { extraGroupRuleProperty(t, open, 4) })
}

func mustUser(t *testing.T, w *egWorld, id string) AccessUser {
	t.Helper()
	u, err := w.a.User(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func names(us []AccessUser) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Name
	}
	return out
}

func briefNames(bs []AccessBrief) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Name
	}
	return out
}

// extraGroupRuleProperty: on random worlds (the seed is fixed), a person has a credential on an inbound in DesiredCreds exactly
// when the rule in Go says so: some group of theirs holds the profile and gets the node, they use the node, are active, and
// have a credential of the protocol. The two places that say it must never differ on any (person, inbound) pair.
func extraGroupRuleProperty(t *testing.T, open func(*testing.T) *Store, worlds int) {
	ctx := context.Background()
	for seed := range worlds {
		r := rand.New(rand.NewPCG(uint64(seed)+7, 99))
		st := open(t)
		a := st.Access()
		nodes := []string{"nod_a", "nod_b", "nod_c"}
		profiles := []struct{ id, protocol string }{{"prf_0", "hysteria2"}, {"prf_1", "hysteria2"}, {"prf_2", "vless"}, {"prf_3", "vless"}}
		for _, n := range nodes {
			relayExec(t, st, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, '192.0.2.10', 'active', 1)`, n, n)
		}
		for _, p := range profiles {
			relayExec(t, st, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, ?, ?, '{}', 1, 1)`, p.id, p.protocol, p.id)
		}
		for _, n := range nodes {
			for _, p := range profiles {
				if r.IntN(3) > 0 {
					relayExec(t, st, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, "inb_"+p.id+n, p.id, n)
				}
			}
		}
		var groupIDs []string
		for i := 0; i < 5; i++ {
			g := AccessGroup{ID: fmt.Sprintf("grp_%d", i), Name: fmt.Sprintf("g%d", i), CreatedAt: egNow}
			for _, p := range profiles {
				if r.IntN(2) == 0 {
					g.ProfileIDs = append(g.ProfileIDs, p.id)
				}
			}
			for _, n := range nodes {
				if r.IntN(3) == 0 {
					g.DisabledNodeIDs = append(g.DisabledNodeIDs, n)
				}
			}
			if err := a.CreateGroup(ctx, g); err != nil {
				t.Fatal(err)
			}
			groupIDs = append(groupIDs, g.ID)
		}
		var userIDs []string
		for i := 0; i < 8; i++ {
			name := fmt.Sprintf("u%d", i)
			u := AccessUser{ID: "usr_" + name, Name: name, GroupID: groupIDs[r.IntN(len(groupIDs))], Status: "active", AppHapp: true, AppAmnezia: true,
				AllNodes: r.IntN(2) == 0, QuotaReset: "month", PeriodStart: egNow, DeviceLimit: 5, SubTokenHash: []byte("tok-" + name), SubTokenEnc: []byte("sealed"), CreatedAt: egNow}
			if !u.AllNodes {
				for _, n := range nodes {
					if r.IntN(2) == 0 {
						u.NodeIDs = append(u.NodeIDs, n)
					}
				}
			}
			if r.IntN(8) == 0 {
				u.Status = "disabled"
				u.Disabled = true
			}
			dev := AccessDevice{ID: "dev_" + name, UserID: u.ID, Implicit: true, CreatedAt: egNow}
			creds := []AccessCred{
				{ID: "crd_" + name + "_hy", DeviceID: dev.ID, UserID: u.ID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: egNow},
				{ID: "crd_" + name + "_vl", DeviceID: dev.ID, UserID: u.ID, Protocol: "vless", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: egNow},
			}
			if err := a.CreateUser(ctx, u, dev, creds); err != nil {
				t.Fatal(err)
			}
			var add []string
			for _, g := range groupIDs {
				if g != u.GroupID && r.IntN(3) == 0 {
					add = append(add, g)
				}
			}
			stored, err := a.User(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.UpdateUser(ctx, stored, stored, false, AccessGroupChange{Add: add}); err != nil {
				t.Fatal(err)
			}
			userIDs = append(userIDs, u.ID)
		}

		groups, err := a.Groups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]AccessGroup{}
		for _, g := range groups {
			byID[g.ID] = g
		}
		users, err := a.UsersByIDs(ctx, userIDs)
		if err != nil {
			t.Fatal(err)
		}
		inbounds, err := a.InboundsFull(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{} // person/inbound
		for _, n := range nodes {
			rows, err := a.DesiredCreds(ctx, n)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, row := range rows {
				if seen[row.InboundID+"/"+row.CredID] {
					t.Errorf("seed %d: credential %s listed twice for %s", seed, row.CredID, row.InboundID)
				}
				seen[row.InboundID+"/"+row.CredID] = true
				got[row.UserID+"/"+row.InboundID] = true
			}
		}
		checked, given := 0, 0
		for _, u := range users {
			gs := GroupsOf(u, byID)
			for _, f := range inbounds {
				want := u.Status == "active" && gs.Gives(f.Profile.ID, f.Node.ID) && (u.AllNodes || slices.Contains(u.NodeIDs, f.Node.ID))
				key := u.ID + "/" + f.Inbound.ID
				if got[key] != want {
					t.Errorf("seed %d: %s on %s (profile %s, node %s): DesiredCreds says %v, the rule in Go says %v (groups %v)",
						seed, u.Name, f.Inbound.ID, f.Profile.ID, f.Node.ID, got[key], want, append([]string{u.GroupID}, u.ExtraGroupIDs...))
				}
				checked++
				if want {
					given++
				}
				has, err := a.HasUserCredentialOnNode(ctx, f.Node.ID, u.ID)
				if err != nil {
					t.Fatal(err)
				}
				// HasUserCredentialOnNode answers for the node: true when the person has any inbound there
				if wantNode := slices.ContainsFunc(inbounds, func(x AccessInboundFull) bool {
					return x.Node.ID == f.Node.ID && got[u.ID+"/"+x.Inbound.ID]
				}); has != wantNode {
					t.Errorf("seed %d: HasUserCredentialOnNode(%s, %s) = %v, DesiredCreds says %v", seed, f.Node.ID, u.Name, has, wantNode)
				}
			}
		}
		if given == 0 || given == checked {
			t.Errorf("seed %d: the world gives %d of %d pairs, it proves nothing", seed, given, checked)
		}
	}
}
