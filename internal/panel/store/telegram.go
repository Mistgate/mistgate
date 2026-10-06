package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Telegram alerts: the bot and the chats of the admins who linked one. Tables: migration 00048. A chat belongs to a bot, so
// a different bot (or none) drops every link in the same transaction.

// TelegramBotRow is the stored bot. Token is vault ciphertext and must never be returned by an admin RPC.
type TelegramBotRow struct {
	Token     []byte
	BotID     int64
	Username  string
	UpdatedAt time.Time
}

// TelegramBot is the stored bot; ErrNotFound when none is set.
func (s *Store) TelegramBot(ctx context.Context) (TelegramBotRow, error) {
	var b TelegramBotRow
	var updated int64
	err := s.R.QueryRowContext(ctx, `SELECT token, bot_id, username, updated_at FROM telegram_bot WHERE id = 1`).Scan(&b.Token, &b.BotID, &b.Username, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramBotRow{}, ErrNotFound
	}
	if err != nil {
		return TelegramBotRow{}, err
	}
	b.UpdatedAt = fromUnix(updated)
	return b, nil
}

// SetTelegramBot stores the bot. When it is another bot than the one stored, every chat link is deleted with it (chat ids
// belong to a bot); dropped says how many.
func (s *Store) SetTelegramBot(ctx context.Context, b TelegramBotRow, now time.Time) (dropped int, err error) {
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM telegram_link WHERE NOT EXISTS (SELECT 1 FROM telegram_bot WHERE id = 1)
			OR EXISTS (SELECT 1 FROM telegram_bot WHERE id = 1 AND bot_id <> ?)`, Args: []any{b.BotID}},
		Stmt{Query: `INSERT INTO telegram_bot (id, token, bot_id, username, updated_at) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET token = excluded.token, bot_id = excluded.bot_id, username = excluded.username, updated_at = excluded.updated_at`,
			Args: []any{b.Token, b.BotID, b.Username, unix(now)}},
	)
	if err != nil {
		return 0, err
	}
	return int(results[0].RowsAffected), nil
}

// ClearTelegramBot forgets the bot and every link; dropped says how many links went.
func (s *Store) ClearTelegramBot(ctx context.Context) (dropped int, err error) {
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM telegram_link`},
		Stmt{Query: `DELETE FROM telegram_bot`},
	)
	if err != nil {
		return 0, err
	}
	return int(results[0].RowsAffected), nil
}

// TelegramLinkRow is one admin's chat with the admin's name and role.
type TelegramLinkRow struct {
	AdminID, AdminName, Role string
	ChatID                   int64
	Enabled                  bool
	LinkedAt                 time.Time
}

const telegramLinkSelect = `SELECT l.admin_id, a.display_name, a.role, l.chat_id, l.enabled, l.linked_at
	FROM telegram_link l JOIN admin a ON a.id = l.admin_id `

func scanTelegramLink(r rowScanner) (TelegramLinkRow, error) {
	var l TelegramLinkRow
	var enabled int
	var linked int64
	if err := r.Scan(&l.AdminID, &l.AdminName, &l.Role, &l.ChatID, &enabled, &linked); err != nil {
		return TelegramLinkRow{}, err
	}
	l.Enabled, l.LinkedAt = enabled != 0, fromUnix(linked)
	return l, nil
}

// TelegramLinks lists every linked chat, oldest first.
func (s *Store) TelegramLinks(ctx context.Context) ([]TelegramLinkRow, error) {
	rows, err := s.R.QueryContext(ctx, telegramLinkSelect+`ORDER BY l.linked_at, l.admin_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelegramLinkRow
	for rows.Next() {
		l, err := scanTelegramLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// TelegramLink is one admin's chat; ErrNotFound when the admin has none.
func (s *Store) TelegramLink(ctx context.Context, adminID string) (TelegramLinkRow, error) {
	l, err := scanTelegramLink(s.R.QueryRowContext(ctx, telegramLinkSelect+`WHERE l.admin_id = ?`, adminID))
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramLinkRow{}, ErrNotFound
	}
	return l, err
}

// TelegramLinkByChat is the link of a chat; ErrNotFound when no admin uses it.
func (s *Store) TelegramLinkByChat(ctx context.Context, chatID int64) (TelegramLinkRow, error) {
	l, err := scanTelegramLink(s.R.QueryRowContext(ctx, telegramLinkSelect+`WHERE l.chat_id = ?`, chatID))
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramLinkRow{}, ErrNotFound
	}
	return l, err
}

// BindTelegramChat links the chat to the admin, alerts on. The admin's earlier chat is replaced; so is the earlier owner
// of this chat (one chat, one admin). ErrNotFound: no such admin.
func (s *Store) BindTelegramChat(ctx context.Context, adminID string, chatID int64, now time.Time) error {
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM telegram_link WHERE (chat_id = ? OR admin_id = ?)
			AND EXISTS (SELECT 1 FROM admin WHERE id = ?)`, Args: []any{chatID, adminID, adminID}},
		Stmt{Query: `INSERT INTO telegram_link (admin_id, chat_id, enabled, linked_at)
			SELECT ?, ?, 1, ? WHERE EXISTS (SELECT 1 FROM admin WHERE id = ?)`,
			Args: []any{adminID, chatID, unix(now), adminID}},
	)
	if err != nil {
		return err
	}
	if results[1].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UnlinkTelegram forgets the admin's chat; it reports whether there was one.
func (s *Store) UnlinkTelegram(ctx context.Context, adminID string) (bool, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM telegram_link WHERE admin_id = ?`, adminID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetTelegramEnabled switches the admin's alerts; ErrNotFound when the admin has no chat.
func (s *Store) SetTelegramEnabled(ctx context.Context, adminID string, enabled bool) error {
	res, err := s.W.ExecContext(ctx, `UPDATE telegram_link SET enabled = ? WHERE admin_id = ?`, enabled, adminID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
