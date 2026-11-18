package capacity

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// speedHours is a day and a quarter of idle hours; the first two hold a link-filling peak: a speed measurement, no people.
func speedHours() (hours []store.HostHour, first int64) {
	const hour = int64(1_800_000_000 - 1_800_000_000%3600)
	for i := range 30 {
		h := store.HostHour{HourStart: hour + int64(i)*3600, Samples: 360, CPUAvg: 3, CPUMax: 11, RxAvg: 1e6, TxAvg: 1e6, RxMax: 2e6, TxMax: 1e6}
		if i < 2 {
			h.RxMax, h.TxMax = 91e6, 90e6
		}
		hours = append(hours, h)
	}
	return hours, hour
}

// A node that only ran its own speed measurement is not overloaded: the peak of that hour is the measurement.
func TestSpeedMeasurementIsNotPeopleTraffic(t *testing.T) {
	hours, hour := speedHours()
	// The peak is one burst: whether or not it is on record as a measurement, it never reaches the status or the advice.
	plain := Read(Node{ID: "de1", BandwidthMbps: 100, Hours: hours})
	if plain.Status != StatusOK || rec(plain, RecBandwidthBusy) != nil || plain.BandwidthPeakPct == nil || *plain.BandwidthPeakPct < 90 {
		t.Fatalf("a burst of 2 hours in 30 is shown, not judged: status %q peak %v", plain.Status, plain.BandwidthPeakPct)
	}
	// On record as a measurement, the hours' peaks are not even shown.
	for name, end := range map[string]int64{"inside the hour": hour + 1200, "ended just after it": hour + 3600 + 30} {
		tests := []int64{end}
		if name == "inside the hour" {
			tests = append(tests, hour+3600+1200)
		}
		r := Read(Node{ID: "de1", BandwidthMbps: 100, Hours: hours, SpeedTests: tests})
		if r.Status != StatusOK || r.Bottleneck == ResBandwidth {
			t.Errorf("%s: status %q, bottleneck %q", name, r.Status, r.Bottleneck)
		}
		if name == "inside the hour" && *r.BandwidthPeakPct > 5 {
			t.Errorf("%s: the measurement's peak is still shown: %v", name, *r.BandwidthPeakPct)
		}
	}
	// a measurement in another hour excuses nothing
	if r := Read(Node{ID: "de1", BandwidthMbps: 100, Hours: hours, SpeedTests: []int64{hour - 7200}}); *r.BandwidthPeakPct < 90 {
		t.Errorf("a measurement two hours earlier hid the peak: %v", *r.BandwidthPeakPct)
	}
}
