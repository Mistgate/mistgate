package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Queries of the MCP plan/apply protocol. Table: migration 00016. Every state
// change is a compare-and-swap on the status, so two racing callers cannot both win.

// Plan statuses, the values of mcp_plan.status.
const (
	PlanPlanned   = "planned"
	PlanAwaiting  = "awaiting"
	PlanApproved  = "approved"
	PlanRejected  = "rejected"
	PlanExpired   = "expired"
	PlanApplying  = "applying"
	PlanApplied   = "applied"
	PlanFailed    = "failed"
	PlanCancelled = "cancelled"
)

const (
	// PlanTTL is how long a plan (and, for a dangerous one, the owner's decision) stays valid.
	PlanTTL = 10 * time.Minute
	// ApplyStuckAfter is how long a plan may stay `applying` before the sweep gives up on it (a crash mid-call).
	ApplyStuckAfter = 5 * time.Minute
	// PlanKeep is how long finished plans stay before the sweep deletes them.
	PlanKeep = 30 * 24 * time.Hour
)

var (
	// ErrPlanState: the plan is not in a state the call needs. The error is a *PlanStateError that carries the
	// status found (errors.As); errors.Is(err, ErrPlanState) is true for it.
	ErrPlanState = errors.New("store: plan is in another state")
	// ErrTooManyPlans: the token holds too many open plans, or too many plans wait for the owner.
	ErrTooManyPlans = errors.New("store: too many open plans")
)

// PlanStateError is ErrPlanState with the status that was found. A plan past its expires_at reports "expired"
// even before the sweep has marked it.
type PlanStateError struct{ Status string }

func (e *PlanStateError) Error() string        { return "store: plan is " + e.Status }
func (e *PlanStateError) Is(target error) bool { return target == ErrPlanState }

// MCPPlan is one mcp_plan row together with the names of its token and its decider.
type MCPPlan struct {
	ID, TokenID, TokenName, TokenProfile, Tool string
	ParamsJSON, FactsJSON, Summary, Danger     string
	Reason, InnerRef                           string
	ParamsHash, ConfirmHash                    []byte
	NeedsApproval                              bool
	Status                                     string
	CreatedAt, ExpiresAt, DecidedAt            time.Time
	AppliedAt                                  time.Time // while applying: when it began; after: when it finished
	DecidedBy, DecidedByName                   string
	Result, Error                              string
	// What came of it as a code the admin UI words itself, and its values (a JSON object of strings); "" before the end.
	OutcomeCode, OutcomeParams string
}

// Open reports whether the plan can still be applied or decided at now.
func (p MCPPlan) Open(now time.Time) bool {
	switch p.Status {
	case PlanPlanned, PlanAwaiting, PlanApproved:
		return now.Before(p.ExpiresAt)
	}
	return false
}

const planColumns = `p.id, p.token_id, COALESCE(t.name, '') AS token_name, COALESCE(t.profile, '') AS token_profile,
	p.tool, p.params_json, p.facts_json,
	p.summary, p.danger, p.reason, p.inner_ref, p.params_hash, p.confirm_hash, p.needs_approval, p.status,
	p.created_at, p.expires_at, p.decided_at, p.applied_at, p.decided_by, COALESCE(d.display_name, '') AS decided_by_name, p.result, p.error,
	p.outcome_code, p.outcome_params`

const planFrom = ` FROM mcp_plan p LEFT JOIN api_token t ON t.id = p.token_id LEFT JOIN admin d ON d.id = p.decided_by `

func scanPlan(r rowScanner) (MCPPlan, error) {
	var p MCPPlan
	var needs int
	var created, expires, decided, applied int64
	err := r.Scan(&p.ID, &p.TokenID, &p.TokenName, &p.TokenProfile, &p.Tool, &p.ParamsJSON, &p.FactsJSON,
		&p.Summary, &p.Danger, &p.Reason, &p.InnerRef, &p.ParamsHash, &p.ConfirmHash, &needs, &p.Status,
		&created, &expires, &decided, &applied, &p.DecidedBy, &p.DecidedByName, &p.Result, &p.Error, &p.OutcomeCode, &p.OutcomeParams)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.NeedsApproval = needs != 0
	p.CreatedAt, p.ExpiresAt = fromUnix(created), fromUnix(expires)
	if decided != 0 {
		p.DecidedAt = fromUnix(decided)
	}
	if applied != 0 {
		p.AppliedAt = fromUnix(applied)
	}
	return p, nil
}

