package fleet

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type nodeService struct{ f *Fleet }

var (
	nodeNameRe = regexp.MustCompile(`^[a-z0-9-]{2,24}$`)
	hostRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

func invalid(msg string) error { return connect.NewError(connect.CodeInvalidArgument, errors.New(msg)) }

// Refusals the admin UI words itself ("err.<code>", web/src/lib/errors.ts).
var (
	errNodeOffline = connect.NewError(connect.CodeFailedPrecondition, errors.New("node_offline"))
	errNodeRetired = connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	errNameTaken   = connect.NewError(connect.CodeAlreadyExists, errors.New("name_taken"))
)

func internalErr(log func(msg string, args ...any), what string, err error) error {
	log(what, "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

func validAddress(a string) bool {
	return len(a) <= 253 && (net.ParseIP(a) != nil || hostRe.MatchString(a))
}

func validResolver(r string) bool {
	if net.ParseIP(r) != nil {
		return true
	}
	host, port, err := net.SplitHostPort(r)
	return err == nil && net.ParseIP(host) != nil && port != ""
}

func validText(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func todayStart(now time.Time) int64 {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()
}

// audit records an admin action; a failure to write it is logged, not fatal.
func (f *Fleet) audit(ctx context.Context, action string, params map[string]string) {
	b, _ := json.Marshal(params)
	err := f.st.Audit(ctx, f.now(), store.AuditEntry{Actor: f.cfg.Actor(ctx), Action: action, Params: string(b), Result: "ok"})
	if err != nil {
		f.log.Warn("audit", "action", action, "err", err)
	}
}

// --- status ---

type nodeStatus struct {
	status adminv1.NodeStatus
	reason *adminv1.StatusReason
}

func reason(code string, kv ...string) *adminv1.StatusReason {
	r := &adminv1.StatusReason{Code: code, Params: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Params[kv[i]] = kv[i+1]
	}
	return r
}

// problem is what counts towards the problems of the fleet, the same rule as the SPA's isProblem (lib/node-status.ts): a
// node that is down or lets no client through, or an online one with any reason but a slightly drifting clock. A host
// blip, a pending node and an update are not problems.
func (st nodeStatus) problem() bool {
	switch st.status {
	case adminv1.NodeStatus_NODE_STATUS_DOWN, adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC:
		return true
	case adminv1.NodeStatus_NODE_STATUS_ONLINE:
		return st.reason != nil && st.reason.Code != "clock_skew"
	}
	return false
}

// inboundsOf is the node's part of store.FleetEnabledInbounds. A node without enabled inbounds gets an empty map (known:
// none), never nil (nil tells statusOf that the caller did not look).
func inboundsOf(all map[string]map[string]string, nodeID string) map[string]string {
	if m := all[nodeID]; m != nil {
		return m
	}
	return map[string]string{}
}

// statusOf derives the status shown in the UI from the stored state, the stream registry and time. enabled is the node's
// enabled inbounds (inbound id -> profile name); nil when the caller needs the status alone, and then the reasons that
// count profiles (no_profiles, the profile and the counts of inbound_failed) are left out.
func (f *Fleet) statusOf(ctx context.Context, n store.NodeRow, s *session, enabled map[string]string, now time.Time) nodeStatus {
	switch n.State {
	case "retired":
		return nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_RETIRED}
	case "pending":
		st := nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_PENDING}
		exp, err := f.st.PendingEnrollmentExpiry(ctx, n.ID, now)
		switch {
		case err != nil:
		case exp.IsZero(): // no usable token: the command expired (or was used and the agent never came)
			st.reason = reason("enrollment_expired")
		default:
			st.reason = reason("enrollment_pending", "expires_in_minutes", fmt.Sprint(int(exp.Sub(now).Minutes())), "expires_unix", fmt.Sprint(exp.Unix()))
		}
		return st
	}
	if f.updating(n.ID) { // a rollout step is in flight: the agent re-executes and its stream drops for seconds
		return nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_UPDATING}
	}
	if s != nil {
		st := nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_ONLINE}
		s.liveMu.Lock()
		drift := s.drift
		var failed []*agentv1.InboundHealth
		for _, h := range s.health {
			_, known := enabled[h.InboundId]
			if h.State == agentv1.InboundRunState_INBOUND_RUN_STATE_FAILED && (enabled == nil || known) {
				failed = append(failed, h)
			}
		}
		s.liveMu.Unlock()
		switch {
		case len(failed) > 0:
			// failed of total: one profile of three down is "works partly", all of them is "broken"
			first := failed[0]
			st.reason = reason("inbound_failed", "inbound", first.InboundId, "error", first.Detail)
			if enabled != nil {
				st.reason.Params["profile"] = enabled[first.InboundId]
				st.reason.Params["failed"] = fmt.Sprint(len(failed))
				st.reason.Params["total"] = fmt.Sprint(max(len(enabled), len(failed)))
			}
		case drift:
			st.reason = reason("state_drift")
		}
		if h := f.hooks(); h != nil {
			noTraffic, failed, total, doctorFail := h.NodeHealth(n.ID)
			switch {
			case noTraffic: // the agent talks but every synthetic check fails
				st.status = adminv1.NodeStatus_NODE_STATUS_NO_TRAFFIC
				st.reason = reason("no_traffic", "failed", fmt.Sprint(failed), "total", fmt.Sprint(total))
			case st.reason == nil && doctorFail != "":
				st.reason = reason("doctor_fail", "check", doctorFail)
			}
		}
		if st.reason == nil && enabled != nil && len(enabled) == 0 { // the agent is fine, but nobody gets this node
			st.reason = reason("no_profiles")
		}
		return st
	}
	gap := now.Sub(latest(n.LastSeenAt, n.LastDisconnectedAt, n.LastConnectedAt))
	minutes := fmt.Sprint(int(gap.Minutes()))
	if gap < blipWindow {
		return nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_BLIP, reason: reason("host_blip", "minutes", minutes)}
	}
	return nodeStatus{status: adminv1.NodeStatus_NODE_STATUS_DOWN, reason: reason("agent_silent", "minutes", minutes)}
}

