//go:build !js

package store

import (
	"context"
	"errors"
	"time"
)

// The rows behind assistant_follow_up (migration 00117): the follow-ups the assistant set in a conversation, which the
// scheduled tasks' scheduler runs at their time (internal/panel/tasks). The Cloudflare edition has no assistant, so this file
// is not in the Workers' wasm.

const (
	// FollowUpsPerConversation and FollowUpsPerAdmin bound the follow-ups waiting at once.
	FollowUpsPerConversation = 3
	FollowUpsPerAdmin        = 20
)

var (
	// ErrFollowUpConversationLimit: the conversation already has FollowUpsPerConversation follow-ups waiting.
	ErrFollowUpConversationLimit = errors.New("store: too many follow-ups in the conversation")
	// ErrFollowUpAdminLimit: the admin already has FollowUpsPerAdmin follow-ups waiting.
	ErrFollowUpAdminLimit = errors.New("store: too many follow-ups of the admin")
)

// AssistantFollowUp is one row of assistant_follow_up.
type AssistantFollowUp struct {
	ID             string
	ConversationID string
	AdminID        string
	NoteEnc        []byte // sealed by the assistant service
	PlanID         string // the plan whose apply starts the clock; "" = it counts from CreatedAt
	DelayS         int
	NotifyTelegram bool
	CreatedAt      time.Time
	DueAt          time.Time // zero while it waits for its plan
	LeaseID        string
	LeaseUntil     time.Time
	Attempts       int
}

// Running reports whether a run holds the follow-up at now.
func (f AssistantFollowUp) Running(now time.Time) bool {
	return f.LeaseID != "" && f.LeaseUntil.After(now)
}

const followUpColumns = `id, conversation_id, admin_id, note_enc, plan_id, delay_s, notify_telegram, created_at, due_at, lease_id, lease_until, attempts`

func scanFollowUp(row interface{ Scan(...any) error }) (AssistantFollowUp, error) {
	var f AssistantFollowUp
	var created, due, leaseUntil int64
	err := row.Scan(&f.ID, &f.ConversationID, &f.AdminID, &f.NoteEnc, &f.PlanID, &f.DelayS, &f.NotifyTelegram, &created, &due, &f.LeaseID, &leaseUntil, &f.Attempts)
	if err != nil {
		return f, err
	}
	f.CreatedAt, f.DueAt, f.LeaseUntil = fromUnix(created), zeroUnix(due), zeroUnix(leaseUntil)
	return f, nil
}

func (s *Store) followUpsWhere(ctx context.Context, where string, args ...any) ([]AssistantFollowUp, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+followUpColumns+` FROM assistant_follow_up WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssistantFollowUp
	for rows.Next() {
		f, err := scanFollowUp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertAssistantFollowUp stores a follow-up in a conversation of its admin, unless the conversation is gone
// (ErrAssistantConversationGone) or a limit is reached (ErrFollowUpConversationLimit, ErrFollowUpAdminLimit). The limits are
// part of the insert itself, so two calls at once cannot pass them.
func (s *Store) InsertAssistantFollowUp(ctx context.Context, f AssistantFollowUp) error {
	r, err := s.W.ExecContext(ctx, `INSERT INTO assistant_follow_up (id, conversation_id, admin_id, note_enc, plan_id, delay_s, notify_telegram, created_at, due_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM assistant_conversation WHERE id = ? AND admin_id = ?)
			AND (SELECT count(*) FROM assistant_follow_up WHERE conversation_id = ?) < ?
			AND (SELECT count(*) FROM assistant_follow_up WHERE admin_id = ?) < ?`,
		f.ID, f.ConversationID, f.AdminID, nonNilBlob(f.NoteEnc), f.PlanID, f.DelayS, scheduledBool(f.NotifyTelegram), unix(f.CreatedAt), unixOrZero(f.DueAt),
		f.ConversationID, f.AdminID, f.ConversationID, FollowUpsPerConversation, f.AdminID, FollowUpsPerAdmin)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	var conversations, inConversation, ofAdmin int
	if err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM assistant_conversation WHERE id = ? AND admin_id = ?),
		(SELECT count(*) FROM assistant_follow_up WHERE conversation_id = ?), (SELECT count(*) FROM assistant_follow_up WHERE admin_id = ?)`,
		f.ConversationID, f.AdminID, f.ConversationID, f.AdminID).Scan(&conversations, &inConversation, &ofAdmin); err != nil {
		return err
	}
	switch {
	case conversations == 0:
		return ErrAssistantConversationGone
	case inConversation >= FollowUpsPerConversation:
		return ErrFollowUpConversationLimit
	default:
		return ErrFollowUpAdminLimit
	}
}

// AssistantFollowUps lists the follow-ups of an admin's conversation, the soonest first (those waiting for a plan last).
func (s *Store) AssistantFollowUps(ctx context.Context, adminID, conversationID string) ([]AssistantFollowUp, error) {
	return s.followUpsWhere(ctx, `admin_id = ? AND conversation_id = ? ORDER BY due_at = 0, due_at, created_at, id`, adminID, conversationID)
}

// AssistantFollowUp reads one follow-up of an admin; ErrNotFound when there is none.
func (s *Store) AssistantFollowUp(ctx context.Context, adminID, id string) (AssistantFollowUp, error) {
	list, err := s.followUpsWhere(ctx, `admin_id = ? AND id = ?`, adminID, id)
	if err != nil {
		return AssistantFollowUp{}, err
	}
	if len(list) == 0 {
		return AssistantFollowUp{}, ErrNotFound
	}
	return list[0], nil
}

