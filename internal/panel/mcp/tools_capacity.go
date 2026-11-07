//go:build !js

package mcp

import (
	"errors"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
)

// Load and the panel's own cost (OpsService GetFleetLoad and GetPanelResources): numbers, node names and recommendation codes.
// No address, path or person is in either answer; names still pass nm() and params insMap (no IP literal) like everywhere else. The VPS
// edition only, like the service.

func capacityTools() []toolDef {
	return []toolDef{
		readTool("capacity_report", ProfileReadonly, procs(adminv1connect.NodeServiceListNodesProcedure, opsv1connect.OpsServiceGetFleetLoadProcedure),
			"How loaded each node was over the last hours (default 168, 1 to 720) and how many more people it can take: per node a status (ok, busy, overloaded, unknown = no data), "+
				"the bottleneck that runs out first (cpu, steal, bandwidth, ram, conntrack, disk), the busy hour (highest hourly CPU mean) with its CPU and people, people at peak, p95 of the hourly CPU peaks, "+
				"steal p95, conntrack and RAM peaks, swap, disk, traffic peaks against the owner's bandwidth or the NIC's link speed, the agent's own CPU and memory with its daily growth, "+
				"cpu_per_100_people_pct and mbps_per_person (a fit over the hours; fit says few_hours, flat or no_trend when the data cannot carry one: then say there is not enough data), "+
				"headroom_people (people it can add before its busy hour reaches 80% CPU), and recommendations as codes with params: update_agent, steal_high (the provider oversells the host: move the node), "+
				"conntrack_near_limit, cpu_busy (target = the node with the most headroom to move people to), cpu_busy_add_node, bandwidth_busy, ram_high, swap_in_use, disk_filling, agent_memory_growing (a likely leak). "+
				"summary: nodes by status, total headroom, the most loaded and the most free nodes. With node: that node only, and the fleet summary. "+
				"A value that is absent is not known (an agent too old to report it), not zero. Use it for load questions and for where to put new people.",
			func(c *call, in capacityArg) (any, error) {
				if in.Hours < 0 || in.Hours > 720 {
					return nil, errors.New("hours: 1 to 720")
				}
				id := ""
				if in.Node != "" {
					nid, _, err := c.nodeRef(in.Node)
					if err != nil {
						return nil, err
					}
					id = nid
				}
				r, err := c.cl.Ops.GetFleetLoad(c.ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: uint32(in.Hours)}))
				if err != nil {
					return nil, apiError(err)
				}
				return capacityView(r.Msg, id), nil
			}),
		readTool("panel_resources", ProfileReadonly, procs(opsv1connect.OpsServiceGetPanelResourcesProcedure),
			"What the panel itself costs now: its process (CPU in percent of the machine, resident memory, Go heap and total, goroutines, threads, open files against the limit, uptime), "+
				"the machine it runs on (CPU busy, steal and I/O wait, load, RAM, swap, the data directory's disk), its database (file and WAL size, pages and free pages; measured at most once a minute), "+
				"agents connected out of the nodes, and the admin API's calls over the last 5 minutes (requests, server and client errors, p95 time in ms).",
			func(c *call, _ noArgs) (any, error) {
				r, err := c.cl.Ops.GetPanelResources(c.ctx, connect.NewRequest(&opsv1.GetPanelResourcesRequest{}))
				if err != nil {
					return nil, apiError(err)
				}
				return panelResourcesView(r.Msg), nil
			}),
	}
}

type capacityArg struct {
	Node  string `json:"node,omitempty" jsonschema:"node id or its exact name; empty = every node that is not retired"`
	Hours int    `json:"hours,omitempty" jsonschema:"the window in hours, 1 to 720; empty = 168 (a week)"`
}

type loadRecV struct {
	Code     string            `json:"code"`
	Severity string            `json:"severity"`
	Params   map[string]string `json:"params,omitempty"`
}

type nodeLoadV struct {
	NodeID          string     `json:"node_id"`
	NodeName        string     `json:"node_name"`
	Connected       bool       `json:"connected"`
	CPUs            uint32     `json:"cpus,omitempty"`
	Status          string     `json:"status"`
	Bottleneck      string     `json:"bottleneck,omitempty"`
	HoursWithData   uint32     `json:"hours_with_data"`
	ExtHours        uint32     `json:"ext_hours"`
	BusyHourUnix    int64      `json:"busy_hour_unix,omitempty"`
	BusyHourCPU     float32    `json:"busy_hour_cpu_pct"`
	PeopleBusyHour  float32    `json:"people_busy_hour"`
	PeoplePeak      uint32     `json:"people_peak"`
	CPUPeakP95      float32    `json:"cpu_peak_p95_pct"`
	StealP95        *float32   `json:"steal_p95_pct,omitempty"`
	ConntrackPeak   *float32   `json:"conntrack_peak_pct,omitempty"`
	RAMPeak         *float32   `json:"ram_peak_pct,omitempty"`
	SwapUsedMax     *uint64    `json:"swap_used_max_bytes,omitempty"`
	DiskUsed        *float32   `json:"disk_used_pct,omitempty"`
	RxPeakBps       uint64     `json:"rx_peak_bps"`
	TxPeakBps       uint64     `json:"tx_peak_bps"`
	CapacityMbps    uint32     `json:"capacity_mbps,omitempty"`
	CapacitySource  string     `json:"capacity_source,omitempty"`
	BandwidthPeak   *float32   `json:"bandwidth_peak_pct,omitempty"`
	AgentCPUAvg     *float32   `json:"agent_cpu_avg_pct,omitempty"`
	AgentCPUMax     *float32   `json:"agent_cpu_max_pct,omitempty"`
	AgentRSSMax     *uint64    `json:"agent_rss_max_bytes,omitempty"`
	AgentRSSGrowth  *float32   `json:"agent_rss_growth_pct_per_day,omitempty"`
	Fit             string     `json:"fit"`
	CPUPer100People *float32   `json:"cpu_per_100_people_pct,omitempty"`
	MbpsPerPerson   *float32   `json:"mbps_per_person,omitempty"`
	HeadroomPeople  *uint32    `json:"headroom_people,omitempty"`
	Recommendations []loadRecV `json:"recommendations,omitempty"`
}