// onlineByProtocol counts distinct online users per protocol.
func (s *session) onlineByProtocol() map[string]uint32 {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	seen := map[string]map[string]bool{}
	for _, o := range s.online {
		if seen[o.protocol] == nil {
			seen[o.protocol] = map[string]bool{}
		}
		seen[o.protocol][o.userID] = true
	}
	out := map[string]uint32{}
	for p, u := range seen {
		out[p] = uint32(len(u))
	}
	return out
}

func protocolCounts(m map[string]uint32) []*adminv1.ProtocolCount {
	out := make([]*adminv1.ProtocolCount, 0, len(m))
	for p, n := range m {
		out = append(out, &adminv1.ProtocolCount{Protocol: p, Users: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Protocol < out[j].Protocol })
	return out
}

func (s *session) currentMetrics() *agentv1.HostMetrics {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return s.metrics
}

func pct(used, total uint64) float32 {
	if total == 0 {
		return 0
	}
	return float32(float64(used) * 100 / float64(total))
}

// nodeMsg builds the Node message. protos, todayBytes and enabled (see statusOf) are looked up by the caller once per
// request.
func capabilityPresent(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}

func (f *Fleet) nodeMsg(ctx context.Context, n store.NodeRow, protos []string, todayBytes uint64, enabled map[string]string, now time.Time) *adminv1.Node {
	s := f.session(n.ID)
	st := f.statusOf(ctx, n, s, enabled, now)
	out := &adminv1.Node{
		Id: n.ID, Name: n.Name, CountryCode: n.CountryCode, Location: n.Location, Provider: n.Provider, Address: n.Address,
		Status: st.status, Reason: st.reason, Protocols: protos, TrafficTodayBytes: todayBytes, AgentVersion: n.AgentVersion,
		LastSeenUnix: fleetUnix(n.LastSeenAt), AwgBackend: n.AwgBackend, TorrentBlockerEnabled: n.TorrentBlockerEnabled,
		BandwidthMbps: uint32(n.BandwidthMbps),
	}
	caps := n.AgentCaps
	if s != nil {
		caps = s.caps
	}
	out.TorrentBlockerSupported = capabilityPresent(caps, capTorrentGuard)
	out.AwgPrepare = awgPrepareMsg(n, caps, now)
	if s != nil {
		out.Online = protocolCounts(s.onlineByProtocol())
		s.liveMu.Lock()
		out.LastSeenUnix = s.lastSeen.Unix()
		s.liveMu.Unlock()
		if m := s.currentMetrics(); m != nil {
			out.HasMetrics, out.CpuPct, out.RamPct, out.UptimeS = true, m.CpuPct, pct(m.RamUsedBytes, m.RamTotalBytes), m.UptimeS
		}
	}
	out.Warp = f.warpSummary(ctx, n.ID, s != nil, now)
	return out
}

func fleetUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// todayBytesByNode sums up+down per node since 00:00 UTC.
func (f *Fleet) todayBytesByNode(ctx context.Context, now time.Time) (map[string]uint64, error) {
	rows, err := f.st.FleetHours(ctx, todayStart(now))
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, r := range rows {
		out[r.NodeID] += r.Up + r.Down
	}
	return out, nil
}

// --- RPCs ---

func (s nodeService) ListNodes(ctx context.Context, req *connect.Request[adminv1.ListNodesRequest]) (*connect.Response[adminv1.ListNodesResponse], error) {
	f := s.f
	now := f.now().UTC()
	nodes, err := f.st.Nodes(ctx, req.Msg.IncludeRetired)
	if err != nil {
		return nil, internalErr(f.log.Error, "list nodes", err)
	}
	protos, err := f.st.FleetNodeProtocols(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "node protocols", err)
	}
	today, err := f.todayBytesByNode(ctx, now)
	if err != nil {
		return nil, internalErr(f.log.Error, "traffic today", err)
	}
	enabled, err := f.st.FleetEnabledInbounds(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "node inbounds", err)
	}
	resp := &adminv1.ListNodesResponse{ExpectedAgentVersion: f.cfg.ExpectedAgentVersion}
	for _, n := range nodes {
		resp.Nodes = append(resp.Nodes, f.nodeMsg(ctx, n, protos[n.ID], today[n.ID], inboundsOf(enabled, n.ID), now))
	}
	return connect.NewResponse(resp), nil
}

