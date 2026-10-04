package access

import (
	"math"
	"time"
)

const networkSampleMaxAge = 90 * time.Second

// NodeTrafficShare is a fresh host-level network sample and that node's share of the measured traffic across a set of
// nodes. Percent is a comparison between those nodes, not utilization of the provider's link capacity.
type NodeTrafficShare struct {
	RxBps, TxBps uint64
	SampledAt    time.Time
	Percent      int
}

// CurrentTrafficShares returns one sample per distinct node and the node's rounded share of total RX+TX traffic.
// Nodes without a recent sample are omitted; callers should describe the result as a share among measured nodes.
func CurrentTrafficShares(nodeIDs []string, source NetworkUsageSource, now time.Time) map[string]NodeTrafficShare {
	if source == nil {
		return nil
	}

	samples := make(map[string]NodeTrafficShare, len(nodeIDs))
	seen := make(map[string]struct{}, len(nodeIDs))
	total := float64(0)
	for _, nodeID := range nodeIDs {
		if nodeID == "" {
			continue
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		seen[nodeID] = struct{}{}

		rx, tx, at, ok := source.NetworkUsage(nodeID)
		if !ok || at.IsZero() {
			continue
		}
		age := now.Sub(at)
		// The sample can arrive after the view's clock snapshot while this function is running.
		if age < -5*time.Second || age > networkSampleMaxAge {
			continue
		}
		samples[nodeID] = NodeTrafficShare{RxBps: rx, TxBps: tx, SampledAt: at}
		total += float64(rx) + float64(tx)
	}

	for nodeID, sample := range samples {
		if total > 0 {
			sample.Percent = int(math.Round((float64(sample.RxBps) + float64(sample.TxBps)) / total * 100))
			sample.Percent = min(100, max(0, sample.Percent))
		}
		samples[nodeID] = sample
	}
	return samples
}
