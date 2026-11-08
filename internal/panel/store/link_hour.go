package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FleetLink is one LinkCounters row of a StatsBatch (agent.proto, capability "links/1"): the connection counters of one credential
// on one inbound since the previous batch. CredID "" is the inbound itself.
type FleetLink struct {
	CredID, InboundID                                                 string
	Connects, Rejects, Drops, Stalls, OnlineS                         uint32
	UnknownKeys, Hellos, Handshakes                                   uint32
	ClientCloses, Freezes, Quiet, Accepted, HellosForeign, RttSamples uint32
	RttMSSum, PktsSent, PktsLost                                      uint64
	// links/3 (migration 00115)
	HsUnfinished, TLSAlertsSent, TLSAlertsRecv, TLSCertRefused uint32
	RealityFallback, AuthFailed, TCPStalls, TCPStalls16K       uint32
	Initiations, Responses, ResponsesUnmatched                 uint32
}

// linkRow is one row of the link_hour statement: the key, its person and the counters in the order of linkHourColumns. A
// typed row written straight to JSON keeps the edge's largest batch inside the isolate's memory (TestD1IngestStatsAtEdgeCap),
// which boxed values and reflection did not once the links/3 counters came.
type linkRow struct {
	cred, inbound, user string
	n                   [linkHourCounters]uint64
}

// linkHourCounters is len(linkHourColumns).
const linkHourCounters = 31

// Positions in linkRow.n (linkHourColumns).
const (
	lcConnects = iota
	lcRejects
	lcDrops
	lcStalls
	lcOnlineS
	lcUnknownKeys
	lcHellos
	lcHandshakes
	lcProbeHandshakes
	lcClientCloses
	lcFreezes
	lcQuiet
	lcAccepted
	lcHellosForeign
	lcRttMSSum
	lcRttSamples
	lcPktsSent
	lcPktsLost
	lcProbePktsSent
	lcProbePktsLost
	lcHsUnfinished // links/3 from here on
)

// row is the link as a stored row of user, with every counter clamped (maxLinkCount, maxLinkSum).
func (l FleetLink) row(user string) linkRow {
	r := linkRow{cred: l.CredID, inbound: l.InboundID, user: user}
	c := func(v uint32) uint64 { return uint64(min(v, maxLinkCount)) }
	w := func(v uint64) uint64 { return min(v, maxLinkSum) }
	copy(r.n[:], []uint64{c(l.Connects), c(l.Rejects), c(l.Drops), c(l.Stalls), c(l.OnlineS), c(l.UnknownKeys), c(l.Hellos),
		c(l.Handshakes), 0, c(l.ClientCloses), c(l.Freezes), c(l.Quiet), c(l.Accepted), c(l.HellosForeign), w(l.RttMSSum),
		c(l.RttSamples), w(l.PktsSent), w(l.PktsLost), 0, 0,
		c(l.HsUnfinished), c(l.TLSAlertsSent), c(l.TLSAlertsRecv), c(l.TLSCertRefused), c(l.RealityFallback), c(l.AuthFailed),
		c(l.TCPStalls), c(l.TCPStalls16K), c(l.Initiations), c(l.Responses), c(l.ResponsesUnmatched)})
	return r
}

// appendJSON writes the row as a JSON array.
func (r *linkRow) appendJSON(b []byte) []byte {
	b = append(b, '[')
	for _, s := range []string{r.cred, r.inbound, r.user} {
		b = appendJSONString(b, s)
		b = append(b, ',')
	}
	for i, v := range r.n {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendUint(b, v, 10)
	}
	return append(b, ']')
}

// appendJSONString writes s as a JSON string. Ids are valid UTF-8 (protobuf strings are); only quotes, backslashes and control
// characters need escaping.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

// maxLinkCount clamps one counter of a row of one batch (about a million: no engine counts that many events or seconds of one key in
// a batch). It keeps a row at 7 digits per counter, so the JSON of the largest batch the edge admits stays under D1's 2,000,000
// bytes for one value (TestD1IngestStatsAtEdgeCap).
const maxLinkCount = 1 << 20

// maxLinkSum clamps the wide fields of a row of one batch (rtt_ms_sum, pkts_sent, pkts_lost): about four billion, far above
// what one key moves in a batch, and low enough that 168 hours of the largest batches cannot overflow an int64 SUM.
const maxLinkSum = 1 << 32

// zero reports a row with no counter at all (the agent does not send one; it is not stored).
func (l FleetLink) zero() bool {
	return l.Connects|l.Rejects|l.Drops|l.Stalls|l.OnlineS|l.UnknownKeys|l.Hellos|l.Handshakes|
		l.ClientCloses|l.Freezes|l.Quiet|l.Accepted|l.HellosForeign|l.RttSamples|l.HsUnfinished|l.TLSAlertsSent|l.TLSAlertsRecv|
		l.TLSCertRefused|l.RealityFallback|l.AuthFailed|l.TCPStalls|l.TCPStalls16K|l.Initiations|l.Responses|l.ResponsesUnmatched == 0 &&
		l.RttMSSum|l.PktsSent|l.PktsLost == 0
}

