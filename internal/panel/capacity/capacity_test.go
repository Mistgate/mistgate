package capacity

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// t0 is the start of a UTC day; hour i of a fixture starts i hours after it.
const t0 = int64(1_800_000_000 / 86400 * 86400)

const mib = 1 << 20

// hour is one hour of a fixture: CPU mean and peak, people online (mean and peak), traffic in Mbit/s, and the resource
// fields when ext is set.
type hour struct {
	cpu, peak, people float64
	mbps              float64
	ext               *store.HostHour // only its resource fields are read
}

func build(hs []hour) []store.HostHour {
	out := make([]store.HostHour, len(hs))
	for i, h := range hs {
		bps := uint64(h.mbps * 1e6)
		x := store.HostHour{
			HourStart: t0 + int64(i)*3600, Samples: 360, CPUAvg: h.cpu, CPUMax: max(h.peak, h.cpu),
			RAMUsedMax: 400 * mib, RAMTotal: 2048 * mib, RxAvg: bps, RxMax: bps * 3 / 2, TxAvg: bps, TxMax: bps * 3 / 2,
			SessionsMax: uint32(math.Ceil(h.people)), SessionsAvg: h.people,
		}
		if e := h.ext; e != nil {
			x.ExtSamples = 360
			x.StealAvg, x.StealMax, x.SwapUsedMax, x.ConntrackMax, x.ConntrackLimit = e.StealAvg, e.StealMax, e.SwapUsedMax, e.ConntrackMax, e.ConntrackLimit
			x.NICSpeedMbps, x.AgentCPUAvg, x.AgentCPUMax, x.AgentRSSMax = e.NICSpeedMbps, e.AgentCPUAvg, e.AgentCPUMax, e.AgentRSSMax
		}
		out[i] = x
	}
	return out
}

// steady is n hours where the CPU follows the people exactly: cpu = base + perPerson*people, people cycling over a day.
func steady(n int, base, perPerson float64, people func(i int) float64, ext *store.HostHour) []hour {
	out := make([]hour, n)
	for i := range out {
		p := people(i)
		c := base + perPerson*p
		out[i] = hour{cpu: c, peak: c + 10, people: p, mbps: p * 2, ext: ext}
	}
	return out
}

func daily(lo, hi float64) func(int) float64 {
	return func(i int) float64 { return lo + (hi-lo)*float64(i%24)/23 }
}

func rec(r Report, code string) *Rec {
	for i := range r.Recs {
		if r.Recs[i].Code == code {
			return &r.Recs[i]
		}
	}
	return nil
}

func codes(r Report) []string {
	var out []string
	for _, x := range r.Recs {
		out = append(out, x.Code)
	}
	return out
}

// finiteEverywhere walks a report and fails on any NaN or Inf, and checks that it encodes as JSON (which refuses them).
func finiteEverywhere(t *testing.T, v any) {
	t.Helper()
	var walk func(reflect.Value, string)
	walk = func(x reflect.Value, path string) {
		switch x.Kind() {
		case reflect.Float32, reflect.Float64:
			if f := x.Float(); math.IsNaN(f) || math.IsInf(f, 0) {
				t.Errorf("%s = %v", path, f)
			}
		case reflect.Pointer, reflect.Interface:
			if !x.IsNil() {
				walk(x.Elem(), path)
			}
		case reflect.Struct:
			for i := 0; i < x.NumField(); i++ {
				walk(x.Field(i), path+"."+x.Type().Field(i).Name)
			}
		case reflect.Slice:
			for i := 0; i < x.Len(); i++ {
				walk(x.Index(i), path)
			}
		}
	}
	walk(reflect.ValueOf(v), "report")
	if _, err := json.Marshal(v); err != nil {
		t.Errorf("does not encode: %v", err)
	}
}

