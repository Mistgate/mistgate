package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mistgate/mistgate/internal/decoy"
)

var ErrNodeDecoyBundleMissing = errors.New("node has no uploaded decoy bundle")

// SetNodeDecoyMode changes one node's selected site. The caller checks that panel mode is available in this edition.
func (s *Store) SetNodeDecoyMode(ctx context.Context, nodeID, mode string) (NodeRow, error) {
	rows, err := s.SetNodesDecoyMode(ctx, []string{nodeID}, mode)
	if err != nil {
		return NodeRow{}, err
	}
	return rows[0], nil
}

// SetNodesDecoyMode updates a set of nodes in one database batch. Own mode is allowed only where a bundle was uploaded.
func (s *Store) SetNodesDecoyMode(ctx context.Context, nodeIDs []string, mode string) ([]NodeRow, error) {
	if mode != decoy.ModeBuiltin && mode != decoy.ModePanel && mode != decoy.ModeOwn {
		return nil, fmt.Errorf("invalid node decoy mode %q", mode)
	}
	if len(nodeIDs) == 0 || len(nodeIDs) > 50 {
		return nil, errors.New("node decoy mode needs 1 to 50 nodes")
	}
	seen := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		if id == "" {
			return nil, errors.New("node decoy mode has an empty node id")
		}
		if _, ok := seen[id]; ok {
			return nil, errors.New("node decoy mode has a duplicate node id")
		}
		seen[id] = struct{}{}
	}
	for _, id := range nodeIDs {
		n, err := s.Node(ctx, id)
		if err != nil {
			return nil, err
		}
		if n.State == "retired" {
			return nil, ErrNodeRetired
		}
		if mode == decoy.ModeOwn {
			if n.DecoyBundleHash == "" {
				return nil, ErrNodeDecoyBundleMissing
			}
			if _, err := s.NodeDecoyBundle(ctx, n.DecoyBundleHash); err != nil {
				return nil, err
			}
		}
	}
	stmts := make([]Stmt, 0, len(nodeIDs)*2)
	for _, id := range nodeIDs {
		condition := `EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`
		args := []any{id}
		if mode == decoy.ModeOwn {
			condition = `EXISTS (SELECT 1 FROM node n JOIN node_decoy_bundle b ON b.sha256 = n.decoy_bundle_hash WHERE n.id = ? AND n.state <> 'retired')`
		}
		stmts = append(stmts, guard(condition, args...), Stmt{Query: `UPDATE node SET decoy_mode = ? WHERE id = ? AND state <> 'retired'`, Args: []any{mode, id}})
	}
	if _, err := s.batch(ctx, stmts...); err != nil {
		if errors.Is(err, errGuard) {
			return nil, ErrConflict
		}
		return nil, err
	}
	out := make([]NodeRow, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n, err := s.Node(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// SetNodeDecoyBundle stores a normalized bundle by content hash and selects it as this node's own site.
func (s *Store) SetNodeDecoyBundle(ctx context.Context, nodeID, hash string, archive []byte) (NodeRow, error) {
	b, err := decoy.ParseStoredArchive(archive)
	if err != nil || b.Hash() != hash {
		return NodeRow{}, decoy.ErrInvalidBundle
	}
	prev, err := s.Node(ctx, nodeID)
	if err != nil {
		return NodeRow{}, err
	}
	if prev.State == "retired" {
		return NodeRow{}, ErrNodeRetired
	}
	stmts := []Stmt{
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, nodeID),
		{Query: `INSERT INTO node_decoy_bundle (sha256, content) VALUES (?, ?) ON CONFLICT (sha256) DO UPDATE SET content = excluded.content`, Args: []any{hash, archive}},
		{Query: `UPDATE node SET decoy_bundle_hash = ?, decoy_mode = 'own' WHERE id = ? AND state <> 'retired'`, Args: []any{hash, nodeID}},
		{Query: `DELETE FROM node_decoy_bundle WHERE sha256 = ? AND NOT EXISTS (SELECT 1 FROM node WHERE decoy_bundle_hash = ?)`, Args: []any{prev.DecoyBundleHash, prev.DecoyBundleHash}},
		{Query: `SELECT ` + nodeCols + ` FROM node WHERE id = ?`, Args: []any{nodeID}, Returning: true},
	}
	results, err := s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) {
		return NodeRow{}, ErrNodeRetired
	}
	if err != nil {
		return NodeRow{}, err
	}
	return scanNode(batchRow(results[len(results)-1].Rows[0]))
}

// NodeDecoyBundle reads a bundle by its content address and verifies the stored bytes.
func (s *Store) NodeDecoyBundle(ctx context.Context, hash string) ([]byte, error) {
	var archive []byte
	err := s.R.QueryRowContext(ctx, `SELECT content FROM node_decoy_bundle WHERE sha256 = ?`, hash).Scan(&archive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNodeDecoyBundleMissing
	}
	if err != nil {
		return nil, err
	}
	b, err := decoy.ParseStoredArchive(archive)
	if err != nil || b.Hash() != hash {
		return nil, errors.New("stored node decoy bundle failed validation")
	}
	return archive, nil
}

// UpdateNodeDecoyAgentState stores the mode/hash confirmed by SetDecoy. Hello remains the recovery source after a reconnect.
func (s *Store) UpdateNodeDecoyAgentState(ctx context.Context, nodeID, mode, hash string) error {
	_, err := s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, nodeID),
		Stmt{Query: `UPDATE node SET decoy_agent_mode = ?, decoy_agent_hash = ? WHERE id = ? AND state <> 'retired'`, Args: []any{mode, hash, nodeID}},
	)
	if errors.Is(err, errGuard) {
		return ErrNodeRetired
	}
	return err
}
