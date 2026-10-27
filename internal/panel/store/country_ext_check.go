package store

import (
	"context"
	"strings"
	"time"
)

// ExtCheck is one source's verdict for a node at a time (migration 00082). DetailJSON contains generic vantage details.
type ExtCheck struct {
	NodeID, Source, Verdict, DetailJSON string
	At                                  time.Time
}

// ExtCheckTime is the newest result time for one node and source, without loading its detail JSON.
type ExtCheckTime struct {
	NodeID, Source string
	At             time.Time
}

// InsertExtChecks stores one set of rows in one statement. A row for the same node, source and second is replaced.
func (s *Store) InsertExtChecks(ctx context.Context, rows []ExtCheck) error {
	if len(rows) == 0 {
		return nil
	}
	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*5)
	for _, r := range rows {
		values = append(values, "(?, ?, ?, ?, ?)")
		detail := r.DetailJSON
		if detail == "" {
			detail = "{}"
		}
		args = append(args, r.NodeID, r.Source, unix(r.At), r.Verdict, detail)
	}
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO country_ext_check
		(node_id, source, at, verdict, detail_json) VALUES `+strings.Join(values, ", "), args...)
	return err
}

// RecentExtChecks reads up to perNodeSource newest rows of every node and source, optionally limited to sources.
func (s *Store) RecentExtChecks(ctx context.Context, perNodeSource int, sources ...string) ([]ExtCheck, error) {
	q := `SELECT node_id, source, at, verdict, detail_json FROM (
			SELECT node_id, source, at, verdict, detail_json,
				ROW_NUMBER() OVER (PARTITION BY node_id, source ORDER BY at DESC) AS rank
			FROM country_ext_check`
	var args []any
	if len(sources) > 0 {
		q += ` WHERE source IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(sources))
	}
	q += `) WHERE rank <= ? ORDER BY node_id, source, at DESC`
	args = append(args, perNodeSource)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExtCheck
	for rows.Next() {
		var c ExtCheck
		var at int64
		if err := rows.Scan(&c.NodeID, &c.Source, &at, &c.Verdict, &c.DetailJSON); err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecentExtCheckTimes reads only the newest result time for every node and source, optionally limited to sources.
func (s *Store) RecentExtCheckTimes(ctx context.Context, sources ...string) ([]ExtCheckTime, error) {
	q := `SELECT node_id, source, at FROM (
			SELECT node_id, source, at,
				ROW_NUMBER() OVER (PARTITION BY node_id, source ORDER BY at DESC) AS rank
			FROM country_ext_check`
	var args []any
	if len(sources) > 0 {
		q += ` WHERE source IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(sources))
	}
	q += `) WHERE rank = 1 ORDER BY node_id, source`
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExtCheckTime
	for rows.Next() {
		var check ExtCheckTime
		var at int64
		if err := rows.Scan(&check.NodeID, &check.Source, &at); err != nil {
			return nil, err
		}
		check.At = fromUnix(at)
		out = append(out, check)
	}
	return out, rows.Err()
}

// ExtVerdictChanges reads the first row in the window and each later source verdict change for every node.
func (s *Store) ExtVerdictChanges(ctx context.Context, since time.Time) ([]ExtCheck, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, source, at, verdict FROM (
			SELECT node_id, source, at, verdict,
				LAG(verdict) OVER (PARTITION BY node_id, source ORDER BY at) AS prev
			FROM country_ext_check WHERE at >= ?
		) WHERE prev IS NULL OR prev <> verdict ORDER BY node_id, source, at`, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExtCheck
	for rows.Next() {
		var c ExtCheck
		var at int64
		if err := rows.Scan(&c.NodeID, &c.Source, &at, &c.Verdict); err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastExtCheckAt is the newest time for one source and node, zero when there is none.
func (s *Store) LastExtCheckAt(ctx context.Context, source, nodeID string) (time.Time, error) {
	var at int64
	if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(at), 0) FROM country_ext_check WHERE source = ? AND node_id = ?`, source, nodeID).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if at == 0 {
		return time.Time{}, nil
	}
	return fromUnix(at), nil
}

// PruneExtChecks deletes rows older than the cut and returns how many went.
func (s *Store) PruneExtChecks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM country_ext_check WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteExtChecks forgets the selected sources, or every source when none are given.
func (s *Store) DeleteExtChecks(ctx context.Context, sources ...string) error {
	if len(sources) == 0 {
		_, err := s.W.ExecContext(ctx, `DELETE FROM country_ext_check`)
		return err
	}
	_, err := s.W.ExecContext(ctx, `DELETE FROM country_ext_check WHERE source IN (SELECT value FROM json_each(?))`, jsonStrings(sources))
	return err
}
