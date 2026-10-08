package store

import (
	"context"
	"strings"
	"time"
)

const (
	portCheckBadFor   = 30 * 24 * time.Hour
	portCheckFreshFor = 24 * time.Hour
)

// PortCheck is the latest conclusive UDP delivery result for one node address and port.
type PortCheck struct {
	NodeID, Address  string
	Port             uint16
	Sent, Got        uint32
	Verdict, Sender  string
	CheckedAt, BadAt time.Time
}

// Bad reports whether this port lost packets in the last 30 days.
func (p PortCheck) Bad(now time.Time) bool {
	if p.BadAt.IsZero() {
		return false
	}
	age := now.Sub(p.BadAt)
	return age >= 0 && age < portCheckBadFor
}

// Fresh reports whether this port has a clean result less than 24 hours old.
func (p PortCheck) Fresh(now time.Time) bool {
	if p.Verdict != "ok" || p.CheckedAt.IsZero() {
		return false
	}
	age := now.Sub(p.CheckedAt)
	return age >= 0 && age < portCheckFreshFor
}

// PutPortChecks replaces the latest conclusive results in one atomic multi-row statement.
func (s *Store) PutPortChecks(ctx context.Context, checks []PortCheck) error {
	if len(checks) == 0 {
		return nil
	}
	values := make([]string, 0, len(checks))
	args := make([]any, 0, len(checks)*8)
	for _, check := range checks {
		values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?, ?)")
		badAt := int64(0)
		if check.Verdict != "ok" {
			badAt = unix(check.CheckedAt)
		}
		args = append(args, check.NodeID, int64(check.Port), check.Address, int64(check.Sent), int64(check.Got),
			check.Verdict, check.Sender, unix(check.CheckedAt), badAt)
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO node_port_check
		(node_id, port, address, sent, got, verdict, sender, checked_at, bad_at)
		VALUES `+strings.Join(values, ", ")+`
		ON CONFLICT(node_id, port) DO UPDATE SET
			address = excluded.address,
			sent = excluded.sent,
			got = excluded.got,
			verdict = excluded.verdict,
			sender = excluded.sender,
			checked_at = excluded.checked_at,
			bad_at = CASE
				WHEN excluded.verdict <> 'ok' THEN excluded.checked_at
				WHEN node_port_check.address <> excluded.address THEN 0
				ELSE node_port_check.bad_at
			END`, args...)
	return err
}

// PortChecks reads stored results, ignoring rows whose node address has since changed.
// With no nodeIDs it returns every current result.
func (s *Store) PortChecks(ctx context.Context, nodeIDs ...string) ([]PortCheck, error) {
	q := `SELECT c.node_id, c.port, c.address, c.sent, c.got, c.verdict, c.sender, c.checked_at, c.bad_at
		FROM node_port_check c JOIN node n ON n.id = c.node_id AND n.address = c.address`
	var args []any
	if len(nodeIDs) > 0 {
		q += ` WHERE c.node_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(nodeIDs))
	}
	q += ` ORDER BY c.node_id, c.port`
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortCheck
	for rows.Next() {
		var check PortCheck
		var port, sent, got, checkedAt, badAt int64
		if err := rows.Scan(&check.NodeID, &port, &check.Address, &sent, &got, &check.Verdict, &check.Sender, &checkedAt, &badAt); err != nil {
			return nil, err
		}
		check.Port, check.Sent, check.Got = uint16(port), uint32(sent), uint32(got)
		check.CheckedAt = fromUnix(checkedAt)
		if badAt != 0 {
			check.BadAt = fromUnix(badAt)
		}
		out = append(out, check)
	}
	return out, rows.Err()
}
