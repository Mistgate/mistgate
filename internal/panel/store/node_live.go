package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// NodeLiveRow is one node and its live projection, if it has one.
type NodeLiveRow struct {
	NodeID     string
	State      string
	LastSeenAt time.Time
	AgentCaps  []string
	Exists     bool
	Session    int64
	Drift      bool
	SampleAt   int64
	RxBps      int64
	TxBps      int64
	CPUPct     int64
	UsersJSON  string
	LiveJSON   string
	Connected  bool
}

// FleetLive is one sanitized projection built from a StatsBatch and its resolved credentials.
type FleetLive struct {
	Apply     bool
	Drift     bool
	SampleAt  int64
	RxBps     uint64
	TxBps     uint64
	CPUPct    int64
	UsersJSON string
	LiveJSON  string
}

// NodeLive lists node live projections. An empty nodeID lists every node; view includes live_json.
func (s *Store) NodeLive(ctx context.Context, now time.Time, nodeID string, view bool) ([]NodeLiveRow, error) {
	columns := `n.id AS node_id, n.state AS node_state, n.last_seen_at AS last_seen_at,
		n.liveness_timeout_s AS liveness_timeout_s, n.agent_caps AS agent_caps,
		l.node_id AS live_node_id, l.session AS session, l.drift AS drift, l.sample_at AS sample_at,
		l.rx_bps AS rx_bps, l.tx_bps AS tx_bps, l.cpu_pct AS cpu_pct, l.users AS users`
	if view {
		columns += `, l.live_json AS live_json`
	}
	results, err := s.read(ctx, Stmt{Query: `SELECT ` + columns + `
		FROM node AS n LEFT JOIN node_live AS l ON l.node_id = n.id
		WHERE (? = '' OR n.id = ?) ORDER BY n.id`, Args: []any{nodeID, nodeID}, Returning: true})
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, errors.New("store: unexpected NodeLive read result count")
	}

	rows := make([]NodeLiveRow, 0, len(results[0].Rows))
	nowUnix := unix(now)
	for _, values := range results[0].Rows {
		var row NodeLiveRow
		var seenAt, timeout int64
		var caps sql.NullString
		var liveNodeID sql.NullString
		var session, drift, sampleAt, rx, tx, cpu sql.NullInt64
		var users sql.NullString
		var liveJSON sql.NullString
		dest := []any{&row.NodeID, &row.State, &seenAt, &timeout, &caps, &liveNodeID, &session, &drift, &sampleAt, &rx, &tx, &cpu, &users}
		if view {
			dest = append(dest, &liveJSON)
		}
		if err := batchRow(values).Scan(dest...); err != nil {
			return nil, err
		}
		row.LastSeenAt = fromUnix(seenAt)
		row.AgentCaps = strings.Fields(caps.String)
		row.Exists = liveNodeID.Valid
		row.Session = session.Int64
		row.Drift = drift.Int64 != 0
		row.SampleAt = sampleAt.Int64
		row.RxBps = rx.Int64
		row.TxBps = tx.Int64
		row.CPUPct = cpu.Int64
		row.UsersJSON = users.String
		row.LiveJSON = liveJSON.String
		row.Connected = row.Exists && row.State == "active" && seenAt >= nowUnix-int64(timeout)
		rows = append(rows, row)
	}
	return rows, nil
}

func nodeSessionValue(session uint64) (int64, error) {
	if session >= 1<<53 {
		return 0, errors.New("store: session id exceeds the D1 safe integer range")
	}
	return int64(session), nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
