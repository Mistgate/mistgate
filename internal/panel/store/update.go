package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Rollout and step states as stored (migration 00013).
const (
	RolloutRunning   = "running"
	RolloutPaused    = "paused"
	RolloutDone      = "done"
	RolloutCancelled = "cancelled"
	RolloutFailed    = "failed"

	StepPending    = "pending"
	StepSent       = "sent"
	StepGating     = "gating"
	StepPassed     = "passed"
	StepFailed     = "failed"
	StepRolledBack = "rolled_back"
	StepSkipped    = "skipped"
)

// LastUpdateRow is Hello.last_update as stored on the node row (JSON). Outcome is "ok", "rolled_back" or "failed".
type LastUpdateRow struct {
	Outcome     string `json:"outcome"`
	FromVersion string `json:"from_version"`
	FromBuilt   int64  `json:"from_built"`
	ToVersion   string `json:"to_version"`
	ToBuilt     int64  `json:"to_built"`
	Reason      string `json:"reason"`
	AtUnix      int64  `json:"at_unix"`
}

// JSON is the stored form.
func (l LastUpdateRow) JSON() string {
	b, _ := json.Marshal(l)
	return string(b)
}

// LastUpdate is what the node reported about its last self-update; false when it never did.
func (n NodeRow) LastUpdate() (LastUpdateRow, bool) {
	var l LastUpdateRow
	if n.LastUpdateJSON == "" || json.Unmarshal([]byte(n.LastUpdateJSON), &l) != nil || l.Outcome == "" {
		return LastUpdateRow{}, false
	}
	return l, true
}

// RolloutRow is one rollout of a signed bundle.
type RolloutRow struct {
	ID, Status, ToVersion string
	ToBuilt               int64
	Manifest, Signature   []byte
	BatchSize             int
	PauseKey              string
	PauseParams           map[string]string
	CreatedBy             string
	CreatedAt, FinishedAt time.Time // FinishedAt zero while active
}

// Active is RUNNING or PAUSED.
func (r RolloutRow) Active() bool { return r.Status == RolloutRunning || r.Status == RolloutPaused }

// StepRow is one node's turn in a rollout.
type StepRow struct {
	RolloutID, NodeID, NodeName string
	Stage                       int
	State                       string
	FromVersion                 string
	FromBuilt                   int64
	PreFailed                   []string // inbound ids that were FAILED before the update
	SentAt, AckedAt             time.Time
	ReconnectedAt, FinishedAt   time.Time
	ErrorKey                    string
	Params                      map[string]string
}

// Decided is a step that will not change any more on its own (passed, failed, rolled back, skipped).
func (s StepRow) Decided() bool {
	return s.State != StepPending && s.State != StepSent && s.State != StepGating
}

const rolloutCols = `id, status, to_version, to_built, manifest, signature, batch_size, pause_key, pause_params_json,
	created_by, created_at, finished_at`

func scanRollout(r rowScanner) (RolloutRow, error) {
	var x RolloutRow
	var params string
	var created, finished int64
	err := r.Scan(&x.ID, &x.Status, &x.ToVersion, &x.ToBuilt, &x.Manifest, &x.Signature, &x.BatchSize, &x.PauseKey, &params,
		&x.CreatedBy, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return RolloutRow{}, ErrNotFound
	}
	if err != nil {
		return RolloutRow{}, err
	}
	_ = json.Unmarshal([]byte(params), &x.PauseParams)
	x.CreatedAt, x.FinishedAt = fromUnix(created), fleetTime(finished)
	return x, nil
}

func insertStep(ctx context.Context, tx *sql.Tx, x StepRow) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO update_step (rollout_id, node_id, node_name, stage, state, from_version, from_built, pre_failed_json,
			sent_at, acked_at, reconnected_at, finished_at, error_key, params_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.RolloutID, x.NodeID, x.NodeName, x.Stage, x.State, x.FromVersion, x.FromBuilt, jsonStrings(x.PreFailed),
		fleetUnix(x.SentAt), fleetUnix(x.AckedAt), fleetUnix(x.ReconnectedAt), fleetUnix(x.FinishedAt), x.ErrorKey, jsonMap(x.Params))
	return err
}