func TestEmpty(t *testing.T) {
	r := Read(Node{ID: "nod_1", Name: "de1"})
	if r.Status != StatusUnknown || r.Bottleneck != "" || r.HoursWithData != 0 || r.Fit != FitFewHours || len(r.Recs) != 0 {
		t.Fatalf("empty node: %+v", r)
	}
	if r.StealP95 != nil || r.RAMPeakPct != nil || r.Headroom != nil || r.CPUPer100 != nil || r.AgentCPUAvg != nil || r.BandwidthPeakPct != nil {
		t.Fatalf("an empty node knows something: %+v", r)
	}
	finiteEverywhere(t, r)
	// Hours without a sample are no hours.
	if r := Read(Node{Name: "de1", Hours: []store.HostHour{{HourStart: t0}}}); r.Status != StatusUnknown || r.HoursWithData != 0 {
		t.Fatalf("an hour without samples counted: %+v", r)
	}
	reports, s := Fleet(nil)
	if len(reports) != 0 || s.OK+s.Busy+s.Overloaded+s.Unknown != 0 || s.MostLoaded != nil || s.MostFree != nil {
		t.Fatalf("empty fleet: %+v %+v", reports, s)
	}
	// A disk that is nearly full is worth saying even before the first hour of history.
	r = Read(Node{Name: "de1", DiskUsed: 95, DiskTotal: 100})
	if r.Status != StatusUnknown || !slices.Equal(codes(r), []string{RecDiskFilling}) || r.Recs[0].Severity != SevCrit {
		t.Fatalf("disk before history: %+v", r)
	}
}

func TestSingleHour(t *testing.T) {
	r := Read(Node{Name: "de1", Hours: build([]hour{{cpu: 50, peak: 70, people: 12, mbps: 30}})})
	if r.Status != StatusOK || r.HoursWithData != 1 || r.BusyHourCPU != 50 || r.BusyHourUnix != t0 || r.PeopleBusyHour != 12 || r.PeoplePeak != 12 {
		t.Fatalf("single hour: %+v", r)
	}
	if r.CPUPeakP95 != 70 || r.Fit != FitFewHours || r.Headroom != nil || r.CPUPer100 != nil || r.MbpsPerPerson != nil {
		t.Fatalf("one hour carries no fit: %+v", r)
	}
	if r.Bottleneck != ResCPU || math.Abs(r.Pressure-70.0/97) > 1e-9 { // the peak, 70/97, is closer to its limit than the mean, 50/80
		t.Fatalf("bottleneck %q pressure %v", r.Bottleneck, r.Pressure)
	}
	if r.RAMPeakPct == nil || math.Abs(*r.RAMPeakPct-400.0/2048*100) > 1e-9 {
		t.Fatalf("ram %v", r.RAMPeakPct)
	}
	finiteEverywhere(t, r)
}

// An agent that predates the resource fields: CPU, RAM and traffic are read, steal, swap, conntrack and the agent's own
// cost stay unknown, and the report asks for an update.
func TestOldAgent(t *testing.T) {
	r := Read(Node{Name: "de1", Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))})
	if r.ExtHours != 0 || r.StealP95 != nil || r.SwapUsedMax != nil || r.ConntrackPeakPct != nil || r.AgentCPUAvg != nil || r.AgentCPUMax != nil ||
		r.AgentRSSMax != nil || r.AgentRSSGrowthPctPerDay != nil || r.LinkMbps != 0 || r.BandwidthPeakPct != nil {
		t.Fatalf("an old agent's hours say something they cannot: %+v", r)
	}
	if !slices.Equal(codes(r), []string{RecUpdateAgent}) || r.Recs[0].Severity != SevInfo {
		t.Fatalf("recs = %v", codes(r))
	}
	if r.Fit != FitOK || r.CPUPer100 == nil || math.Abs(*r.CPUPer100-40) > 1e-6 {
		t.Fatalf("the fit needs no resource fields: fit=%s cpu/100=%v", r.Fit, r.CPUPer100)
	}
	finiteEverywhere(t, r)
}

func TestIdleNode(t *testing.T) {
	ext := &store.HostHour{StealAvg: 0.5, AgentCPUAvg: 0.2, AgentCPUMax: 1, AgentRSSMax: 40 * mib, ConntrackMax: 30, ConntrackLimit: 262144}
	hs := make([]hour, 72)
	for i := range hs {
		hs[i] = hour{cpu: 2, peak: 5, ext: ext}
	}
	r := Read(Node{Name: "nl1", Connected: true, Hours: build(hs)})
	if r.Status != StatusOK || r.Bottleneck != "" || r.Fit != FitFlat || r.Headroom != nil || r.CPUPer100 != nil || r.MbpsPerPerson != nil {
		t.Fatalf("idle node: %+v", r)
	}
	if len(r.Recs) != 0 || r.PeoplePeak != 0 || r.PeopleBusyHour != 0 {
		t.Fatalf("idle node recs %v", codes(r))
	}
	if r.AgentCPUAvg == nil || *r.AgentCPUAvg != 0.2 || r.AgentRSSGrowthPctPerDay == nil || *r.AgentRSSGrowthPctPerDay != 0 {
		t.Fatalf("agent: %v %v", r.AgentCPUAvg, r.AgentRSSGrowthPctPerDay)
	}
	finiteEverywhere(t, r)
}

