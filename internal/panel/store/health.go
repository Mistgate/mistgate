package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Queries of the health module (internal/panel/health): alerts, doctor results, the probe credential,
// synthetic check samples and the retention sweeps. Tables: migration 00012.

func jsonMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func parseMap(s string) map[string]string {
	var m map[string]string
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// ---------------------------------------------------------------------------------------------------
// Alerts

// HealthAlert is one health_alert row. Kind is the lower-case name of the alert kind ("no_traffic").
type HealthAlert struct {
	ID, Kind            string
	Severity            int // 1 info, 2 warning, 3 critical
	NodeID, Subject     string
	Params              map[string]string
	TitleKey, WhyKey    string
	FirstSeen, LastSeen time.Time
	ResolvedAt          time.Time // zero = active
	Resolution          string
	MutedUntil          time.Time // zero = not muted
	CreatedAt           time.Time
}

const alertCols = `id, kind, severity, node_id, subject, params_json, title_key, why_key, first_seen, last_seen,
	resolved_at, resolution, muted_until, created_at`

func scanAlert(r rowScanner) (HealthAlert, error) {
	var a HealthAlert
	var params string
	var first, last, res, mute, created int64
	err := r.Scan(&a.ID, &a.Kind, &a.Severity, &a.NodeID, &a.Subject, &params, &a.TitleKey, &a.WhyKey,
		&first, &last, &res, &a.Resolution, &mute, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return HealthAlert{}, ErrNotFound
	}
	if err != nil {
		return HealthAlert{}, err
	}
	a.Params = parseMap(params)
	a.FirstSeen, a.LastSeen, a.ResolvedAt, a.MutedUntil, a.CreatedAt = fleetTime(first), fleetTime(last), fleetTime(res), fleetTime(mute), fleetTime(created)
	return a, nil
}

func (s *Store) alerts(ctx context.Context, where string, args ...any) ([]HealthAlert, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+alertCols+` FROM health_alert WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HealthAlert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// HealthAlert returns one alert or ErrNotFound.
func (s *Store) HealthAlert(ctx context.Context, id string) (HealthAlert, error) {
	return scanAlert(s.R.QueryRowContext(ctx, `SELECT `+alertCols+` FROM health_alert WHERE id = ?`, id))
}

// ActiveAlerts returns every unresolved alert.
func (s *Store) ActiveAlerts(ctx context.Context) ([]HealthAlert, error) {
	return s.alerts(ctx, `resolved_at = 0 ORDER BY first_seen DESC, id`)
}

// AlertHistory returns alerts resolved since `since`, newest resolution first; nodeID "" = all.
func (s *Store) AlertHistory(ctx context.Context, since time.Time, nodeID string, limit int) ([]HealthAlert, error) {
	return s.alerts(ctx, `resolved_at >= ? AND resolved_at > 0 AND (? = '' OR node_id = ?) ORDER BY resolved_at DESC, id LIMIT ?`,
		unix(since), nodeID, nodeID, limit)
}

// OpenAlert records that the condition of a holds. A resolved alert of the same key that ended less than
// reopenWithin ago is re-opened (id and first_seen stay, so flapping does not fill the history); otherwise a
// row is inserted. The active-alert unique index makes concurrent opens update the same row instead of duplicating it.
func (s *Store) OpenAlert(ctx context.Context, a HealthAlert, reopenWithin time.Duration, now time.Time) (out HealthAlert, reopened bool, err error) {
	a.ID, a.FirstSeen, a.CreatedAt = NewID("alt_"), now, now
	results, err := s.batch(ctx,
		Stmt{Query: `UPDATE health_alert SET resolved_at = 0, resolution = '', severity = ?, params_json = ?,
			title_key = ?, why_key = ?, last_seen = ?
			WHERE id = (SELECT id FROM health_alert WHERE kind = ? AND node_id = ? AND subject = ?
				AND resolved_at >= ? AND resolved_at > 0 ORDER BY resolved_at DESC LIMIT 1)
			AND NOT EXISTS (SELECT 1 FROM health_alert WHERE kind = ? AND node_id = ? AND subject = ? AND resolved_at = 0)
			RETURNING ` + alertCols,
			Args: []any{int64(a.Severity), jsonMap(a.Params), a.TitleKey, a.WhyKey, unix(now),
				a.Kind, a.NodeID, a.Subject, unix(now.Add(-reopenWithin)), a.Kind, a.NodeID, a.Subject}, Returning: true},
		Stmt{Query: `INSERT INTO health_alert (` + alertCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', 0, ?)
			ON CONFLICT (kind, node_id, subject) WHERE resolved_at = 0 DO UPDATE SET
				resolved_at = 0, resolution = '', severity = excluded.severity, params_json = excluded.params_json,
				title_key = excluded.title_key, why_key = excluded.why_key, last_seen = excluded.last_seen
			RETURNING ` + alertCols,
			Args: []any{a.ID, a.Kind, int64(a.Severity), a.NodeID, a.Subject, jsonMap(a.Params), a.TitleKey, a.WhyKey,
				unix(now), unix(now), unix(now)}, Returning: true},
	)
	if err != nil {
		return HealthAlert{}, false, err
	}
	if len(results[1].Rows) != 1 {
		return HealthAlert{}, false, errors.New("store: alert upsert returned no row")
	}
	out, err = scanAlert(batchRow(results[1].Rows[0]))
	if err != nil {
		return HealthAlert{}, false, err
	}
	return out, results[0].RowsAffected == 1, nil
}

