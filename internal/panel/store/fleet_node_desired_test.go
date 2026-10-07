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
	newDigest := []byte(`{"r":8,"h":"new-state"}`)
	if err := s.NodeDesired(ctx, nodeID, 8, "new-state", newDigest); err != nil {
		t.Fatal(err)
	}
	oldDigest := []byte(`{"r":7,"h":"old-state"}`)
	if err := s.NodeDesired(ctx, nodeID, 7, "old-state", oldDigest); err != nil {
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
	var digest string
	if err := s.R.QueryRowContext(ctx, `SELECT digest FROM node_sent WHERE node_id = ?`, nodeID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != string(oldDigest) {
		t.Fatalf("sent digest = %q, want the unguarded upsert %q", digest, oldDigest)
	}
	gotNode, gotDigest, err := s.NodeWithSentDigest(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if gotNode.DesiredRevision != 8 || gotNode.DesiredHash != "new-state" || string(gotDigest) != string(oldDigest) {
		t.Fatalf("node with sent digest = %+v/%q", gotNode, gotDigest)
	}
}
