package access

import (
	"math"
	"time"
)

const networkSampleMaxAge = 90 * time.Second

// NodeNetworkUtilization is a fresh host-level network sample and, when the node's symmetric
// capacity is known, its utilization in the busier direction (RX or TX).
type NodeNetworkUtilization struct {
	RxBps, TxBps uint64
	SampledAt    time.Time
	LoadPercent  *int
}

// CurrentNetworkUtilization returns one sample per distinct node. LoadPercent is how busy the node is: the larger of
// RX/TX divided by its configured symmetric capacity, or its CPU use when the source reports it (CPUUsageSource) and
// that is higher. A one-core VPS usually runs out of CPU before its link, and the CPU is known without the owner typing
// the provider's limit; with neither the percentage is nil. Nodes without a recent sample are omitted.
func CurrentNetworkUtilization(nodeIDs []string, capacityMbps map[string]int, source NetworkUsageSource, now time.Time) map[string]NodeNetworkUtilization {
	if source == nil {
		return nil
	}

	samples := make(map[string]NodeNetworkUtilization, len(nodeIDs))
	seen := make(map[string]struct{}, len(nodeIDs))
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
		usage := NodeNetworkUtilization{RxBps: rx, TxBps: tx, SampledAt: at}
		if capacity := capacityMbps[nodeID]; capacity > 0 {
			capacityBps := float64(capacity) * 1_000_000
			percent := int(math.Round(float64(max(rx, tx)) / capacityBps * 100))
			percent = min(100, max(0, percent))
			usage.LoadPercent = &percent
		}
		if cpu, ok := source.(CPUUsageSource); ok {
			if pct, cat, ok := cpu.CPUUsage(nodeID); ok && now.Sub(cat) >= -5*time.Second && now.Sub(cat) <= networkSampleMaxAge {
				percent := min(100, max(0, int(math.Round(pct))))
				if usage.LoadPercent == nil || percent > *usage.LoadPercent {
					usage.LoadPercent = &percent
				}
			}
		}
		samples[nodeID] = usage
	}
	return samples
}
