//go:build !js

package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

// Migration 00134 marks the nodes whose agent always asks to seal its link (v0.1.66 and later) and nobody else: an older,
// empty, dev or unrecognised version stays unmarked, and so does a node the version of which only looks like one.
func TestMigrationMarksNodesWithASealingAgent(t *testing.T) {
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
	if _, err := provider.DownTo(ctx, 133); err != nil {
		t.Fatalf("down to the schema before the mark: %v", err)
	}
	want := map[string]bool{
		"v0.1.65": false, "v0.1.66": true, "v0.1.87": true, "v0.1.100": true, "v0.2.0": true, "v1.0.0": true, "v10.0.0": true,
		"0.1.87": true, "0.1.65": false, "": false, "dev": false, "0.1.0-dev": false, "v0.1.87-dirty": false, "v0.1": false,
		"v0.1.x": false, "v0.1.87.1": false, "v0..87": false, "vv0.1.87": false, "v0.0.99": false, "v00000000.1.87": false,
	}
	ids := map[string]string{}
	i := 0
	for version := range want {
		i++
		id := "nod_" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		ids[version] = id
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at, agent_version) VALUES (?, ?, 'de1.example.com', 'active', 1, ?)`, id, id, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.UpTo(ctx, 134); err != nil {
		t.Fatalf("apply the mark: %v", err)
	}
	for version, marked := range want {
		var got bool
		if err := st.R.QueryRowContext(ctx, `SELECT link_sealed FROM node WHERE id = ?`, ids[version]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != marked {
			t.Errorf("agent version %q: link_sealed = %v, want %v", version, got, marked)
		}
	}
}
