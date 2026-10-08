package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func fleetIsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func jsonStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// NodeRow is one node row.
type NodeRow struct {
	ID, Name, Address, CountryCode, Location, Provider, Notes string
	DNSResolvers                                              []string
	LivenessTimeoutS, ApplyTimeoutS, DialTimeoutS             int
	State                                                     string // pending | active | retired
	CertSerial                                                string
	AgentVersion                                              string
	APIVersion                                                int
	BootAt, LastSeenAt, LastConnectedAt, LastDisconnectedAt   time.Time // zero = never
	AgentInstanceID                                           string
	LastSeq                                                   uint64
	DesiredRevision                                           uint64
	DesiredHash                                               string
	AppliedRevision                                           uint64
	AppliedHash                                               string
	CreatedAt                                                 time.Time
	RetiredAt                                                 time.Time
	// From the last Hello: Hello.built, Hello.capabilities and Hello.last_update as JSON
	// ("" = never reported; see LastUpdateRow).
	AgentBuilt     int64
	AgentCaps      []string
	LastUpdateJSON string
	// AwgBackend is the node setting "auto" | "kernel" | "userspace" (migration 00014), sent to agents that list awg/1.
	AwgBackend string
	// AwgPrepareJSON is the state of the automatic kernel-module build (migration 00019, see AwgPrepareRow); "" = never asked.
	AwgPrepareJSON string
	// TorrentBlockerEnabled is the per-node recognized BitTorrent traffic setting (migration 00036).
	TorrentBlockerEnabled bool
	// ClientIPv6 is the per-node "IPv6 for clients" switch (migration 00046); true (the default, what every node did
	// before) lets clients leave over IPv6.
	ClientIPv6 bool
	// BandwidthMbps is the optional symmetric network capacity used to report node utilization (migration 00039).
	BandwidthMbps int
}

const nodeCols = `id, name, address, country_code, location, provider, notes, dns_resolvers,
	liveness_timeout_s, apply_timeout_s, dial_timeout_s, state, cert_serial, agent_version, api_version,
	boot_at, last_seen_at, last_connected_at, last_disconnected_at, agent_instance_id, last_seq,
	desired_revision, desired_hash, applied_revision, applied_hash, created_at, retired_at,
	agent_built, agent_caps, last_update_json, awg_backend, awg_prepare_json, torrent_blocker_enabled, bandwidth_mbps, client_ipv6`

type rowScanner interface{ Scan(dest ...any) error }

func scanNode(r rowScanner) (NodeRow, error) {
	var n NodeRow
	var dns string
	var serial sql.NullString
	var boot, seen, conn, disc, created int64
	var retired sql.NullInt64
	var lastSeq, desRev, appRev int64
	var caps string
	err := r.Scan(&n.ID, &n.Name, &n.Address, &n.CountryCode, &n.Location, &n.Provider, &n.Notes, &dns,
		&n.LivenessTimeoutS, &n.ApplyTimeoutS, &n.DialTimeoutS, &n.State, &serial, &n.AgentVersion, &n.APIVersion,
		&boot, &seen, &conn, &disc, &n.AgentInstanceID, &lastSeq,
		&desRev, &n.DesiredHash, &appRev, &n.AppliedHash, &created, &retired,
		&n.AgentBuilt, &caps, &n.LastUpdateJSON, &n.AwgBackend, &n.AwgPrepareJSON, &n.TorrentBlockerEnabled, &n.BandwidthMbps, &n.ClientIPv6)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeRow{}, ErrNotFound
	}
	if err != nil {
		return NodeRow{}, err
	}
	_ = json.Unmarshal([]byte(dns), &n.DNSResolvers)
	n.CertSerial = serial.String
	n.BootAt, n.LastSeenAt, n.LastConnectedAt, n.LastDisconnectedAt = fleetTime(boot), fleetTime(seen), fleetTime(conn), fleetTime(disc)
	n.LastSeq, n.DesiredRevision, n.AppliedRevision = uint64(lastSeq), uint64(desRev), uint64(appRev)
	n.CreatedAt = fleetTime(created)
	n.RetiredAt = fleetTime(retired.Int64)
	n.AgentCaps = strings.Fields(caps)
	return n, nil
}

