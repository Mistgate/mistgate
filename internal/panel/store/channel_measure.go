//go:build !js

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The rows behind the channel measure (migration 00148). The VPS edition only, like path_speed.go.

// ChannelMeasureRow is what a node keeps of its channel measures: when the last one started (a failed one counts) and the last that
// gave a figure. At is the zero time and ResultJSON empty until one has.
type ChannelMeasureRow struct {
	Started    time.Time
	At         time.Time
	ResultJSON string
}

// ChannelMeasure reads the node's row; ok is false when it never started one.
func (s *Store) ChannelMeasure(ctx context.Context, nodeID string) (row ChannelMeasureRow, ok bool, err error) {
	var started, at int64
	err = s.R.QueryRowContext(ctx, `SELECT started_unix, at_unix, result_json FROM node_channel_measure WHERE node_id = ?`, nodeID).
		Scan(&started, &at, &row.ResultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelMeasureRow{}, false, nil
	}
	if err != nil {
		return ChannelMeasureRow{}, false, err
	}
	row.Started = fromUnix(started)
	if at > 0 {
		row.At = fromUnix(at)
	}
	return row, true, nil
}

// StartChannelMeasure records that a measure of the node starts now; the previous result stays.
func (s *Store) StartChannelMeasure(ctx context.Context, nodeID string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO node_channel_measure (node_id, started_unix) VALUES (?, ?)
		ON CONFLICT(node_id) DO UPDATE SET started_unix = excluded.started_unix`, nodeID, unix(at))
	return err
}

// SetChannelMeasure stores the result of the measure that ran.
func (s *Store) SetChannelMeasure(ctx context.Context, nodeID string, at time.Time, resultJSON string) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO node_channel_measure (node_id, started_unix, at_unix, result_json) VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET at_unix = excluded.at_unix, result_json = excluded.result_json`,
		nodeID, unix(at), unix(at), resultJSON)
	return err
}

// ChannelMeasures reads the stored last result of every node that has one, by node id.
func (s *Store) ChannelMeasures(ctx context.Context) (map[string]ChannelMeasureRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, started_unix, at_unix, result_json FROM node_channel_measure WHERE at_unix > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ChannelMeasureRow{}
	for rows.Next() {
		var id string
		var started, at int64
		var r ChannelMeasureRow
		if err := rows.Scan(&id, &started, &at, &r.ResultJSON); err != nil {
			return nil, err
		}
		r.Started, r.At = fromUnix(started), fromUnix(at)
		out[id] = r
	}
	return out, rows.Err()
}
