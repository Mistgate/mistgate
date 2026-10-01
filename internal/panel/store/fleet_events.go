package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// EventRow is one row of the event log. NodeName/UserName and ProfileName/Protocol (the profile of InboundID, "" once
// the inbound is gone) are filled by Events only.
type EventRow struct {
	ID                                                      int64
	Time                                                    time.Time
	Severity                                                int    // 1 info, 2 warning, 3 error
	Code, Source                                            string // source: agent | panel | admin
	NodeID, NodeName, UserID, UserName, DeviceID, InboundID string
	ProfileName, Protocol                                   string
	Params                                                  map[string]string
	SrcInstance                                             string // agent events: dedup
	SrcSeq                                                  uint64
}

// InsertEvent appends an event (for panel and admin events; other modules may use it too).
func (s *Store) InsertEvent(ctx context.Context, e EventRow) error {
	return insertEvent(ctx, s.W, e)
}

// EventFilter selects events; zero values mean "any".
type EventFilter struct {
	NodeID, UserID string
	MinSeverity    int
	BeforeID       int64 // events with id < BeforeID; 0 = newest
	Limit          int
	Match          *CodeMatch // nil = any code
}

// CodeMatch keeps the events whose code is one of Codes or starts with one of Prefixes, or whose severity is at least
// MinSeverity (0 = severity does not count); Not keeps the others instead.
type CodeMatch struct {
	Codes, Prefixes []string
	MinSeverity     int
	Not             bool
}

// sql is the WHERE fragment of the match and its arguments.
func (m *CodeMatch) sql() (string, []any) {
	var or []string
	var args []any
	if len(m.Codes) > 0 {
		or = append(or, `e.code IN (?`+repeatComma(len(m.Codes)-1)+`)`)
		for _, c := range m.Codes {
			args = append(args, c)
		}
	}
	for _, p := range m.Prefixes { // substr, not LIKE: "_" in a code is a wildcard there
		or = append(or, `substr(e.code, 1, ?) = ?`)
		args = append(args, len(p), p)
	}
	if m.MinSeverity > 0 {
		or = append(or, `e.severity >= ?`)
		args = append(args, m.MinSeverity)
	}
	if len(or) == 0 {
		or = []string{"0"}
	}
	q := `(` + strings.Join(or, ` OR `) + `)`
	if m.Not {
		q = `NOT ` + q
	}
	return ` AND ` + q, args
}

