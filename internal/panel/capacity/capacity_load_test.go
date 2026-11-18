package capacity

import (
	"fmt"
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The live cases the report got wrong, as series of hours: a lone burst on the link (a speed measurement that was not on
// record), the first hours of a new node (an install), and a headroom drawn from noise.

// raw is one fixture hour: samples, CPU mean/peak, people, and the busier direction's mean and peak in Mbit/s.
type raw struct {
	samples   uint32
	cpu, peak float64
	people    float64
	mean, top float64
}

func rawHours(rs []raw) []store.HostHour {
	out := make([]store.HostHour, len(rs))
	for i, r := range rs {
		out[i] = store.HostHour{HourStart: t0 + int64(i)*3600, Samples: r.samples, CPUAvg: r.cpu, CPUMax: max(r.peak, r.cpu),
			RAMUsedMax: 400 * mib, RAMTotal: 2048 * mib, ExtSamples: r.samples, SessionsAvg: r.people, SessionsMax: uint32(r.people) + 2,
			RxAvg: uint64(r.mean * 1e6), TxAvg: uint64(r.mean * 1e6 / 2), RxMax: uint64(r.top * 1e6), TxMax: uint64(r.top * 1e6 * 0.87)}
	}
	return out
}

// week is 168 quiet hours: CPU 1 to 2 percent, 3 to 10 people, the link's mean about 5 Mbit/s and its hourly peaks lo..hi
// Mbit/s, wandering.
func week(lo, hi float64) []raw {
	out := make([]raw, 168)
	for i := range out {
		out[i] = raw{samples: 360, cpu: 1 + float64(i%3)/2, peak: 5 + float64(i%7), people: 3 + float64(i%8),
			mean: 4.9, top: lo + (hi-lo)*float64((i*7)%21)/20}
	}
	return out
}

func withBurst(rs []raw, at, n int, mbps float64) []raw {
	out := slices.Clone(rs)
	for i := at; i < at+n; i++ {
		out[i].top = mbps
	}
	return out
}

func ids(r Report) string { return fmt.Sprintf("%s|%s|%v", r.Status, r.Bottleneck, codes(r)) }

// Case 1: 225 Mbit/s once, in two hours, of a week that peaks 10 to 30 (and sometimes 73) Mbit/s on a 250 Mbit/s link.
func TestLoneBurstOnTheLink(t *testing.T) {
	quiet := week(10, 30)
	for i := 0; i < 168; i += 17 {
		quiet[i].top = 73
	}
	burst := withBurst(quiet, 100, 2, 225)

	r := Read(Node{Name: "be", BandwidthMbps: 250, Hours: rawHours(burst), WindowHours: 168})
	if r.Status != StatusOK || r.Bottleneck != "" || len(r.Recs) != 0 {
		t.Fatalf("one burst: %s", ids(r))
	}
	if r.BandwidthPeakPct == nil || *r.BandwidthPeakPct < 89 || *r.BandwidthTypicalPct > 15 || *r.BandwidthBusyHourPct > 3 {
		t.Fatalf("the report must say both: highest %v, typical %v, busy hour %v", r.BandwidthPeakPct, r.BandwidthTypicalPct, r.BandwidthBusyHourPct)
	}
	// on record as a measurement the hours are not read at all: the highest is the week's own 73
	var tests []int64
	for _, h := range []int{100, 101} {
		tests = append(tests, t0+int64(h)*3600+600)
	}
	m := Read(Node{Name: "be", BandwidthMbps: 250, Hours: rawHours(burst), SpeedTests: tests, WindowHours: 168})
	if m.Status != StatusOK || *m.BandwidthPeakPct > 30 {
		t.Fatalf("marked: %s, highest %v", ids(m), *m.BandwidthPeakPct)
	}
	// the same week without the burst reads the same
	if q := Read(Node{Name: "be", BandwidthMbps: 250, Hours: rawHours(quiet), WindowHours: 168}); ids(q) != ids(r) {
		t.Fatalf("burst %s, none %s", ids(r), ids(q))
	}
}

// One burst in a week never changes the status or the advice, wherever the week's own peaks sit below the line, however long
// the burst and wherever in the week it falls.
func TestOneBurstChangesNothing(t *testing.T) {
	for _, b := range []struct {
		name   string
		lo, hi float64 // hourly peaks of a 100 Mbit/s link
	}{{"idle", 5, 25}, {"middle", 30, 55}, {"just under busy", 45, 65}} {
		for _, n := range []int{1, 2} {
			for _, at := range []int{0, 37, 100, 166} {
				base := week(b.lo, b.hi)
				want := Read(Node{Name: "de1", BandwidthMbps: 100, Hours: rawHours(base), WindowHours: 168})
				got := Read(Node{Name: "de1", BandwidthMbps: 100, Hours: rawHours(withBurst(base, at, n, 100)), WindowHours: 168})
				if ids(got) != ids(want) {
					t.Errorf("%s, %d h at %d: %s -> %s", b.name, n, at, ids(want), ids(got))
				}
			}
		}
	}
}

// A link that is really full stays overloaded: typical peaks at the line, or an evening of full hours.
func TestRealLinkOverloadIsKept(t *testing.T) {
	always := week(95, 99) // 100 Mbit/s link, peaks 95 to 99 in every hour
	if r := Read(Node{Name: "de1", BandwidthMbps: 100, Hours: rawHours(always), WindowHours: 168}); r.Status != StatusOverloaded || r.Bottleneck != ResBandwidth ||
		rec(r, RecBandwidthBusy) == nil || rec(r, RecBandwidthBusy).Severity != SevCrit {
		t.Fatalf("peaks at the line all week: %+v", r.Recs)
	}
	evening := week(10, 30) // six hours a day at 85 Mbit/s mean: a quarter of the hours, so the typical peak stays low
	for i := range evening {
		if i%24 >= 18 {
			evening[i].mean, evening[i].top = 85, 99
		}
	}
	r := Read(Node{Name: "de1", BandwidthMbps: 100, Hours: rawHours(evening), WindowHours: 168})
	if r.Status != StatusOverloaded || r.Bottleneck != ResBandwidth || rec(r, RecBandwidthBusy) == nil {
		t.Fatalf("a full evening: %s %v", ids(r), r.BandwidthBusyHourPct)
	}
	// busy, not overloaded: typical peaks 75 to 85
	if r := Read(Node{Name: "de1", BandwidthMbps: 100, Hours: rawHours(week(75, 85)), WindowHours: 168}); r.Status != StatusBusy || rec(r, RecBandwidthBusy).Severity != SevWarn {
		t.Fatalf("typical peaks at 80: %s", ids(r))
	}
}

// Case 3: a node that connected 20 hours ago. Its first hour was the install (104 samples of 360, CPU 79%, one person).
func TestNewNodeIsNotRead(t *testing.T) {
	newNode := func(hours int) []raw {
		out := make([]raw, hours)
		for i := range out {
			out[i] = raw{samples: 360, cpu: 0.5 + float64(i%8)/2, peak: 8, people: 1 + float64(i%3), mean: 1, top: 4}
		}
		out[0] = raw{samples: 104, cpu: 79, peak: 100, people: 1, mean: 40, top: 90}
		return out
	}
	for _, c := range []struct {
		name  string
		hours int
		first int64
		fill  uint32 // samples of the first hour
		want  string
	}{
		{"20 hours, partial first", 20, t0, 104, StatusUnknown},
		{"20 hours, first hour full", 20, t0, 360, StatusUnknown},
		{"20 hours, first unknown", 20, 0, 104, StatusUnknown},
		{"a day and more, partial first", 30, t0, 104, StatusOK},
		{"a day and more, full first, known", 30, t0, 360, StatusOK},
		{"a day and more, partial first, unknown", 30, 0, 104, StatusOK},
	} {
		rs := newNode(c.hours)
		rs[0].samples = c.fill
		r := Read(Node{Name: "se1", Connected: true, BandwidthMbps: 1000, CPUs: 1, Hours: rawHours(rs), FirstHour: c.first, WindowHours: 168})
		if r.Status != c.want || r.HoursWithData != c.hours {
			t.Errorf("%s: %s, hours %d", c.name, ids(r), r.HoursWithData)
		}
		if len(r.Recs) != 0 || r.Headroom != nil {
			t.Errorf("%s: advice for a node with no history: %+v headroom %v", c.name, r.Recs, r.Headroom)
		}
		if c.want == StatusOK && (r.BusyHourCPU > 5 || r.Bottleneck != "") {
			t.Errorf("%s: the install is the busy hour: %v at %d, bottleneck %q", c.name, r.BusyHourCPU, r.BusyHourUnix, r.Bottleneck)
		}
	}
	// only the disk is said while it is young
	r := Read(Node{Name: "se1", Hours: rawHours(newNode(5)), FirstHour: t0, DiskUsed: 95, DiskTotal: 100})
	if r.Status != StatusUnknown || !slices.Equal(codes(r), []string{RecDiskFilling}) {
		t.Errorf("young with a full disk: %s", ids(r))
	}
	// and the fleet sends nobody to it or from it
	young := Node{ID: "n1", Name: "se1", Connected: true, Hours: rawHours(newNode(20)), FirstHour: t0}
	roomy := Node{ID: "n2", Name: "nl1", Connected: true, Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))}
	reports, s := Fleet([]Node{young, roomy})
	for _, r := range reports {
		if r.Name == "se1" && (r.Status != StatusUnknown || len(r.Recs) != 0) {
			t.Errorf("fleet read of a young node: %s", ids(r))
		}
	}
	if s.Unknown != 1 || s.OK != 1 || s.HeadroomNodes != 1 {
		t.Errorf("summary %+v", s)
	}
}

