package connectiondiag

import (
	"math"
	"testing"
)

func TestConnectionQualityDerivedMetrics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rttSum     uint64
		rttSamples uint64
		lost, sent uint64
		wantRTT    uint32
		wantLoss   uint32
	}{
		{name: "no samples", rttSum: 200, lost: 10, wantLoss: 0},
		{name: "average", rttSum: 1500, rttSamples: 3, lost: 5, sent: 500, wantRTT: 500, wantLoss: 10},
		{name: "zero loss is measured", rttSum: 8, rttSamples: 2, sent: 100, wantRTT: 4, wantLoss: 0},
		{name: "large packets", lost: 1 << 56, sent: 1 << 57, wantLoss: 500},
		{name: "large RTT", rttSum: uint64(math.MaxUint64), rttSamples: 1, wantRTT: math.MaxUint32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AverageRTTMS(tc.rttSum, tc.rttSamples); got != tc.wantRTT {
				t.Errorf("AverageRTTMS = %d, want %d", got, tc.wantRTT)
			}
			if got := LossPermille(tc.lost, tc.sent); got != tc.wantLoss {
				t.Errorf("LossPermille = %d, want %d", got, tc.wantLoss)
			}
		})
	}
}
