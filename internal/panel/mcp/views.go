package mcp

import (
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The projections: every tool has its own output struct and builds it from the fields it chose.
// A proto message is never marshalled to an agent, so a field added to the API does not reach an agent until someone adds
// it here. Strings that came from data (names, notes, event and alert parameters, log lines) go through clean.
//
// Never projected, on purpose: node addresses and certificate pins, subscription links, device keys and configs,
// the exit IP of a check, anything of the token store.

func nm(s string) string { return clean(s, maxName) }

func sumUsers(pc []*adminv1.ProtocolCount) uint32 {
	var n uint32
	for _, p := range pc {
		n += p.GetUsers()
	}
	return n
}

func onlineByProtocol(pc []*adminv1.ProtocolCount) map[string]uint32 {
	if len(pc) == 0 {
		return nil
	}
	m := make(map[string]uint32, len(pc))
	for _, p := range pc {
		m[clean(p.GetProtocol(), 40)] += p.GetUsers()
	}
	return m
}

func nodeStatus(s adminv1.NodeStatus) string { return enumName("NODE_STATUS_", s.String()) }

// ---------------------------------------------------------------------------------------------------------------------
// fleet_status

type nodeBriefV struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Country string   `json:"country,omitempty"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	Online  uint32   `json:"online_users"`
	DownBps uint64   `json:"down_bps"`
	UpBps   uint64   `json:"up_bps"`
	CPUPct  *float32 `json:"cpu_pct,omitempty"`
}

type topConsumerV struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	DownBps  uint64 `json:"down_bps"`
}

// FleetStatusV is the fleet_status result.
type FleetStatusV struct {
	NodesTotal     uint32         `json:"nodes_total"`
	NodesProblem   uint32         `json:"nodes_problem"`
	UsersOnline    uint32         `json:"users_online"`
	AlertsActive   uint32         `json:"alerts_active"`
	AlertsCritical uint32         `json:"alerts_critical"`
	Nodes          []nodeBriefV   `json:"nodes"`
	TopConsumers   []topConsumerV `json:"top_consumers,omitempty"`
	Truncated      bool           `json:"truncated,omitempty"`
}

func (v *FleetStatusV) shrink() bool {
	v.Truncated = true
	return cut(&v.TopConsumers) || cut(&v.Nodes)
}

func fleetStatusView(r *adminv1.OverviewResponse) *FleetStatusV {
	v := &FleetStatusV{
		NodesTotal: r.GetNodesTotal(), NodesProblem: r.GetNodesProblem(), UsersOnline: r.GetUsersOnline(),
		AlertsActive: r.GetAlertsActive(), AlertsCritical: r.GetAlertsCritical(),
		Nodes: []nodeBriefV{},
	}
	for _, n := range r.GetNodes() {
		b := nodeBriefV{
			ID: n.GetId(), Name: nm(n.GetName()), Country: clean(n.GetCountryCode(), 8),
			Status: nodeStatus(n.GetStatus()), Reason: clean(n.GetReason().GetCode(), 60),
			Online: sumUsers(n.GetOnline()), DownBps: n.GetDownBps(), UpBps: n.GetUpBps(),
		}
		if n.GetHasMetrics() {
			c := n.GetCpuPct()
			b.CPUPct = &c
		}
		v.Nodes = append(v.Nodes, b)
	}
	for i, t := range r.GetTopConsumers() {
		if i == 10 {
			break
		}
		v.TopConsumers = append(v.TopConsumers, topConsumerV{
			UserID: t.GetUserId(), UserName: nm(t.GetUserName()), NodeID: t.GetNodeId(), NodeName: nm(t.GetNodeName()),
			DownBps: t.GetDownBps(),
		})
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// node_get, node_metrics

type inboundV struct {
	ID        string `json:"id"`
	Profile   string `json:"profile"`
	Protocol  string `json:"protocol"`
	Port      uint32 `json:"port"`
	State     string `json:"state"`
	LastError string `json:"last_error,omitempty"`
}

type onlineUserV struct {
	UserID      string `json:"user_id"`
	UserName    string `json:"user_name"`
	Protocol    string `json:"protocol"`
	Device      string `json:"device,omitempty"`
	ConnectedAt int64  `json:"connected_unix"`
	DownBps     uint64 `json:"down_bps"`
}

type topUserV struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Bytes    uint64 `json:"bytes"`
}

type factsV struct {
	Hostname  string   `json:"hostname,omitempty"`
	OS        string   `json:"os,omitempty"`
	Kernel    string   `json:"kernel,omitempty"`
	Arch      string   `json:"arch,omitempty"`
	CPUCount  uint32   `json:"cpu_count"`
	RAMTotal  uint64   `json:"ram_total_bytes"`
	DiskTotal uint64   `json:"disk_total_bytes"`
	Virt      string   `json:"virt,omitempty"`
	HasIPv6   bool     `json:"has_ipv6"`
	Engines   []string `json:"engines,omitempty"`
}

// NodeGetV is the node_get result.
type NodeGetV struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Country       string            `json:"country,omitempty"`
	Location      string            `json:"location,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Status        string            `json:"status"`
	Reason        string            `json:"reason,omitempty"`
	Protocols     []string          `json:"protocols,omitempty"`
	AgentVersion  string            `json:"agent_version,omitempty"`
	UptimeS       uint64            `json:"uptime_s"`
	LastSeenUnix  int64             `json:"last_seen_unix"`
	CreatedUnix   int64             `json:"created_unix"`
	Warp          string            `json:"warp,omitempty"`
	Facts         *factsV           `json:"facts,omitempty"`
	Inbounds      []inboundV        `json:"inbounds"`
	OnlineByProto map[string]uint32 `json:"online_by_protocol,omitempty"`
	OnlineUsers   []onlineUserV     `json:"online_users,omitempty"`
	OnlineTotal   uint32            `json:"online_total"`
	Notes         string            `json:"notes,omitempty"`
	Truncated     bool              `json:"truncated,omitempty"`
}

