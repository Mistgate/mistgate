// Package store owns the panel's database, embedded migrations, and the plain-SQL
// queries the other modules use.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	// ErrNotFound is returned when a looked-up row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrSetupClosed is returned when the first admin cannot be created: the token is
	// unknown, used or expired, or an admin already exists.
	ErrSetupClosed = errors.New("store: setup closed")
)

// Store wraps the writer and read pools. The VPS backend uses one writer connection
// and a read pool; the D1 backend uses the same binding for both pools.
type Store struct {
	W, R     *sql.DB
	awgRetry sync.Mutex // Serializes local retries after concurrent AWG batches fail their guards.
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.R.Close(), s.W.Close())
}

// NewID returns prefix + 128 random bits as lowercase base32 (e.g. "adm_…").
func NewID(prefix string) string {
	var b [16]byte
	rand.Read(b[:]) // never fails on supported platforms
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

func unix(t time.Time) int64 { return t.UTC().Unix() }

func fromUnix(s int64) time.Time { return time.Unix(s, 0).UTC() }

// AuditEntry is one audit-log row.
type AuditEntry struct {
	Actor  string // admin id, or "anonymous"
	Action string
	Params string // JSON object without secrets; "" means {}
	Result string
	Source string // AuditPanel (default), AuditBot, AuditMCP or AuditAPI
	IP     string // client address the action came from, "" if none
}

// Audit sources, the values of audit.source.
const (
	AuditPanel = "panel"
	AuditBot   = "bot"
	AuditMCP   = "mcp"
	AuditAPI   = "api"
)

type auditSourceKey struct{}

// WithAuditSource returns ctx marking every audit row written with it, whose entry names no source, as coming
// from source (AuditAPI or AuditMCP: a call made with an API token). Modules pass the request context to Audit,
// so their rows carry the source of the call without knowing about tokens. An unknown source is ignored.
func WithAuditSource(ctx context.Context, source string) context.Context {
	switch source {
	case AuditPanel, AuditBot, AuditMCP, AuditAPI:
		return context.WithValue(ctx, auditSourceKey{}, source)
	}
	return ctx
}

// Audit appends a row to the audit log. An entry without a Source takes the one of WithAuditSource, else AuditPanel.
func (s *Store) Audit(ctx context.Context, now time.Time, e AuditEntry) error {
	if e.Params == "" {
		e.Params = "{}"
	}
	if e.Source == "" {
		e.Source, _ = ctx.Value(auditSourceKey{}).(string)
	}
	if e.Source == "" {
		e.Source = AuditPanel
	}
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO audit (ts, actor, action, params, result, source, ip) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		unix(now), e.Actor, e.Action, e.Params, e.Result, e.Source, e.IP)
	return err
}

// AuditRow is an audit entry as listed in the UI.
type AuditRow struct {
	ID        int64
	Time      time.Time
	Actor     string
	ActorName string // the admin's display name, or the token's name for "token:<id>" / "mcp:<id>"; "" if neither exists
	Action    string
	Params    string
	Result    string
	Source    string
	IP        string
}

// ListAudit returns up to limit rows, newest first, with id < beforeID (0 = from the
// newest), optionally only those of one source ("" = all).
func (s *Store) ListAudit(ctx context.Context, source string, beforeID int64, limit int) ([]AuditRow, error) {
	return s.ListAuditQuery(ctx, AuditQuery{Source: source, BeforeID: beforeID, Limit: limit})
}

// AuditQuery is one page of the audit log: newest first, id < BeforeID (0 = from the newest), at most Limit rows.
// Source keeps one source ("" = all). Actions and Prefixes keep the rows whose action is one of Actions or starts with
// one of Prefixes (both empty = every action). Failed keeps the rows whose result is not "ok".
type AuditQuery struct {
	Source            string
	BeforeID          int64
	Limit             int
	Actions, Prefixes []string
	Failed            bool
}

// ListAuditQuery returns the rows of q.
func (s *Store) ListAuditQuery(ctx context.Context, q AuditQuery) ([]AuditRow, error) {
	where := `(? = '' OR a.source = ?) AND (? = 0 OR a.id < ?)`
	args := []any{q.Source, q.Source, q.BeforeID, q.BeforeID}
	if len(q.Actions)+len(q.Prefixes) > 0 {
		var or []string
		if len(q.Actions) > 0 {
			or = append(or, `a.action IN (?`+strings.Repeat(`, ?`, len(q.Actions)-1)+`)`)
			for _, x := range q.Actions {
				args = append(args, x)
			}
		}
		for _, p := range q.Prefixes {
			or = append(or, `a.action LIKE ? ESCAPE '\'`)
			args = append(args, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p)+"%")
		}
		where += ` AND (` + strings.Join(or, " OR ") + `)`
	}
	if q.Failed {
		where += ` AND a.result NOT IN ('', 'ok')`
	}
	rows, err := s.R.QueryContext(ctx, `
		SELECT a.id, a.ts, a.actor, COALESCE(d.display_name, k.name, ''), a.action, a.params, a.result, a.source, a.ip
		FROM audit a LEFT JOIN admin d ON d.id = a.actor
		LEFT JOIN api_token k ON (a.actor LIKE 'token:%' OR a.actor LIKE 'mcp:%') AND k.id = substr(a.actor, instr(a.actor, ':') + 1)
		WHERE `+where+`
		ORDER BY a.id DESC LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var ts int64
		if err := rows.Scan(&r.ID, &ts, &r.Actor, &r.ActorName, &r.Action, &r.Params, &r.Result, &r.Source, &r.IP); err != nil {
			return nil, err
		}
		r.Time = fromUnix(ts)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Setting returns a setting value, or ErrNotFound.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.R.QueryRowContext(ctx, `SELECT v FROM setting WHERE k = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SettingValues returns the stored values among keys. Missing settings are omitted.
func (s *Store) SettingValues(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.R.QueryContext(ctx,
		`SELECT k, v FROM setting WHERE k IN (SELECT value FROM json_each(?))`, accJSON(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

// SetSettings upserts several settings in one transaction.
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	stmts := make([]Stmt, 0, len(kv))
	for k, v := range kv {
		stmts = append(stmts, Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, Args: []any{k, v}})
	}
	_, err := s.batch(ctx, stmts...)
	return err
}
