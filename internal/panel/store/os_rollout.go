package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Rollout modes and step states of an OS update as stored (migration 00130). The statuses are the agent rollout's.
const (
	OsModeSecurity   = "security"
	OsModeAll        = "all"
	OsModeRebootOnly = "reboot_only"

	OsRebootNever      = "never"
	OsRebootIfRequired = "if_required"
	OsRebootAlways     = "always"

	OsStepPending   = "pending"
	OsStepSent      = "sent"
	OsStepRebooting = "rebooting"
	OsStepGating    = "gating"
	OsStepPassed    = "passed"
	OsStepFailed    = "failed"
	OsStepSkipped   = "skipped"
)

// OsRolloutRow is one OS update rollout.
type OsRolloutRow struct {
	ID, Status, Mode, Reboot string
	BatchSize                int
	PauseKey                 string
	PauseParams              map[string]string
	CreatedBy                string
	CreatedAt, FinishedAt    time.Time // FinishedAt zero while active
}

// Active is RUNNING or PAUSED.
func (r OsRolloutRow) Active() bool { return r.Status == RolloutRunning || r.Status == RolloutPaused }

// OsStepRow is one node's turn in an OS rollout.
type OsStepRow struct {
	RolloutID, NodeID, NodeName string
	Stage                       int
	State                       string
	JobID                       string
	Sends                       int
	AckedAt                     time.Time
	Phase                       string
	PhaseAt                     time.Time
	BootBefore                  time.Time
	KernelFrom, KernelTo        string
	PreFailed                   []string
	SentAt, RebootAt, BackAt    time.Time
	FinishedAt                  time.Time
	ErrorKey                    string
	Params                      map[string]string
	loaded                      bool
	loadedState                 string
}

// Decided is a step that will not change any more on its own.
func (s OsStepRow) Decided() bool {
	return s.State != OsStepPending && s.State != OsStepSent && s.State != OsStepRebooting && s.State != OsStepGating
}

const osRolloutCols = `id, status, mode, reboot, batch_size, pause_key, pause_params_json, created_by, created_at, finished_at`

func scanOsRollout(r rowScanner) (OsRolloutRow, error) {
	var x OsRolloutRow
	var params string
	var batch, created, finished int64
	err := r.Scan(&x.ID, &x.Status, &x.Mode, &x.Reboot, &batch, &x.PauseKey, &params, &x.CreatedBy, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return OsRolloutRow{}, ErrNotFound
	}
	if err != nil {
		return OsRolloutRow{}, err
	}
	_ = json.Unmarshal([]byte(params), &x.PauseParams)
	x.BatchSize, x.CreatedAt, x.FinishedAt = int(batch), fromUnix(created), fleetTime(finished)
	return x, nil
}

func insertOsStepStmt(x OsStepRow) Stmt {
	return Stmt{Query: `
		INSERT INTO os_step (rollout_id, node_id, node_name, stage, state, job_id, sends, acked_at, phase, phase_at, boot_before,
			kernel_from, kernel_to, pre_failed_json, sent_at, reboot_at, back_at, finished_at, error_key, params_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{x.RolloutID, x.NodeID, x.NodeName, int64(x.Stage), x.State, x.JobID, int64(x.Sends), fleetUnix(x.AckedAt), x.Phase,
			fleetUnix(x.PhaseAt), fleetUnix(x.BootBefore), x.KernelFrom, x.KernelTo, jsonStrings(x.PreFailed), fleetUnix(x.SentAt),
			fleetUnix(x.RebootAt), fleetUnix(x.BackAt), fleetUnix(x.FinishedAt), x.ErrorKey, jsonMap(x.Params)}}
}

// CreateOsRollout stores a rollout and its steps in one atomic batch. ErrConflict when another OS rollout is active (the
// unique index of migration 00130 allows one RUNNING or PAUSED).
func (s *Store) CreateOsRollout(ctx context.Context, r OsRolloutRow, steps []OsStepRow) error {
	batchSize := r.BatchSize
	if batchSize == 0 { // old callers use the single-node default
		batchSize = 1
	}
	stmts := []Stmt{guard(`NOT EXISTS (SELECT 1 FROM os_rollout WHERE id = ?) AND NOT EXISTS (SELECT 1 FROM os_rollout WHERE status IN ('running', 'paused'))`, r.ID)}
	stmts = append(stmts, Stmt{Query: `
		INSERT INTO os_rollout (id, status, mode, reboot, batch_size, pause_key, pause_params_json, created_by, created_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{r.ID, r.Status, r.Mode, r.Reboot, int64(batchSize), r.PauseKey, jsonMap(r.PauseParams), r.CreatedBy, unix(r.CreatedAt), fleetUnix(r.FinishedAt)}})
	for _, x := range steps {
		x.RolloutID = r.ID
		stmts = append(stmts, insertOsStepStmt(x))
	}
	_, err := s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) || fleetIsUnique(err) {
		return ErrConflict
	}
	return err
}

