package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"time"
)

// maxDelta clamps a single reported counter so a broken or hostile node cannot overflow SQLite integers
// (1 PiB per 10 s interval is beyond any real link).
const maxDelta = 1 << 50

// FleetTraffic is one TrafficDelta of a StatsBatch.
type FleetTraffic struct {
	CredID, InboundID string
	Up, Down          uint64
}

// FleetSessionRef is one open session of a StatsBatch (used for the hourly peaks and AWG device touches).
type FleetSessionRef struct {
	CredID, InboundID string
	ConnectedAt       time.Time
}

// FleetInboundCert is a certificate reported for one inbound in a StatsBatch.
type FleetInboundCert struct {
	InboundID, Pin string
	NotAfter       time.Time
}

// FleetStatsIn is one reliable StatsBatch to commit.
type FleetStatsIn struct {
	NodeID, Instance string
	Seq              uint64
	Now              time.Time
	HourStart        int64 // bucket hour (multiple of 3600), already clamped by the caller
	Traffic          []FleetTraffic
	Sessions         []FleetSessionRef
	Certs            []FleetInboundCert
}

// FleetCredRef is what a credential id resolves to (credentials are soft-deleted, so revoked ones resolve).
type FleetCredRef struct{ UserID, DeviceID, Protocol string }

// FleetStatsOut is the outcome of IngestStats.
type FleetStatsOut struct {
	Duplicate bool                    // seq already committed: nothing was written
	Refs      map[string]FleetCredRef // every credential of the batch that resolved
	Skipped   int                     // deltas dropped: unknown credential, or inbound not on this node
	Probe     int                     // deltas of the health probe system credential, ignored on purpose (not an anomaly)
	Users     []string                // users whose used_bytes grew (for the access module's status recompute)
}

type fleetStatsSums struct{ up, down uint64 }

type fleetStatsBucketWrite struct {
	UserID    string `json:"user_id"`
	NodeID    string `json:"node_id"`
	Protocol  string `json:"protocol"`
	HourStart int64  `json:"hour_start"`
	BytesUp   int64  `json:"bytes_up"`
	BytesDown int64  `json:"bytes_down"`
}

type fleetStatsHourWrite struct {
	NodeID      string `json:"node_id"`
	Protocol    string `json:"protocol"`
	HourStart   int64  `json:"hour_start"`
	BytesUp     int64  `json:"bytes_up"`
	BytesDown   int64  `json:"bytes_down"`
	PeakUsers   int    `json:"peak_users"`
	PeakDevices int    `json:"peak_devices"`
}

type fleetStatsUserWrite struct {
	UserID string `json:"user_id"`
	Bytes  int64  `json:"bytes"`
}

type fleetInboundCertWrite struct {
	InboundID string `json:"inbound_id"`
	Pin       string `json:"pin"`
	NotAfter  int64  `json:"not_after"`
}

type fleetDeviceTouchWrite struct {
	DeviceID   string `json:"device_id"`
	LastSeenAt int64  `json:"last_seen_at"`
	NotAfter   int64  `json:"not_after"`
}

