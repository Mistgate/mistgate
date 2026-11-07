//go:build !js

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/logscrub"
)

// The rows behind debug_capture and debug_capture_line (migrations 00108 and 00109): on-demand captures of one node's debug log
// (agent.proto "DEBUG CAPTURE"). They exist for the OpsService of the VPS edition; the Cloudflare edition has no caller, so this
// file is not in the Workers' wasm.

const (
	// DebugCaptureKeep is how long a capture and its lines are kept.
	DebugCaptureKeep = 24 * time.Hour
	// DebugCapturesPerNode is how many captures of one node are kept: starting another drops the oldest beyond this.
	DebugCapturesPerNode = 5
	// DebugMaxLines and DebugMaxBytes bound one capture on intake, whatever the node sends.
	DebugMaxLines = 20000
	DebugMaxBytes = 4 << 20
	// DebugRunGrace is how long past its end a running capture still waits for the node's report; after that it reads as expired.
	DebugRunGrace = 20 * time.Minute

	debugMessageBytes = 1024
	debugAttrRunes    = 160
	debugSourceBytes  = 32
)

// ErrDebugBusy: a capture of this node is still running.
var ErrDebugBusy = errors.New("store: a debug capture of this node is running")

// DebugCaptureRow is one capture. NodeName is resolved on read (empty when the node is gone).
type DebugCaptureRow struct {
	ID, NodeID, NodeName, Actor, Level string
	Sources                            []string
	DurationS                          int
	StartedAt, EndsAt                  time.Time
	State, EndReason                   string // running | done | expired (read time); "stopped", "expired", "shutdown" once done
	Records, Bytes, Dropped            int
}

// Running reports whether the capture may still receive records at now.
func (r DebugCaptureRow) Running(now time.Time) bool {
	return r.State == "running" && now.Before(r.EndsAt.Add(DebugRunGrace))
}

// DebugLineIn is one record as the node sent it. Level is agent.proto Severity (4 debug, 1 info, 2 warning, 3 error).
type DebugLineIn struct {
	AtMs            int64
	Level           int32
	Source, Message string
	Attrs           map[string]string
}

// DebugLineRow is one stored record. Level: 0 debug, 1 info, 2 warn, 3 error.
type DebugLineRow struct {
	Seq, AtMs       int64
	Level           int
	Source, Message string
	Attrs           map[string]string
}

// DebugLineFilter selects the lines of one capture. A text search is the caller's: it must run on the cleaned text of the answer.
type DebugLineFilter struct {
	MinLevel int
	Source   string
	AfterSeq int64
	Limit    int
}

// BeginDebugCapture records a capture that the node is about to run. A running capture of the same node is ErrDebugBusy; the
// oldest captures beyond DebugCapturesPerNode (and their lines) are dropped in the same batch.
func (s *Store) BeginDebugCapture(ctx context.Context, row DebugCaptureRow, now time.Time) error {
	var running int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM debug_capture WHERE node_id = ? AND state = 'running' AND ends_at + ? > ?`,
		row.NodeID, int64(DebugRunGrace/time.Second), unix(now)).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return ErrDebugBusy
	}
	keep := DebugCapturesPerNode - 1
	oldest := `SELECT id FROM debug_capture WHERE node_id = ? AND id NOT IN
		(SELECT id FROM debug_capture WHERE node_id = ? ORDER BY started_at DESC, id DESC LIMIT ?)`
	_, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM debug_capture_line WHERE capture_id IN (` + oldest + `)`, Args: []any{row.NodeID, row.NodeID, keep}},
		Stmt{Query: `DELETE FROM debug_capture WHERE id IN (` + oldest + `)`, Args: []any{row.NodeID, row.NodeID, keep}},
		Stmt{Query: `INSERT INTO debug_capture (id, node_id, actor, level, sources, duration_s, started_at, ends_at, state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'running')`,
			Args: []any{row.ID, row.NodeID, row.Actor, row.Level, strings.Join(row.Sources, ","), row.DurationS, unix(row.StartedAt), unix(row.EndsAt)}})
	return err
}