// ActiveOsRollout is the RUNNING or PAUSED OS rollout, ErrNotFound when there is none.
func (s *Store) ActiveOsRollout(ctx context.Context) (OsRolloutRow, error) {
	return scanOsRollout(s.R.QueryRowContext(ctx, `SELECT `+osRolloutCols+` FROM os_rollout WHERE status IN ('running', 'paused')`))
}

// LatestOsRollout is the newest OS rollout of any status, ErrNotFound when there never was one.
func (s *Store) LatestOsRollout(ctx context.Context) (OsRolloutRow, error) {
	return scanOsRollout(s.R.QueryRowContext(ctx, `SELECT `+osRolloutCols+` FROM os_rollout ORDER BY created_at DESC, rowid DESC LIMIT 1`))
}

// OsRollout returns one OS rollout or ErrNotFound.
func (s *Store) OsRollout(ctx context.Context, id string) (OsRolloutRow, error) {
	return scanOsRollout(s.R.QueryRowContext(ctx, `SELECT `+osRolloutCols+` FROM os_rollout WHERE id = ?`, id))
}

// OsRolloutSteps lists the steps of an OS rollout by stage, then node name.
func (s *Store) OsRolloutSteps(ctx context.Context, rolloutID string) ([]OsStepRow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT rollout_id, node_id, node_name, stage, state, job_id, sends, acked_at, phase, phase_at, boot_before, kernel_from, kernel_to,
		       pre_failed_json, sent_at, reboot_at, back_at, finished_at, error_key, params_json
		FROM os_step WHERE rollout_id = ? ORDER BY stage, node_name COLLATE NOCASE, node_id`, rolloutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OsStepRow
	for rows.Next() {
		var x OsStepRow
		var pre, params string
		var sends, acked, phaseAt, boot, sent, reboot, back, fin int64
		if err := rows.Scan(&x.RolloutID, &x.NodeID, &x.NodeName, &x.Stage, &x.State, &x.JobID, &sends, &acked, &x.Phase, &phaseAt, &boot,
			&x.KernelFrom, &x.KernelTo, &pre, &sent, &reboot, &back, &fin, &x.ErrorKey, &params); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(pre), &x.PreFailed)
		_ = json.Unmarshal([]byte(params), &x.Params)
		x.Sends = int(sends)
		x.AckedAt, x.PhaseAt, x.BootBefore, x.SentAt, x.RebootAt, x.BackAt, x.FinishedAt =
			fleetTime(acked), fleetTime(phaseAt), fleetTime(boot), fleetTime(sent), fleetTime(reboot), fleetTime(back), fleetTime(fin)
		x.loaded, x.loadedState = true, x.State
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetOsRolloutStatus moves a rollout to status only while it is in one of from (a compare-and-set). pauseKey and params
// describe a pause and are cleared by any other status. It reports whether the row changed.
func (s *Store) SetOsRolloutStatus(ctx context.Context, id string, from []string, status, pauseKey string, params map[string]string, finished time.Time) (bool, error) {
	if len(from) == 0 {
		return false, nil
	}
	if status != RolloutPaused {
		pauseKey, params = "", nil
	}
	q := `UPDATE os_rollout SET status = ?, pause_key = ?, pause_params_json = ?, finished_at = ? WHERE id = ? AND status IN (?` +
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

// SaveOsStep overwrites the mutable fields of a step. A row read by OsRolloutSteps is a compare-and-set on its state.
func (s *Store) SaveOsStep(ctx context.Context, x *OsStepRow) error {
	if x == nil {
		return ErrNotFound
	}
	query := `UPDATE os_step SET state = ?, job_id = ?, sends = ?, acked_at = ?, phase = ?, phase_at = ?, boot_before = ?, kernel_from = ?, kernel_to = ?,
		pre_failed_json = ?, sent_at = ?, reboot_at = ?, back_at = ?, finished_at = ?, error_key = ?, params_json = ?
		WHERE rollout_id = ? AND node_id = ?`
	args := []any{x.State, x.JobID, int64(x.Sends), fleetUnix(x.AckedAt), x.Phase, fleetUnix(x.PhaseAt), fleetUnix(x.BootBefore), x.KernelFrom, x.KernelTo,
		jsonStrings(x.PreFailed), fleetUnix(x.SentAt), fleetUnix(x.RebootAt), fleetUnix(x.BackAt), fleetUnix(x.FinishedAt), x.ErrorKey, jsonMap(x.Params),
		x.RolloutID, x.NodeID}
	if x.loaded {
		query += ` AND state = ?`
		args = append(args, x.loadedState)
	}
	res, err := s.W.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if x.loaded {
			return ErrConflict
		}
		return ErrNotFound
	}
	x.loaded, x.loadedState = true, x.State
	return nil
}

// SkipPendingOsSteps turns every PENDING step of a rollout into SKIPPED with errorKey (cancel).
func (s *Store) SkipPendingOsSteps(ctx context.Context, rolloutID, errorKey string, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE os_step SET state = 'skipped', error_key = ?, finished_at = ? WHERE rollout_id = ? AND state = 'pending'`,
		errorKey, unix(now), rolloutID)
	return err
}

