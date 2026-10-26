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
	DownBps     uint64 `json:"down_bps"`              // the person's whole rate on this node, not this row's share
	Connections int    `json:"connections,omitempty"` // more than one: the node reported this row that many times
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
	// one row per person, protocol and device: the node reports each connection, each with the person's whole rate
	seen := map[string]int{}
	for _, u := range r.GetOnlineUsers() {
		key := u.GetUserId() + "|" + u.GetProtocol() + "|" + u.GetDeviceModel()
		if i, ok := seen[key]; ok {
			v.OnlineUsers[i].Connections++
			continue
		}
		if len(v.OnlineUsers) == maxOnlineUsers {
			continue
		}
		seen[key] = len(v.OnlineUsers)
		v.OnlineUsers = append(v.OnlineUsers, onlineUserV{
			UserID: u.GetUserId(), UserName: nm(u.GetUserName()), Protocol: clean(u.GetProtocol(), 40),
			Device: nm(u.GetDeviceModel()), ConnectedAt: u.GetConnectedAtUnix(), DownBps: u.GetDownBps(), Connections: 1,
		})
	}
	for i := range v.OnlineUsers {
		if v.OnlineUsers[i].Connections == 1 {
			v.OnlineUsers[i].Connections = 0 // omitted: the usual case
		}
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
// node_ports

type portInboundV struct {
	InboundID string `json:"inbound_id"`
	ProfileID string `json:"profile_id"`
	Profile   string `json:"profile"`
}

type portCheckV struct {
	Port     uint32         `json:"port"`
	Verdict  string         `json:"verdict,omitempty"`
	Sent     uint32         `json:"sent"`
	Got      uint32         `json:"got"`
	Checked  int64          `json:"checked_unix"`
	Bad      int64          `json:"bad_unix"`
	Sender   string         `json:"sender,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Inbounds []portInboundV `json:"inbounds"`
}

// tcpCheckV is one row of the TCP part of the port check: does a connection to Port reach Mistgate itself.
type tcpCheckV struct {
	Port    uint32 `json:"port"`
	Role    string `json:"role"`
	ALPN    bool   `json:"alpn"`
	Profile string `json:"profile,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Sender  string `json:"sender,omitempty"`
	Checked int64  `json:"checked_unix"`
}

// NodePortsV is the node_ports result.
type NodePortsV struct {
	NodeID    string       `json:"node_id"`
	NodeName  string       `json:"node_name"`
	ErrorCode string       `json:"error_code,omitempty"`
	Sender    string       `json:"sender,omitempty"`
	Ports     []portCheckV `json:"ports"`
	TCP       []tcpCheckV  `json:"tcp"`
}

func nodePortsView(nodeID, nodeName string, checks []*adminv1.PortCheck, tcp []*adminv1.TcpCheck, inbounds []*adminv1.Inbound, errorCode, sender string) *NodePortsV {
	v := &NodePortsV{NodeID: nodeID, NodeName: nm(nodeName), ErrorCode: clean(errorCode, 40), Sender: clean(sender, 80), Ports: []portCheckV{}, TCP: []tcpCheckV{}}
	for _, row := range tcp {
		if row != nil {
			v.TCP = append(v.TCP, tcpCheckV{Port: row.GetPort(), Role: clean(row.GetRole(), 16), ALPN: row.GetAlpn(), Profile: nm(row.GetProfile()),
				Verdict: clean(row.GetVerdict(), 16), Reason: clean(row.GetReason(), 40), Sender: clean(row.GetSender(), 80), Checked: row.GetCheckedUnix()})
		}
	}
	for _, check := range checks {
		if check == nil {
			continue
		}
		row := portCheckV{Port: check.GetPort(), Verdict: clean(check.GetVerdict(), 16), Sent: check.GetSent(), Got: check.GetGot(),
			Checked: check.GetCheckedUnix(), Bad: check.GetBadUnix(), Sender: clean(check.GetSender(), 80), Reason: clean(check.GetReason(), 40),
			Inbounds: []portInboundV{}}
		if row.Reason == "" && row.Verdict == "" {
			row.Reason = v.ErrorCode
		}
		if row.Sender == "" {
			row.Sender = v.Sender
		}
		for _, inbound := range inbounds {
			if inbound == nil || inbound.GetPort() != row.Port || inbound.GetState() == adminv1.InboundState_INBOUND_STATE_DISABLED {
				continue
			}
			row.Inbounds = append(row.Inbounds, portInboundV{InboundID: clean(inbound.GetId(), 80), ProfileID: clean(inbound.GetProfileId(), 80),
				Profile: nm(inbound.GetProfileName())})
		}
		v.Ports = append(v.Ports, row)
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// users

type userV struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	GroupID     string `json:"group_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Status      string `json:"status"`
	Online      bool   `json:"online"`
	DevicesUsed uint32 `json:"devices_used"`
	DeviceLimit uint32 `json:"device_limit"`
	UsedBytes   uint64 `json:"used_bytes"`
	QuotaBytes  uint64 `json:"quota_bytes"`
	QuotaReset  string `json:"quota_reset,omitempty"`
	ExpiresUnix int64  `json:"expires_unix"`
	LastSeen    int64  `json:"last_seen_unix"`
	Node        string `json:"current_node,omitempty"`
	SpeedLimit  uint64 `json:"speed_limit_bps,omitempty"` // per person: all devices together, on one node
	// DeviceSpeedLimit: each device alone.
	DeviceSpeedLimit uint64   `json:"device_speed_limit_bps,omitempty"`
	Happ             bool     `json:"app_happ"`
	Amnezia          bool     `json:"app_amnezia"`
	AllNodes         bool     `json:"all_nodes"`
	NodeIDs          []string `json:"node_ids,omitempty"`
	CreatedUnix      int64    `json:"created_unix"`
}

func userView(u *adminv1.User) userV {
	v := userV{
		ID: u.GetId(), Name: nm(u.GetName()), GroupID: u.GetGroupId(), Group: nm(u.GetGroupName()),
		Status: enumName("USER_STATUS_", u.GetStatus().String()), Online: u.GetOnline(),
		DevicesUsed: u.GetDevicesUsed(), DeviceLimit: u.GetDeviceLimit(), UsedBytes: u.GetUsedBytes(), QuotaBytes: u.GetQuotaBytes(),
		QuotaReset: enumName("QUOTA_RESET_", u.GetQuotaReset().String()), ExpiresUnix: u.GetExpiresUnix(), LastSeen: u.GetLastSeenUnix(),
		Node: nm(u.GetCurrentNodeName()), SpeedLimit: u.GetSpeedLimitBps(), DeviceSpeedLimit: u.GetDeviceSpeedLimitBps(), Happ: u.GetApps().GetHapp(), Amnezia: u.GetApps().GetAmnezia(),
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
	ID string `json:"id"`
	// Kind: link (an app with an ID of its own), link_shared (the place of every app that sends none) or key (AmneziaVPN).
	Kind      string `json:"kind,omitempty"`
	Platform  string `json:"platform,omitempty"`
	Model     string `json:"model,omitempty"`
	OSVersion string `json:"os_version,omitempty"`
	Client    string `json:"client,omitempty"`
	UsedBytes uint64 `json:"used_bytes"`
	// Fingerprint: the first 6 hex characters of the hash of the device's ID, to tell two phones of one model apart.
	Fingerprint string `json:"fingerprint,omitempty"`
	// OnlineNodes: the nodes that have a session of this device now. Two or more: the same keys are in use on two servers.
	OnlineNodes []nodeRefV `json:"online_nodes,omitempty"`
	FirstSeen   int64      `json:"first_seen_unix"`
	LastSeen    int64      `json:"last_seen_unix"`
	Online      bool       `json:"online"`
	Protocols   []string   `json:"protocols,omitempty"`
	AwgProfile  string     `json:"awg_profile,omitempty"`
	AwgVersion  string     `json:"awg_version,omitempty"`
	Stale       bool       `json:"stale,omitempty"`
	Address     string     `json:"address,omitempty"`
	LastHandsh  int64      `json:"last_handshake_unix,omitempty"`
}

type nodeRefV struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type blockedDeviceV struct {
	ID          string `json:"id"`
	Platform    string `json:"platform,omitempty"`
	Model       string `json:"model,omitempty"`
	Client      string `json:"client,omitempty"`
	BlockedUnix int64  `json:"blocked_unix"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func blockedDeviceViews(bs []*adminv1.BlockedDevice) []blockedDeviceV {
	var out []blockedDeviceV
	for _, b := range bs {
		out = append(out, blockedDeviceV{ID: b.GetId(), Platform: clean(b.GetPlatform(), 40), Model: nm(b.GetModel()), Client: clean(b.GetClient(), 40),
			BlockedUnix: b.GetBlockedUnix(), Fingerprint: clean(b.GetFingerprint(), 12)})
	}
	return out
}

// deviceViews describes devices; nodes names the nodes an online device is on (node id -> name, from the user's node access).
func deviceViews(ds []*adminv1.Device, nodes map[string]string) []deviceV {
	out := make([]deviceV, 0, len(ds))
	for _, d := range ds {
		dv := deviceV{
			ID: d.GetId(), Kind: enumName("DEVICE_KIND_", d.GetKind().String()), Platform: clean(d.GetPlatform(), 40), Model: nm(d.GetModel()),
			OSVersion: clean(d.GetOsVersion(), 40), Client: clean(d.GetClient(), 40), UsedBytes: d.GetUsedBytes(), Fingerprint: clean(d.GetFingerprint(), 12),
			FirstSeen: d.GetFirstSeenUnix(),
			LastSeen:  d.GetLastSeenUnix(), Online: d.GetOnline(), AwgProfile: nm(d.GetAwgProfileName()),
			AwgVersion: clean(d.GetAwgVersion(), 20), Stale: d.GetStale(), Address: clean(d.GetAddress(), 64),
			LastHandsh: d.GetLastHandshakeUnix(),
		}
		for _, p := range d.GetProtocols() {
			dv.Protocols = append(dv.Protocols, clean(p, 40))
		}
		for _, id := range d.GetOnlineNodeIds() {
			dv.OnlineNodes = append(dv.OnlineNodes, nodeRefV{ID: id, Name: nm(nodes[id])})
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
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Protocol string           `json:"protocol"`
	Formats  []profileFormatV `json:"formats,omitempty"`
}

// profileFormatV says whether one subscription format (link_list, mihomo, xray_json, singbox_json) carries a profile.
// note is a stable code, never text: why the format leaves the profile out (xhttp, self_signed, gecko, awg, unsupported),
// or, with ok, a loss (hop_single_port).
type profileFormatV struct {
	Format string `json:"format"`
	OK     bool   `json:"ok"`
	Note   string `json:"note,omitempty"`
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
		r := profileRefV{ID: p.GetId(), Name: nm(p.GetName()), Protocol: clean(p.GetProtocol(), 40)}
		for _, f := range p.GetFormats() {
			r.Formats = append(r.Formats, profileFormatV{Format: subFormat(f.GetFormat()), OK: f.GetOk(), Note: clean(f.GetNote(), 40)})
		}
		out = append(out, r)
	}
	return out
}

// nodeNames maps node ids to names from the user's node access (the nodes an online device can be on).
func nodeNames(r *adminv1.GetUserResponse) map[string]string {
	out := make(map[string]string, len(r.GetNodeAccess()))
	for _, n := range r.GetNodeAccess() {
		out[n.GetNodeId()] = n.GetNodeName()
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
		User: userView(r.GetUser()), Devices: deviceViews(r.GetDevices(), nodeNames(r)), Profiles: profileRefs(r.GetProfiles()),
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
	UserID  string    `json:"user_id"`
	Name    string    `json:"name"`
	Limit   uint32    `json:"device_limit"`
	Devices []deviceV `json:"devices"`
	// Blocked: devices the owner revoked and refused; they are not in devices and take no place. Unblocking is in the admin panel.
	Blocked   []blockedDeviceV `json:"blocked,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
}

func (v *UserDevicesV) shrink() bool {
	v.Truncated = true
	return cut(&v.Devices) || cut(&v.Blocked)
}

func userDevicesView(r *adminv1.GetUserResponse) *UserDevicesV {
	u := r.GetUser()
	return &UserDevicesV{UserID: u.GetId(), Name: nm(u.GetName()), Limit: u.GetDeviceLimit(), Devices: deviceViews(r.GetDevices(), nodeNames(r)),
		Blocked: blockedDeviceViews(r.GetBlockedDevices())}
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
	Opened     int64             `json:"opened_unix"`
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
		Params: cleanMap(a.GetParams(), maxParamValue), Why: clean(a.GetWhyKey(), 80), First: a.GetFirstSeenUnix(), Opened: a.GetOpenedUnix(),
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
	Deployed  bool   `json:"deployed"`
	Status    string `json:"status,omitempty"`
	AtUnix    int64  `json:"at_unix,omitempty"`
	LatencyMs uint32 `json:"latency_ms,omitempty"` // cold time of the last round, from the panel's server
	// connect_ms and response_ms of the last round: the time to open the tunnel and the first connection, and one more
	// request on that connection (0 = not measured). The *_hour_ms fields are medians over the last hour (0 = no data).
	ConnectMs      uint32 `json:"connect_ms,omitempty"`
	ResponseMs     uint32 `json:"response_ms,omitempty"`
	ResponseHourMs uint32 `json:"response_hour_ms,omitempty"`
	ConnectHourMs  uint32 `json:"connect_hour_ms,omitempty"`
	// Hysteria2: the packets the panel's own QUIC client sent and declared lost, in the last round and summed over the last
	// hour (the panel's server path to the node, not a person's).
	UDPSent     uint32 `json:"udp_sent,omitempty"`
	UDPLost     uint32 `json:"udp_lost,omitempty"`
	UDPSentHour uint32 `json:"udp_sent_hour,omitempty"`
	UDPLostHour uint32 `json:"udp_lost_hour,omitempty"`
	// Hysteria2: the way back, what the node sent to the check and declared lost over the last hour (its own counters).
	NodeSentHour uint32 `json:"node_sent_hour,omitempty"`
	NodeLostHour uint32 `json:"node_lost_hour,omitempty"`
	ExitCtry     string `json:"exit_country,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorInfo    string `json:"error_detail,omitempty"`
	FailStreak   uint32 `json:"fail_streak,omitempty"`
	OK24h        uint32 `json:"ok_24h"`
	Failed24h    uint32 `json:"failed_24h"`
}

type checkRowV struct {
	NodeID     string   `json:"node_id"`
	NodeName   string   `json:"node_name"`
	NodeStatus string   `json:"node_status"`
	Cells      []checkV `json:"cells"`
	// Country is the latest country check of the node: how it looks from probes inside a country. Absent while that check
	// is off or has not run for the node.
	Country *countryCheckV `json:"country_check,omitempty"`
}

type countryPortV struct {
	Port uint32 `json:"port"`
	OK   bool   `json:"ok"`
	Ms   uint32 `json:"ms,omitempty"`
}

type countryProbeV struct {
	Network string         `json:"network"`
	ASN     uint32         `json:"asn,omitempty"`
	City    string         `json:"city,omitempty"`
	Eyeball bool           `json:"eyeball,omitempty"`
	Dark    bool           `json:"dark,omitempty"`
	ICMPOK  bool           `json:"icmp_ok"`
	ICMPMs  uint32         `json:"icmp_ms,omitempty"`
	TCP     []countryPortV `json:"tcp,omitempty"`
	Valid   bool           `json:"valid"` // false: it failed the control site or went offline; shown, not counted
}

// countryCheckV: verdict is ok, partial (some home networks get nothing from the node; dark marks their probes),
// tcp_blocked (ICMP passes, TCP is cut), blocked, listed (no live check, but the node's address is in a public block list:
// in the registry, not proven dropped) or unknown (reason says why a live round was not enough).
// Network names come from a third party and are shown as data.
type countryCheckV struct {
	Country string   `json:"country"`
	Verdict string   `json:"verdict"`
	AtUnix  int64    `json:"at_unix"`
	Reason  string   `json:"reason,omitempty"`
	Eyeball bool     `json:"eyeball,omitempty"`  // the probes are home or mobile networks
	NoProbe []string `json:"no_probe,omitempty"` // named operators without a matching online home or mobile probe
	// Confidence: eyeball or dc_only (a live check), listed (a block list only); absent when there is nothing to say.
	Confidence string `json:"confidence,omitempty"`
	// ListedBy: the block lists that hold the node's address (antifilter_ipsum, antifilter_community). CDNProvider and
	// CDNRegion: the CDN or hosting range the address is in. All three are empty while the block-list source is off.
	ListedBy    []string `json:"listed_by,omitempty"`
	CDNProvider string   `json:"cdn_provider,omitempty"`
	CDNRegion   string   `json:"cdn_region,omitempty"`
	// Sources: which sources the verdict rests on (globalping, tspu, lists). With both live sources, a Confidence of conflict
	// means they disagree and the worse verdict is shown; GlobalpingVerdict and TSPUVerdict are what each said on its own.
	Sources           []string `json:"sources,omitempty"`
	GlobalpingVerdict string   `json:"globalping_verdict,omitempty"`
	TSPUVerdict       string   `json:"tspu_verdict,omitempty"`
	// TSPU is the probe node's view of this node, when that source is on and has a recent run for it.
	TSPU   *countryTSPUV   `json:"tspu,omitempty"`
	Probes []countryProbeV `json:"probes"`
}

// countryTSPUV: what a node of the fleet inside the country saw with TCP SYNs of a limited TTL. A data-centre view, so its
// confidence is dc_only. Ports: blocked (the SYN dies at the TSPU), open (connected, or a router behind it answered), refused
// (a reset) or timeout (no verdict). DPIHop is the TTL of the last router before the TSPU box.
type countryTSPUV struct {
	Vantage string      `json:"probe_node"`
	AtUnix  int64       `json:"at_unix"`
	DPIHop  uint32      `json:"dpi_hop"`
	Ports   []tspuPortV `json:"ports"`
}

type tspuPortV struct {
	Port    uint32 `json:"port"`
	Verdict string `json:"verdict"`
}

// tspuRunV: the newest run of the TSPU probe. Status: ok, control_failed (the method does not apply on the probe node's network),
// no_dpi_hop (the TSPU could not be placed with the reference addresses; Reason says why), busy, failed (Detail says why).
// Absent while that source is off or has not run. Reason, for no_dpi_hop: refs_pass (every blocked reference was tried and at
// least one got an answer, so blocked addresses get through on the probe node's path and it has no TSPU this method can see),
// no_hops (no router on the path answered, so the route is hidden); absent when not known.
type tspuRunV struct {
	Status       string `json:"status"`
	AtUnix       int64  `json:"at_unix"`
	ProbeNode    string `json:"probe_node,omitempty"`
	DPIHop       uint32 `json:"dpi_hop,omitempty"`
	BlockedRef   string `json:"blocked_ref,omitempty"`
	NodesChecked uint32 `json:"nodes_checked,omitempty"`
	Detail       string `json:"detail,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func tspuRunView(r *adminv1.TspuProbeRun, names map[string]string) *tspuRunV {
	if r == nil {
		return nil
	}
	node := r.GetNodeId()
	if n, ok := names[node]; ok {
		node = n
	}
	return &tspuRunV{Status: clean(r.GetStatus(), 20), AtUnix: r.GetAtUnix(), ProbeNode: nm(node), DPIHop: r.GetDpiHop(), BlockedRef: clean(r.GetBlockedRef(), 45),
		NodesChecked: r.GetNodesChecked(), Detail: clean(r.GetDetail(), 40), Reason: clean(r.GetReason(), 20)}
}

func countryCheckView(c *adminv1.CountryCheck) *countryCheckV {
	if c == nil {
		return nil
	}
	v := &countryCheckV{Country: clean(c.GetCountry(), 8), Verdict: enumName("COUNTRY_VERDICT_", c.GetVerdict().String()), AtUnix: c.GetAtUnix(),
		Reason: clean(c.GetReason(), 40), Eyeball: c.GetEyeball(), Confidence: clean(c.GetConfidence(), 16),
		CDNProvider: nm(c.GetCdnProvider()), CDNRegion: nm(c.GetCdnRegion()), Probes: []countryProbeV{}}
	for _, name := range c.GetNoProbe() {
		v.NoProbe = append(v.NoProbe, clean(name, 40))
	}
	for _, l := range c.GetListedBy() {
		v.ListedBy = append(v.ListedBy, clean(l, 40))
	}
	for _, src := range c.GetSources() {
		v.Sources = append(v.Sources, clean(src, 20))
	}
	if g := c.GetGlobalpingVerdict(); g != adminv1.CountryVerdict_COUNTRY_VERDICT_UNSPECIFIED {
		v.GlobalpingVerdict = enumName("COUNTRY_VERDICT_", g.String())
	}
	if t := c.GetTspuVerdict(); t != adminv1.CountryVerdict_COUNTRY_VERDICT_UNSPECIFIED {
		v.TSPUVerdict = enumName("COUNTRY_VERDICT_", t.String())
		v.TSPU = &countryTSPUV{Vantage: nm(c.GetTspuVantageName()), AtUnix: c.GetTspuAtUnix(), DPIHop: c.GetDpiHop(), Ports: []tspuPortV{}}
		for _, p := range c.GetTspuPorts() {
			v.TSPU.Ports = append(v.TSPU.Ports, tspuPortV{Port: p.GetPort(), Verdict: clean(p.GetVerdict(), 12)})
		}
	}
	for _, p := range c.GetProbes() {
		pv := countryProbeV{Network: nm(p.GetNetwork()), ASN: p.GetAsn(), City: nm(p.GetCity()), Eyeball: p.GetEyeball(), Dark: p.GetDark(),
			ICMPOK: p.GetIcmpOk(), ICMPMs: p.GetIcmpMs(), Valid: p.GetValid()}
		for _, t := range p.GetTcp() {
			pv.TCP = append(pv.TCP, countryPortV{Port: t.GetPort(), OK: t.GetOk(), Ms: t.GetMs()})
		}
		v.Probes = append(v.Probes, pv)
	}
	return v
}

type checkColV struct {
	ProfileID string `json:"profile_id"`
	Profile   string `json:"profile"`
	Protocol  string `json:"protocol"`
}

// udpLossV is set when the panel's host loses inbound UDP: its UDP probes of these nodes keep needing a handshake retry
// while its TCP probes are fine. People connect to the nodes directly and are not affected.
type udpLossV struct {
	Nodes        []string `json:"nodes"`
	SharePercent uint32   `json:"share_percent"`
	// Packets the panel's Hysteria2 client lost per thousand sent over the listed nodes' rounds of the last hour (the way to
	// the nodes), and what the listed nodes lost of what they sent to the check (the way back).
	LostPermille     uint32 `json:"lost_permille,omitempty"`
	NodeLostPermille uint32 `json:"node_lost_permille,omitempty"`
}

// KlickReportsV is the klick_reports result: the switch of the kl!ck reports and, while it is on, what they say per node.
type KlickReportsV struct {
	Enabled   bool         `json:"enabled"`
	Reports   uint32       `json:"reports_7d"`
	LastUnix  int64        `json:"last_unix,omitempty"`
	NowUnix   int64        `json:"now_unix"`
	Nodes     []klickNodeV `json:"nodes"`
	Truncated bool         `json:"truncated,omitempty"`
}

type klickTallyV struct {
	Reports         uint32 `json:"reports"`
	OK              uint32 `json:"ok"`
	Freeze          uint32 `json:"freeze"`
	TCPBlocked      uint32 `json:"tcp_blocked"`
	HandshakeFailed uint32 `json:"handshake_failed"`
	NoAnswer        uint32 `json:"no_answer"`
	LastUnix        int64  `json:"last_unix"`
}

type klickGroupV struct {
	Name string `json:"name"`
	klickTallyV
}

type klickNodeV struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
	klickTallyV
	Operators []klickGroupV `json:"operators"`
	Regions   []klickGroupV `json:"regions"`
	Kinds     []klickKindV  `json:"kinds"`
}

type klickKindV struct {
	Kind string `json:"kind"`
	klickTallyV
	MedianDelayMS uint32 `json:"median_delay_ms"`
	DLChecked     uint32 `json:"dl_checked"`
	DLStalled     uint32 `json:"dl_stalled"`
}

func (v *KlickReportsV) shrink() bool {
	v.Truncated = true
	for i := range v.Nodes {
		if cut(&v.Nodes[i].Kinds) || cut(&v.Nodes[i].Operators) || cut(&v.Nodes[i].Regions) {
			return true
		}
	}
	return cut(&v.Nodes)
}

func klickTally(t *adminv1.ClientProbeTally) klickTallyV {
	return klickTallyV{Reports: t.GetReports(), OK: t.GetOk(), Freeze: t.GetFreeze(), TCPBlocked: t.GetTcpBlocked(),
		HandshakeFailed: t.GetHandshakeFailed(), NoAnswer: t.GetNoAnswer(), LastUnix: t.GetLastUnix()}
}

func klickGroups(gs []*adminv1.ClientProbeGroup) []klickGroupV {
	out := []klickGroupV{}
	for _, g := range gs {
		out = append(out, klickGroupV{Name: nm(g.GetName()), klickTallyV: klickTally(g.GetTally())})
	}
	return out
}

// klickReportsView projects GetClientProbe; node (an id, "" = every node) keeps one node. The operator and region names come
// from the app, so they are cleaned like any other name.
func klickReportsView(r *adminv1.GetClientProbeResponse, nodes []*adminv1.Node, node string) *KlickReportsV {
	s := r.GetSettings()
	v := &KlickReportsV{Enabled: s.GetEnabled(), Reports: s.GetReports(), LastUnix: s.GetLastUnix(), NowUnix: r.GetNowUnix(), Nodes: []klickNodeV{}}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.GetId()] = n.GetName()
	}
	for _, n := range r.GetNodes() {
		if node != "" && n.GetNodeId() != node {
			continue
		}
		nv := klickNodeV{NodeID: clean(n.GetNodeId(), 80), NodeName: nm(names[n.GetNodeId()]), klickTallyV: klickTally(n.GetTally()),
			Operators: klickGroups(n.GetOperators()), Regions: klickGroups(n.GetRegions()), Kinds: []klickKindV{}}
		for _, k := range n.GetKinds() {
			nv.Kinds = append(nv.Kinds, klickKindV{Kind: clean(k.GetKind(), 40), klickTallyV: klickTally(k.GetTally()),
				MedianDelayMS: k.GetMedianDelayMs(), DLChecked: k.GetDlChecked(), DLStalled: k.GetDlStalled()})
		}
		v.Nodes = append(v.Nodes, nv)
	}
	return v
}

// UserConnectionV is the user_connection result: a person's connection counters per device, node and inbound over the window.
type UserConnectionV struct {
	SinceUnix            int64          `json:"since_unix"`
	NowUnix              int64          `json:"now_unix"`
	Rows                 []deviceConnV  `json:"rows"`
	NodesWithoutCounters []nodeWithoutV `json:"nodes_without_counters"`
	Truncated            bool           `json:"truncated,omitempty"`
}

type deviceConnV struct {
	DeviceID     string      `json:"device_id"`
	NodeID       string      `json:"node_id"`
	NodeName     string      `json:"node_name"`
	InboundID    string      `json:"inbound_id"`
	Profile      string      `json:"profile"`
	Protocol     string      `json:"protocol"`
	Connects     uint32      `json:"connects"`
	Rejects      uint32      `json:"rejects"`
	Drops        uint32      `json:"drops"`
	Stalls       uint32      `json:"stalls"`
	OnlineS      uint64      `json:"online_s"`
	LastHourUnix int64       `json:"last_hour_unix"`
	Platform     string      `json:"platform"`
	Client       string      `json:"client"`
	Kind         string      `json:"kind"`
	Hours        []linkHourV `json:"hours,omitempty"`
	ClientCloses uint32      `json:"client_closes,omitempty"`
	Freezes      uint32      `json:"freezes,omitempty"`
	Quiet        uint32      `json:"quiet,omitempty"`
	RttMs        *uint32     `json:"rtt_ms,omitempty"`
	LossPermille *uint32     `json:"loss_permille,omitempty"`
	RttSamples   uint32      `json:"rtt_samples,omitempty"`
	PktsSent     uint64      `json:"pkts_sent,omitempty"`
}

type nodeWithoutV struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
}

func (v *UserConnectionV) shrink() bool {
	v.Truncated = true
	for i := range v.Rows {
		if cutOld(&v.Rows[i].Hours) {
			return true
		}
	}
	return cut(&v.Rows) || cut(&v.NodesWithoutCounters)
}

// userConnectionView projects GetUserConnection; the nodes of nodes_without_counters get their names from the node list.
func userConnectionView(r *adminv1.GetUserConnectionResponse, nodes []*adminv1.Node) *UserConnectionV {
	v := &UserConnectionV{SinceUnix: r.GetSinceUnix(), NowUnix: r.GetNowUnix(), Rows: []deviceConnV{}, NodesWithoutCounters: []nodeWithoutV{}}
	for _, d := range r.GetRows() {
		dv := deviceConnV{DeviceID: clean(d.GetDeviceId(), 80), NodeID: clean(d.GetNodeId(), 80), NodeName: nm(d.GetNodeName()),
			InboundID: clean(d.GetInboundId(), 80), Profile: nm(d.GetProfileName()), Protocol: clean(d.GetProtocol(), 40),
			Connects: d.GetConnects(), Rejects: d.GetRejects(), Drops: d.GetDrops(), Stalls: d.GetStalls(), OnlineS: d.GetOnlineS(),
			LastHourUnix: d.GetLastHourUnix(), Platform: clean(d.GetPlatform(), 40), Client: clean(d.GetClient(), 80), Kind: clean(d.GetKind(), 40),
			Hours: []linkHourV{}, ClientCloses: d.GetClientCloses(), Freezes: d.GetFreezes(), Quiet: d.GetQuiet(),
			RttMs: metricWhen(d.GetRttMs(), d.GetRttSamples() > 0), LossPermille: metricWhen(d.GetLossPermille(), d.GetPktsSent() > 0),
			RttSamples: d.GetRttSamples(), PktsSent: d.GetPktsSent()}
		for _, h := range d.GetHours() {
			dv.Hours = append(dv.Hours, linkHourView(h))
		}
		v.Rows = append(v.Rows, dv)
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.GetId()] = n.GetName()
	}
	for _, id := range r.GetNodesWithoutCounters() {
		v.NodesWithoutCounters = append(v.NodesWithoutCounters, nodeWithoutV{NodeID: clean(id, 80), NodeName: nm(names[id])})
	}
	return v
}

// NodeConnectionsV is the node_connections result: a node's connection counters per inbound over the window.
type NodeConnectionsV struct {
	NodeID    string         `json:"node_id"`
	NodeName  string         `json:"node_name"`
	Supported bool           `json:"supported"`
	SinceUnix int64          `json:"since_unix"`
	NowUnix   int64          `json:"now_unix"`
	Inbounds  []inboundConnV `json:"inbounds"`
	Truncated bool           `json:"truncated,omitempty"`
}

type inboundConnV struct {
	InboundID     string        `json:"inbound_id"`
	ProfileID     string        `json:"profile_id"`
	Profile       string        `json:"profile"`
	Protocol      string        `json:"protocol"`
	Connects      uint32        `json:"connects"`
	Rejects       uint32        `json:"rejects"`
	Drops         uint32        `json:"drops"`
	Stalls        uint32        `json:"stalls"`
	OnlineS       uint64        `json:"online_s"`
	UnknownKeys   uint32        `json:"unknown_keys"`
	Hellos        uint32        `json:"hellos"`
	Handshakes    uint32        `json:"handshakes"`
	KeysStalled   uint32        `json:"keys_stalled"`
	KeysTried     uint32        `json:"keys_tried"`
	Unsupported   []string      `json:"unsupported,omitempty"`
	Hours         []linkHourV   `json:"hours,omitempty"`
	Clients       []clientConnV `json:"clients,omitempty"`
	AWG           *awgPacketsV  `json:"awg,omitempty"`
	ClientCloses  uint32        `json:"client_closes,omitempty"`
	Freezes       uint32        `json:"freezes,omitempty"`
	Quiet         uint32        `json:"quiet,omitempty"`
	Accepted      uint32        `json:"accepted,omitempty"`
	HellosForeign uint32        `json:"hellos_foreign,omitempty"`
	RttMs         *uint32       `json:"rtt_ms,omitempty"`
	LossPermille  *uint32       `json:"loss_permille,omitempty"`
	RttSamples    uint32        `json:"rtt_samples,omitempty"`
	PktsSent      uint64        `json:"pkts_sent,omitempty"`
}

type linkHourV struct {
	HourUnix      int64   `json:"hour_unix"`
	Connects      uint32  `json:"connects,omitempty"`
	Rejects       uint32  `json:"rejects,omitempty"`
	Drops         uint32  `json:"drops,omitempty"`
	Stalls        uint32  `json:"stalls,omitempty"`
	OnlineS       uint64  `json:"online_s,omitempty"`
	UnknownKeys   uint32  `json:"unknown_keys,omitempty"`
	Hellos        uint32  `json:"hellos,omitempty"`
	Handshakes    uint32  `json:"handshakes,omitempty"`
	ClientCloses  uint32  `json:"client_closes,omitempty"`
	Freezes       uint32  `json:"freezes,omitempty"`
	Quiet         uint32  `json:"quiet,omitempty"`
	Accepted      uint32  `json:"accepted,omitempty"`
	HellosForeign uint32  `json:"hellos_foreign,omitempty"`
	RttMs         *uint32 `json:"rtt_ms,omitempty"`
	LossPermille  *uint32 `json:"loss_permille,omitempty"`
	RttSamples    uint32  `json:"rtt_samples,omitempty"`
	PktsSent      uint64  `json:"pkts_sent,omitempty"`
}

type clientConnV struct {
	Client       string `json:"client"`
	Platform     string `json:"platform"`
	Devices      uint32 `json:"devices"`
	Connects     uint32 `json:"connects"`
	Rejects      uint32 `json:"rejects"`
	Drops        uint32 `json:"drops"`
	Stalls       uint32 `json:"stalls"`
	OnlineS      uint64 `json:"online_s"`
	Freezes      uint32 `json:"freezes,omitempty"`
	Quiet        uint32 `json:"quiet,omitempty"`
	RttMs        uint32 `json:"rtt_ms,omitempty"`
	LossPermille uint32 `json:"loss_permille,omitempty"`
}

type awgPacketsV struct {
	UDPRxPackets uint64 `json:"udp_rx_packets"`
	ReportedUnix int64  `json:"reported_unix"`
	Backend      string `json:"backend"`
	PeersOnline  uint32 `json:"peers_online"`
}

func linkHourView(h *adminv1.LinkHour) linkHourV {
	return linkHourV{HourUnix: h.GetHourStartUnix(), Connects: h.GetConnects(), Rejects: h.GetRejects(), Drops: h.GetDrops(),
		Stalls: h.GetStalls(), OnlineS: h.GetOnlineS(), UnknownKeys: h.GetUnknownKeys(), Hellos: h.GetHellos(), Handshakes: h.GetHandshakes(),
		ClientCloses: h.GetClientCloses(), Freezes: h.GetFreezes(), Quiet: h.GetQuiet(), Accepted: h.GetAccepted(),
		HellosForeign: h.GetHellosForeign(), RttMs: metricWhen(h.GetRttMs(), h.GetRttSamples() > 0),
		LossPermille: metricWhen(h.GetLossPermille(), h.GetPktsSent() > 0), RttSamples: h.GetRttSamples(), PktsSent: h.GetPktsSent()}
}

func metricWhen(value uint32, available bool) *uint32 {
	if !available {
		return nil
	}
	return &value
}

func inboundConnectionsView(i *adminv1.InboundConnections) inboundConnV {
	v := inboundConnV{InboundID: clean(i.GetInboundId(), 80), ProfileID: clean(i.GetProfileId(), 80), Profile: nm(i.GetProfileName()),
		Protocol: clean(i.GetProtocol(), 40), Connects: i.GetConnects(), Rejects: i.GetRejects(), Drops: i.GetDrops(), Stalls: i.GetStalls(),
		OnlineS: i.GetOnlineS(), UnknownKeys: i.GetUnknownKeys(), Hellos: i.GetHellos(), Handshakes: i.GetHandshakes(),
		KeysStalled: i.GetKeysStalled(), KeysTried: i.GetKeysTried(), Unsupported: append([]string(nil), i.GetUnsupported()...),
		Hours: []linkHourV{}, Clients: []clientConnV{}, ClientCloses: i.GetClientCloses(), Freezes: i.GetFreezes(), Quiet: i.GetQuiet(),
		Accepted: i.GetAccepted(), HellosForeign: i.GetHellosForeign(), RttMs: metricWhen(i.GetRttMs(), i.GetRttSamples() > 0),
		LossPermille: metricWhen(i.GetLossPermille(), i.GetPktsSent() > 0), RttSamples: i.GetRttSamples(), PktsSent: i.GetPktsSent()}
	for _, h := range i.GetHours() {
		v.Hours = append(v.Hours, linkHourView(h))
	}
	for _, c := range i.GetClients() {
		v.Clients = append(v.Clients, clientConnV{Client: clean(c.GetClient(), 80), Platform: clean(c.GetPlatform(), 40), Devices: c.GetDevices(),
			Connects: c.GetConnects(), Rejects: c.GetRejects(), Drops: c.GetDrops(), Stalls: c.GetStalls(), OnlineS: c.GetOnlineS(),
			Freezes: c.GetFreezes(), Quiet: c.GetQuiet(), RttMs: c.GetRttMs(), LossPermille: c.GetLossPermille()})
	}
	if a := i.GetAwg(); a != nil {
		v.AWG = &awgPacketsV{UDPRxPackets: a.GetUdpRxPackets(), ReportedUnix: a.GetReportedUnix(), Backend: clean(a.GetBackend(), 32), PeersOnline: a.GetPeersOnline()}
	}
	return v
}

func (v *NodeConnectionsV) shrink() bool {
	v.Truncated = true
	for i := range v.Inbounds {
		if cutOld(&v.Inbounds[i].Hours) || cut(&v.Inbounds[i].Clients) {
			return true
		}
	}
	return cut(&v.Inbounds)
}

func nodeConnectionsView(r *adminv1.GetNodeConnectionsResponse, id, name string) *NodeConnectionsV {
	v := &NodeConnectionsV{NodeID: clean(id, 80), NodeName: nm(name), Supported: r.GetSupported(), SinceUnix: r.GetSinceUnix(), NowUnix: r.GetNowUnix(),
		Inbounds: []inboundConnV{}}
	for _, i := range r.GetInbounds() {
		v.Inbounds = append(v.Inbounds, inboundConnectionsView(i))
	}
	return v
}

// FleetConnectionsV is the all-node connection-counter summary.
type FleetConnectionsV struct {
	SinceUnix int64            `json:"since_unix"`
	NowUnix   int64            `json:"now_unix"`
	Nodes     []fleetNodeConnV `json:"nodes"`
	Truncated bool             `json:"truncated,omitempty"`
}

type fleetNodeConnV struct {
	NodeID      string         `json:"node_id"`
	NodeName    string         `json:"node_name"`
	CountryCode string         `json:"country_code"`
	Supported   bool           `json:"supported"`
	Inbounds    []inboundConnV `json:"inbounds"`
}

func (v *FleetConnectionsV) shrink() bool {
	v.Truncated = true
	for i := range v.Nodes {
		if cut(&v.Nodes[i].Inbounds) {
			return true
		}
	}
	return cut(&v.Nodes)
}

func fleetConnectionsView(r *adminv1.GetFleetConnectionsResponse) *FleetConnectionsV {
	v := &FleetConnectionsV{SinceUnix: r.GetSinceUnix(), NowUnix: r.GetNowUnix(), Nodes: []fleetNodeConnV{}}
	for _, n := range r.GetNodes() {
		nv := fleetNodeConnV{NodeID: clean(n.GetNodeId(), 80), NodeName: nm(n.GetNodeName()), CountryCode: clean(n.GetCountryCode(), 8),
			Supported: n.GetSupported(), Inbounds: []inboundConnV{}}
		for _, i := range n.GetInbounds() {
			nv.Inbounds = append(nv.Inbounds, inboundConnectionsView(i))
		}
		v.Nodes = append(v.Nodes, nv)
	}
	return v
}

// NodeJournalV is the node_journal result: saved warning and error records, ordered newest first.
type NodeJournalV struct {
	SinceUnix           int64               `json:"since_unix"`
	NowUnix             int64               `json:"now_unix"`
	Entries             []nodeJournalEntryV `json:"entries"`
	Truncated           bool                `json:"truncated,omitempty"`
	NodesWithoutJournal []string            `json:"nodes_without_journal"`
}

type nodeJournalEntryV struct {
	NodeID      string            `json:"node_id"`
	NodeName    string            `json:"node_name"`
	HourStart   int64             `json:"hour_start_unix"`
	Level       string            `json:"level"`
	Source      string            `json:"source"`
	Message     string            `json:"message"`
	InboundID   string            `json:"inbound_id"`
	ProfileName string            `json:"profile_name"`
	Attrs       map[string]string `json:"attrs"`
	Count       uint32            `json:"count"`
	FirstUnix   int64             `json:"first_unix"`
	LastUnix    int64             `json:"last_unix"`
}

func (v *NodeJournalV) shrink() bool {
	v.Truncated = true
	return cut(&v.Entries) || cut(&v.NodesWithoutJournal)
}

func nodeJournalView(r *adminv1.GetNodeJournalResponse) *NodeJournalV {
	v := &NodeJournalV{SinceUnix: r.GetSinceUnix(), NowUnix: r.GetNowUnix(), Entries: []nodeJournalEntryV{},
		NodesWithoutJournal: append([]string{}, r.GetNodesWithoutJournal()...), Truncated: r.GetTruncated()}
	for _, entry := range r.GetEntries() {
		v.Entries = append(v.Entries, nodeJournalEntryV{
			NodeID: clean(entry.GetNodeId(), 80), NodeName: nm(entry.GetNodeName()), HourStart: entry.GetHourStartUnix(),
			Level: enumName("EVENT_SEVERITY_", entry.GetLevel().String()), Source: clean(entry.GetSource(), 32),
			Message: clean(entry.GetMessage(), 256), InboundID: clean(entry.GetInboundId(), 64), ProfileName: nm(entry.GetProfileName()),
			Attrs: cleanMap(entry.GetAttrs(), 160), Count: entry.GetCount(), FirstUnix: entry.GetFirstUnix(), LastUnix: entry.GetLastUnix(),
		})
	}
	return v
}

// ChecksV is the checks_results result: the node x profile matrix, one cell per column in the order of columns.
// All times are measured on the panel's server, not on a person's device.
type ChecksV struct {
	NowUnix   int64       `json:"now_unix"`
	IntervalS uint32      `json:"interval_s"`
	Columns   []checkColV `json:"columns"`
	Rows      []checkRowV `json:"rows"`
	UdpLoss   *udpLossV   `json:"udp_loss,omitempty"`
	// TSPUProbe: the newest run of the TSPU probe, while that source is on and has run.
	TSPUProbe *tspuRunV `json:"tspu_probe,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
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
	country := map[string]*adminv1.CountryCheck{}
	for _, c := range r.GetCountryChecks() {
		country[c.GetNodeId()] = c
	}
	names := map[string]string{}
	for _, row := range r.GetRows() {
		names[row.GetNodeId()] = row.GetNodeName()
	}
	v.TSPUProbe = tspuRunView(r.GetTspuRun(), names)
	for _, row := range r.GetRows() {
		rv := checkRowV{NodeID: row.GetNodeId(), NodeName: nm(row.GetNodeName()), NodeStatus: nodeStatus(row.GetNodeStatus()), Cells: []checkV{},
			Country: countryCheckView(country[row.GetNodeId()])}
		for _, c := range row.GetCells() {
			cv := checkV{Deployed: c.GetDeployed(), FailStreak: c.GetFailStreak(), ResponseHourMs: c.GetResponseHourMs(), ConnectHourMs: c.GetConnectHourMs(),
				UDPSentHour: c.GetUdpSentHour(), UDPLostHour: c.GetUdpLostHour(), NodeSentHour: c.GetNodeSentHour(), NodeLostHour: c.GetNodeLostHour()}
			if l := c.GetLast(); l != nil {
				cv.Status = enumName("CHECK_STATUS_", l.GetStatus().String())
				cv.AtUnix, cv.LatencyMs, cv.ExitCtry = l.GetAtUnix(), l.GetLatencyMs(), clean(l.GetExitCountry(), 8)
				cv.ConnectMs, cv.ResponseMs = l.GetConnectMs(), l.GetResponseMs()
				cv.UDPSent, cv.UDPLost = l.GetUdpSent(), l.GetUdpLost()
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
	if u := r.GetUdpLoss(); u != nil {
		names := map[string]string{}
		for _, row := range r.GetRows() {
			names[row.GetNodeId()] = nm(row.GetNodeName())
		}
		uv := &udpLossV{Nodes: []string{}, SharePercent: u.GetSharePercent(), LostPermille: u.GetLostPermille(), NodeLostPermille: u.GetNodeLostPermille()}
		for _, id := range u.GetNodeIds() {
			if n := names[id]; n != "" {
				uv.Nodes = append(uv.Nodes, n)
			}
		}
		v.UdpLoss = uv
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