func (s nodeService) GetNode(ctx context.Context, req *connect.Request[adminv1.GetNodeRequest]) (*connect.Response[adminv1.GetNodeResponse], error) {
	f := s.f
	now := f.now().UTC()
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "get node", err)
	}
	inbounds, err := f.st.FleetInbounds(ctx, n.ID, false)
	if err != nil {
		return nil, internalErr(f.log.Error, "node inbounds", err)
	}
	var protos []string
	seen := map[string]bool{}
	enabled := map[string]string{}
	for _, in := range inbounds {
		if in.Enabled {
			enabled[in.ID] = in.ProfileName
		}
		if in.Enabled && !seen[in.Protocol] {
			seen[in.Protocol] = true
			protos = append(protos, in.Protocol)
		}
	}
	sort.Strings(protos)
	today, err := f.todayBytesByNode(ctx, now)
	if err != nil {
		return nil, internalErr(f.log.Error, "traffic today", err)
	}
	facts, err := f.st.NodeFacts(ctx, n.ID)
	if err != nil {
		return nil, internalErr(f.log.Error, "node facts", err)
	}
	resp := &adminv1.GetNodeResponse{
		Node: f.nodeMsg(ctx, n, protos, today[n.ID], enabled, now), Notes: n.Notes, DnsResolvers: n.DNSResolvers,
		Timeouts:    &adminv1.NodeTimeouts{LivenessTimeoutS: uint32(n.LivenessTimeoutS), ApplyTimeoutS: uint32(n.ApplyTimeoutS), DialTimeoutS: uint32(n.DialTimeoutS)},
		CreatedUnix: n.CreatedAt.Unix(),
	}
	if n.State == "pending" {
		if exp, err := f.st.PendingEnrollmentExpiry(ctx, n.ID, now); err == nil {
			resp.EnrollmentExpiresUnix = fleetUnix(exp)
		}
	}
	if facts.Hostname != "" || facts.OS != "" || len(facts.Engines) > 0 {
		nf := &adminv1.NodeFacts{Hostname: facts.Hostname, Os: facts.OS, Kernel: facts.Kernel, Arch: facts.Arch, CpuCount: facts.CPUCount,
			RamTotalBytes: facts.RAMTotal, DiskTotalBytes: facts.DiskTotal, Virt: facts.Virt, HasIpv6: facts.HasIPv6, BootUnix: fleetUnix(n.BootAt)}
		for _, e := range facts.Engines {
			nf.Engines = append(nf.Engines, e.Protocol+": "+e.Version)
		}
		resp.Facts = nf
	}
	for _, row := range inbounds {
		resp.Inbounds = append(resp.Inbounds, f.inboundMsg(n, row))
	}
	if s := f.session(n.ID); s != nil {
		if m := s.currentMetrics(); m != nil {
			resp.Metrics = &adminv1.NodeMetrics{CpuPct: m.CpuPct, SoftirqPct: m.SoftirqPct, Load1: m.Load1, RamUsedBytes: m.RamUsedBytes,
				RamTotalBytes: m.RamTotalBytes, DiskUsedBytes: m.DiskUsedBytes, DiskTotalBytes: m.DiskTotalBytes,
				NetRxBps: m.NetRxBps, NetTxBps: m.NetTxBps, AtUnix: now.Unix()}
		}
		resp.OnlineUsers, err = f.onlineUsers(ctx, s)
		if err != nil {
			return nil, internalErr(f.log.Error, "online users", err)
		}
	}
	top, err := f.st.FleetTopUsers(ctx, n.ID, todayStart(now), 10)
	if err != nil {
		return nil, internalErr(f.log.Error, "top users", err)
	}
	for _, t := range top {
		resp.TopToday = append(resp.TopToday, &adminv1.TopUser{UserId: t.UserID, UserName: t.UserName, Bytes: t.Bytes})
	}
	return connect.NewResponse(resp), nil
}

