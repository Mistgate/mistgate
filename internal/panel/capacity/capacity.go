// Package capacity reads a node's hours of host load (store.HostHour) as an answer to "how loaded is it, what runs out
// first, and how many more people can it take". It is pure: no store, no clock, no I/O, so every number it gives is
// tested on fixed input. The OpsService (GetFleetLoad), the MCP tool capacity_report and the admin's Load view read it.
//
// Rules it keeps:
//
//   - "Not known" is never zero. An hour whose samples came from an agent that predates the resource fields
//     (ExtSamples == 0) says nothing about steal, swap, conntrack or the agent itself; those values are nil when no hour
//     of the window knows them, and the report asks for an agent update.
//   - Every float it returns is finite: a division by a zero count, a fit over flat data or a bogus row gives nil (or 0
//     where 0 is the truth), never NaN or Inf.
//   - The per-person costs come from a least-squares fit over the hours, and only when the hours can carry one: at least
//     MinFitHours hours with samples and people online that varied by at least MinPeopleSpread. Otherwise Fit says why.
package capacity

import (
	"math"
	"sort"
	"strconv"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// Thresholds. "Busy" is worth a look; "overloaded" is what people already feel. The CPU ones are hourly: a busy hour at a
// 60% mean has minutes at the ceiling inside it. The others match the node doctor's, so the Load view and the doctor's
// alerts do not disagree.
const (
	CPUBusyPct = 60.0 // busy-hour CPU mean
	CPUOverPct = 80.0
	// The hourly CPU peaks are judged by their 75th percentile, the peak most hours stay under: a server's own upkeep (the
	// distribution's updates, a restart) spikes a small box to 100% for a moment in a few hours a day, and the 95th percentile
	// read that as an overloaded node at a 10% busy hour. Peaks alone make a node busy at most: what people feel is the mean.
	CPUPeakBusyPct   = 85.0 // p75 of the hourly CPU peaks
	CPUPeakOverPct   = 97.0 // the pressure scale of the peaks (never "overloaded" by itself)
	StealBusyPct     = 10.0 // p95 of the hourly steal means (doctor cpu_steal: 10 / 25 over 10 minutes)
	StealOverPct     = 25.0
	LinkBusyPct      = 70.0 // the busier direction's hourly peak against the link
	LinkOverPct      = 90.0
	RAMBusyPct       = 85.0 // hourly peak of used memory (MemTotal - MemAvailable)
	RAMOverPct       = 95.0
	ConntrackBusyPct = 80.0 // doctor conntrack_fill: 80 / 95
	ConntrackOverPct = 95.0
	DiskBusyPct      = 80.0 // doctor disk_space: 80 / 92
	DiskOverPct      = 92.0

	// HeadroomCPUPct is where the busy hour may go: the headroom is the people a node can add before its busy-hour CPU
	// mean reaches it.
	HeadroomCPUPct = CPUOverPct
	// MaxHeadroom caps a headroom that a tiny per-person cost would make absurd.
	MaxHeadroom = 10000

	// MinFitHours and MinPeopleSpread gate the per-person fit: a day of hours, and people online that moved enough for
	// the CPU to follow them.
	MinFitHours     = 24
	MinPeopleSpread = 4.0

	// SwapNoteBytes is the swap use worth a note: a few idle pages swapped out are normal.
	SwapNoteBytes = 100 << 20

	// The agent's memory: a leak shows as the daily peak climbing for days, steadily, faster than the people do.
	LeakMinDays      = 3
	LeakMinPctPerDay = 5.0
	LeakMinR2        = 0.6
)

// Statuses.
const (
	StatusOK         = "ok"
	StatusBusy       = "busy"
	StatusOverloaded = "overloaded"
	StatusUnknown    = "unknown" // no hour with samples in the window
)

// Resources a node can run out of (Report.Bottleneck).
const (
	ResCPU       = "cpu"
	ResSteal     = "steal"
	ResBandwidth = "bandwidth"
	ResRAM       = "ram"
	ResConntrack = "conntrack"
	ResDisk      = "disk"
)

// Fit outcomes (Report.Fit).
const (
	FitOK       = "ok"
	FitFewHours = "few_hours" // fewer than MinFitHours hours with samples
	FitFlat     = "flat"      // people online barely changed (an idle node among them)
	FitNoTrend  = "no_trend"  // the CPU does not rise with the people
)

// Recommendation codes. Params are plain decimal strings and node names; the UI and the assistant word them.
const (
	RecUpdateAgent        = "update_agent"         // the agent predates the resource fields: steal, swap, conntrack and its own cost are not known
	RecStealHigh          = "steal_high"           // steal_pct: the provider oversells the host; move the node
	RecConntrackNearLimit = "conntrack_near_limit" // fill_pct, limit: raise nf_conntrack_max or spread people
	RecCPUBusy            = "cpu_busy"             // cpu_pct, target, target_headroom: move people to the node with the most headroom
	RecCPUBusyAddNode     = "cpu_busy_add_node"    // cpu_pct: no other node has room; add one
	RecBandwidthBusy      = "bandwidth_busy"       // peak_pct, link_mbps, target (optional)
	RecRAMHigh            = "ram_high"             // ram_pct
	RecSwapInUse          = "swap_in_use"          // swap_mb
	RecDiskFilling        = "disk_filling"         // disk_pct
	RecAgentMemoryGrowing = "agent_memory_growing" // growth_pct_day, rss_mb, days
)

// Severities of a recommendation.
const (
	SevInfo = "info"
	SevWarn = "warn"
	SevCrit = "crit"
)

// Node is what the report reads about one node.
type Node struct {
	ID, Name string
	// CPUs and RAMTotal are the node's facts (0 = not known; the hours carry their own RAM total too).
	CPUs     uint32
	RAMTotal uint64
	// BandwidthMbps is the owner's capacity of the node (Node.bandwidth_mbps); 0 falls back to the NIC's link speed.
	BandwidthMbps uint32
	// DiskUsed and DiskTotal are the latest sample's root file system (DiskTotal 0 = not known).
	DiskUsed, DiskTotal uint64
	Connected           bool
	// Hours of host load in the window, any order.
	Hours []store.HostHour
}

// Rec is one recommendation.
type Rec struct {
	Code     string
	Severity string
	Params   map[string]string
}

// Report is one node's capacity read. Pointers are nil when the value is not known.
type Report struct {
	ID, Name  string
	CPUs      uint32
	Connected bool
	Status    string
	// Bottleneck is the resource closest to its limit, when one is at least halfway there; "" otherwise.
	Bottleneck string
	// Pressure is how close the bottleneck is to its "overloaded" threshold, 1 = at it. It orders nodes.
	Pressure float64

	HoursWithData, ExtHours int
	// The busy hour: the hour with the highest CPU mean, and the people online in it.
	BusyHourUnix   int64
	BusyHourCPU    float64
	PeopleBusyHour float64
	PeoplePeak     uint32
	CPUPeakP95     float64 // the highest peaks, shown; a few spikes push it to 100
	CPUPeakTypical float64 // p75 of the hourly peaks: what the status and the advice read

	StealP95         *float64
	ConntrackPeakPct *float64
	ConntrackLimit   uint64
	RAMPeakPct       *float64
	SwapUsedMax      *uint64
	DiskUsedPct      *float64

	RxPeakBps, TxPeakBps uint64
	// LinkMbps is the capacity the traffic is set against; LinkSource "configured" (the owner's), "nic" (the link speed the
	// agent reported) or "" (not known).
	LinkMbps         uint32
	LinkSource       string
	BandwidthPeakPct *float64

	AgentCPUAvg, AgentCPUMax  *float64
	AgentRSSMax               *uint64
	AgentRSSGrowthPctPerDay   *float64
	AgentRSSDays              int
	AgentRSSGrowthConsistentR float64 // R² of the daily-peak fit (0 when there is none)

	// Fit says whether the per-person costs could be read (FitOK) or why not.
	Fit           string
	CPUPer100     *float64 // CPU percent of the machine per 100 people online
	CPUFitR2      *float64
	MbpsPerPerson *float64
	// Headroom is the people the node could add before its busy-hour CPU reaches HeadroomCPUPct (and, with a known link
	// and per-person traffic, before the busy direction reaches LinkOverPct). 0 when the node is overloaded already.
	Headroom *int

	Recs []Rec
}

// Ref names a node in the summary.
type Ref struct {
	ID, Name    string
	BusyHourCPU float64
	Headroom    *int
	Status      string
}

// Summary is the fleet in a few numbers.
type Summary struct {
	OK, Busy, Overloaded, Unknown int
	// HeadroomPeople adds up the nodes whose headroom is known (HeadroomNodes of them).
	HeadroomPeople, HeadroomNodes int
	// MostLoaded are up to three nodes closest to a limit, most loaded first; MostFree up to three that are not busy, most
	// headroom first.
	MostLoaded, MostFree []Ref
}

// Fleet reads every node and the fleet as a whole. Reports are ordered most pressing first (overloaded, busy, ok, unknown;
// then by pressure, then by name).
func Fleet(nodes []Node) ([]Report, Summary) {
	out := make([]Report, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, Read(n))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := statusRank(out[i].Status), statusRank(out[j].Status)
		if a != b {
			return a > b
		}
		if out[i].Pressure != out[j].Pressure {
			return out[i].Pressure > out[j].Pressure
		}
		return out[i].Name < out[j].Name
	})
	addTargets(out)
	return out, summarize(out)
}

