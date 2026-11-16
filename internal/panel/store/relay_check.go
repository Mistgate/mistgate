package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// The latest check of a relay lives in relay.check_json (migration 00085); there is no history. The fleet module makes the
// check (fleet.CheckRelay), the access module reads it for the gate that lets a relay be switched on.

// The stages of a check, as RelayCheck.Stage names them.
const (
	RelayStageLeg          = "leg"           // the entry node to the exit's server
	RelayStagePath         = "path"          // a third node to the entry's forwarded port, counted at the exit
	RelayStageReverseReach = "reverse_reach" // the exit node to the entry's HTTPS site
	RelayStageReverseSpeed = "reverse_speed" // an explicit speed test from the exit node to the entry
)

// RelayStage is one stage of a check. Status is "ok", "fail", "skipped" or "" (did not run); Code says why a stage failed or
// was skipped (the words of relay.proto RelayStage.code); Sender is the node id that sent the probe, or "panel".
type RelayStage struct {
	Status      string  `json:"status,omitempty"`
	Code        string  `json:"code,omitempty"`
	Sender      string  `json:"sender,omitempty"`
	Verdict     string  `json:"verdict,omitempty"`
	Sent        uint32  `json:"sent,omitempty"`
	Got         uint32  `json:"got,omitempty"`
	At          int64   `json:"at,omitempty"`
	MB          uint32  `json:"mb,omitempty"`
	MbitS       float64 `json:"mbit_s,omitempty"`
	Bytes       uint64  `json:"bytes,omitempty"`
	ConnectMS   uint32  `json:"connect_ms,omitempty"`
	FirstByteMS uint32  `json:"first_byte_ms,omitempty"`
	DurationMS  uint32  `json:"duration_ms,omitempty"`
	// Port is the TCP port of the bridge endpoint a reverse_reach probe was made for (0 for every other stage).
	Port uint16 `json:"port,omitempty"`
}

// Unverifiable says the stage was skipped because it could not attribute a TLS identity to the server.
func (s RelayStage) Unverifiable() bool { return s.Status == "skipped" && s.Code == "unverifiable" }

// Acceptable says the stage either proved the server or could not check it without finding a failure.
func (s RelayStage) Acceptable() bool {
	return s.Status == "ok" || s.Unverifiable()
}

// RelayMonitorCheck is the result of the client-eye probe through an enabled relay. The two automatic-check fields are
// panel bookkeeping and are deliberately not part of admin.v1.RelayMonitor.
type RelayMonitorCheck struct {
	AtUnix             int64  `json:"at_unix"`
	OK                 bool   `json:"ok"`
	Stage              string `json:"stage,omitempty"`
	Code               string `json:"code,omitempty"`
	ResponseMS         uint32 `json:"response_ms,omitempty"`
	FailStreak         uint32 `json:"fail_streak,omitempty"`
	TargetKey          string `json:"target_key,omitempty"`
	AutoCheckAt        int64  `json:"auto_check_at,omitempty"`
	AutoCheckTargetKey string `json:"auto_check_target_key,omitempty"`
}

// RelayCounters is one snapshot of the named nft counter for a forward. Until the agent counts return traffic, its rate and
// day total describe only bytes sent toward the exit.
type RelayCounters struct {
	Bytes           uint64 `json:"bytes"`
	Packets         uint64 `json:"packets"`
	CountedUnix     int64  `json:"counted_unix"`
	PrevBytes       uint64 `json:"prev_bytes,omitempty"`
	PrevCountedUnix int64  `json:"prev_counted_unix,omitempty"`
	TodayBytes      uint64 `json:"today_bytes,omitempty"`
	TodayDay        int64  `json:"today_day,omitempty"`
}

const (
	relayCounterFreshWindow = 15 * time.Minute
	maxRelayCounterBPS      = uint64(1_000_000_000_000) // A jump above 1 Tb/s is not a useful rate sample.
)

// BPS returns the forward rate when both readings are recent and ordered. A lower current counter is a reset, whose new value
// is the increment; zero or stale intervals do not produce a rate.
func (c RelayCounters) BPS(now time.Time) uint64 {
	if c.PrevCountedUnix <= 0 || c.CountedUnix <= c.PrevCountedUnix || now.Unix() < c.CountedUnix || now.Unix() < c.PrevCountedUnix {
		return 0
	}
	interval := c.CountedUnix - c.PrevCountedUnix
	if interval > int64(relayCounterFreshWindow/time.Second) || now.Unix()-c.CountedUnix > int64(relayCounterFreshWindow/time.Second) ||
		now.Unix()-c.PrevCountedUnix > int64(relayCounterFreshWindow/time.Second) {
		return 0
	}
	delta := c.Bytes
	if c.Bytes >= c.PrevBytes {
		delta = c.Bytes - c.PrevBytes
	}
	maxBytes := maxRelayCounterBPS / 8 * uint64(interval)
	if delta > maxBytes {
		return 0
	}
	bps := delta * 8 / uint64(interval)
	if bps > maxRelayCounterBPS {
		return 0
	}
	return bps
}

