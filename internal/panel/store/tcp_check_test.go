//go:build !js

package store

import (
	"context"
	"testing"
)

func TestTCPChecksSQLite(t *testing.T) {
	tcpCheckStoreProof(t, openTemp(t))
}

// 00056 adds a table and nothing else: rolling back drops it with its rows, applying again gives an empty one, and
// the node it pointed at is untouched.
func TestTCPCheckMigrationUpDownUp(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 55); err != nil {
		t.Fatalf("up to 55: %v", err)
	}
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_a', 'node-a', 'node-a.example.com', 'active', 1)`)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up to latest: %v", err)
	}
	execT(t, s, `INSERT INTO node_tcp_check (node_id, port, role, verdict, sender, checked_at) VALUES ('nod_a', 80, 'acme', 'ours', 'panel', 5)`)
	if got, err := s.TCPChecks(ctx, "nod_a"); err != nil || len(got) != 1 || got[0].ALPN || got[0].Profile != "" || got[0].Reason != "" {
		t.Fatalf("a row with the defaults = %+v, %v", got, err)
	}
	if _, err := p.DownTo(ctx, 55); err != nil {
		t.Fatalf("down to 55: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `SELECT 1 FROM node_tcp_check`); err == nil {
		t.Fatal("the table is left after down")
	}
	if countT(t, s, `SELECT count(*) FROM node WHERE id = 'nod_a'`) != 1 {
		t.Fatal("down touched the node")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got, err := s.TCPChecks(ctx, "nod_a"); err != nil || len(got) != 0 {
		t.Fatalf("not empty after up again: %+v, %v", got, err)
	}
}