func statusRank(s string) int {
	switch s {
	case StatusOverloaded:
		return 3
	case StatusBusy:
		return 2
	case StatusOK:
		return 1
	}
	return 0
}

// Read is one node's report, without the fleet's targets (Fleet adds them to cpu_busy and bandwidth_busy).
func Read(n Node) Report {
	r := Report{ID: n.ID, Name: n.Name, CPUs: n.CPUs, Connected: n.Connected, Status: StatusUnknown, Fit: FitFewHours}
	hours := make([]store.HostHour, 0, len(n.Hours))
	for _, h := range n.Hours {
		if h.Samples > 0 {
			hours = append(hours, sane(h))
		}
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].HourStart < hours[j].HourStart })
	r.HoursWithData = len(hours)
	if n.DiskTotal > 0 {
		r.DiskUsedPct = ptr(pct(float64(n.DiskUsed), float64(n.DiskTotal)))
	}
	if len(hours) == 0 {
		if r.DiskUsedPct != nil && *r.DiskUsedPct >= DiskBusyPct {
			r.Recs = append(r.Recs, Rec{Code: RecDiskFilling, Severity: sevOf(*r.DiskUsedPct >= DiskOverPct), Params: map[string]string{"disk_pct": itoa(*r.DiskUsedPct)}})
		}
		return r
	}

	var peaks []float64
	var steals []float64
	var extSamples, agentCPUSum float64
	ramTotal := n.RAMTotal
	for i, h := range hours {
		peaks = append(peaks, h.CPUMax)
		if i == 0 || h.CPUAvg >= r.BusyHourCPU { // ties: the later hour
			r.BusyHourUnix, r.BusyHourCPU, r.PeopleBusyHour = h.HourStart, h.CPUAvg, h.SessionsAvg
		}
		r.PeoplePeak = max(r.PeoplePeak, h.SessionsMax)
		r.RxPeakBps, r.TxPeakBps = max(r.RxPeakBps, h.RxMax), max(r.TxPeakBps, h.TxMax)
		if total := cmp(h.RAMTotal, ramTotal); total > 0 {
			v := pct(float64(h.RAMUsedMax), float64(total))
			if r.RAMPeakPct == nil || v > *r.RAMPeakPct {
				r.RAMPeakPct = ptr(v)
			}
		}
		if h.ExtSamples == 0 {
			continue
		}
		r.ExtHours++
		steals = append(steals, h.StealAvg)
		if r.SwapUsedMax == nil || h.SwapUsedMax > *r.SwapUsedMax {
			r.SwapUsedMax = ptr(h.SwapUsedMax)
		}
		if h.ConntrackLimit > 0 {
			v := pct(float64(h.ConntrackMax), float64(h.ConntrackLimit))
			if r.ConntrackPeakPct == nil || v > *r.ConntrackPeakPct {
				r.ConntrackPeakPct, r.ConntrackLimit = ptr(v), h.ConntrackLimit
			}
		}
		if h.NICSpeedMbps > 0 {
			r.LinkMbps, r.LinkSource = h.NICSpeedMbps, "nic" // the latest hour wins (hours are in order)
		}
		w := float64(h.ExtSamples)
		extSamples += w
		agentCPUSum += h.AgentCPUAvg * w
		if r.AgentCPUMax == nil || h.AgentCPUMax > *r.AgentCPUMax {
			r.AgentCPUMax = ptr(h.AgentCPUMax)
		}
		if r.AgentRSSMax == nil || h.AgentRSSMax > *r.AgentRSSMax {
			r.AgentRSSMax = ptr(h.AgentRSSMax)
		}
	}
	r.CPUPeakP95 = p95(peaks)
	r.CPUPeakTypical = percentile(peaks, 0.75)
	if len(steals) > 0 {
		r.StealP95 = ptr(p95(steals))
	}
	if extSamples > 0 {
		r.AgentCPUAvg = ptr(finite(agentCPUSum / extSamples))
	}
	if n.BandwidthMbps > 0 {
		r.LinkMbps, r.LinkSource = n.BandwidthMbps, "configured"
	}
	if r.LinkMbps > 0 {
		r.BandwidthPeakPct = ptr(pct(float64(max(r.RxPeakBps, r.TxPeakBps)), float64(r.LinkMbps)*1e6))
	}
	r.leak(hours)
	r.fit(hours)
	r.judge()
	r.recommend()
	return r
}

