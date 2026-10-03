package store

import (
	"context"
	"testing"
)

func TestTorrentBlockerMigrationDefaultsExistingNodesOff(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 35); err != nil {
		t.Fatalf("up to 35: %v", err)
	}
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_old', 'old', 'old.example.com', 'active', 1)`)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up through torrent blocker migration: %v", err)
	}
	var enabled bool
	if err := s.R.QueryRowContext(ctx, `SELECT torrent_blocker_enabled FROM node WHERE id = 'nod_old'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("an existing node was enabled by the migration")
	}
	if _, err := p.DownTo(ctx, 35); err != nil {
		t.Fatalf("down to 35: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `SELECT torrent_blocker_enabled FROM node`); err == nil {
		t.Fatal("torrent blocker column is left after rolling back the migration")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestTorrentBlockerSettingDefaultsOffAndCanBeUpdated(t *testing.T) {
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "torrent")
	ctx := context.Background()

	n, err := s.Node(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if n.TorrentBlockerEnabled {
		t.Fatal("torrent blocker should default off")
	}

	enabled := true
	n, err = s.UpdateNode(ctx, nodeID, NodePatch{TorrentBlockerEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if !n.TorrentBlockerEnabled {
		t.Fatal("torrent blocker setting was not persisted")
	}

	disabled := false
	n, err = s.UpdateNode(ctx, nodeID, NodePatch{TorrentBlockerEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if n.TorrentBlockerEnabled {
		t.Fatal("torrent blocker setting was not disabled")
	}
}
