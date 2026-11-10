package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// OsStatusRow is the latest stored snapshot received from one node.
type OsStatusRow struct {
	NodeID     string
	StatusJSON string
	MeasuredAt time.Time
	ReceivedAt time.Time
}

// PutOsStatus replaces the node's latest snapshot. The caller validates the wire values before storage.
func (s *Store) PutOsStatus(ctx context.Context, nodeID, statusJSON string, measuredAt, receivedAt time.Time) error {
	if nodeID == "" || len(statusJSON) == 0 || len(statusJSON) > 64<<10 || !json.Valid([]byte(statusJSON)) {
		return errors.New("store: invalid operating-system status")
	}
	_, err := s.W.ExecContext(ctx, "INSERT INTO node_os_status(node_id, status_json, measured_at, received_at)\n"+
		"VALUES(?, ?, ?, ?)\n"+
		"ON CONFLICT(node_id) DO UPDATE SET status_json = excluded.status_json,\n"+
		"measured_at = excluded.measured_at, received_at = excluded.received_at",
		nodeID, statusJSON, unix(measuredAt), unix(receivedAt))
	return err
}

// OsStatuses returns the latest snapshot for every node. An empty map means none have reported yet.
func (s *Store) OsStatuses(ctx context.Context) (map[string]OsStatusRow, error) {
	rows, err := s.R.QueryContext(ctx, "SELECT node_id, status_json, measured_at, received_at FROM node_os_status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]OsStatusRow)
	for rows.Next() {
		var row OsStatusRow
		var measured, received int64
		if err := rows.Scan(&row.NodeID, &row.StatusJSON, &measured, &received); err != nil {
			return nil, err
		}
		row.MeasuredAt, row.ReceivedAt = fromUnix(measured), fromUnix(received)
		out[row.NodeID] = row
	}
	return out, rows.Err()
}

// OsStatus returns one snapshot or ErrNotFound.
func (s *Store) OsStatus(ctx context.Context, nodeID string) (OsStatusRow, error) {
	var row OsStatusRow
	var measured, received int64
	err := s.R.QueryRowContext(ctx, "SELECT node_id, status_json, measured_at, received_at FROM node_os_status WHERE node_id = ?", nodeID).
		Scan(&row.NodeID, &row.StatusJSON, &measured, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return OsStatusRow{}, ErrNotFound
	}
	if err != nil {
		return OsStatusRow{}, err
	}
	row.MeasuredAt, row.ReceivedAt = fromUnix(measured), fromUnix(received)
	return row, nil
}