// DropDebugCapture removes a capture the node did not accept (offline, too old, busy, refused): nothing ran, so nothing is kept and
// the per-node slots are not used up. A capture that has received lines stays.
func (s *Store) DropDebugCapture(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM debug_capture WHERE id = ? AND state = 'running' AND records = 0`, id)
	return err
}

// debugLevel maps agent.proto Severity to the stored level.
func debugLevel(sev int32) int {
	switch sev {
	case 4:
		return 0
	case 2:
		return 2
	case 3:
		return 3
	default:
		return 1
	}
}

// AppendDebugLines stores a chunk of a running capture of this node, after the panel's own scrub: addresses, the secret names
// (the journal's secrets), names in xray's text, attribute keys of the journal's allow-list only, every text cut. Lines beyond
// DebugMaxLines or DebugMaxBytes are counted as dropped, not stored. A capture that is not running (unknown, other node, already
// done) takes nothing: the second return value is false.
func (s *Store) AppendDebugLines(ctx context.Context, nodeID, id string, lines []DebugLineIn, secrets []string, now time.Time) (stored int, ok bool, err error) {
	row, err := s.DebugCapture(ctx, id)
	if errors.Is(err, ErrNotFound) || err == nil && (row.NodeID != nodeID || !row.Running(now)) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	seq, size, dropped := int64(row.Records), row.Bytes, 0
	var stmts []Stmt
	add := 0
	for _, l := range lines {
		source := logscrub.Clip(scrubJournalText(l.Source, secrets), debugSourceBytes)
		if !validDebugSource(source) {
			source = "agent"
		}
		clean := func(text string) string {
			text = scrubJournalText(text, secrets)
			if source == "xray" { // its text may name a destination
				text = logscrub.Hosts(text)
			}
			return text
		}
		msg := logscrub.Clip(clean(logscrub.Clip(l.Message, debugMessageBytes)), debugMessageBytes)
		attrs := map[string]string{}
		for _, key := range logscrub.JournalAttrs {
			if v, has := l.Attrs[key]; has {
				attrs[key] = logscrub.ClipRunes(clean(logscrub.ClipRunes(v, 640)), debugAttrRunes)
			}
		}
		attrsJSON, err := plainJSON(attrs)
		if err != nil {
			return 0, false, err
		}
		cost := len(msg) + len(source) + len(attrsJSON) + 48
		if seq >= DebugMaxLines || size+cost > DebugMaxBytes {
			dropped++
			continue
		}
		seq++
		size += cost
		add++
		stmts = append(stmts, Stmt{Query: `INSERT INTO debug_capture_line (capture_id, seq, at_ms, level, source, message, attrs_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, Args: []any{id, seq, l.AtMs, debugLevel(l.Level), source, msg, attrsJSON}})
	}
	stmts = append(stmts, Stmt{Query: `UPDATE debug_capture SET records = ?, bytes = ?, dropped = dropped + ? WHERE id = ?`,
		Args: []any{seq, size, dropped, id}})
	if _, err := s.batch(ctx, stmts...); err != nil {
		return 0, false, err
	}
	return add, true, nil
}

