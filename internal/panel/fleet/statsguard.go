package fleet

import (
	"math"
	"sort"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	// defaultMaxNodeBytesPerSec is the most a node may report for all its users together, per second of
	// the batch interval: 10 Gbit/s, above any link the fleet runs on. Config.MaxNodeBytesPerSec overrides it.
	defaultMaxNodeBytesPerSec = 10_000_000_000 / 8
	// A batch covers 10 s normally; the agent coalesces up to one UTC hour (agent.proto), so no interval is
	// believed to be longer than that.
	maxBatchSpan = time.Hour
	// rejectEventEvery limits the stats_rejected events of one stream (the table has no retention).
	rejectEventEvery = time.Minute
)

// guardedStats is a StatsBatch after the sanity checks.
type guardedStats struct {
	traffic          []store.FleetTraffic
	links            []store.FleetLink // connection counters, at most maxStatsDeltas rows
	networks         []store.FleetNetwork
	personNetworks   []store.FleetPersonNetwork
	networksDBSHA256 string
	journal          []store.FleetJournal
	rejected         int    // deltas dropped
	reason           string // why, for the event; "" when nothing was dropped
}

// guardStats checks the numbers of a batch before they reach used_bytes. A node is trusted with its own
// traffic, not with the accounting of users on other nodes, so:
//   - a delta that cannot be real (more than the node's link could carry in the interval, which also covers
//     values above 2^63, i.e. "negative" ones) is dropped, not clamped: clamping would still add a huge fake
//     amount;
//   - the sum of the batch may not exceed the same budget, else the whole traffic part is dropped;
//   - repeated (credential, inbound) pairs are added with saturation before they are judged.
//
// The sessions, health and metrics of the batch are not affected, and the stream goes on.
func (f *Fleet) guardStats(st *agentv1.StatsBatch, streamCaps []string) guardedStats {
	elapsed := time.Duration(st.IntervalEndUnix-st.IntervalStartUnix) * time.Second
	elapsed = min(max(elapsed, time.Second), maxBatchSpan)
	rate := f.cfg.MaxNodeBytesPerSec
	if rate == 0 {
		rate = defaultMaxNodeBytesPerSec
	}
	budget := satMul(rate, uint64(elapsed/time.Second))

	links := f.guardLinks(st)
	networks, personNetworks, networksDBSHA256 := f.guardNetworks(st, streamCaps)
	journal := f.guardJournal(st)
	if len(st.Traffic) > maxStatsDeltas {
		return guardedStats{links: links, networks: networks, personNetworks: personNetworks, networksDBSHA256: networksDBSHA256,
			journal: journal, rejected: len(st.Traffic), reason: "too_many_deltas"}
	}
	type key struct{ cred, inbound string }
	at := map[key]int{}
	out := guardedStats{links: links, networks: networks, personNetworks: personNetworks, networksDBSHA256: networksDBSHA256, journal: journal}
	for _, d := range st.Traffic {
		k := key{d.CredId, d.InboundId}
		i, dup := at[k]
		if !dup {
			at[k] = len(out.traffic)
			out.traffic = append(out.traffic, store.FleetTraffic{CredID: d.CredId, InboundID: d.InboundId})
			i = len(out.traffic) - 1
		}
		out.traffic[i].Up = satAdd(out.traffic[i].Up, d.BytesUp)
		out.traffic[i].Down = satAdd(out.traffic[i].Down, d.BytesDown)
	}
	var total uint64
	kept := out.traffic[:0]
	for _, d := range out.traffic {
		if d.Up > budget || d.Down > budget || satAdd(d.Up, d.Down) > budget {
			out.rejected++
			out.reason = "delta_over_limit"
			continue
		}
		total = satAdd(total, satAdd(d.Up, d.Down))
		kept = append(kept, d)
	}
	out.traffic = kept
	if total > budget {
		return guardedStats{links: links, networks: networks, personNetworks: personNetworks, networksDBSHA256: networksDBSHA256,
			journal: journal, rejected: len(st.Traffic), reason: "batch_over_limit"}
	}
	return out
}