func (f *Fleet) onlineUsers(ctx context.Context, s *session) ([]*adminv1.OnlineUser, error) {
	s.liveMu.Lock()
	online := append([]onlineSess(nil), s.online...)
	down := make(map[string]uint64, len(s.userDown))
	for k, v := range s.userDown {
		down[k] = v
	}
	s.liveMu.Unlock()
	var uids, dids []string
	for _, o := range online {
		uids, dids = append(uids, o.userID), append(dids, o.deviceID)
	}
	names, err := f.st.FleetUserNames(ctx, uids)
	if err != nil {
		return nil, err
	}
	models, err := f.st.FleetDeviceModels(ctx, dids)
	if err != nil {
		return nil, err
	}
	sort.Slice(online, func(i, j int) bool { return online[i].since.Before(online[j].since) })
	out := make([]*adminv1.OnlineUser, 0, len(online))
	for _, o := range online {
		out = append(out, &adminv1.OnlineUser{UserId: o.userID, UserName: names[o.userID], Protocol: o.protocol,
			DeviceModel: models[o.deviceID], ConnectedAtUnix: o.since.Unix(), DownBps: down[o.userID]})
	}
	return out, nil
}

func inboundStateMsg(s string) adminv1.InboundState {
	switch s {
	case "active":
		return adminv1.InboundState_INBOUND_STATE_ACTIVE
	case "failed":
		return adminv1.InboundState_INBOUND_STATE_FAILED
	case "disabled":
		return adminv1.InboundState_INBOUND_STATE_DISABLED
	}
	return adminv1.InboundState_INBOUND_STATE_PENDING
}

