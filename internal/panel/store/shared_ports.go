package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The snapshot of the TCP ports a node shares by SNI (migration 00151).

// SharedPorts reads the node's last snapshot; ok is false when it shares no port.
func (s *Store) SharedPorts(ctx context.Context, nodeID string) (portsJSON string, at time.Time, ok bool, err error) {
	var unixAt int64
	err = s.R.QueryRowContext(ctx, `SELECT ports_json, at_unix FROM node_shared_ports WHERE node_id = ?`, nodeID).Scan(&portsJSON, &unixAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	return portsJSON, fromUnix(unixAt), true, nil
}

// SetSharedPorts stores the snapshot; an empty one removes the row.
func (s *Store) SetSharedPorts(ctx context.Context, nodeID, portsJSON string, at time.Time) error {
	if portsJSON == "" {
		_, err := s.W.ExecContext(ctx, `DELETE FROM node_shared_ports WHERE node_id = ?`, nodeID)
		return err
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO node_shared_ports (node_id, at_unix, ports_json) VALUES (?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET at_unix = excluded.at_unix, ports_json = excluded.ports_json`, nodeID, unix(at), portsJSON)
	return err
}
