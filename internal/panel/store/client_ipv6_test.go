package store

import (
	"context"
	"testing"
)

// 00046: every node that exists keeps IPv6 for clients on (what it did before); the switch is a patch like the others.
func TestClientIPv6MigrationKeepsExistingNodesOn(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 45); err != nil {
		t.Fatalf("up to 45: %v", err)
	}
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_old', 'old', 'old.example.com', 'active', 1)`)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up through the client IPv6 migration: %v", err)
	}
	n, err := s.Node(ctx, "nod_old")
	if err != nil {
		t.Fatal(err)
	}
	if !n.ClientIPv6 {
		t.Fatal("an existing node lost IPv6 for clients in the migration")
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE node SET client_ipv6 = 2 WHERE id = 'nod_old'`); err == nil {
		t.Fatal("the column accepts a value other than 0 and 1")
	}

	off := false
	if n, err = s.UpdateNode(ctx, "nod_old", NodePatch{ClientIPv6: &off}); err != nil || n.ClientIPv6 {
		t.Fatalf("turn off: %+v, %v", n.ClientIPv6, err)
	}
	if n, err = s.UpdateNode(ctx, "nod_old", NodePatch{Notes: ptrTo("x")}); err != nil || n.ClientIPv6 {
		t.Fatalf("another patch changed the switch: %v, %v", n.ClientIPv6, err)
	}
	on := true
	if n, err = s.UpdateNode(ctx, "nod_old", NodePatch{ClientIPv6: &on}); err != nil || !n.ClientIPv6 {
		t.Fatalf("turn on: %v, %v", n.ClientIPv6, err)
	}

	if _, err := p.DownTo(ctx, 45); err != nil {
		t.Fatalf("down to 45: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `SELECT client_ipv6 FROM node`); err == nil {
		t.Fatal("the column is left after rolling back the migration")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func ptrTo[T any](v T) *T { return &v }
