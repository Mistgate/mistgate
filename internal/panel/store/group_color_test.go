//go:build !js

package store

import (
	"context"
	"slices"
	"testing"
)

func groupColors(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.W.QueryContext(context.Background(), `SELECT id, color FROM user_group`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, c string
		if err := rows.Scan(&id, &c); err != nil {
			t.Fatal(err)
		}
		out[id] = c
	}
	return out
}

// 00045 gives the groups that exist already distinct tones, oldest first (then by id), wrapping after the palette, and
// rolls back.
func TestGroupColorMigrationSpreadsExistingGroups(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 44); err != nil {
		t.Fatalf("up to 44: %v", err)
	}
	// created_at ties are broken by id; grp_g is the oldest
	for _, g := range []struct {
		id string
		at int
	}{{"grp_b", 5}, {"grp_a", 5}, {"grp_g", 1}, {"grp_c", 6}, {"grp_d", 7}, {"grp_e", 8}, {"grp_f", 9}} {
		execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES (?, ?, ?)`, g.id, g.id, g.at)
	}
	if _, err := p.UpTo(ctx, 45); err != nil {
		t.Fatalf("up to 45: %v", err)
	}
	got := groupColors(t, s)
	want := map[string]string{"grp_g": "lavender", "grp_a": "sand", "grp_b": "sage", "grp_c": "rose", "grp_d": "sky", "grp_e": "mint", "grp_f": "lavender"}
	for id, c := range want {
		if got[id] != c {
			t.Errorf("%s = %q, want %q", id, got[id], c)
		}
	}
	// the first six never share a tone
	seen := map[string]bool{}
	for _, id := range []string{"grp_g", "grp_a", "grp_b", "grp_c", "grp_d", "grp_e"} {
		if seen[got[id]] {
			t.Errorf("%s repeats %s", id, got[id])
		}
		seen[got[id]] = true
	}
	if _, err := p.DownTo(ctx, 44); err != nil {
		t.Fatalf("down again: %v", err)
	}
	if _, err := p.UpTo(ctx, 45); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestNewGroupTakesTheLeastUsedTone(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	a := s.Access()
	add := func(id, color string) string {
		t.Helper()
		if err := a.CreateGroup(ctx, AccessGroup{ID: id, Name: id, Color: color}); err != nil {
			t.Fatal(err)
		}
		g, err := a.Group(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return g.Color
	}
	var first []string
	for _, id := range []string{"grp_1", "grp_2", "grp_3", "grp_4", "grp_5", "grp_6"} {
		first = append(first, add(id, ""))
	}
	if !slices.Equal(first, GroupTones) {
		t.Fatalf("the first six groups wear %v, want the palette %v", first, GroupTones)
	}
	if got := add("grp_7", ""); got != "lavender" { // all used once: the earliest
		t.Errorf("seventh = %q", got)
	}
	// grp_3 (sage) takes lavender: sage is now unused and wins over the earlier, busier tones
	lav := "lavender"
	if err := a.UpdateGroup(ctx, "grp_3", nil, nil, nil, &lav); err != nil {
		t.Fatal(err)
	}
	if got := add("grp_8", ""); got != "sage" {
		t.Errorf("after sage was freed, a new group wears %q", got)
	}
	// a colour asked for is kept
	if got := add("grp_9", "mint"); got != "mint" {
		t.Errorf("explicit colour = %q", got)
	}
}