// TouchAlert records that the condition of an active alert still holds and refreshes what may change with
// the diagnosis (severity, texts, params). A mute ends when the severity rises: it was muted at the milder level.
func (s *Store) TouchAlert(ctx context.Context, a HealthAlert, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE health_alert SET last_seen = ?, muted_until = CASE WHEN ? > severity THEN 0 ELSE muted_until END,
		severity = ?, title_key = ?, why_key = ?, params_json = ?
		WHERE id = ? AND resolved_at = 0`, unix(now), a.Severity, a.Severity, a.TitleKey, a.WhyKey, jsonMap(a.Params), a.ID)
	return err
}

// ResolveAlert ends an active alert. It reports false when the alert was not active any more.
func (s *Store) ResolveAlert(ctx context.Context, id, resolution string, now time.Time) (bool, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE health_alert SET resolved_at = ?, resolution = ? WHERE id = ? AND resolved_at = 0`,
		max(unix(now), 1), resolution, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// InsertResolvedAlert writes a history-only record (HOST_BLIP): an alert that never was active.
func (s *Store) InsertResolvedAlert(ctx context.Context, a HealthAlert) error {
	a.ID = NewID("alt_")
	_, err := s.W.ExecContext(ctx, `INSERT INTO health_alert (`+alertCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		a.ID, a.Kind, a.Severity, a.NodeID, a.Subject, jsonMap(a.Params), a.TitleKey, a.WhyKey,
		unix(a.FirstSeen), unix(a.LastSeen), max(unix(a.ResolvedAt), 1), a.Resolution, unix(a.CreatedAt))
	return err
}

// MuteAlert sets (or, with a zero until, clears) the mute of an active alert and returns it. ErrNotFound when
// the alert does not exist, ErrConflict when it is already resolved.
func (s *Store) MuteAlert(ctx context.Context, id string, until time.Time) (HealthAlert, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE health_alert SET muted_until = ? WHERE id = ? AND resolved_at = 0`, fleetUnix(until), id)
	if err != nil {
		return HealthAlert{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.HealthAlert(ctx, id); err != nil {
			return HealthAlert{}, err
		}
		return HealthAlert{}, ErrConflict
	}
	return s.HealthAlert(ctx, id)
}

// AlertCounts counts the active alerts that feed the badge: not muted, severity warning or critical.
func (s *Store) AlertCounts(ctx context.Context, now time.Time) (active, critical int, err error) {
	err = s.R.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(severity = 3), 0) FROM health_alert
		WHERE resolved_at = 0 AND severity >= 2 AND muted_until <= ?`, unix(now)).Scan(&active, &critical)
	return active, critical, err
}

// ---------------------------------------------------------------------------------------------------
// Doctor

// DoctorRow is one stored DoctorResult.
type DoctorRow struct {
	NodeID, CheckID  string
	Status           int // agent.proto DoctorStatus: 1 ok, 2 warn, 3 fail, 4 skip
	TitleKey, Detail string
	DetailCode       string // agent.proto DoctorResult.detail_code; "" from an agent that predates it
	Params           map[string]string
	FixID            string
	Measured         time.Time
	Received         time.Time
}