// Events returns events newest first, names resolved from the node/user rows (a missing row leaves the
// name empty). hasMore reports that more rows exist beyond Limit.
func (s *Store) Events(ctx context.Context, f EventFilter) (out []EventRow, hasMore bool, err error) {
	q := `SELECT e.id, e.ts, e.severity, e.code, e.source, coalesce(e.node_id, ''), coalesce(n.name, ''),
	             coalesce(e.user_id, ''), coalesce(u.name, ''), coalesce(e.device_id, ''), coalesce(e.inbound_id, ''), e.params_json,
	             coalesce(p.name, ''), coalesce(p.protocol, '')
	      FROM event e LEFT JOIN node n ON n.id = e.node_id LEFT JOIN user u ON u.id = e.user_id
	      LEFT JOIN inbound i ON i.id = e.inbound_id AND i.node_id = e.node_id LEFT JOIN profile p ON p.id = i.profile_id WHERE 1 = 1`
	var args []any
	if f.NodeID != "" {
		q += ` AND e.node_id = ?`
		args = append(args, f.NodeID)
	}
	if f.UserID != "" {
		q += ` AND e.user_id = ?`
		args = append(args, f.UserID)
	}
	if f.MinSeverity > 1 {
		q += ` AND e.severity >= ?`
		args = append(args, f.MinSeverity)
	}
	if f.BeforeID > 0 {
		q += ` AND e.id < ?`
		args = append(args, f.BeforeID)
	}
	if f.Match != nil {
		w, a := f.Match.sql()
		q += w
		args = append(args, a...)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := s.R.QueryContext(ctx, q+` ORDER BY e.id DESC LIMIT ?`, append(args, f.Limit+1)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var e EventRow
		var ts int64
		var params string
		if err := rows.Scan(&e.ID, &ts, &e.Severity, &e.Code, &e.Source, &e.NodeID, &e.NodeName, &e.UserID, &e.UserName,
			&e.DeviceID, &e.InboundID, &params, &e.ProfileName, &e.Protocol); err != nil {
			return nil, false, err
		}
		e.Time = fromUnix(ts)
		_ = json.Unmarshal([]byte(params), &e.Params)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > f.Limit {
		out, hasMore = out[:f.Limit], true
	}
	return out, hasMore, nil
}

// LastNodeEventCode returns the code of the newest event of the node among codes ("" if none). Used to emit
// node_down once per outage without extra state.
func (s *Store) LastNodeEventCode(ctx context.Context, nodeID string, codes ...string) (string, error) {
	if len(codes) == 0 {
		return "", nil
	}
	q := `SELECT code FROM event WHERE node_id = ? AND code IN (?` + repeatComma(len(codes)-1) + `) ORDER BY id DESC LIMIT 1`
	args := []any{nodeID}
	for _, c := range codes {
		args = append(args, c)
	}
	var code string
	err := s.R.QueryRowContext(ctx, q, args...).Scan(&code)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return code, err
}

func repeatComma(n int) string {
	s := ""
	for range n {
		s += ", ?"
	}
	return s
}

// FleetHourRow is one node_traffic_hour row.
type FleetHourRow struct {
	NodeID, Protocol string
	Hour             int64
	Up, Down         uint64
	PeakUsers        uint64
}

// FleetHours returns the hourly rollup since the given hour (Overview, node cards).
func (s *Store) FleetHours(ctx context.Context, sinceHour int64) ([]FleetHourRow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT node_id, protocol, hour_start, bytes_up, bytes_down, peak_users FROM node_traffic_hour WHERE hour_start >= ?`, sinceHour)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetHourRow
	for rows.Next() {
		var r FleetHourRow
		var up, down, peak int64
		if err := rows.Scan(&r.NodeID, &r.Protocol, &r.Hour, &up, &down, &peak); err != nil {
			return nil, err
		}
		r.Up, r.Down, r.PeakUsers = uint64(up), uint64(down), uint64(peak)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FleetTop is one user's bytes on a node.
type FleetTop struct {
	UserID, UserName string
	Bytes            uint64
}

// FleetTopUsers returns the top users of a node by bytes since the given hour ("top today").
func (s *Store) FleetTopUsers(ctx context.Context, nodeID string, sinceHour int64, limit int) ([]FleetTop, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT b.user_id, coalesce(u.name, ''), sum(b.bytes_up + b.bytes_down) AS total
		FROM traffic_bucket b LEFT JOIN user u ON u.id = b.user_id
		WHERE b.node_id = ? AND b.hour_start >= ? GROUP BY b.user_id ORDER BY total DESC LIMIT ?`, nodeID, sinceHour, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetTop
	for rows.Next() {
		var r FleetTop
		var n int64
		if err := rows.Scan(&r.UserID, &r.UserName, &n); err != nil {
			return nil, err
		}
		r.Bytes = uint64(n)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FleetNodeProtocols returns the distinct protocols of the enabled inbounds per node.
func (s *Store) FleetNodeProtocols(ctx context.Context) (map[string][]string, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT DISTINCT i.node_id, p.protocol FROM inbound i JOIN profile p ON p.id = i.profile_id
		WHERE i.enabled = 1 ORDER BY i.node_id, p.protocol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var n, p string
		if err := rows.Scan(&n, &p); err != nil {
			return nil, err
		}
		out[n] = append(out[n], p)
	}
	return out, rows.Err()
}
