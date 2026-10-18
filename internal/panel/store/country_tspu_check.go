package store

import (
	"context"
	"strings"
	"time"
)

// TSPUCheck is what a probe node's TSPU probe said about one node at one time (migration 00068). Vantage is the id of the node
// that probed. Verdict is blocked | open | partial | unknown; DetailJSON is one entry per port.
type TSPUCheck struct {
	NodeID, Vantage, Verdict, DetailJSON string
	DPIHop                               int
	ControlOK                            bool
	At                                   time.Time
}

// InsertTSPUChecks stores the rows of one run in one statement. A row for the same node, vantage and second is replaced.
func (s *Store) InsertTSPUChecks(ctx context.Context, rows []TSPUCheck) error {
	if len(rows) == 0 {
		return nil
	}
	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*7)
	for _, r := range rows {
		values = append(values, "(?, ?, ?, ?, ?, ?, ?)")
		controlOK := 0
		if r.ControlOK {
			controlOK = 1
		}
		detail := r.DetailJSON
		if detail == "" {
			detail = "[]"
		}
		args = append(args, r.NodeID, r.Vantage, unix(r.At), r.Verdict, r.DPIHop, controlOK, detail)
	}
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO country_tspu_check
		(node_id, vantage, at, verdict, dpi_hop, control_ok, detail_json) VALUES `+strings.Join(values, ", "), args...)
	return err
}

// RecentTSPUChecks reads the newest row of every node and vantage (of the given nodes only, when there are any), ordered by
// node id and vantage.
func (s *Store) RecentTSPUChecks(ctx context.Context, nodeIDs ...string) ([]TSPUCheck, error) {
	q := `SELECT node_id, vantage, at, verdict, dpi_hop, control_ok, detail_json FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY node_id, vantage ORDER BY at DESC) AS rank FROM country_tspu_check`
	var args []any
	if len(nodeIDs) > 0 {
		q += ` WHERE node_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(nodeIDs))
	}
	rows, err := s.R.QueryContext(ctx, q+`) WHERE rank = 1 ORDER BY node_id, vantage`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TSPUCheck
	for rows.Next() {
		var c TSPUCheck
		var at, controlOK int64
		if err := rows.Scan(&c.NodeID, &c.Vantage, &at, &c.Verdict, &c.DPIHop, &controlOK, &c.DetailJSON); err != nil {
			return nil, err
		}
		c.At, c.ControlOK = fromUnix(at), controlOK != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneTSPUChecks deletes the rows older than the cut and returns how many went.
func (s *Store) PruneTSPUChecks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM country_tspu_check WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TSPUHistory reads the newest perNode rows of every node from one vantage (of the given nodes only, when there are any),
// newest first within a node, ordered by node id. The alert reads the last few runs from it.
func (s *Store) TSPUHistory(ctx context.Context, vantage string, perNode int, nodeIDs ...string) ([]TSPUCheck, error) {
	return s.tspuHistory(ctx, vantage, nil, perNode, nodeIDs...)
}

// TSPUHistorySince reads up to perNode rows from the requested window for every node from one vantage. Filtering by the
// window before ranking keeps a burst of recent tests from crowding older rows out of a history view.
func (s *Store) TSPUHistorySince(ctx context.Context, vantage string, since time.Time, perNode int, nodeIDs ...string) ([]TSPUCheck, error) {
	return s.tspuHistory(ctx, vantage, &since, perNode, nodeIDs...)
}

func (s *Store) tspuHistory(ctx context.Context, vantage string, since *time.Time, perNode int, nodeIDs ...string) ([]TSPUCheck, error) {
	q := `SELECT node_id, vantage, at, verdict, dpi_hop, control_ok, detail_json FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY at DESC) AS rank FROM country_tspu_check WHERE vantage = ?`
	args := []any{vantage}
	if since != nil {
		q += ` AND at >= ?`
		args = append(args, unix(*since))
	}
	if len(nodeIDs) > 0 {
		q += ` AND node_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(nodeIDs))
	}
	rows, err := s.R.QueryContext(ctx, q+`) WHERE rank <= ? ORDER BY node_id, at DESC`, append(args, perNode)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TSPUCheck
	for rows.Next() {
		var c TSPUCheck
		var at, controlOK int64
		if err := rows.Scan(&c.NodeID, &c.Vantage, &at, &c.Verdict, &c.DPIHop, &controlOK, &c.DetailJSON); err != nil {
			return nil, err
		}
		c.At, c.ControlOK = fromUnix(at), controlOK != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastTSPUCheckAt is the time of the newest row from one vantage, zero when there is none.
func (s *Store) LastTSPUCheckAt(ctx context.Context, vantage string) (time.Time, error) {
	var at int64
	if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(at), 0) FROM country_tspu_check WHERE vantage = ?`, vantage).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if at == 0 {
		return time.Time{}, nil
	}
	return fromUnix(at), nil
}

// DeleteTSPUChecks forgets every stored row (Disconnect).
func (s *Store) DeleteTSPUChecks(ctx context.Context) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM country_tspu_check`)
	return err
}
