package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SettingNetworkMode = "networks.mode"

var ErrInvalidNetworkMode = errors.New("store: network mode must be 0, 1 or 2")

// ParseNetworkMode returns the stored setting, or the default when it is absent or invalid.
func ParseNetworkMode(value string) uint32 {
	mode, err := strconv.ParseUint(value, 10, 32)
	if err != nil || mode > 2 {
		return 1
	}
	return uint32(mode)
}

// NetworkMode reads the operator setting. A missing row means the default mode 1.
func (s *Store) NetworkMode(ctx context.Context) (uint32, error) {
	values, err := s.SettingValues(ctx, []string{SettingNetworkMode})
	if err != nil {
		return 0, err
	}
	return ParseNetworkMode(values[SettingNetworkMode]), nil
}

// FleetNetwork is one NetworkCounters row of a StatsBatch.
type FleetNetwork struct {
	InboundID  string
	ASN        uint32
	Attempts   uint32
	Unfinished uint32
	Connects   uint32
	Broken     uint32
	People     uint32
}

// FleetPersonNetwork is one person_networks row. CredID is resolved to a user before storage.
type FleetPersonNetwork struct {
	CredID, InboundID string
	ASN               uint32
	Connects          uint32
}

type networkHourWrite struct {
	HourStart  int64  `json:"hour_start"`
	InboundID  string `json:"inbound_id"`
	ASN        uint32 `json:"asn"`
	Attempts   int64  `json:"attempts"`
	Unfinished int64  `json:"unfinished"`
	Connects   int64  `json:"connects"`
	Broken     int64  `json:"broken"`
	People     int64  `json:"people"`
}

type userNetworkWrite struct {
	UserID   string `json:"user_id"`
	ASN      uint32 `json:"asn"`
	Connects int64  `json:"connects"`
	LastHour int64  `json:"last_hour"`
}

type networkKey struct {
	inbound string
	asn     uint32
}

type networkHourCounters struct {
	attempts, unfinished, connects, broken uint64
	people                                 uint32
}

func addNetworkCounter(a, b uint64) uint64 { return min(satAdd(a, b), uint64(maxLinkCount)) }

func networkHourStmt(nodeID string, hourStart int64, rows []FleetNetwork) (Stmt, error) {
	if len(rows) == 0 {
		return Stmt{}, nil
	}
	aggregated := make(map[networkKey]networkHourCounters, len(rows))
	for _, row := range rows {
		if row.InboundID == "" || len(row.InboundID) > 64 {
			continue
		}
		key := networkKey{inbound: row.InboundID, asn: row.ASN}
		value := aggregated[key]
		value.attempts = addNetworkCounter(value.attempts, uint64(row.Attempts))
		value.unfinished = addNetworkCounter(value.unfinished, uint64(row.Unfinished))
		value.connects = addNetworkCounter(value.connects, uint64(row.Connects))
		value.broken = addNetworkCounter(value.broken, uint64(row.Broken))
		value.people = max(value.people, min(row.People, uint32(maxLinkCount)))
		aggregated[key] = value
	}
	keys := make([]networkKey, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].inbound != keys[j].inbound {
			return keys[i].inbound < keys[j].inbound
		}
		return keys[i].asn < keys[j].asn
	})
	encoded := make([]networkHourWrite, 0, len(keys))
	for _, key := range keys {
		v := aggregated[key]
		encoded = append(encoded, networkHourWrite{HourStart: hourStart, InboundID: key.inbound, ASN: key.asn,
			Attempts: i64(v.attempts), Unfinished: i64(v.unfinished), Connects: i64(v.connects), Broken: i64(v.broken), People: int64(v.people)})
	}
	if len(encoded) == 0 {
		return Stmt{}, nil
	}
	b, err := json.Marshal(encoded)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
		SELECT ?1, CAST(json_extract(row.value, '$.hour_start') AS INTEGER), json_extract(row.value, '$.inbound_id'),
			CAST(json_extract(row.value, '$.asn') AS INTEGER), CAST(json_extract(row.value, '$.attempts') AS INTEGER),
			CAST(json_extract(row.value, '$.unfinished') AS INTEGER), CAST(json_extract(row.value, '$.connects') AS INTEGER),
			CAST(json_extract(row.value, '$.broken') AS INTEGER), CAST(json_extract(row.value, '$.people') AS INTEGER)
		FROM json_each(?2) AS row
		WHERE coalesce((SELECT v FROM setting WHERE k = 'networks.mode'), '1') <> '0'
		ON CONFLICT (node_id, hour_start, inbound_id, asn) DO UPDATE SET
			attempts = attempts + excluded.attempts,
			unfinished = unfinished + excluded.unfinished,
			connects = connects + excluded.connects,
			broken = broken + excluded.broken,
			people = max(people, excluded.people)`, Args: []any{nodeID, string(b)}}, nil
}

func userNetworkStmt(rows []userNetworkWrite) (Stmt, error) {
	if len(rows) == 0 {
		return Stmt{}, nil
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		INSERT INTO user_network (user_id, asn, connects, last_hour)
		SELECT json_extract(row.value, '$.user_id'), CAST(json_extract(row.value, '$.asn') AS INTEGER),
			CAST(json_extract(row.value, '$.connects') AS INTEGER), CAST(json_extract(row.value, '$.last_hour') AS INTEGER)
		FROM json_each(?1) AS row
		WHERE (SELECT v FROM setting WHERE k = 'networks.mode') = '2'
		ON CONFLICT (user_id, asn) DO UPDATE SET
			connects = user_network.connects + excluded.connects,
			last_hour = max(user_network.last_hour, excluded.last_hour)`, Args: []any{string(b)}}, nil
}