// guardNetworks makes the owner switch effective even when an agent sends rows it should not send.
func (f *Fleet) guardNetworks(st *agentv1.StatsBatch, caps []string) ([]store.FleetNetwork, []store.FleetPersonNetwork, string) {
	if !store.CapabilityPresent(caps, capNetworks) {
		return nil, nil, ""
	}
	mode := f.NetworkMode()
	digest := validNetworksDigest(st.GetNetworksDbSha256())
	if mode == 0 {
		return nil, nil, digest
	}
	type networkKey struct {
		inbound string
		asn     uint32
	}
	rows := make(map[networkKey]store.FleetNetwork, len(st.GetNetworks()))
	overflow := map[string]bool{}
	nonZero := 0
	dropped := 0
	for _, row := range st.GetNetworks() {
		if row == nil || row.GetInboundId() == "" || len(row.GetInboundId()) > 64 {
			dropped++
			continue
		}
		key := networkKey{inbound: row.GetInboundId(), asn: row.GetAsn()}
		if key.asn == 0 && overflow[key.inbound] {
			dropped++ // the node sends one overflow row for each inbound
			continue
		}
		if _, exists := rows[key]; !exists {
			if key.asn != 0 {
				if nonZero == 512 {
					dropped++
					continue
				}
				nonZero++
			} else {
				overflow[key.inbound] = true
			}
		}
		out := rows[key]
		out.InboundID, out.ASN = key.inbound, key.asn
		out.Attempts = clampNetworkCounter(out.Attempts, row.GetAttempts())
		out.Unfinished = clampNetworkCounter(out.Unfinished, row.GetUnfinished())
		out.Connects = clampNetworkCounter(out.Connects, row.GetConnects())
		out.Broken = clampNetworkCounter(out.Broken, row.GetBroken())
		out.People = max(out.People, min(row.GetPeople(), uint32(store.MaxLinkCount)))
		rows[key] = out
	}
	keys := make([]networkKey, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].inbound != keys[j].inbound {
			return keys[i].inbound < keys[j].inbound
		}
		return keys[i].asn < keys[j].asn
	})
	networks := make([]store.FleetNetwork, 0, len(keys))
	for _, key := range keys {
		networks = append(networks, rows[key])
	}
	var people []store.FleetPersonNetwork
	if mode == 2 {
		input := st.GetPersonNetworks()
		if len(input) > 2048 {
			dropped += len(input) - 2048
			input = input[:2048]
		}
		people = make([]store.FleetPersonNetwork, 0, len(input))
		for _, row := range input {
			if row == nil || row.GetCredId() == "" || row.GetInboundId() == "" || len(row.GetCredId()) > 64 || len(row.GetInboundId()) > 64 {
				dropped++
				continue
			}
			people = append(people, store.FleetPersonNetwork{CredID: row.GetCredId(), InboundID: row.GetInboundId(), ASN: row.GetAsn(),
				Connects: min(row.GetConnects(), uint32(store.MaxLinkCount))})
		}
	}
	if dropped > 0 && f.log != nil {
		f.log.Warn("network counters over the row limit or malformed, the rest dropped", "code", "network_stats_dropped", "rows", dropped)
	}
	return networks, people, digest
}

func clampNetworkCounter(current, added uint32) uint32 {
	return uint32(min(uint64(current)+uint64(min(added, uint32(store.MaxLinkCount))), uint64(store.MaxLinkCount)))
}

const (
	maxJournalEntries = 64
	journalSkew       = 300 // seconds a journal time may lie outside its batch's interval
)