func scanPlanRow(values []any, p *MCPPlan) error {
	got, err := scanPlan(batchRow(values))
	if err == nil {
		*p = got
	}
	return err
}

// effective is the status a reader should see at now: an unapplied plan past its expiry is expired.
func (p MCPPlan) effective(now time.Time) string {
	switch p.Status {
	case PlanPlanned, PlanAwaiting, PlanApproved:
		if !now.Before(p.ExpiresAt) {
			return PlanExpired
		}
	}
	return p.Status
}

const openStatuses = `('planned', 'awaiting', 'approved')`

// CreateMCPPlan stores a new plan. Status must be planned (a safe tool) or awaiting (needs_approval). ID,
// ParamsHash (SHA-256 of ParamsJSON) and the expiry (CreatedAt + PlanTTL) are filled in when empty; ConfirmHash
// is required. A token may hold maxOpenPerToken open plans, and a new awaiting plan counts against maxAwaiting
// awaiting ones in the whole panel (ErrTooManyPlans). A revoked token cannot make plans (*PlanStateError cancelled).
func (s *Store) CreateMCPPlan(ctx context.Context, p MCPPlan, maxOpenPerToken, maxAwaiting int) error {
	if p.Status != PlanPlanned && p.Status != PlanAwaiting {
		return fmt.Errorf("store: a new plan is planned or awaiting, not %q", p.Status)
	}
	if (p.Status == PlanAwaiting) != p.NeedsApproval {
		return errors.New("store: an awaiting plan, and only it, needs approval")
	}
	if len(p.ConfirmHash) == 0 || p.TokenID == "" || p.Tool == "" {
		return errors.New("store: a plan needs a token, a tool and a confirm hash")
	}
	if p.ID == "" {
		p.ID = NewID("pln_")
	}
	if len(p.ParamsHash) == 0 {
		h := sha256.Sum256([]byte(p.ParamsJSON))
		p.ParamsHash = h[:]
	}
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = p.CreatedAt.Add(PlanTTL)
	}
	if p.Danger == "" {
		p.Danger = "[]"
	}
	if p.FactsJSON == "" {
		p.FactsJSON = "[]"
	}
	now := unix(p.CreatedAt)
	results, err := s.batch(ctx,
		Stmt{Query: `SELECT EXISTS (SELECT 1 FROM api_token WHERE id = ?) AS token_exists,
			COALESCE((SELECT revoked_at FROM api_token WHERE id = ?), 0) AS revoked_at,
			(SELECT count(*) FROM mcp_plan WHERE token_id = ? AND status IN ` + openStatuses + ` AND expires_at > ?) AS token_open_count,
			(SELECT count(*) FROM mcp_plan WHERE status = 'awaiting' AND expires_at > ?) AS panel_awaiting_count`,
			Args: []any{p.TokenID, p.TokenID, p.TokenID, now, now}, Returning: true},
		Stmt{Query: `INSERT INTO mcp_plan
		(id, token_id, tool, params_json, params_hash, confirm_hash, facts_json, summary, danger,
		 needs_approval, reason, inner_ref, status, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM api_token WHERE id = ? AND revoked_at = 0)
		AND (SELECT count(*) FROM mcp_plan WHERE token_id = ? AND status IN ` + openStatuses + ` AND expires_at > ?) < ?
		AND (? <> 'awaiting' OR (SELECT count(*) FROM mcp_plan WHERE status = 'awaiting' AND expires_at > ?) < ?)`,
			Args: []any{p.ID, p.TokenID, p.Tool, p.ParamsJSON, p.ParamsHash, p.ConfirmHash, p.FactsJSON, p.Summary, p.Danger,
				int64(accBool(p.NeedsApproval)), Clip(p.Reason, 300), p.InnerRef, p.Status, now, unix(p.ExpiresAt),
				p.TokenID, p.TokenID, now, int64(maxOpenPerToken), p.Status, now, int64(maxAwaiting)}},
	)
	if err != nil {
		return err
	}
	if results[1].RowsAffected == 1 {
		return nil
	}
	if len(results[0].Rows) != 1 {
		return errors.New("store: plan limit check returned no row")
	}
	state := results[0].Rows[0]
	exists, _ := state[0].(int64)
	revoked, _ := state[1].(int64)
	open, _ := state[2].(int64)
	awaiting, _ := state[3].(int64)
	if exists == 0 {
		return ErrNotFound
	}
	if revoked != 0 {
		return &PlanStateError{Status: PlanCancelled}
	}
	if open >= int64(maxOpenPerToken) {
		return ErrTooManyPlans
	}
	if p.Status == PlanAwaiting && awaiting >= int64(maxAwaiting) {
		return ErrTooManyPlans
	}
	return ErrTooManyPlans
}

