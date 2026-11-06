package store

import (
	"context"
	"errors"
	"time"
)

var ErrAssistantLimit = errors.New("store: assistant limit reached")

type AssistantProviderChoice struct {
	Provider int32
	Model    string
	Budget   float64
}

// SaveAssistantSettings applies the provider choices and shared settings as one database batch on both store backends.
func (s *Store) SaveAssistantSettings(ctx context.Context, choices []AssistantProviderChoice, enabled bool, provider, effort, audience int32, notes string, retention uint32, now time.Time) error {
	statements := make([]Stmt, 0, len(choices)+1)
	for _, choice := range choices {
		statements = append(statements, Stmt{Query: `UPDATE assistant_provider SET model=?,budget_usd=? WHERE provider=?`, Args: []any{choice.Model, choice.Budget, choice.Provider}})
	}
	value := 0
	if enabled {
		value = 1
	}
	statements = append(statements, Stmt{Query: `UPDATE assistant_settings SET enabled=?,provider=?,effort=?,audience=?,panel_notes=?,retention_days=?,updated_at=? WHERE id=1`, Args: []any{value, provider, effort, audience, notes, retention, unix(now)}})
	_, err := s.batch(ctx, statements...)
	return err
}

// TakeAssistantRate admits one turn in the current minute, with the counter shared by D1 isolates.
func (s *Store) TakeAssistantRate(ctx context.Context, adminID string, minute int64, limit int) (bool, error) {
	r, err := s.W.ExecContext(ctx, `INSERT INTO assistant_rate(admin_id,minute,turns) VALUES(?,?,1)
		ON CONFLICT(admin_id,minute) DO UPDATE SET turns=assistant_rate.turns+1 WHERE assistant_rate.turns < ?`, adminID, minute, limit)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) AcquireAssistantTurn(ctx context.Context, conversationID, adminID, leaseID string, now, expires time.Time) (bool, error) {
	r, err := s.W.ExecContext(ctx, `INSERT INTO assistant_lock(conversation_id,admin_id,lease_id,expires_at,stop_requested) VALUES(?,?,?,?,0)
		ON CONFLICT(conversation_id) DO UPDATE SET admin_id=excluded.admin_id,lease_id=excluded.lease_id,expires_at=excluded.expires_at,stop_requested=0
		WHERE assistant_lock.expires_at <= ?`, conversationID, adminID, leaseID, unix(expires), unix(now))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) RequestAssistantStop(ctx context.Context, conversationID, adminID string, now time.Time) (bool, error) {
	r, err := s.W.ExecContext(ctx, `UPDATE assistant_lock SET stop_requested=1 WHERE conversation_id=? AND admin_id=? AND expires_at>?`, conversationID, adminID, unix(now))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) InsertAssistantMemory(ctx context.Context, id, adminID string, sealed []byte, byAssistant bool, conversationID string, now time.Time) error {
	by := 0
	if byAssistant {
		by = 1
	}
	r, err := s.W.ExecContext(ctx, `INSERT INTO assistant_memory(id,admin_id,text_enc,by_assistant,conversation_id,created_at,updated_at)
		SELECT ?,?,?,?,?,?,? WHERE (SELECT count(*) FROM assistant_memory WHERE admin_id=?) < 100`, id, adminID, sealed, by, conversationID, unix(now), unix(now), adminID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAssistantLimit
	}
	return nil
}

