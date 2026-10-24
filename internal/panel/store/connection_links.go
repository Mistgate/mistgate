package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// LinkHourSums is one non-zero hour bucket for an inbound or device.
type LinkHourSums struct {
	HourStart                                             int64
	Connects, Rejects, Drops, Stalls, OnlineS             uint64
	UnknownKeys, Hellos, Handshakes                       uint64
	ClientCloses, Freezes, Quiet, Accepted, HellosForeign uint64
	RttMSSum, RttSamples, PktsSent, PktsLost              uint64
}

// LinkClientSums is one app and platform on an inbound over a window.
type LinkClientSums struct {
	Client, Platform                                         string
	Devices                                                  uint64
	Connects, Rejects, Drops, Stalls, OnlineS                uint64
	Freezes, Quiet, RttMSSum, RttSamples, PktsSent, PktsLost uint64
}

// NodeLinkReport is the totals and optional hour buckets of one inbound, plus its app breakdown.
type NodeLinkReport struct {
	Totals  LinkInboundSums
	Hours   []LinkHourSums
	Clients []LinkClientSums
}

// NodeLinkReports reads one node's totals, optional hour buckets and client breakdown in one query.
func (s *Store) NodeLinkReports(ctx context.Context, nodeID string, since time.Time, byHour bool) ([]NodeLinkReport, error) {
	query := `WITH base AS (
			SELECT l.inbound_id, l.hour_start, l.cred_id,
				SUM(l.connects) AS connects, SUM(l.rejects) AS rejects, SUM(l.drops) AS drops,
				SUM(l.stalls) AS stalls, SUM(l.online_s) AS online_s, SUM(l.unknown_keys) AS unknown_keys,
				SUM(l.hellos) AS hellos, SUM(l.handshakes) AS handshakes, SUM(l.probe_handshakes) AS probe,
				SUM(l.client_closes) AS client_closes, SUM(l.freezes) AS freezes, SUM(l.quiet) AS quiet,
				SUM(l.accepted) AS accepted, SUM(l.hellos_foreign) AS hellos_foreign, SUM(l.rtt_ms_sum) AS rtt_ms_sum,
				SUM(l.rtt_samples) AS rtt_samples, SUM(l.pkts_sent) AS pkts_sent, SUM(l.pkts_lost) AS pkts_lost,
				COALESCE(d.id, '') AS device_id, COALESCE(d.client, '') AS client, COALESCE(d.platform, '') AS platform
			FROM link_hour l
			LEFT JOIN device_credential dc ON dc.id = l.cred_id AND l.cred_id <> ''
			LEFT JOIN device d ON d.id = dc.device_id
			WHERE l.node_id = ? AND l.hour_start >= ?
			GROUP BY l.inbound_id, l.hour_start, l.cred_id, d.id, d.client, d.platform
		), keys AS (
			SELECT inbound_id, cred_id, SUM(connects) AS connects, SUM(stalls) AS stalls
			FROM base GROUP BY inbound_id, cred_id
		), key_totals AS (
			SELECT inbound_id, SUM(cred_id <> '' AND stalls > 0) AS keys_stalled,
				SUM(cred_id <> '' AND connects + stalls > 0) AS keys_tried
			FROM keys GROUP BY inbound_id
		), totals AS (
			SELECT inbound_id, SUM(connects) AS connects, SUM(rejects) AS rejects, SUM(drops) AS drops,
				SUM(stalls) AS stalls, SUM(online_s) AS online_s, SUM(unknown_keys) AS unknown_keys,
				MAX(0, SUM(hellos) - SUM(probe)) AS hellos, MAX(0, SUM(handshakes) - SUM(probe)) AS handshakes,
				SUM(client_closes) AS client_closes, SUM(freezes) AS freezes, SUM(quiet) AS quiet,
				MAX(0, SUM(accepted) - SUM(probe)) AS accepted, SUM(hellos_foreign) AS hellos_foreign,
				SUM(rtt_ms_sum) AS rtt_ms_sum, SUM(rtt_samples) AS rtt_samples, SUM(pkts_sent) AS pkts_sent, SUM(pkts_lost) AS pkts_lost
			FROM base GROUP BY inbound_id
		), hours AS (
			SELECT inbound_id, hour_start, SUM(connects) AS connects, SUM(rejects) AS rejects, SUM(drops) AS drops,
				SUM(stalls) AS stalls, SUM(online_s) AS online_s, SUM(unknown_keys) AS unknown_keys,
				MAX(0, SUM(hellos) - SUM(probe)) AS hellos, MAX(0, SUM(handshakes) - SUM(probe)) AS handshakes,
				SUM(client_closes) AS client_closes, SUM(freezes) AS freezes, SUM(quiet) AS quiet,
				MAX(0, SUM(accepted) - SUM(probe)) AS accepted, SUM(hellos_foreign) AS hellos_foreign,
				SUM(rtt_ms_sum) AS rtt_ms_sum, SUM(rtt_samples) AS rtt_samples, SUM(pkts_sent) AS pkts_sent, SUM(pkts_lost) AS pkts_lost
			FROM base WHERE ? = 1 GROUP BY inbound_id, hour_start
			HAVING SUM(connects) + SUM(rejects) + SUM(drops) + SUM(stalls) + SUM(online_s) + SUM(unknown_keys) +
				MAX(0, SUM(hellos) - SUM(probe)) + MAX(0, SUM(handshakes) - SUM(probe)) + SUM(client_closes) + SUM(freezes) +
				SUM(quiet) + MAX(0, SUM(accepted) - SUM(probe)) + SUM(hellos_foreign) + SUM(rtt_samples) + SUM(pkts_sent) + SUM(pkts_lost) > 0
		), client_totals AS (
			SELECT inbound_id, client, platform, COUNT(DISTINCT device_id) AS devices,
				SUM(connects) AS connects, SUM(rejects) AS rejects, SUM(drops) AS drops,
				SUM(stalls) AS stalls, SUM(online_s) AS online_s, SUM(freezes) AS freezes, SUM(quiet) AS quiet,
				SUM(rtt_ms_sum) AS rtt_ms_sum, SUM(rtt_samples) AS rtt_samples, SUM(pkts_sent) AS pkts_sent, SUM(pkts_lost) AS pkts_lost
			FROM base WHERE cred_id <> '' AND device_id <> ''
			GROUP BY inbound_id, client, platform
			HAVING SUM(connects) + SUM(rejects) + SUM(drops) + SUM(stalls) + SUM(online_s) + SUM(freezes) + SUM(quiet) +
				SUM(rtt_samples) + SUM(pkts_sent) + SUM(pkts_lost) > 0
		), ranked_clients AS (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY inbound_id ORDER BY connects + rejects + drops + stalls DESC, client, platform) AS rank
			FROM client_totals
		)
		SELECT 0 AS row_type, t.inbound_id, 0 AS hour_start, '' AS client, '' AS platform, 0 AS devices,
			t.connects, t.rejects, t.drops, t.stalls, t.online_s, t.unknown_keys, t.hellos, t.handshakes,
			t.client_closes, t.freezes, t.quiet, t.accepted, t.hellos_foreign, t.rtt_ms_sum, t.rtt_samples, t.pkts_sent, t.pkts_lost,
			k.keys_stalled, k.keys_tried
		FROM totals t JOIN key_totals k USING (inbound_id)
		UNION ALL
		SELECT 1, inbound_id, hour_start, '', '', 0,
			connects, rejects, drops, stalls, online_s, unknown_keys, hellos, handshakes,
			client_closes, freezes, quiet, accepted, hellos_foreign, rtt_ms_sum, rtt_samples, pkts_sent, pkts_lost, 0, 0
		FROM hours
		UNION ALL
		SELECT 2, inbound_id, 0, client, platform, devices,
			connects, rejects, drops, stalls, online_s, 0, 0, 0,
			0, freezes, quiet, 0, 0, rtt_ms_sum, rtt_samples, pkts_sent, pkts_lost, 0, 0
		FROM ranked_clients WHERE rank <= 8
		ORDER BY inbound_id, row_type, hour_start, connects DESC, client, platform`
	rows, err := s.R.QueryContext(ctx, query, nodeID, linkHourOf(since), int64(boolInt(byHour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*NodeLinkReport{}
	var order []string
	for rows.Next() {
		var kind int
		var row NodeLinkReadRow
		if err := rows.Scan(&kind, &row.InboundID, &row.HourStart, &row.Client, &row.Platform, &row.Devices,
			&row.Connects, &row.Rejects, &row.Drops, &row.Stalls, &row.OnlineS, &row.UnknownKeys, &row.Hellos,
			&row.Handshakes, &row.ClientCloses, &row.Freezes, &row.Quiet, &row.Accepted, &row.HellosForeign, &row.RttMSSum,
			&row.RttSamples, &row.PktsSent, &row.PktsLost, &row.KeysStalled, &row.KeysTried); err != nil {
			return nil, err
		}
		report := byID[row.InboundID]
		if report == nil {
			report = &NodeLinkReport{Totals: LinkInboundSums{InboundID: row.InboundID}, Hours: []LinkHourSums{}, Clients: []LinkClientSums{}}
			byID[row.InboundID] = report
			order = append(order, row.InboundID)
		}
		switch kind {
		case 0:
			report.Totals = LinkInboundSums{InboundID: row.InboundID, Connects: row.Connects, Rejects: row.Rejects, Drops: row.Drops,
				Stalls: row.Stalls, OnlineS: row.OnlineS, UnknownKeys: row.UnknownKeys, Hellos: row.Hellos, Handshakes: row.Handshakes,
				KeysStalled: row.KeysStalled, KeysTried: row.KeysTried, ClientCloses: row.ClientCloses, Freezes: row.Freezes,
				Quiet: row.Quiet, Accepted: row.Accepted, HellosForeign: row.HellosForeign, RttMSSum: row.RttMSSum,
				RttSamples: row.RttSamples, PktsSent: row.PktsSent, PktsLost: row.PktsLost}
		case 1:
			report.Hours = append(report.Hours, LinkHourSums{HourStart: row.HourStart, Connects: row.Connects, Rejects: row.Rejects,
				Drops: row.Drops, Stalls: row.Stalls, OnlineS: row.OnlineS, UnknownKeys: row.UnknownKeys, Hellos: row.Hellos, Handshakes: row.Handshakes,
				ClientCloses: row.ClientCloses, Freezes: row.Freezes, Quiet: row.Quiet, Accepted: row.Accepted,
				HellosForeign: row.HellosForeign, RttMSSum: row.RttMSSum, RttSamples: row.RttSamples, PktsSent: row.PktsSent, PktsLost: row.PktsLost})
		case 2:
			report.Clients = append(report.Clients, LinkClientSums{Client: row.Client, Platform: row.Platform, Devices: row.Devices,
				Connects: row.Connects, Rejects: row.Rejects, Drops: row.Drops, Stalls: row.Stalls, OnlineS: row.OnlineS,
				Freezes: row.Freezes, Quiet: row.Quiet, RttMSSum: row.RttMSSum, RttSamples: row.RttSamples,
				PktsSent: row.PktsSent, PktsLost: row.PktsLost})
		}
	}
	out := make([]NodeLinkReport, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, rows.Err()
}

// NodeLinkReadRow is the flat row scanned from the single-query report above.
type NodeLinkReadRow struct {
	InboundID, Client, Platform                           string
	HourStart                                             int64
	Devices                                               uint64
	Connects, Rejects, Drops, Stalls, OnlineS             uint64
	UnknownKeys, Hellos, Handshakes                       uint64
	ClientCloses, Freezes, Quiet, Accepted, HellosForeign uint64
	RttMSSum, RttSamples, PktsSent, PktsLost              uint64
	KeysStalled, KeysTried                                uint64
}

// UserLinksByHour reads one person's rows and optional hour buckets, with device metadata from the same query.
func (s *Store) UserLinksByHour(ctx context.Context, userID string, since time.Time, byHour bool) ([]UserLink, error) {
	query := `WITH base AS (
			SELECT dc.device_id, l.node_id, n.name AS node_name, l.inbound_id, p.name AS profile_name, p.protocol,
				COALESCE(d.platform, '') AS platform, COALESCE(d.client, '') AS client, COALESCE(d.kind, '') AS kind,
				l.hour_start, SUM(l.connects) AS connects, SUM(l.rejects) AS rejects, SUM(l.drops) AS drops,
				SUM(l.stalls) AS stalls, SUM(l.online_s) AS online_s,
				SUM(l.client_closes) AS client_closes, SUM(l.freezes) AS freezes, SUM(l.quiet) AS quiet,
				SUM(l.accepted) AS accepted, SUM(l.hellos_foreign) AS hellos_foreign, SUM(l.rtt_ms_sum) AS rtt_ms_sum,
				SUM(l.rtt_samples) AS rtt_samples, SUM(l.pkts_sent) AS pkts_sent, SUM(l.pkts_lost) AS pkts_lost
			FROM link_hour l
			JOIN device_credential dc ON dc.id = l.cred_id
			LEFT JOIN device d ON d.id = dc.device_id
			JOIN node n ON n.id = l.node_id
			JOIN inbound i ON i.id = l.inbound_id
			JOIN profile p ON p.id = i.profile_id
			WHERE l.user_id = ? AND l.user_id <> '' AND l.hour_start >= ? AND l.cred_id <> ''
			GROUP BY dc.device_id, l.node_id, n.name, l.inbound_id, p.name, p.protocol, d.platform, d.client, d.kind, l.hour_start
		), totals AS (
			SELECT device_id, node_id, node_name, inbound_id, profile_name, protocol, platform, client, kind,
				SUM(connects) AS connects, SUM(rejects) AS rejects, SUM(drops) AS drops, SUM(stalls) AS stalls,
				SUM(online_s) AS online_s, SUM(client_closes) AS client_closes, SUM(freezes) AS freezes, SUM(quiet) AS quiet,
				SUM(accepted) AS accepted, SUM(hellos_foreign) AS hellos_foreign, SUM(rtt_ms_sum) AS rtt_ms_sum,
				SUM(rtt_samples) AS rtt_samples, SUM(pkts_sent) AS pkts_sent, SUM(pkts_lost) AS pkts_lost, MAX(hour_start) AS last_hour
			FROM base GROUP BY device_id, node_id, inbound_id
			HAVING SUM(connects) + SUM(rejects) + SUM(drops) + SUM(stalls) + SUM(online_s) + SUM(client_closes) + SUM(freezes) +
				SUM(quiet) + SUM(accepted) + SUM(hellos_foreign) + SUM(rtt_samples) + SUM(pkts_sent) + SUM(pkts_lost) > 0
		), hours AS (
			SELECT device_id, node_id, inbound_id, hour_start, SUM(connects) AS connects, SUM(rejects) AS rejects,
				SUM(drops) AS drops, SUM(stalls) AS stalls, SUM(online_s) AS online_s,
				SUM(client_closes) AS client_closes, SUM(freezes) AS freezes, SUM(quiet) AS quiet,
				SUM(accepted) AS accepted, SUM(hellos_foreign) AS hellos_foreign, SUM(rtt_ms_sum) AS rtt_ms_sum,
				SUM(rtt_samples) AS rtt_samples, SUM(pkts_sent) AS pkts_sent, SUM(pkts_lost) AS pkts_lost
			FROM base WHERE ? = 1 GROUP BY device_id, node_id, inbound_id, hour_start
			HAVING SUM(connects) + SUM(rejects) + SUM(drops) + SUM(stalls) + SUM(online_s) + SUM(client_closes) + SUM(freezes) +
				SUM(quiet) + SUM(accepted) + SUM(hellos_foreign) + SUM(rtt_samples) + SUM(pkts_sent) + SUM(pkts_lost) > 0
		)
		SELECT 0 AS row_type, device_id, node_id, node_name, inbound_id, profile_name, protocol, platform, client, kind,
			connects, rejects, drops, stalls, online_s, client_closes, freezes, quiet, accepted, hellos_foreign, rtt_ms_sum, rtt_samples,
			pkts_sent, pkts_lost, last_hour, 0 AS hour_start
		FROM totals
		UNION ALL
		SELECT 1, b.device_id, b.node_id, b.node_name, b.inbound_id, b.profile_name, b.protocol, b.platform, b.client, b.kind,
			h.connects, h.rejects, h.drops, h.stalls, h.online_s, h.client_closes, h.freezes, h.quiet, h.accepted, h.hellos_foreign,
			h.rtt_ms_sum, h.rtt_samples, h.pkts_sent, h.pkts_lost, 0, h.hour_start
		FROM hours h JOIN base b USING (device_id, node_id, inbound_id, hour_start)
		GROUP BY h.device_id, h.node_id, h.inbound_id, h.hour_start
		ORDER BY device_id, node_name, profile_name, inbound_id, row_type, hour_start`
	rows, err := s.R.QueryContext(ctx, query, userID, linkHourOf(since), int64(boolInt(byHour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ device, node, inbound string }
	indices := map[key]int{}
	var out []UserLink
	for rows.Next() {
		var kind int
		var r UserLink
		var last, hour int64
		if err := rows.Scan(&kind, &r.DeviceID, &r.NodeID, &r.NodeName, &r.InboundID, &r.ProfileName, &r.Protocol, &r.Platform,
			&r.Client, &r.Kind, &r.Connects, &r.Rejects, &r.Drops, &r.Stalls, &r.OnlineS, &r.ClientCloses, &r.Freezes, &r.Quiet,
			&r.Accepted, &r.HellosForeign, &r.RttMSSum, &r.RttSamples, &r.PktsSent, &r.PktsLost, &last, &hour); err != nil {
			return nil, err
		}
		k := key{r.DeviceID, r.NodeID, r.InboundID}
		i, ok := indices[k]
		if !ok {
			r.LastHour = fromUnix(last)
			r.Hours = []LinkHourSums{}
			out = append(out, r)
			i = len(out) - 1
			indices[k] = i
		}
		if kind == 1 {
			out[i].Hours = append(out[i].Hours, LinkHourSums{HourStart: hour, Connects: r.Connects, Rejects: r.Rejects,
				Drops: r.Drops, Stalls: r.Stalls, OnlineS: r.OnlineS, ClientCloses: r.ClientCloses, Freezes: r.Freezes, Quiet: r.Quiet,
				Accepted: r.Accepted, HellosForeign: r.HellosForeign, RttMSSum: r.RttMSSum, RttSamples: r.RttSamples,
				PktsSent: r.PktsSent, PktsLost: r.PktsLost})
		}
	}
	return out, rows.Err()
}

// FleetNodeLinkSums reads all non-retired nodes' inbound totals from link_hour in one query.
func (s *Store) FleetNodeLinkSums(ctx context.Context, since time.Time) ([]FleetNodeLinkSum, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, inbound_id, SUM(connects), SUM(rejects), SUM(drops), SUM(stalls),
			SUM(online_s), SUM(unknown_keys), MAX(0, SUM(hellos) - SUM(probe)), MAX(0, SUM(handshakes) - SUM(probe)),
			SUM(client_closes), SUM(freezes), SUM(quiet), MAX(0, SUM(accepted) - SUM(probe)), SUM(hellos_foreign),
			SUM(rtt_ms_sum), SUM(rtt_samples), SUM(pkts_sent), SUM(pkts_lost),
			SUM(cred_id <> '' AND stalls > 0), SUM(cred_id <> '' AND connects + stalls > 0)
		FROM (SELECT l.node_id, l.inbound_id, l.cred_id, SUM(l.connects) AS connects, SUM(l.rejects) AS rejects,
				SUM(l.drops) AS drops, SUM(l.stalls) AS stalls, SUM(l.online_s) AS online_s, SUM(l.unknown_keys) AS unknown_keys,
				SUM(l.hellos) AS hellos, SUM(l.handshakes) AS handshakes, SUM(l.probe_handshakes) AS probe,
				SUM(l.client_closes) AS client_closes, SUM(l.freezes) AS freezes, SUM(l.quiet) AS quiet,
				SUM(l.accepted) AS accepted, SUM(l.hellos_foreign) AS hellos_foreign, SUM(l.rtt_ms_sum) AS rtt_ms_sum,
				SUM(l.rtt_samples) AS rtt_samples, SUM(l.pkts_sent) AS pkts_sent, SUM(l.pkts_lost) AS pkts_lost
			FROM link_hour l JOIN node n ON n.id = l.node_id
			WHERE n.state <> 'retired' AND l.hour_start >= ?
			GROUP BY l.node_id, l.inbound_id, l.cred_id)
		GROUP BY node_id, inbound_id ORDER BY node_id, inbound_id`, linkHourOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetNodeLinkSum
	for rows.Next() {
		var r FleetNodeLinkSum
		if err := rows.Scan(&r.NodeID, &r.InboundID, &r.Connects, &r.Rejects, &r.Drops, &r.Stalls, &r.OnlineS,
			&r.UnknownKeys, &r.Hellos, &r.Handshakes, &r.ClientCloses, &r.Freezes, &r.Quiet, &r.Accepted, &r.HellosForeign,
			&r.RttMSSum, &r.RttSamples, &r.PktsSent, &r.PktsLost, &r.KeysStalled, &r.KeysTried); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FleetNodeLinkSum is one node and inbound's total over a window.
type FleetNodeLinkSum struct {
	NodeID string
	LinkInboundSums
}

// FleetConnectionNode is the metadata GetFleetConnections needs, with its inbounds from the same read.
type FleetConnectionNode struct {
	ID, Name, CountryCode string
	AgentCaps             []string
	Inbounds              []FleetInboundRow
}

// FleetConnectionNodes reads every non-retired node and its inbounds in one query. It returns no credentials or profile settings.
func (s *Store) FleetConnectionNodes(ctx context.Context) ([]FleetConnectionNode, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT n.id, n.name, n.country_code, n.agent_caps,
		i.id, p.id, p.name, p.protocol, i.awg_health_json, i.awg_health_at
		FROM node n LEFT JOIN inbound i ON i.node_id = n.id LEFT JOIN profile p ON p.id = i.profile_id
		WHERE n.state <> 'retired' ORDER BY n.name COLLATE NOCASE, n.id, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indices := map[string]int{}
	var out []FleetConnectionNode
	for rows.Next() {
		var id, name, country, caps string
		var inboundID, profileID, profileName, protocol, healthJSON sql.NullString
		var healthAt sql.NullInt64
		if err := rows.Scan(&id, &name, &country, &caps, &inboundID, &profileID, &profileName, &protocol, &healthJSON, &healthAt); err != nil {
			return nil, err
		}
		idx, ok := indices[id]
		if !ok {
			var node FleetConnectionNode
			node.ID, node.Name, node.CountryCode = id, name, country
			node.AgentCaps = strings.Fields(caps)
			node.Inbounds = []FleetInboundRow{}
			out = append(out, node)
			idx = len(out) - 1
			indices[id] = idx
		}
		if inboundID.Valid {
			r := FleetInboundRow{ID: inboundID.String, ProfileID: profileID.String, ProfileName: profileName.String, Protocol: protocol.String,
				AwgHealthJSON: healthJSON.String}
			if healthAt.Valid {
				r.AwgHealthAt = fromUnix(healthAt.Int64)
			}
			out[idx].Inbounds = append(out[idx].Inbounds, r)
		}
	}
	return out, rows.Err()
}

// DeviceKindName returns the persisted kind as a lowercase enum name without a possible generated prefix.
func DeviceKindName(kind string) string {
	kind = strings.TrimPrefix(kind, "DEVICE_KIND_")
	return strings.ToLower(kind)
}