// PutDoctor stores results of one node: replace drops the node's other rows first (a full report), otherwise
// the rows are merged by check id (a partial one). Received is set on every row.
func (s *Store) PutDoctor(ctx context.Context, nodeID string, rows []DoctorRow, replace bool, now time.Time) error {
	stmts := make([]Stmt, 0, len(rows)+1)
	if replace {
		stmts = append(stmts, Stmt{Query: `DELETE FROM doctor_result WHERE node_id = ?`, Args: []any{nodeID}})
	}
	for _, r := range rows {
		stmts = append(stmts, Stmt{Query: `INSERT INTO doctor_result (node_id, check_id, status, title_key, detail, detail_code, params_json, fix_id, measured_unix, received_unix)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node_id, check_id) DO UPDATE SET status = excluded.status, title_key = excluded.title_key, detail = excluded.detail,
				detail_code = excluded.detail_code, params_json = excluded.params_json, fix_id = excluded.fix_id, measured_unix = excluded.measured_unix,
				received_unix = excluded.received_unix`,
			Args: []any{nodeID, r.CheckID, int64(r.Status), r.TitleKey, r.Detail, r.DetailCode, jsonMap(r.Params), r.FixID, fleetUnix(r.Measured), unix(now)}})
	}
	if len(stmts) == 0 {
		return nil
	}
	_, err := s.batch(ctx, stmts...)
	return err
}

