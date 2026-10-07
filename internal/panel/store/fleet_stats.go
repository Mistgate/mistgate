package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
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

// IngestStats commits one StatsBatch in one read batch and one guarded write batch together with node.last_seq
// (agent.proto "RELIABLE MESSAGES"): traffic_bucket, node_traffic_hour (+peaks), user.used_bytes, and user.used_bytes
// (status rules belong to the access module).
// seq == 0 (not a reliable message) skips the dedup and the counter update.
func (s *Store) IngestStats(ctx context.Context, in FleetStatsIn) (FleetStatsOut, error) {
	type bucketKey struct{ user, protocol string }
	type sums struct{ up, down uint64 }
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

		perBucket := map[bucketKey]*sums{}
		perProto := map[string]*sums{}
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
				perBucket[k] = &sums{}
			}
			perBucket[k].up = satAdd(perBucket[k].up, up)
			perBucket[k].down = satAdd(perBucket[k].down, down)
			if perProto[ref.Protocol] == nil {
				perProto[ref.Protocol] = &sums{}
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

		stmts := []Stmt{guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND agent_instance_id = ? AND last_seq = ?)`, in.NodeID, instance, int64(last))}
		for k, v := range perBucket {
			stmts = append(stmts, Stmt{Query: `
				INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (user_id, node_id, protocol, hour_start) DO UPDATE SET
					bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down`,
				Args: []any{k.user, in.NodeID, k.protocol, in.HourStart, i64(v.up), i64(v.down)}})
		}
		protos := map[string]bool{}
		for p := range perProto {
			protos[p] = true
		}
		for p := range users {
			protos[p] = true
		}
		for p := range protos {
			var up, down uint64
			if v := perProto[p]; v != nil {
				up, down = v.up, v.down
			}
			stmts = append(stmts, Stmt{Query: `
				INSERT INTO node_traffic_hour (node_id, protocol, hour_start, bytes_up, bytes_down, peak_users, peak_devices)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (node_id, protocol, hour_start) DO UPDATE SET
					bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down,
					peak_users = max(peak_users, excluded.peak_users), peak_devices = max(peak_devices, excluded.peak_devices)`,
				Args: []any{in.NodeID, p, in.HourStart, i64(up), i64(down), len(users[p]), len(devs[p])}})
		}

		for uid, n := range perUser {
			stmts = append(stmts, Stmt{Query: `UPDATE user SET used_bytes = used_bytes + ?, last_seen_at = ?, last_node_id = ? WHERE id = ?`,
				Args: []any{i64(n), unix(in.Now), in.NodeID, uid}})
			out.Users = append(out.Users, uid)
		}
		for _, cert := range in.Certs {
			stmts = append(stmts, inboundCertStmt(in.NodeID, cert.InboundID, cert.Pin, cert.NotAfter, in.Now))
		}
		for _, se := range in.Sessions {
			ref, ok := creds[se.CredID]
			if !ok || ref.Protocol != "awg" || ref.DeviceID == "" || se.ConnectedAt.IsZero() || se.ConnectedAt.Unix() <= 0 {
				continue
			}
			touchedAt := se.ConnectedAt
			if touchedAt.After(in.Now) {
				touchedAt = in.Now
			}
			stmts = append(stmts, Stmt{Query: `UPDATE device SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?`,
				Args: []any{unix(touchedAt), ref.DeviceID, unix(touchedAt)}})
		}
		if in.Seq == 0 {
			stmts = append(stmts, Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ?`, Args: []any{unix(in.Now), in.NodeID}})
		} else {
			stmts = append(stmts, Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?) WHERE id = ?`,
				Args: []any{int64(in.Seq), in.Instance, unix(in.Now), in.NodeID}})
		}
		return stmts, nil
	})
	if err != nil {
		return FleetStatsOut{}, err
	}
	return out, nil
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
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := commitSeq(ctx, tx, nodeID, instance, seq, now); err != nil {
		return err
	}
	return tx.Commit()
}

func commitSeq(ctx context.Context, tx *sql.Tx, nodeID, instance string, seq uint64, now time.Time) error {
	if seq == 0 {
		_, err := tx.ExecContext(ctx, `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ?`, unix(now), nodeID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?) WHERE id = ?`,
		int64(seq), instance, unix(now), nodeID)
	return err
}

// IngestEvent stores one reliable agent Event and node.last_seq in one atomic batch. dup reports that the
// seq was already committed.
func (s *Store) IngestEvent(ctx context.Context, nodeID, instance string, seq uint64, now time.Time, e EventRow) (dup bool, err error) {
	e.NodeID, e.Source, e.SrcInstance, e.SrcSeq = nodeID, "agent", instance, seq
	args := eventInsertArgs(e)
	stmts := []Stmt{{Query: `SELECT id FROM node WHERE id = ?`, Args: []any{nodeID}, Returning: true}}
	if seq == 0 {
		stmts = append(stmts,
			Stmt{Query: `INSERT OR IGNORE INTO event (ts, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
				SELECT ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?
				WHERE EXISTS (SELECT 1 FROM node WHERE id = ?)`, Args: append(args, nodeID)},
			Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ? RETURNING id`, Args: []any{unix(now), nodeID}, Returning: true})
	} else {
		stmts = append(stmts,
			Stmt{Query: `INSERT OR IGNORE INTO event (ts, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
				SELECT ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?
				WHERE EXISTS (SELECT 1 FROM node WHERE id = ? AND (agent_instance_id <> ? OR last_seq < ?))`,
				Args: append(args, nodeID, instance, int64(seq))},
			Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?)
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