// inboundMsg builds the admin Inbound; port and TLS name come from the plugin's own spec when it builds.
func (f *Fleet) inboundMsg(n store.NodeRow, row store.FleetInboundRow) *adminv1.Inbound {
	out := &adminv1.Inbound{
		Id: row.ID, ProfileId: row.ProfileID, ProfileName: row.ProfileName, Protocol: row.Protocol, NodeId: n.ID, NodeName: n.Name,
		Port: uint32(row.PortOverride), TlsServerName: row.TLSNameOverride, State: inboundStateMsg(row.State), LastError: row.LastError,
		CertPinSha256: row.CertPin, CertNotAfterUnix: fleetUnix(row.CertNotAfter), Awg: awgStatusMsg(row),
	}
	if !row.Enabled {
		out.State = adminv1.InboundState_INBOUND_STATE_DISABLED
	}
	if spec, err := f.buildSpec(n, row); err == nil {
		out.Port, out.TlsServerName, out.Egress = uint32(spec.Listen.Port), spec.TLS.ServerName, spec.Egress
	}
	return out
}

func randomToken() string {
	var b [32]byte
	rand.Read(b[:]) // never fails on supported platforms
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

func (s nodeService) CreateEnrollment(ctx context.Context, req *connect.Request[adminv1.CreateEnrollmentRequest]) (*connect.Response[adminv1.CreateEnrollmentResponse], error) {
	f := s.f
	m := req.Msg
	if f.cfg.PanelAddr == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("panel address is not configured"))
	}
	ttl := time.Duration(m.TtlSeconds) * time.Second
	switch {
	case m.TtlSeconds == 0:
		ttl = time.Hour
	case m.TtlSeconds > 86400:
		return nil, invalid("ttl_seconds must be at most 86400")
	}
	now := f.now().UTC()
	var newNode *store.NodeRow
	if m.NodeId == "" {
		name := strings.ToLower(m.Name)
		cc := strings.ToUpper(m.CountryCode)
		switch {
		case !nodeNameRe.MatchString(name):
			return nil, invalid("name must match [a-z0-9-]{2,24}")
		case !validAddress(m.Address):
			return nil, invalid("address must be a host name or an IP")
		case cc != "" && (len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z'):
			return nil, invalid("country_code must be two letters")
		case !validText(m.Location, 100) || !validText(m.Provider, 100):
			return nil, invalid("location and provider must be short plain text")
		}
		newNode = &store.NodeRow{ID: store.NewID("nod_"), Name: name, Address: m.Address, CountryCode: cc, Location: m.Location, Provider: m.Provider}
	}
	token := randomToken()
	n, err := f.st.CreateEnrollment(ctx, newNode, m.NodeId, hashToken(token), f.cfg.Actor(ctx), now, now.Add(ttl))
	switch {
	case errors.Is(err, store.ErrConflict):
		return nil, errNameTaken
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case errors.Is(err, store.ErrNodeRetired):
		return nil, errNodeRetired
	case err != nil:
		return nil, internalErr(f.log.Error, "create enrollment", err)
	}
	f.audit(ctx, "node.create_enrollment", map[string]string{"node_id": n.ID, "name": n.Name}) // never the token
	return connect.NewResponse(&adminv1.CreateEnrollmentResponse{
		Node:           f.nodeMsg(ctx, n, nil, 0, nil, now),
		InstallCommand: installCommand(f.cfg.PanelAddr, f.cfg.AgentSNI, f.ca.fingerprint, token),
		CaFingerprint:  "sha256:" + f.ca.fingerprint,
		ExpiresUnix:    now.Add(ttl).Unix(),
		CopyCommand:    f.copyCommand(n.Address),
	}), nil
}

// nodeBinaryPath is where the admin puts the agent binary on the server: the install command runs it from there, and
// `install` copies it to /usr/local/bin itself, so nothing depends on PATH or on the file being executable after scp.
const nodeBinaryPath = "/root/mistgate-node"

// installCommand is the one line to paste on the server as root.
func installCommand(panelAddr, sni, caFingerprint, token string) string {
	return fmt.Sprintf("chmod +x %[1]s && %[1]s enroll --panel %[2]s --sni %[3]s --ca-sha256 %[4]s --token %[5]s && %[1]s install",
		nodeBinaryPath, panelAddr, sni, caFingerprint, token)
}

