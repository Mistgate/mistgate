//go:build !js

package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// The public label of a node (migration 00129): a node from before has none, the label round-trips through the node row and the
// access view of it, and the column refuses more than 40 characters whatever wrote it.
func TestNodePublicLabel(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, st.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 128); err != nil {
		t.Fatalf("down to the schema before the label: %v", err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_old', 'de1', 'de1.example.com', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("apply the label: %v", err)
	}
	if n, err := st.Node(ctx, "nod_old"); err != nil || n.PublicLabel != "" {
		t.Fatalf("a node from before the label: %q, %v", n.PublicLabel, err)
	}

	label := "Fast"
	if n, err := st.UpdateNode(ctx, "nod_old", NodePatch{PublicLabel: &label}); err != nil || n.PublicLabel != "Fast" {
		t.Fatalf("set: %q, %v", n.PublicLabel, err)
	}
	if n, err := st.UpdateNode(ctx, "nod_old", NodePatch{Notes: &label}); err != nil || n.PublicLabel != "Fast" {
		t.Fatalf("an update of another field changed the label: %q, %v", n.PublicLabel, err)
	}
	if n, err := st.Access().Node(ctx, "nod_old"); err != nil || n.PublicLabel != "Fast" {
		t.Fatalf("the access view of the node: %q, %v", n.PublicLabel, err)
	}
	long := strings.Repeat("я", 41)
	if _, err := st.UpdateNode(ctx, "nod_old", NodePatch{PublicLabel: &long}); err == nil {
		t.Error("the column took 41 characters")
	}
	empty := ""
	if n, err := st.UpdateNode(ctx, "nod_old", NodePatch{PublicLabel: &empty}); err != nil || n.PublicLabel != "" {
		t.Fatalf("clear: %q, %v", n.PublicLabel, err)
	}
}