func (v *NodeGetV) shrink() bool {
	v.Truncated = true
	return cut(&v.OnlineUsers) || cut(&v.Inbounds)
}

func nodeGetView(r *adminv1.GetNodeResponse) *NodeGetV {
	n := r.GetNode()
	v := &NodeGetV{
		ID: n.GetId(), Name: nm(n.GetName()), Country: clean(n.GetCountryCode(), 8), Location: nm(n.GetLocation()),
		Provider: nm(n.GetProvider()), Status: nodeStatus(n.GetStatus()), Reason: clean(n.GetReason().GetCode(), 60),
		AgentVersion: clean(n.GetAgentVersion(), 60), UptimeS: n.GetUptimeS(), LastSeenUnix: n.GetLastSeenUnix(),
		CreatedUnix: r.GetCreatedUnix(), Inbounds: []inboundV{},
		OnlineByProto: onlineByProtocol(n.GetOnline()), OnlineTotal: sumUsers(n.GetOnline()),
		Notes: clean(r.GetNotes(), maxNote),
	}
	for _, p := range n.GetProtocols() {
		v.Protocols = append(v.Protocols, clean(p, 40))
	}
	if w := n.GetWarp(); w != nil && w.GetState() != adminv1.WarpState_WARP_STATE_UNSPECIFIED {
		v.Warp = enumName("WARP_STATE_", w.GetState().String())
	}
	if f := r.GetFacts(); f != nil {
		fv := &factsV{
			Hostname: nm(f.GetHostname()), OS: nm(f.GetOs()), Kernel: nm(f.GetKernel()), Arch: clean(f.GetArch(), 20),
			CPUCount: f.GetCpuCount(), RAMTotal: f.GetRamTotalBytes(), DiskTotal: f.GetDiskTotalBytes(),
			Virt: clean(f.GetVirt(), 40), HasIPv6: f.GetHasIpv6(),
		}
		for _, e := range f.GetEngines() {
			fv.Engines = append(fv.Engines, clean(e, 60))
		}
		v.Facts = fv
	}
	for _, in := range r.GetInbounds() {
		v.Inbounds = append(v.Inbounds, inboundV{
			ID: in.GetId(), Profile: nm(in.GetProfileName()), Protocol: clean(in.GetProtocol(), 40), Port: in.GetPort(),
			State: enumName("INBOUND_STATE_", in.GetState().String()), LastError: clean(in.GetLastError(), 200),
		})
	}
	for i, u := range r.GetOnlineUsers() {
		if i == maxOnlineUsers {
			break
		}
		v.OnlineUsers = append(v.OnlineUsers, onlineUserV{
			UserID: u.GetUserId(), UserName: nm(u.GetUserName()), Protocol: clean(u.GetProtocol(), 40),
			Device: nm(u.GetDeviceModel()), ConnectedAt: u.GetConnectedAtUnix(), DownBps: u.GetDownBps(),
		})
	}
	return v
}

// WarpStatusV is the warp_status result. It intentionally projects only status and probe fields from GetWarp: account
// addresses, endpoints, keys and registration metadata stay inside the panel.
type WarpStatusV struct {
	NodeID            string      `json:"node_id"`
	NodeName          string      `json:"node_name"`
	HasAccount        bool        `json:"has_account"`
	Enabled           bool        `json:"enabled"`
	Paused            bool        `json:"paused"`
	AgentState        string      `json:"agent_state"`
	Colo              string      `json:"colo,omitempty"`
	LastHandshakeAgeS *uint64     `json:"last_handshake_age_s,omitempty"`
	ProbeCloudflare   *warpProbeV `json:"probe_cloudflare,omitempty"`
	ProbeOther        *warpProbeV `json:"probe_other,omitempty"`
	RegisteredUnix    int64       `json:"registered_unix,omitempty"`
	HasToken          bool        `json:"has_token"`
	Profiles          []string    `json:"profiles"`
}