func validDebugSource(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// FinishDebugCapture ends a running capture of this node: done, with how it ended and what the node could not keep.
func (s *Store) FinishDebugCapture(ctx context.Context, nodeID, id, reason string, nodeDropped uint32) error {
	switch reason {
	case "expired", "stopped", "shutdown":
	default:
		reason = "expired"
	}
	_, err := s.W.ExecContext(ctx, `UPDATE debug_capture SET state = 'done', end_reason = ?, dropped = dropped + ?
		WHERE id = ? AND node_id = ? AND state = 'running'`, reason, int64(nodeDropped), id, nodeID)
	return err
}

const debugColumns = `c.id, c.node_id, COALESCE(n.name, ''), c.actor, c.level, c.sources, c.duration_s, c.started_at, c.ends_at,
	c.state, c.end_reason, c.records, c.bytes, c.dropped`

func scanDebugCapture(r rowScanner, now time.Time) (DebugCaptureRow, error) {
	var c DebugCaptureRow
	var sources string
	var started, ends int64
	if err := r.Scan(&c.ID, &c.NodeID, &c.NodeName, &c.Actor, &c.Level, &sources, &c.DurationS, &started, &ends,
		&c.State, &c.EndReason, &c.Records, &c.Bytes, &c.Dropped); err != nil {
		return c, err
	}
	if sources != "" {
		c.Sources = strings.Split(sources, ",")
	}
	c.StartedAt, c.EndsAt = fromUnix(started), fromUnix(ends)
	if c.State == "running" && !now.IsZero() && !now.Before(c.EndsAt.Add(DebugRunGrace)) {
		c.State, c.EndReason = "expired", "no_report"
	}
	return c, nil
}

// DebugCapture returns one capture, or ErrNotFound. A running capture whose report never came reads as expired at the real time.
func (s *Store) DebugCapture(ctx context.Context, id string) (DebugCaptureRow, error) {
	row, err := scanDebugCapture(s.R.QueryRowContext(ctx, `SELECT `+debugColumns+` FROM debug_capture c LEFT JOIN node n ON n.id = c.node_id
		WHERE c.id = ?`, id), time.Time{})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// LatestDebugCapture is the newest capture of a node, or ErrNotFound.
func (s *Store) LatestDebugCapture(ctx context.Context, nodeID string, now time.Time) (DebugCaptureRow, error) {
	row, err := scanDebugCapture(s.R.QueryRowContext(ctx, `SELECT `+debugColumns+` FROM debug_capture c LEFT JOIN node n ON n.id = c.node_id
		WHERE c.node_id = ? ORDER BY c.started_at DESC, c.id DESC LIMIT 1`, nodeID), now)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// DebugCaptures lists the captures of a node (every node when empty), newest first.
func (s *Store) DebugCaptures(ctx context.Context, nodeID string, now time.Time) ([]DebugCaptureRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+debugColumns+` FROM debug_capture c LEFT JOIN node n ON n.id = c.node_id
		WHERE (? = '' OR c.node_id = ?) ORDER BY c.started_at DESC, c.id DESC LIMIT 100`, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DebugCaptureRow
	for rows.Next() {
		c, err := scanDebugCapture(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DebugLines returns the lines of a capture, oldest first after AfterSeq, with the filter applied, and the sources the capture
// holds.
func (s *Store) DebugLines(ctx context.Context, id string, f DebugLineFilter) ([]DebugLineRow, []string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	where := []string{"capture_id = ?", "seq > ?", "level >= ?"}
	args := []any{id, f.AfterSeq, f.MinLevel}
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	args = append(args, limit+1) // one more tells whether there is a next page
	rows, err := s.R.QueryContext(ctx, `SELECT seq, at_ms, level, source, message, attrs_json FROM debug_capture_line
		WHERE `+strings.Join(where, " AND ")+` ORDER BY seq ASC LIMIT ?`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []DebugLineRow
	for rows.Next() {
		var l DebugLineRow
		var attrsJSON string
		if err := rows.Scan(&l.Seq, &l.AtMs, &l.Level, &l.Source, &l.Message, &attrsJSON); err != nil {
			return nil, nil, err
		}
		l.Attrs = map[string]string{}
		if err := json.Unmarshal([]byte(attrsJSON), &l.Attrs); err != nil {
			return nil, nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	srcRows, err := s.R.QueryContext(ctx, `SELECT DISTINCT source FROM debug_capture_line WHERE capture_id = ? ORDER BY source`, id)
	if err != nil {
		return nil, nil, err
	}
	defer srcRows.Close()
	var sources []string
	for srcRows.Next() {
		var src string
		if err := srcRows.Scan(&src); err != nil {
			return nil, nil, err
		}
		sources = append(sources, src)
	}
	return out, sources, srcRows.Err()
}

// PruneDebugCaptures deletes the captures that started before the cut, and their lines.
func (s *Store) PruneDebugCaptures(ctx context.Context, before time.Time) (int64, error) {
	cut := unix(before)
	res, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM debug_capture_line WHERE capture_id IN (SELECT id FROM debug_capture WHERE started_at < ?)`, Args: []any{cut}},
		Stmt{Query: `DELETE FROM debug_capture WHERE started_at < ?`, Args: []any{cut}})
	if err != nil {
		return 0, err
	}
	if len(res) != 2 {
		return 0, nil
	}
	return res[1].RowsAffected, nil
}
