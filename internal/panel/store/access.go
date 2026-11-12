package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// This file and access_*.go hold the queries of the access module (profiles, inbounds, groups, users,
// devices, credentials). They sit behind Store.Access() and use an "Access" prefix on types and errors so
// they cannot collide with the other modules' files in this package.

var (
	// ErrAccessExists is returned when a unique name or key is already taken.
	ErrAccessExists = errors.New("store: already exists")
	// ErrAccessVersion is returned when an optimistic-concurrency version does not match.
	ErrAccessVersion = errors.New("store: version mismatch")
	// ErrAccessInUse is returned when a row cannot be deleted because others still depend on it.
	ErrAccessInUse = errors.New("store: in use")
)

// Access is the query set of the access module.
type Access struct{ s *Store }

// Access returns the access module's queries.
func (s *Store) Access() Access { return Access{s} }

func accIsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func accIsFK(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

func accBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

// accTimeIfNonzero treats 0 as unset; accTimeFromUnix preserves it as the Unix epoch.
func accTimeIfNonzero(v sql.NullInt64) time.Time {
	if !v.Valid || v.Int64 == 0 {
		return time.Time{}
	}
	return fromUnix(v.Int64)
}

func accTimeFromUnix(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromUnix(v.Int64)
}

// accNullStr stores "" as NULL.
func accNullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func accNullUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return unix(t)
}

// AccessNode is the part of a node row the access module reads (the fleet module owns the table).
type AccessNode struct {
	ID, Name, Address, CountryCode, Location, Provider string
	BandwidthMbps                                      int
	State                                              string // pending | active | retired
	LastSeenAt                                         time.Time
	AgentCaps                                          []string
	// IPv6Entry is the node's IPv6 entry (nodes.ipv6_entry): the address of its "· IPv6" server, "" = off.
	IPv6Entry string
	// PublicLabel is the owner's short text for people (nodes.public_label): "" = none. Not the node's name.
	PublicLabel string
	// ClientIPv6 is the node's "IPv6 for clients" switch (nodes.client_ipv6); false also for a row that was not read from the table.
	ClientIPv6 bool
	// IPv6 is what the doctor's ipv6 check last said of the node's uplink (the state the node page words as "IPv6 at the hoster does
	// not work"); the zero value means it has not reported. WarpIPv6 says the node's enabled WARP account has an IPv6 address, which is
	// what makes the node route client IPv6 into WARP.
	IPv6     IPv6Doctor
	WarpIPv6 bool
}

const accNodeCols = `n.id AS node_id, n.name AS node_name, n.address AS node_address, n.country_code AS node_country_code,
	n.location AS node_location, n.provider AS node_provider, n.bandwidth_mbps AS node_bandwidth_mbps, n.state AS node_state,
	n.last_seen_at AS node_last_seen_at, n.agent_caps AS node_agent_caps, n.ipv6_entry AS node_ipv6_entry, n.public_label AS node_public_label,
	n.client_ipv6 AS node_client_ipv6, (SELECT d.params_json FROM doctor_result d WHERE d.node_id = n.id AND d.check_id = 'ipv6') AS node_ipv6_params,
	coalesce((SELECT 1 FROM warp_account w WHERE w.node_id = n.id AND w.enabled = 1 AND w.address_v6 <> ''), 0) AS node_warp_ipv6`

func scanAccessNode(r rowScanner) (AccessNode, error) {
	var n AccessNode
	var id, name, address, country, location, provider, state, caps, entry, label sql.NullString
	var bandwidth, clientIPv6, warpIPv6 sql.NullInt64
	var ipv6Params sql.NullString
	var seen int64
	err := r.Scan(&id, &name, &address, &country, &location, &provider, &bandwidth, &state, &seen, &caps, &entry, &label, &clientIPv6, &ipv6Params, &warpIPv6)
	n.ID, n.Name, n.Address, n.CountryCode = id.String, name.String, address.String, country.String
	n.Location, n.Provider, n.BandwidthMbps, n.State = location.String, provider.String, int(bandwidth.Int64), state.String
	n.LastSeenAt, n.IPv6Entry, n.PublicLabel = fleetTime(seen), entry.String, label.String
	n.ClientIPv6, n.WarpIPv6 = clientIPv6.Int64 != 0, warpIPv6.Int64 != 0
	if ipv6Params.Valid {
		n.IPv6 = DoctorIPv6(&DoctorRow{NodeID: n.ID, CheckID: "ipv6", Params: parseMap(ipv6Params.String)})
	}
	if caps.String != "" {
		n.AgentCaps = strings.Fields(caps.String)
	}
	return n, err
}

// Node returns one node.
func (a Access) Node(ctx context.Context, id string) (AccessNode, error) {
	n, err := scanAccessNode(a.s.R.QueryRowContext(ctx, `SELECT `+accNodeCols+` FROM node n WHERE n.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// Nodes returns all nodes that are not retired, ordered by name.
func (a Access) Nodes(ctx context.Context) ([]AccessNode, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT `+accNodeCols+` FROM node n WHERE n.state <> 'retired' ORDER BY n.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessNode
	for rows.Next() {
		n, err := scanAccessNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ExistingNodeIDs returns those of ids that exist.
func (a Access) ExistingNodeIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT id FROM node WHERE id IN (SELECT value FROM json_each(?))`, accJSON(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