// IngestStats commits one StatsBatch in one read batch and one guarded write batch together with node.last_seq
// (agent.proto "RELIABLE MESSAGES"): traffic buckets and hourly peaks, user usage, inbound certificates, and device touches.
// seq == 0 (not a reliable message) skips the dedup and the counter update.
func (s *Store) IngestStats(ctx context.Context, in FleetStatsIn) (FleetStatsOut, error) {
	type bucketKey struct{ user, protocol string }
	var out FleetStatsOut
	_, err := s.retryGuarded(ctx, func() ([]Stmt, error) {
		out = FleetStatsOut{Refs: map[string]FleetCredRef{}}
		var instance string
		var last uint64
		onNode := map[string]string{} // inbound id -> its protocol
		creds := map[string]FleetCredRef{}
		probes := map[string]bool{}

		credIDs := make([]string, 0, len(in.Traffic)+len(in.Sessions))
		seenCreds := map[string]bool{}
		for _, d := range in.Traffic {
			if !seenCreds[d.CredID] {
				seenCreds[d.CredID] = true
				credIDs = append(credIDs, d.CredID)
			}
		}
		for _, se := range in.Sessions {
			if !seenCreds[se.CredID] {
				seenCreds[se.CredID] = true
				credIDs = append(credIDs, se.CredID)
			}
		}

		r := reads{}
		r.add(func(rows [][]any) error {
			if len(rows) != 1 {
				return ErrNotFound
			}
			return batchRow(rows[0]).Scan(&instance, &last)
		}, `SELECT agent_instance_id, last_seq FROM node WHERE id = ?`, in.NodeID)
		r.add(func(rows [][]any) error {
			for _, row := range rows {
				var id, protocol string
				if err := batchRow(row).Scan(&id, &protocol); err != nil {
					return err
				}
				onNode[id] = protocol
			}
			return nil
		}, `SELECT i.id, p.protocol FROM inbound i JOIN profile p ON p.id = i.profile_id WHERE i.node_id = ?`, in.NodeID)
		r.add(func(rows [][]any) error {
			for _, row := range rows {
				var id string
				var ref FleetCredRef
				if err := batchRow(row).Scan(&id, &ref.UserID, &ref.DeviceID, &ref.Protocol); err != nil {
					return err
				}
				creds[id] = ref
			}
			return nil
		}, `SELECT id, user_id, device_id, protocol FROM device_credential WHERE id IN (SELECT value FROM json_each(?))`, jsonStrings(credIDs))
		r.add(func(rows [][]any) error {
			for _, row := range rows {
				var id string
				if err := batchRow(row).Scan(&id); err != nil {
					return err
				}
				probes[id] = true
			}
			return nil
		}, `SELECT cred_id FROM health_probe_cred WHERE cred_id IN (SELECT value FROM json_each(?))`, jsonStrings(credIDs))
		if err := r.run(ctx, s); err != nil {
			return nil, err
		}
		lastForInstance := last
		if instance != in.Instance {
			lastForInstance = 0 // another agent instance took over: its seq space starts fresh
		}
		if in.Seq != 0 && in.Seq <= lastForInstance {
			out.Duplicate = true
			return nil, nil
		}
		out.Refs = creds

		perBucket := map[bucketKey]*fleetStatsSums{}
		perProto := map[string]*fleetStatsSums{}
		perUser := map[string]uint64{}
		for _, d := range in.Traffic {
			ref, ok := creds[d.CredID]
			if !ok && probes[d.CredID] {
				out.Probe++
				continue
			}
			if !ok || onNode[d.InboundID] != ref.Protocol { // also: the credential must be of the inbound's protocol
				out.Skipped++
				continue
			}
			up, down := min(d.Up, maxDelta), min(d.Down, maxDelta)
			k := bucketKey{ref.UserID, ref.Protocol}
			if perBucket[k] == nil {
				perBucket[k] = &fleetStatsSums{}
			}
			perBucket[k].up = satAdd(perBucket[k].up, up)
			perBucket[k].down = satAdd(perBucket[k].down, down)
			if perProto[ref.Protocol] == nil {
				perProto[ref.Protocol] = &fleetStatsSums{}
			}
			perProto[ref.Protocol].up = satAdd(perProto[ref.Protocol].up, up)
			perProto[ref.Protocol].down = satAdd(perProto[ref.Protocol].down, down)
			perUser[ref.UserID] = satAdd(perUser[ref.UserID], satAdd(up, down))
		}

		// Peaks: distinct users / devices per protocol in this snapshot.
		users := map[string]map[string]bool{}
		devs := map[string]map[string]bool{}
		for _, se := range in.Sessions {
			ref, ok := creds[se.CredID]
			if !ok || onNode[se.InboundID] != ref.Protocol {
				continue
			}
			if users[ref.Protocol] == nil {
				users[ref.Protocol], devs[ref.Protocol] = map[string]bool{}, map[string]bool{}
			}
			users[ref.Protocol][ref.UserID] = true
			devs[ref.Protocol][ref.DeviceID] = true
		}

		bucketWrites := make([]fleetStatsBucketWrite, 0, len(perBucket))
		for k, v := range perBucket {
			bucketWrites = append(bucketWrites, fleetStatsBucketWrite{UserID: k.user, NodeID: in.NodeID, Protocol: k.protocol,
				HourStart: in.HourStart, BytesUp: i64(v.up), BytesDown: i64(v.down)})
		}
		sort.Slice(bucketWrites, func(i, j int) bool {
			if bucketWrites[i].UserID != bucketWrites[j].UserID {
				return bucketWrites[i].UserID < bucketWrites[j].UserID
			}
			return bucketWrites[i].Protocol < bucketWrites[j].Protocol
		})
		protos := map[string]bool{}
		for p := range perProto {
			protos[p] = true
		}
		for p := range users {
			protos[p] = true
		}
		protocolIDs := make([]string, 0, len(protos))
		for p := range protos {
			protocolIDs = append(protocolIDs, p)
		}
		sort.Strings(protocolIDs)
		hourWrites := make([]fleetStatsHourWrite, 0, len(protocolIDs))
		for _, p := range protocolIDs {
			var up, down uint64
			if v := perProto[p]; v != nil {
				up, down = v.up, v.down
			}
			hourWrites = append(hourWrites, fleetStatsHourWrite{NodeID: in.NodeID, Protocol: p, HourStart: in.HourStart,
				BytesUp: i64(up), BytesDown: i64(down), PeakUsers: len(users[p]), PeakDevices: len(devs[p])})
		}
		userIDs := make([]string, 0, len(perUser))
		for uid := range perUser {
			userIDs = append(userIDs, uid)
		}
		sort.Strings(userIDs)
		userWrites := make([]fleetStatsUserWrite, 0, len(userIDs))
		for _, uid := range userIDs {
			userWrites = append(userWrites, fleetStatsUserWrite{UserID: uid, Bytes: i64(perUser[uid])})
			out.Users = append(out.Users, uid)
		}
		certByInbound := make(map[string]FleetInboundCert, len(in.Certs))
		for _, cert := range in.Certs {
			certByInbound[cert.InboundID] = cert
		}
		certIDs := make([]string, 0, len(certByInbound))
		for id := range certByInbound {
			certIDs = append(certIDs, id)
		}
		sort.Strings(certIDs)
		certWrites := make([]fleetInboundCertWrite, 0, len(certIDs))
		for _, id := range certIDs {
			cert := certByInbound[id]
			certWrites = append(certWrites, fleetInboundCertWrite{InboundID: cert.InboundID, Pin: cert.Pin, NotAfter: fleetUnix(cert.NotAfter)})
		}
		touchAtByDevice := make(map[string]int64)
		for _, se := range in.Sessions {
			ref, ok := creds[se.CredID]
			if !ok || ref.Protocol != "awg" || ref.DeviceID == "" || se.ConnectedAt.Unix() <= 0 {
				continue
			}
			touchedAt := se.ConnectedAt
			if touchedAt.After(in.Now) {
				touchedAt = in.Now
			}
			touchedUnix := unix(touchedAt)
			if previous, ok := touchAtByDevice[ref.DeviceID]; !ok || touchedUnix > previous {
				touchAtByDevice[ref.DeviceID] = touchedUnix
			}
		}
		touchIDs := make([]string, 0, len(touchAtByDevice))
		for id := range touchAtByDevice {
			touchIDs = append(touchIDs, id)
		}
		sort.Strings(touchIDs)
		touchWrites := make([]fleetDeviceTouchWrite, 0, len(touchIDs))
		for _, id := range touchIDs {
			seenAt := touchAtByDevice[id]
			touchWrites = append(touchWrites, fleetDeviceTouchWrite{DeviceID: id, LastSeenAt: seenAt, NotAfter: seenAt})
		}
		stmts, err := fleetStatsWriteStmts(in, instance, last, bucketWrites, hourWrites, userWrites, certWrites, touchWrites)
		if err != nil {
			return nil, err
		}
		return stmts, nil
	})
	if err != nil {
		return FleetStatsOut{}, err
	}
	return out, nil
}

