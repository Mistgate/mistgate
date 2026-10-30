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
	// read the columns themselves: Node() follows the newest schema, and later migrations add node columns
	decoy := func() (mode, hash, agentMode, agentHash string, err error) {
		err = s.R.QueryRowContext(ctx, `SELECT decoy_mode, decoy_bundle_hash, decoy_agent_mode, decoy_agent_hash FROM node WHERE id = 'nod_existing'`).Scan(&mode, &hash, &agentMode, &agentHash)
		return
	}
	mode, hash, agentMode, agentHash, err := decoy()
	if err != nil {
		t.Fatal(err)
	}
	if mode != "builtin" || hash != "" || agentMode != "" || agentHash != "" {
		t.Fatalf("existing node decoy state = %q %q %q %q", mode, hash, agentMode, agentHash)
	}

	if _, err := p.DownTo(ctx, 58); err != nil {
		t.Fatalf("down to 58: %v", err)
	}
	if _, err := p.UpTo(ctx, 59); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if mode, _, _, _, err = decoy(); err != nil || mode != "builtin" {
		t.Fatalf("node after migration rerun = %q, %v", mode, err)
	}
}