// The fit: CPU = 10 + 0.4 x people exactly, so 40% per 100 people with R² 1, and the busy hour (40 people, 26%) leaves
// (80 - 26) / 0.4 = 135 people of room.
func TestFitAndHeadroom(t *testing.T) {
	r := Read(Node{Name: "de1", Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))})
	if r.Fit != FitOK || r.CPUPer100 == nil || math.Abs(*r.CPUPer100-40) > 1e-6 || r.CPUFitR2 == nil || math.Abs(*r.CPUFitR2-1) > 1e-9 {
		t.Fatalf("fit: %s %v %v", r.Fit, r.CPUPer100, r.CPUFitR2)
	}
	if math.Abs(r.BusyHourCPU-26) > 1e-9 || r.PeopleBusyHour != 40 || r.BusyHourUnix != t0+47*3600 {
		t.Fatalf("busy hour %v at %d with %v people", r.BusyHourCPU, r.BusyHourUnix, r.PeopleBusyHour)
	}
	if r.Headroom == nil || *r.Headroom != 135 {
		t.Fatalf("headroom %v", r.Headroom)
	}
	// 2 Mbit/s per person (traffic = 2 x people), through the origin.
	if r.MbpsPerPerson == nil || math.Abs(*r.MbpsPerPerson-2) > 1e-6 {
		t.Fatalf("mbps per person %v", r.MbpsPerPerson)
	}

	// With the owner's 200 Mbit/s the link is what runs out first: the busy hour carries 80 Mbit/s, 90% of the link is 180,
	// so 50 more people at 2 Mbit/s.
	r = Read(Node{Name: "de1", BandwidthMbps: 200, Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))})
	if r.Headroom == nil || *r.Headroom != 50 || r.LinkSource != "configured" {
		t.Fatalf("headroom with a link: %v %q", r.Headroom, r.LinkSource)
	}

	// Fewer than a day of hours: no fit, whatever the data.
	if r := Read(Node{Name: "de1", Hours: build(steady(23, 10, 0.4, daily(0, 40), nil))}); r.Fit != FitFewHours || r.Headroom != nil || r.MbpsPerPerson != nil {
		t.Fatalf("23 hours fitted: %+v", r)
	}
	// People that barely move: no fit.
	if r := Read(Node{Name: "de1", Hours: build(steady(48, 10, 0.4, daily(20, 23), nil))}); r.Fit != FitFlat || r.CPUPer100 != nil || r.MbpsPerPerson == nil {
		t.Fatalf("flat people fitted: %+v", r)
	}
	// CPU that falls as people rise is noise, not a cost.
	if r := Read(Node{Name: "de1", Hours: build(steady(48, 60, -0.5, daily(0, 40), nil))}); r.Fit != FitNoTrend || r.CPUPer100 != nil || r.Headroom != nil {
		t.Fatalf("a falling CPU fitted: %+v", r)
	}
}

func TestSaturatedNode(t *testing.T) {
	ext := &store.HostHour{StealAvg: 3, NICSpeedMbps: 1000, AgentCPUAvg: 30, AgentCPUMax: 45, AgentRSSMax: 300 * mib, ConntrackMax: 1000, ConntrackLimit: 262144}
	r := Read(Node{Name: "de1", Connected: true, Hours: build(steady(48, 20, 0.5, daily(40, 150), ext))})
	// busy hour: 20 + 0.5 x 150 = 95% (clamped peaks at 100)
	if r.Status != StatusOverloaded || r.Bottleneck != ResCPU || r.Pressure < 1 {
		t.Fatalf("saturated: status %s bottleneck %s pressure %v", r.Status, r.Bottleneck, r.Pressure)
	}
	if r.Headroom == nil || *r.Headroom != 0 {
		t.Fatalf("an overloaded node has room: %v", r.Headroom)
	}
	c := rec(r, RecCPUBusy)
	// peak_pct is the typical peak (p75 of the hourly peaks), not the highest one
	if c == nil || c.Severity != SevCrit || c.Params["cpu_pct"] != "95" || c.Params["peak_pct"] != "91" || r.Recs[0].Code != RecCPUBusy {
		t.Fatalf("recs %+v", r.Recs)
	}
	if r.CPUPeakP95 != 100 || r.LinkMbps != 1000 || r.LinkSource != "nic" {
		t.Fatalf("peaks %v link %d %q", r.CPUPeakP95, r.LinkMbps, r.LinkSource)
	}
	finiteEverywhere(t, r)
}

