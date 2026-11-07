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
	// Ext is set when the agent sent the resource fields (HostMetrics.agent present, migration 00110); without it the fields
	// below are not known and are not folded in.
	Ext *HostSampleExt
}

// HostSampleExt is what an agent with the resource fields adds to a sample.
type HostSampleExt struct {
	StealPct, IOWaitPct          float64
	SwapUsed                     uint64
	ConntrackCount, ConntrackMax uint64
	TCPSockets                   uint32
	NICSpeedMbps                 uint32
	AgentCPUPct                  float64
	AgentRSS                     uint64
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
	sessions := int64(max(s.Sessions, 0))
	// Without the resource fields the ext columns get zeros: ext_samples does not count the sample, maxima are unchanged, and
	// nic_speed_mbps and conntrack_limit (latest values, not maxima) keep what an earlier sample of the hour said.
	var ext, steal, iowait, swap, ctCount, ctMax, tcpSocks, nic, agentCPU, agentRSS int64
	if e := s.Ext; e != nil {
		ext = 1
		steal, iowait, agentCPU = x100(e.StealPct), x100(e.IOWaitPct), x100(e.AgentCPUPct)
		swap, ctCount, ctMax, agentRSS = i64(e.SwapUsed), i64(e.ConntrackCount), i64(e.ConntrackMax), i64(e.AgentRSS)
		tcpSocks, nic = int64(e.TCPSockets), int64(e.NICSpeedMbps)
	}
	return Stmt{Query: `
		INSERT INTO node_host_hour (node_id, hour_start, samples, cpu_sum_x100, cpu_max_x100, softirq_max_x100, load1_max_x100,
			ram_used_max, ram_total, rx_bps_sum, rx_bps_max, tx_bps_sum, tx_bps_max, sessions_max, sessions_sum,
			ext_samples, steal_sum_x100, steal_max_x100, iowait_max_x100, swap_used_max, conntrack_max, conntrack_limit,
			tcp_sockets_max, nic_speed_mbps, agent_cpu_sum_x100, agent_cpu_max_x100, agent_rss_max)
		VALUES (?1, ?2, 1, ?3, ?3, ?4, ?5, ?6, ?7, ?8, ?8, ?9, ?9, ?10, ?10,
			?11, ?12, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?19, ?20)
		ON CONFLICT (node_id, hour_start) DO UPDATE SET
			samples = samples + 1, cpu_sum_x100 = cpu_sum_x100 + excluded.cpu_sum_x100,
			cpu_max_x100 = max(cpu_max_x100, excluded.cpu_max_x100),
			softirq_max_x100 = max(softirq_max_x100, excluded.softirq_max_x100),
			load1_max_x100 = max(load1_max_x100, excluded.load1_max_x100),
			ram_used_max = max(ram_used_max, excluded.ram_used_max), ram_total = excluded.ram_total,
			rx_bps_sum = rx_bps_sum + excluded.rx_bps_sum, rx_bps_max = max(rx_bps_max, excluded.rx_bps_max),
			tx_bps_sum = tx_bps_sum + excluded.tx_bps_sum, tx_bps_max = max(tx_bps_max, excluded.tx_bps_max),
			sessions_max = max(sessions_max, excluded.sessions_max), sessions_sum = sessions_sum + excluded.sessions_sum,
			ext_samples = ext_samples + excluded.ext_samples,
			steal_sum_x100 = steal_sum_x100 + excluded.steal_sum_x100, steal_max_x100 = max(steal_max_x100, excluded.steal_max_x100),
			iowait_max_x100 = max(iowait_max_x100, excluded.iowait_max_x100),
			swap_used_max = max(swap_used_max, excluded.swap_used_max), conntrack_max = max(conntrack_max, excluded.conntrack_max),
			conntrack_limit = CASE WHEN excluded.ext_samples > 0 THEN excluded.conntrack_limit ELSE conntrack_limit END,
			tcp_sockets_max = max(tcp_sockets_max, excluded.tcp_sockets_max),
			nic_speed_mbps = CASE WHEN excluded.ext_samples > 0 THEN excluded.nic_speed_mbps ELSE nic_speed_mbps END,
			agent_cpu_sum_x100 = agent_cpu_sum_x100 + excluded.agent_cpu_sum_x100,
			agent_cpu_max_x100 = max(agent_cpu_max_x100, excluded.agent_cpu_max_x100),
			agent_rss_max = max(agent_rss_max, excluded.agent_rss_max)`,
		Args: []any{nodeID, hourStart, cpu, soft, load, i64(s.RAMUsed), i64(s.RAMTotal), rx, tx, sessions,
			ext, steal, iowait, swap, ctCount, ctMax, tcpSocks, nic, agentCPU, agentRSS}}
}