// sane keeps a row from a bogus agent inside what a percentage and a count can be.
func sane(h store.HostHour) store.HostHour {
	clamp := func(v float64) float64 { return min(max(finite(v), 0), 100) }
	h.CPUAvg, h.CPUMax, h.SoftirqMax = clamp(h.CPUAvg), clamp(h.CPUMax), clamp(h.SoftirqMax)
	h.StealAvg, h.StealMax, h.IOWaitMax = clamp(h.StealAvg), clamp(h.StealMax), clamp(h.IOWaitMax)
	h.AgentCPUAvg, h.AgentCPUMax = clamp(h.AgentCPUAvg), clamp(h.AgentCPUMax)
	h.SessionsAvg = max(finite(h.SessionsAvg), 0)
	h.LoadMax = max(finite(h.LoadMax), 0)
	return h
}

// fit reads the per-person costs: CPU on people (least squares with an intercept: an idle node still uses some CPU) and
// traffic on people (through the origin: no people, no traffic).
func (r *Report) fit(hours []store.HostHour) {
	r.Fit = FitFewHours
	if len(hours) < MinFitHours {
		return
	}
	xs := make([]float64, len(hours))
	ys := make([]float64, len(hours))
	var sxy, sxx float64
	lo, hi := math.Inf(1), math.Inf(-1)
	var busiest float64 // the highest hourly mean of the busier direction, Mbit/s
	for i, h := range hours {
		xs[i], ys[i] = h.SessionsAvg, h.CPUAvg
		lo, hi = min(lo, h.SessionsAvg), max(hi, h.SessionsAvg)
		mbps := float64(max(h.RxAvg, h.TxAvg)) / 1e6
		busiest = max(busiest, mbps)
		sxy += h.SessionsAvg * mbps
		sxx += h.SessionsAvg * h.SessionsAvg
	}
	if sxx > 0 {
		if v := sxy / sxx; v > 0 && !math.IsInf(v, 0) {
			r.MbpsPerPerson = ptr(finite(v))
		}
	}
	if hi-lo < MinPeopleSpread {
		r.Fit = FitFlat
		return
	}
	slope, _, r2, ok := leastSquares(xs, ys)
	if !ok || slope <= 0 {
		r.Fit = FitNoTrend
		return
	}
	r.Fit = FitOK
	r.CPUPer100 = ptr(finite(slope * 100))
	r.CPUFitR2 = ptr(finite(r2))
	room := 0
	if r.BusyHourCPU < HeadroomCPUPct {
		room = int(math.Min(math.Floor((HeadroomCPUPct-r.BusyHourCPU)/slope+1e-6), MaxHeadroom)) // 1e-6: a fit's rounding is not a person
	}
	if r.LinkMbps > 0 && r.MbpsPerPerson != nil && *r.MbpsPerPerson > 0 {
		left := float64(r.LinkMbps)*LinkOverPct/100 - busiest
		bw := 0
		if left > 0 {
			bw = int(math.Min(math.Floor(left / *r.MbpsPerPerson + 1e-6), MaxHeadroom))
		}
		room = min(room, bw)
	}
	r.Headroom = &room
}