type loadRefV struct {
	NodeID         string  `json:"node_id"`
	NodeName       string  `json:"node_name"`
	Status         string  `json:"status"`
	BusyHourCPU    float32 `json:"busy_hour_cpu_pct"`
	HeadroomPeople *uint32 `json:"headroom_people,omitempty"`
}

type loadSummaryV struct {
	OK             uint32     `json:"ok"`
	Busy           uint32     `json:"busy"`
	Overloaded     uint32     `json:"overloaded"`
	Unknown        uint32     `json:"unknown"`
	HeadroomPeople uint32     `json:"headroom_people"`
	HeadroomNodes  uint32     `json:"headroom_nodes"`
	MostLoaded     []loadRefV `json:"most_loaded,omitempty"`
	MostFree       []loadRefV `json:"most_free,omitempty"`
}

// CapacityV is the capacity_report result.
type CapacityV struct {
	Hours     uint32       `json:"hours"`
	NowUnix   int64        `json:"now_unix"`
	Summary   loadSummaryV `json:"summary"`
	Nodes     []nodeLoadV  `json:"nodes"`
	Truncated bool         `json:"truncated,omitempty"`
}

func (v *CapacityV) shrink() bool {
	v.Truncated = true
	return cut(&v.Nodes) // most pressing first: the tail is the quiet nodes
}

func capacityView(r *opsv1.GetFleetLoadResponse, nodeID string) *CapacityV {
	v := &CapacityV{Hours: r.GetHours(), NowUnix: r.GetNowUnix(), Nodes: []nodeLoadV{}}
	s := r.GetSummary()
	v.Summary = loadSummaryV{OK: s.GetOk(), Busy: s.GetBusy(), Overloaded: s.GetOverloaded(), Unknown: s.GetUnknown(),
		HeadroomPeople: s.GetHeadroomPeople(), HeadroomNodes: s.GetHeadroomNodes()}
	ref := func(x *opsv1.LoadNodeRef) loadRefV {
		return loadRefV{NodeID: clean(x.GetNodeId(), 64), NodeName: nm(x.GetNodeName()), Status: clean(x.GetStatus(), 16),
			BusyHourCPU: x.GetBusyHourCpuPct(), HeadroomPeople: x.HeadroomPeople}
	}
	for _, x := range s.GetMostLoaded() {
		v.Summary.MostLoaded = append(v.Summary.MostLoaded, ref(x))
	}
	for _, x := range s.GetMostFree() {
		v.Summary.MostFree = append(v.Summary.MostFree, ref(x))
	}
	for _, n := range r.GetNodes() {
		if nodeID != "" && n.GetNodeId() != nodeID {
			continue
		}
		x := nodeLoadV{
			NodeID: clean(n.GetNodeId(), 64), NodeName: nm(n.GetNodeName()), Connected: n.GetConnected(), CPUs: n.GetCpus(),
			Status: clean(n.GetStatus(), 16), Bottleneck: clean(n.GetBottleneck(), 16), HoursWithData: n.GetHoursWithData(), ExtHours: n.GetExtHours(),
			BusyHourUnix: n.GetBusyHourUnix(), BusyHourCPU: n.GetBusyHourCpuPct(), PeopleBusyHour: n.GetPeopleBusyHour(), PeoplePeak: n.GetPeoplePeak(),
			CPUPeakP95: n.GetCpuPeakP95Pct(), StealP95: n.StealP95Pct, ConntrackPeak: n.ConntrackPeakPct, RAMPeak: n.RamPeakPct,
			SwapUsedMax: n.SwapUsedMaxBytes, DiskUsed: n.DiskUsedPct, RxPeakBps: n.GetRxPeakBps(), TxPeakBps: n.GetTxPeakBps(),
			CapacityMbps: n.GetCapacityMbps(), CapacitySource: clean(n.GetCapacitySource(), 16), BandwidthPeak: n.BandwidthPeakPct,
			AgentCPUAvg: n.AgentCpuAvgPct, AgentCPUMax: n.AgentCpuMaxPct, AgentRSSMax: n.AgentRssMaxBytes, AgentRSSGrowth: n.AgentRssGrowthPctPerDay,
			Fit: clean(n.GetFit(), 16), CPUPer100People: n.CpuPctPerHundredPeople, MbpsPerPerson: n.MbpsPerPerson, HeadroomPeople: n.HeadroomPeople,
		}
		for _, rec := range n.GetRecommendations() {
			x.Recommendations = append(x.Recommendations, loadRecV{Code: clean(rec.GetCode(), 40), Severity: clean(rec.GetSeverity(), 8),
				Params: insMap(rec.GetParams(), maxParamValue)})
		}
		v.Nodes = append(v.Nodes, x)
	}
	return v
}

