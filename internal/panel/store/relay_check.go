package store

import (
	"context"
	"encoding/json"
)

// The latest check of a relay lives in relay.check_json (migration 00085); there is no history. The fleet module makes the
// check (fleet.CheckRelay), the access module reads it for the gate that lets a relay be switched on.

// The stages of a check, as RelayCheck.Stage names them.
const (
	RelayStageLeg  = "leg"  // the entry node to the exit's server
	RelayStagePath = "path" // a third node to the entry's forwarded port, counted at the exit
)

// RelayStage is one stage of a check. Status is "ok", "fail", "skipped" or "" (did not run); Code says why a stage failed or
// was skipped (the words of relay.proto RelayStage.code); Sender is the node id that sent the probe, or "panel".
type RelayStage struct {
	Status  string `json:"status,omitempty"`
	Code    string `json:"code,omitempty"`
	Sender  string `json:"sender,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Sent    uint32 `json:"sent,omitempty"`
	Got     uint32 `json:"got,omitempty"`
	At      int64  `json:"at,omitempty"`
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

// RelayCounters is one snapshot of the named nft counter for a forward.
type RelayCounters struct {
	Bytes       uint64 `json:"bytes"`
	Packets     uint64 `json:"packets"`
	CountedUnix int64  `json:"counted_unix"`
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
	At       int64              `json:"at,omitempty"`
	Network  string             `json:"network,omitempty"`
	Port     uint16             `json:"port,omitempty"`
	To       string             `json:"to,omitempty"` // the exit's address:port
	Leg      RelayStage         `json:"leg"`
	Path     RelayStage         `json:"path"`
	OK       bool               `json:"ok"`              // the leg ran and no stage that ran failed
	Stage    string             `json:"stage,omitempty"` // the first stage that failed
	Monitor  *RelayMonitorCheck `json:"monitor,omitempty"`
	Counters *RelayCounters     `json:"counters,omitempty"`
}

// Settle recomputes OK and Stage from the two stages.
func (c *RelayCheck) Settle() {
	c.Stage = ""
	switch {
	case c.Leg.Status == "fail":
		c.Stage = RelayStageLeg
	case c.Path.Status == "fail":
		c.Stage = RelayStagePath
	}
	c.OK = c.Leg.Status != "" && c.Leg.Status != "skipped" && c.Stage == ""
}

// Verified: both stages ran and passed, the relay works from outside.
func (c RelayCheck) Verified() bool { return c.Leg.Status == "ok" && c.Path.Status == "ok" }

// Check reads the relay's latest check; the second result is false when there is none.
func (r Relay) Check() (RelayCheck, bool) {
	var c RelayCheck
	if json.Unmarshal([]byte(r.CheckJSON), &c) != nil || (c.At == 0 && c.Monitor == nil && c.Counters == nil) {
		return RelayCheck{}, false
	}
	return c, true
}

// Proven: the relay's latest check found both stages passing, so the path from outside through the entry to the exit was seen to
// work. The panel clears the path when it switches a relay on or moves it (access.UpdateRelay) and writes it again only when the
// path check passes, so a relay that is on but not proven yet (waiting for its entry, or failing) is not offered to people.
func (l RelayLink) Proven() bool {
	c, ok := l.Check()
	return ok && c.Verified()
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
	res, err := s.W.ExecContext(ctx, `UPDATE relay SET check_json = json_patch(
		json_remove(COALESCE(check_json, '{}'), '$.at', '$.network', '$.port', '$.to', '$.leg', '$.path', '$.ok', '$.stage'), ?)
		WHERE id = ?`, string(b), id)
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
		if update.RelayID == "" {
			continue
		}
		b, err := json.Marshal(update.RelayCounters)
		if err != nil {
			return err
		}
		stmts = append(stmts, Stmt{Query: `UPDATE relay SET check_json = json_set(COALESCE(check_json, '{}'), '$.counters', json(?))
			WHERE id = ? AND via_node_id = ? AND network = ? AND port = ? AND enabled = 1`,
			Args: []any{string(b), update.RelayID, nodeID, update.Network, update.Port}})
	}
	if len(stmts) == 0 {
		return nil
	}
	_, err := s.batch(ctx, stmts...)
	return err
}