// leak reads the trend of the agent's daily peak memory: a leak climbs day after day, steadily, faster than the people.
func (r *Report) leak(hours []store.HostHour) {
	type day struct {
		rss    uint64
		people uint32
	}
	byDay := map[int64]*day{}
	for _, h := range hours {
		if h.ExtSamples == 0 || h.AgentRSSMax == 0 {
			continue
		}
		k := h.HourStart / 86400
		d := byDay[k]
		if d == nil {
			d = &day{}
			byDay[k] = d
		}
		d.rss, d.people = max(d.rss, h.AgentRSSMax), max(d.people, h.SessionsMax)
	}
	r.AgentRSSDays = len(byDay)
	if len(byDay) < LeakMinDays {
		return
	}
	keys := make([]int64, 0, len(byDay))
	for k := range byDay {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	xs, rss, people := make([]float64, len(keys)), make([]float64, len(keys)), make([]float64, len(keys))
	for i, k := range keys {
		xs[i], rss[i], people[i] = float64(k-keys[0]), float64(byDay[k].rss), float64(byDay[k].people)
	}
	slope, icpt, r2, ok := leastSquares(xs, rss)
	if !ok {
		return
	}
	base := icpt // the fitted first day
	if base <= 0 {
		base = mean(rss)
	}
	if base <= 0 {
		return
	}
	g := finite(slope / base * 100)
	r.AgentRSSGrowthPctPerDay = ptr(g)
	r.AgentRSSGrowthConsistentR = finite(r2)
	if g < LeakMinPctPerDay || r2 < LeakMinR2 {
		return
	}
	// More people need more memory: only a climb well ahead of theirs is a leak.
	if ps, pi, _, ok := leastSquares(xs, people); ok && pi > 0 && ps/pi*100 >= g/2 {
		return
	}
	r.Recs = append(r.Recs, Rec{Code: RecAgentMemoryGrowing, Severity: SevWarn, Params: map[string]string{
		"growth_pct_day": itoa(g), "rss_mb": strconv.FormatUint(uint64(rss[len(rss)-1])>>20, 10), "days": strconv.Itoa(len(keys)),
	}})
}

// judge sets the status, the bottleneck and the pressure from every resource that is known.
func (r *Report) judge() {
	type res struct {
		name       string
		value      float64
		busy, over float64
		busyOnly   bool // may make the node busy, never overloaded
	}
	var all []res
	all = append(all, res{ResCPU, r.BusyHourCPU, CPUBusyPct, CPUOverPct, false}, res{ResCPU, r.CPUPeakTypical, CPUPeakBusyPct, CPUPeakOverPct, true})
	if r.StealP95 != nil {
		all = append(all, res{ResSteal, *r.StealP95, StealBusyPct, StealOverPct, false})
	}
	if r.BandwidthPeakPct != nil {
		all = append(all, res{ResBandwidth, *r.BandwidthPeakPct, LinkBusyPct, LinkOverPct, false})
	}
	if r.RAMPeakPct != nil {
		all = append(all, res{ResRAM, *r.RAMPeakPct, RAMBusyPct, RAMOverPct, false})
	}
	if r.ConntrackPeakPct != nil {
		all = append(all, res{ResConntrack, *r.ConntrackPeakPct, ConntrackBusyPct, ConntrackOverPct, false})
	}
	if r.DiskUsedPct != nil {
		all = append(all, res{ResDisk, *r.DiskUsedPct, DiskBusyPct, DiskOverPct, false})
	}
	r.Status = StatusOK
	for _, x := range all {
		p := finite(x.value / x.over)
		if x.busyOnly {
			p = min(p, 0.99) // below the "overloaded" line, whatever the peaks
		}
		if p > r.Pressure {
			r.Pressure, r.Bottleneck = p, x.name
		}
		switch {
		case x.value >= x.over && !x.busyOnly:
			r.Status = StatusOverloaded
		case x.value >= x.busy && r.Status == StatusOK:
			r.Status = StatusBusy
		}
	}
	if r.Pressure < 0.5 {
		r.Bottleneck = ""
	}
	if r.Status == StatusOverloaded && r.Headroom != nil {
		zero := 0
		r.Headroom = &zero
	}
}

// recommend lists what to do, most severe first. cpu_busy and bandwidth_busy get their target from Fleet.
func (r *Report) recommend() {
	add := func(code, sev string, kv ...string) {
		p := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = kv[i+1]
		}
		r.Recs = append(r.Recs, Rec{Code: code, Severity: sev, Params: p})
	}
	if r.ExtHours == 0 {
		add(RecUpdateAgent, SevInfo)
	}
	if r.StealP95 != nil && *r.StealP95 >= StealBusyPct {
		add(RecStealHigh, sevOf(*r.StealP95 >= StealOverPct), "steal_pct", itoa(*r.StealP95))
	}
	if r.ConntrackPeakPct != nil && *r.ConntrackPeakPct >= ConntrackBusyPct {
		add(RecConntrackNearLimit, sevOf(*r.ConntrackPeakPct >= ConntrackOverPct), "fill_pct", itoa(*r.ConntrackPeakPct), "limit", strconv.FormatUint(r.ConntrackLimit, 10))
	}
	if r.BusyHourCPU >= CPUBusyPct || r.CPUPeakTypical >= CPUPeakBusyPct {
		add(RecCPUBusy, sevOf(r.BusyHourCPU >= CPUOverPct), "cpu_pct", itoa(r.BusyHourCPU), "peak_pct", itoa(r.CPUPeakTypical))
	}
	if r.BandwidthPeakPct != nil && *r.BandwidthPeakPct >= LinkBusyPct {
		add(RecBandwidthBusy, sevOf(*r.BandwidthPeakPct >= LinkOverPct), "peak_pct", itoa(*r.BandwidthPeakPct), "link_mbps", strconv.Itoa(int(r.LinkMbps)))
	}
	if r.RAMPeakPct != nil && *r.RAMPeakPct >= RAMBusyPct {
		add(RecRAMHigh, sevOf(*r.RAMPeakPct >= RAMOverPct), "ram_pct", itoa(*r.RAMPeakPct))
	}
	if r.SwapUsedMax != nil && *r.SwapUsedMax >= SwapNoteBytes {
		add(RecSwapInUse, SevInfo, "swap_mb", strconv.FormatUint(*r.SwapUsedMax>>20, 10))
	}
	if r.DiskUsedPct != nil && *r.DiskUsedPct >= DiskBusyPct {
		add(RecDiskFilling, sevOf(*r.DiskUsedPct >= DiskOverPct), "disk_pct", itoa(*r.DiskUsedPct))
	}
	sortRecs(r.Recs)
}