// NextAssistantFollowUps is, per conversation of an admin, the time of its next follow-up (those waiting for a plan left out).
func (s *Store) NextAssistantFollowUps(ctx context.Context, adminID string) (map[string]time.Time, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT conversation_id, min(due_at) FROM assistant_follow_up WHERE admin_id = ? AND due_at > 0 GROUP BY conversation_id`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var due int64
		if err := rows.Scan(&id, &due); err != nil {
			return nil, err
		}
		out[id] = fromUnix(due)
	}
	return out, rows.Err()
}

// DeleteAssistantFollowUp removes an admin's follow-up that no run holds at now; false when there is none (or it is running).
func (s *Store) DeleteAssistantFollowUp(ctx context.Context, adminID, conversationID, id string, now time.Time) (bool, error) {
	r, err := s.W.ExecContext(ctx, `DELETE FROM assistant_follow_up WHERE id = ? AND admin_id = ? AND conversation_id = ? AND lease_until <= ?`, id, adminID, conversationID, unix(now))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// ArmAssistantFollowUps starts the clock of the follow-ups that wait for a plan: one whose plan was applied is due delay_s
// after the apply; one whose plan ended otherwise (failed, rejected, expired, dismissed, gone) goes. It returns how many went.
func (s *Store) ArmAssistantFollowUps(ctx context.Context) (int64, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `UPDATE assistant_follow_up SET due_at = (SELECT max(p.applied_at, 1) FROM mcp_plan p WHERE p.id = assistant_follow_up.plan_id) + delay_s
			WHERE due_at = 0 AND plan_id <> '' AND EXISTS (SELECT 1 FROM mcp_plan p WHERE p.id = assistant_follow_up.plan_id AND p.status = ?)`, Args: []any{PlanApplied}},
		Stmt{Query: `DELETE FROM assistant_follow_up WHERE due_at = 0 AND plan_id <> '' AND lease_until = 0
			AND NOT EXISTS (SELECT 1 FROM mcp_plan p WHERE p.id = assistant_follow_up.plan_id AND p.status IN (?, ?, ?, ?))`,
			Args: []any{PlanPlanned, PlanAwaiting, PlanApproved, PlanApplying}},
	)
	if err != nil {
		return 0, err
	}
	return results[1].RowsAffected, nil
}

// DueAssistantFollowUps lists the follow-ups whose time has come and that no run holds, the most overdue first.
func (s *Store) DueAssistantFollowUps(ctx context.Context, now time.Time, limit int) ([]AssistantFollowUp, error) {
	return s.followUpsWhere(ctx, `due_at > 0 AND due_at <= ? AND lease_until <= ? ORDER BY due_at, id LIMIT ?`, unix(now), unix(now), limit)
}

// ClaimAssistantFollowUp takes the follow-up's lease for one run until `until`: false when a run holds it or it is gone (or
// not due yet). A follow-up runs once: its row goes when the run ends (FinishAssistantFollowUp).
func (s *Store) ClaimAssistantFollowUp(ctx context.Context, id, leaseID string, now, until time.Time) (bool, error) {
	r, err := s.W.ExecContext(ctx, `UPDATE assistant_follow_up SET lease_id = ?, lease_until = ? WHERE id = ? AND due_at > 0 AND due_at <= ? AND lease_until <= ?`,
		leaseID, unix(until), id, unix(now), unix(now))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// FinishAssistantFollowUp removes the follow-up its run held: the conversation keeps what came of it.
func (s *Store) FinishAssistantFollowUp(ctx context.Context, id, leaseID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM assistant_follow_up WHERE id = ? AND lease_id = ?`, id, leaseID)
	return err
}

// DeferAssistantFollowUp gives the lease back with a later time (the conversation was busy, the store failed); counted adds
// one to its attempts.
func (s *Store) DeferAssistantFollowUp(ctx context.Context, id, leaseID string, next time.Time, counted bool) error {
	_, err := s.W.ExecContext(ctx, `UPDATE assistant_follow_up SET lease_id = '', lease_until = 0, due_at = ?, attempts = attempts + ? WHERE id = ? AND lease_id = ?`,
		unix(next), scheduledBool(counted), id, leaseID)
	return err
}

// RecoverAssistantFollowUps gives back the follow-ups a stopped panel left running: they run again (attempts says how often
// that happened). It runs once when the scheduler starts, before any run of its own.
func (s *Store) RecoverAssistantFollowUps(ctx context.Context) (int64, error) {
	r, err := s.W.ExecContext(ctx, `UPDATE assistant_follow_up SET lease_id = '', lease_until = 0, attempts = attempts + 1 WHERE lease_id <> '' OR lease_until <> 0`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// UnreadAssistantConversations counts an admin's conversations a follow-up answered since they last opened them, and gives
// the time of their next follow-up (zero: none).
func (s *Store) UnreadAssistantConversations(ctx context.Context, adminID string) (int, time.Time, error) {
	var unread int
	var next int64
	err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM assistant_conversation WHERE admin_id = ? AND unread = 1),
		COALESCE((SELECT min(due_at) FROM assistant_follow_up WHERE admin_id = ? AND due_at > 0), 0)`, adminID, adminID).Scan(&unread, &next)
	return unread, zeroUnix(next), err
}

// SetAssistantConversationUnread marks a conversation read (opened) or unread (a follow-up answered in it).
func (s *Store) SetAssistantConversationUnread(ctx context.Context, adminID, conversationID string, unread bool) error {
	_, err := s.W.ExecContext(ctx, `UPDATE assistant_conversation SET unread = ? WHERE id = ? AND admin_id = ? AND unread <> ?`,
		scheduledBool(unread), conversationID, adminID, scheduledBool(unread))
	return err
}