type warpProbeV struct {
	OK          bool   `json:"ok"`
	LatencyMS   uint32 `json:"latency_ms,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
}

func warpAgentState(s adminv1.WarpState) string {
	switch s {
	case adminv1.WarpState_WARP_STATE_UP:
		return "up"
	case adminv1.WarpState_WARP_STATE_STARTING:
		return "starting"
	case adminv1.WarpState_WARP_STATE_DOWN:
		return "down"
	default:
		return "none"
	}
}

func warpProbeView(p *adminv1.WarpProbeResult, fallback bool) *warpProbeV {
	if p == nil {
		return &warpProbeV{OK: fallback}
	}
	return &warpProbeV{OK: p.GetOk(), LatencyMS: p.GetLatencyMs(), FailureCode: clean(p.GetFailureCode(), 60)}
}

func warpStatusView(nodeID, nodeName string, r *adminv1.GetWarpResponse, now time.Time) *WarpStatusV {
	v := &WarpStatusV{NodeID: nodeID, NodeName: nm(nodeName), AgentState: "none", Profiles: []string{}}
	if a := r.GetAccount(); a != nil {
		v.HasAccount, v.Enabled, v.Paused = true, a.GetEnabled(), !a.GetEnabled()
		v.RegisteredUnix, v.HasToken = a.GetCreatedUnix(), a.GetHasToken()
	}
	if h := r.GetHealth(); h != nil {
		v.AgentState, v.Colo = warpAgentState(h.GetState()), clean(h.GetColo(), 8)
		if at := h.GetLastHandshakeUnix(); at > 0 {
			age := uint64(max(int64(0), now.Unix()-at))
			v.LastHandshakeAgeS = &age
		}
		v.ProbeCloudflare = warpProbeView(h.GetProbeCloudflare(), h.GetProbeCloudflareOk())
		v.ProbeOther = warpProbeView(h.GetProbeOther(), h.GetProbeOtherOk())
	}
	for _, in := range r.GetInbounds() {
		v.Profiles = append(v.Profiles, nm(in.GetProfileName()))
	}
	return v
}

// NodeMetricsV is the node_metrics result. The API has no history per node: these are the current gauges and today's totals.
type NodeMetricsV struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	HasMetrics   bool              `json:"has_metrics"`
	AtUnix       int64             `json:"at_unix,omitempty"`
	CPUPct       float32           `json:"cpu_pct"`
	SoftIRQPct   float32           `json:"softirq_pct"`
	Load1        float32           `json:"load1"`
	RAMUsed      uint64            `json:"ram_used_bytes"`
	RAMTotal     uint64            `json:"ram_total_bytes"`
	DiskUsed     uint64            `json:"disk_used_bytes"`
	DiskTotal    uint64            `json:"disk_total_bytes"`
	NetRxBps     uint64            `json:"net_rx_bps"`
	NetTxBps     uint64            `json:"net_tx_bps"`
	TrafficToday uint64            `json:"traffic_today_bytes"`
	OnlineByProt map[string]uint32 `json:"online_by_protocol,omitempty"`
	TopToday     []topUserV        `json:"top_today,omitempty"`
}

func nodeMetricsView(r *adminv1.GetNodeResponse) *NodeMetricsV {
	n, m := r.GetNode(), r.GetMetrics()
	v := &NodeMetricsV{
		ID: n.GetId(), Name: nm(n.GetName()), Status: nodeStatus(n.GetStatus()), HasMetrics: n.GetHasMetrics() && m != nil,
		TrafficToday: n.GetTrafficTodayBytes(), OnlineByProt: onlineByProtocol(n.GetOnline()),
	}
	if m != nil {
		v.AtUnix, v.CPUPct, v.SoftIRQPct, v.Load1 = m.GetAtUnix(), m.GetCpuPct(), m.GetSoftirqPct(), m.GetLoad1()
		v.RAMUsed, v.RAMTotal, v.DiskUsed, v.DiskTotal = m.GetRamUsedBytes(), m.GetRamTotalBytes(), m.GetDiskUsedBytes(), m.GetDiskTotalBytes()
		v.NetRxBps, v.NetTxBps = m.GetNetRxBps(), m.GetNetTxBps()
	}
	for i, t := range r.GetTopToday() {
		if i == 10 {
			break
		}
		v.TopToday = append(v.TopToday, topUserV{UserID: t.GetUserId(), UserName: nm(t.GetUserName()), Bytes: t.GetBytes()})
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// node_doctor

type doctorItemV struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"`
	Title    string            `json:"title_key,omitempty"`
	Detail   string            `json:"detail,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	FixID    string            `json:"fix_id,omitempty"`
	Measured int64             `json:"measured_unix,omitempty"`
}

type nodeDoctorV struct {
	NodeID       string        `json:"node_id"`
	NodeName     string        `json:"node_name"`
	NodeStatus   string        `json:"node_status"`
	Supported    bool          `json:"agent_supported"`
	HasReport    bool          `json:"has_report"`
	ReceivedUnix int64         `json:"received_unix,omitempty"`
	AgeS         uint32        `json:"age_s,omitempty"`
	Stale        bool          `json:"stale,omitempty"`
	Items        []doctorItemV `json:"items,omitempty"`
}

// DoctorV is the node_doctor result.
type DoctorV struct {
	Fresh     bool          `json:"fresh"`
	Nodes     []nodeDoctorV `json:"nodes"`
	Truncated bool          `json:"truncated,omitempty"`
}

func (v *DoctorV) shrink() bool {
	v.Truncated = true
	for i := range v.Nodes {
		if cut(&v.Nodes[i].Items) {
			return true
		}
	}
	return cut(&v.Nodes)
}

func doctorView(nodes []*adminv1.NodeDoctor, fresh bool) *DoctorV {
	v := &DoctorV{Fresh: fresh, Nodes: []nodeDoctorV{}}
	for _, d := range nodes {
		nd := nodeDoctorV{
			NodeID: d.GetNodeId(), NodeName: nm(d.GetNodeName()), NodeStatus: nodeStatus(d.GetNodeStatus()),
			Supported: d.GetAgentSupported(), HasReport: d.GetHasReport(), ReceivedUnix: d.GetReceivedUnix(),
			AgeS: d.GetAgeS(), Stale: d.GetStale(),
		}
		for _, it := range d.GetItems() {
			nd.Items = append(nd.Items, doctorItemV{
				ID: clean(it.GetId(), 60), Status: enumName("DOCTOR_STATUS_", it.GetStatus().String()),
				Title: clean(it.GetTitleKey(), 80), Detail: clean(it.GetDetail(), 200),
				Params: cleanMap(it.GetParams(), maxParamValue), FixID: clean(it.GetFixId(), 60), Measured: it.GetMeasuredUnix(),
			})
		}
		v.Nodes = append(v.Nodes, nd)
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// users

type userV struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	GroupID     string   `json:"group_id,omitempty"`
	Group       string   `json:"group,omitempty"`
	Status      string   `json:"status"`
	Online      bool     `json:"online"`
	DevicesUsed uint32   `json:"devices_used"`
	DeviceLimit uint32   `json:"device_limit"`
	UsedBytes   uint64   `json:"used_bytes"`
	QuotaBytes  uint64   `json:"quota_bytes"`
	QuotaReset  string   `json:"quota_reset,omitempty"`
	ExpiresUnix int64    `json:"expires_unix"`
	LastSeen    int64    `json:"last_seen_unix"`
	Node        string   `json:"current_node,omitempty"`
	SpeedLimit  uint64   `json:"speed_limit_bps,omitempty"`
	Happ        bool     `json:"app_happ"`
	Amnezia     bool     `json:"app_amnezia"`
	AllNodes    bool     `json:"all_nodes"`
	NodeIDs     []string `json:"node_ids,omitempty"`
	CreatedUnix int64    `json:"created_unix"`
}

func userView(u *adminv1.User) userV {
	v := userV{
		ID: u.GetId(), Name: nm(u.GetName()), GroupID: u.GetGroupId(), Group: nm(u.GetGroupName()),
		Status: enumName("USER_STATUS_", u.GetStatus().String()), Online: u.GetOnline(),
		DevicesUsed: u.GetDevicesUsed(), DeviceLimit: u.GetDeviceLimit(), UsedBytes: u.GetUsedBytes(), QuotaBytes: u.GetQuotaBytes(),
		QuotaReset: enumName("QUOTA_RESET_", u.GetQuotaReset().String()), ExpiresUnix: u.GetExpiresUnix(), LastSeen: u.GetLastSeenUnix(),
		Node: nm(u.GetCurrentNodeName()), SpeedLimit: u.GetSpeedLimitBps(), Happ: u.GetApps().GetHapp(), Amnezia: u.GetApps().GetAmnezia(),
		AllNodes: u.GetNodes().GetAll(), CreatedUnix: u.GetCreatedUnix(),
	}
	if !v.AllNodes {
		for i, id := range u.GetNodes().GetNodeIds() {
			if i == maxIDs {
				break
			}
			v.NodeIDs = append(v.NodeIDs, clean(id, 60))
		}
	}
	return v
}

// UsersV is the users_search result.
type UsersV struct {
	Users         []userV `json:"users"`
	NextPageToken string  `json:"next_page_token,omitempty"`
	Total         uint32  `json:"total"`
	Online        uint32  `json:"online"`
	Expiring      uint32  `json:"expiring"`
	OverQuota     uint32  `json:"over_quota"`
	Truncated     bool    `json:"truncated,omitempty"`
}

// shrink also drops the page token: it would skip the cut users.
func (v *UsersV) shrink() bool {
	v.Truncated, v.NextPageToken = true, ""
	return cut(&v.Users)
}

func usersView(r *adminv1.ListUsersResponse) *UsersV {
	v := &UsersV{
		Users: []userV{}, NextPageToken: clean(r.GetNextPageToken(), 200),
		Total: r.GetCounts().GetAll(), Online: r.GetCounts().GetOnline(), Expiring: r.GetCounts().GetExpiring(),
		OverQuota: r.GetCounts().GetOverQuota(),
	}
	for _, u := range r.GetUsers() {
		v.Users = append(v.Users, userView(u))
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// groups_list

type groupV struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ProfileIDs []string `json:"profile_ids,omitempty"`
	UserCount  uint32   `json:"user_count"`
	DNSPreset  string   `json:"dns_preset_id,omitempty"`
}

// GroupsV is the groups_list result.
type GroupsV struct {
	Groups    []groupV `json:"groups"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (v *GroupsV) shrink() bool {
	v.Truncated = true
	return cut(&v.Groups)
}

func groupsView(r *adminv1.ListGroupsResponse) *GroupsV {
	v := &GroupsV{Groups: []groupV{}}
	for _, g := range r.GetGroups() {
		gv := groupV{ID: g.GetId(), Name: nm(g.GetName()), UserCount: g.GetUserCount(), DNSPreset: clean(g.GetDnsPresetId(), 60)}
		for i, id := range g.GetProfileIds() {
			if i == maxIDs {
				break
			}
			gv.ProfileIDs = append(gv.ProfileIDs, clean(id, 60))
		}
		v.Groups = append(v.Groups, gv)
	}
	return v
}

type deviceV struct {
	ID         string   `json:"id"`
	Platform   string   `json:"platform,omitempty"`
	Model      string   `json:"model,omitempty"`
	FirstSeen  int64    `json:"first_seen_unix"`
	LastSeen   int64    `json:"last_seen_unix"`
	Online     bool     `json:"online"`
	Protocols  []string `json:"protocols,omitempty"`
	AwgProfile string   `json:"awg_profile,omitempty"`
	AwgVersion string   `json:"awg_version,omitempty"`
	Stale      bool     `json:"stale,omitempty"`
	Address    string   `json:"address,omitempty"`
	LastHandsh int64    `json:"last_handshake_unix,omitempty"`
}

func deviceViews(ds []*adminv1.Device) []deviceV {
	out := make([]deviceV, 0, len(ds))
	for _, d := range ds {
		dv := deviceV{
			ID: d.GetId(), Platform: clean(d.GetPlatform(), 40), Model: nm(d.GetModel()), FirstSeen: d.GetFirstSeenUnix(),
			LastSeen: d.GetLastSeenUnix(), Online: d.GetOnline(), AwgProfile: nm(d.GetAwgProfileName()),
			AwgVersion: clean(d.GetAwgVersion(), 20), Stale: d.GetStale(), Address: clean(d.GetAddress(), 64),
			LastHandsh: d.GetLastHandshakeUnix(),
		}
		for _, p := range d.GetProtocols() {
			dv.Protocols = append(dv.Protocols, clean(p, 40))
		}
		out = append(out, dv)
	}
	return out
}

type nodeAccessV struct {
	NodeID    string   `json:"node_id"`
	Name      string   `json:"name"`
	Selected  bool     `json:"selected"`
	Protocols []string `json:"protocols,omitempty"`
}

type profileRefV struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// UserGetV is the user_get result: no subscription link, no keys.
type UserGetV struct {
	User       userV         `json:"user"`
	Devices    []deviceV     `json:"devices"`
	Profiles   []profileRefV `json:"profiles,omitempty"`
	NodeAccess []nodeAccessV `json:"node_access,omitempty"`
	Truncated  bool          `json:"truncated,omitempty"`
}

func (v *UserGetV) shrink() bool {
	v.Truncated = true
	return cut(&v.NodeAccess) || cut(&v.Devices)
}

func profileRefs(ps []*adminv1.ProfileRef) []profileRefV {
	var out []profileRefV
	for _, p := range ps {
		out = append(out, profileRefV{ID: p.GetId(), Name: nm(p.GetName()), Protocol: clean(p.GetProtocol(), 40)})
	}
	return out
}

func nodeAccessViews(ns []*adminv1.NodeAccess) []nodeAccessV {
	var out []nodeAccessV
	for _, n := range ns {
		a := nodeAccessV{NodeID: n.GetNodeId(), Name: nm(n.GetNodeName()), Selected: n.GetSelected()}
		for _, p := range n.GetProtocols() {
			a.Protocols = append(a.Protocols, clean(p, 40))
		}
		out = append(out, a)
	}
	return out
}

func userGetView(r *adminv1.GetUserResponse) *UserGetV {
	return &UserGetV{
		User: userView(r.GetUser()), Devices: deviceViews(r.GetDevices()), Profiles: profileRefs(r.GetProfiles()),
		NodeAccess: nodeAccessViews(r.GetNodeAccess()),
	}
}

type dayV struct {
	DayUnix int64  `json:"day_unix"`
	Bytes   uint64 `json:"bytes"`
}

type nodeTrafficV struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Protocol string `json:"protocol"`
	Bytes    uint64 `json:"bytes"`
}