func sortRecs(recs []Rec) {
	rank := map[string]int{SevCrit: 0, SevWarn: 1, SevInfo: 2}
	sort.SliceStable(recs, func(i, j int) bool { return rank[recs[i].Severity] < rank[recs[j].Severity] })
}

func sevOf(over bool) string {
	if over {
		return SevCrit
	}
	return SevWarn
}

// addTargets names, in every cpu_busy and bandwidth_busy, the other node with the most headroom; with none, cpu_busy becomes
// cpu_busy_add_node.
func addTargets(reports []Report) {
	for i := range reports {
		r := &reports[i]
		for k := range r.Recs {
			rec := &r.Recs[k]
			if rec.Code != RecCPUBusy && rec.Code != RecBandwidthBusy {
				continue
			}
			best := -1
			for j := range reports {
				o := reports[j]
				if j == i || o.Status != StatusOK || o.Headroom == nil || *o.Headroom <= 0 || !o.Connected {
					continue
				}
				if best < 0 || *o.Headroom > *reports[best].Headroom || *o.Headroom == *reports[best].Headroom && o.Name < reports[best].Name {
					best = j
				}
			}
			if best >= 0 {
				rec.Params["target"] = reports[best].Name
				rec.Params["target_headroom"] = strconv.Itoa(*reports[best].Headroom)
			} else if rec.Code == RecCPUBusy {
				rec.Code = RecCPUBusyAddNode
			}
		}
	}
}