// Node returns one node or ErrNotFound.
func (s *Store) Node(ctx context.Context, id string) (NodeRow, error) {
	return scanNode(s.R.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM node WHERE id = ?`, id))
}

// NodeWithSentDigest returns one node and its last-sent digest in one read batch.
func (s *Store) NodeWithSentDigest(ctx context.Context, id string) (NodeRow, []byte, error) {
	var node NodeRow
	var digest string
	r := reads{}
	r.add(oneRow(&node, scanNode), `SELECT `+nodeCols+` FROM node WHERE id = ?`, id)
	r.add(maybeOneRow(&digest, scanString), `SELECT digest FROM node_sent WHERE node_id = ?`, id)
	if err := r.run(ctx, s); err != nil {
		return NodeRow{}, nil, err
	}
	return node, []byte(digest), nil
}

// Nodes lists nodes by name; retired ones only when asked for.
func (s *Store) Nodes(ctx context.Context, includeRetired bool) ([]NodeRow, error) {
	q := `SELECT ` + nodeCols + ` FROM node`
	if !includeRetired {
		q += ` WHERE state <> 'retired'`
	}
	rows, err := s.R.QueryContext(ctx, q+` ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NodePatch updates the admin-editable fields; nil = unchanged.
type NodePatch struct {
	Name, Address, CountryCode, Location, Provider, Notes *string
	DNSResolvers                                          *[]string
	LivenessTimeoutS, ApplyTimeoutS, DialTimeoutS         *int
	AwgBackend                                            *string // "auto" | "kernel" | "userspace"
	TorrentBlockerEnabled                                 *bool
	BandwidthMbps                                         *int
	ClientIPv6                                            *bool
}

// UpdateNode applies the patch and returns the new row. ErrConflict on a name clash.
func (s *Store) UpdateNode(ctx context.Context, id string, p NodePatch) (NodeRow, error) {
	var set []string
	var args []any
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	for _, f := range []struct {
		col string
		v   *string
	}{{"name", p.Name}, {"address", p.Address}, {"country_code", p.CountryCode}, {"location", p.Location},
		{"provider", p.Provider}, {"notes", p.Notes}, {"awg_backend", p.AwgBackend}} {
		if f.v != nil {
			add(f.col, *f.v)
		}
	}
	if p.TorrentBlockerEnabled != nil {
		add("torrent_blocker_enabled", *p.TorrentBlockerEnabled)
	}
	if p.BandwidthMbps != nil {
		add("bandwidth_mbps", *p.BandwidthMbps)
	}
	if p.ClientIPv6 != nil {
		add("client_ipv6", *p.ClientIPv6)
	}
	if p.AwgBackend != nil {
		// A backend chosen by hand ends the wish of an unfinished kernel-module build: it must not switch the node later.
		set = append(set, `awg_prepare_json = CASE WHEN awg_prepare_json = '' THEN '' ELSE json_remove(awg_prepare_json, '$.want') END`)
	}
	if p.DNSResolvers != nil {
		add("dns_resolvers", jsonStrings(*p.DNSResolvers))
	}
	for _, f := range []struct {
		col string
		v   *int
	}{{"liveness_timeout_s", p.LivenessTimeoutS}, {"apply_timeout_s", p.ApplyTimeoutS}, {"dial_timeout_s", p.DialTimeoutS}} {
		if f.v != nil {
			add(f.col, *f.v)
		}
	}
	if len(set) > 0 {
		res, err := s.W.ExecContext(ctx, `UPDATE node SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, id)...)
		if err != nil {
			if fleetIsUnique(err) {
				return NodeRow{}, ErrConflict
			}
			return NodeRow{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NodeRow{}, ErrNotFound
		}
	}
	return s.Node(ctx, id)
}

// SetBandwidthIfUnset stores a measured capacity only while the field is still 0 (never measured, never typed): the one
// automatic write of a value, and never over what an admin entered, even if they did it while the measurement ran.
// It reports whether the value was stored.
func (s *Store) SetBandwidthIfUnset(ctx context.Context, id string, mbps int) (bool, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE node SET bandwidth_mbps = ? WHERE id = ? AND bandwidth_mbps = 0 AND state <> 'retired'`, mbps, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// NodeFactsRow is the host facts block reported in Hello.
type NodeFactsRow struct {
	Hostname, OS, Kernel, Arch string
	CPUCount                   uint32
	RAMTotal, DiskTotal        uint64
	Virt                       string
	HasIPv6                    bool
	Engines                    []EngineRow
}

// EngineRow is one engine a node runs.
type EngineRow struct{ Protocol, Version string }

// HelloInfo is what a Hello tells the panel.
type HelloInfo struct {
	AgentVersion string
	APIVersion   uint32
	Instance     string
	BootAt       time.Time
	Facts        NodeFactsRow
	// Built is Hello.built, Caps Hello.capabilities. LastUpdateJSON is Hello.last_update as JSON (LastUpdateRow);
	// "" = the Hello carried none, which keeps what is stored (the agent reports an outcome once).
	Built          int64
	Caps           []string
	LastUpdateJSON string
}

// NodeHello records a (re)connect: the node becomes active, facts are stored, and the reliable-message
// counter is reset when the agent instance changed. prev is the row before the update (for blip/restart
// detection); ackedSeq is the highest committed seq of this instance. ErrNodeRetired for retired nodes.
func (s *Store) NodeHello(ctx context.Context, id string, session uint64, h HelloInfo, now time.Time) (prev NodeRow, ackedSeq uint64, err error) {
	sessionID, err := nodeSessionValue(session)
	if err != nil {
		return NodeRow{}, 0, err
	}
	engines := make([]map[string]string, len(h.Facts.Engines))
	for i, e := range h.Facts.Engines {
		engines[i] = map[string]string{"protocol": e.Protocol, "version": e.Version}
	}
	eb, _ := json.Marshal(engines)
	f := h.Facts
	results, err := s.batch(ctx,
		Stmt{Query: `SELECT ` + nodeCols + ` FROM node WHERE id = ?`, Args: []any{id}, Returning: true},
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, id),
		Stmt{Query: `
		UPDATE node SET state = 'active', agent_version = ?, api_version = ?, boot_at = CASE WHEN ? > 0 THEN ? ELSE boot_at END,
			last_seen_at = ?, last_connected_at = ?, agent_instance_id = ?,
			last_seq = CASE WHEN agent_instance_id = ? THEN last_seq ELSE 0 END,
			agent_built = ?, agent_caps = ?, last_update_json = CASE WHEN ? <> '' THEN ? ELSE last_update_json END
		WHERE id = ?`,
			Args: []any{h.AgentVersion, h.APIVersion, fleetUnix(h.BootAt), fleetUnix(h.BootAt), unix(now), unix(now), h.Instance, h.Instance,
				max(h.Built, 0), strings.Join(h.Caps, " "), h.LastUpdateJSON, h.LastUpdateJSON, id}},
		Stmt{Query: `
		INSERT INTO node_facts (node_id, hostname, os, kernel, arch, cpu_count, ram_total_bytes, disk_total_bytes, virt, has_ipv6, engines_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (node_id) DO UPDATE SET hostname = excluded.hostname, os = excluded.os, kernel = excluded.kernel,
			arch = excluded.arch, cpu_count = excluded.cpu_count, ram_total_bytes = excluded.ram_total_bytes,
			disk_total_bytes = excluded.disk_total_bytes, virt = excluded.virt, has_ipv6 = excluded.has_ipv6,
			engines_json = excluded.engines_json, updated_at = excluded.updated_at`,
			Args: []any{id, f.Hostname, f.OS, f.Kernel, f.Arch, f.CPUCount, int64(f.RAMTotal), int64(f.DiskTotal), f.Virt, f.HasIPv6, string(eb), unix(now)}},
		// An older session's Hello cannot land after a newer one: the VPS has one writer and claimOwner cancels the
		// old session's context; the edge serialises with blockConcurrencyWhile.
		Stmt{Query: `INSERT OR REPLACE INTO node_live (node_id, session) VALUES (?, ?)`, Args: []any{id, sessionID}},
	)
	if errors.Is(err, errGuard) {
		current, readErr := s.Node(ctx, id)
		if errors.Is(readErr, ErrNotFound) {
			return NodeRow{}, 0, ErrNotFound
		}
		if readErr != nil {
			return NodeRow{}, 0, readErr
		}
		if current.State == "retired" {
			return NodeRow{}, 0, ErrNodeRetired
		}
		return NodeRow{}, 0, ErrConflict
	}
	if err != nil {
		return NodeRow{}, 0, err
	}
	prev, err = scanNode(batchRow(results[0].Rows[0]))
	if err != nil {
		return NodeRow{}, 0, err
	}
	if prev.AgentInstanceID == h.Instance {
		ackedSeq = prev.LastSeq
	}
	return prev, ackedSeq, nil
}

// NodeFacts returns the stored host facts (zero value if none yet).
func (s *Store) NodeFacts(ctx context.Context, id string) (NodeFactsRow, error) {
	var f NodeFactsRow
	var ram, disk int64
	var eng string
	err := s.R.QueryRowContext(ctx, `
		SELECT hostname, os, kernel, arch, cpu_count, ram_total_bytes, disk_total_bytes, virt, has_ipv6, engines_json
		FROM node_facts WHERE node_id = ?`, id).Scan(&f.Hostname, &f.OS, &f.Kernel, &f.Arch, &f.CPUCount, &ram, &disk, &f.Virt, &f.HasIPv6, &eng)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeFactsRow{}, nil
	}
	if err != nil {
		return NodeFactsRow{}, err
	}
	f.RAMTotal, f.DiskTotal = uint64(ram), uint64(disk)
	var es []struct{ Protocol, Version string }
	_ = json.Unmarshal([]byte(eng), &es)
	for _, e := range es {
		f.Engines = append(f.Engines, EngineRow{e.Protocol, e.Version})
	}
	return f, nil
}

// NodeDisconnected records the end of the session that still owns the live row.
func (s *Store) NodeDisconnected(ctx context.Context, id string, session uint64, seen, now time.Time) error {
	sessionID, err := nodeSessionValue(session)
	if err != nil {
		return err
	}
	_, err = s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM node_live WHERE node_id = ? AND session = ?)`, id, sessionID),
		Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?), last_disconnected_at = ? WHERE id = ?`,
			Args: []any{unix(seen), unix(now), id}},
		Stmt{Query: `DELETE FROM node_live WHERE node_id = ? AND session = ?`, Args: []any{id, sessionID}},
	)
	if errors.Is(err, errGuard) {
		return nil
	}
	return err
}

