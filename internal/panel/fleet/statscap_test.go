package fleet

import (
	"fmt"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The cap is a build-tagged constant (65,536 on the VPS, lower on the js edge build): a batch with one delta more than the
// cap is rejected whole, with the reason that session_core turns into a rate-limited stats_rejected event.
func TestGuardStatsRejectsABatchAboveTheCap(t *testing.T) {
	f := &Fleet{}
	deltas := make([]*agentv1.TrafficDelta, maxStatsDeltas+1)
	for i := range deltas {
		deltas[i] = &agentv1.TrafficDelta{CredId: fmt.Sprintf("crd_%d", i), InboundId: "inb_cap", BytesUp: 1, BytesDown: 1}
	}
	batch := func(n int) *agentv1.StatsBatch {
		return &agentv1.StatsBatch{IntervalStartUnix: 1_000, IntervalEndUnix: 1_010, Traffic: deltas[:n]}
	}
	g := f.guardStats(batch(maxStatsDeltas + 1))
	if g.reason != "too_many_deltas" || g.rejected != maxStatsDeltas+1 || len(g.traffic) != 0 {
		t.Fatalf("a batch of %d deltas: reason %q, rejected %d, kept %d; want too_many_deltas, all rejected, none kept",
			maxStatsDeltas+1, g.reason, g.rejected, len(g.traffic))
	}
	g = f.guardStats(batch(maxStatsDeltas))
	if g.reason != "" || g.rejected != 0 || len(g.traffic) != maxStatsDeltas {
		t.Fatalf("a batch of exactly %d deltas: reason %q, rejected %d, kept %d; want all kept", maxStatsDeltas, g.reason, g.rejected, len(g.traffic))
	}
}
