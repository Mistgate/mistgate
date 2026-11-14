package store

import (
	"context"
	"encoding/json"
	"time"
)

// The way a relay passes traffic on (migration 00138, relay.proto "TWO WAYS TO PASS ON"). Nothing here decides a way: the panel's
// controller (and the owner) write them, the desired state and the admin API read them.
const (
	RelayModeAuto    = "auto"    // the owner's choice only: the panel picks dnat or reverse
	RelayModeDNAT    = "dnat"    // the entry dials the exit
	RelayModeReverse = "reverse" // the exit dials the entry
)

// ValidRelayMode: the owner's choice (Relay.Mode) is one of the three words.
func ValidRelayMode(mode string) bool {
	return mode == RelayModeAuto || mode == RelayModeDNAT || mode == RelayModeReverse
}

// Reverse says the relay stands in the reverse way now.
func (r Relay) Reverse() bool { return r.ActiveMode == RelayModeReverse }

// SetRelayActiveMode records the way a relay stands in now and why (relay.proto Relay.mode_reason). The change time moves only
// when the way changes, so a repeated call with the same way and another reason keeps the time. ErrNotFound when the relay is gone.
func (s *Store) SetRelayActiveMode(ctx context.Context, id, active, reason string, now time.Time) error {
	res, err := s.W.ExecContext(ctx, `UPDATE relay SET
		mode_changed_at = CASE WHEN active_mode <> ?1 THEN ?4 ELSE mode_changed_at END,
		active_mode = ?1, mode_reason = ?2, updated_at = ?4 WHERE id = ?3`, active, reason, id, unix(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RelayBridgeCheck is what the two agents of a pair last reported of the bridge of a relay: check_json.bridge. The panel merges
// the BridgeState of both sides into it (a bridge is connected when the exit holds a live connection).
type RelayBridgeCheck struct {
	Connected bool   `json:"connected,omitempty"`
	Conns     uint32 `json:"conns,omitempty"`
	RTTMS     uint32 `json:"rtt_ms,omitempty"`
	SinceUnix int64  `json:"since_unix,omitempty"`
	Error     string `json:"error,omitempty"`
	AtUnix    int64  `json:"at_unix,omitempty"` // when the panel last had a report
}

// relayModeCheck is the members of check_json the reverse way adds. Each has its own writer, like the other members, so one
// writer never overwrites another's.
type relayModeCheck struct {
	Bridge *RelayBridgeCheck `json:"bridge"`
	// A float: D1 binds every JS number as a real, so a count written there may come back as 2.0.
	DirectStreak float64 `json:"direct_streak"`
}

func (r Relay) modeCheck() relayModeCheck {
	var c relayModeCheck
	if json.Unmarshal([]byte(r.CheckJSON), &c) != nil {
		return relayModeCheck{}
	}
	return c
}

// Bridge is the last report of the bridge of the relay; the second result is false when there is none.
func (r Relay) Bridge() (RelayBridgeCheck, bool) {
	c := r.modeCheck()
	if c.Bridge == nil {
		return RelayBridgeCheck{}, false
	}
	return *c.Bridge, true
}

// DirectStreak is how many checks of the direct leg passed in a row while the relay stands in the reverse way (0 to 3).
func (r Relay) DirectStreak() uint32 { return uint32(max(0, min(r.modeCheck().DirectStreak, 3))) }

// SetRelayBridge stores the bridge report of a relay in check_json.bridge; nil removes it. ErrNotFound when the relay is gone.
func (s *Store) SetRelayBridge(ctx context.Context, id string, b *RelayBridgeCheck) error {
	if b == nil {
		return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_remove(COALESCE(check_json, '{}'), '$.bridge') WHERE id = ?`, id)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_set(COALESCE(check_json, '{}'), '$.bridge', json(?)) WHERE id = ?`, string(raw), id)
}

// SetRelayDirectStreak stores the count of good direct checks in check_json.direct_streak (0 removes it). ErrNotFound when the
// relay is gone.
func (s *Store) SetRelayDirectStreak(ctx context.Context, id string, streak uint32) error {
	if streak == 0 {
		return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_remove(COALESCE(check_json, '{}'), '$.direct_streak') WHERE id = ?`, id)
	}
	return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_set(COALESCE(check_json, '{}'), '$.direct_streak', CAST(? AS INTEGER)) WHERE id = ?`, int64(min(streak, 3)), id)
}

func (s *Store) relayCheckMember(ctx context.Context, query string, args ...any) error {
	res, err := s.W.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetNodeBridgePort sets the TCP port of the node's site that the bridge arrives at (0 = 443 on the node's own site). ErrNotFound
// when the node is gone or retired.
func (s *Store) SetNodeBridgePort(ctx context.Context, nodeID string, port uint16) error {
	res, err := s.W.ExecContext(ctx, `UPDATE node SET bridge_port = ? WHERE id = ? AND state <> 'retired'`, int64(port), nodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureNodeBridgePath gives the node the path of its bridge when it has none and returns the one it has: two callers racing for
// the first reverse relay of an entry end with the same path. ErrNotFound when the node is gone or retired.
func (s *Store) EnsureNodeBridgePath(ctx context.Context, nodeID, path string) (string, error) {
	if _, err := s.W.ExecContext(ctx, `UPDATE node SET bridge_path = ? WHERE id = ? AND bridge_path = '' AND state <> 'retired'`, path, nodeID); err != nil {
		return "", err
	}
	n, err := s.Node(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if n.State == "retired" {
		return "", ErrNotFound
	}
	return n.BridgePath, nil
}
