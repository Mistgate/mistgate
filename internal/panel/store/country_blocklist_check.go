package store

import (
	"context"
	"strings"
	"time"
)

// BlocklistCheck is what the block lists said about one node's address at one time (migration 00067). ListedBy is a comma
// separated list of the lists that hold the address ("antifilter_ipsum,antifilter_community"); ListsAt is the date of the
// newest list that was used.
type BlocklistCheck struct {
	NodeID, RKNSubnet, ListedBy, CDNProvider, CDNRegion string
	InRKN                                               bool
	At, ListsAt                                         time.Time
}

// InsertBlocklistChecks stores the rows of one check in one statement. A row for the same node and second is replaced.
func (s *Store) InsertBlocklistChecks(ctx context.Context, rows []BlocklistCheck) error {
	if len(rows) == 0 {
		return nil
	}
	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*8)
	for _, r := range rows {
		values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?)")
		inRKN := 0
		if r.InRKN {
			inRKN = 1
		}
		listsAt := int64(0)
		if !r.ListsAt.IsZero() {
			listsAt = unix(r.ListsAt)
		}
		args = append(args, r.NodeID, unix(r.At), inRKN, r.RKNSubnet, r.ListedBy, r.CDNProvider, r.CDNRegion, listsAt)
	}
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO country_blocklist_check
		(node_id, at, in_rkn, rkn_subnet, listed_by, cdn_provider, cdn_region, lists_at) VALUES `+strings.Join(values, ", "), args...)
	return err
}

// RecentBlocklistChecks reads the newest check of every node (or of the given nodes), ordered by node id.
func (s *Store) RecentBlocklistChecks(ctx context.Context, nodeIDs ...string) ([]BlocklistCheck, error) {
	q := `SELECT node_id, at, in_rkn, rkn_subnet, listed_by, cdn_provider, cdn_region, lists_at FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY at DESC) AS rank FROM country_blocklist_check`
	var args []any
	if len(nodeIDs) > 0 {
		q += ` WHERE node_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(nodeIDs))
	}
	rows, err := s.R.QueryContext(ctx, q+`) WHERE rank = 1 ORDER BY node_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlocklistCheck
	for rows.Next() {
		var c BlocklistCheck
		var at, inRKN, listsAt int64
		if err := rows.Scan(&c.NodeID, &at, &inRKN, &c.RKNSubnet, &c.ListedBy, &c.CDNProvider, &c.CDNRegion, &listsAt); err != nil {
			return nil, err
		}
		c.At, c.InRKN = fromUnix(at), inRKN != 0
		if listsAt > 0 {
			c.ListsAt = fromUnix(listsAt)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BlocklistHistory reads every check since a time, oldest first inside a node, ordered by node id: the node, the time and whether
// a list held the address (InRKN, ListedBy). The lists are compared every 6 hours, so a week is a few dozen rows a node.
func (s *Store) BlocklistHistory(ctx context.Context, since time.Time) ([]BlocklistCheck, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, at, in_rkn, listed_by FROM country_blocklist_check WHERE at >= ? ORDER BY node_id, at`, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlocklistCheck
	for rows.Next() {
		var c BlocklistCheck
		var at, inRKN int64
		if err := rows.Scan(&c.NodeID, &at, &inRKN, &c.ListedBy); err != nil {
			return nil, err
		}
		c.At, c.InRKN = fromUnix(at), inRKN != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneBlocklistChecks deletes the checks older than the cut and returns how many went.
func (s *Store) PruneBlocklistChecks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM country_blocklist_check WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteBlocklistChecks forgets every stored check (Disconnect).
func (s *Store) DeleteBlocklistChecks(ctx context.Context) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM country_blocklist_check`)
	return err
}