// PruneOsRollouts deletes finished OS rollouts (their steps go with them) older than cutoff that are not among the newest keep
// finished ones. An active rollout is never touched.
func (s *Store) PruneOsRollouts(ctx context.Context, keep int, cutoff time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `
		DELETE FROM os_rollout
		WHERE status NOT IN ('running', 'paused') AND created_at < ?
		  AND id NOT IN (SELECT id FROM os_rollout WHERE status NOT IN ('running', 'paused')
		                 ORDER BY created_at DESC, rowid DESC LIMIT ?)`, unix(cutoff), max(keep, 0))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OsRebootsSince lists, per node, when the panel's OS jobs rebooted it (os_step.reboot_at) at or after since, oldest first. A
// step that never reached its reboot has no such time.
func (s *Store) OsRebootsSince(ctx context.Context, since time.Time) (map[string][]time.Time, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, reboot_at FROM os_step WHERE reboot_at > 0 AND reboot_at >= ? ORDER BY reboot_at`, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]time.Time{}
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = append(out[id], fromUnix(at))
	}
	return out, rows.Err()
}

// OsJobEvents returns, oldest first, the events an agent raised for one OS job (os_update_started, os_reboot_started,
// os_update_done, os_update_failed). They are the reliable record of how a job ended: the status snapshot is sent once and
// may be lost. An agent can raise one more than once; the caller reads them by job id and takes the first end it finds.
func (s *Store) OsJobEvents(ctx context.Context, nodeID, jobID string, since time.Time) ([]EventRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT ts, severity, code, params_json FROM event
		WHERE node_id = ? AND ts >= ? AND code IN ('os_update_started', 'os_reboot_started', 'os_update_done', 'os_update_failed')
		  AND json_extract(params_json, '$.job_id') = ? ORDER BY id LIMIT 20`, nodeID, unix(since), jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var ts int64
		var params string
		if err := rows.Scan(&ts, &e.Severity, &e.Code, &params); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(params), &e.Params)
		e.Time, e.NodeID = fromUnix(ts), nodeID
		out = append(out, e)
	}
	return out, rows.Err()
}