// RelayCounterUpdate is one counter snapshot reported by the entry node.
type RelayCounterUpdate struct {
	RelayID string
	Network string
	Port    uint16
	RelayCounters
}

// RelayCheck is what the last check of a relay found. Port and To are what it was made for: a relay moved to another port or
// pointed at another address is not covered by it.
type RelayCheck struct {
	At           int64      `json:"at,omitempty"`
	Network      string     `json:"network,omitempty"`
	Port         uint16     `json:"port,omitempty"`
	To           string     `json:"to,omitempty"` // the exit's address:port
	Leg          RelayStage `json:"leg"`
	Path         RelayStage `json:"path"`
	ReverseReach RelayStage `json:"reverse_reach,omitzero"`
	ReverseSpeed RelayStage `json:"reverse_speed,omitzero"`
	// DirectSpeed is the panel's own 8 MB proof of the direct way before a reverse relay returns to DNAT.
	DirectSpeed RelayStage `json:"direct_speed,omitzero"`
	// Bridge is derived from the agents' reports (Relay.BridgeStage) by whoever shows or uses the check; it is never stored.
	Bridge   RelayStage         `json:"-"`
	OK       bool               `json:"ok"`              // the leg ran and no stage that ran failed
	Stage    string             `json:"stage,omitempty"` // the first direct stage that failed
	Monitor  *RelayMonitorCheck `json:"monitor,omitempty"`
	Counters *RelayCounters     `json:"counters,omitempty"`
}

// Settle recomputes OK and Stage from the two stages. An unverifiable leg is not a failure: its owner may choose to enable the
// relay, while Verified remains false until both stages actually prove the server.
func (c *RelayCheck) Settle() {
	c.Stage = ""
	switch {
	case c.Leg.Status == "fail":
		c.Stage = RelayStageLeg
	case c.Path.Status == "fail":
		c.Stage = RelayStagePath
	}
	c.OK = c.Leg.Acceptable() && c.Stage == ""
}

// Verified: both stages ran and passed, the relay works from outside.
func (c RelayCheck) Verified() bool { return c.Leg.Status == "ok" && c.Path.Status == "ok" }

// CanUse says both stages passed or were explicitly skipped because the server identity is unverifiable.
func (c RelayCheck) CanUse() bool { return c.Leg.Acceptable() && c.Path.Acceptable() }

// Check reads the relay's latest check; the second result is false when there is none.
func (r Relay) Check() (RelayCheck, bool) {
	var c RelayCheck
	if json.Unmarshal([]byte(r.CheckJSON), &c) != nil || (c.At == 0 && c.Monitor == nil && c.Counters == nil &&
		c.Leg.At == 0 && c.Path.At == 0 && c.ReverseReach.At == 0 && c.ReverseSpeed.At == 0 && c.DirectSpeed.At == 0) {
		return RelayCheck{}, false
	}
	return c, true
}

// Proven: the relay's latest check found both stages passing, so the path from outside through the entry to the exit was seen to
// work. A relay with an unverifiable stage can be usable while not proven.
func (l RelayLink) Proven() bool {
	c, ok := l.Check()
	return ok && c.Verified()
}

// Usable says every stage either passed or was unverifiable. The caller checks whether the owner enabled the relay. A relay in the
// reverse way stands on the bridge BECAUSE its direct leg fails (or freezes): the leg is then no part of what makes it usable, the
// path through the bridge is. Without this the relay is taken out of every subscription at the moment it begins to work.
func (l RelayLink) Usable() bool {
	c, ok := l.Check()
	if ok && l.Reverse() {
		return c.Path.Acceptable()
	}
	return ok && c.CanUse()
}

