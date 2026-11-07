//go:build !js

package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// access_report and block_diagnosis: "which servers do not open for people, for whom, and what to change". They compose what the
// panel already stores (link counters, users and groups, country checks, TSPU ports, kl!ck reports, port checks, path loss,
// relays and profiles) through the same admin procedures as the single-purpose reads, then apply fixed rules
// (tools_access_block.go, tools_access_report.go). The model explains and chooses; it does not invent evidence.

const (
	accessUserPage  = 200
	accessUserPages = 2 // at most 400 people are read; the result says when that is a sample
	accessWorkers   = 8
	accessNodeCap   = 40
	accessTimeout   = 75 * time.Second
)

var accessProcs = procs(
	adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure, adminv1connect.NodeServiceGetFleetConnectionsProcedure,
	adminv1connect.NodeServiceGetPathLossProcedure, adminv1connect.UserServiceListUsersProcedure, adminv1connect.UserServiceGetUserConnectionProcedure,
	adminv1connect.GroupServiceListGroupsProcedure, adminv1connect.ProfileServiceListProfilesProcedure, adminv1connect.HealthServiceGetChecksProcedure,
	adminv1connect.ClientProbeServiceGetClientProbeProcedure, adminv1connect.RelayServiceListRelaysProcedure,
)

var blockProcs = procs(
	adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure, adminv1connect.NodeServiceGetFleetConnectionsProcedure,
	adminv1connect.NodeServiceGetPathLossProcedure, adminv1connect.ProfileServiceListProfilesProcedure, adminv1connect.HealthServiceGetChecksProcedure,
	adminv1connect.ClientProbeServiceGetClientProbeProcedure, adminv1connect.RelayServiceListRelaysProcedure,
)

func accessTools() []toolDef {
	return []toolDef{
		readToolWithin(accessTimeout, "access_report", ProfileReadonly, accessProcs,
			"Which servers do not open for people, for whom, and what to change, in one call. hours is 1 to 168 (default 24); optional country (two letters), group (id or exact name), node (id or exact name) and protocol (hysteria2, awg, vless ...) narrow it. "+
				"matrix: per node and server (protocol, transport, port, network, egress, CDN) over the window: given_users, devices and users that tried and that succeeded, success_pct (absent for VLESS, which keeps no attempt counter: attempts_observable false), "+
				"rejects split as far as the counters allow (reject_split: key_expired, unknown_key, handshake_incomplete, no_auth, stalled_handshake), stalls, freezes, loss and rtt for Hysteria2, never-connected and untried people given the server, a verdict, and the same per group. "+
				"country: per node the country verdict, TSPU ports (blocked, open, refused, timeout), dark and reaching operators, and the kl!ck reports by operator and by server kind. "+
				"block_diagnosis: the block class of each node (see block_diagnosis). "+
				"findings: machine-readable codes (node_down, node_blocked_in_country, ip_blocked, port_blocked, tcp_throttled, udp_blocked, protocol_fingerprinted, whitelist_mode, operator_specific, regional, not_blocked_but_broken, protocol_blocked_on_node, udp_lossy_path, awg_handshake_no_reply, relay_down, group_without_working_way, cdn_only_option, given_never_connected, counters_unavailable) with severity, evidence refs and affected users and groups. "+
				"recommendations: ranked structured suggestions (kind, target, why = evidence refs, expected_effect, plan_tool when one tool makes the change, next_tool when a read or probe comes first): add_relay (relay_plan), move_port (profile_update_plan), prefer_protocol (group_update_plan), add_cdn_profile (profile_create_plan), enable_ipv6_entry (node_ipv6_plan), remove_from_subscriptions (node_update_plan), change_obfuscation, change_reality_sni, prefer_tcp_protocol, new_node_or_ip, check_entry_on_mobile, fix_ours, investigate. "+
				"The rules are fixed; a missing signal is reported as unknown with the check that would tell, never guessed. limits lists what the counters cannot tell apart. users_sampled true means only the first people were read. Drill down with user_diagnose, path_probe and node_trace. "+
				"Names and counts only: never a key, a link, a client address or a node address. "+connectiondiag2(),
			func(c *call, in accessArg) (any, error) { return c.accessReport(in) }),

		readToolWithin(accessTimeout, "block_diagnosis", ProfileReadonly, blockProcs,
			"How a node is blocked, not only whether: per node (and per port or protocol where it matters) a class with confidence, evidence refs, the operators and regions concerned, what is unknown and which check would tell, and the next steps with the plan tool for each. "+
				"Classes: ip_blocked (every TCP port dark, often ICMP too, the address itself is banned; changing ports does not help; scope tcp_only when ICMP still passes), "+
				"port_blocked (the TSPU probe or Globalping sees some ports cut while others of the same address open), "+
				"tcp_throttled (TCP connects but kl!ck downloads stall after a few KB: DPI freezing), "+
				"udp_blocked (packets from in-country senders are lost or QUIC and WireGuard handshakes never complete, while TCP works), "+
				"protocol_fingerprinted (UDP passes as a whole but one protocol's handshakes fail while another works: weak, low confidence until per-reason handshake counters exist), "+
				"whitelist_mode (mobile networks dark, fixed lines reach it: only whitelisted addresses help, ports and protocols do not), "+
				"operator_specific and regional (dark only for some operators or regions), not_blocked_but_broken (the node is down, a server failed or a port is closed on the host: our problem, not the censor's), and unknown (no view from inside the country). "+
				"node (id or exact name), country and hours (1 to 168, default 24, for the link counters) are optional. Stored data only: it contacts no node. Names and counts only. "+connectiondiag2(),
			func(c *call, in blockArg) (any, error) { return c.blockDiagnosis(in) }),
	}
}

