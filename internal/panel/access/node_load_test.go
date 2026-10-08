package access

import (
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestCurrentNetworkUtilizationUsesNodeCapacityAndFreshProjection(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	rows := map[string]store.NodeLiveRow{
		"de1":       {Connected: true, SampleAt: now.Add(-time.Second).Unix(), RxBps: 64_000_000, TxBps: 10_000_000, CPUPct: 13},
		"nl1":       {Connected: true, SampleAt: now.Add(-2 * time.Second).Unix(), RxBps: 20_000_000, TxBps: 30_000_000, CPUPct: 5},
		"unknown":   {Connected: true, SampleAt: now.Unix(), RxBps: 5_000_000, TxBps: 1_000_000, CPUPct: 12},
		"stale":     {Connected: true, SampleAt: now.Add(-networkSampleMaxAge - time.Second).Unix(), RxBps: 1_000_000},
		"future":    {Connected: true, SampleAt: now.Add(6 * time.Second).Unix(), RxBps: 1_000_000},
		"offline":   {Connected: false, SampleAt: now.Unix(), RxBps: 1_000_000},
		"no-sample": {Connected: true},
	}
	usage := CurrentNetworkUtilization(
		[]string{"de1", "de1", "nl1", "unknown", "stale", "future", "offline", "no-sample", "missing", ""},
		map[string]int{"de1": 100, "nl1": 50}, rows, now,
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
	if got := usage["unknown"].LoadPercent; got == nil || *got != 12 {
		t.Errorf("unknown-capacity CPU utilization = %v, want 12%%", got)
	}
	if got := usage["de1"].RxBps; got != 64_000_000 {
		t.Errorf("de1 receive rate = %d", got)
	}
}

func TestCurrentNetworkUtilizationUsesCPUAndBusierLink(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rows := map[string]store.NodeLiveRow{
		"no-capacity": {Connected: true, SampleAt: now.Unix(), RxBps: 30_000_000, TxBps: 5_000_000, CPUPct: 72},
		"busy-link":   {Connected: true, SampleAt: now.Unix(), RxBps: 30_000_000, TxBps: 5_000_000, CPUPct: 10},
		"stale":       {Connected: true, SampleAt: now.Add(-networkSampleMaxAge - time.Second).Unix(), RxBps: 30_000_000, CPUPct: 99},
	}
	got := CurrentNetworkUtilization([]string{"no-capacity", "busy-link", "stale"}, map[string]int{"busy-link": 50, "stale": 100}, rows, now)
	if p := got["no-capacity"].LoadPercent; p == nil || *p != 72 {
		t.Errorf("no capacity set: the CPU is the load, got %v, want 72", p)
	}
	if p := got["busy-link"].LoadPercent; p == nil || *p != 60 {
		t.Errorf("the link busier than the CPU: got %v, want 60 (30 Mbps / 50)", p)
	}
	if _, ok := got["stale"]; ok {
		t.Errorf("a stale projection sample was included: %#v", got["stale"])
	}
}

func TestCurrentNetworkUtilizationZeroTrafficAndEmptyRows(t *testing.T) {
	now := time.Now().UTC()
	rows := map[string]store.NodeLiveRow{
		"de1": {Connected: true, SampleAt: now.Unix()},
		"nl1": {Connected: true, SampleAt: now.Unix()},
	}
	got := CurrentNetworkUtilization([]string{"de1", "nl1"}, map[string]int{"de1": 100}, rows, now)
	if got["de1"].LoadPercent == nil || *got["de1"].LoadPercent != 0 {
		t.Errorf("zero traffic with known capacity = %#v, want 0%%", got["de1"])
	}
	if got["nl1"].LoadPercent == nil || *got["nl1"].LoadPercent != 0 {
		t.Errorf("zero CPU without capacity = %#v, want 0%%", got["nl1"])
	}
	if got := CurrentNetworkUtilization([]string{"de1"}, nil, nil, now); len(got) != 0 {
		t.Errorf("usage without rows = %#v", got)
	}
}