// NodeDesired stores the desired revision and hash (kept across panel restarts so revisions stay monotonic: an older
// revision leaves the node row alone) and, in the same batch, the digest of what was sent, which carries its own revision.
func (s *Store) NodeDesired(ctx context.Context, id string, rev uint64, hash string, digest []byte) error {
	_, err := s.batch(ctx,
		Stmt{Query: `UPDATE node SET desired_revision = ?, desired_hash = ? WHERE id = ? AND desired_revision <= ?`,
			Args: []any{int64(rev), hash, id, int64(rev)}},
		Stmt{Query: `INSERT INTO node_sent (node_id, digest) VALUES (?, ?) ON CONFLICT(node_id) DO UPDATE SET digest = excluded.digest`,
			Args: []any{id, string(digest)}},
	)
	return err
}

// InboundApplied is one InboundResult as stored on the inbound row.
type InboundApplied struct {
	ID, State, Error, SpecHash, CertPin string
	CertNotAfter                        time.Time
}

// NodeApplied records an ApplyResult: the node's applied revision/hash and per-inbound outcome.
func (s *Store) NodeApplied(ctx context.Context, id string, session uint64, drift bool, rev uint64, hash string, in []InboundApplied, now time.Time) error {
	sessionID, err := nodeSessionValue(session)
	if err != nil {
		return err
	}
	stmts := []Stmt{
		{Query: `UPDATE node SET applied_revision = ?, applied_hash = ? WHERE id = ?`, Args: []any{int64(rev), hash, id}},
		{Query: `UPDATE node_live SET drift = ? WHERE node_id = ? AND session = ?`, Args: []any{boolInt(drift), id, sessionID}},
	}
	for _, r := range in {
		stmts = append(stmts, Stmt{Query: `
			UPDATE inbound SET state = ?, last_error = ?, applied_spec_hash = ?, cert_pin_sha256 = ?, cert_not_after = ?, updated_at = ?
			WHERE id = ? AND node_id = ? AND enabled = 1`, Args: []any{r.State, r.Error, r.SpecHash, r.CertPin, fleetUnix(r.CertNotAfter), unix(now), r.ID, id}})
	}
	_, err = s.batch(ctx, stmts...)
	return err
}