// copyCommand puts the linux/amd64 agent of the trusted update bundle on the server, run on the panel's own server;
// "" when the panel has no trusted bundle (the admin brings the binary of the panel's release himself).
func (f *Fleet) copyCommand(address string) string {
	u := f.updates()
	if u == nil {
		return ""
	}
	bin := u.NodeBinary("linux", "amd64")
	if bin == "" {
		return ""
	}
	host := address
	if strings.Contains(host, ":") { // an IPv6 address: scp wants it in brackets
		host = "[" + host + "]"
	}
	return fmt.Sprintf("scp %s root@%s:%s", bin, host, nodeBinaryPath)
}

func (s nodeService) UpdateNode(ctx context.Context, req *connect.Request[adminv1.UpdateNodeRequest]) (*connect.Response[adminv1.UpdateNodeResponse], error) {
	f := s.f
	m := req.Msg
	p := store.NodePatch{Address: m.Address, CountryCode: m.CountryCode, Location: m.Location, Provider: m.Provider, Notes: m.Notes}
	if m.Name != nil {
		name := strings.ToLower(*m.Name)
		if !nodeNameRe.MatchString(name) {
			return nil, invalid("name must match [a-z0-9-]{2,24}")
		}
		p.Name = &name
	}
	if m.Address != nil && !validAddress(*m.Address) {
		return nil, invalid("address must be a host name or an IP")
	}
	if m.CountryCode != nil {
		cc := strings.ToUpper(*m.CountryCode)
		if cc != "" && (len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z') {
			return nil, invalid("country_code must be two letters")
		}
		p.CountryCode = &cc
	}
	for _, v := range []*string{m.Location, m.Provider} {
		if v != nil && !validText(*v, 100) {
			return nil, invalid("location and provider must be short plain text")
		}
	}
	if m.Notes != nil && !validText(strings.NewReplacer("\n", "", "\r", "", "\t", "").Replace(*m.Notes), 2000) {
		return nil, invalid("notes must be plain text up to 2000 bytes")
	}
	if m.DnsResolvers != nil {
		vals := m.DnsResolvers.Values
		if len(vals) > 8 {
			return nil, invalid("at most 8 resolvers")
		}
		for _, r := range vals {
			if !validResolver(r) {
				return nil, invalid("a resolver must be an IP or ip:port")
			}
		}
		if vals == nil {
			vals = []string{}
		}
		p.DNSResolvers = &vals
	}
	if t := m.Timeouts; t != nil {
		or := func(v uint32, def int) int {
			if v == 0 {
				return def
			}
			return int(v)
		}
		l, a, d := or(t.LivenessTimeoutS, 90), or(t.ApplyTimeoutS, 120), or(t.DialTimeoutS, 15)
		if l < 15 || l > 3600 || a < 10 || a > 3600 || d < 5 || d > 120 {
			return nil, invalid("timeouts out of range (liveness 15-3600, apply 10-3600, dial 5-120 s)")
		}
		p.LivenessTimeoutS, p.ApplyTimeoutS, p.DialTimeoutS = &l, &a, &d
	}
	if m.AwgBackend != nil { // a node with awg inbounds needs an agent that follows the setting
		cur, err := f.st.Node(ctx, m.NodeId)
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		if err != nil {
			return nil, internalErr(f.log.Error, "update node", err)
		}
		if err := f.checkAwgBackend(ctx, cur, *m.AwgBackend); err != nil {
			return nil, err
		}
		p.AwgBackend = m.AwgBackend
	}
	if m.TorrentBlockerEnabled != nil {
		if *m.TorrentBlockerEnabled {
			cur, err := f.st.Node(ctx, m.NodeId)
			if errors.Is(err, store.ErrNotFound) {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
			}
			if err != nil {
				return nil, internalErr(f.log.Error, "update node", err)
			}
			caps := cur.AgentCaps
			if sess := f.session(cur.ID); sess != nil {
				caps = sess.caps
			}
			if !capabilityPresent(caps, capTorrentGuard) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
			}
		}
		p.TorrentBlockerEnabled = m.TorrentBlockerEnabled
	}
	if m.BandwidthMbps != nil {
		if *m.BandwidthMbps > 1_000_000 {
			return nil, invalid("bandwidth_mbps must be between 0 and 1000000")
		}
		bandwidth := int(*m.BandwidthMbps)
		p.BandwidthMbps = &bandwidth
	}
	oldAddress := ""
	if m.Address != nil {
		if cur, err := f.st.Node(ctx, m.NodeId); err == nil { // a missing node is answered by the update
			oldAddress = cur.Address
		}
	}
	n, err := f.st.UpdateNode(ctx, m.NodeId, p)
	switch {
	case errors.Is(err, store.ErrConflict):
		return nil, errNameTaken
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case err != nil:
		return nil, internalErr(f.log.Error, "update node", err)
	}
	if m.Address != nil && n.Address != oldAddress {
		if err := f.staleKeysOn(ctx, n.ID); err != nil {
			return nil, internalErr(f.log.Error, "mark devices stale", err)
		}
	}
	f.audit(ctx, "node.update", map[string]string{"node_id": n.ID})
	f.StateChanged() // address, DNS, timeouts and node settings feed the desired state
	now := f.now().UTC()
	protos, _ := f.st.FleetNodeProtocols(ctx)
	today, _ := f.todayBytesByNode(ctx, now)
	enabled, _ := f.st.FleetEnabledInbounds(ctx)
	return connect.NewResponse(&adminv1.UpdateNodeResponse{Node: f.nodeMsg(ctx, n, protos[n.ID], today[n.ID], inboundsOf(enabled, n.ID), now)}), nil
}

