//go:build !js

package store

import (
	"context"
	"time"
)

// The rows behind path_probe, node_trace and node_dns_check (migrations 00102 to 00104). They exist for the MCP tools of the
// VPS edition: the Cloudflare edition has no caller for them, so this file is not in the Workers' wasm.

const (
	// PathProbeKeepHours is how long a path probe is kept (7 days).
	PathProbeKeepHours = 7 * 24
	// NodeTraceKeep is how many traces of one pair are kept.
	NodeTraceKeep = 20
	// NodeDNSRunKeep is how many resolver runs of one node are kept.
	NodeDNSRunKeep = 24
)

// PathProbeRow is one stored path probe.
type PathProbeRow struct {
	ID         int64
	FromID     string
	ToID       string
	Kind       string
	At         time.Time
	ResultJSON string
}

// AddPathProbe stores the result of one probe.
func (s *Store) AddPathProbe(ctx context.Context, fromID, toID, kind string, at time.Time, resultJSON string) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO path_probe (from_node_id, to_node_id, kind, at_unix, result_json) VALUES (?, ?, ?, ?, ?)`,
		fromID, toID, kind, unix(at), resultJSON)
	return err
}

// PathProbes returns the probes since a time, newest first, at most limit (0 = 50). A node is matched as sender or target
// when nodeID is not empty.
func (s *Store) PathProbes(ctx context.Context, since time.Time, nodeID string, limit int) ([]PathProbeRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.R.QueryContext(ctx, `SELECT id, from_node_id, to_node_id, kind, at_unix, result_json FROM path_probe
		WHERE at_unix >= ? AND (? = '' OR from_node_id = ? OR to_node_id = ?) ORDER BY at_unix DESC, id DESC LIMIT ?`,
		unix(since), nodeID, nodeID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathProbeRow
	for rows.Next() {
		var r PathProbeRow
		var at int64
		if err := rows.Scan(&r.ID, &r.FromID, &r.ToID, &r.Kind, &at, &r.ResultJSON); err != nil {
			return nil, err
		}
		r.At = fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PrunePathProbes deletes the probes older than before.
func (s *Store) PrunePathProbes(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM path_probe WHERE at_unix < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// NodeTraceRow is one stored trace of a pair.
type NodeTraceRow struct {
	ID         int64
	FromID     string
	ToID       string
	At         time.Time
	ResultJSON string
}

// AddNodeTrace stores a trace and drops the pair's traces beyond the newest NodeTraceKeep.
func (s *Store) AddNodeTrace(ctx context.Context, fromID, toID string, at time.Time, resultJSON string) error {
	_, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO node_trace (from_node_id, to_node_id, at_unix, result_json) VALUES (?, ?, ?, ?)`,
			Args: []any{fromID, toID, unix(at), resultJSON}},
		Stmt{Query: `DELETE FROM node_trace WHERE from_node_id = ? AND to_node_id = ? AND id NOT IN
			(SELECT id FROM node_trace WHERE from_node_id = ? AND to_node_id = ? ORDER BY at_unix DESC, id DESC LIMIT ?)`,
			Args: []any{fromID, toID, fromID, toID, NodeTraceKeep}})
	return err
}

// NodeTraces returns the traces of a pair, newest first, at most limit (0 = NodeTraceKeep).
func (s *Store) NodeTraces(ctx context.Context, fromID, toID string, limit int) ([]NodeTraceRow, error) {
	if limit <= 0 || limit > NodeTraceKeep {
		limit = NodeTraceKeep
	}
	rows, err := s.R.QueryContext(ctx, `SELECT id, from_node_id, to_node_id, at_unix, result_json FROM node_trace
		WHERE from_node_id = ? AND to_node_id = ? ORDER BY at_unix DESC, id DESC LIMIT ?`, fromID, toID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeTraceRow
	for rows.Next() {
		var r NodeTraceRow
		var at int64
		if err := rows.Scan(&r.ID, &r.FromID, &r.ToID, &at, &r.ResultJSON); err != nil {
			return nil, err
		}
		r.At = fromUnix(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// NodeDNSRun is one run of the resolver check of a node.
type NodeDNSRun struct {
	NodeID                      string
	At                          time.Time
	P50MS, P95MS, Failed, Total uint32
}

// AddNodeDNSRun stores a run (one per node and second) and drops the node's runs beyond the newest NodeDNSRunKeep.
func (s *Store) AddNodeDNSRun(ctx context.Context, r NodeDNSRun) error {
	_, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO node_dns_run (node_id, at_unix, p50_ms, p95_ms, failed, domains) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (node_id, at_unix) DO NOTHING`,
			Args: []any{r.NodeID, unix(r.At), int64(r.P50MS), int64(r.P95MS), int64(r.Failed), int64(r.Total)}},
		Stmt{Query: `DELETE FROM node_dns_run WHERE node_id = ? AND at_unix NOT IN
			(SELECT at_unix FROM node_dns_run WHERE node_id = ? ORDER BY at_unix DESC LIMIT ?)`,
			Args: []any{r.NodeID, r.NodeID, NodeDNSRunKeep}})
	return err
}

// NodeDNSRuns returns a node's runs, oldest first.
func (s *Store) NodeDNSRuns(ctx context.Context, nodeID string) ([]NodeDNSRun, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT at_unix, p50_ms, p95_ms, failed, domains FROM node_dns_run
		WHERE node_id = ? ORDER BY at_unix DESC LIMIT ?`, nodeID, NodeDNSRunKeep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeDNSRun
	for rows.Next() {
		r := NodeDNSRun{NodeID: nodeID}
		var at, p50, p95, failed, total int64
		if err := rows.Scan(&at, &p50, &p95, &failed, &total); err != nil {
			return nil, err
		}
		r.At, r.P50MS, r.P95MS, r.Failed, r.Total = fromUnix(at), uint32(p50), uint32(p95), uint32(failed), uint32(total)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
