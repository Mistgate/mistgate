package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Relays: a port of an entry node that passes the traffic of a server on another node (the exit) on unchanged
// (agent.proto "RELAY FORWARDS"). The table is migration 00085.

var (
	// ErrRelayExists: the entry already forwards this server.
	ErrRelayExists = errors.New("store: relay exists")
	// ErrRelayPortTaken: another relay of the entry holds this network and port.
	ErrRelayPortTaken = errors.New("store: relay port taken")
	// ErrRelayNodes: the server or the entry is not there, is retired, or both are the same node.
	ErrRelayNodes = errors.New("store: relay needs a server and another, enrolled node")
)

// Relay is one forward as stored.
type Relay struct {
	ID, InboundID, ViaNodeID string
	Network                  string // "tcp" | "udp": the network of the exit's server
	Port                     uint16 // the port on the entry node
	Enabled                  bool
	// State is what the entry's agent made of the forward in its last ApplyResult: "pending" (not reported since it was
	// switched on), "active" (installed) or "failed" (LastError says why). ToIP is the exit address it points at.
	State, LastError, ToIP string
	AppliedAt              time.Time
	CheckJSON              string
	CreatedAt, UpdatedAt   time.Time
}

// RelayLink is a relay with what the panel knows of its two ends.
type RelayLink struct {
	Relay
	// The exit: the node and the server (inbound) the forward leads to.
	ExitNodeID, ExitNodeName, ExitNodeState string
	ExitEnabled                             bool
	ExitState                               string // the inbound's state
	ProfileID, ProfileName, Protocol        string
	// The entry node.
	ViaNodeName, ViaNodeState string
	ViaNodeCaps               []string
	// What a person's subscription needs of the entry: where to connect, its country (the label of the twin) and its capacity.
	ViaNodeAddress, ViaNodeCountry string
	ViaNodeBandwidthMbps           int
}

// InboundLive is the part of "an inbound is usable" that is not about a person: it is on and not failed, on an enrolled
// node. The access module's liveInbound and the relay rule (RelayLink.Live) are this one definition.
func InboundLive(enabled bool, state, nodeState string) bool {
	return enabled && (state == "pending" || state == "active") && nodeState == "active"
}

// Live: the exit's server is usable, so the entry may forward to it. A relay is usable for a person when this holds, the
// person may use the server and the entry node is one of the person's nodes (access.relayUsable).
func (l RelayLink) Live() bool { return InboundLive(l.ExitEnabled, l.ExitState, l.ExitNodeState) }

// RelayFilter selects relays. The zero value selects all of them.
type RelayFilter struct {
	ID          string
	ViaNodeID   string // the entry node
	NodeID      string // either end
	InboundID   string // the exit's server
	EnabledOnly bool
}

// The columns of a relay whose names the joined tables share are aliased: a batched read (D1) answers by column name.
const relayCols = `r.id AS relay_id, r.inbound_id, r.via_node_id, r.network, r.port, r.enabled AS relay_enabled, r.state AS relay_state,
	r.last_error, r.to_ip, r.applied_at, r.check_json, r.created_at, r.updated_at`

const relayLinkQuery = `SELECT ` + relayCols + `,
		x.id AS exit_node_id, x.name AS exit_node_name, x.state AS exit_node_state, i.enabled AS inbound_enabled, i.state AS inbound_state,
		p.id AS profile_id, p.name AS profile_name, p.protocol, e.name AS via_node_name, e.state AS via_node_state,
		e.agent_caps AS via_node_caps, e.address AS via_node_address, e.country_code AS via_node_country,
		e.bandwidth_mbps AS via_node_bandwidth_mbps
	FROM relay r
	JOIN inbound i ON i.id = r.inbound_id
	JOIN profile p ON p.id = i.profile_id
	JOIN node x ON x.id = i.node_id
	JOIN node e ON e.id = r.via_node_id`

func scanRelay(r rowScanner, extra ...any) (Relay, error) {
	var v Relay
	var port, applied, created, updated int64
	dest := append([]any{&v.ID, &v.InboundID, &v.ViaNodeID, &v.Network, &port, &v.Enabled, &v.State, &v.LastError, &v.ToIP, &applied,
		&v.CheckJSON, &created, &updated}, extra...)
	if err := r.Scan(dest...); err != nil {
		return Relay{}, err
	}
	v.Port, v.AppliedAt, v.CreatedAt, v.UpdatedAt = uint16(port), fleetTime(applied), fromUnix(created), fromUnix(updated)
	return v, nil
}

