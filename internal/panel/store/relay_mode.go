package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// The way a relay passes traffic on (migration 00141, relay.proto "TWO WAYS TO PASS ON"). Nothing here decides a way: the panel's
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

// BridgeEndpointPort is the TCP port the exits call on this node as an entry: its separate bridge site, or 443 on its own site.
func (n NodeRow) BridgeEndpointPort() uint16 {
	if n.BridgePort != 0 {
		return n.BridgePort
	}
	return 443
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
	Connected bool                  `json:"connected,omitempty"`
	Conns     uint32                `json:"conns,omitempty"`
	RTTMS     uint32                `json:"rtt_ms,omitempty"`
	SinceUnix int64                 `json:"since_unix,omitempty"`
	Error     string                `json:"error,omitempty"`
	AtUnix    int64                 `json:"at_unix,omitempty"` // when the panel last had a report
	Entry     *RelayBridgeSideCheck `json:"entry,omitempty"`
	Exit      *RelayBridgeSideCheck `json:"exit,omitempty"`
	// Pool is the TLS connections the panel asks the exit to hold for this relay (2 or 4; 0 = not decided yet, which is 2), and
	// PoolAt the last time the reason for 4 held. See BridgePool.
	Pool   uint32 `json:"pool,omitempty"`
	PoolAt int64  `json:"pool_at,omitempty"`
}

// RelayBridgeSideCheck is the state one of the two agents most recently reported for this relay.
type RelayBridgeSideCheck struct {
	Connected bool   `json:"connected,omitempty"`
	Conns     uint32 `json:"conns,omitempty"`
	RTTMS     uint32 `json:"rtt_ms,omitempty"`
	SinceUnix int64  `json:"since_unix,omitempty"`
	Error     string `json:"error,omitempty"`
	AtUnix    int64  `json:"at_unix,omitempty"`
	// Clients is the people the entry carries through this bridge right now (BridgeState.clients_now; the entry's report only).
	// One bridge serves every relay of the pair, so each relay of it shows the same figure.
	Clients uint32 `json:"clients,omitempty"`
	// SitePin and SitePort are what the entry's agent reports of the bridge site it runs itself (BridgeState.site_cert_pin_sha256
	// and the port it was reported for): the pin the exits verify it by. The entry's report only; empty for a bridge on the site
	// of an inbound. Not a secret (a hash of a certificate every visitor of the site is shown), and kept here, with the report it
	// came with, because the panel process that builds the exit's desired state is not the one that heard the entry.
	SitePin  string `json:"site_pin,omitempty"`
	SitePort uint16 `json:"site_port,omitempty"`
}

// bridgePinTTL is how old the entry's report of its site pin may be and still be trusted: the pin of a site changes only when its
// certificate is renewed, and the next report says so.
const bridgePinTTL = 15 * time.Minute

// BridgeSitePin is the pin of the entry's own bridge site on this port, "" when the entry has not reported one for this port (or
// not lately). It is the one source the exits' BridgeDial.pin_sha256 and the reverse_reach probe use for a separate bridge site.
func (r Relay) BridgeSitePin(port uint16, now time.Time) string {
	b, ok := r.Bridge()
	if !ok || b.Entry == nil || b.Entry.SitePin == "" || b.Entry.SitePort != port || port == 0 ||
		b.Entry.AtUnix <= 0 || now.Unix() < b.Entry.AtUnix || now.Sub(time.Unix(b.Entry.AtUnix, 0)) > bridgePinTTL {
		return ""
	}
	return b.Entry.SitePin
}

// relayModeCheck is the members of check_json the reverse way adds. Each has its own writer, like the other members, so one
// writer never overwrites another's.
type relayModeCheck struct {
	Bridge *RelayBridgeCheck `json:"bridge"`
	// A float: D1 binds every JS number as a real, so a count written there may come back as 2.0.
	DirectStreak   float64 `json:"direct_streak"`
	DirectStreakAt float64 `json:"direct_streak_at"`
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
	b := *c.Bridge
	if b.Entry == nil && b.Exit == nil && !b.Connected && b.Conns == 0 && b.RTTMS == 0 && b.SinceUnix == 0 && b.Error == "" && b.AtUnix == 0 && b.Pool == 0 {
		return RelayBridgeCheck{}, false
	}
	if b.Exit != nil {
		b.Connected, b.Conns, b.RTTMS = b.Exit.Connected, b.Exit.Conns, b.Exit.RTTMS
		b.SinceUnix, b.Error, b.AtUnix = b.Exit.SinceUnix, b.Exit.Error, b.Exit.AtUnix
	}
	return b, true
}

