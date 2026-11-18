package store

import (
	"context"
	"time"
)

// NoteSpeedTest records that the node ran a speed measurement that ended about now, and forgets the ones older than the
// longest window a capacity report reads.
func (s *Store) NoteSpeedTest(ctx context.Context, nodeID string, now time.Time) error {
	if _, err := s.W.ExecContext(ctx, `DELETE FROM node_speed_test WHERE node_id = ? AND at < ?`, nodeID, now.Add(-(HostMaxHours+1)*time.Hour).Unix()); err != nil {
		return err
	}
	_, err := s.W.ExecContext(ctx, `INSERT OR IGNORE INTO node_speed_test (node_id, at) VALUES (?, ?)`, nodeID, now.Unix())
	return err
}

// SpeedTestTimes is when the node's speed measurements ended since the given time, oldest first.
func (s *Store) SpeedTestTimes(ctx context.Context, nodeID string, since time.Time) ([]int64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT at FROM node_speed_test WHERE node_id = ? AND at >= ? ORDER BY at`, nodeID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}