// An hour with too few samples says nothing, wherever it falls; the hour that is still running is one of them.
func TestPartialHoursAreLeftOut(t *testing.T) {
	hs := build(steady(48, 10, 0.4, daily(0, 40), nil))
	hs[10].Samples, hs[10].CPUAvg, hs[10].CPUMax = 50, 95, 100
	r := Read(Node{Name: "de1", Hours: hs})
	if r.HoursWithData != 48 || r.BusyHourCPU > 30 || r.CPUPeakP95 > 40 || r.Status != StatusOK {
		t.Fatalf("a partial hour counted: %+v", r)
	}
	// the cut is half of the node's fullest hour, not a number of samples: a node sampled every 30 s has 120 in a full hour
	for i := range hs {
		hs[i].Samples = 120
	}
	hs[10].Samples = 50
	if r := Read(Node{Name: "de1", Hours: hs}); r.BusyHourCPU > 30 || r.HoursWithData != 48 {
		t.Fatalf("slow sampling: %v", r.BusyHourCPU)
	}
	hs[10].Samples = 61
	if r := Read(Node{Name: "de1", Hours: hs}); r.BusyHourCPU != 95 {
		t.Fatalf("an hour over half full left out: %v", r.BusyHourCPU)
	}
	// a day-long window has a day less the hour that is running
	day := build(steady(24, 10, 0.4, daily(0, 40), nil))
	day[23].Samples = 100
	if r := Read(Node{Name: "de1", Hours: day}); r.Fit != FitFewHours {
		t.Fatalf("23 settled hours fitted without a window: %s", r.Fit)
	}
	if r := Read(Node{Name: "de1", Hours: day, WindowHours: 24}); r.Fit != FitOK || r.Headroom == nil {
		t.Fatalf("a 24 h window: %s", r.Fit)
	}
}