// PanelResourcesV is the panel_resources result.
type PanelResourcesV struct {
	NowUnix int64 `json:"now_unix"`
	Process struct {
		CPUPct     float32 `json:"cpu_pct"`
		RSS        uint64  `json:"rss_bytes"`
		GoTotal    uint64  `json:"go_total_bytes"`
		GoHeap     uint64  `json:"go_heap_bytes"`
		Goroutines uint32  `json:"goroutines"`
		Threads    uint32  `json:"threads"`
		OpenFDs    uint32  `json:"open_fds"`
		FDLimit    uint32  `json:"fd_limit"`
		UptimeS    uint64  `json:"uptime_s"`
	} `json:"process"`
	Host struct {
		CPUs      uint32  `json:"cpus"`
		CPUBusy   float32 `json:"cpu_busy_pct"`
		Steal     float32 `json:"steal_pct"`
		IOWait    float32 `json:"iowait_pct"`
		Load1     float32 `json:"load1"`
		RAMUsed   uint64  `json:"ram_used_bytes"`
		RAMTotal  uint64  `json:"ram_total_bytes"`
		SwapUsed  uint64  `json:"swap_used_bytes"`
		SwapTotal uint64  `json:"swap_total_bytes"`
		DiskUsed  uint64  `json:"disk_used_bytes"`
		DiskTotal uint64  `json:"disk_total_bytes"`
	} `json:"host"`
	Database struct {
		FileBytes    uint64 `json:"file_bytes"`
		WALBytes     uint64 `json:"wal_bytes"`
		Pages        uint64 `json:"page_count"`
		FreePages    uint64 `json:"freelist_count"`
		PageSize     uint32 `json:"page_size"`
		MeasuredUnix int64  `json:"measured_unix,omitempty"`
	} `json:"database"`
	AgentsConnected uint32 `json:"agents_connected"`
	Nodes           uint32 `json:"nodes"`
	RPC             struct {
		WindowS      uint32  `json:"window_s"`
		Requests     uint64  `json:"requests"`
		ServerErrors uint64  `json:"server_errors"`
		ClientErrors uint64  `json:"client_errors"`
		P95Ms        float32 `json:"p95_ms"`
	} `json:"admin_api"`
}

func panelResourcesView(r *opsv1.GetPanelResourcesResponse) *PanelResourcesV {
	v := &PanelResourcesV{NowUnix: r.GetNowUnix(), AgentsConnected: r.GetAgentsConnected(), Nodes: r.GetNodes()}
	p, h, d, c := r.GetProcess(), r.GetHost(), r.GetDatabase(), r.GetRpc()
	v.Process.CPUPct, v.Process.RSS, v.Process.GoTotal, v.Process.GoHeap = p.GetCpuPct(), p.GetRssBytes(), p.GetGoTotalBytes(), p.GetGoHeapBytes()
	v.Process.Goroutines, v.Process.Threads, v.Process.OpenFDs, v.Process.FDLimit, v.Process.UptimeS = p.GetGoroutines(), p.GetThreads(), p.GetOpenFds(), p.GetFdLimit(), p.GetUptimeS()
	v.Host.CPUs, v.Host.CPUBusy, v.Host.Steal, v.Host.IOWait, v.Host.Load1 = h.GetCpus(), h.GetCpuBusyPct(), h.GetStealPct(), h.GetIowaitPct(), h.GetLoad1()
	v.Host.RAMUsed, v.Host.RAMTotal, v.Host.SwapUsed, v.Host.SwapTotal = h.GetRamUsedBytes(), h.GetRamTotalBytes(), h.GetSwapUsedBytes(), h.GetSwapTotalBytes()
	v.Host.DiskUsed, v.Host.DiskTotal = h.GetDiskUsedBytes(), h.GetDiskTotalBytes()
	v.Database.FileBytes, v.Database.WALBytes, v.Database.Pages, v.Database.FreePages = d.GetFileBytes(), d.GetWalBytes(), d.GetPageCount(), d.GetFreelistCount()
	v.Database.PageSize, v.Database.MeasuredUnix = d.GetPageSize(), d.GetMeasuredUnix()
	v.RPC.WindowS, v.RPC.Requests, v.RPC.ServerErrors, v.RPC.ClientErrors, v.RPC.P95Ms = c.GetWindowS(), c.GetRequests(), c.GetServerErrors(), c.GetClientErrors(), c.GetP95Ms()
	return v
}