// A small box at a 10% busy hour whose CPU touched 100% for a moment in three hours of 29 (the distribution's own updates):
// not overloaded, its headroom stands, no advice to move people. The same spikes in most hours make it busy, never overloaded.
func TestCPUSpikes(t *testing.T) {
	hours := func(spiky func(i int) bool) []hour {
		out := steady(29, 4, 0.5, daily(2, 12), &store.HostHour{AgentCPUAvg: 4, AgentCPUMax: 20, ConntrackLimit: 65536})
		for i := range out {
			if spiky(i) {
				out[i].peak = 100
			}
		}
		return out
	}
	rare := Read(Node{Name: "de1", Connected: true, Hours: build(hours(func(i int) bool { return i == 3 || i == 14 || i == 27 }))})
	if rare.Status != StatusOK || rec(rare, RecCPUBusy) != nil || rare.Headroom == nil || *rare.Headroom == 0 {
		t.Fatalf("rare spikes: status %s headroom %v recs %+v", rare.Status, rare.Headroom, rare.Recs)
	}
	if rare.CPUPeakP95 != 100 || rare.CPUPeakTypical >= CPUPeakBusyPct {
		t.Fatalf("rare spikes: p95 %v typical %v", rare.CPUPeakP95, rare.CPUPeakTypical)
	}
	often := Read(Node{Name: "de1", Connected: true, Hours: build(hours(func(i int) bool { return i%4 != 0 }))})
	c := rec(often, RecCPUBusy)
	if often.Status != StatusBusy || often.Bottleneck != ResCPU || often.Pressure >= 1 || c == nil || c.Severity != SevWarn || c.Params["peak_pct"] != "100" {
		t.Fatalf("frequent spikes: status %s bottleneck %s pressure %v recs %+v", often.Status, often.Bottleneck, often.Pressure, often.Recs)
	}
	if often.Headroom == nil || *often.Headroom == 0 {
		t.Fatalf("frequent spikes at a low mean keep their headroom: %v", often.Headroom)
	}
	finiteEverywhere(t, rare)
	finiteEverywhere(t, often)
}

func TestEachResource(t *testing.T) {
	base := func(ext *store.HostHour) []store.HostHour { return build(steady(48, 10, 0.2, daily(0, 30), ext)) }
	t.Run("steal", func(t *testing.T) {
		r := Read(Node{Name: "de1", Hours: base(&store.HostHour{StealAvg: 30})})
		s := rec(r, RecStealHigh)
		if r.Status != StatusOverloaded || r.Bottleneck != ResSteal || s == nil || s.Severity != SevCrit || s.Params["steal_pct"] != "30" {
			t.Fatalf("%s %s %+v", r.Status, r.Bottleneck, r.Recs)
		}
		if r := Read(Node{Name: "de1", Hours: base(&store.HostHour{StealAvg: 12})}); r.Status != StatusBusy || rec(r, RecStealHigh).Severity != SevWarn {
			t.Fatalf("steal 12: %s %+v", r.Status, r.Recs)
		}
	})
	t.Run("conntrack", func(t *testing.T) {
		r := Read(Node{Name: "de1", Hours: base(&store.HostHour{ConntrackMax: 900, ConntrackLimit: 1000})})
		c := rec(r, RecConntrackNearLimit)
		if r.Status != StatusBusy || r.Bottleneck != ResConntrack || c == nil || c.Params["fill_pct"] != "90" || c.Params["limit"] != "1000" {
			t.Fatalf("%s %s %+v", r.Status, r.Bottleneck, r.Recs)
		}
		// No limit (conntrack not loaded): not known, not zero.
		if r := Read(Node{Name: "de1", Hours: base(&store.HostHour{ConntrackMax: 0, ConntrackLimit: 0})}); r.ConntrackPeakPct != nil {
			t.Fatalf("conntrack without a limit: %v", *r.ConntrackPeakPct)
		}
	})
	t.Run("bandwidth", func(t *testing.T) {
		// 30 people x 2 Mbit/s = 60 Mbit/s mean, 90 peak: 90% of a 100 Mbit/s link.
		r := Read(Node{Name: "de1", BandwidthMbps: 100, Hours: base(nil)})
		b := rec(r, RecBandwidthBusy)
		if r.Status != StatusOverloaded || r.Bottleneck != ResBandwidth || b == nil || b.Params["peak_pct"] != "90" || b.Params["link_mbps"] != "100" {
			t.Fatalf("%s %s %+v", r.Status, r.Bottleneck, r.Recs)
		}
	})
	t.Run("ram and swap", func(t *testing.T) {
		hs := base(&store.HostHour{SwapUsedMax: 300 * mib})
		for i := range hs {
			hs[i].RAMUsedMax = 1900 * mib
		}
		r := Read(Node{Name: "de1", Hours: hs})
		if r.Status != StatusBusy || r.Bottleneck != ResRAM || rec(r, RecRAMHigh) == nil || rec(r, RecSwapInUse).Params["swap_mb"] != "300" {
			t.Fatalf("%s %s %+v", r.Status, r.Bottleneck, r.Recs)
		}
		// A few idle pages in swap are not worth a word.
		if r := Read(Node{Name: "de1", Hours: base(&store.HostHour{SwapUsedMax: 20 * mib})}); rec(r, RecSwapInUse) != nil || *r.SwapUsedMax != 20*mib {
			t.Fatalf("small swap: %+v", r.Recs)
		}
		// The RAM total of the facts stands in for an hour that has none.
		hs = base(nil)
		for i := range hs {
			hs[i].RAMTotal = 0
		}
		if r := Read(Node{Name: "de1", RAMTotal: 800 * mib, Hours: hs}); r.RAMPeakPct == nil || *r.RAMPeakPct != 50 {
			t.Fatalf("ram from the facts: %v", r.RAMPeakPct)
		}
	})
	t.Run("disk", func(t *testing.T) {
		r := Read(Node{Name: "de1", DiskUsed: 85, DiskTotal: 100, Hours: base(nil)})
		if r.Status != StatusBusy || r.Bottleneck != ResDisk || rec(r, RecDiskFilling).Params["disk_pct"] != "85" {
			t.Fatalf("%s %s %+v", r.Status, r.Bottleneck, r.Recs)
		}
	})
}