// Busy with nobody on it is not people's load: no advice to move them, one that says so.
func TestBusyWithFewPeople(t *testing.T) {
	for _, c := range []struct {
		people float64
		want   string
	}{{1, RecCPUBusyFewPeople}, {2, RecCPUBusyFewPeople}, {3, RecCPUBusy}, {12, RecCPUBusy}} {
		hs := build(steady(30, 70, 0, func(int) float64 { return c.people }, nil))
		r := Read(Node{Name: "se1", Connected: true, Hours: hs})
		other := RecCPUBusy
		if c.want == RecCPUBusy {
			other = RecCPUBusyFewPeople
		}
		if r.Status != StatusBusy || rec(r, c.want) == nil || rec(r, other) != nil {
			t.Errorf("%v people: %s", c.people, ids(r))
		}
	}
	// with another node that has room, the few-people advice still names no target
	busy := Node{ID: "n1", Name: "se1", Connected: true, Hours: build(steady(30, 70, 0, func(int) float64 { return 1 }, nil))}
	roomy := Node{ID: "n2", Name: "nl1", Connected: true, Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))}
	reports, _ := Fleet([]Node{busy, roomy})
	if c := rec(reports[0], RecCPUBusyFewPeople); c == nil || c.Params["target"] != "" || c.Params["people"] != "1" {
		t.Fatalf("%+v", reports[0].Recs)
	}
}