// DirectStreak is how many checks of the direct leg passed in a row while the relay stands in the reverse way (0 to 3).
func (r Relay) DirectStreak() uint32 { return uint32(max(0, min(r.modeCheck().DirectStreak, 3))) }

// DirectStreakAt is the last passing direct check counted toward returning to DNAT.
func (r Relay) DirectStreakAt() int64 { return int64(max(0, r.modeCheck().DirectStreakAt)) }

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

// SetRelayBridgeSide updates one agent's report without replacing the other side or the bridge check stage.
func (s *Store) SetRelayBridgeSide(ctx context.Context, id, side string, report *RelayBridgeSideCheck) error {
	if side != "entry" && side != "exit" {
		return ErrNotFound
	}
	base := `json_set(COALESCE(check_json, '{}'), '$.bridge', json(COALESCE(json_extract(check_json, '$.bridge'), '{}')))`
	path := `$.bridge.` + side
	if report == nil {
		if side == "exit" {
			return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_set(json_remove(`+base+`, '`+path+`'),
				'$.bridge.connected', false, '$.bridge.conns', 0, '$.bridge.rtt_ms', 0, '$.bridge.since_unix', 0, '$.bridge.error', '', '$.bridge.at_unix', 0)
				WHERE id = ?`, id)
		}
		return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_remove(`+base+`, '`+path+`') WHERE id = ?`, id)
	} else {
		raw, err := json.Marshal(report)
		if err != nil {
			return err
		}
		query := `UPDATE relay SET check_json = json_set(` + base + `, '` + path + `', json(?)`
		args := []any{string(raw)}
		if side == "exit" {
			connected := "false"
			if report.Connected {
				connected = "true"
			}
			query += `, '$.bridge.connected', json(?), '$.bridge.conns', CAST(? AS INTEGER), '$.bridge.rtt_ms', CAST(? AS INTEGER), '$.bridge.since_unix', CAST(? AS INTEGER), '$.bridge.error', ?, '$.bridge.at_unix', CAST(? AS INTEGER)`
			args = append(args, connected, report.Conns, report.RTTMS, report.SinceUnix, report.Error, report.AtUnix)
		}
		query += `) WHERE id = ?`
		args = append(args, id)
		return s.relayCheckMember(ctx, query, args...)
	}
}

// BridgeStage is the bridge stage of a check, derived from what the exit's agent reported (the entry's report says who is on the
// other side, not whether the bridge works for the exit): "ok" when it holds a connection and has for 10 seconds, "skipped" while
// the connection is younger, "fail" with the agent's word as the code, "disconnected" without a connection and "stale" when the
// last report is older than 90 seconds or none came. The time of an ok or skipped stage is the report's, so a caller that asks
// whether it is fresh asks whether the agent is still talking. A fail that comes from silence is stamped with now: the silence is
// the finding.
func (r RelayLink) BridgeStage(now time.Time) RelayStage {
	stage := RelayStage{Status: "fail", Code: "disconnected", Sender: r.ExitNodeID, At: now.Unix()}
	b, ok := r.Bridge()
	if !ok || b.Exit == nil {
		return stage
	}
	report := b.Exit
	switch {
	case report.AtUnix <= 0 || now.Unix() < report.AtUnix || now.Sub(time.Unix(report.AtUnix, 0)) > relayBridgeReportFresh:
		stage.Code = "stale"
	case report.Error != "":
		stage.Code, stage.At = report.Error, report.AtUnix
	case !report.Connected || report.Conns == 0:
		stage.At = report.AtUnix
	case report.SinceUnix <= 0 || now.Unix()-report.SinceUnix < int64(relayBridgeStable/time.Second):
		stage.Status, stage.Code, stage.At = "skipped", "warming", report.AtUnix
	default:
		stage.Status, stage.Code, stage.At = "ok", "", report.AtUnix
	}
	return stage
}

