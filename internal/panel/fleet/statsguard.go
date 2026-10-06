package fleet

import (
	"math"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	// defaultMaxNodeBytesPerSec is the most a node may report for all its users together, per second of
	// the batch interval: 10 Gbit/s, above any link the fleet runs on. Config.MaxNodeBytesPerSec overrides it.
	defaultMaxNodeBytesPerSec = 10_000_000_000 / 8
	// A batch covers 10 s normally; the agent coalesces up to one UTC hour (agent.proto), so no interval is
	// believed to be longer than that.
	maxBatchSpan = time.Hour
	// maxStatsDeltas bounds the work of one batch: a node cannot have more credentials than this.
	maxStatsDeltas = 1 << 16
	// rejectEventEvery limits the stats_rejected events of one stream (the table has no retention).
	rejectEventEvery = time.Minute
)

// guardedStats is a StatsBatch after the sanity checks.
type guardedStats struct {
	traffic  []store.FleetTraffic
	rejected int    // deltas dropped
	reason   string // why, for the event; "" when nothing was dropped
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
func (f *Fleet) guardStats(st *agentv1.StatsBatch) guardedStats {
	elapsed := time.Duration(st.IntervalEndUnix-st.IntervalStartUnix) * time.Second
	elapsed = min(max(elapsed, time.Second), maxBatchSpan)
	rate := f.cfg.MaxNodeBytesPerSec
	if rate == 0 {
		rate = defaultMaxNodeBytesPerSec
	}
	budget := satMul(rate, uint64(elapsed/time.Second))

	if len(st.Traffic) > maxStatsDeltas {
		return guardedStats{rejected: len(st.Traffic), reason: "too_many_deltas"}
	}
	type key struct{ cred, inbound string }
	at := map[key]int{}
	var out guardedStats
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
		return guardedStats{rejected: len(st.Traffic), reason: "batch_over_limit"}
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