func fleetStatsWriteStmts(in FleetStatsIn, instance string, last uint64, buckets []fleetStatsBucketWrite,
	hours []fleetStatsHourWrite, users []fleetStatsUserWrite, certs []fleetInboundCertWrite,
	touches []fleetDeviceTouchWrite) ([]Stmt, error) {
	bucketJSON, err := json.Marshal(buckets)
	if err != nil {
		return nil, err
	}
	hourJSON, err := json.Marshal(hours)
	if err != nil {
		return nil, err
	}
	userJSON, err := json.Marshal(users)
	if err != nil {
		return nil, err
	}
	certStmt, err := inboundCertsStmt(in.NodeID, in.Now, certs)
	if err != nil {
		return nil, err
	}
	touchStmt, err := deviceTouchStmt(touches)
	if err != nil {
		return nil, err
	}
	return []Stmt{
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND agent_instance_id = ? AND last_seq = ?)`, in.NodeID, instance, int64(last)),
		{Query: `
			INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down)
			SELECT json_extract(bucket.value, '$.user_id'), json_extract(bucket.value, '$.node_id'),
				json_extract(bucket.value, '$.protocol'), CAST(json_extract(bucket.value, '$.hour_start') AS INTEGER),
				CAST(json_extract(bucket.value, '$.bytes_up') AS INTEGER), CAST(json_extract(bucket.value, '$.bytes_down') AS INTEGER)
			FROM json_each(?1) AS bucket
			WHERE 1
			ON CONFLICT (user_id, node_id, protocol, hour_start) DO UPDATE SET
				bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down`, Args: []any{string(bucketJSON)}},
		{Query: `
			INSERT INTO node_traffic_hour (node_id, protocol, hour_start, bytes_up, bytes_down, peak_users, peak_devices)
			SELECT json_extract(hour.value, '$.node_id'), json_extract(hour.value, '$.protocol'),
				CAST(json_extract(hour.value, '$.hour_start') AS INTEGER), CAST(json_extract(hour.value, '$.bytes_up') AS INTEGER),
				CAST(json_extract(hour.value, '$.bytes_down') AS INTEGER), CAST(json_extract(hour.value, '$.peak_users') AS INTEGER),
				CAST(json_extract(hour.value, '$.peak_devices') AS INTEGER)
			FROM json_each(?1) AS hour
			WHERE 1
			ON CONFLICT (node_id, protocol, hour_start) DO UPDATE SET
				bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down,
				peak_users = max(peak_users, excluded.peak_users), peak_devices = max(peak_devices, excluded.peak_devices)`, Args: []any{string(hourJSON)}},
		{Query: `
			UPDATE user SET used_bytes = used_bytes + CAST(json_extract(change.value, '$.bytes') AS INTEGER),
				last_seen_at = ?2, last_node_id = ?3
			FROM json_each(?1) AS change
			WHERE user.id = json_extract(change.value, '$.user_id')`, Args: []any{string(userJSON), unix(in.Now), in.NodeID}},
		certStmt,
		touchStmt,
		commitSeqStmt(in.NodeID, in.Instance, in.Seq, in.Now),
	}, nil
}

func inboundCertsStmt(nodeID string, now time.Time, certs []fleetInboundCertWrite) (Stmt, error) {
	encoded, err := json.Marshal(certs)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		UPDATE inbound SET cert_pin_sha256 = json_extract(cert.value, '$.pin'),
			cert_not_after = CAST(json_extract(cert.value, '$.not_after') AS INTEGER), updated_at = ?2
		FROM json_each(?1) AS cert
		WHERE inbound.id = json_extract(cert.value, '$.inbound_id') AND inbound.node_id = ?3 AND inbound.enabled = 1
			AND (inbound.cert_pin_sha256 != json_extract(cert.value, '$.pin') OR
				inbound.cert_not_after != CAST(json_extract(cert.value, '$.not_after') AS INTEGER))`,
		Args: []any{string(encoded), unix(now), nodeID}}, nil
}

