package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// FleetSessionRef is one open session of a StatsBatch (used for the hourly peaks).
type FleetSessionRef struct{ CredID, InboundID string }

// FleetStatsIn is one reliable StatsBatch to commit.
type FleetStatsIn struct {
	NodeID, Instance string
	Seq              uint64
	Now              time.Time
	HourStart        int64 // bucket hour (multiple of 3600), already clamped by the caller
	Traffic          []FleetTraffic
	Sessions         []FleetSessionRef
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

// IngestStats commits one StatsBatch in ONE transaction together with node.last_seq (agent.proto
// "RELIABLE MESSAGES"): traffic_bucket, node_traffic_hour (+peaks), user.used_bytes, and user.used_bytes (status rules belong to the access module).
// seq == 0 (not a reliable message) skips the dedup and the counter update.
func (s *Store) IngestStats(ctx context.Context, in FleetStatsIn) (FleetStatsOut, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return FleetStatsOut{}, err
	}
	defer tx.Rollback()

	inst, last, err := nodeSeq(ctx, tx, in.NodeID)
	if err != nil {
		return FleetStatsOut{}, err
	}
	if inst != in.Instance {
		last = 0 // another agent instance took over: its seq space starts fresh
	}
	out := FleetStatsOut{Refs: map[string]FleetCredRef{}}
	if in.Seq != 0 && in.Seq <= last {
		out.Duplicate = true
		return out, nil
	}

	onNode := map[string]string{} // inbound id -> its protocol
	rows, err := tx.QueryContext(ctx, `SELECT i.id, p.protocol FROM inbound i JOIN profile p ON p.id = i.profile_id WHERE i.node_id = ?`, in.NodeID)
	if err != nil {
		return FleetStatsOut{}, err
	}
	for rows.Next() {
		var id, protocol string
		if err := rows.Scan(&id, &protocol); err != nil {
			rows.Close()
			return FleetStatsOut{}, err
		}
		onNode[id] = protocol
	}
	rows.Close()

	resolve := func(credID string) (FleetCredRef, bool, error) {
		if r, ok := out.Refs[credID]; ok {
			return r, true, nil
		}
		var r FleetCredRef
		err := tx.QueryRowContext(ctx, `SELECT user_id, device_id, protocol FROM device_credential WHERE id = ?`, credID).
			Scan(&r.UserID, &r.DeviceID, &r.Protocol)
		if errors.Is(err, sql.ErrNoRows) {
			return r, false, nil
		}
		if err != nil {
			return r, false, err
		}
		out.Refs[credID] = r
		return r, true, nil
	}

	type bucketKey struct{ user, protocol string }
	type sums struct{ up, down uint64 }
	perBucket := map[bucketKey]*sums{}
	perProto := map[string]*sums{}
	perUser := map[string]uint64{}
	for _, d := range in.Traffic {
		ref, ok, err := resolve(d.CredID)
		if err != nil {
			return FleetStatsOut{}, err
		}
		if !ok {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM health_probe_cred WHERE cred_id = ?`, d.CredID).Scan(&one)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return FleetStatsOut{}, err
			}
			if err == nil {
				out.Probe++
				continue
			}
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
		ref, ok, err := resolve(se.CredID)
		if err != nil {
			return FleetStatsOut{}, err
		}
		if !ok || onNode[se.InboundID] != ref.Protocol {
			continue
		}
		if users[ref.Protocol] == nil {
			users[ref.Protocol], devs[ref.Protocol] = map[string]bool{}, map[string]bool{}
		}
		users[ref.Protocol][ref.UserID] = true
		devs[ref.Protocol][ref.DeviceID] = true
	}

	for k, v := range perBucket {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, node_id, protocol, hour_start) DO UPDATE SET
				bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down`,
			k.user, in.NodeID, k.protocol, in.HourStart, i64(v.up), i64(v.down)); err != nil {
			return FleetStatsOut{}, err
		}
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
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO node_traffic_hour (node_id, protocol, hour_start, bytes_up, bytes_down, peak_users, peak_devices)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node_id, protocol, hour_start) DO UPDATE SET
				bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down,
				peak_users = max(peak_users, excluded.peak_users), peak_devices = max(peak_devices, excluded.peak_devices)`,
			in.NodeID, p, in.HourStart, i64(up), i64(down), len(users[p]), len(devs[p])); err != nil {
			return FleetStatsOut{}, err
		}
	}

	for uid, n := range perUser {
		if _, err := tx.ExecContext(ctx,
			`UPDATE user SET used_bytes = used_bytes + ?, last_seen_at = ?, last_node_id = ? WHERE id = ?`,
			i64(n), unix(in.Now), in.NodeID, uid); err != nil {
			return FleetStatsOut{}, err
		}
		out.Users = append(out.Users, uid)
	}

	if err := commitSeq(ctx, tx, in.NodeID, in.Instance, in.Seq, in.Now); err != nil {
		return FleetStatsOut{}, err
	}
	return out, tx.Commit()
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

func nodeSeq(ctx context.Context, tx *sql.Tx, nodeID string) (instance string, last uint64, err error) {
	var l int64
	err = tx.QueryRowContext(ctx, `SELECT agent_instance_id, last_seq FROM node WHERE id = ?`, nodeID).Scan(&instance, &l)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return instance, uint64(l), err
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

// IngestEvent stores one reliable agent Event and node.last_seq in one transaction. dup reports that the
// seq was already committed.
func (s *Store) IngestEvent(ctx context.Context, nodeID, instance string, seq uint64, now time.Time, e EventRow) (dup bool, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	inst, last, err := nodeSeq(ctx, tx, nodeID)
	if err != nil {
		return false, err
	}
	if inst != instance {
		last = 0
	}
	if seq != 0 && seq <= last {
		return true, nil
	}
	e.NodeID, e.Source, e.SrcInstance, e.SrcSeq = nodeID, "agent", instance, seq
	if err := insertEvent(ctx, tx, e); err != nil {
		return false, err
	}
	if err := commitSeq(ctx, tx, nodeID, instance, seq, now); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

type fleetExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertEvent(ctx context.Context, ex fleetExecer, e EventRow) error {
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
	_, err := ex.ExecContext(ctx, `
		INSERT OR IGNORE INTO event (ts, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
		VALUES (?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?)`,
		unix(e.Time), e.Severity, e.Code, e.Source, e.NodeID, e.UserID, e.DeviceID, e.InboundID, params, inst, seq)
	return err
}
