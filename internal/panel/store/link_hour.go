package store

import (
	"context"
	"encoding/json"
	"time"
)

// FleetLink is one LinkCounters row of a StatsBatch (agent.proto, capability "links/1"): the connection counters of one credential
// on one inbound since the previous batch. CredID "" is the inbound itself.
type FleetLink struct {
	CredID, InboundID                         string
	Connects, Rejects, Drops, Stalls, OnlineS uint32
	UnknownKeys, Hellos, Handshakes           uint32
}

// maxLinkCount clamps one counter of a row of one batch (about a million: no engine counts that many events or seconds of one key in
// a batch). It keeps a row at 7 digits per counter, so the JSON of the largest batch the edge admits stays under D1's 2,000,000
// bytes for one value (TestD1IngestStatsAtEdgeCap).
const maxLinkCount = 1 << 20

// zero reports a row with no counter at all (the agent does not send one; it is not stored).
func (l FleetLink) zero() bool {
	return l.Connects|l.Rejects|l.Drops|l.Stalls|l.OnlineS|l.UnknownKeys|l.Hellos|l.Handshakes == 0
}

// linkHourStmt adds the rows to the hour's bucket in one statement. The rows go as one JSON argument of positional arrays
// ([cred, inbound, user, connects, rejects, drops, stalls, online_s, unknown_keys, hellos, handshakes, probe_handshakes]): D1 binds at
// most 100 values.
// With no rows it still runs (an empty array), so the batch has the same shape on every call.
func linkHourStmt(nodeID string, hourStart int64, rows [][]any) (Stmt, error) {
	if rows == nil {
		rows = [][]any{}
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, user_id, connects, rejects, drops, stalls, online_s,
			unknown_keys, hellos, handshakes, probe_handshakes)
		SELECT ?2, ?3, json_extract(l.value, '$[1]'), json_extract(l.value, '$[0]'), json_extract(l.value, '$[2]'),
			CAST(json_extract(l.value, '$[3]') AS INTEGER), CAST(json_extract(l.value, '$[4]') AS INTEGER),
			CAST(json_extract(l.value, '$[5]') AS INTEGER), CAST(json_extract(l.value, '$[6]') AS INTEGER),
			CAST(json_extract(l.value, '$[7]') AS INTEGER), CAST(json_extract(l.value, '$[8]') AS INTEGER),
			CAST(json_extract(l.value, '$[9]') AS INTEGER), CAST(json_extract(l.value, '$[10]') AS INTEGER),
			CAST(json_extract(l.value, '$[11]') AS INTEGER)
		FROM json_each(?1) AS l
		WHERE 1
		ON CONFLICT (node_id, hour_start, inbound_id, cred_id) DO UPDATE SET
			connects = connects + excluded.connects, rejects = rejects + excluded.rejects, drops = drops + excluded.drops,
			stalls = stalls + excluded.stalls, online_s = online_s + excluded.online_s,
			unknown_keys = unknown_keys + excluded.unknown_keys, hellos = hellos + excluded.hellos,
			handshakes = handshakes + excluded.handshakes, probe_handshakes = probe_handshakes + excluded.probe_handshakes`,
		Args: []any{string(encoded), nodeID, hourStart}}, nil
}

// linkHourOf is the first hour bucket a window of the given start touches: the hour that holds since.
func linkHourOf(since time.Time) int64 { return unix(since) / 3600 * 3600 }

// PruneLinkHours deletes the hour buckets that end before the cut (the bucket holding the cut stays whole) and returns how many
// rows went.
func (s *Store) PruneLinkHours(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM link_hour WHERE hour_start < ?`, linkHourOf(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LinkInboundSums is the counters of one inbound of a node over a window: its keys and its own row together. KeysStalled is the
// number of keys with a stall, KeysTried the number with a connect or a stall.
type LinkInboundSums struct {
	InboundID                                     string
	Connects, Rejects, Drops, Stalls, UnknownKeys uint64
	OnlineS, Hellos, Handshakes                   uint64
	KeysStalled, KeysTried                        uint64
}

// NodeLinkSums reads the counters of a node's inbounds over the hour buckets that touch [since, now], one row per inbound that
// has any. It is one read (a per-key subquery folds the keys). The handshakes of the panel's own checks (probe_handshakes) are
// taken off hellos and handshakes; the agent flushes those every 10 minutes and the probe's connects at once, so the window's
// edges may be off by a check or two, and the difference stops at zero.
func (s *Store) NodeLinkSums(ctx context.Context, nodeID string, since time.Time) ([]LinkInboundSums, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT inbound_id, SUM(connects), SUM(rejects), SUM(drops), SUM(stalls), SUM(online_s), SUM(unknown_keys),
			MAX(0, SUM(hellos) - SUM(probe)), MAX(0, SUM(handshakes) - SUM(probe)),
			SUM(cred_id <> '' AND stalls > 0), SUM(cred_id <> '' AND connects + stalls > 0)
		FROM (SELECT inbound_id, cred_id, SUM(connects) AS connects, SUM(rejects) AS rejects, SUM(drops) AS drops,
				SUM(stalls) AS stalls, SUM(online_s) AS online_s, SUM(unknown_keys) AS unknown_keys, SUM(hellos) AS hellos,
				SUM(handshakes) AS handshakes, SUM(probe_handshakes) AS probe
			FROM link_hour WHERE node_id = ? AND hour_start >= ? GROUP BY inbound_id, cred_id)
		GROUP BY inbound_id ORDER BY inbound_id`, nodeID, linkHourOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkInboundSums
	for rows.Next() {
		var r LinkInboundSums
		if err := rows.Scan(&r.InboundID, &r.Connects, &r.Rejects, &r.Drops, &r.Stalls, &r.OnlineS, &r.UnknownKeys, &r.Hellos,
			&r.Handshakes, &r.KeysStalled, &r.KeysTried); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserLink is the counters of one device of a person on one inbound of a node over a window. LastHour is the start of the newest
// hour with a counter.
type UserLink struct {
	DeviceID, NodeID, NodeName, InboundID, ProfileName, Protocol string
	Platform, Client, Kind                                       string
	Connects, Rejects, Drops, Stalls, OnlineS                    uint64
	LastHour                                                     time.Time
	Hours                                                        []LinkHourSums
}

// UserLinks reads a person's counters over the hour buckets that touch [since, now], per device, node and inbound, the ones with a
// counter only; device, then node name, then profile name. The device comes from the credential; an inbound or node that no longer
// exists has no row.
func (s *Store) UserLinks(ctx context.Context, userID string, since time.Time) ([]UserLink, error) {
	return s.UserLinksByHour(ctx, userID, since, false)
}

// LinkKeepHours is how long link_hour rows live, and the most a connection RPC may look back.
const LinkKeepHours = 168

// LinkWindowHours is the window of a connection RPC in hours: 0 means 24, and more than the retention is cut to it.
func LinkWindowHours(requested uint32) int {
	switch {
	case requested == 0:
		return 24
	case requested > LinkKeepHours:
		return LinkKeepHours
	}
	return int(requested)
}
