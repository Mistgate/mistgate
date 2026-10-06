package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Queries of DNS per node (migration 00044): what the owner offers on a node and what a person picked. The rule that
// turns them into an effective preset lives in the dns module.

// NodeDNSOption is one preset the owner offers on a node.
type NodeDNSOption struct {
	NodeID, PresetID string
	Position         int
	Default          bool
}

// UserNodeDNS is a person's pick on one node. UpdatedMs is Unix milliseconds (see the migration).
type UserNodeDNS struct {
	UserID, NodeID, PresetID string
	UpdatedMs                int64
}

// NodeOptions returns the offered presets of every node, in the owner's order.
func (d DNS) NodeOptions(ctx context.Context) (map[string][]NodeDNSOption, error) {
	rows, err := d.s.R.QueryContext(ctx, `SELECT node_id, preset_id, position, is_default FROM node_dns_option ORDER BY node_id, position, preset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]NodeDNSOption{}
	for rows.Next() {
		var o NodeDNSOption
		var def int
		if err := rows.Scan(&o.NodeID, &o.PresetID, &o.Position, &def); err != nil {
			return nil, err
		}
		o.Default = def == 1
		out[o.NodeID] = append(out[o.NodeID], o)
	}
	return out, rows.Err()
}

// SetNodeOptions replaces what a node offers: presetIDs in order, defaultID (one of them, or "") as the default. The
// caller checked the ids. ErrNotFound when the node or a preset is gone. Picks of people for presets that are no
// longer offered stay (they do nothing while the preset is not offered).
func (d DNS) SetNodeOptions(ctx context.Context, nodeID string, presetIDs []string, defaultID string) error {
	stmts := []Stmt{{Query: `DELETE FROM node_dns_option WHERE node_id = ?`, Args: []any{nodeID}}}
	for i, id := range presetIDs {
		stmts = append(stmts, Stmt{Query: `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES (?, ?, ?, ?)`,
			Args: []any{nodeID, id, int64(i), int64(accBool(id == defaultID))}})
	}
	_, err := d.s.batch(ctx, stmts...)
	return accMapErr(err)
}

// UserNodeChoices returns what one person picked, by node id.
func (d DNS) UserNodeChoices(ctx context.Context, userID string) (map[string]UserNodeDNS, error) {
	rows, err := d.s.R.QueryContext(ctx, `SELECT node_id, preset_id, updated_at FROM user_node_dns WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]UserNodeDNS{}
	for rows.Next() {
		c := UserNodeDNS{UserID: userID}
		if err := rows.Scan(&c.NodeID, &c.PresetID, &c.UpdatedMs); err != nil {
			return nil, err
		}
		out[c.NodeID] = c
	}
	return out, rows.Err()
}

// SetUserNodeChoice records a person's pick (replacing the earlier one); presetID "" removes it. ErrNotFound when the
// user, node or preset is gone.
func (d DNS) SetUserNodeChoice(ctx context.Context, userID, nodeID, presetID string, now time.Time) error {
	if presetID == "" {
		_, err := d.s.W.ExecContext(ctx, `DELETE FROM user_node_dns WHERE user_id = ? AND node_id = ?`, userID, nodeID)
		return err
	}
	_, err := d.s.W.ExecContext(ctx,
		`INSERT INTO user_node_dns (user_id, node_id, preset_id, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id, node_id) DO UPDATE SET preset_id = excluded.preset_id, updated_at = excluded.updated_at`,
		userID, nodeID, presetID, now.UnixMilli())
	return accMapErr(err)
}

// ResetUserNodeChoices removes every pick of a person and says how many there were.
func (d DNS) ResetUserNodeChoices(ctx context.Context, userID string) (int, error) {
	res, err := d.s.W.ExecContext(ctx, `DELETE FROM user_node_dns WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// NodeExists reports whether a node id exists (retired nodes included).
func (d DNS) NodeExists(ctx context.Context, nodeID string) (bool, error) {
	var one int
	err := d.s.R.QueryRowContext(ctx, `SELECT 1 FROM node WHERE id = ?`, nodeID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// NodeName is the name the owner gave a node; ErrNotFound when there is none.
func (d DNS) NodeName(ctx context.Context, nodeID string) (string, error) {
	var name string
	err := d.s.R.QueryRowContext(ctx, `SELECT name FROM node WHERE id = ?`, nodeID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// NodeNames maps every node id to its name.
func (d DNS) NodeNames(ctx context.Context) (map[string]string, error) {
	rows, err := d.s.R.QueryContext(ctx, `SELECT id, name FROM node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