// MCPPlanByConfirm finds the plan of a token by SHA-256 of its confirm token. The status is the stored one:
// compare ExpiresAt with the clock. ErrNotFound if the token has no such plan.
func (s *Store) MCPPlanByConfirm(ctx context.Context, tokenID string, confirmHash []byte) (MCPPlan, error) {
	return scanPlan(s.R.QueryRowContext(ctx, `SELECT `+planColumns+planFrom+`WHERE p.token_id = ? AND p.confirm_hash = ?`, tokenID, confirmHash))
}

// GetMCPPlan returns one plan with its stored status. ErrNotFound if there is none.
func (s *Store) GetMCPPlan(ctx context.Context, id string) (MCPPlan, error) {
	return scanPlan(s.R.QueryRowContext(ctx, `SELECT `+planColumns+planFrom+`WHERE p.id = ?`, id))
}

// ListApprovals returns the plans that need the owner (needs_approval = 1): those awaiting a decision first
// (oldest first), then up to historyLimit others, newest first (0 = 50, at most 200), unless awaitingOnly. The
// status of a plan past its expiry is reported as expired whether or not the sweep ran. awaiting counts the
// plans still awaiting at now.
func (s *Store) ListApprovals(ctx context.Context, now time.Time, awaitingOnly bool, historyLimit int) (rows []MCPPlan, awaiting int, err error) {
	if historyLimit <= 0 {
		historyLimit = 50
	}
	historyLimit = min(historyLimit, 200)
	q := func(where, order string, limit int) ([]MCPPlan, error) {
		r, err := s.R.QueryContext(ctx, `SELECT `+planColumns+planFrom+`WHERE p.needs_approval = 1 AND `+where+` ORDER BY `+order+` LIMIT ?`, now.Unix(), limit)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		var out []MCPPlan
		for r.Next() {
			p, err := scanPlan(r)
			if err != nil {
				return nil, err
			}
			p.Status = p.effective(now)
			out = append(out, p)
		}
		return out, r.Err()
	}
	waiting, err := q(`p.status = 'awaiting' AND p.expires_at > ?`, `p.created_at, p.rowid`, 1000)
	if err != nil {
		return nil, 0, err
	}
	rows, awaiting = waiting, len(waiting)
	if awaitingOnly {
		return rows, awaiting, nil
	}
	history, err := q(`NOT (p.status = 'awaiting' AND p.expires_at > ?)`, `p.created_at DESC, p.rowid DESC`, historyLimit)
	if err != nil {
		return nil, 0, err
	}
	return append(rows, history...), awaiting, nil
}

// DecideMCPPlan is the owner's answer to a plan awaiting approval: a compare-and-swap awaiting -> approved or
// rejected that also needs the plan unexpired and its token unrevoked. ErrNotFound for an unknown id,
// *PlanStateError (ErrPlanState) with the status found when the plan is not awaiting any more.
func (s *Store) DecideMCPPlan(ctx context.Context, id, adminID string, approve bool, now time.Time) (MCPPlan, error) {
	return s.decideMCPPlan(ctx, id, adminID, approve, nil, now)
}

