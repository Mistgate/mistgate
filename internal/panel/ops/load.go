//go:build !js

package ops

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/capacity"
)

// GetFleetLoad: the capacity read of every node (internal/panel/capacity) over a window of its hourly host load. The hours
// change once an hour, so an answer is kept for a minute per window; at most loadCacheMax windows are kept.

const (
	loadDefaultHours = 168
	loadMaxHours     = 720
	loadCacheFor     = time.Minute
	loadCacheMax     = 4
)

type loadCache struct {
	mu      sync.Mutex
	entries map[uint32]loadEntry
}

type loadEntry struct {
	at   time.Time
	resp *opsv1.GetFleetLoadResponse
}

func (r rpc) GetFleetLoad(ctx context.Context, req *connect.Request[opsv1.GetFleetLoadRequest]) (*connect.Response[opsv1.GetFleetLoadResponse], error) {
	s := r.s
	hours := req.Msg.Hours
	if hours == 0 {
		hours = loadDefaultHours
	}
	if hours > loadMaxHours {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hours: 1 to 720"))
	}
	now := s.cfg.Now()
	s.load.mu.Lock()
	if e, ok := s.load.entries[hours]; ok && now.Sub(e.at) < loadCacheFor && !now.Before(e.at) {
		s.load.mu.Unlock()
		return connect.NewResponse(e.resp), nil // never changed after it was built: safe to hand out again
	}
	s.load.mu.Unlock()

	resp := &opsv1.GetFleetLoadResponse{NowUnix: now.Unix(), Hours: hours, Nodes: []*opsv1.NodeLoad{}, Summary: &opsv1.FleetLoadSummary{}}
	if s.cfg.Capacity != nil {
		nodes, err := s.cfg.Capacity(ctx, int(hours))
		if err != nil {
			return nil, internalErr(s.cfg.Log, "read the fleet's load", err)
		}
		reports, sum := capacity.Fleet(nodes)
		for _, rep := range reports {
			resp.Nodes = append(resp.Nodes, nodeLoad(rep))
		}
		resp.Summary = loadSummary(sum)
	}

	s.load.mu.Lock()
	if s.load.entries == nil {
		s.load.entries = map[uint32]loadEntry{}
	}
	if len(s.load.entries) >= loadCacheMax { // drop the oldest window
		var oldest uint32
		var at time.Time
		for k, e := range s.load.entries {
			if at.IsZero() || e.at.Before(at) {
				oldest, at = k, e.at
			}
		}
		delete(s.load.entries, oldest)
	}
	s.load.entries[hours] = loadEntry{at: now, resp: resp}
	s.load.mu.Unlock()
	return connect.NewResponse(resp), nil
}

func nodeLoad(r capacity.Report) *opsv1.NodeLoad {
	n := &opsv1.NodeLoad{
		NodeId: r.ID, NodeName: r.Name, Connected: r.Connected, Cpus: r.CPUs, Status: r.Status, Bottleneck: r.Bottleneck,
		HoursWithData: u32(r.HoursWithData), ExtHours: u32(r.ExtHours), BusyHourUnix: r.BusyHourUnix, BusyHourCpuPct: f32(r.BusyHourCPU),
		PeopleBusyHour: f32(r.PeopleBusyHour), PeoplePeak: r.PeoplePeak, CpuPeakP95Pct: f32(r.CPUPeakP95),
		StealP95Pct: f32p(r.StealP95), ConntrackPeakPct: f32p(r.ConntrackPeakPct), RamPeakPct: f32p(r.RAMPeakPct), SwapUsedMaxBytes: r.SwapUsedMax,
		DiskUsedPct: f32p(r.DiskUsedPct), RxPeakBps: r.RxPeakBps, TxPeakBps: r.TxPeakBps, CapacityMbps: r.LinkMbps, CapacitySource: r.LinkSource,
		BandwidthPeakPct: f32p(r.BandwidthPeakPct), AgentCpuAvgPct: f32p(r.AgentCPUAvg), AgentCpuMaxPct: f32p(r.AgentCPUMax),
		AgentRssMaxBytes: r.AgentRSSMax, AgentRssGrowthPctPerDay: f32p(r.AgentRSSGrowthPctPerDay), Fit: r.Fit,
		CpuPctPerHundredPeople: f32p(r.CPUPer100), MbpsPerPerson: f32p(r.MbpsPerPerson), HeadroomPeople: u32p(r.Headroom),
		Recommendations: []*opsv1.LoadRecommendation{},
	}
	for _, x := range r.Recs {
		n.Recommendations = append(n.Recommendations, &opsv1.LoadRecommendation{Code: x.Code, Severity: x.Severity, Params: x.Params})
	}
	return n
}

func loadSummary(s capacity.Summary) *opsv1.FleetLoadSummary {
	out := &opsv1.FleetLoadSummary{Ok: u32(s.OK), Busy: u32(s.Busy), Overloaded: u32(s.Overloaded), Unknown: u32(s.Unknown),
		HeadroomPeople: u32(s.HeadroomPeople), HeadroomNodes: u32(s.HeadroomNodes)}
	ref := func(r capacity.Ref) *opsv1.LoadNodeRef {
		return &opsv1.LoadNodeRef{NodeId: r.ID, NodeName: r.Name, Status: r.Status, BusyHourCpuPct: f32(r.BusyHourCPU), HeadroomPeople: u32p(r.Headroom)}
	}
	for _, r := range s.MostLoaded {
		out.MostLoaded = append(out.MostLoaded, ref(r))
	}
	for _, r := range s.MostFree {
		out.MostFree = append(out.MostFree, ref(r))
	}
	return out
}

func u32(v int) uint32 { return uint32(min(max(v, 0), math.MaxUint32)) }

func u32p(v *int) *uint32 {
	if v == nil {
		return nil
	}
	x := u32(*v)
	return &x
}

// f32 rounds to two decimals: the report is read by people and assistants, not by another computation.
func f32(v float64) float32 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return float32(math.Round(v*100) / 100)
}

func f32p(v *float64) *float32 {
	if v == nil {
		return nil
	}
	x := f32(*v)
	return &x
}
