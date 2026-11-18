//go:build !js

package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func shareT(t *testing.T, s *Store) (map[string]NodeShare, []AccessGroup) {
	t.Helper()
	shares, err := s.NodeShares(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.Access().Groups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return shares, groups
}

func newNodeT(t *testing.T, s *Store, name string, share NodeShareChoice) {
	t.Helper()
	now := time.Unix(1_000, 0)
	if _, err := s.CreateEnrollment(context.Background(), &NodeRow{ID: "nod_" + name, Name: name, Address: name + ".example.com", Share: share}, "", []byte("tok-"+name), "adm_test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func groupT(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.Access().CreateGroup(context.Background(), AccessGroup{ID: "grp_" + name, Name: name, CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
}

// The first node of a panel goes to every group, a later one to none, and what the request says wins; a retired node is not
// the first one.
func TestNewNodeShare(t *testing.T) {
	s := openTemp(t)
	groupT(t, s, "family")
	groupT(t, s, "friends")

	newNodeT(t, s, "de1", NodeShareChoice{})
	newNodeT(t, s, "nl1", NodeShareChoice{})
	newNodeT(t, s, "fi1", NodeShareChoice{Mode: ShareAll})
	newNodeT(t, s, "se1", NodeShareChoice{Mode: ShareListed, GroupIDs: []string{"grp_friends"}})
	newNodeT(t, s, "pl1", NodeShareChoice{Mode: ShareNone})
	shares, groups := shareT(t, s)
	for id, want := range map[string]string{"nod_de1": ShareAll, "nod_nl1": ShareNone, "nod_fi1": ShareAll, "nod_se1": ShareListed, "nod_pl1": ShareNone} {
		if got := shares[id].Mode(); got != want {
			t.Errorf("%s: mode %s, want %s", id, got, want)
		}
	}
	if got := shares["nod_se1"]; got.Receiving != 1 || got.Groups != 2 || got.ChangedAt.Unix() != 1_000 {
		t.Errorf("listed node: %+v", got)
	}
	// a group made later is on the disabled list of the held nodes only
	groupT(t, s, "later")
	_, groups = shareT(t, s)
	for _, g := range groups {
		if g.Name == "later" && !slices.Equal(g.DisabledNodeIDs, []string{"nod_nl1", "nod_pl1", "nod_se1"}) {
			t.Errorf("a group made later starts with %v disabled", g.DisabledNodeIDs)
		}
	}

	// the only node left is retired: the next one is the first again
	s2 := openTemp(t)
	groupT(t, s2, "family")
	newNodeT(t, s2, "de1", NodeShareChoice{})
	if _, err := s2.W.ExecContext(context.Background(), `UPDATE node SET state = 'retired', retired_at = 1 WHERE id = 'nod_de1'`); err != nil {
		t.Fatal(err)
	}
	newNodeT(t, s2, "nl1", NodeShareChoice{})
	if shares, _ := shareT(t, s2); shares["nod_nl1"].Mode() != ShareAll {
		t.Errorf("first live node after a retired one: %s", shares["nod_nl1"].Mode())
	}
}

func TestSetNodeShareIsAtomicAndChecksTheNode(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	groupT(t, s, "family")
	newNodeT(t, s, "de1", NodeShareChoice{})
	newNodeT(t, s, "nl1", NodeShareChoice{})
	a := s.Access()
	if err := a.SetNodeShare(ctx, "nod_nobody", NodeShareChoice{Mode: ShareAll}, time.Unix(5_000, 0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE node SET state = 'retired', retired_at = 1 WHERE id = 'nod_nl1'`); err != nil {
		t.Fatal(err)
	}
	if err := a.SetNodeShare(ctx, "nod_nl1", NodeShareChoice{Mode: ShareAll}, time.Unix(5_000, 0)); !errors.Is(err, ErrNodeRetired) {
		t.Fatalf("retired node: %v", err)
	}
	if shares, _ := shareT(t, s); shares["nod_nl1"].Mode() != ShareNone || shares["nod_nl1"].ChangedAt.Unix() == 5_000 {
		t.Fatalf("a refused change wrote: %+v", shares["nod_nl1"])
	}
	// a listed name that is no group is not a row and not an error: the node goes to the listed groups that exist
	if err := a.SetNodeShare(ctx, "nod_de1", NodeShareChoice{Mode: ShareListed, GroupIDs: []string{"grp_gone", "grp_family"}}, time.Unix(6_000, 0)); err != nil {
		t.Fatal(err)
	}
	if shares, _ := shareT(t, s); shares["nod_de1"].Mode() != ShareListed || shares["nod_de1"].Receiving != 1 || shares["nod_de1"].ChangedAt.Unix() != 6_000 {
		t.Fatalf("%+v", shares["nod_de1"])
	}
}

// 00146 leaves every node as it was: handed to every group, with the groups' own switches kept.
func TestNodeShareMigrationKeepsExistingNodes(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 145); err != nil {
		t.Fatalf("down to 145: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', 'de1.example.com', 'active', 10), ('nod_nl1', 'nl1', 'nl1.example.com', 'active', 20)`,
		`INSERT INTO user_group (id, name, created_at) VALUES ('grp_family', 'family', 1), ('grp_friends', 'friends', 1)`,
		`INSERT INTO user_group_disabled_node (group_id, node_id) VALUES ('grp_friends', 'nod_nl1')`,
	} {
		if _, err := s.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 146); err != nil {
		t.Fatalf("up to 146: %v", err)
	}
	shares, groups := shareT(t, s)
	for _, id := range []string{"nod_de1", "nod_nl1"} {
		if sh := shares[id]; sh.Mode() != ShareAll || sh.Hold {
			t.Errorf("%s after the migration: %+v", id, sh)
		}
	}
	if shares["nod_nl1"].Receiving != 1 || shares["nod_de1"].Receiving != 2 || shares["nod_nl1"].ChangedAt.Unix() != 20 {
		t.Errorf("receiving: de1 %d, nl1 %d, changed %v", shares["nod_de1"].Receiving, shares["nod_nl1"].Receiving, shares["nod_nl1"].ChangedAt)
	}
	for _, g := range groups {
		want := []string{}
		if g.Name == "friends" {
			want = []string{"nod_nl1"}
		}
		if !slices.Equal(g.DisabledNodeIDs, want) {
			t.Errorf("group %s: disabled %v, want %v", g.Name, g.DisabledNodeIDs, want)
		}
	}
	// and a group made after it still gets every node
	groupT(t, s, "later")
	_, groups = shareT(t, s)
	if i := slices.IndexFunc(groups, func(g AccessGroup) bool { return g.Name == "later" }); i < 0 || len(groups[i].DisabledNodeIDs) != 0 {
		t.Errorf("a new group after the migration: %+v", groups)
	}
}

func TestGroupGetsNode(t *testing.T) {
	g := AccessGroup{DisabledNodeIDs: []string{"nod_nl1"}}
	if !g.GetsNode("nod_de1") || g.GetsNode("nod_nl1") || !(AccessGroup{}).GetsNode("nod_nl1") {
		t.Fatal("a group receives every node but the ones on its list")
	}
}

// An SSH install keeps the share it was asked for until its node is made, and the automatic choice stays automatic.
func TestProvisionJobKeepsItsShare(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	for i, want := range []NodeShareChoice{{}, {Mode: ShareNone}, {Mode: ShareListed, GroupIDs: []string{"grp_family", "grp_friends"}}} {
		id := []string{"prv_a", "prv_b", "prv_c"}[i]
		job := NodeProvisionJob{ID: id, NodeID: "nod_" + id, Name: "n" + id, Address: "a.example.com", SSHHost: "h.example.com", SSHPort: 22,
			HostFingerprint: "SHA256:x", Secret: []byte("sealed"), CreatedBy: "adm_test", CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(10, 0), Sharing: want}
		if err := s.CreateNodeProvisionJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		got, err := s.NodeProvisionJob(ctx, id)
		if err != nil || got.Sharing.Mode != want.Mode || !slices.Equal(got.Sharing.GroupIDs, want.GroupIDs) {
			t.Errorf("job %s: share %+v, want %+v (%v)", id, got.Sharing, want, err)
		}
	}
}