func (s *Store) UpdateAssistantMemory(ctx context.Context, id, adminID string, sealed []byte, now time.Time) (bool, error) {
	r, err := s.W.ExecContext(ctx, `UPDATE assistant_memory SET text_enc=?,by_assistant=0,conversation_id='',updated_at=? WHERE id=? AND admin_id=?`, sealed, unix(now), id, adminID)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) DeleteAssistantMemory(ctx context.Context, id, adminID string) (bool, error) {
	r, err := s.W.ExecContext(ctx, `DELETE FROM assistant_memory WHERE id=? AND admin_id=?`, id, adminID)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) DeleteAssistantMemoryAll(ctx context.Context, adminID string) (int64, error) {
	r, err := s.W.ExecContext(ctx, `DELETE FROM assistant_memory WHERE admin_id=?`, adminID)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

func (s *Store) AssistantStopRequested(ctx context.Context, conversationID, leaseID string) (bool, error) {
	var stop int
	err := s.R.QueryRowContext(ctx, `SELECT stop_requested FROM assistant_lock WHERE conversation_id=? AND lease_id=?`, conversationID, leaseID).Scan(&stop)
	if err != nil {
		return false, err
	}
	return stop != 0, nil
}

func (s *Store) ReleaseAssistantTurn(ctx context.Context, conversationID, leaseID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM assistant_lock WHERE conversation_id=? AND lease_id=?`, conversationID, leaseID)
	return err
}

func (s *Store) CancelAssistantMCPPlan(ctx context.Context, id, tokenID string, now time.Time) (bool, error) {
	r, err := s.W.ExecContext(ctx, `UPDATE mcp_plan SET status='cancelled',error='dismissed in assistant' WHERE id=? AND token_id=? AND status IN ('planned','awaiting') AND expires_at>?`, id, tokenID, unix(now))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *Store) DeleteAssistantConversation(ctx context.Context, adminID, id string) (bool, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM assistant_plan WHERE conversation_id=? AND admin_id=?`, Args: []any{id, adminID}},
		Stmt{Query: `DELETE FROM assistant_message WHERE conversation_id=? AND admin_id=?`, Args: []any{id, adminID}},
		Stmt{Query: `DELETE FROM assistant_conversation WHERE id=? AND admin_id=?`, Args: []any{id, adminID}},
	)
	if err != nil {
		return false, err
	}
	return results[2].RowsAffected > 0, nil
}

func (s *Store) DeleteAssistantConversations(ctx context.Context, adminID string, all bool) (int64, error) {
	where := "admin_id=?"
	args := []any{adminID}
	if all {
		where = "1=1"
		args = nil
	}
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM assistant_plan WHERE conversation_id IN (SELECT id FROM assistant_conversation WHERE ` + where + `)`, Args: args},
		Stmt{Query: `DELETE FROM assistant_message WHERE conversation_id IN (SELECT id FROM assistant_conversation WHERE ` + where + `)`, Args: args},
		Stmt{Query: `DELETE FROM assistant_conversation WHERE ` + where, Args: args},
	)
	if err != nil {
		return 0, err
	}
	return results[2].RowsAffected, nil
}

func (s *Store) RetainAssistantConversations(ctx context.Context, cutoff time.Time) (int64, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM assistant_plan WHERE conversation_id IN (SELECT id FROM assistant_conversation WHERE updated_at < ?)`, Args: []any{unix(cutoff)}},
		Stmt{Query: `DELETE FROM assistant_message WHERE conversation_id IN (SELECT id FROM assistant_conversation WHERE updated_at < ?)`, Args: []any{unix(cutoff)}},
		Stmt{Query: `DELETE FROM assistant_conversation WHERE updated_at < ?`, Args: []any{unix(cutoff)}},
	)
	if err != nil {
		return 0, err
	}
	return results[2].RowsAffected, nil
}

func (s *Store) AssistantRetention(ctx context.Context, now time.Time) error {
	if _, err := s.W.ExecContext(ctx, `INSERT INTO assistant_settings (id) VALUES (1) ON CONFLICT(id) DO NOTHING`); err != nil {
		return err
	}
	var days uint32
	if err := s.R.QueryRowContext(ctx, `SELECT retention_days FROM assistant_settings WHERE id=1`).Scan(&days); err != nil {
		return err
	}
	if days == 0 {
		return nil
	}
	_, err := s.RetainAssistantConversations(ctx, now.Add(-time.Duration(days)*24*time.Hour))
	return err
}
