package access

import (
	"math"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const networkSampleMaxAge = 90 * time.Second

// NodeNetworkUtilization is a fresh host-level network sample and, when the node's symmetric
// capacity is known, its utilization in the busier direction (RX or TX).
type NodeNetworkUtilization struct {
	RxBps, TxBps uint64
	SampledAt    time.Time
	LoadPercent  *int
}

// CurrentNetworkUtilization returns one fresh sample per distinct connected node. LoadPercent is the larger of the
// RX/TX rate as a percentage of its configured capacity and CPU use. Nodes without a recent sample are omitted.
func CurrentNetworkUtilization(nodeIDs []string, capacityMbps map[string]int, rows map[string]store.NodeLiveRow, now time.Time) map[string]NodeNetworkUtilization {
	if rows == nil {
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

		row, ok := rows[nodeID]
		if !ok || !row.Connected || row.SampleAt <= 0 {
			continue
		}
		rx, tx := uint64(max(row.RxBps, 0)), uint64(max(row.TxBps, 0))
		at := time.Unix(row.SampleAt, 0).UTC()
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
		percent := min(100, max(0, int(math.Round(float64(row.CPUPct)))))
		if usage.LoadPercent == nil || percent > *usage.LoadPercent {
			usage.LoadPercent = &percent
		}
		samples[nodeID] = usage
	}
	return samples
}