// RelayLinks lists the relays the filter selects with both ends, oldest first.
func (s *Store) RelayLinks(ctx context.Context, f RelayFilter) ([]RelayLink, error) {
	var where []string
	var args []any
	if f.ID != "" {
		where, args = append(where, `r.id = ?`), append(args, f.ID)
	}
	if f.ViaNodeID != "" {
		where, args = append(where, `r.via_node_id = ?`), append(args, f.ViaNodeID)
	}
	if f.NodeID != "" {
		where, args = append(where, `(r.via_node_id = ? OR x.id = ?)`), append(args, f.NodeID, f.NodeID)
	}
	if f.InboundID != "" {
		where, args = append(where, `r.inbound_id = ?`), append(args, f.InboundID)
	}
	if f.EnabledOnly {
		where = append(where, `r.enabled = 1`)
	}
	q := relayLinkQuery
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := s.R.QueryContext(ctx, q+` ORDER BY r.created_at, r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelayLink
	for rows.Next() {
		l, err := scanRelayLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// scanRelayLink scans a row of relayLinkQuery.
func scanRelayLink(r rowScanner) (RelayLink, error) {
	var l RelayLink
	var caps string
	var address, country sql.NullString
	var bandwidth sql.NullInt64
	var err error
	if l.Relay, err = scanRelay(r, &l.ExitNodeID, &l.ExitNodeName, &l.ExitNodeState, &l.ExitEnabled, &l.ExitState,
		&l.ProfileID, &l.ProfileName, &l.Protocol, &l.ViaNodeName, &l.ViaNodeState, &caps, &address, &country, &bandwidth); err != nil {
		return RelayLink{}, err
	}
	l.ViaNodeCaps = strings.Fields(caps)
	l.ViaNodeAddress, l.ViaNodeCountry, l.ViaNodeBandwidthMbps = address.String, country.String, int(bandwidth.Int64)
	return l, nil
}

// RelayLink returns one relay with both ends, or ErrNotFound.
func (s *Store) RelayLink(ctx context.Context, id string) (RelayLink, error) {
	all, err := s.RelayLinks(ctx, RelayFilter{ID: id})
	if err != nil {
		return RelayLink{}, err
	}
	if len(all) == 0 {
		return RelayLink{}, ErrNotFound
	}
	return all[0], nil
}

// HasRelaysOnNode reports whether a relay leads to a server of the node: its inbound state decides whether the entry
// forwards, so the entry's desired state must follow what the node reports.
func (s *Store) HasRelaysOnNode(ctx context.Context, nodeID string) (bool, error) {
	var one int
	err := s.R.QueryRowContext(ctx, `SELECT 1 FROM relay r JOIN inbound i ON i.id = r.inbound_id WHERE i.node_id = ? LIMIT 1`, nodeID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CreateRelay stores a new relay, switched off and pending. The caller chose the id, the network and the port; the server
// and the entry must be two different nodes that are not retired. ErrRelayNodes, ErrRelayExists, ErrRelayPortTaken. The
// two unique keys of the table make both of the last two safe against a concurrent create, on SQLite and on D1.
func (s *Store) CreateRelay(ctx context.Context, r Relay, now time.Time) error {
	_, err := s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM inbound i JOIN node x ON x.id = i.node_id JOIN node e ON e.id = ?2
			WHERE i.id = ?1 AND x.state <> 'retired' AND e.state <> 'retired' AND x.id <> e.id)`, r.InboundID, r.ViaNodeID),
		Stmt{Query: `INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, 'pending', '{}', ?, ?)`,
			Args: []any{r.ID, r.InboundID, r.ViaNodeID, r.Network, int64(r.Port), unix(now), unix(now)}},
	)
	switch {
	case errors.Is(err, errGuard):
		return ErrRelayNodes
	case err != nil && strings.Contains(err.Error(), "relay.network"):
		return ErrRelayPortTaken
	case err != nil && strings.Contains(err.Error(), "relay.inbound_id"):
		return ErrRelayExists
	}
	return err
}

// RelayPatch changes a relay; nil = unchanged.
type RelayPatch struct {
	Enabled *bool
	Port    *uint16
}

// UpdateRelay applies the patch. A change of the switch or the port puts the relay back to pending (the agent has not
// reported on the new forward yet). ErrNotFound, ErrRelayPortTaken.
func (s *Store) UpdateRelay(ctx context.Context, id string, p RelayPatch, now time.Time) error {
	set := []string{`updated_at = ?`}
	args := []any{unix(now)}
	if p.Enabled != nil {
		set, args = append(set, `enabled = ?`), append(args, accBool(*p.Enabled))
	}
	if p.Port != nil {
		set, args = append(set, `port = ?`), append(args, int64(*p.Port))
	}
	if p.Enabled != nil || p.Port != nil {
		set = append(set, `state = 'pending'`, `last_error = ''`, `to_ip = ''`, `applied_at = 0`)
	}
	res, err := s.W.ExecContext(ctx, `UPDATE relay SET `+strings.Join(set, `, `)+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		if strings.Contains(err.Error(), "relay.network") {
			return ErrRelayPortTaken
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRelay removes a relay. ErrNotFound.
func (s *Store) DeleteRelay(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM relay WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RelayApplied is one RelayResult of an ApplyResult.
type RelayApplied struct {
	ID, Error, ToIP string
	OK              bool
}

// maxRelayResults is the most forwards an agent holds (agent.proto "RELAY FORWARDS"); more results are ignored.
const maxRelayResults = 64

// RelaysApplied stores what the entry node reported for its forwards: ok -> active, else failed with the error. Only
// relays of this node that are switched on (a result for a relay switched off meanwhile is stale).
func (s *Store) RelaysApplied(ctx context.Context, nodeID string, rs []RelayApplied, now time.Time) error {
	if len(rs) == 0 {
		return nil
	}
	rs = rs[:min(len(rs), maxRelayResults)]
	stmts := make([]Stmt, 0, len(rs))
	for _, r := range rs {
		state, msg := "active", ""
		if !r.OK {
			state, msg = "failed", Clip(r.Error, 512)
		}
		stmts = append(stmts, Stmt{Query: `UPDATE relay SET state = ?, last_error = ?, to_ip = ?, applied_at = ?, updated_at = ?
			WHERE id = ? AND via_node_id = ? AND enabled = 1`,
			Args: []any{state, msg, Clip(r.ToIP, 64), unix(now), unix(now), r.ID, nodeID}})
	}
	_, err := s.batch(ctx, stmts...)
	return err
}
