//go:build js

package fleet

import "testing"

// TestD1IngestStatsAtEdgeCap (store package) measures the largest batch at this number; the two copies must agree.
func TestEdgeStatsCapMatchesTheD1Measurement(t *testing.T) {
	if maxStatsDeltas != 10_000 {
		t.Fatalf("maxStatsDeltas = %d on js, but internal/panel/store/measure_d1_js_test.go measures 10,000 (edgeStatsDeltaCap): change both together", maxStatsDeltas)
	}
}
