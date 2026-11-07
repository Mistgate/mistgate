package store

import (
	"context"
	"math"
	"time"
)

// NodeApplyMeta is the when and the how of a node's last report of what it runs (migration 00100).
type NodeApplyMeta struct {
	AppliedAt  time.Time
	Status     string // "applied", "partial", "in_sync" or "" (never)
	DurationMS uint32
	DriftSince time.Time // zero = no drift
}

// NodeApplyMetaStmt is the upsert that records an apply (or an in-sync Hello). A drift keeps the time it began until a
// report without drift clears it.
func NodeApplyMetaStmt(nodeID, status string, durationMS uint32, drift bool, now time.Time) Stmt {
	var since int64
	if drift {
		since = unix(now)
	}
	return Stmt{Query: `
		INSERT INTO node_apply_meta (node_id, applied_at, status, duration_ms, drift_since) VALUES (?1, ?2, ?3, ?4, ?5)
		ON CONFLICT (node_id) DO UPDATE SET applied_at = excluded.applied_at, status = excluded.status,
			duration_ms = excluded.duration_ms,
			drift_since = CASE WHEN excluded.drift_since = 0 THEN 0
				WHEN node_apply_meta.drift_since = 0 THEN excluded.drift_since ELSE node_apply_meta.drift_since END`,
		Args: []any{nodeID, unix(now), status, int64(durationMS), since}}
}

// RecordNodeApplyMeta stores NodeApplyMetaStmt. The node must exist.
func (s *Store) RecordNodeApplyMeta(ctx context.Context, nodeID, status string, durationMS uint32, drift bool, now time.Time) error {
	stmt := NodeApplyMetaStmt(nodeID, status, durationMS, drift, now)
	_, err := s.W.ExecContext(ctx, stmt.Query, stmt.Args...)
	return err
}

// RuntimeInbound is one inbound row of a node as the runtime read needs it: the panel's side (enabled, spec version) and the
// last result the node reported for it (state, error, applied spec hash, certificate expiry).
type RuntimeInbound struct {
	NodeID, ID, ProfileName, Protocol string
	Enabled                           bool
	SpecVersion                       int64
	State, LastError, AppliedSpecHash string
	CertNotAfter                      time.Time
}

// NodeRuntimeInbounds lists the inbounds of one node, or of every node that is not retired when nodeID is empty, ordered by
// node and id. No pin, address or credential is read.
func (s *Store) NodeRuntimeInbounds(ctx context.Context, nodeID string) ([]RuntimeInbound, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT i.node_id, i.id, p.name, p.protocol, i.enabled, i.spec_version, i.state, i.last_error, i.applied_spec_hash, i.cert_not_after
		FROM inbound i JOIN profile p ON p.id = i.profile_id JOIN node n ON n.id = i.node_id
		WHERE (?1 = '' OR i.node_id = ?1) AND n.state <> 'retired'
		ORDER BY i.node_id, i.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuntimeInbound
	for rows.Next() {
		var r RuntimeInbound
		var notAfter int64
		if err := rows.Scan(&r.NodeID, &r.ID, &r.ProfileName, &r.Protocol, &r.Enabled, &r.SpecVersion, &r.State, &r.LastError,
			&r.AppliedSpecHash, &notAfter); err != nil {
			return nil, err
		}
		r.CertNotAfter = fleetTime(notAfter)
		out = append(out, r)
	}
	return out, rows.Err()
}