// SetRelayCheck stores the check of a relay. ErrNotFound when the relay is gone.
func (s *Store) SetRelayCheck(ctx context.Context, id string, c RelayCheck) error {
	// Each writer owns one member of check_json. Keep the independently updated client-eye result and traffic counters.
	c.Monitor = nil
	c.Counters = nil
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	// D1 can bind a number as REAL. Cast the root timestamp and port so json_set writes integers into the check document.
	res, err := s.W.ExecContext(ctx, `UPDATE relay SET check_json = json_patch(
		json_remove(COALESCE(check_json, '{}'), '$.at', '$.network', '$.port', '$.to', '$.leg', '$.path', '$.reverse_reach', '$.reverse_speed', '$.direct_speed', '$.ok', '$.stage'),
		json_set(json(?), '$.at', CAST(? AS INTEGER), '$.port', CAST(? AS INTEGER)))
		WHERE id = ?`, string(b), c.At, c.Port, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRelayMonitorCheck updates only check_json.monitor, leaving the leg/path check and counters intact.
func (s *Store) SetRelayMonitorCheck(ctx context.Context, id string, m RelayMonitorCheck) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	res, err := s.W.ExecContext(ctx, `UPDATE relay SET check_json = json_set(COALESCE(check_json, '{}'), '$.monitor', json(?)) WHERE id = ?`, string(b), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRelayCounters updates the counter member for relays owned by nodeID. Reports from an older or switched-off forward
// cannot update another entry's relay or make a disabled relay appear to be carrying current traffic.
func (s *Store) SetRelayCounters(ctx context.Context, nodeID string, updates []RelayCounterUpdate) error {
	stmts := make([]Stmt, 0, len(updates))
	for _, update := range updates {
		if update.RelayID == "" || update.CountedUnix <= 0 {
			continue
		}
		b, err := json.Marshal(struct {
			Bytes       uint64 `json:"bytes"`
			Packets     uint64 `json:"packets"`
			CountedUnix int64  `json:"counted_unix"`
		}{Bytes: update.Bytes, Packets: update.Packets, CountedUnix: update.CountedUnix})
		if err != nil {
			return err
		}
		// The day is cast in SQL: a bound number may reach D1 as a REAL, and the JSON member must stay an integer.
		day := time.Unix(update.CountedUnix, 0).UTC()
		dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC).Unix()
		stmts = append(stmts, Stmt{Query: `UPDATE relay SET check_json = json_set(COALESCE(check_json, '{}'), '$.counters',
			json_set(json(?1),
				'$.prev_bytes', json_extract(check_json, '$.counters.bytes'),
				'$.prev_counted_unix', json_extract(check_json, '$.counters.counted_unix'),
				'$.today_bytes', CASE
					WHEN json_extract(check_json, '$.counters.today_day') = ?2 THEN
						COALESCE(json_extract(check_json, '$.counters.today_bytes'), 0) + CASE
							WHEN json_type(check_json, '$.counters.counted_unix') IS NULL THEN json_extract(?1, '$.bytes')
							WHEN json_extract(?1, '$.bytes') < json_extract(check_json, '$.counters.bytes') THEN json_extract(?1, '$.bytes')
							ELSE json_extract(?1, '$.bytes') - json_extract(check_json, '$.counters.bytes') END
					ELSE CASE
						WHEN json_type(check_json, '$.counters.counted_unix') IS NULL THEN json_extract(?1, '$.bytes')
						WHEN json_extract(?1, '$.bytes') < json_extract(check_json, '$.counters.bytes') THEN json_extract(?1, '$.bytes')
						ELSE json_extract(?1, '$.bytes') - json_extract(check_json, '$.counters.bytes') END
				END,
				'$.today_day', CAST(?2 AS INTEGER)))
			WHERE id = ?3 AND via_node_id = ?4 AND network = ?5 AND port = ?6 AND enabled = 1
				AND (json_type(check_json, '$.counters.counted_unix') IS NULL OR
					json_extract(check_json, '$.counters.counted_unix') < json_extract(?1, '$.counted_unix'))`,
			Args: []any{string(b), dayStart, update.RelayID, nodeID, update.Network, update.Port}})
	}
	if len(stmts) == 0 {
		return nil
	}
	_, err := s.batch(ctx, stmts...)
	return err
}

// RelayTodayBytesByNode sums the persisted daily forward increments of each entry's relays for one UTC day boundary. A relay
// deleted during the day takes its share with it: the day total lives in the relay row.
func (s *Store) RelayTodayBytesByNode(ctx context.Context, dayStart int64) (map[string]uint64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT via_node_id, SUM(json_extract(check_json, '$.counters.today_bytes')) FROM relay
		WHERE json_extract(check_json, '$.counters.today_day') = ? GROUP BY via_node_id`, dayStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uint64{}
	for rows.Next() {
		var nodeID string
		var sum sql.NullInt64
		if err := rows.Scan(&nodeID, &sum); err != nil {
			return nil, err
		}
		if sum.Valid && sum.Int64 > 0 {
			out[nodeID] = uint64(sum.Int64)
		}
	}
	return out, rows.Err()
}