// ApproveMCPPlanWithSecret is DecideMCPPlan's approval that also stores what the owner entered for the apply (vault
// ciphertext: the node_install SSH password), in the same compare-and-swap. TakeMCPPlanOwnerSecret hands it out once.
func (s *Store) ApproveMCPPlanWithSecret(ctx context.Context, id, adminID string, secret []byte, now time.Time) (MCPPlan, error) {
	if len(secret) == 0 {
		return MCPPlan{}, errors.New("store: an owner secret is required")
	}
	return s.decideMCPPlan(ctx, id, adminID, true, secret, now)
}

func (s *Store) decideMCPPlan(ctx context.Context, id, adminID string, approve bool, secret []byte, now time.Time) (MCPPlan, error) {
	to := PlanRejected
	if approve {
		to = PlanApproved
	}
	var sealed any // NULL without a secret
	if len(secret) > 0 {
		sealed = secret
	}
	results, err := s.batch(ctx, Stmt{Query: `
		UPDATE mcp_plan SET status = ?, decided_by = ?, decided_at = ?, owner_secret = ?
		WHERE id = ? AND status = 'awaiting' AND needs_approval = 1 AND expires_at > ?
		  AND EXISTS (SELECT 1 FROM api_token t WHERE t.id = mcp_plan.token_id AND t.revoked_at = 0)`,
		Args: []any{to, adminID, unix(now), sealed, id, unix(now)}},
		Stmt{Query: `SELECT ` + planColumns + planFrom + `WHERE p.id = ?`, Args: []any{id}, Returning: true},
	)
	if err != nil {
		return MCPPlan{}, err
	}
	var p MCPPlan
	if len(results[1].Rows) == 0 {
		return MCPPlan{}, ErrNotFound
	}
	if err := scanPlanRow(results[1].Rows[0], &p); err != nil {
		return MCPPlan{}, err
	}
	if results[0].RowsAffected == 0 {
		return MCPPlan{}, &PlanStateError{Status: p.effective(now)}
	}
	return p, nil
}

// BeginApply is the compare-and-swap planned|approved -> applying that lets exactly one caller run a plan. It
// needs the plan to belong to tokenID, to be for tool, to carry the given confirm hash and params hash (both
// are also checked against SHA-256 of the stored params_json, so a tampered row is refused) and to be
// unexpired. A plan that is not there for this token and tool is ErrNotFound: one answer, no oracle. A plan in
// another state is a *PlanStateError (ErrPlanState) with the status found. applied_at is set to now (the start).
func (s *Store) BeginApply(ctx context.Context, id, tokenID, tool string, paramsHash, confirmHash []byte, now time.Time) (MCPPlan, error) {
	p, err := s.GetMCPPlan(ctx, id)
	if err != nil {
		return MCPPlan{}, err
	}
	if p.TokenID != tokenID || p.Tool != tool || !bytes.Equal(p.ConfirmHash, confirmHash) {
		return MCPPlan{}, ErrNotFound
	}
	sum := sha256.Sum256([]byte(p.ParamsJSON))
	if !bytes.Equal(p.ParamsHash, sum[:]) || !bytes.Equal(paramsHash, sum[:]) {
		return MCPPlan{}, fmt.Errorf("store: plan %s: the stored arguments do not match their hash", id)
	}
	results, err := s.batch(ctx, Stmt{Query: `
		UPDATE mcp_plan SET status = 'applying', applied_at = ?
		WHERE id = ? AND token_id = ? AND tool = ? AND params_hash = ? AND confirm_hash = ?
		  AND params_json = ? AND status IN ('planned', 'approved') AND expires_at > ?`,
		Args: []any{unix(now), id, tokenID, tool, paramsHash, confirmHash, p.ParamsJSON, unix(now)}},
		Stmt{Query: `SELECT ` + planColumns + planFrom + `WHERE p.id = ?`, Args: []any{id}, Returning: true},
	)
	if err != nil {
		return MCPPlan{}, err
	}
	if len(results[1].Rows) == 0 {
		return MCPPlan{}, ErrNotFound
	}
	var current MCPPlan
	if err := scanPlanRow(results[1].Rows[0], &current); err != nil {
		return MCPPlan{}, err
	}
	if results[0].RowsAffected == 0 {
		return MCPPlan{}, &PlanStateError{Status: current.effective(now)}
	}
	return current, nil
}