// NodeApplyMetas reads the apply metadata of one node, or of every node when nodeID is empty; a node without a row is absent.
func (s *Store) NodeApplyMetas(ctx context.Context, nodeID string) (map[string]NodeApplyMeta, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, applied_at, status, duration_ms, drift_since FROM node_apply_meta
		WHERE (?1 = '' OR node_id = ?1)`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeApplyMeta{}
	for rows.Next() {
		var id string
		var at, ms, drift int64
		var m NodeApplyMeta
		if err := rows.Scan(&id, &at, &m.Status, &ms, &drift); err != nil {
			return nil, err
		}
		m.AppliedAt, m.DriftSince = fleetTime(at), fleetTime(drift)
		m.DurationMS = uint32(min(max(ms, 0), math.MaxUint32))
		out[id] = m
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------------------------------------------------
// Host load per hour (migration 00101).

// HostKeepHours is how long node_host_hour rows are kept (90 days); HostMaxHours is the longest window one read returns.
const (
	HostKeepHours = 90 * 24
	HostMaxHours  = 720
)

// HostSample is one HostMetrics reading with the number of sessions of the same batch.
type HostSample struct {
	CPUPct, SoftirqPct, Load1 float64
	RAMUsed, RAMTotal         uint64
	RxBps, TxBps              uint64
	Sessions                  int
}

func x100(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return int64(math.Round(min(v, 1e7) * 100))
}

// HostHourStmt folds one sample into the node's hour row: one upsert, run inside the batch that commits the stats, so it costs
// no round trip of its own.
func HostHourStmt(nodeID string, hourStart int64, s HostSample) Stmt {
	cpu, soft, load := x100(s.CPUPct), x100(s.SoftirqPct), x100(s.Load1)
	rx, tx := i64(min(s.RxBps, 1<<50)), i64(min(s.TxBps, 1<<50))
	return Stmt{Query: `
		INSERT INTO node_host_hour (node_id, hour_start, samples, cpu_sum_x100, cpu_max_x100, softirq_max_x100, load1_max_x100,
			ram_used_max, ram_total, rx_bps_sum, rx_bps_max, tx_bps_sum, tx_bps_max, sessions_max)
		VALUES (?1, ?2, 1, ?3, ?3, ?4, ?5, ?6, ?7, ?8, ?8, ?9, ?9, ?10)
		ON CONFLICT (node_id, hour_start) DO UPDATE SET
			samples = samples + 1, cpu_sum_x100 = cpu_sum_x100 + excluded.cpu_sum_x100,
			cpu_max_x100 = max(cpu_max_x100, excluded.cpu_max_x100),
			softirq_max_x100 = max(softirq_max_x100, excluded.softirq_max_x100),
			load1_max_x100 = max(load1_max_x100, excluded.load1_max_x100),
			ram_used_max = max(ram_used_max, excluded.ram_used_max), ram_total = excluded.ram_total,
			rx_bps_sum = rx_bps_sum + excluded.rx_bps_sum, rx_bps_max = max(rx_bps_max, excluded.rx_bps_max),
			tx_bps_sum = tx_bps_sum + excluded.tx_bps_sum, tx_bps_max = max(tx_bps_max, excluded.tx_bps_max),
			sessions_max = max(sessions_max, excluded.sessions_max)`,
		Args: []any{nodeID, hourStart, cpu, soft, load, i64(s.RAMUsed), i64(s.RAMTotal), rx, tx, int64(max(s.Sessions, 0))}}
}

// HostHour is one hour of a node's host load: means and peaks.
type HostHour struct {
	HourStart                           int64
	Samples                             uint32
	CPUAvg, CPUMax, SoftirqMax, LoadMax float64
	RAMUsedMax, RAMTotal                uint64
	RxAvg, RxMax, TxAvg, TxMax          uint64
	SessionsMax                         uint32
}

// NodeHostHours reads a node's hourly host load for the last hours (at most HostMaxHours), oldest first.
func (s *Store) NodeHostHours(ctx context.Context, nodeID string, now time.Time, hours int) ([]HostHour, error) {
	hours = min(max(hours, 1), HostMaxHours)
	since := now.UTC().Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour).Unix()
	rows, err := s.R.QueryContext(ctx, `
		SELECT hour_start, samples, cpu_sum_x100, cpu_max_x100, softirq_max_x100, load1_max_x100, ram_used_max, ram_total,
			rx_bps_sum, rx_bps_max, tx_bps_sum, tx_bps_max, sessions_max
		FROM node_host_hour WHERE node_id = ? AND hour_start >= ? AND samples > 0 ORDER BY hour_start`, nodeID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostHour
	for rows.Next() {
		var h HostHour
		var samples, cpuSum, cpuMax, softMax, loadMax, ramMax, ramTotal, rxSum, rxMax, txSum, txMax, sessions int64
		if err := rows.Scan(&h.HourStart, &samples, &cpuSum, &cpuMax, &softMax, &loadMax, &ramMax, &ramTotal,
			&rxSum, &rxMax, &txSum, &txMax, &sessions); err != nil {
			return nil, err
		}
		n := max(samples, 1)
		h.Samples = uint32(min(samples, math.MaxUint32))
		h.CPUAvg, h.CPUMax, h.SoftirqMax, h.LoadMax = float64(cpuSum)/float64(n)/100, float64(cpuMax)/100, float64(softMax)/100, float64(loadMax)/100
		h.RAMUsedMax, h.RAMTotal = uint64(max(ramMax, 0)), uint64(max(ramTotal, 0))
		h.RxAvg, h.RxMax, h.TxAvg, h.TxMax = uint64(max(rxSum/n, 0)), uint64(max(rxMax, 0)), uint64(max(txSum/n, 0)), uint64(max(txMax, 0))
		h.SessionsMax = uint32(min(max(sessions, 0), math.MaxUint32))
		out = append(out, h)
	}
	return out, rows.Err()
}

const nodeHostHourPruneBatch = 1000

// PruneNodeHostHours drops up to one batch of hour rows older than the cut (the hour bucket that touches the cut stays).
// Rows left after this batch are removed on a later sweep.
func (s *Store) PruneNodeHostHours(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM node_host_hour
		WHERE (node_id, hour_start) IN (
			SELECT node_id, hour_start FROM node_host_hour WHERE hour_start < ? LIMIT ?
		)`, hourBucket(before), nodeHostHourPruneBatch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