func summarize(reports []Report) Summary {
	var s Summary
	for _, r := range reports {
		switch r.Status {
		case StatusOK:
			s.OK++
		case StatusBusy:
			s.Busy++
		case StatusOverloaded:
			s.Overloaded++
		default:
			s.Unknown++
		}
		if r.Headroom != nil {
			s.HeadroomPeople += *r.Headroom
			s.HeadroomNodes++
		}
	}
	ref := func(r Report) Ref {
		return Ref{ID: r.ID, Name: r.Name, BusyHourCPU: r.BusyHourCPU, Headroom: r.Headroom, Status: r.Status}
	}
	for _, r := range reports { // already most pressing first
		if len(s.MostLoaded) == 3 {
			break
		}
		if r.Status != StatusUnknown && r.Pressure > 0 {
			s.MostLoaded = append(s.MostLoaded, ref(r))
		}
	}
	free := make([]Report, 0, len(reports))
	for _, r := range reports {
		if r.Status == StatusOK && r.Headroom != nil && *r.Headroom > 0 {
			free = append(free, r)
		}
	}
	sort.SliceStable(free, func(i, j int) bool {
		if *free[i].Headroom != *free[j].Headroom {
			return *free[i].Headroom > *free[j].Headroom
		}
		return free[i].Name < free[j].Name
	})
	for i := 0; i < len(free) && i < 3; i++ {
		s.MostFree = append(s.MostFree, ref(free[i]))
	}
	return s
}

