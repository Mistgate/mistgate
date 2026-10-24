package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/logscrub"
)

const (
	// NodeJournalKeepHours is the journal retention and the largest window the RPC may read.
	NodeJournalKeepHours  = 7 * 24
	maxNodeJournalRows    = 64
	maxNodeJournalPerHour = 500
)

// FleetJournal is one agent.proto JournalEntry after the fleet guard has bounded its fields.
type FleetJournal struct {
	Level           int32
	Source, Message string
	InboundID       string
	Attrs           map[string]string
	Count           uint32
	FirstAt, LastAt int64
}

// NodeJournalFilter selects saved node journal entries. Since and Until are inclusive hour-window bounds.
type NodeJournalFilter struct {
	NodeID, Source, InboundID, Query string
	Since, Until                     time.Time
	MinLevel                         int32
	Limit                            int
}

// NodeJournalRow is one saved error kind with its display names resolved at read time.
type NodeJournalRow struct {
	NodeID, NodeName, ProfileName string
	HourStart                     int64
	Level                         int32
	Source, Message, InboundID    string
	Attrs                         map[string]string
	Count                         uint32
	FirstAt, LastAt               int64
}

// nodeJournalStmt stores a batch in one statement. Each input is a positional JSON array so D1 binds only the JSON,
// node id and hour, regardless of how many entries the agent sent.
func nodeJournalStmt(nodeID string, hourStart int64, entries []FleetJournal, secrets []string) (Stmt, error) {
	rows := make([][]any, 0, min(len(entries), maxNodeJournalRows))
	for _, entry := range entries {
		if len(rows) == maxNodeJournalRows {
			break
		}
		if entry.Level != 2 && entry.Level != 3 {
			continue
		}
		message := logscrub.Clip(scrubJournalText(entry.Message, secrets), 256)
		source := logscrub.Clip(entry.Source, 32)
		inboundID := logscrub.Clip(entry.InboundID, 64)
		attrs := make(map[string]string, len(entry.Attrs))
		for key, value := range entry.Attrs {
			attrs[key] = logscrub.ClipRunes(scrubJournalText(value, secrets), 160)
		}
		attrsJSON, err := json.Marshal(attrs)
		if err != nil {
			return Stmt{}, err
		}
		keyMaterial := strconv.FormatInt(int64(entry.Level), 10) + "|" + source + "|" + message + "|" + inboundID + "|" + attrs["code"]
		sum := sha256.Sum256([]byte(keyMaterial))
		count := entry.Count
		if count == 0 {
			count = 1
		}
		firstAt, lastAt := entry.FirstAt, entry.LastAt
		if firstAt > lastAt {
			firstAt, lastAt = lastAt, firstAt
		}
		rows = append(rows, []any{hex.EncodeToString(sum[:])[:32], entry.Level, source, message, inboundID,
			string(attrsJSON), int64(count), firstAt, lastAt})
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		INSERT INTO node_journal (node_id, hour_start, key, level, source, message, inbound_id, attrs_json, count, first_at, last_at)
		SELECT ?2, ?3, json_extract(j.value, '$[0]'), CAST(json_extract(j.value, '$[1]') AS INTEGER),
			json_extract(j.value, '$[2]'), json_extract(j.value, '$[3]'), json_extract(j.value, '$[4]'),
			json_extract(j.value, '$[5]'), CAST(json_extract(j.value, '$[6]') AS INTEGER),
			CAST(json_extract(j.value, '$[7]') AS INTEGER), CAST(json_extract(j.value, '$[8]') AS INTEGER)
		FROM json_each(?1) AS j
		WHERE EXISTS (
			SELECT 1 FROM node_journal existing
			WHERE existing.node_id = ?2 AND existing.hour_start = ?3 AND existing.key = json_extract(j.value, '$[0]')
		) OR (
			SELECT COUNT(*) FROM node_journal existing WHERE existing.node_id = ?2 AND existing.hour_start = ?3
		) + (
			SELECT COUNT(DISTINCT json_extract(candidate.value, '$[0]'))
			FROM json_each(?1) AS candidate
			WHERE CAST(candidate.key AS INTEGER) <= CAST(j.key AS INTEGER)
				AND NOT EXISTS (
					SELECT 1 FROM node_journal existing
					WHERE existing.node_id = ?2 AND existing.hour_start = ?3 AND existing.key = json_extract(candidate.value, '$[0]')
				)
		) <= ` + strconv.Itoa(maxNodeJournalPerHour) + `
		ORDER BY CAST(j.key AS INTEGER)
		ON CONFLICT (node_id, hour_start, key) DO UPDATE SET
			count = count + excluded.count,
			first_at = MIN(first_at, excluded.first_at),
			last_at = MAX(last_at, excluded.last_at)`, Args: []any{string(encoded), nodeID, hourStart}}, nil
}

// minJournalSecret: a shorter secret (a hand-picked SNI label such as "www") would cut ordinary words out of every message.
// The generated ones are 16 (agent SNI label) and 24 (path prefixes) characters.
const minJournalSecret = 8

// scrubJournalText is the panel's second privacy pass. Path prefixes are secret too; matching is case insensitive because an
// SNI is a DNS name, and replacing a path prefix case insensitively is safe even though routing itself is case sensitive.
func scrubJournalText(text string, secrets []string) string {
	text = logscrub.Addrs(text)
	ordered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret = strings.Trim(secret, "/"); len(secret) >= minJournalSecret {
			ordered = append(ordered, secret)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	for _, secret := range ordered {
		text = replaceJournalSecret(text, secret)
	}
	return text
}

// replaceJournalSecret replaces every ASCII-case-insensitive occurrence. Only ASCII letters are folded, so the folded text has
// the same byte offsets as the original (strings.ToLower may change the length of non-ASCII text, which the node controls).
func replaceJournalSecret(text, secret string) string {
	needle := asciiLower(secret)
	for from := 0; ; {
		i := strings.Index(asciiLower(text[from:]), needle)
		if i < 0 {
			return text
		}
		i += from
		text = text[:i] + "<secret>" + text[i+len(secret):]
		from = i + len("<secret>")
	}
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// NodeJournalWindowHours applies the admin RPC's bounded window: 0 means 24 hours and the maximum is retention.
func NodeJournalWindowHours(requested uint32) int {
	switch {
	case requested == 0:
		return 24
	case requested > NodeJournalKeepHours:
		return NodeJournalKeepHours
	default:
		return int(requested)
	}
}

// NodeJournal reads the matching entries from the hourly buckets that touch [since, until]. Query is a case-insensitive
// substring of message or attrs_json; LIKE metacharacters are escaped so they stay literal.
func (s *Store) NodeJournal(ctx context.Context, filter NodeJournalFilter) ([]NodeJournalRow, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if filter.MinLevel < 2 {
		filter.MinLevel = 2
	}
	where := []string{"j.hour_start >= ?", "j.hour_start <= ?", "j.level >= ?"}
	args := []any{hourBucket(filter.Since), hourBucket(filter.Until), filter.MinLevel}
	if filter.NodeID != "" {
		where = append(where, "j.node_id = ?")
		args = append(args, filter.NodeID)
	} else {
		where = append(where, "n.state <> 'retired'")
	}
	if filter.Source != "" {
		where = append(where, "j.source = ?")
		args = append(args, filter.Source)
	}
	if filter.InboundID != "" {
		where = append(where, "j.inbound_id = ?")
		args = append(args, filter.InboundID)
	}
	if filter.Query != "" {
		q := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filter.Query)
		pattern := "%" + q + "%"
		where = append(where, `(lower(j.message) LIKE lower(?) ESCAPE '\' OR lower(j.attrs_json) LIKE lower(?) ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	args = append(args, limit)
	query := `SELECT j.node_id, n.name, j.hour_start, j.level, j.source, j.message, j.inbound_id,
		COALESCE(p.name, ''), j.attrs_json, j.count, j.first_at, j.last_at
		FROM node_journal j JOIN node n ON n.id = j.node_id
		LEFT JOIN inbound i ON i.id = j.inbound_id AND i.node_id = j.node_id
		LEFT JOIN profile p ON p.id = i.profile_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY j.last_at DESC, n.name COLLATE NOCASE ASC, j.node_id ASC, j.key ASC LIMIT ?`
	rows, err := s.R.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NodeJournalRow, 0, limit)
	for rows.Next() {
		var row NodeJournalRow
		var attrsJSON string
		var count int64
		if err := rows.Scan(&row.NodeID, &row.NodeName, &row.HourStart, &row.Level, &row.Source, &row.Message, &row.InboundID,
			&row.ProfileName, &attrsJSON, &count, &row.FirstAt, &row.LastAt); err != nil {
			return nil, err
		}
		row.Count = uint32(min(max(count, int64(0)), int64(^uint32(0))))
		row.Attrs = map[string]string{}
		if err := json.Unmarshal([]byte(attrsJSON), &row.Attrs); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func hourBucket(t time.Time) int64 { return t.UTC().Truncate(time.Hour).Unix() }

// PruneNodeJournal keeps the whole hour bucket that touches the seven-day cut.
func (s *Store) PruneNodeJournal(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM node_journal WHERE hour_start < ?`, hourBucket(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