// SaveNetworkMode changes the setting and removes data made obsolete by the change in one write batch.
func (s *Store) SaveNetworkMode(ctx context.Context, mode uint32) error {
	if mode > 2 {
		return ErrInvalidNetworkMode
	}
	stmts := make([]Stmt, 0, 4)
	switch mode {
	case 0:
		stmts = append(stmts,
			Stmt{Query: `DELETE FROM network_hour`},
			Stmt{Query: `DELETE FROM user_network`},
		)
	case 1:
		stmts = append(stmts, Stmt{Query: `DELETE FROM user_network`})
	}
	if mode == 1 {
		stmts = append(stmts, Stmt{Query: `DELETE FROM setting WHERE k = ?`, Args: []any{SettingNetworkMode}})
	} else {
		stmts = append(stmts, Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
			Args: []any{SettingNetworkMode, strconv.FormatUint(uint64(mode), 10)}})
	}
	_, err := s.batch(ctx, stmts...)
	return err
}

// PruneNetworkHours deletes expired aggregate buckets using their hour index.
func (s *Store) PruneNetworkHours(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.W.ExecContext(ctx, `DELETE FROM network_hour WHERE hour_start < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PruneUserNetworks deletes recent-network rows that have not been updated within retention.
func (s *Store) PruneUserNetworks(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.W.ExecContext(ctx, `DELETE FROM user_network WHERE last_hour < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// FleetNetworkNode is non-secret metadata needed by GetNetworks. Address is consumed only inside the panel to mark relay ASNs.
type FleetNetworkNode struct {
	ID, Name, Address, CountryCode, NetworksDBSHA256 string
	AgentCaps                                        []string
	Inbounds                                         []FleetNetworkInbound
}

type FleetNetworkInbound struct {
	ID, ProfileName, Protocol string
	CDN                       bool
}

// FleetNetworkNodes reads node and inbound metadata in one query without credentials or protocol secrets.
func (s *Store) FleetNetworkNodes(ctx context.Context) ([]FleetNetworkNode, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT n.id, n.name, n.address, n.country_code, n.agent_caps, n.networks_db_sha256,
			i.id, p.name, p.protocol,
			CASE WHEN p.protocol = 'vless' AND coalesce(json_extract(p.settings_json, '$.cdn.host'), '') <> '' THEN 1 ELSE 0 END
		FROM node n LEFT JOIN inbound i ON i.node_id = n.id LEFT JOIN profile p ON p.id = i.profile_id
		WHERE n.state <> 'retired' ORDER BY n.name COLLATE NOCASE, n.id, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indices := map[string]int{}
	var out []FleetNetworkNode
	for rows.Next() {
		var id, name, address, country, caps, digest string
		var inboundID, profileName, protocol sql.NullString
		var cdn sql.NullInt64
		if err := rows.Scan(&id, &name, &address, &country, &caps, &digest, &inboundID, &profileName, &protocol, &cdn); err != nil {
			return nil, err
		}
		idx, ok := indices[id]
		if !ok {
			out = append(out, FleetNetworkNode{ID: id, Name: name, Address: address, CountryCode: country, NetworksDBSHA256: digest,
				AgentCaps: strings.Fields(caps), Inbounds: []FleetNetworkInbound{}})
			idx = len(out) - 1
			indices[id] = idx
		}
		if inboundID.Valid {
			out[idx].Inbounds = append(out[idx].Inbounds, FleetNetworkInbound{ID: inboundID.String, ProfileName: profileName.String,
				Protocol: protocol.String, CDN: cdn.Valid && cdn.Int64 != 0})
		}
	}
	return out, rows.Err()
}

// FleetNetworkSums contains the current window and the preceding window for one node, inbound and ASN.
type FleetNetworkSums struct {
	NodeID, InboundID                                                      string
	ASN                                                                    uint32
	Attempts, Unfinished, Connects, Broken                                 uint64
	PeakPeople                                                             uint64
	BaselineAttempts, BaselineUnfinished, BaselineConnects, BaselineBroken uint64
	BaselinePeakPeople                                                     uint64
}

// FleetNetworkHourSums aggregates the current and previous equal windows, stopping after limit rows.
func (s *Store) FleetNetworkHourSums(ctx context.Context, since, previousSince, until time.Time, nodeID string, limit int) ([]FleetNetworkSums, error) {
	if limit < 1 {
		limit = 5001
	}
	rows, err := s.R.QueryContext(ctx, `
		WITH totals AS (
			SELECT node_id, inbound_id, asn,
				SUM(CASE WHEN hour_start >= ?1 THEN attempts ELSE 0 END) AS attempts,
				SUM(CASE WHEN hour_start >= ?1 THEN unfinished ELSE 0 END) AS unfinished,
				SUM(CASE WHEN hour_start >= ?1 THEN connects ELSE 0 END) AS connects,
				SUM(CASE WHEN hour_start >= ?1 THEN broken ELSE 0 END) AS broken,
				MAX(CASE WHEN hour_start >= ?1 THEN people ELSE 0 END) AS people,
				SUM(CASE WHEN hour_start < ?1 THEN attempts ELSE 0 END) AS baseline_attempts,
				SUM(CASE WHEN hour_start < ?1 THEN unfinished ELSE 0 END) AS baseline_unfinished,
				SUM(CASE WHEN hour_start < ?1 THEN connects ELSE 0 END) AS baseline_connects,
				SUM(CASE WHEN hour_start < ?1 THEN broken ELSE 0 END) AS baseline_broken,
				MAX(CASE WHEN hour_start < ?1 THEN people ELSE 0 END) AS baseline_people
			FROM network_hour h JOIN node n ON n.id = h.node_id AND n.state <> 'retired'
			WHERE hour_start >= ?2 AND hour_start < ?3 AND (?4 = '' OR node_id = ?4)
			GROUP BY node_id, inbound_id, asn
		)
		SELECT node_id, inbound_id, asn, attempts, unfinished, connects, broken, people,
			baseline_attempts, baseline_unfinished, baseline_connects, baseline_broken, baseline_people
		FROM totals ORDER BY max(connects, baseline_connects) DESC, connects DESC, attempts DESC, node_id, inbound_id, asn LIMIT ?5`,
		unix(since), unix(previousSince), unix(until), nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetNetworkSums
	for rows.Next() {
		var row FleetNetworkSums
		if err := rows.Scan(&row.NodeID, &row.InboundID, &row.ASN, &row.Attempts, &row.Unfinished, &row.Connects, &row.Broken, &row.PeakPeople,
			&row.BaselineAttempts, &row.BaselineUnfinished, &row.BaselineConnects, &row.BaselineBroken, &row.BaselinePeakPeople); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