// guardJournal bounds the node-provided journal before it reaches the store. Attribute keys are sorted before the cap so Go map
// iteration cannot change which attributes are kept.
func (f *Fleet) guardJournal(st *agentv1.StatsBatch) []store.FleetJournal {
	entries := st.GetJournal()
	if len(entries) > maxJournalEntries && f.log != nil {
		f.log.Warn("node journal over the row limit, the rest dropped", "rows", len(entries), "limit", maxJournalEntries)
	}
	if len(entries) > maxJournalEntries {
		entries = entries[:maxJournalEntries]
	}
	out := make([]store.FleetJournal, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || (entry.GetLevel() != agentv1.Severity_SEVERITY_WARNING && entry.GetLevel() != agentv1.Severity_SEVERITY_ERROR) {
			continue
		}
		// the agent's allow-list again, looked up rather than iterated: a node's own keys are never sorted or kept
		attrs := make(map[string]string, 8)
		for _, key := range logscrub.JournalAttrs {
			if v, ok := entry.GetAttrs()[key]; ok && len(attrs) < 8 {
				attrs[key] = logscrub.ClipRunes(v, 640) // the store cleans, then cuts to 160
			}
		}
		// the times stay within the batch's interval (and a little clock skew), so no node can sort itself first
		firstAt, lastAt := entry.GetFirstUnix(), entry.GetLastUnix()
		if firstAt > lastAt {
			firstAt, lastAt = lastAt, firstAt
		}
		if lo, hi := st.GetIntervalStartUnix()-journalSkew, st.GetIntervalEndUnix()+journalSkew; st.GetIntervalEndUnix() > 0 {
			firstAt, lastAt = min(max(firstAt, lo), hi), min(max(lastAt, lo), hi)
		}
		count := entry.GetCount()
		if count == 0 {
			count = 1
		}
		out = append(out, store.FleetJournal{Level: int32(entry.GetLevel()), Source: logscrub.Clip(entry.GetSource(), 32),
			Message: logscrub.Clip(entry.GetMessage(), 1024), InboundID: logscrub.Clip(entry.GetInboundId(), 64), Attrs: attrs,
			Count: count, FirstAt: firstAt, LastAt: lastAt})
	}
	return out
}

// guardLinks takes the connection counters of a batch (agent.proto LinkCounters). They are counts of events, not bytes, so no
// budget applies; only the number of rows is bounded (the same cap as the traffic deltas) and the rest is dropped with a warning.
// The store checks every row against the node's inbounds and the credentials it resolves.
func (f *Fleet) guardLinks(st *agentv1.StatsBatch) []store.FleetLink {
	rows := st.Links
	if len(rows) > maxStatsDeltas {
		f.log.Warn("connection counters over the row limit, the rest dropped", "rows", len(rows), "limit", maxStatsDeltas)
		rows = rows[:maxStatsDeltas]
	}
	out := make([]store.FleetLink, 0, len(rows))
	for _, l := range rows {
		if len(l.CredId) > 64 || len(l.InboundId) > 64 { // ids are 30 characters; a longer one only swells the D1 arguments
			continue
		}
		out = append(out, store.FleetLink{CredID: l.CredId, InboundID: l.InboundId, Connects: l.Connects, Rejects: l.Rejects,
			Drops: l.Drops, Stalls: l.Stalls, OnlineS: l.OnlineS, UnknownKeys: l.UnknownKeys, Hellos: l.Hellos, Handshakes: l.Handshakes,
			ClientCloses: l.ClientCloses, Freezes: l.Freezes, Quiet: l.Quiet, Accepted: l.Accepted, HellosForeign: l.HellosForeign,
			RttMSSum: l.RttMsSum, RttSamples: l.RttSamples, PktsSent: l.PktsSent, PktsLost: l.PktsLost,
			HsUnfinished: l.HsUnfinished, TLSAlertsSent: l.TlsAlertsSent, TLSAlertsRecv: l.TlsAlertsRecv, TLSCertRefused: l.TlsCertRefused,
			RealityFallback: l.RealityFallback, AuthFailed: l.AuthFailed, TCPStalls: l.TcpStalls, TCPStalls16K: l.TcpStalls_16K,
			Initiations: l.Initiations, Responses: l.Responses, ResponsesUnmatched: l.ResponsesUnmatched})
	}
	return out
}

func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

func satMul(a, b uint64) uint64 {
	if b != 0 && a > math.MaxUint64/b {
		return math.MaxUint64
	}
	return a * b
}

type stuckSeq struct {
	instance string
	seq      uint64
}