// connectiondiag2 is the shared note on counters.
func connectiondiag2() string {
	return "Counters mean what user_connection says they mean; zero values of unsupported counters mean nothing."
}

type accessArg struct {
	Hours    int    `json:"hours,omitempty" jsonschema:"window in hours back from now, 1 to 168; empty = 24"`
	Country  string `json:"country,omitempty" jsonschema:"two-letter country code of the people, e.g. RU"`
	Group    string `json:"group,omitempty" jsonschema:"a group id or its exact name"`
	Node     string `json:"node,omitempty" jsonschema:"node id or exact name"`
	Protocol string `json:"protocol,omitempty" jsonschema:"hysteria2, awg, vless or another protocol id"`
}

type blockArg struct {
	Node    string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every node"`
	Country string `json:"country,omitempty" jsonschema:"two-letter country code, e.g. RU"`
	Hours   int    `json:"hours,omitempty" jsonschema:"window of the link counters, 1 to 168; empty = 24"`
}

// BlockDiagnosisV is the block_diagnosis result.
type BlockDiagnosisV struct {
	NowUnix     int64        `json:"now_unix"`
	Hours       int          `json:"hours"`
	Nodes       []blockNodeV `json:"nodes"`
	Unavailable []string     `json:"unavailable,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
}

func (v *BlockDiagnosisV) shrink() bool {
	v.Truncated = true
	for i := range v.Nodes {
		for j := range v.Nodes[i].Classes {
			if len(v.Nodes[i].Classes[j].Next) > 0 {
				for a := range v.Nodes {
					for b := range v.Nodes[a].Classes {
						v.Nodes[a].Classes[b].Next = nil
					}
				}
				return true
			}
		}
	}
	return cut(&v.Nodes)
}

func accessHours(h int) (int, error) {
	if h < 0 || h > 168 {
		return 0, errors.New("hours must be 1 to 168")
	}
	if h == 0 {
		h = 24
	}
	return h, nil
}

func (c *call) accessReport(a accessArg) (any, error) {
	hours, err := accessHours(a.Hours)
	if err != nil {
		return nil, err
	}
	in := &accessInput{Hours: hours}
	if a.Country != "" {
		if len(a.Country) != 2 {
			return nil, errors.New("country: two letters")
		}
		in.Country = strings.ToUpper(a.Country)
	}
	if a.Protocol != "" {
		in.Protocol = normProtocol(clean(a.Protocol, 40))
	}
	if err := c.gatherAccess(in, a.Node, a.Group, true); err != nil {
		return nil, err
	}
	return buildAccess(in), nil
}

func (c *call) blockDiagnosis(a blockArg) (any, error) {
	hours, err := accessHours(a.Hours)
	if err != nil {
		return nil, err
	}
	in := &accessInput{Hours: hours}
	if a.Country != "" {
		if len(a.Country) != 2 {
			return nil, errors.New("country: two letters")
		}
		in.Country = strings.ToUpper(a.Country)
	}
	if err := c.gatherAccess(in, a.Node, "", false); err != nil {
		return nil, err
	}
	r := buildAccess(in)
	return &BlockDiagnosisV{NowUnix: r.NowUnix, Hours: r.Hours, Nodes: r.BlockDiagnosis, Unavailable: r.Unavailable}, nil
}

func inboundStateName(s adminv1.InboundState) string { return enumName("INBOUND_STATE_", s.String()) }

// gatherAccess reads the stored facts. Only the node list is required; a source that fails is named in unavailable.
func (c *call) gatherAccess(in *accessInput, nodeRef, groupRef string, withUsers bool) error {
	now := c.e.now().Unix()
	in.NowUnix, in.SinceUnix = now, now-int64(in.Hours)*3600
	in.Profiles = map[string]accProfile{}
	in.Totals = map[string]accTotals{}
	in.Countries = map[string]*accCountry{}
	in.Klick = map[string]*accKlick{}
	in.UDPLossNodes = map[string]bool{}
	miss := func(s string) { in.Unavailable = append(in.Unavailable, s) }

	nodes, err := c.listNodes()
	if err != nil {
		return err
	}
	if nodeRef != "" {
		n, err := resolveNode(nodes, nodeRef)
		if err != nil {
			return err
		}
		in.Node = n.GetId()
	}
	ids := map[string]string{}
	for _, n := range nodes {
		st := nodeStatus(n.GetStatus())
		if st == "retired" {
			continue
		}
		an := accNode{ID: clean(n.GetId(), 80), Name: nm(n.GetName()), Country: clean(n.GetCountryCode(), 8), Status: st}
		if v6 := n.GetIpv6(); v6 != nil {
			an.IPv6Supported, an.IPv6On = v6.GetEntrySupported(), v6.GetEntry() != ""
		}
		ids[n.GetId()] = n.GetName()
		in.Nodes = append(in.Nodes, an)
	}
	if len(in.Nodes) > accessNodeCap {
		in.Nodes = in.Nodes[:accessNodeCap]
		miss("nodes_beyond_cap")
	}
	byID := map[string]*accNode{}
	for i := range in.Nodes {
		byID[in.Nodes[i].ID] = &in.Nodes[i]
	}
	for i := range in.Nodes {
		n := &in.Nodes[i]
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: n.ID}))
		if err != nil {
			miss("node:" + n.Name)
			continue
		}
		for _, x := range r.Msg.GetInbounds() {
			in.Inbounds = append(in.Inbounds, accInbound{ID: clean(x.GetId(), 80), NodeID: n.ID, ProfileID: clean(x.GetProfileId(), 80), Protocol: clean(x.GetProtocol(), 40),
				Egress: clean(x.GetEgress(), 20), Port: x.GetPort(), Enabled: x.GetEnabled(), Direct: x.GetDirectInSubscriptions(), CDN: x.GetCdn(),
				State: inboundStateName(x.GetState()), LastErrorCode: errorCode(x.GetLastError())})
		}
		for _, p := range r.Msg.GetPortChecks() {
			n.UDPChecks = append(n.UDPChecks, accUDPCheck{Port: p.GetPort(), Verdict: clean(p.GetVerdict(), 12), Sender: nm(p.GetSender())})
		}
		for _, t := range r.Msg.GetTcpChecks() {
			n.TCPChecks = append(n.TCPChecks, accTCPCheck{Port: t.GetPort(), Verdict: clean(t.GetVerdict(), 12), Sender: nm(t.GetSender())})
		}
	}
	if r, err := c.cl.Profile.ListProfiles(c.ctx, connect.NewRequest(&adminv1.ListProfilesRequest{})); err != nil {
		miss("profiles")
	} else {
		for _, p := range r.Msg.GetProfiles() {
			in.Profiles[p.GetId()] = accProfile{ID: p.GetId(), Name: nm(p.GetName()), Protocol: clean(p.GetProtocol(), 40), Summary: clean(p.GetSummary(), 200)}
		}
	}
	if r, err := c.cl.Node.GetFleetConnections(c.ctx, connect.NewRequest(&adminv1.GetFleetConnectionsRequest{Hours: uint32(in.Hours)})); err != nil {
		miss("connection_counters")
	} else {
		for _, fn := range r.Msg.GetNodes() {
			if n := byID[fn.GetNodeId()]; n != nil && !fn.GetSupported() {
				n.NoCounters = true
			}
			for _, i := range fn.GetInbounds() {
				in.Totals[i.GetInboundId()] = accTotals{Protocol: clean(i.GetProtocol(), 40), Connects: i.GetConnects(), Rejects: i.GetRejects(), Drops: i.GetDrops(), Stalls: i.GetStalls(),
					Freezes: i.GetFreezes(), Quiet: i.GetQuiet(), ClientCloses: i.GetClientCloses(), Hellos: i.GetHellos(), Handshakes: i.GetHandshakes(), Accepted: i.GetAccepted(),
					HellosForeign: i.GetHellosForeign(), UnknownKeys: i.GetUnknownKeys(), KeysStalled: i.GetKeysStalled(), KeysTried: i.GetKeysTried(), OnlineS: i.GetOnlineS(),
					RttMs: i.GetRttMs(), RttSamples: i.GetRttSamples(), LossPermille: i.GetLossPermille(), PktsSent: i.GetPktsSent(), Unsupported: slices.Clone(i.GetUnsupported())}
			}
		}
	}
	if r, err := c.cl.Health.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{})); err != nil {
		miss("checks")
	} else {
		for _, id := range r.Msg.GetUdpLoss().GetNodeIds() {
			in.UDPLossNodes[id] = true
		}
		in.TSPURunStatus = clean(r.Msg.GetTspuRun().GetStatus(), 20)
		for _, cc := range r.Msg.GetCountryChecks() {
			if old := in.Countries[cc.GetNodeId()]; old != nil && old.AtUnix >= cc.GetAtUnix() {
				continue
			}
			in.Countries[cc.GetNodeId()] = countryFromProto(cc)
		}
	}
	if r, err := c.cl.ClientProbe.GetClientProbe(c.ctx, connect.NewRequest(&adminv1.GetClientProbeRequest{})); err != nil {
		miss("klick")
	} else {
		in.KlickEnabled = r.Msg.GetSettings().GetEnabled()
		for _, n := range r.Msg.GetNodes() {
			in.Klick[n.GetNodeId()] = klickFromProto(n)
		}
	}
	if r, err := c.cl.Relay.ListRelays(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.ListRelaysRequest{})); err != nil {
		miss("relays")
	} else {
		for _, x := range r.Msg.GetRelays() {
			ar := accRelay{ID: clean(x.GetId(), 80), EntryID: clean(x.GetViaNodeId(), 80), ExitID: clean(x.GetExitNodeId(), 80), InboundID: clean(x.GetInboundId(), 80),
				ProfileID: clean(x.GetProfileId(), 80), State: inboundStateName(x.GetState()), Enabled: x.GetEnabled(), ExitLive: x.GetExitLive(), AgentSupported: x.GetAgentSupported(),
				MonitorFails: x.GetMonitor().GetFailStreak()}
			if ck := x.GetCheck(); ck != nil && ck.GetAtUnix() != 0 {
				ar.Checked, ar.CheckOK = true, ck.GetOk()
			}
			in.Relays = append(in.Relays, ar)
		}
	}
	if r, err := c.cl.Node.GetPathLoss(c.ctx, connect.NewRequest(&adminv1.GetPathLossRequest{Hours: uint32(in.Hours)})); err != nil {
		miss("path_loss")
	} else {
		for _, row := range r.Msg.GetRows() {
			in.Loss = append(in.Loss, accLoss{FromID: row.GetFromNodeId(), FromName: nm(row.GetFromName()), ToID: row.GetToNodeId(), ToName: nm(row.GetToName()), LossPct: float64(row.GetLossPct())})
		}
	}
	if !withUsers {
		return nil
	}
	return c.gatherUsers(in, groupRef)
}

func countryFromProto(cc *adminv1.CountryCheck) *accCountry {
	o := &accCountry{NodeID: cc.GetNodeId(), Country: clean(cc.GetCountry(), 8), Verdict: enumName("COUNTRY_VERDICT_", cc.GetVerdict().String()),
		Confidence: clean(cc.GetConfidence(), 20), Sources: cleanAll(cc.GetSources(), 20, 8), ListedBy: cleanAll(cc.GetListedBy(), 40, 8)}
	if v6 := cc.GetIpv6(); v6 != nil {
		o.IPv6Verdict = enumName("COUNTRY_VERDICT_", v6.GetVerdict().String())
	}
	o.AtUnix = cc.GetAtUnix()
	for _, p := range cc.GetTspuPorts() {
		o.TSPU = append(o.TSPU, accTSPUPort{Port: p.GetPort(), Verdict: clean(p.GetVerdict(), 12)})
	}
	for _, v := range cc.GetVantages() {
		o.Vantages = append(o.Vantages, accVantage{Network: nm(v.GetNetwork()), Region: nm(v.GetRegion()), Kind: clean(v.GetKind(), 12), State: clean(v.GetState(), 12), ICMPOK: v.GetIcmpOk(), TCPOK: v.GetTcpOk()})
	}
	ports := map[uint32]*accProbePort{}
	for _, p := range cc.GetProbes() {
		if !p.GetValid() || !p.GetEyeball() {
			continue
		}
		for _, t := range p.GetTcp() {
			pp := ports[t.GetPort()]
			if pp == nil {
				pp = &accProbePort{Port: t.GetPort()}
				ports[t.GetPort()] = pp
			}
			pp.Probes++
			if !t.GetOk() {
				pp.Fail++
			}
		}
	}
	for _, p := range ports {
		p.OK = p.Fail < p.Probes
		o.ProbePorts = append(o.ProbePorts, *p)
	}
	slices.SortFunc(o.ProbePorts, func(a, b accProbePort) int { return int(a.Port) - int(b.Port) })
	for _, sv := range cc.GetSourceVerdicts() {
		if strings.Contains(strings.ToLower(sv.GetReason()), "whitelist") && (strings.HasPrefix(sv.GetSource(), "bsbord") || strings.HasPrefix(sv.GetSource(), "cheburcheck")) {
			o.WhitelistSource = true
		}
	}
	return o
}

func tallyFromProto(t *adminv1.ClientProbeTally) accKlickTally {
	return accKlickTally{Reports: t.GetReports(), OK: t.GetOk(), Freeze: t.GetFreeze(), TCPBlocked: t.GetTcpBlocked(), HandshakeFailed: t.GetHandshakeFailed(), NoAnswer: t.GetNoAnswer()}
}

func klickFromProto(n *adminv1.ClientProbeNode) *accKlick {
	k := &accKlick{accKlickTally: tallyFromProto(n.GetTally())}
	for _, g := range n.GetOperators() {
		k.Operators = append(k.Operators, accKlickGroup{Name: clean(g.GetName(), 40), accKlickTally: tallyFromProto(g.GetTally())})
	}
	for _, g := range n.GetRegions() {
		k.Regions = append(k.Regions, accKlickGroup{Name: clean(g.GetName(), 40), accKlickTally: tallyFromProto(g.GetTally())})
	}
	for _, x := range n.GetKinds() {
		k.Kinds = append(k.Kinds, accKlickKind{Kind: clean(x.GetKind(), 40), accKlickTally: tallyFromProto(x.GetTally()), MedianDelayMs: x.GetMedianDelayMs(), DLChecked: x.GetDlChecked(), DLStalled: x.GetDlStalled()})
	}
	return k
}

// gatherUsers reads the groups, the people (at most accessUserPages of accessUserPage) and each one's device counters.
func (c *call) gatherUsers(in *accessInput, groupRef string) error {
	miss := func(s string) { in.Unavailable = append(in.Unavailable, s) }
	gr, err := c.cl.Group.ListGroups(c.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	if err != nil {
		miss("groups")
		return nil
	}
	for _, g := range gr.Msg.GetGroups() {
		in.Groups = append(in.Groups, accGroup{ID: g.GetId(), Name: nm(g.GetName()), ProfileIDs: slices.Clone(g.GetProfileIds()), DisabledNodes: slices.Clone(g.GetDisabledNodeIds())})
		if groupRef != "" && (g.GetId() == groupRef || g.GetName() == groupRef) {
			in.Group = g.GetId()
		}
	}
	if groupRef != "" && in.Group == "" {
		return errors.New("group not found")
	}
	token := ""
	for page := 0; page < accessUserPages; page++ {
		r, err := c.cl.User.ListUsers(c.ctx, connect.NewRequest(&adminv1.ListUsersRequest{GroupId: in.Group, PageSize: accessUserPage, PageToken: token}))
		if err != nil {
			miss("users")
			break
		}
		in.UsersTotal = int(r.Msg.GetCounts().GetAll())
		for _, u := range r.Msg.GetUsers() {
			if u.GetStatus() == adminv1.UserStatus_USER_STATUS_DISABLED {
				continue
			}
			in.Users = append(in.Users, accUser{ID: clean(u.GetId(), 80), GroupID: u.GetGroupId(), AllNodes: u.GetNodes().GetAll() || u.GetNodes() == nil,
				NodeIDs: slices.Clone(u.GetNodes().GetNodeIds()), LastSeen: u.GetLastSeenUnix()})
		}
		token = r.Msg.GetNextPageToken()
		if token == "" {
			break
		}
	}
	if in.UsersTotal < len(in.Users) {
		in.UsersTotal = len(in.Users)
	}
	var mu sync.Mutex
	failed := false
	sem := make(chan struct{}, accessWorkers)
	var wg sync.WaitGroup
	for _, u := range in.Users {
		sem <- struct{}{}
		wg.Add(1)
		go func(uid string) {
			defer func() { <-sem; wg.Done() }()
			ctx, cancel := context.WithCancel(c.ctx)
			defer cancel()
			r, err := c.cl.User.GetUserConnection(ctx, connect.NewRequest(&adminv1.GetUserConnectionRequest{UserId: uid, Hours: uint32(in.Hours)}))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = true
				return
			}
			for _, d := range r.Msg.GetRows() {
				in.Rows = append(in.Rows, accRow{UserID: uid, DeviceID: clean(d.GetDeviceId(), 80), NodeID: d.GetNodeId(), InboundID: d.GetInboundId(),
					Connects: d.GetConnects(), Rejects: d.GetRejects(), Drops: d.GetDrops(), Stalls: d.GetStalls(), Freezes: d.GetFreezes(), Quiet: d.GetQuiet(),
					ClientCloses: d.GetClientCloses(), OnlineS: d.GetOnlineS(), RttMs: d.GetRttMs(), RttSamples: d.GetRttSamples(), LossPermille: d.GetLossPermille(), PktsSent: d.GetPktsSent()})
			}
		}(u.ID)
	}
	wg.Wait()
	if failed {
		miss("device_counters_partial")
	}
	return nil
}