// UserTrafficV is the user_traffic result.
type UserTrafficV struct {
	UserID     string         `json:"user_id"`
	Name       string         `json:"name"`
	UsedBytes  uint64         `json:"used_bytes"`
	QuotaBytes uint64         `json:"quota_bytes"`
	QuotaReset string         `json:"quota_reset,omitempty"`
	NextReset  int64          `json:"next_reset_unix,omitempty"`
	Daily      []dayV         `json:"daily"`
	PerNode    []nodeTrafficV `json:"per_node,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
}

func (v *UserTrafficV) shrink() bool {
	v.Truncated = true
	return cut(&v.PerNode)
}

func userTrafficView(r *adminv1.GetUserResponse) *UserTrafficV {
	u := r.GetUser()
	v := &UserTrafficV{
		UserID: u.GetId(), Name: nm(u.GetName()), UsedBytes: u.GetUsedBytes(), QuotaBytes: u.GetQuotaBytes(),
		QuotaReset: enumName("QUOTA_RESET_", u.GetQuotaReset().String()), NextReset: u.GetNextResetUnix(), Daily: []dayV{},
	}
	days := r.GetDailyTraffic()
	if len(days) > 14 {
		days = days[len(days)-14:]
	}
	for _, d := range days {
		v.Daily = append(v.Daily, dayV{DayUnix: d.GetDayUnix(), Bytes: d.GetBytes()})
	}
	for _, t := range r.GetNodeTraffic() {
		v.PerNode = append(v.PerNode, nodeTrafficV{NodeID: t.GetNodeId(), NodeName: nm(t.GetNodeName()), Protocol: clean(t.GetProtocol(), 40), Bytes: t.GetBytes()})
	}
	return v
}

// UserDevicesV is the user_devices result.
type UserDevicesV struct {
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Limit     uint32    `json:"device_limit"`
	Devices   []deviceV `json:"devices"`
	Truncated bool      `json:"truncated,omitempty"`
}

func (v *UserDevicesV) shrink() bool {
	v.Truncated = true
	return cut(&v.Devices)
}

func userDevicesView(r *adminv1.GetUserResponse) *UserDevicesV {
	u := r.GetUser()
	return &UserDevicesV{UserID: u.GetId(), Name: nm(u.GetName()), Limit: u.GetDeviceLimit(), Devices: deviceViews(r.GetDevices())}
}

// ---------------------------------------------------------------------------------------------------------------------
// subscription_preview

type clientV struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Protocols []string `json:"protocols,omitempty"`
	Formats   []string `json:"formats,omitempty"`
}

type serveV struct {
	RuleIndex int32  `json:"rule_index"`
	Format    string `json:"format"`
	Browser   bool   `json:"browser"`
}

// SubscriptionPreviewV is the subscription_preview result. It says which format a client would get and what the user's
// access gives; it is not the rendered subscription and carries no link (a link is a credential).
type SubscriptionPreviewV struct {
	User      userV         `json:"user"`
	Clients   []clientV     `json:"clients,omitempty"`
	Client    *clientV      `json:"client,omitempty"`
	UserAgent string        `json:"user_agent,omitempty"`
	Serve     *serveV       `json:"serve,omitempty"`
	Profiles  []profileRefV `json:"profiles,omitempty"`
	Nodes     []nodeAccessV `json:"nodes,omitempty"`
	Note      string        `json:"note"`
	Truncated bool          `json:"truncated,omitempty"`
}

func (v *SubscriptionPreviewV) shrink() bool {
	v.Truncated = true
	return cut(&v.Nodes) || cut(&v.Clients)
}

func clientView(c *adminv1.ClientInfo) clientV {
	cv := clientV{ID: clean(c.GetId(), 60), Name: nm(c.GetName())}
	for _, p := range c.GetProtocols() {
		cv.Protocols = append(cv.Protocols, clean(p, 40))
	}
	for _, f := range c.GetFormats() {
		cv.Formats = append(cv.Formats, subFormat(f))
	}
	return cv
}

func subFormat(f adminv1.SubFormat) string { return enumName("SUB_FORMAT_", f.String()) }

// ---------------------------------------------------------------------------------------------------------------------
// alerts, events, checks, audit

type alertV struct {
	ID         string            `json:"id"`
	Severity   string            `json:"severity"`
	Kind       string            `json:"kind"`
	NodeID     string            `json:"node_id,omitempty"`
	NodeName   string            `json:"node_name,omitempty"`
	Subject    string            `json:"subject,omitempty"`
	Title      string            `json:"title_key,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	Why        string            `json:"why_key,omitempty"`
	First      int64             `json:"first_seen_unix"`
	Last       int64             `json:"last_seen_unix"`
	Resolved   int64             `json:"resolved_at_unix,omitempty"`
	Resolution string            `json:"resolution,omitempty"`
	MutedTill  int64             `json:"muted_until_unix,omitempty"`
	Actions    []string          `json:"actions,omitempty"`
}

