//go:build !js

package store

import (
	"context"
	"testing"
)

func TestNodeDecoyMigrationKeepsExistingNodesBuiltin(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 58); err != nil {
		t.Fatalf("up to 58: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_existing', 'de1', '203.0.113.10', 'active', 1)`); err != nil {
		t.Fatal(err)
	}

	if _, err := p.UpTo(ctx, 59); err != nil {
		t.Fatalf("up to 59: %v", err)
	}
	n, err := s.Node(ctx, "nod_existing")
	if err != nil {
		t.Fatal(err)
	}
	if n.DecoyMode != "builtin" || n.DecoyBundleHash != "" || n.DecoyAgentMode != "" || n.DecoyAgentHash != "" {
		t.Fatalf("existing node decoy state = %+v", n)
	}

	if _, err := p.DownTo(ctx, 58); err != nil {
		t.Fatalf("down to 58: %v", err)
	}
	if _, err := p.UpTo(ctx, 59); err != nil {
		t.Fatalf("up again: %v", err)
	}
	n, err = s.Node(ctx, "nod_existing")
	if err != nil || n.DecoyMode != "builtin" {
		t.Fatalf("node after migration rerun = %+v, %v", n, err)
	}
}
