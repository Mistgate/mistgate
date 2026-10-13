//go:build !js

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/mistgate/mistgate/internal/decoy"
)

func TestNodeDecoyBundleIsContentAddressedAndOwnedByNode(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	nodeA, _ := fixtureInbound(t, s, "a")
	nodeB, _ := fixtureInbound(t, s, "b")
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("example site")}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.SetNodeDecoyBundle(ctx, nodeA, bundle.Hash(), bundle.Archive())
	if err != nil {
		t.Fatal(err)
	}
	if first.DecoyMode != decoy.ModeOwn || first.DecoyBundleHash != bundle.Hash() {
		t.Fatalf("first upload state = %+v", first)
	}
	second, err := s.SetNodeDecoyBundle(ctx, nodeB, bundle.Hash(), bundle.Archive())
	if err != nil {
		t.Fatal(err)
	}
	if second.DecoyMode != decoy.ModeOwn || second.DecoyBundleHash != first.DecoyBundleHash {
		t.Fatalf("second upload state = %+v", second)
	}
	if got := countT(t, s, `SELECT count(*) FROM node_decoy_bundle WHERE sha256 = ?`, bundle.Hash()); got != 1 {
		t.Fatalf("content-addressed copies = %d, want one", got)
	}
	stored, err := s.NodeDecoyBundle(ctx, bundle.Hash())
	if err != nil {
		t.Fatal(err)
	}
	read, err := decoy.ParseStoredArchive(stored)
	if err != nil || read.Hash() != bundle.Hash() {
		t.Fatalf("stored bundle = %v, %v", read, err)
	}
	if _, err := s.SetNodesDecoyMode(ctx, []string{nodeA, nodeB}, decoy.ModePanel); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SetNodesDecoyMode(ctx, []string{nodeA, nodeB}, decoy.ModeOwn)
	if err != nil || len(rows) != 2 || rows[0].DecoyMode != decoy.ModeOwn || rows[1].DecoyMode != decoy.ModeOwn {
		t.Fatalf("bulk own mode = %+v, %v", rows, err)
	}
}

func TestBulkOwnDecoyModeRefusesWithoutEveryBundle(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	nodeA, _ := fixtureInbound(t, s, "a")
	nodeB, _ := fixtureInbound(t, s, "b")
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("example site")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodeDecoyBundle(ctx, nodeA, bundle.Hash(), bundle.Archive()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodeDecoyMode(ctx, nodeA, decoy.ModeBuiltin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodesDecoyMode(ctx, []string{nodeA, nodeB}, decoy.ModeOwn); !errors.Is(err, ErrNodeDecoyBundleMissing) {
		t.Fatalf("bulk own mode error = %v", err)
	}
	for _, id := range []string{nodeA, nodeB} {
		n, err := s.Node(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if n.DecoyMode != decoy.ModeBuiltin {
			t.Errorf("node %s partially changed to %q", id, n.DecoyMode)
		}
	}
}
