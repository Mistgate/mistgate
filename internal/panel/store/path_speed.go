//go:build !js

package store

import (
	"context"
	"time"
)

// The rows behind path_speed (migration 00107) and path_speed_start (00116): results and start reservations for the bounded
// throughput tests between two nodes. They exist for the MCP tool of the VPS edition; the Cloudflare edition has no caller for
// them, so this file is not in the Workers' wasm.

// PathSpeedKeepHours is how long a speed test result is kept (7 days).
const PathSpeedKeepHours = 7 * 24

// PathSpeedRow is one stored speed test.
type PathSpeedRow struct {
	ID         int64
	FromID     string
	ToID       string
	Direction  string
	At         time.Time
	ResultJSON string
}

// PathSpeedStartUsage is the recent daily-limit usage for one caller and two nodes.
type PathSpeedStartUsage struct {
	Caller []time.Time
	Nodes  map[string][]time.Time
}

// PathSpeedStartUsage returns reservations after since for actorID and either node. The starts are ordered oldest first so the
// caller can calculate when a limit expires.
func (s *Store) PathSpeedStartUsage(ctx context.Context, actorID, fromID, toID string, since time.Time) (PathSpeedStartUsage, error) {
	usage := PathSpeedStartUsage{Nodes: map[string][]time.Time{fromID: {}, toID: {}}}
	rows, err := s.R.QueryContext(ctx, `SELECT actor_id, from_node_id, to_node_id, at_unix FROM path_speed_start
		WHERE at_unix > ? AND (actor_id = ? OR from_node_id IN (?, ?) OR to_node_id IN (?, ?)) ORDER BY at_unix, id`,
		unix(since), actorID, fromID, toID, fromID, toID)
	if err != nil {
		return PathSpeedStartUsage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var actor, from, to string
		var at int64
		if err := rows.Scan(&actor, &from, &to, &at); err != nil {
			return PathSpeedStartUsage{}, err
		}
		started := fromUnix(at)
		if actor == actorID {
			usage.Caller = append(usage.Caller, started)
		}
		if from == fromID || to == fromID {
			usage.Nodes[fromID] = append(usage.Nodes[fromID], started)
		}
		if from == toID || to == toID {
			usage.Nodes[toID] = append(usage.Nodes[toID], started)
		}
	}
	return usage, rows.Err()
}

// AddPathSpeedStart reserves one test before it contacts either agent. Failed tests still count against the daily limits.
func (s *Store) AddPathSpeedStart(ctx context.Context, actorID, fromID, toID string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO path_speed_start (actor_id, from_node_id, to_node_id, at_unix) VALUES (?, ?, ?, ?)`,
		actorID, fromID, toID, unix(at))
	return err
}

// PrunePathSpeedStarts deletes reservations older than the rolling daily window.
func (s *Store) PrunePathSpeedStarts(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM path_speed_start WHERE at_unix <= ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AddPathSpeed stores the result of one speed test.
func (s *Store) AddPathSpeed(ctx context.Context, fromID, toID, direction string, at time.Time, resultJSON string) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO path_speed (from_node_id, to_node_id, direction, at_unix, result_json) VALUES (?, ?, ?, ?, ?)`,
		fromID, toID, direction, unix(at), resultJSON)
	return err
}

// PathSpeeds returns the tests since a time, newest first, at most limit (0 = 50). A node is matched as sender or target when
// nodeID is not empty.
func (s *Store) PathSpeeds(ctx context.Context, since time.Time, nodeID string, limit int) ([]PathSpeedRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.R.QueryContext(ctx, `SELECT id, from_node_id, to_node_id, direction, at_unix, result_json FROM path_speed
		WHERE at_unix >= ? AND (? = '' OR from_node_id = ? OR to_node_id = ?) ORDER BY at_unix DESC, id DESC LIMIT ?`,
		unix(since), nodeID, nodeID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathSpeedRow
	for rows.Next() {
		var r PathSpeedRow
		var at int64
		if err := rows.Scan(&r.ID, &r.FromID, &r.ToID, &r.Direction, &at, &r.ResultJSON); err != nil {
			return nil, err
		}
		r.At = fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PrunePathSpeeds deletes the tests older than before.
func (s *Store) PrunePathSpeeds(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM path_speed WHERE at_unix < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
