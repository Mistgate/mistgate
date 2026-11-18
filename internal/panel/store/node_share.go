package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// The ways a node is handed out to groups (migration 00146). A group receives every node that is not on its disabled list; a
// node the owner holds back is on the list of every group that has not been given it, and of groups made later. The mode is
// read off that: hold_new_groups = 0 is all, otherwise listed while some group receives it and none when no group does.
const (
	ShareAll    = "all"    // every group, groups made later too (until a group turns it off)
	ShareListed = "listed" // only the groups the owner named; a group made later does not get it
	ShareNone   = "none"   // no group; a node held back until the owner gives it to one
)

// NodeShare is how one node is handed out now.
type NodeShare struct {
	Hold      bool      // hold_new_groups: groups made later do not get the node
	Retired   bool      // the node left the fleet; its share is no longer in force
	Receiving int       // groups that receive it
	Groups    int       // every group
	ChangedAt time.Time // when the owner last set it (the node's creation until then); zero when unknown
}

// Mode is the share's mode: ShareAll, ShareListed or ShareNone.
func (n NodeShare) Mode() string {
	switch {
	case !n.Hold:
		return ShareAll
	case n.Receiving == 0:
		return ShareNone
	}
	return ShareListed
}

// NodeShareChoice is a share asked for. Mode "" leaves the choice to the panel when the node is made: the first node of the
// panel goes to every group, a later one to none.
type NodeShareChoice struct {
	Mode     string   `json:"mode,omitempty"` // "", ShareAll, ShareListed (with GroupIDs) or ShareNone
	GroupIDs []string `json:"groups,omitempty"`
}

// JSON is the choice as a job stores it ("" for the automatic one).
func (c NodeShareChoice) JSON() string {
	if c.Mode == "" {
		return ""
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// ParseNodeShareChoice reads what JSON wrote; anything unreadable is the automatic choice.
func ParseNodeShareChoice(s string) NodeShareChoice {
	var c NodeShareChoice
	if s == "" || json.Unmarshal([]byte(s), &c) != nil {
		return NodeShareChoice{}
	}
	return c
}

// nodeShareStmts writes a share: the hold flag, then a disabled row for every group that does not receive the node. The
// automatic choice holds the node when another node that is not retired exists.
func nodeShareStmts(nodeID string, c NodeShareChoice, now time.Time) []Stmt {
	update := Stmt{Query: `UPDATE node SET share_changed_at = ?, hold_new_groups = 1 WHERE id = ?`, Args: []any{unix(now), nodeID}}
	switch c.Mode {
	case ShareAll:
		update.Query = `UPDATE node SET share_changed_at = ?, hold_new_groups = 0 WHERE id = ?`
	case "":
		update.Query = `UPDATE node SET share_changed_at = ?,
			hold_new_groups = EXISTS (SELECT 1 FROM node o WHERE o.id <> ? AND o.state <> 'retired') WHERE id = ?`
		update.Args = []any{unix(now), nodeID, nodeID}
	}
	insert := Stmt{Query: `INSERT INTO user_group_disabled_node (group_id, node_id)
		SELECT g.id, ? FROM user_group g WHERE (SELECT hold_new_groups FROM node WHERE id = ?) = 1`, Args: []any{nodeID, nodeID}}
	if c.Mode == ShareListed && len(c.GroupIDs) > 0 {
		insert.Query += ` AND g.id NOT IN (?` + strings.Repeat(`, ?`, len(c.GroupIDs)-1) + `)`
		for _, id := range c.GroupIDs {
			insert.Args = append(insert.Args, id)
		}
	}
	return []Stmt{update, {Query: `DELETE FROM user_group_disabled_node WHERE node_id = ?`, Args: []any{nodeID}}, insert}
}

// SetNodeShare hands a node out as asked, in one transaction. ErrNodeRetired for a retired node, ErrNotFound for none.
func (a Access) SetNodeShare(ctx context.Context, nodeID string, c NodeShareChoice, now time.Time) error {
	c.GroupIDs = slices.Compact(slices.Sorted(slices.Values(c.GroupIDs)))
	stmts := append([]Stmt{guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, nodeID)}, nodeShareStmts(nodeID, c, now)...)
	if _, err := a.s.batch(ctx, stmts...); err != nil {
		if errors.Is(err, errGuard) {
			if _, readErr := a.s.Node(ctx, nodeID); readErr == nil {
				return ErrNodeRetired
			}
			return ErrNotFound
		}
		return err
	}
	return nil
}

// NodeShares reads how every node is handed out, in one query.
func (s *Store) NodeShares(ctx context.Context) (map[string]NodeShare, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT n.id, n.hold_new_groups, n.share_changed_at, n.created_at, n.state = 'retired',
			(SELECT count(*) FROM user_group),
			(SELECT count(*) FROM user_group g WHERE NOT EXISTS (SELECT 1 FROM user_group_disabled_node d WHERE d.group_id = g.id AND d.node_id = n.id))
		FROM node n`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeShare{}
	for rows.Next() {
		var id string
		var hold bool
		var changed, created int64
		var n NodeShare
		if err := rows.Scan(&id, &hold, &changed, &created, &n.Retired, &n.Groups, &n.Receiving); err != nil {
			return nil, err
		}
		n.Hold = hold
		n.ChangedAt = fromUnix(max(changed, created))
		out[id] = n
	}
	return out, rows.Err()
}

// NodeAudience counts, per group, the active people who would see a node when their group receives it: those who use every
// node or picked this one. The panel's own cache of what a share change does to people.
func (a Access) NodeAudience(ctx context.Context, nodeID string) (map[string]int, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT u.group_id, count(*) FROM user u WHERE u.status = 'active'
		AND (u.all_nodes = 1 OR EXISTS (SELECT 1 FROM user_node un WHERE un.user_id = u.id AND un.node_id = ?)) GROUP BY u.group_id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
