package store

import (
	"context"
	"strings"
	"time"
)

// CountryCheck is one round of the country check for one node (migrations 00066 and 00084). Verdict is ok | partial |
// tcp_blocked | blocked | unknown; Detail is the JSON the health module writes and reads (per probe network, ASN, city and
// answers). Family is the address family that was measured, 4 or 6; the zero value means 4 (what every row before
// migration 00084 is).
type CountryCheck struct {
	NodeID, Country, Verdict, Detail string
	Family                           int
	At                               time.Time
}

// FamilyOrV4 is Family with the zero value read as IPv4.
func (c CountryCheck) FamilyOrV4() int {
	if c.Family == 0 {
		return 4
	}
	return c.Family
}

// InsertCountryChecks stores the rows of one round in one statement. A row for the same node, country, family and second
// is replaced; the same second with another family or country is a row of its own.
func (s *Store) InsertCountryChecks(ctx context.Context, rows []CountryCheck) error {
	if len(rows) == 0 {
		return nil
	}
	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*6)
	for _, r := range rows {
		values = append(values, "(?, ?, ?, ?, ?, ?)")
		detail := r.Detail
		if detail == "" {
			detail = "{}"
		}
		args = append(args, r.NodeID, unix(r.At), r.Country, r.FamilyOrV4(), r.Verdict, detail)
	}
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO country_check (node_id, at, country, family, verdict, detail_json) VALUES `+
		strings.Join(values, ", "), args...)
	return err
}

// RecentCountryChecks reads up to perNode newest rounds of one address family (4 or 6) of every node (or of the given
// nodes), newest first inside a node.
func (s *Store) RecentCountryChecks(ctx context.Context, family, perNode int, nodeIDs ...string) ([]CountryCheck, error) {
	q := `SELECT node_id, at, country, family, verdict, detail_json FROM (
			SELECT node_id, at, country, family, verdict, detail_json,
				ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY at DESC) AS rank
			FROM country_check WHERE family = ?`
	args := []any{family}
	if len(nodeIDs) > 0 {
		q += ` AND node_id IN (SELECT value FROM json_each(?))`
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
		if err := rows.Scan(&c.NodeID, &at, &c.Country, &c.Family, &c.Verdict, &c.Detail); err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountryVerdictChanges reads the rounds of one country and address family since a time that changed a node's verdict: the first round of every
// node and each round whose verdict differs from the one before it, oldest first inside a node, ordered by node id. Only the
// node, the time and the verdict: the Reachability page's history needs no more, and a week of rounds every 10 minutes is a
// thousand rows a node that would otherwise cross the D1 binding.
func (s *Store) CountryVerdictChanges(ctx context.Context, country string, family int, since time.Time) ([]CountryCheck, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, at, verdict FROM (
			SELECT node_id, at, verdict, LAG(verdict) OVER (PARTITION BY node_id ORDER BY at) AS prev
			FROM country_check WHERE country = ? AND family = ? AND at >= ?
		) WHERE prev IS NULL OR prev <> verdict ORDER BY node_id, at`, country, family, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CountryCheck
	for rows.Next() {
		c := CountryCheck{Country: country, Family: family}
		var at int64
		if err := rows.Scan(&c.NodeID, &at, &c.Verdict); err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastCountryCheckAt is the time of the newest stored round of any node in one address family, zero when there is none.
func (s *Store) LastCountryCheckAt(ctx context.Context, family int) (time.Time, error) {
	var at int64
	if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(at), 0) FROM country_check WHERE family = ?`, family).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if at == 0 {
		return time.Time{}, nil
	}
	return fromUnix(at), nil
}

// PruneCountryChecks deletes the rounds older than the cut, whatever their family, and returns how many went.
func (s *Store) PruneCountryChecks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM country_check WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
