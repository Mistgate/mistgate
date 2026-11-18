package capacity

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// A node that only ran its own speed measurement is not overloaded: the peak of that hour is the measurement.
func TestSpeedMeasurementIsNotPeopleTraffic(t *testing.T) {
	const hour = int64(1_800_000_000 - 1_800_000_000%3600)
	hours := []store.HostHour{
		{HourStart: hour, Samples: 60, CPUAvg: 3, CPUMax: 11, RxMax: 91e6, TxMax: 90e6},
		{HourStart: hour + 3600, Samples: 60, CPUAvg: 3, CPUMax: 11, RxMax: 2e6, TxMax: 1e6},
	}
	plain := Read(Node{ID: "de1", BandwidthMbps: 100, Hours: hours})
	if plain.Status != StatusOverloaded {
		t.Fatalf("without a measurement on record the peak counts: status %q", plain.Status)
	}
	for name, end := range map[string]int64{"inside the hour": hour + 1200, "ended just after it": hour + 3600 + 30} {
		r := Read(Node{ID: "de1", BandwidthMbps: 100, Hours: hours, SpeedTests: []int64{end}})
		if r.Status == StatusOverloaded || r.Bottleneck == ResBandwidth {
			t.Errorf("%s: status %q, bottleneck %q", name, r.Status, r.Bottleneck)
		}
	}
	// a measurement in another hour excuses nothing
	if r := Read(Node{ID: "de1", BandwidthMbps: 100, Hours: hours, SpeedTests: []int64{hour - 7200}}); r.Status != StatusOverloaded {
		t.Errorf("a measurement two hours earlier hid the peak: status %q", r.Status)
	}
}