func alertView(a *adminv1.Alert) alertV {
	v := alertV{
		ID: a.GetId(), Severity: enumName("ALERT_SEVERITY_", a.GetSeverity().String()), Kind: enumName("ALERT_KIND_", a.GetKind().String()),
		NodeID: a.GetNodeId(), NodeName: nm(a.GetNodeName()), Subject: nm(a.GetSubject()), Title: clean(a.GetTitleKey(), 80),
		Params: cleanMap(a.GetParams(), maxParamValue), Why: clean(a.GetWhyKey(), 80), First: a.GetFirstSeenUnix(),
		Last: a.GetLastSeenUnix(), Resolved: a.GetResolvedAtUnix(), Resolution: clean(a.GetResolution(), 60),
		MutedTill: a.GetMutedUntilUnix(),
	}
	for _, x := range a.GetActions() {
		v.Actions = append(v.Actions, clean(x, 60))
	}
	return v
}

// AlertsV is the alerts_list result.
type AlertsV struct {
	NowUnix   int64    `json:"now_unix"`
	Active    []alertV `json:"active"`
	History   []alertV `json:"history,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (v *AlertsV) shrink() bool {
	v.Truncated = true
	return cut(&v.History) || cut(&v.Active)
}

func alertsView(r *adminv1.ListAlertsResponse, history bool) *AlertsV {
	v := &AlertsV{NowUnix: r.GetNowUnix(), Active: []alertV{}}
	for i, a := range r.GetActive() {
		if i == maxListLimit {
			v.Truncated = true
			break
		}
		v.Active = append(v.Active, alertView(a))
	}
	if history {
		for i, a := range r.GetHistory() {
			if i == defaultListLimit {
				v.Truncated = true
				break
			}
			v.History = append(v.History, alertView(a))
		}
	}
	return v
}

type eventV struct {
	ID       int64             `json:"id"`
	Time     int64             `json:"time_unix"`
	Severity string            `json:"severity"`
	Code     string            `json:"code"`
	Params   map[string]string `json:"params,omitempty"`
	NodeID   string            `json:"node_id,omitempty"`
	NodeName string            `json:"node_name,omitempty"`
	UserID   string            `json:"user_id,omitempty"`
	UserName string            `json:"user_name,omitempty"`
}

// EventsV is the events_search result.
type EventsV struct {
	Events       []eventV `json:"events"`
	HasMore      bool     `json:"has_more"`
	NextBeforeID int64    `json:"next_before_id,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

func (v *EventsV) shrink() bool {
	v.Truncated, v.HasMore = true, true
	ok := cut(&v.Events)
	if n := len(v.Events); n > 0 {
		v.NextBeforeID = v.Events[n-1].ID
	}
	return ok
}

func eventsView(evs []*adminv1.Event, code string, hasMore bool) *EventsV {
	v := &EventsV{Events: []eventV{}, HasMore: hasMore}
	for _, e := range evs {
		if code != "" && e.GetCode() != code {
			continue
		}
		v.Events = append(v.Events, eventV{
			ID: e.GetId(), Time: e.GetTimeUnix(), Severity: enumName("EVENT_SEVERITY_", e.GetSeverity().String()),
			Code: clean(e.GetCode(), 80), Params: cleanMap(e.GetParams(), maxParamValue),
			NodeID: e.GetNodeId(), NodeName: nm(e.GetNodeName()), UserID: e.GetUserId(), UserName: nm(e.GetUserName()),
		})
	}
	if hasMore && len(evs) > 0 {
		// the cursor is the oldest event the page covered, filtered out or not
		v.NextBeforeID = evs[len(evs)-1].GetId()
	}
	return v
}

type checkV struct {
	Deployed   bool   `json:"deployed"`
	Status     string `json:"status,omitempty"`
	AtUnix     int64  `json:"at_unix,omitempty"`
	LatencyMs  uint32 `json:"latency_ms,omitempty"`
	ExitCtry   string `json:"exit_country,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	ErrorInfo  string `json:"error_detail,omitempty"`
	FailStreak uint32 `json:"fail_streak,omitempty"`
	OK24h      uint32 `json:"ok_24h"`
	Failed24h  uint32 `json:"failed_24h"`
}

type checkRowV struct {
	NodeID     string   `json:"node_id"`
	NodeName   string   `json:"node_name"`
	NodeStatus string   `json:"node_status"`
	Cells      []checkV `json:"cells"`
}

type checkColV struct {
	ProfileID string `json:"profile_id"`
	Profile   string `json:"profile"`
	Protocol  string `json:"protocol"`
}

// ChecksV is the checks_results result: the node x profile matrix, one cell per column in the order of columns.
type ChecksV struct {
	NowUnix   int64       `json:"now_unix"`
	IntervalS uint32      `json:"interval_s"`
	Columns   []checkColV `json:"columns"`
	Rows      []checkRowV `json:"rows"`
	Truncated bool        `json:"truncated,omitempty"`
}

func (v *ChecksV) shrink() bool {
	v.Truncated = true
	return cut(&v.Rows)
}

func checksView(r *adminv1.GetChecksResponse) *ChecksV {
	v := &ChecksV{NowUnix: r.GetNowUnix(), IntervalS: r.GetIntervalS(), Columns: []checkColV{}, Rows: []checkRowV{}}
	for _, c := range r.GetColumns() {
		v.Columns = append(v.Columns, checkColV{ProfileID: c.GetProfileId(), Profile: nm(c.GetProfileName()), Protocol: clean(c.GetProtocol(), 40)})
	}
	for _, row := range r.GetRows() {
		rv := checkRowV{NodeID: row.GetNodeId(), NodeName: nm(row.GetNodeName()), NodeStatus: nodeStatus(row.GetNodeStatus()), Cells: []checkV{}}
		for _, c := range row.GetCells() {
			cv := checkV{Deployed: c.GetDeployed(), FailStreak: c.GetFailStreak()}
			if l := c.GetLast(); l != nil {
				cv.Status = enumName("CHECK_STATUS_", l.GetStatus().String())
				cv.AtUnix, cv.LatencyMs, cv.ExitCtry = l.GetAtUnix(), l.GetLatencyMs(), clean(l.GetExitCountry(), 8)
				cv.ErrorCode, cv.ErrorInfo = clean(l.GetErrorCode(), 60), clean(l.GetErrorDetail(), 200)
			}
			for _, b := range c.GetHistory() {
				cv.OK24h += b.GetOk()
				cv.Failed24h += b.GetFailed()
			}
			rv.Cells = append(rv.Cells, cv)
		}
		v.Rows = append(v.Rows, rv)
	}
	return v
}

type auditV struct {
	ID        int64  `json:"id"`
	Time      int64  `json:"time_unix"`
	Source    string `json:"source"`
	ActorID   string `json:"actor_id"`
	ActorName string `json:"actor_name,omitempty"`
	Action    string `json:"action"`
	Params    string `json:"params,omitempty"`
	Result    string `json:"result,omitempty"`
	IP        string `json:"ip,omitempty"`
}

// AuditV is the audit_search result.
type AuditV struct {
	Entries      []auditV `json:"entries"`
	NextBeforeID int64    `json:"next_before_id,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

func (v *AuditV) shrink() bool {
	v.Truncated = true
	ok := cut(&v.Entries)
	if n := len(v.Entries); n > 0 {
		v.NextBeforeID = v.Entries[n-1].ID
	}
	return ok
}

// ---------------------------------------------------------------------------------------------------------------------
// updates_status

type lastUpdateV struct {
	Outcome string `json:"outcome"`
	From    string `json:"from_version,omitempty"`
	To      string `json:"to_version,omitempty"`
	Reason  string `json:"reason,omitempty"`
	AtUnix  int64  `json:"at_unix"`
}

type nodeUpdateV struct {
	NodeID      string       `json:"node_id"`
	Name        string       `json:"name"`
	Version     string       `json:"version,omitempty"`
	Supports    bool         `json:"supports_update"`
	State       string       `json:"state"`
	Last        *lastUpdateV `json:"last_update,omitempty"`
	Inbounds    uint32       `json:"inbounds"`
	OnlineUsers uint32       `json:"online_users"`
}

type rolloutStepV struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Stage    uint32 `json:"stage"`
	State    string `json:"state"`
	From     string `json:"from_version,omitempty"`
	ErrorKey string `json:"error_key,omitempty"`
}

type rolloutV struct {
	ID        string         `json:"id"`
	Status    string         `json:"status"`
	ToVersion string         `json:"to_version"`
	BatchSize uint32         `json:"batch_size"`
	Created   int64          `json:"created_unix"`
	Finished  int64          `json:"finished_unix,omitempty"`
	PauseKey  string         `json:"pause_key,omitempty"`
	Steps     []rolloutStepV `json:"steps"`
}

type bundleV struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Built   int64  `json:"built,omitempty"`
	Expires int64  `json:"expires_unix,omitempty"`
	Files   int    `json:"files"`
	Error   string `json:"error_key,omitempty"`
}