// HostHour is one hour of a node's host load: means and peaks.
type HostHour struct {
	HourStart                           int64
	Samples                             uint32
	CPUAvg, CPUMax, SoftirqMax, LoadMax float64
	RAMUsedMax, RAMTotal                uint64
	RxAvg, RxMax, TxAvg, TxMax          uint64
	SessionsMax                         uint32
	// SessionsAvg is the mean people online over the hour's samples.
	SessionsAvg float64
	// ExtSamples is how many samples carried the resource fields; the fields below are 0 when it is 0 (an agent that
	// predates them) and mean "not known", not "none".
	ExtSamples                    uint32
	StealAvg, StealMax, IOWaitMax float64
	SwapUsedMax                   uint64
	ConntrackMax, ConntrackLimit  uint64
	TCPSocketsMax                 uint32
	NICSpeedMbps                  uint32
	AgentCPUAvg, AgentCPUMax      float64
	AgentRSSMax                   uint64
}

// NodeHostHours reads a node's hourly host load for the last hours (at most HostMaxHours), oldest first.
func (s *Store) NodeHostHours(ctx context.Context, nodeID string, now time.Time, hours int) ([]HostHour, error) {
	hours = min(max(hours, 1), HostMaxHours)
	since := now.UTC().Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour).Unix()
	rows, err := s.R.QueryContext(ctx, `
		SELECT hour_start, samples, cpu_sum_x100, cpu_max_x100, softirq_max_x100, load1_max_x100, ram_used_max, ram_total,
			rx_bps_sum, rx_bps_max, tx_bps_sum, tx_bps_max, sessions_max, sessions_sum,
			ext_samples, steal_sum_x100, steal_max_x100, iowait_max_x100, swap_used_max, conntrack_max, conntrack_limit,
			tcp_sockets_max, nic_speed_mbps, agent_cpu_sum_x100, agent_cpu_max_x100, agent_rss_max
		FROM node_host_hour WHERE node_id = ? AND hour_start >= ? AND samples > 0 ORDER BY hour_start`, nodeID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostHour
	for rows.Next() {
		var h HostHour
		var samples, cpuSum, cpuMax, softMax, loadMax, ramMax, ramTotal, rxSum, rxMax, txSum, txMax, sessions, sessionsSum int64
		var ext, stealSum, stealMax, iowaitMax, swapMax, ctMax, ctLimit, tcpMax, nic, agentSum, agentMax, agentRSS int64
		if err := rows.Scan(&h.HourStart, &samples, &cpuSum, &cpuMax, &softMax, &loadMax, &ramMax, &ramTotal,
			&rxSum, &rxMax, &txSum, &txMax, &sessions, &sessionsSum,
			&ext, &stealSum, &stealMax, &iowaitMax, &swapMax, &ctMax, &ctLimit, &tcpMax, &nic, &agentSum, &agentMax, &agentRSS); err != nil {
			return nil, err
		}
		pos := func(v int64) uint64 { return uint64(max(v, 0)) }
		u32 := func(v int64) uint32 { return uint32(min(max(v, 0), math.MaxUint32)) }
		h.SessionsAvg = float64(max(sessionsSum, 0)) / float64(max(samples, 1))
		if ext > 0 {
			h.ExtSamples = u32(ext)
			h.StealAvg, h.StealMax, h.IOWaitMax = float64(stealSum)/float64(ext)/100, float64(stealMax)/100, float64(iowaitMax)/100
			h.SwapUsedMax, h.ConntrackMax, h.ConntrackLimit = pos(swapMax), pos(ctMax), pos(ctLimit)
			h.TCPSocketsMax, h.NICSpeedMbps = u32(tcpMax), u32(nic)
			h.AgentCPUAvg, h.AgentCPUMax, h.AgentRSSMax = float64(agentSum)/float64(ext)/100, float64(agentMax)/100, pos(agentRSS)
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

// PruneNodeHostHours drops hour rows older than the cut (the hour bucket that touches the cut stays).
func (s *Store) PruneNodeHostHours(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM node_host_hour WHERE hour_start < ?`, hourBucket(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
