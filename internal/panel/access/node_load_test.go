package access

import (
	"testing"
	"time"
)

type networkUsageFunc func(nodeID string) (rxBps, txBps uint64, sampledAt time.Time, ok bool)

func (f networkUsageFunc) NetworkUsage(nodeID string) (uint64, uint64, time.Time, bool) {
	return f(nodeID)
}

func TestCurrentNetworkUtilizationUsesNodeCapacityAndBusierDirection(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	source := networkUsageFunc(func(nodeID string) (uint64, uint64, time.Time, bool) {
		switch nodeID {
		case "de1":
			return 64_000_000, 10_000_000, now.Add(-time.Second), true
		case "nl1":
			return 20_000_000, 30_000_000, now.Add(-2 * time.Second), true
		case "unknown":
			return 5_000_000, 1_000_000, now, true
		case "stale":
			return 1_000_000, 0, now.Add(-networkSampleMaxAge - time.Second), true
		case "future":
			return 1_000_000, 0, now.Add(6 * time.Second), true
		default:
			return 0, 0, time.Time{}, false
		}
	})

	usage := CurrentNetworkUtilization(
		[]string{"de1", "de1", "nl1", "unknown", "stale", "future", "missing", ""},
		map[string]int{"de1": 100, "nl1": 50}, source, now,
	)
	if len(usage) != 3 {
		t.Fatalf("got %d samples, want 3: %#v", len(usage), usage)
	}
	if got := usage["de1"].LoadPercent; got == nil || *got != 64 {
		t.Errorf("de1 utilization = %v, want 64%% (64 Mbps RX / 100 Mbps)", got)
	}
	if got := usage["nl1"].LoadPercent; got == nil || *got != 60 {
		t.Errorf("nl1 utilization = %v, want 60%% (30 Mbps TX / 50 Mbps)", got)
	}
	if got := usage["unknown"].LoadPercent; got != nil {
		t.Errorf("unknown-capacity utilization = %v, want nil", *got)
	}
	if got := usage["de1"].RxBps; got != 64_000_000 {
		t.Errorf("de1 receive rate = %d", got)
	}
}

func TestCurrentNetworkUtilizationZeroTrafficAndNoSource(t *testing.T) {
	now := time.Now()
	source := networkUsageFunc(func(nodeID string) (uint64, uint64, time.Time, bool) {
		return 0, 0, now, true
	})
	got := CurrentNetworkUtilization([]string{"de1", "nl1"}, map[string]int{"de1": 100}, source, now)
	if got["de1"].LoadPercent == nil || *got["de1"].LoadPercent != 0 {
		t.Errorf("zero traffic with known capacity = %#v, want 0%%", got["de1"])
	}
	if got["nl1"].LoadPercent != nil {
		t.Errorf("zero traffic with unknown capacity = %#v, want nil percent", got["nl1"])
	}
	if got := CurrentNetworkUtilization([]string{"de1"}, nil, nil, now); len(got) != 0 {
		t.Errorf("usage without a source = %#v", got)
	}
}