// linkHourColumns are the counter columns of a row's positional array from index 3 on (0 cred, 1 inbound, 2 user), each
// added up on conflict.
var linkHourColumns = []string{"connects", "rejects", "drops", "stalls", "online_s", "unknown_keys", "hellos", "handshakes",
	"probe_handshakes", "client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms_sum", "rtt_samples", "pkts_sent",
	"pkts_lost", "probe_pkts_sent", "probe_pkts_lost",
	"hs_unfinished", "tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused", "reality_fallback", "auth_failed", "tcp_stalls",
	"tcp_stalls_16k", "initiations", "responses", "responses_unmatched"}

// linkHourParts is how many JSON arguments the rows are split into: one bound value stays under D1's 2,000,000 bytes at the
// 10,000-row edge cap (TestD1IngestStatsAtEdgeCap) with every counter of every row at its clamp.
const linkHourParts = 3

var linkHourQuery = func() string {
	var cols, sel, set, from strings.Builder
	for i, c := range linkHourColumns {
		fmt.Fprintf(&cols, ", %s", c)
		fmt.Fprintf(&sel, ", CAST(json_extract(l.value, '$[%d]') AS INTEGER)", i+3)
		if i > 0 {
			set.WriteString(", ")
		}
		fmt.Fprintf(&set, "%s = %s + excluded.%s", c, c, c)
	}
	for i := range linkHourParts {
		if i > 0 {
			from.WriteString(" UNION ALL ")
		}
		fmt.Fprintf(&from, "SELECT value FROM json_each(?%d)", i+1)
	}
	return fmt.Sprintf(`
		INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, user_id%s)
		SELECT ?%d, ?%d, json_extract(l.value, '$[1]'), json_extract(l.value, '$[0]'), json_extract(l.value, '$[2]')%s
		FROM (%s) AS l
		WHERE 1
		ON CONFLICT (node_id, hour_start, inbound_id, cred_id) DO UPDATE SET %s`,
		cols.String(), linkHourParts+1, linkHourParts+2, sel.String(), from.String(), set.String())
}()

// linkHourStmt adds the rows to the hour's bucket in one statement. The rows go as linkHourParts JSON arguments of positional
// arrays ([cred, inbound, user] and then linkHourColumns): D1 binds at most 100 values, and splitting the rows keeps each
// bound value under D1's 2,000,000-byte limit at the 10,000-row edge cap. With no rows it still runs (empty arrays), so the
// batch has the same shape on every call.
func linkHourStmt(nodeID string, hourStart int64, rows []linkRow) (Stmt, error) {
	args := make([]any, 0, linkHourParts+2)
	per := (len(rows) + linkHourParts - 1) / linkHourParts
	var b []byte
	for i := range linkHourParts {
		lo, hi := min(i*per, len(rows)), min((i+1)*per, len(rows))
		b = append(b[:0], '[')
		for j := lo; j < hi; j++ {
			if j > lo {
				b = append(b, ',')
			}
			b = rows[j].appendJSON(b)
		}
		b = append(b, ']')
		args = append(args, string(b))
	}
	return Stmt{Query: linkHourQuery, Args: append(args, nodeID, hourStart)}, nil
}

// linkHourOf is the first hour bucket a window of the given start touches: the hour that holds since.
func linkHourOf(since time.Time) int64 { return unix(since) / 3600 * 3600 }