// staleKeysOn marks the issued configs of every per-device (AmneziaWG) profile on the node stale: each one holds the
// node's address as its endpoint. A port change of an inbound does the same (access.Service.UpdateInbound).
func (f *Fleet) staleKeysOn(ctx context.Context, nodeID string) error {
	ins, err := f.st.FleetInbounds(ctx, nodeID, false)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, in := range ins {
		if p, ok := f.reg.Get(in.Protocol); ok && protocols.IsPerDevice(p) && !done[in.ProfileID] {
			done[in.ProfileID] = true
			if err := f.st.Access().BumpProfileEpoch(ctx, in.ProfileID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s nodeService) RestartInbounds(ctx context.Context, req *connect.Request[adminv1.RestartInboundsRequest]) (*connect.Response[adminv1.RestartInboundsResponse], error) {
	f := s.f
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "restart inbounds", err)
	}
	if id := req.Msg.InboundId; id != "" {
		rows, err := f.st.FleetInbounds(ctx, n.ID, false)
		if err != nil {
			return nil, internalErr(f.log.Error, "restart inbounds", err)
		}
		found := false
		for _, r := range rows {
			found = found || r.ID == id
		}
		if !found {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("inbound not found on this node"))
		}
	}
	sess := f.session(n.ID)
	if sess == nil {
		return nil, errNodeOffline
	}
	res, err := sess.roundtrip(ctx, time.Duration(n.ApplyTimeoutS)*f.unit, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RestartInbound{
			RestartInbound: &agentv1.RestartInbound{RequestId: reqID, InboundId: req.Msg.InboundId}}}
	})
	if err != nil {
		return nil, err
	}
	if !res.Ok {
		f.log.Warn("agent failed to restart inbounds", "node", n.ID, "error", res.Error)
		return nil, connect.NewError(connect.CodeInternal, errors.New("the agent could not restart: "+clip(res.Error, 200)))
	}
	f.audit(ctx, "node.restart_inbounds", map[string]string{"node_id": n.ID, "inbound_id": req.Msg.InboundId})
	// the node's own engine_restarted rows say that it happened; this one says who asked
	row := store.EventRow{Time: f.now(), Severity: 1, Code: "profiles_restarted", Source: "admin", NodeID: n.ID, InboundID: req.Msg.InboundId,
		Params: map[string]string{"actor": auth.ActorLabel(ctx), "count": fmt.Sprint(res.Affected)}}
	if err := f.st.InsertEvent(ctx, row); err != nil {
		f.log.Warn("restart event", "node", n.ID, "err", err)
	}
	return connect.NewResponse(&adminv1.RestartInboundsResponse{Restarted: res.Affected}), nil
}

