package access

import (
	"testing"
	"time"
)

type networkUsageFunc func(nodeID string) (rxBps, txBps uint64, sampledAt time.Time, ok bool)

func (f networkUsageFunc) NetworkUsage(nodeID string) (uint64, uint64, time.Time, bool) {
	return f(nodeID)
}

func TestCurrentTrafficShares(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	source := networkUsageFunc(func(nodeID string) (uint64, uint64, time.Time, bool) {
		switch nodeID {
		case "de1":
			return 64_000_000, 10_000_000, now.Add(-time.Second), true
		case "nl1":
			return 20_000_000, 6_000_000, now.Add(-2 * time.Second), true
		case "stale":
			return 1_000_000, 0, now.Add(-networkSampleMaxAge - time.Second), true
		case "future":
			return 1_000_000, 0, now.Add(6 * time.Second), true
		default:
			return 0, 0, time.Time{}, false
		}
	})

	shares := CurrentTrafficShares([]string{"de1", "de1", "nl1", "stale", "future", "missing", ""}, source, now)
	if len(shares) != 2 {
		t.Fatalf("got %d shares, want 2: %#v", len(shares), shares)
	}
	if got := shares["de1"].Percent; got != 74 {
		t.Errorf("de1 share = %d, want 74", got)
	}
	if got := shares["nl1"].Percent; got != 26 {
		t.Errorf("nl1 share = %d, want 26", got)
	}
	if got := shares["de1"].RxBps; got != 64_000_000 {
		t.Errorf("de1 receive rate = %d", got)
	}
}

func TestCurrentTrafficSharesZeroTrafficAndNoSource(t *testing.T) {
	now := time.Now()
	source := networkUsageFunc(func(nodeID string) (uint64, uint64, time.Time, bool) {
		return 0, 0, now, true
	})
	if got := CurrentTrafficShares([]string{"de1", "nl1"}, source, now); got["de1"].Percent != 0 || got["nl1"].Percent != 0 {
		t.Errorf("zero traffic shares = %#v", got)
	}
	if got := CurrentTrafficShares([]string{"de1"}, nil, now); len(got) != 0 {
		t.Errorf("shares without a source = %#v", got)
	}
}
