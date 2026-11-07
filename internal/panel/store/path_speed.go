//go:build !js

package store

import (
	"context"
	"time"
)

// The rows behind path_speed (migration 00107): the results of the bounded throughput tests between two nodes. They exist for
// the MCP tool of the VPS edition; the Cloudflare edition has no caller for them, so this file is not in the Workers' wasm.

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
