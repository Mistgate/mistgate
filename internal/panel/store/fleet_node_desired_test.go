//go:build !js

package store

import (
	"context"
	"testing"
)

func TestNodeDesiredDoesNotRegressRevision(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "desired_revision")
	if err := s.NodeDesired(ctx, nodeID, 8, "new-state"); err != nil {
		t.Fatal(err)
	}
	if err := s.NodeDesired(ctx, nodeID, 7, "old-state"); err != nil {
		t.Fatal(err)
	}
	var revision int64
	var hash string
	if err := s.R.QueryRowContext(ctx, `SELECT desired_revision, desired_hash FROM node WHERE id = ?`, nodeID).Scan(&revision, &hash); err != nil {
		t.Fatal(err)
	}
	if revision != 8 || hash != "new-state" {
		t.Fatalf("desired state regressed to revision %d hash %q", revision, hash)
	}
}