// UpdatesV is the updates_status result.
type UpdatesV struct {
	NowUnix      int64         `json:"now_unix"`
	PanelVersion string        `json:"panel_version"`
	PanelBuilt   int64         `json:"panel_built,omitempty"`
	Bundle       *bundleV      `json:"bundle,omitempty"`
	Nodes        []nodeUpdateV `json:"nodes"`
	Rollout      *rolloutV     `json:"rollout,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"`
}

func (v *UpdatesV) shrink() bool {
	v.Truncated = true
	if v.Rollout != nil && cut(&v.Rollout.Steps) {
		return true
	}
	return cut(&v.Nodes)
}

func rolloutView(r *adminv1.Rollout) *rolloutV {
	if r == nil {
		return nil
	}
	v := &rolloutV{
		ID: r.GetId(), Status: enumName("ROLLOUT_STATUS_", r.GetStatus().String()), ToVersion: clean(r.GetToVersion(), 60),
		BatchSize: r.GetBatchSize(), Created: r.GetCreatedUnix(), Finished: r.GetFinishedUnix(), PauseKey: clean(r.GetPauseKey(), 80),
		Steps: []rolloutStepV{},
	}
	for _, s := range r.GetSteps() {
		v.Steps = append(v.Steps, rolloutStepV{
			NodeID: s.GetNodeId(), NodeName: nm(s.GetNodeName()), Stage: s.GetStage(), State: enumName("STEP_STATE_", s.GetState().String()),
			From: clean(s.GetFromVersion(), 60), ErrorKey: clean(s.GetErrorKey(), 80),
		})
	}
	return v
}

