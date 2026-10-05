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

// 00040: torrent events already written lose the client's address and the destination; the rest of them and every
// other event stay as they are.
func TestTorrentEventPrivacyMigration(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 39); err != nil {
		t.Fatal(err)
	}
	execT(t, s, `INSERT INTO event (ts, severity, code, source, params_json) VALUES
		(1, 2, 'torrent_attempt', 'agent', '{"user_id":"usr_a","protocol":"udp","torrent_protocol":"dht","client_ip":"10.66.4.2","destination":"203.0.113.8:6881"}'),
		(2, 2, 'torrent_attempt', 'agent', '{"protocol":"tcp"}'),
		(3, 1, 'other', 'agent', '{"client_ip":"203.0.113.9"}')`)
	if _, err := p.UpTo(ctx, 40); err != nil {
		t.Fatal(err)
	}
	var got []string
	rows, err := s.R.QueryContext(ctx, `SELECT params_json FROM event ORDER BY ts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		rows.Scan(&v)
		got = append(got, v)
	}
	want := []string{`{"user_id":"usr_a","protocol":"udp","torrent_protocol":"dht"}`, `{"protocol":"tcp"}`, `{"client_ip":"203.0.113.9"}`}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("events after the migration = %q, want %q", got, want)
	}
	if _, err := p.DownTo(ctx, 39); err != nil {
		t.Fatalf("down: %v", err)
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
