package store

import (
	"context"
	"strings"
	"time"
)

// CountryCheck is one round of the country check for one node (migration 00066). Verdict is ok | partial | tcp_blocked |
// blocked | unknown; Detail is the JSON the health module writes and reads (per probe network, ASN, city and answers).
type CountryCheck struct {
	NodeID, Country, Verdict, Detail string
	At                               time.Time
}

// InsertCountryChecks stores the rows of one round in one statement. A row for the same node and second is replaced.
func (s *Store) InsertCountryChecks(ctx context.Context, rows []CountryCheck) error {
	if len(rows) == 0 {
		return nil
	}
	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*5)
	for _, r := range rows {
		values = append(values, "(?, ?, ?, ?, ?)")
		detail := r.Detail
		if detail == "" {
			detail = "{}"
		}
		args = append(args, r.NodeID, unix(r.At), r.Country, r.Verdict, detail)
	}
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO country_check (node_id, at, country, verdict, detail_json) VALUES `+
		strings.Join(values, ", "), args...)
	return err
}

// RecentCountryChecks reads up to perNode newest rounds of every node (or of the given nodes), newest first inside a node.
func (s *Store) RecentCountryChecks(ctx context.Context, perNode int, nodeIDs ...string) ([]CountryCheck, error) {
	q := `SELECT node_id, at, country, verdict, detail_json FROM (
			SELECT node_id, at, country, verdict, detail_json,
				ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY at DESC) AS rank
			FROM country_check`
	var args []any
	if len(nodeIDs) > 0 {
		q += ` WHERE node_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(nodeIDs))
	}
	q += `) WHERE rank <= ? ORDER BY node_id, at DESC`
	args = append(args, perNode)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CountryCheck
	for rows.Next() {
		var c CountryCheck
		var at int64
		if err := rows.Scan(&c.NodeID, &at, &c.Country, &c.Verdict, &c.Detail); err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountryVerdictChanges reads the rounds of one country since a time that changed a node's verdict: the first round of every
// node and each round whose verdict differs from the one before it, oldest first inside a node, ordered by node id. Only the
// node, the time and the verdict: the Reachability page's history needs no more, and a week of rounds every 10 minutes is a
// thousand rows a node that would otherwise cross the D1 binding.
func (s *Store) CountryVerdictChanges(ctx context.Context, country string, since time.Time) ([]CountryCheck, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, at, verdict FROM (
			SELECT node_id, at, verdict, LAG(verdict) OVER (PARTITION BY node_id ORDER BY at) AS prev
			FROM country_check WHERE country = ? AND at >= ?
		) WHERE prev IS NULL OR prev <> verdict ORDER BY node_id, at`, country, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CountryCheck
	for rows.Next() {
		c := CountryCheck{Country: country}
		var at int64
		if err := rows.Scan(&c.NodeID, &at, &c.Verdict); err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastCountryCheckAt is the time of the newest stored round of any node, zero when there is none.
func (s *Store) LastCountryCheckAt(ctx context.Context) (time.Time, error) {
	var at int64
	if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(at), 0) FROM country_check`).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if at == 0 {
		return time.Time{}, nil
	}
	return fromUnix(at), nil
}

// PruneCountryChecks deletes the rounds older than the cut and returns how many went.
func (s *Store) PruneCountryChecks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM country_check WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