// SetRelayBridgePool stores the pool the panel asks of the exit for this relay (check_json.bridge.pool) and when the reason for it
// last held. D1 binds a number as REAL, so both are cast back to integers in SQL. ErrNotFound when the relay is gone.
func (s *Store) SetRelayBridgePool(ctx context.Context, id string, pool uint32, at int64) error {
	return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_set(
		json_set(COALESCE(check_json, '{}'), '$.bridge', json(COALESCE(json_extract(check_json, '$.bridge'), '{}'))),
		'$.bridge.pool', CAST(? AS INTEGER), '$.bridge.pool_at', CAST(? AS INTEGER)) WHERE id = ?`, int64(pool), at, id)
}

// The pool of an exit's connections to an entry (BridgeDial.pool) and how the panel chooses it. Two connections do for most pairs
// (the bridge keeps its own waiting pairs on each and spreads people over them); four carry about twice the people and shield a
// pair from the loss of one connection, at the price of more waiting pairs. The panel raises the pool to 4 when either holds:
//   - the relay is busy: its forward carried 10 Mbit/s or more at the last reading (a fresh counter), or
//   - the bridge is nearly full: the entry carries 80 % or more of what two connections can (2 x BridgePeoplePerConn people),
//     so that the people who come next are not turned away.
//
// It goes back to 2 only after neither has held for 30 minutes and the entry carries under 40 % of what two connections can, so a
// load that moves around a threshold does not move the pool with it. The figure of people is the bridge's, the same for every
// relay of the pair, and so is the answer; a pair with several relays takes the largest pool any of them asks for.
const (
	BridgePoolMin  = 2
	BridgePoolMax  = 4
	bridgeBusyBPS  = 10_000_000
	bridgePoolHold = 30 * time.Minute
)

// BridgeCapacity is the most people a pool of this many connections carries at once (plugin.BridgePeoplePerConn a connection).
func BridgeCapacity(pool uint32) uint32 { return pool * uint32(plugin.BridgePeoplePerConn) }

// BridgeNearFull says the entry carries 80 % or more of what the pool can.
func BridgeNearFull(clients, pool uint32) bool {
	return pool > 0 && uint64(clients)*5 >= uint64(BridgeCapacity(pool))*4
}

// BridgePool is the pool to ask for, and when the reason for 4 last held, given what was asked before (0 = nothing yet).
func BridgePool(prev uint32, prevAt int64, clients uint32, bps uint64, now time.Time) (uint32, int64) {
	atTwo := BridgeCapacity(BridgePoolMin)
	reason := bps >= bridgeBusyBPS || BridgeNearFull(clients, BridgePoolMin)
	switch {
	case reason:
		return BridgePoolMax, now.Unix()
	case prev >= BridgePoolMax && (prevAt <= 0 || prevAt > now.Unix() || now.Sub(time.Unix(prevAt, 0)) < bridgePoolHold || uint64(clients)*5 >= uint64(atTwo)*2):
		return BridgePoolMax, prevAt
	}
	return BridgePoolMin, 0
}

// SetRelayDirectStreak stores the count of good direct checks in check_json.direct_streak (0 removes it). ErrNotFound when the
// relay is gone.
func (s *Store) SetRelayDirectStreak(ctx context.Context, id string, streak uint32) error {
	return s.SetRelayDirectState(ctx, id, streak, 0)
}

// SetRelayDirectState stores the streak and the timestamp of its latest sample. D1 returns JSON numbers as REAL, so both values
// are cast back to integers in SQL.
func (s *Store) SetRelayDirectState(ctx context.Context, id string, streak uint32, at int64) error {
	if streak == 0 {
		return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_remove(COALESCE(check_json, '{}'), '$.direct_streak', '$.direct_streak_at') WHERE id = ?`, id)
	}
	return s.relayCheckMember(ctx, `UPDATE relay SET check_json = json_set(COALESCE(check_json, '{}'), '$.direct_streak', CAST(? AS INTEGER), '$.direct_streak_at', CAST(? AS INTEGER)) WHERE id = ?`, int64(min(streak, 3)), at, id)
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