func (s nodeService) RetireNode(ctx context.Context, req *connect.Request[adminv1.RetireNodeRequest]) (*connect.Response[adminv1.RetireNodeResponse], error) {
	f := s.f
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "retire node", err)
	}
	if req.Msg.ConfirmName != n.Name {
		return nil, invalid("confirm_name does not match the node name")
	}
	switch err := f.st.RetireNode(ctx, n.ID, f.now().UTC()); {
	case errors.Is(err, store.ErrNodeRetired):
		return nil, errNodeRetired
	case err != nil:
		return nil, internalErr(f.log.Error, "retire node", err)
	}
	notified := false
	if sess := f.session(n.ID); sess != nil {
		notified = sess.enqueue(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Retire{
			Retire: &agentv1.Retire{RequestId: store.NewID("req_")}}})
		if notified {
			// The agent exits on Retire and closes the stream; give it a moment, then cut the stream.
			select {
			case <-sess.done:
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
			}
		}
		sess.cancel(connect.NewError(connect.CodeCanceled, errors.New("node retired")))
	}
	f.event(ctx, 1, "node_retired", n.ID, map[string]string{"actor": f.cfg.Actor(ctx)})
	f.audit(ctx, "node.retire", map[string]string{"node_id": n.ID, "name": n.Name})
	return connect.NewResponse(&adminv1.RetireNodeResponse{AgentNotified: notified}), nil
}

func (s nodeService) StreamLogs(ctx context.Context, req *connect.Request[adminv1.StreamLogsRequest], stream *connect.ServerStream[adminv1.StreamLogsResponse]) error {
	f := s.f
	m := req.Msg
	n, err := f.st.Node(ctx, m.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return internalErr(f.log.Error, "stream logs", err)
	}
	sess := f.session(n.ID)
	if sess == nil {
		return errNodeOffline
	}
	tail := m.TailLines
	if tail == 0 {
		tail = 200
	}
	tail = min(tail, 1000)
	const followMax = 600
	wait := time.Duration(n.ApplyTimeoutS) * f.unit
	if m.Follow {
		wait = (followMax + 15) * time.Second
	}

	reqID := store.NewID("req_")
	sub := &logSub{ch: make(chan *agentv1.LogChunk, 64)}
	sess.liveMu.Lock()
	sess.logs[reqID] = sub
	sess.liveMu.Unlock()
	defer func() {
		sess.liveMu.Lock()
		delete(sess.logs, reqID)
		sess.liveMu.Unlock()
	}()
	if !sess.enqueue(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{
		RequestId: reqID, Sources: m.Sources, TailLines: tail, Follow: m.Follow, FollowMaxSeconds: followMax,
		MinLevel: agentv1.Severity(m.MinLevel)}}}) {
		return errLinkLost
	}
	cancelReq := func() {
		select {
		case <-sess.done:
		default:
			sess.enqueue(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogCancel{LogCancel: &agentv1.LogCancel{RequestId: reqID}}})
		}
	}
	end := func(msg string) error {
		return stream.Send(&adminv1.StreamLogsResponse{Eof: true, Dropped: sub.dropped.Swap(0), Error: msg})
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case c := <-sub.ch:
			resp := &adminv1.StreamLogsResponse{Eof: c.Eof, Dropped: c.Dropped + sub.dropped.Swap(0), Error: c.Error}
			for _, l := range c.Lines {
				resp.Lines = append(resp.Lines, &adminv1.LogLine{TimeUnixMs: l.TimeUnixMs, Level: adminv1.LogLevel(l.Level), Source: l.Source, Message: l.Message})
			}
			if err := stream.Send(resp); err != nil {
				cancelReq()
				return err
			}
			if c.Eof {
				return nil
			}
		case <-sess.done:
			return end("the node link dropped")
		case <-ctx.Done():
			cancelReq()
			return nil
		case <-t.C:
			cancelReq()
			return end("the node did not answer in time")
		}
	}
}