// SetInboundCert stores the certificate an inbound serves as the node's stats report it (an ACME certificate appears and
// renews after the ApplyResult was sent). Only an enabled inbound of this node, and only when the value differs.
func (s *Store) SetInboundCert(ctx context.Context, nodeID, inboundID, pin string, notAfter, now time.Time) error {
	stmt := inboundCertStmt(nodeID, inboundID, pin, notAfter, now)
	_, err := s.W.ExecContext(ctx, stmt.Query, stmt.Args...)
	return err
}

func inboundCertStmt(nodeID, inboundID, pin string, notAfter, now time.Time) Stmt {
	return Stmt{Query: `
		UPDATE inbound SET cert_pin_sha256 = ?1, cert_not_after = ?2, updated_at = ?3
		WHERE id = ?4 AND node_id = ?5 AND enabled = 1 AND (cert_pin_sha256 != ?1 OR cert_not_after != ?2)`,
		Args: []any{pin, fleetUnix(notAfter), unix(now), inboundID, nodeID}}
}

// RetireNode marks the node retired, revokes every certificate and kills unused enrollment tokens.
// The row, its traffic and its events stay, and so does its saved server access: the sealed password may be the only
// copy (a generated one), so only the owner's ForgetNodeServerAccess deletes it. ErrNotFound / ErrNodeRetired as appropriate.
func (s *Store) RetireNode(ctx context.Context, id string, now time.Time) error {
	_, err := s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, id),
		Stmt{Query: `UPDATE node SET state = 'retired', retired_at = ?, desired_hash = '' WHERE id = ?`, Args: []any{unix(now), id}},
		Stmt{Query: `UPDATE node_cert SET revoked_at = ?, revoke_reason = 'retired' WHERE node_id = ? AND (revoked_at IS NULL OR revoked_at > ?)`, Args: []any{unix(now), id, unix(now)}},
		Stmt{Query: `UPDATE enrollment_token SET expires_at = ? WHERE node_id = ? AND used_at IS NULL AND expires_at > ?`, Args: []any{unix(now), id, unix(now)}},
		// An install still running for the node is cancelled; it may already have changed the host, so the remote-outcome
		// warning stays while its saved credentials go.
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, 'cancelled', 'remote_outcome_unknown', ? FROM node_provision_job
			WHERE node_id = ? AND state IN ('queued', 'running', 'cancel_requested')`, Args: []any{unix(now), id}},
		Stmt{Query: `UPDATE node_provision_job
			SET state = 'cancelled', phase = 'cancelled', error_code = 'remote_outcome_unknown', secret = X'', updated_at = ?
			WHERE node_id = ? AND state IN ('queued', 'running', 'cancel_requested')`, Args: []any{unix(now), id}},
		// A retired node takes no inbounds any more: its parked AWG server keys are dead weight.
		Stmt{Query: `DELETE FROM awg_retained_key WHERE node_id = ?`, Args: []any{id}},
	)
	if errors.Is(err, errGuard) {
		current, readErr := s.Node(ctx, id)
		if errors.Is(readErr, ErrNotFound) {
			return ErrNotFound
		}
		if readErr != nil {
			return readErr
		}
		if current.State == "retired" {
			return ErrNodeRetired
		}
		return ErrConflict
	}
	return err
}
