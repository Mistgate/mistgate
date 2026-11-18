package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// randomGroups makes n groups from small pools of profiles and nodes, so that the cases that matter (the same profile in two
// groups, a node on one disabled list and not on the other) come up often. The seed is fixed: a failure repeats.
func randomGroups(r *rand.Rand, n int) AccessGroups {
	pick := func(prefix string, pool int) []string {
		var out []string
		for i := 0; i < pool; i++ {
			if r.IntN(2) == 0 {
				out = append(out, fmt.Sprintf("%s%d", prefix, i))
			}
		}
		return out
	}
	gs := make(AccessGroups, n)
	for i := range gs {
		gs[i] = AccessGroup{ID: fmt.Sprintf("grp_%d", i), ProfileIDs: pick("prf_", 4), DisabledNodeIDs: pick("nod_", 4)}
	}
	return gs
}

// One group gives exactly what the rule of a group has always been: it holds the profile and gets the node (and, for a twin
// through a relay, gets the entry node too).
func TestAccessGroupsOneGroupIsTheGroupRule(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		g := randomGroups(r, 1)[0]
		for p := 0; p < 5; p++ {
			profile := fmt.Sprintf("prf_%d", p)
			for n := 0; n < 5; n++ {
				node := fmt.Sprintf("nod_%d", n)
				want := slices.Contains(g.ProfileIDs, profile) && GroupGetsNode(g.DisabledNodeIDs, node)
				if got := (AccessGroups{g}).Gives(profile, node); got != want {
					t.Fatalf("Gives(%s, %s) = %v on %+v, want %v", profile, node, got, g, want)
				}
				for e := 0; e < 5; e++ {
					entry := fmt.Sprintf("nod_%d", e)
					if got, want := (AccessGroups{g}).Gives(profile, node, entry), want && GroupGetsNode(g.DisabledNodeIDs, entry); got != want {
						t.Fatalf("Gives(%s, %s, %s) = %v on %+v, want %v", profile, node, entry, got, g, want)
					}
				}
			}
			if got, want := (AccessGroups{g}).Holds(profile), slices.Contains(g.ProfileIDs, profile); got != want {
				t.Fatalf("Holds(%s) = %v on %+v", profile, got, g)
			}
		}
		if got := (AccessGroups{g}).ProfileIDs(); !slices.Equal(got, slices.Sorted(slices.Values(g.ProfileIDs))) && len(g.ProfileIDs) > 0 {
			t.Fatalf("ProfileIDs() = %v on %+v", got, g)
		}
	}
}

// Many groups give the union of what each gives, pair by pair: the profile and every node must come from the same group.
func TestAccessGroupsGivesIsTheUnionOfSingles(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 300 {
		gs := randomGroups(r, 1+r.IntN(4))
		for p := 0; p < 5; p++ {
			profile := fmt.Sprintf("prf_%d", p)
			for n := 0; n < 5; n++ {
				node := fmt.Sprintf("nod_%d", n)
				for e := 0; e < 5; e++ {
					entry := fmt.Sprintf("nod_%d", e)
					var any1, any2 bool
					for _, g := range gs {
						any1 = any1 || (AccessGroups{g}).Gives(profile, node)
						any2 = any2 || (AccessGroups{g}).Gives(profile, node, entry)
					}
					if got := gs.Gives(profile, node); got != any1 {
						t.Fatalf("Gives(%s, %s) = %v, want %v on %+v", profile, node, got, any1, gs)
					}
					if got := gs.Gives(profile, node, entry); got != any2 {
						t.Fatalf("Gives(%s, %s, %s) = %v, want %v on %+v", profile, node, entry, got, any2, gs)
					}
				}
			}
		}
	}
}

// The pair rule is not the flat union: a profile of one group on a node that only the other group gets is not given.
func TestAccessGroupsPairRuleIsNotTheFlatUnion(t *testing.T) {
	family := AccessGroup{ID: "grp_family", ProfileIDs: []string{"prf_hy2", "prf_vless"}, DisabledNodeIDs: []string{"nod_own1"}}
	home := AccessGroup{ID: "grp_alice_home", ProfileIDs: []string{"prf_awg_home"}}
	gs := AccessGroups{family, home}
	for _, c := range []struct {
		profile string
		nodes   []string
		want    bool
	}{
		{"prf_hy2", []string{"nod_de1"}, true},
		{"prf_hy2", []string{"nod_own1"}, false}, // own1 is held back from family; home does not hold hy2
		{"prf_awg_home", []string{"nod_own1"}, true},
		{"prf_awg_home", []string{"nod_de1"}, true},
		{"prf_hy2", []string{"nod_de1", "nod_own1"}, false}, // the same group must get the entry too
		{"prf_awg_home", []string{"nod_de1", "nod_own1"}, true},
		{"prf_unknown", []string{"nod_de1"}, false},
	} {
		if got := gs.Gives(c.profile, c.nodes...); got != c.want {
			t.Errorf("Gives(%s, %v) = %v, want %v", c.profile, c.nodes, got, c.want)
		}
	}
	if !slices.Equal(gs.ProfileIDs(), []string{"prf_awg_home", "prf_hy2", "prf_vless"}) {
		t.Errorf("ProfileIDs() = %v", gs.ProfileIDs())
	}
	if !gs.Holds("prf_awg_home") || gs.Holds("prf_x") {
		t.Error("Holds is wrong")
	}
}

func TestGroupsOfKeepsTheMainGroupFirst(t *testing.T) {
	byID := map[string]AccessGroup{"grp_a": {ID: "grp_a"}, "grp_b": {ID: "grp_b"}}
	if got := GroupsOf(AccessUser{GroupID: "grp_b"}, byID); len(got) != 1 || got[0].ID != "grp_b" {
		t.Errorf("GroupsOf = %+v", got)
	}
	if got := GroupsOf(AccessUser{GroupID: "grp_gone"}, byID); len(got) != 0 {
		t.Errorf("a person whose group is gone has groups: %+v", got)
	}
}