// DoctorResults returns the stored results of one node, or of every node for "" (ordered by node, check).
func (s *Store) DoctorResults(ctx context.Context, nodeID string) ([]DoctorRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node_id, check_id, status, title_key, detail, detail_code, params_json, fix_id, measured_unix, received_unix
		FROM doctor_result WHERE (? = '' OR node_id = ?) ORDER BY node_id, check_id`, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DoctorRow
	for rows.Next() {
		var r DoctorRow
		var params string
		var meas, recv int64
		if err := rows.Scan(&r.NodeID, &r.CheckID, &r.Status, &r.TitleKey, &r.Detail, &r.DetailCode, &params, &r.FixID, &meas, &recv); err != nil {
			return nil, err
		}
		r.Params, r.Measured, r.Received = parseMap(params), fleetTime(meas), fleetTime(recv)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------------------------------
// Probe credential

// ProbeCredRow is the system credential of one inbound. AWGIdx is the peer index of the credential's tunnel address
// in the profile's client network (migration 00020); 0 for a protocol without an address (hysteria2).
type ProbeCredRow struct {
	InboundID, CredID string
	SecretEnc         []byte
	DataJSON          string
	AWGIdx            int
}

// ProbeCred returns the credential of an inbound or ErrNotFound.
func (s *Store) ProbeCred(ctx context.Context, inboundID string) (ProbeCredRow, error) {
	var r ProbeCredRow
	err := s.R.QueryRowContext(ctx, `SELECT inbound_id, cred_id, secret_enc, data_json, awg_idx FROM health_probe_cred WHERE inbound_id = ?`, inboundID).
		Scan(&r.InboundID, &r.CredID, &r.SecretEnc, &r.DataJSON, &r.AWGIdx)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeCredRow{}, ErrNotFound
	}
	return r, err
}

// ProbeCredIssue makes the credential of an inbound once its peer index is known: it fills CredID, SecretEnc and
// DataJSON (the store sets InboundID and AWGIdx). It runs once per attempt between the candidate read and guarded write,
// so it must not access the database. Treat its result as provisional: do not keep it; only credentials returned by the
// store after commit or read back from the store are real.
type ProbeCredIssue func(idx int) (ProbeCredRow, error)

// InsertProbeCredIdx stores the credential of an inbound whose credential holds a tunnel address (AWG): the peer index
// comes from the profile's allocator, the same one the devices use. A guarded batch protects the index and credential
// together, so a device added at the same moment cannot get the same address. An inbound that has a credential already
// keeps it (two callers racing: the first wins, both then read the same row back with ProbeCred). ErrNotFound when the
// inbound does not exist, ErrAccessSubnetFull when the network has no free index.
func (s *Store) InsertProbeCredIdx(ctx context.Context, inboundID, profileID string, maxIdx int, now time.Time, issue ProbeCredIssue) error {
	cutoff := unix(now.Add(-awgQuarantine))
	_, _, err := s.retryGuarded(ctx, func() ([]Stmt, error) {
		var one int
		switch err := s.R.QueryRowContext(ctx, `SELECT 1 FROM health_probe_cred WHERE inbound_id = ?`, inboundID).Scan(&one); {
		case err == nil:
			return nil, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}

		var storedProfile string
		err := s.R.QueryRowContext(ctx, `SELECT profile_id FROM inbound WHERE id = ?`, inboundID).Scan(&storedProfile)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if storedProfile != profileID {
			return nil, ErrNotFound
		}
		idx, err := s.Access().awgAllocIdxRead(ctx, profileID, maxIdx, cutoff)
		if err != nil {
			return nil, err
		}
		r, err := issue(idx)
		if err != nil {
			return nil, err
		}
		r.InboundID, r.AWGIdx = inboundID, idx
		stmts := []Stmt{
			guard(`
				EXISTS (SELECT 1 FROM inbound WHERE id = ?4 AND profile_id = ?1)
				AND NOT EXISTS (SELECT 1 FROM health_probe_cred WHERE inbound_id = ?4)
				AND ?3 NOT IN (`+awgTakenSQL+`)`,
				profileID, cutoff, int64(idx), inboundID),
			awgPurgeExpiredStmt(profileID, cutoff),
			{Query: `INSERT INTO health_probe_cred (inbound_id, cred_id, secret_enc, data_json, created_at, awg_idx) VALUES (?, ?, ?, ?, ?, ?)`,
				Args: []any{r.InboundID, r.CredID, r.SecretEnc, r.DataJSON, unix(now), int64(r.AWGIdx)}},
		}
		return stmts, nil
	})
	if accIsFK(err) {
		return ErrNotFound
	}
	return err
}

// InsertProbeCred stores a credential unless the inbound already has one (two callers racing: the first wins,
// both then read the same row back with ProbeCred). ErrNotFound when the inbound does not exist.
func (s *Store) InsertProbeCred(ctx context.Context, r ProbeCredRow, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT OR IGNORE INTO health_probe_cred (inbound_id, cred_id, secret_enc, data_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		r.InboundID, r.CredID, r.SecretEnc, r.DataJSON, unix(now))
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------------------------------------------
// Check samples

// CheckSample is one finished round of the synthetic checker. Status: 1 ok, 2 degraded, 3 failed.
type CheckSample struct {
	InboundID                                   string
	At                                          time.Time
	Status                                      int
	LatencyMS                                   uint32
	ExitIP, ExitCountry, ErrorCode, ErrorDetail string
}

// InsertSample stores a round. A second round of the same inbound in the same second replaces the first.
func (s *Store) InsertSample(ctx context.Context, c CheckSample) error {
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO health_check_sample (inbound_id, at, status, latency_ms, exit_ip, exit_country, error_code, error_detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.InboundID, unix(c.At), c.Status, c.LatencyMS, c.ExitIP, c.ExitCountry, c.ErrorCode, c.ErrorDetail)
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return ErrNotFound // the inbound was deleted while the round ran
	}
	return err
}

// SamplesSince returns every stored round at or after since, oldest first, per inbound.
func (s *Store) SamplesSince(ctx context.Context, since time.Time) ([]CheckSample, error) {
	return s.samples(ctx, `SELECT inbound_id, at, status, latency_ms, exit_ip, exit_country, error_code, error_detail
		FROM health_check_sample WHERE at >= ? ORDER BY inbound_id, at`, unix(since))
}

// RecentSamples returns the newest n rounds of an inbound, newest first (to rebuild the fail streak after a restart).
func (s *Store) RecentSamples(ctx context.Context, inboundID string, n int) ([]CheckSample, error) {
	return s.samples(ctx, `SELECT inbound_id, at, status, latency_ms, exit_ip, exit_country, error_code, error_detail
		FROM health_check_sample WHERE inbound_id = ? ORDER BY at DESC LIMIT ?`, inboundID, n)
}

func (s *Store) samples(ctx context.Context, q string, args ...any) ([]CheckSample, error) {
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckSample
	for rows.Next() {
		var c CheckSample
		var at, lat int64
		if err := rows.Scan(&c.InboundID, &at, &c.Status, &lat, &c.ExitIP, &c.ExitCountry, &c.ErrorCode, &c.ErrorDetail); err != nil {
			return nil, err
		}
		c.At, c.LatencyMS = fromUnix(at), uint32(max(lat, 0))
		out = append(out, c)
	}
	return out, rows.Err()
}

// DailyRow is one health_check_daily row.
type DailyRow struct {
	InboundID            string
	Day                  time.Time // 00:00 UTC
	OK, Failed, Degraded int
	P50MS, P95MS         uint32
}

// DailyRows returns the aggregates of an inbound (all when inboundID is empty), oldest day first.
func (s *Store) DailyRows(ctx context.Context, inboundID string) ([]DailyRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT inbound_id, day, ok, failed, degraded, p50_ms, p95_ms FROM health_check_daily
		WHERE (? = '' OR inbound_id = ?) ORDER BY day, inbound_id`, inboundID, inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyRow
	for rows.Next() {
		var r DailyRow
		var day, p50, p95 int64
		if err := rows.Scan(&r.InboundID, &day, &r.OK, &r.Failed, &r.Degraded, &p50, &p95); err != nil {
			return nil, err
		}
		r.Day, r.P50MS, r.P95MS = fromUnix(day), uint32(p50), uint32(p95)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RollupDaily aggregates the stored rounds of every UTC day that has ended before now into health_check_daily.
// A day that already has a row is left alone, so it runs safely every hour. The raw rounds live 25 h, which
// covers a whole finished day for an hour after midnight.
// A panel that was down for more than an hour after midnight loses that day's aggregate for the
// hours already pruned (it is computed from what is left); nothing reads the aggregates yet.
func (s *Store) RollupDaily(ctx context.Context, now time.Time) error {
	today := unix(now) - unix(now)%86400
	_, err := s.batch(ctx, Stmt{Query: `WITH daily_sample AS (
			SELECT inbound_id, at - (at % 86400) AS day, status, latency_ms
			FROM health_check_sample WHERE at < ?
		), counts AS (
			SELECT inbound_id, day,
				SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END) AS ok,
				SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END) AS degraded,
				SUM(CASE WHEN status NOT IN (1, 2) THEN 1 ELSE 0 END) AS failed
			FROM daily_sample GROUP BY inbound_id, day
		), ranked_latency AS (
			SELECT inbound_id, day, latency_ms,
				ROW_NUMBER() OVER (PARTITION BY inbound_id, day ORDER BY latency_ms) AS rank,
				COUNT(*) OVER (PARTITION BY inbound_id, day) AS total
			FROM daily_sample WHERE status != 3 AND latency_ms > 0
		), percentiles AS (
			SELECT inbound_id, day,
				MAX(CASE WHEN rank = (total * 50 + 99) / 100 THEN latency_ms ELSE 0 END) AS p50_ms,
				MAX(CASE WHEN rank = (total * 95 + 99) / 100 THEN latency_ms ELSE 0 END) AS p95_ms
			FROM ranked_latency GROUP BY inbound_id, day
		)
		INSERT OR IGNORE INTO health_check_daily (inbound_id, day, ok, failed, degraded, p50_ms, p95_ms)
		SELECT c.inbound_id, c.day, c.ok, c.failed, c.degraded, COALESCE(p.p50_ms, 0), COALESCE(p.p95_ms, 0)
		FROM counts c LEFT JOIN percentiles p ON p.inbound_id = c.inbound_id AND p.day = c.day`, Args: []any{today}})
	return err
}

// ---------------------------------------------------------------------------------------------------
// Retention

// PruneHealth deletes check rounds older than samples, aggregates older than daily and alerts resolved longer ago
// than alerts. It returns the number of rows removed.
func (s *Store) PruneHealth(ctx context.Context, now time.Time, samples, daily, alerts time.Duration) (int64, error) {
	var total int64
	for _, q := range []struct {
		sql string
		cut int64
	}{
		{`DELETE FROM health_check_sample WHERE at < ?`, unix(now.Add(-samples))},
		{`DELETE FROM health_check_daily WHERE day < ?`, unix(now.Add(-daily))},
		{`DELETE FROM health_alert WHERE resolved_at > 0 AND resolved_at < ?`, unix(now.Add(-alerts))},
	} {
		res, err := s.W.ExecContext(ctx, q.sql, q.cut)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// PruneEvents deletes events older than info (severity 1) and older than other (severity 2 and 3), in batches
// so the single writer is never held for long. It returns the number of rows removed.
func (s *Store) PruneEvents(ctx context.Context, now time.Time, info, other time.Duration, batch int) (int64, error) {
	var total int64
	for _, q := range []struct {
		where string
		cut   int64
	}{
		{`severity = 1 AND ts < ?`, unix(now.Add(-info))},
		{`severity IN (2, 3) AND ts < ?`, unix(now.Add(-other))},
	} {
		for {
			res, err := s.W.ExecContext(ctx, `DELETE FROM event WHERE id IN (SELECT id FROM event WHERE `+q.where+` LIMIT ?)`, q.cut, batch)
			if err != nil {
				return total, err
			}
			n, _ := res.RowsAffected()
			total += n
			if n < int64(batch) {
				break
			}
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(20 * time.Millisecond): // let the stats writer in between batches
			}
		}
	}
	return total, nil
}
