package access

import "math"

// nodeLoadPercent compares the busiest direction on the node's main network interface with its configured symmetric
// link capacity. Using the busiest direction reflects full-duplex links without adding RX and TX together.
func nodeLoadPercent(rxBps, txBps uint64, capacityMbps int) (int, bool) {
	if capacityMbps <= 0 {
		return 0, false
	}
	rate := max(rxBps, txBps)
	utilization := float64(rate) / (float64(capacityMbps) * 1_000_000)
	if utilization >= 1 {
		return 100, true
	}
	return int(math.Round(utilization * 100)), true
}