func TestAgentMemoryTrend(t *testing.T) {
	// Five days, the agent's peak climbing 20 MiB a day from 100 MiB while the people stay the same: a leak.
	var hs []hour
	for d := range 5 {
		for h := range 24 {
			hs = append(hs, hour{cpu: 20, peak: 30, people: float64(10 + h%5), mbps: 10,
				ext: &store.HostHour{AgentRSSMax: uint64(100+20*d) * mib, AgentCPUAvg: 2, AgentCPUMax: 4}})
		}
	}
	r := Read(Node{Name: "de1", Hours: build(hs)})
	g := rec(r, RecAgentMemoryGrowing)
	if g == nil || g.Params["growth_pct_day"] != "20" || g.Params["rss_mb"] != "180" || g.Params["days"] != "5" || r.AgentRSSDays != 5 {
		t.Fatalf("leak: %+v growth=%v", r.Recs, r.AgentRSSGrowthPctPerDay)
	}
	// The same climb with the people doubling alongside is growth, not a leak.
	hs = hs[:0]
	for d := range 5 {
		for h := range 24 {
			p := float64(10*(1+d)) + float64(h%5)
			hs = append(hs, hour{cpu: 20, peak: 30, people: p, ext: &store.HostHour{AgentRSSMax: uint64(100+20*d) * mib}})
		}
	}
	if r := Read(Node{Name: "de1", Hours: build(hs)}); rec(r, RecAgentMemoryGrowing) != nil {
		t.Fatalf("growth with the people called a leak: %+v", r.Recs)
	}
	// Two days are too few to call it.
	if r := Read(Node{Name: "de1", Hours: build(hs[:48])}); rec(r, RecAgentMemoryGrowing) != nil || r.AgentRSSGrowthPctPerDay != nil {
		t.Fatalf("two days: %+v", r)
	}
	// A saw (restarts) is not a steady climb.
	hs = hs[:0]
	for d := range 6 {
		for range 24 {
			rss := uint64(100) * mib
			if d%2 == 1 {
				rss = 300 * mib
			}
			hs = append(hs, hour{cpu: 20, peak: 30, people: 10, ext: &store.HostHour{AgentRSSMax: rss}})
		}
	}
	if r := Read(Node{Name: "de1", Hours: build(hs)}); rec(r, RecAgentMemoryGrowing) != nil {
		t.Fatalf("a saw called a leak: %+v", r.Recs)
	}
}

