//go:build !js

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The last neighbour scan of each node (migration 00151). The VPS edition's ProbeService writes and reads it; the Cloudflare edition
// has no caller, so this file is not in the Workers' wasm.

// NodeSniScanRow is the last finished scan of a node.
type NodeSniScanRow struct {
	NodeID     string
	At         time.Time
	ResultJSON string
}

// PutNodeSniScan keeps a finished scan as the node's last one.
func (s *Store) PutNodeSniScan(ctx context.Context, nodeID string, at time.Time, resultJSON string) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO node_sni_scan (node_id, at_unix, result_json) VALUES (?, ?, ?)
		ON CONFLICT (node_id) DO UPDATE SET at_unix = excluded.at_unix, result_json = excluded.result_json`, nodeID, unix(at), resultJSON)
	return err
}

// NodeSniScan returns the node's last finished scan; ok is false when there is none.
func (s *Store) NodeSniScan(ctx context.Context, nodeID string) (NodeSniScanRow, bool, error) {
	r := NodeSniScanRow{NodeID: nodeID}
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT at_unix, result_json FROM node_sni_scan WHERE node_id = ?`, nodeID).Scan(&at, &r.ResultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeSniScanRow{}, false, nil
	}
	if err != nil {
		return NodeSniScanRow{}, false, err
	}
	r.At = fromUnix(at)
	return r, true, nil
}