func updatesView(r *adminv1.GetUpdatesResponse) *UpdatesV {
	v := &UpdatesV{NowUnix: r.GetNowUnix(), PanelVersion: clean(r.GetPanel().GetVersion(), 60), PanelBuilt: r.GetPanel().GetBuilt(), Nodes: []nodeUpdateV{}}
	if b := r.GetBundle(); b != nil {
		v.Bundle = &bundleV{
			Status: enumName("BUNDLE_STATUS_", b.GetStatus().String()), Version: clean(b.GetVersion(), 60), Built: b.GetBuilt(),
			Expires: b.GetExpiresUnix(), Files: len(b.GetFiles()), Error: clean(b.GetErrorKey(), 80),
		}
	}
	for _, n := range r.GetNodes() {
		nv := nodeUpdateV{
			NodeID: n.GetNodeId(), Name: nm(n.GetName()), Version: clean(n.GetVersion(), 60), Supports: n.GetSupportsUpdate(),
			State: enumName("NODE_UPDATE_STATE_", n.GetState().String()), Inbounds: n.GetInbounds(), OnlineUsers: n.GetOnlineUsers(),
		}
		if l := n.GetLastUpdate(); l != nil && l.GetOutcome() != "" {
			nv.Last = &lastUpdateV{
				Outcome: clean(l.GetOutcome(), 40), From: clean(l.GetFromVersion(), 60), To: clean(l.GetToVersion(), 60),
				Reason: clean(l.GetReason(), 200), AtUnix: l.GetAtUnix(),
			}
		}
		v.Nodes = append(v.Nodes, nv)
	}
	v.Rollout = rolloutView(r.GetRollout())
	return v
}
