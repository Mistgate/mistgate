package store

import (
	"context"
	"encoding/json"
	"time"
)

// SettingClientProbe is the switch of the kl!ck reports ("1" on, anything else off; absent = off). subsettings reads it with the
// subscription settings, in the same query, for the Probe-Report header; clientprobe changes it.
const SettingClientProbe = "client_probe.enabled"

// ClientProbeNetwork is the network a kl!ck report was made on, as the app saw it (migration 00075). Zero values are unknown.
type ClientProbeNetwork struct {
	VPN, DirectChecked                     bool
	ASN                                    uint32
	Operator, Country, Region, NetworkType string
}

// ClientProbeResult is one server result of a report that matched a node. DetailJSON is what was measured.
type ClientProbeResult struct {
	NodeID, Kind, Verdict, DetailJSON string
	Port                              uint16
}

// InsertClientProbeReport stores one report in one statement (its rows go as one JSON argument: D1 binds at most 100 values).
func (s *Store) InsertClientProbeReport(ctx context.Context, report string, at time.Time, n ClientProbeNetwork, rows []ClientProbeResult) error {
	if len(rows) == 0 {
		return nil
	}
	type row struct {
		N string `json:"n"`
		K string `json:"k"`
		P uint16 `json:"p"`
		V string `json:"v"`
		D string `json:"d"`
	}
	in := make([]row, len(rows))
	for i, r := range rows {
		d := r.DetailJSON
		if d == "" {
			d = "{}"
		}
		in[i] = row{N: r.NodeID, K: r.Kind, P: r.Port, V: r.Verdict, D: d}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.W.ExecContext(ctx, `INSERT OR IGNORE INTO client_probe_report
		(report, at, node_id, kind, port, verdict, detail_json, vpn, direct_checked, asn, operator, country, region, net_type)
		SELECT ?, ?, json_extract(value, '$.n'), json_extract(value, '$.k'), json_extract(value, '$.p'), json_extract(value, '$.v'),
			json_extract(value, '$.d'), ?, ?, ?, ?, ?, ?, ? FROM json_each(?)`,
		report, unix(at), boolInt(n.VPN), boolInt(n.DirectChecked), int64(n.ASN), n.Operator, n.Country, n.Region, n.NetworkType, string(b))
	return err
}

// ClientProbeCount is what the reports since a time say of one node from one network (one AS, operator, country and region).
// A report counts once per node: OK when every result of the node in it was ok, and once in each verdict one of them got.
type ClientProbeCount struct {
	NodeID                                     string
	ASN                                        uint32
	Operator, Country, Region                  string
	Reports, OK, Freeze, TCPBlocked, Handshake int
	NoAnswer                                   int
	Last                                       time.Time
}

// ClientProbeCounts reads the counts since a time, by node and network.
func (s *Store) ClientProbeCounts(ctx context.Context, since time.Time) ([]ClientProbeCount, error) {
	rows, err := s.R.QueryContext(ctx, `WITH per AS (
			SELECT node_id, report, MAX(at) AS at, asn, operator, country, region,
				MIN(verdict = 'ok') AS ok, MAX(verdict = 'freeze') AS freeze, MAX(verdict = 'tcp_blocked') AS tcp,
				MAX(verdict = 'handshake_failed') AS hs, MAX(verdict = 'no_answer') AS na
			FROM client_probe_report WHERE at >= ? GROUP BY node_id, report)
		SELECT node_id, asn, operator, country, region, COUNT(*), SUM(ok), SUM(freeze), SUM(tcp), SUM(hs), SUM(na), MAX(at)
		FROM per GROUP BY node_id, asn, operator, country, region ORDER BY node_id`, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientProbeCount
	for rows.Next() {
		var c ClientProbeCount
		var asn, at int64
		if err := rows.Scan(&c.NodeID, &asn, &c.Operator, &c.Country, &c.Region, &c.Reports, &c.OK, &c.Freeze, &c.TCPBlocked, &c.Handshake, &c.NoAnswer, &at); err != nil {
			return nil, err
		}
		c.ASN, c.Last = uint32(asn), fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClientProbeTotals is the number of reports since a time and when the newest came (zero: none).
func (s *Store) ClientProbeTotals(ctx context.Context, since time.Time) (int, time.Time, error) {
	var n int
	var at int64
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(DISTINCT report), COALESCE(MAX(at), 0) FROM client_probe_report WHERE at >= ?`, unix(since)).Scan(&n, &at); err != nil {
		return 0, time.Time{}, err
	}
	if at == 0 {
		return n, time.Time{}, nil
	}
	return n, fromUnix(at), nil
}

// PruneClientProbeReports deletes the rows older than the cut and returns how many went.
func (s *Store) PruneClientProbeReports(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM client_probe_report WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteClientProbeReports forgets every stored report (Disconnect).
func (s *Store) DeleteClientProbeReports(ctx context.Context) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM client_probe_report`)
	return err
}