// leastSquares fits y = slope*x + intercept. ok is false for fewer than two points or no spread in x.
func leastSquares(xs, ys []float64) (slope, intercept, r2 float64, ok bool) {
	n := float64(len(xs))
	if len(xs) < 2 || len(xs) != len(ys) {
		return 0, 0, 0, false
	}
	mx, my := mean(xs), mean(ys)
	var sxx, sxy, syy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	if sxx <= 0 || n < 2 {
		return 0, 0, 0, false
	}
	slope = sxy / sxx
	intercept = my - slope*mx
	if syy > 0 {
		r2 = sxy * sxy / (sxx * syy)
	} else {
		r2 = 1 // y is flat and the line through it is exact
	}
	if !isFinite(slope) || !isFinite(intercept) || !isFinite(r2) {
		return 0, 0, 0, false
	}
	return slope, intercept, r2, true
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// p95 is the nearest-rank 95th percentile (0 for none).
func p95(v []float64) float64 { return percentile(v, 0.95) }

// percentile is the nearest-rank q-th percentile, q in (0, 1] (0 for none).
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return finite(s[min(max(i, 0), len(s)-1)])
}

func pct(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return finite(part / whole * 100)
}

// cmp prefers the hour's own value and falls back to the facts.
func cmp(hour, facts uint64) uint64 {
	if hour > 0 {
		return hour
	}
	return facts
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func finite(v float64) float64 {
	if !isFinite(v) {
		return 0
	}
	return v
}

func ptr[T any](v T) *T { return &v }

func itoa(v float64) string { return strconv.Itoa(int(math.Round(finite(v)))) }