func TestFleetTargetsAndSummary(t *testing.T) {
	busy := Node{ID: "n1", Name: "de1", Connected: true, Hours: build(steady(48, 20, 0.5, daily(40, 90), nil))}   // busy hour 65%
	roomy := Node{ID: "n2", Name: "nl1", Connected: true, Hours: build(steady(48, 10, 0.4, daily(0, 40), nil))}   // headroom 135
	smaller := Node{ID: "n3", Name: "fi1", Connected: true, Hours: build(steady(48, 10, 0.4, daily(0, 60), nil))} // 34%: headroom 115
	offline := Node{ID: "n4", Name: "msk1", Connected: false, Hours: build(steady(48, 5, 0.1, daily(0, 10), nil))}
	empty := Node{ID: "n5", Name: "de2"}
	reports, s := Fleet([]Node{roomy, empty, offline, busy, smaller})

	names := make([]string, len(reports))
	for i, r := range reports {
		names[i] = r.Name
	}
	if !slices.Equal(names, []string{"de1", "fi1", "nl1", "msk1", "de2"}) {
		t.Fatalf("order %v", names)
	}
	c := rec(reports[0], RecCPUBusy)
	if c == nil || c.Params["target"] != "nl1" || c.Params["target_headroom"] != "135" {
		t.Fatalf("target: %+v", reports[0].Recs)
	}
	if s.OK != 3 || s.Busy != 1 || s.Overloaded != 0 || s.Unknown != 1 {
		t.Fatalf("summary counts %+v", s)
	}
	// de1 is busy but still below 80%: (80 - 65) / 0.5 = 30 people; msk1 (80 - 6) / 0.1 = 740.
	if s.HeadroomNodes != 4 || s.HeadroomPeople != 30+115+135+740 {
		t.Fatalf("headroom %d over %d nodes", s.HeadroomPeople, s.HeadroomNodes)
	}
	if len(s.MostLoaded) != 3 || s.MostLoaded[0].Name != "de1" || len(s.MostFree) != 3 || s.MostFree[0].Name != "msk1" || s.MostFree[1].Name != "nl1" {
		t.Fatalf("most loaded %+v, most free %+v", s.MostLoaded, s.MostFree)
	}

	// Nobody else has room: add a node.
	reports, _ = Fleet([]Node{busy, offline})
	if rec(reports[0], RecCPUBusyAddNode) == nil || rec(reports[0], RecCPUBusy) != nil {
		t.Fatalf("no target: %+v", reports[0].Recs)
	}
	finiteEverywhere(t, reports)
}

// Whatever an agent sent, the report holds only finite numbers.
func TestBogusRowsStayFinite(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	hs := build(steady(30, 10, 0.4, daily(0, 40), &store.HostHour{StealAvg: nan, AgentCPUAvg: inf, AgentRSSMax: 1 << 50, ConntrackMax: 5, ConntrackLimit: 1}))
	hs[3].CPUAvg, hs[4].CPUMax, hs[5].SessionsAvg, hs[6].SessionsAvg, hs[7].LoadMax = nan, inf, inf, -5, nan
	hs[8].RAMUsedMax, hs[8].RAMTotal = 1<<62, 1
	reports, s := Fleet([]Node{{Name: "de1", Hours: hs, DiskUsed: 1 << 62, DiskTotal: 1}, {Name: "nl1", Hours: build([]hour{{cpu: nan, people: inf}})}})
	finiteEverywhere(t, reports)
	finiteEverywhere(t, s)
}

func TestP95AndLeastSquares(t *testing.T) {
	if p95(nil) != 0 || p95([]float64{7}) != 7 {
		t.Fatal("p95 of little")
	}
	v := make([]float64, 100)
	for i := range v {
		v[i] = float64(100 - i) // 1..100, unsorted
	}
	if p95(v) != 95 {
		t.Fatalf("p95 = %v", p95(v))
	}
	if _, _, _, ok := leastSquares([]float64{1, 1, 1}, []float64{1, 2, 3}); ok {
		t.Fatal("no spread in x fitted")
	}
	s, i, r2, ok := leastSquares([]float64{0, 1, 2, 3}, []float64{1, 3, 5, 7})
	if !ok || s != 2 || i != 1 || r2 != 1 {
		t.Fatalf("fit %v %v %v", s, i, r2)
	}
	if _, _, r2, ok := leastSquares([]float64{0, 1, 2}, []float64{4, 4, 4}); !ok || r2 != 1 {
		t.Fatal("a flat y is an exact fit")
	}
}