func deviceTouchStmt(touches []fleetDeviceTouchWrite) (Stmt, error) {
	encoded, err := json.Marshal(touches)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		UPDATE device SET last_seen_at = CAST(json_extract(touch.value, '$.last_seen_at') AS INTEGER)
		FROM json_each(?1) AS touch
		WHERE device.id = json_extract(touch.value, '$.device_id')
			AND device.last_seen_at < CAST(json_extract(touch.value, '$.not_after') AS INTEGER)`,
		Args: []any{string(encoded)}}, nil
}

func commitSeqStmt(nodeID, instance string, seq uint64, now time.Time) Stmt {
	if seq == 0 {
		return Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ?`, Args: []any{unix(now), nodeID}}
	}
	return Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?) WHERE id = ?`,
		Args: []any{int64(seq), instance, unix(now), nodeID}}
}

// satAdd adds without wrapping around; i64 converts without turning a huge value negative. The fleet module
// filters implausible numbers before they get here (statsguard.go): these keep SQLite from ever being handed
// something it cannot store, whatever the caller does.
func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

func i64(n uint64) int64 { return int64(min(n, math.MaxInt64)) }

// SkipSeq marks the reliable message seq of the node's current agent instance as processed without applying
// its effects, for a batch that cannot be stored (the node gets its Ack and moves on).
func (s *Store) SkipSeq(ctx context.Context, nodeID, instance string, seq uint64, now time.Time) error {
	_, err := s.batch(ctx, commitSeqStmt(nodeID, instance, seq, now))
	return err
}

// IngestEvent stores one reliable agent Event and node.last_seq in one atomic batch. dup reports that the
// seq was already committed.
func (s *Store) IngestEvent(ctx context.Context, nodeID, instance string, seq uint64, now time.Time, e EventRow) (dup bool, err error) {
	e.NodeID, e.Source, e.SrcInstance, e.SrcSeq = nodeID, "agent", instance, seq
	stmts := []Stmt{
		{Query: `SELECT id FROM node WHERE id = ?`, Args: []any{nodeID}, Returning: true},
		eventInsertStmt(eventInsertArgs(e), nodeID, instance, seq),
	}
	if seq == 0 {
		stmts = append(stmts, Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ? RETURNING id`,
			Args: []any{unix(now), nodeID}, Returning: true})
	} else {
		stmts = append(stmts, Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?)
				WHERE id = ? AND (agent_instance_id <> ? OR last_seq < ?) RETURNING id`,
			Args: []any{int64(seq), instance, unix(now), nodeID, instance, int64(seq)}, Returning: true})
	}
	results, err := s.batch(ctx, stmts...)
	if err != nil {
		return false, err
	}
	if len(results[0].Rows) == 0 {
		return false, ErrNotFound
	}
	return len(results[len(results)-1].Rows) == 0, nil
}

func eventInsertStmt(args []any, nodeID, instance string, seq uint64) Stmt {
	args = append(args, nodeID, int64(seq), instance, int64(seq))
	return Stmt{Query: `INSERT OR IGNORE INTO event (ts, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
		SELECT ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM node WHERE id = ? AND (? = 0 OR agent_instance_id <> ? OR last_seq < ?))`, Args: args}
}

type fleetExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertEvent(ctx context.Context, ex fleetExecer, e EventRow) error {
	_, err := ex.ExecContext(ctx, `
		INSERT OR IGNORE INTO event (ts, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
		VALUES (?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?)`, eventInsertArgs(e)...)
	return err
}

func eventInsertArgs(e EventRow) []any {
	params := "{}"
	if len(e.Params) > 0 {
		b, _ := json.Marshal(e.Params)
		params = string(b)
	}
	var seq any
	var inst any
	if e.SrcSeq != 0 {
		seq, inst = int64(e.SrcSeq), e.SrcInstance
	}
	return []any{unix(e.Time), e.Severity, e.Code, e.Source, e.NodeID, e.UserID, e.DeviceID, e.InboundID, params, inst, seq}
}