// ProbePackets is, per inbound, the packets its node sent to the panel's health check and declared lost (probe_pkts_sent and
// probe_pkts_lost) over the hour buckets that touch [since, now]. Agents flush them every 10 minutes, so the newest are missing
// for up to that long. Inbounds without any are left out.
func (s *Store) ProbePackets(ctx context.Context, since time.Time) (map[string][2]uint64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT inbound_id, SUM(probe_pkts_sent), SUM(probe_pkts_lost) FROM link_hour
		WHERE cred_id = '' AND hour_start >= ? GROUP BY inbound_id HAVING SUM(probe_pkts_sent) > 0`, linkHourOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]uint64{}
	for rows.Next() {
		var id string
		var sent, lost int64
		if err := rows.Scan(&id, &sent, &lost); err != nil {
			return nil, err
		}
		out[id] = [2]uint64{uint64(max(sent, 0)), uint64(max(lost, 0))}
	}
	return out, rows.Err()
}

// PruneLinkHours deletes the hour buckets that end before the cut (the bucket holding the cut stays whole) and returns how many
// rows went.
func (s *Store) PruneLinkHours(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM link_hour WHERE hour_start < ?`, linkHourOf(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LinkInboundSums is the counters of one inbound of a node over a window: its keys and its own row together. KeysStalled is the
// number of keys with a stall, KeysTried the number with a connect or a stall.
type LinkInboundSums struct {
	InboundID                                             string
	Connects, Rejects, Drops, Stalls, UnknownKeys         uint64
	OnlineS, Hellos, Handshakes                           uint64
	KeysStalled, KeysTried                                uint64
	ClientCloses, Freezes, Quiet, Accepted, HellosForeign uint64
	RttMSSum, RttSamples, PktsSent, PktsLost              uint64
	LinkReasons
}

// NodeLinkSums reads the counters of a node's inbounds over the hour buckets that touch [since, now], one row per inbound that
// has any. It is one read (a per-key subquery folds the keys). The handshakes of the panel's own checks (probe_handshakes) are
// taken off hellos and handshakes; the agent flushes those every 10 minutes and the probe's connects at once, so the window's
// edges may be off by a check or two, and the difference stops at zero.
func (s *Store) NodeLinkSums(ctx context.Context, nodeID string, since time.Time) ([]LinkInboundSums, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT inbound_id, SUM(connects), SUM(rejects), SUM(drops), SUM(stalls), SUM(online_s), SUM(unknown_keys),
			MAX(0, SUM(hellos) - SUM(probe)), MAX(0, SUM(handshakes) - SUM(probe)),
			SUM(client_closes), SUM(freezes), SUM(quiet), MAX(0, SUM(accepted) - SUM(probe)), SUM(hellos_foreign),
			SUM(rtt_ms_sum), SUM(rtt_samples), SUM(pkts_sent), SUM(pkts_lost),
			SUM(cred_id <> '' AND stalls > 0), SUM(cred_id <> '' AND connects + stalls > 0)
		FROM (SELECT inbound_id, cred_id, SUM(connects) AS connects, SUM(rejects) AS rejects, SUM(drops) AS drops,
				SUM(stalls) AS stalls, SUM(online_s) AS online_s, SUM(unknown_keys) AS unknown_keys, SUM(hellos) AS hellos,
				SUM(handshakes) AS handshakes, SUM(probe_handshakes) AS probe, SUM(client_closes) AS client_closes,
				SUM(freezes) AS freezes, SUM(quiet) AS quiet, SUM(accepted) AS accepted, SUM(hellos_foreign) AS hellos_foreign,
				SUM(rtt_ms_sum) AS rtt_ms_sum, SUM(rtt_samples) AS rtt_samples, SUM(pkts_sent) AS pkts_sent, SUM(pkts_lost) AS pkts_lost
			FROM link_hour WHERE node_id = ? AND hour_start >= ? GROUP BY inbound_id, cred_id)
		GROUP BY inbound_id ORDER BY inbound_id`, nodeID, linkHourOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkInboundSums
	for rows.Next() {
		var r LinkInboundSums
		if err := rows.Scan(&r.InboundID, &r.Connects, &r.Rejects, &r.Drops, &r.Stalls, &r.OnlineS, &r.UnknownKeys, &r.Hellos,
			&r.Handshakes, &r.ClientCloses, &r.Freezes, &r.Quiet, &r.Accepted, &r.HellosForeign, &r.RttMSSum, &r.RttSamples,
			&r.PktsSent, &r.PktsLost, &r.KeysStalled, &r.KeysTried); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserLink is the counters of one device of a person on one inbound of a node over a window. LastHour is the start of the newest
// hour with a counter.
type UserLink struct {
	DeviceID, NodeID, NodeName, InboundID, ProfileName, Protocol string
	Platform, Client, Kind                                       string
	Connects, Rejects, Drops, Stalls, OnlineS                    uint64
	ClientCloses, Freezes, Quiet, Accepted, HellosForeign        uint64
	RttMSSum, RttSamples, PktsSent, PktsLost                     uint64
	LastHour                                                     time.Time
	Hours                                                        []LinkHourSums
	LinkReasons
	// What decides which links/3 counters the row can have (connectiondiag.Variant): the VLESS security, transport and CDN host
	// as a small JSON object ("" for other protocols), and the AmneziaWG backend of the inbound's last report ("" = none).
	Variant, AwgBackend string
}

// UserLinks reads a person's counters over the hour buckets that touch [since, now], per device, node and inbound, the ones with a
// counter only; device, then node name, then profile name. The device comes from the credential; an inbound or node that no longer
// exists has no row.
func (s *Store) UserLinks(ctx context.Context, userID string, since time.Time) ([]UserLink, error) {
	return s.UserLinksByHour(ctx, userID, since, false)
}

// LinkKeepHours is how long link_hour rows live, and the most a connection RPC may look back.
const LinkKeepHours = 168

// LinkWindowHours is the window of a connection RPC in hours: 0 means 24, and more than the retention is cut to it.
func LinkWindowHours(requested uint32) int {
	switch {
	case requested == 0:
		return 24
	case requested > LinkKeepHours:
		return LinkKeepHours
	}
	return int(requested)
}