// CreateRollout stores a rollout and its steps in one transaction. ErrConflict when another rollout is active
// (the unique index 00013 allows one RUNNING or PAUSED rollout).
func (s *Store) CreateRollout(ctx context.Context, r RolloutRow, steps []StepRow) error {
	return s.createRollout(ctx, r, steps, nil, nil)
}

// CreateRolloutAndClearSchedules atomically starts an owner-triggered rollout and removes
// any scheduled update for nodes that it will update. A later timer must not undo that choice.
func (s *Store) CreateRolloutAndClearSchedules(ctx context.Context, r RolloutRow, steps []StepRow, nodeIDs []string) error {
	return s.createRollout(ctx, r, steps, nil, nodeIDs)
}

// CreateScheduledRollout atomically starts a scheduled node update and consumes the exact
// schedule it was based on. A concurrent reschedule or cancellation makes this fail safely.
func (s *Store) CreateScheduledRollout(ctx context.Context, r RolloutRow, steps []StepRow, schedule NodeUpdateScheduleRow) error {
	return s.createRollout(ctx, r, steps, &schedule, nil)
}

// AddNodeToRunningRollout inserts a manually selected node as its own stage after stage, shifting later stages down
// the rollout plan. The update and its release must still be current, and consuming the node's schedule is atomic.
func (s *Store) AddNodeToRunningRollout(ctx context.Context, rolloutID, version string, built int64, stage int, x StepRow) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE update_rollout SET status = status
		WHERE id = ? AND status = 'running' AND to_version = ? AND to_built = ?`, rolloutID, version, built)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE update_step SET stage = stage + 1 WHERE rollout_id = ? AND stage >= ?`, rolloutID, stage); err != nil {
		return err
	}
	x.RolloutID = rolloutID
	x.Stage = stage
	x.State = StepPending
	if err := insertStep(ctx, tx, x); err != nil {
		if fleetIsUnique(err) {
			return ErrConflict
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_update_schedule WHERE node_id = ?`, x.NodeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) createRollout(ctx context.Context, r RolloutRow, steps []StepRow, schedule *NodeUpdateScheduleRow, clearScheduleIDs []string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO update_rollout (id, status, to_version, to_built, manifest, signature, batch_size, pause_key, pause_params_json,
			created_by, created_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Status, r.ToVersion, r.ToBuilt, r.Manifest, r.Signature, r.BatchSize, r.PauseKey, jsonMap(r.PauseParams),
		r.CreatedBy, unix(r.CreatedAt), fleetUnix(r.FinishedAt))
	if err != nil {
		if fleetIsUnique(err) {
			return ErrConflict
		}
		return err
	}
	for _, x := range steps {
		x.RolloutID = r.ID
		if err := insertStep(ctx, tx, x); err != nil {
			return err
		}
	}
	if schedule != nil {
		res, err := tx.ExecContext(ctx, `DELETE FROM node_update_schedule
			WHERE node_id = ? AND to_version = ? AND to_built = ? AND scheduled_at = ? AND timezone_offset_minutes = ?`,
			schedule.NodeID, schedule.ToVersion, schedule.ToBuilt, schedule.ScheduledAt, schedule.TimezoneOffsetMinutes)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrConflict
		}
	} else {
		for _, nodeID := range clearScheduleIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM node_update_schedule WHERE node_id = ?`, nodeID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ActiveRollout is the RUNNING or PAUSED rollout, ErrNotFound when there is none.
func (s *Store) ActiveRollout(ctx context.Context) (RolloutRow, error) {
	return scanRollout(s.R.QueryRowContext(ctx, `SELECT `+rolloutCols+` FROM update_rollout WHERE status IN ('running', 'paused')`))
}

// LatestRollout is the newest rollout of any status, ErrNotFound when there never was one.
func (s *Store) LatestRollout(ctx context.Context) (RolloutRow, error) {
	return scanRollout(s.R.QueryRowContext(ctx, `SELECT `+rolloutCols+` FROM update_rollout ORDER BY created_at DESC, rowid DESC LIMIT 1`))
}

// Rollout returns one rollout or ErrNotFound.
func (s *Store) Rollout(ctx context.Context, id string) (RolloutRow, error) {
	return scanRollout(s.R.QueryRowContext(ctx, `SELECT `+rolloutCols+` FROM update_rollout WHERE id = ?`, id))
}

// RolloutSteps lists the steps of a rollout by stage, then node name.
func (s *Store) RolloutSteps(ctx context.Context, rolloutID string) ([]StepRow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT rollout_id, node_id, node_name, stage, state, from_version, from_built, pre_failed_json, sent_at, acked_at,
		       reconnected_at, finished_at, error_key, params_json
		FROM update_step WHERE rollout_id = ? ORDER BY stage, node_name COLLATE NOCASE, node_id`, rolloutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepRow
	for rows.Next() {
		var x StepRow
		var pre, params string
		var sent, acked, recon, fin int64
		if err := rows.Scan(&x.RolloutID, &x.NodeID, &x.NodeName, &x.Stage, &x.State, &x.FromVersion, &x.FromBuilt, &pre,
			&sent, &acked, &recon, &fin, &x.ErrorKey, &params); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(pre), &x.PreFailed)
		_ = json.Unmarshal([]byte(params), &x.Params)
		x.SentAt, x.AckedAt, x.ReconnectedAt, x.FinishedAt = fleetTime(sent), fleetTime(acked), fleetTime(recon), fleetTime(fin)
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetRolloutStatus moves a rollout to status only if it is currently in one of from (a compare-and-set: two
// callers cannot both win). pauseKey and params describe a pause and are cleared by any other status. It
// reports whether the row changed; finished is recorded for the terminal statuses.
func (s *Store) SetRolloutStatus(ctx context.Context, id string, from []string, status, pauseKey string, params map[string]string, finished time.Time) (bool, error) {
	if len(from) == 0 {
		return false, nil
	}
	if status != RolloutPaused {
		pauseKey, params = "", nil
	}
	q := `UPDATE update_rollout SET status = ?, pause_key = ?, pause_params_json = ?, finished_at = ? WHERE id = ? AND status IN (?` +
		strings.Repeat(", ?", len(from)-1) + `)`
	args := []any{status, pauseKey, jsonMap(params), fleetUnix(finished), id}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.W.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SaveStep overwrites the mutable fields of a step (the worker owns its rows).
func (s *Store) SaveStep(ctx context.Context, x StepRow) error {
	res, err := s.W.ExecContext(ctx, `
		UPDATE update_step SET state = ?, from_version = ?, from_built = ?, pre_failed_json = ?, sent_at = ?, acked_at = ?,
			reconnected_at = ?, finished_at = ?, error_key = ?, params_json = ?
		WHERE rollout_id = ? AND node_id = ?`,
		x.State, x.FromVersion, x.FromBuilt, jsonStrings(x.PreFailed), fleetUnix(x.SentAt), fleetUnix(x.AckedAt),
		fleetUnix(x.ReconnectedAt), fleetUnix(x.FinishedAt), x.ErrorKey, jsonMap(x.Params), x.RolloutID, x.NodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SkipPendingSteps turns every PENDING step of a rollout into SKIPPED with errorKey (cancel).
func (s *Store) SkipPendingSteps(ctx context.Context, rolloutID, errorKey string, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE update_step SET state = 'skipped', error_key = ?, finished_at = ? WHERE rollout_id = ? AND state = 'pending'`,
		errorKey, unix(now), rolloutID)
	return err
}

// PruneRollouts deletes finished rollouts (their steps go with them) that are older than cutoff and are not among
// the newest keep finished ones. An active rollout is never touched. It returns how many were removed.
func (s *Store) PruneRollouts(ctx context.Context, keep int, cutoff time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `
		DELETE FROM update_rollout
		WHERE status NOT IN ('running', 'paused') AND created_at < ?
		  AND id NOT IN (SELECT id FROM update_rollout WHERE status NOT IN ('running', 'paused')
		                 ORDER BY created_at DESC, rowid DESC LIMIT ?)`, unix(cutoff), max(keep, 0))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// NodeEventSince reports whether the node has an event with this code at or after since (Unix seconds compare). A
// non-zero toBuilt also requires the event's to_built parameter, when it has one, to be that build (an event without
// the parameter, from an older agent, is judged by time alone).
func (s *Store) NodeEventSince(ctx context.Context, nodeID, code string, since time.Time, toBuilt int64) (bool, error) {
	var one int
	err := s.R.QueryRowContext(ctx, `SELECT 1 FROM event WHERE node_id = ? AND code = ? AND ts >= ?
		AND (? = 0 OR json_extract(params_json, '$.to_built') IS NULL OR json_extract(params_json, '$.to_built') = ?) LIMIT 1`,
		nodeID, code, unix(since), toBuilt, strconv.FormatInt(toBuilt, 10)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// GateRollback is a rollout step the panel rolled back because the node failed its gate.
type GateRollback struct {
	ErrorKey string // the gate's code: probe_failed, inbound_failed, state_not_applied
	ToBuilt  int64  // the build the rollout shipped
	At       time.Time
}

// GateRollbacks returns, per node, the newest step that ended rolled back by the gate: a rolled_back step whose
// error key is neither the owner's (manual) nor the agent's own (rolled_back_by_agent).
func (s *Store) GateRollbacks(ctx context.Context) (map[string]GateRollback, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT s.node_id, s.error_key, r.to_built, s.finished_at
		FROM update_step s JOIN update_rollout r ON r.id = s.rollout_id
		WHERE s.state = 'rolled_back' AND s.error_key NOT IN ('', 'manual', 'rolled_back_by_agent')
		ORDER BY s.finished_at, s.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]GateRollback{}
	for rows.Next() {
		var node string
		var g GateRollback
		var at int64
		if err := rows.Scan(&node, &g.ErrorKey, &g.ToBuilt, &at); err != nil {
			return nil, err
		}
		g.At = fleetTime(at)
		out[node] = g // oldest first: the newest stays
	}
	return out, rows.Err()
}

// LastAuditByNode returns, per params.node_id, the time of the newest audit row of an action.
func (s *Store) LastAuditByNode(ctx context.Context, action string) (map[string]time.Time, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT json_extract(params, '$.node_id'), max(ts) FROM audit
		WHERE action = ? AND json_valid(params) GROUP BY 1`, action)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var node sql.NullString
		var ts int64
		if err := rows.Scan(&node, &ts); err != nil {
			return nil, err
		}
		if node.String != "" {
			out[node.String] = fromUnix(ts)
		}
	}
	return out, rows.Err()
}

// SetLastUpdateIfNewer stores l as the node's last update unless the stored one is newer: a Hello that carried a later
// outcome must not be overwritten by an event that arrives late.
func (s *Store) SetLastUpdateIfNewer(ctx context.Context, nodeID string, l LastUpdateRow) error {
	_, err := s.W.ExecContext(ctx, `UPDATE node SET last_update_json = ? WHERE id = ?
		AND (CASE WHEN json_valid(last_update_json) THEN coalesce(json_extract(last_update_json, '$.at_unix'), 0) ELSE 0 END) <= ?`,
		l.JSON(), nodeID, l.AtUnix)
	return err
}
