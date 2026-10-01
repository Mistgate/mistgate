package store

import (
	"context"
	"time"
)

// Accepted doctor warnings (migration 00027,HealthService.AcceptDoctorItem).

// DoctorAccept is one accepted warning.
type DoctorAccept struct {
	NodeID, CheckID, DetailCode string
	By, ByName                  string // admin id, and the admin's display name ("" when the admin is gone)
	At                          time.Time
}

// AcceptDoctor stores (or renews) an acceptance, in one statement and only while the stored result of the check is still
// a WARN (2) with that detail code; false when it is not (a newer report changed it, and DropStaleAccepts may have run).
func (s *Store) AcceptDoctor(ctx context.Context, a DoctorAccept) (bool, error) {
	res, err := s.W.ExecContext(ctx, `INSERT INTO doctor_accept (node_id, check_id, detail_code, accepted_by, accepted_at)
		SELECT node_id, check_id, detail_code, ?, ? FROM doctor_result WHERE node_id = ? AND check_id = ? AND status = 2 AND detail_code = ?
		ON CONFLICT (node_id, check_id) DO UPDATE SET detail_code = excluded.detail_code, accepted_by = excluded.accepted_by,
			accepted_at = excluded.accepted_at`, a.By, unix(a.At), a.NodeID, a.CheckID, a.DetailCode)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UnacceptDoctor removes an acceptance; false when there was none.
func (s *Store) UnacceptDoctor(ctx context.Context, nodeID, checkID string) (bool, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM doctor_accept WHERE node_id = ? AND check_id = ?`, nodeID, checkID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DoctorAccepts lists the acceptances of one node, or of every node for "".
func (s *Store) DoctorAccepts(ctx context.Context, nodeID string) ([]DoctorAccept, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT a.node_id, a.check_id, a.detail_code, a.accepted_by, coalesce(d.display_name, ''), a.accepted_at
		FROM doctor_accept a LEFT JOIN admin d ON d.id = a.accepted_by WHERE (? = '' OR a.node_id = ?) ORDER BY a.node_id, a.check_id`, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DoctorAccept
	for rows.Next() {
		var a DoctorAccept
		var at int64
		if err := rows.Scan(&a.NodeID, &a.CheckID, &a.DetailCode, &a.By, &a.ByName, &at); err != nil {
			return nil, err
		}
		a.At = fromUnix(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DropStaleAccepts removes the acceptances of a node that its stored doctor results no longer match: the check turned
// FAIL (3), or it is a WARN (2) with another detail code. Run after storing a report.
func (s *Store) DropStaleAccepts(ctx context.Context, nodeID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM doctor_accept WHERE node_id = ? AND EXISTS (
		SELECT 1 FROM doctor_result r WHERE r.node_id = doctor_accept.node_id AND r.check_id = doctor_accept.check_id
			AND (r.status = 3 OR (r.status = 2 AND r.detail_code <> doctor_accept.detail_code)))`, nodeID)
	return err
}