// TakeMCPPlanOwnerSecret hands out, once, what the owner entered when approving plan id of tool for tokenID, and clears
// it: only while that plan is applying with a recorded human decision. ErrNotFound otherwise (nothing stored, already
// taken, another token or tool, or a plan in another state).
func (s *Store) TakeMCPPlanOwnerSecret(ctx context.Context, id, tokenID, tool string) ([]byte, error) {
	var secret []byte
	err := s.R.QueryRowContext(ctx, `SELECT owner_secret FROM mcp_plan
		WHERE id = ? AND token_id = ? AND tool = ? AND status = 'applying' AND needs_approval = 1 AND decided_by <> ''
		  AND owner_secret IS NOT NULL`, id, tokenID, tool).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) || err == nil && len(secret) == 0 {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	res, err := s.W.ExecContext(ctx, `UPDATE mcp_plan SET owner_secret = NULL WHERE id = ? AND token_id = ? AND tool = ?
		AND status = 'applying' AND needs_approval = 1 AND decided_by <> '' AND owner_secret = ?`, id, tokenID, tool, secret)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, ErrNotFound
	}
	return secret, nil
}

// FinishApply ends an apply: applying -> applied (ok, with result) or failed (errText), with the outcome as a code and
// its values (a JSON object of strings, "" = none). An owner secret still there is cleared. *PlanStateError if the plan
// is not applying (the sweep may have given up on it).
func (s *Store) FinishApply(ctx context.Context, id string, ok bool, result, errText, outcomeCode, outcomeParams string, now time.Time) error {
	to := PlanFailed
	if ok {
		to = PlanApplied
	}
	if outcomeParams == "" {
		outcomeParams = "{}"
	}
	res, err := s.W.ExecContext(ctx, `UPDATE mcp_plan SET status = ?, applied_at = ?, result = ?, error = ?, outcome_code = ?, outcome_params = ?, owner_secret = NULL WHERE id = ? AND status = 'applying'`,
		to, unix(now), Clip(result, 500), Clip(errText, 300), Clip(outcomeCode, 64), Clip(outcomeParams, 1000), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		p, err := s.GetMCPPlan(ctx, id)
		if err != nil {
			return err
		}
		return &PlanStateError{Status: p.Status}
	}
	return nil
}

// ExpireMCPPlans is the once-a-minute sweep: unapplied plans past their expiry become expired; plans stuck in
// applying for ApplyStuckAfter (the panel restarted mid-call: the outcome is unknown and an apply is never
// resumed) become failed; finished plans older than PlanKeep are deleted; an owner secret left on a plan that can no
// longer be applied is cleared. It returns how many rows it touched.
func (s *Store) ExpireMCPPlans(ctx context.Context, now time.Time) (int, error) {
	total := 0
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE mcp_plan SET status = 'expired' WHERE status IN ` + openStatuses + ` AND expires_at <= ?`, []any{unix(now)}},
		{`UPDATE mcp_plan SET status = 'failed', error = 'interrupted: check the result before retrying', outcome_code = 'interrupted' WHERE status = 'applying' AND applied_at <= ?`,
			[]any{unix(now.Add(-ApplyStuckAfter))}},
		{`DELETE FROM mcp_plan WHERE status IN ('rejected', 'expired', 'applied', 'failed', 'cancelled') AND created_at < ?`, []any{unix(now.Add(-PlanKeep))}},
		// an owner secret lives only while its plan can still be applied
		{`UPDATE mcp_plan SET owner_secret = NULL WHERE owner_secret IS NOT NULL AND status NOT IN ('approved', 'applying')`, nil},
	} {
		res, err := s.W.ExecContext(ctx, q.sql, q.args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}