// The headroom is drawn from a fit; a fit that is noise, or a line followed far beyond the people it saw, gives no number.
func TestHeadroomIsHeldBack(t *testing.T) {
	// a noise slope: the CPU wanders 2 to 6 percent whatever the people do
	var xs, ys []float64
	noisy := make([]hour, 72)
	for i := range noisy {
		p := float64(i % 24)
		c := 4 + 0.02*p + float64((i*7)%5)
		noisy[i] = hour{cpu: c, peak: c + 5, people: p, mbps: p}
		xs, ys = append(xs, p), append(ys, c)
	}
	if slope, _, r2, ok := leastSquares(xs, ys); !ok || slope <= 0 || r2 >= MinFitR2 {
		t.Fatalf("the fixture is not a noise slope: %v %v", slope, r2)
	}
	r := Read(Node{Name: "fi2", Hours: build(noisy), WindowHours: 168})
	if r.Fit != FitNoTrend || r.Headroom != nil || r.CPUPer100 != nil {
		t.Fatalf("noise fitted: %s headroom %v", r.Fit, r.Headroom)
	}
	// a clean, tiny cost: (80 - 1.2) / 0.01 = 7880 on paper; 20 people have been seen, so no more than 5 x 20
	r = Read(Node{Name: "fi2", Hours: build(steady(48, 1, 0.01, daily(0, 20), nil)), WindowHours: 168})
	if r.Fit != FitOK || r.Headroom == nil || *r.Headroom != MaxHeadroomOverPeak*20 {
		t.Fatalf("a tiny cost: %s headroom %v", r.Fit, r.Headroom)
	}
	// few people seen: the cut is not below 5 x 10
	r = Read(Node{Name: "fi2", Hours: build(steady(48, 1, 0.01, daily(0, 5), nil)), WindowHours: 168})
	if r.Headroom == nil || *r.Headroom != MaxHeadroomOverPeak*MinPeopleBasis {
		t.Fatalf("a small peak: %s %v", r.Fit, r.Headroom)
	}
	// an honest fit that stays inside the cut is not touched (the 135 of TestFitAndHeadroom has 40 people seen, 200 allowed)
	if r := Read(Node{Name: "de1", Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))}); r.Headroom == nil || *r.Headroom != 135 {
		t.Fatalf("headroom %v", r.Headroom)
	}
}

// Case 1b: the owner typed 250 Mbit/s; a stored speed test between nodes went 308. The number is wrong, so a link judged against
// it is busy at most and the advice is to check the number, not to move people.
func TestLinkWiderThanTheNumberSet(t *testing.T) {
	full := week(95, 99) // against 100 Mbit/s this is a link at the line, all week
	for _, c := range []struct {
		name     string
		link     uint32
		measured uint32
		hours    []raw
		src      string // "nic" leaves the link to the card
		seen     uint32
		status   string
		code     string
	}{
		{"a speed test over the number", 100, 308, full, "", 308, StatusBusy, RecBandwidthLimitLow},
		{"a test under the number does not say so", 100, 80, full, "", 0, StatusOverloaded, RecBandwidthBusy},
		{"an hourly peak over the number", 100, 0, withBurst(full, 50, 1, 140), "", 140, StatusBusy, RecBandwidthLimitLow},
		{"a quiet node and a fast test", 100, 308, week(5, 25), "", 308, StatusOK, RecBandwidthLimitLow},
		{"the card's own speed is not doubted", 100, 308, full, "nic", 0, StatusOverloaded, RecBandwidthBusy},
	} {
		hs := rawHours(c.hours)
		n := Node{Name: "be", Hours: hs, WindowHours: 168, MeasuredMbps: c.measured}
		if c.src == "nic" {
			for i := range hs {
				hs[i].ExtSamples, hs[i].NICSpeedMbps = 360, 100
			}
		} else {
			n.BandwidthMbps = c.link
		}
		r := Read(n)
		if r.Status != c.status || r.LinkSeenMbps != c.seen || rec(r, c.code) == nil {
			t.Errorf("%s: %s, seen %d, recs %v", c.name, ids(r), r.LinkSeenMbps, codes(r))
			continue
		}
		if c.code == RecBandwidthLimitLow {
			p := rec(r, c.code).Params
			if p["link_mbps"] != "100" || p["seen_mbps"] != fmt.Sprint(c.seen) || rec(r, RecBandwidthBusy) != nil {
				t.Errorf("%s: %+v", c.name, r.Recs)
			}
		}
	}
}
