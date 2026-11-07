//go:build !js

package fleet

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/gen/mistgate/probe/v1/probev1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/tcpcheck"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

// On-demand probes between fleet nodes, for the MCP tools path_probe, node_trace and node_dns_check (agent.proto "PATH PROBE").
// Every target is a node (or one of its inbounds' ports) resolved here by id: a caller never names a host or a URL. The agent
// is sent the target's address, which stays inside the panel: results, audit rows and errors carry node names only.
//
// The code is not in the Cloudflare edition (path_probe_js.go): the Workers' size has no room for it, and nothing there calls it.

const (
	// A caller may start probeCallerMax probes in probeCallerWindow, and any one node may be an end of probeNodeMax in
	// probeNodeWindow. One probe runs per node pair at a time.
	probeCallerMax    = 12
	probeCallerWindow = 10 * time.Minute
	probeNodeMax      = 6
	probeNodeWindow   = 5 * time.Minute

	// The MTU sweep: at most probeMaxSizes sizes, probeSweepCount datagrams of each at probeSweepPPS, to one port.
	probeMaxSizes   = 5
	probeSweepCount = 10
	probeSweepPPS   = 10

	traceWait   = 70 * time.Second // the agent's walk is bounded by 60 s
	dnsRunWait  = 40 * time.Second
	traceMaxTTL = 30
	dnsTrendMin = 4 // runs needed to say anything about a trend
)

// probeService is the ProbeService (proto/mistgate/probe/v1). It is its own package and not part of NodeService on purpose: the
// admin Worker has no room for its messages (scripts/check-edge-sizes.sh), and this file is not built for js/wasm.
type probeService struct{ f *Fleet }

// ProbeHandler is the admin ProbeService; see NodeHandler for opts. Only the panel VPS mounts it.
func (f *Fleet) ProbeHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return probev1connect.NewProbeServiceHandler(probeService{f}, adminOpts(opts)...)
}

// startProbe admits a probe between two nodes (a == b for a single-node probe): it applies the limits and takes the pair. The
// returned function gives the pair back. An admitted probe counts against the limits even when it fails.
func (f *Fleet) startProbe(ctx context.Context, a, b string) (func(), error) {
	actor := f.cfg.Actor(ctx)
	now := f.now()
	pair := a + "|" + b
	if b < a {
		pair = b + "|" + a
	}
	p := &f.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy == nil {
		p.busy, p.caller, p.node = map[string]struct{}{}, map[string][]time.Time{}, map[string][]time.Time{}
	}
	recent := func(times []time.Time, window time.Duration) []time.Time {
		return slices.DeleteFunc(slices.Clone(times), func(t time.Time) bool { return now.Sub(t) >= window })
	}
	retry := func(times []time.Time, window time.Duration) error {
		wait := window - now.Sub(times[0])
		return connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("probe rate limit reached: try again in %d s", int(wait.Seconds())+1))
	}
	callerTimes := recent(p.caller[actor], probeCallerWindow)
	if len(callerTimes) >= probeCallerMax {
		return nil, retry(callerTimes, probeCallerWindow)
	}
	ends := []string{a}
	if b != a {
		ends = append(ends, b)
	}
	nodeTimes := make(map[string][]time.Time, 2)
	for _, id := range ends {
		nodeTimes[id] = recent(p.node[id], probeNodeWindow)
		if len(nodeTimes[id]) >= probeNodeMax {
			return nil, retry(nodeTimes[id], probeNodeWindow)
		}
	}
	if _, running := p.busy[pair]; running {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("a probe between these nodes is already running"))
	}
	if p.active >= maxActiveProbes {
		return nil, activeProbeLimitError()
	}
	p.caller[actor] = append(callerTimes, now)
	for _, id := range ends {
		p.node[id] = append(nodeTimes[id], now)
	}
	p.busy[pair] = struct{}{}
	p.active++
	return func() {
		p.mu.Lock()
		delete(p.busy, pair)
		p.active--
		p.mu.Unlock()
	}, nil
}

func activeProbeLimitError() error {
	return connect.NewError(connect.CodeResourceExhausted, errors.New("fleet probe limit reached; try again later"))
}

// probeNode reads a node a probe may name: not retired.
func (f *Fleet) probeNode(ctx context.Context, id string) (store.NodeRow, error) {
	n, err := f.st.Node(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && n.State == "retired" {
		return store.NodeRow{}, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return store.NodeRow{}, internalErr(f.log.Error, "read node for probe", err)
	}
	return n, nil
}

func (f *Fleet) probePair(ctx context.Context, fromID, toID string) (from, to store.NodeRow, err error) {
	if fromID == "" || toID == "" || fromID == toID {
		return from, to, connect.NewError(connect.CodeInvalidArgument, errors.New("from and to must be two different nodes"))
	}
	if from, err = f.probeNode(ctx, fromID); err != nil {
		return from, to, err
	}
	to, err = f.probeNode(ctx, toID)
	return from, to, err
}

// probeLive says whether a node's agent is connected here and lists a capability.
func (f *Fleet) probeLive(live map[string]store.NodeLiveRow, id string, caps ...string) (store.NodeLiveRow, string) {
	row := live[id]
	if !row.Connected || (f.cfg.Remote == nil && f.session(id) == nil) {
		return row, "node_offline"
	}
	for _, c := range caps {
		if !slices.Contains(row.AgentCaps, c) {
			return row, "agent_too_old"
		}
	}
	return row, ""
}

// ---------------------------------------------------------------------------------------------------------------------
// path_probe

type probePortJSON struct {
	Port    uint16 `json:"port"`
	Sent    uint32 `json:"sent,omitempty"`
	Got     uint32 `json:"got,omitempty"`
	Verdict string `json:"verdict"`
	MS      uint32 `json:"ms,omitempty"`
}

type probeSizeJSON struct {
	Size uint32 `json:"size"`
	Sent uint32 `json:"sent"`
	Got  uint32 `json:"got"`
}

// pathProbeResult is what a probe produced; it is also the stored JSON.
type pathProbeResult struct {
	Error   string          `json:"error,omitempty"`
	Ports   []probePortJSON `json:"ports,omitempty"`
	Sizes   []probeSizeJSON `json:"sizes,omitempty"`
	Largest uint32          `json:"largest,omitempty"`
}

// PathProbe runs one probe from a node to another and keeps its result for 7 days.
func (s probeService) PathProbe(ctx context.Context, req *connect.Request[probev1.PathProbeRequest]) (*connect.Response[probev1.PathProbeResponse], error) {
	f, m := s.f, req.Msg
	switch m.Kind {
	case "udp", "tcp", "tls":
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kind must be udp, tcp or tls"))
	}
	ports, sizes, err := probeArgs(m)
	if err != nil {
		return nil, err
	}
	from, to, err := f.probePair(ctx, m.FromNodeId, m.ToNodeId)
	if err != nil {
		return nil, err
	}
	release, err := f.startProbe(ctx, from.ID, to.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	res := f.runPathProbe(ctx, from, to, m.Kind, ports, sizes)
	at := f.now().UTC()
	if res.Error == "" {
		if b, err := json.Marshal(res); err == nil {
			if err := f.st.AddPathProbe(ctx, from.ID, to.ID, m.Kind, at, string(b)); err != nil {
				f.log.Warn("store path probe", "code", "store_failed", "err", err)
			}
		}
	}
	f.audit(context.WithoutCancel(ctx), "node.path_probe", map[string]string{
		"from": from.Name, "to": to.Name, "kind": m.Kind, "sizes": strconv.Itoa(len(sizes)), "error": res.Error,
	})
	return connect.NewResponse(pathProbeMessage(from.Name, to.Name, m.Kind, at, res)), nil
}

// probeArgs validates the optional ports and sizes of a request.
func probeArgs(m *probev1.PathProbeRequest) (ports []uint16, sizes []uint32, err error) {
	bad := func(msg string) error { return connect.NewError(connect.CodeInvalidArgument, errors.New(msg)) }
	if len(m.Ports) > udpcheck.MaxPorts {
		return nil, nil, bad("at most 8 ports")
	}
	for _, p := range m.Ports {
		if p == 0 || p > 65535 {
			return nil, nil, bad("a port is 1 to 65535")
		}
		if !slices.Contains(ports, uint16(p)) {
			ports = append(ports, uint16(p))
		}
	}
	if len(m.Sizes) > 0 && m.Kind != "udp" {
		return nil, nil, bad("sizes (the MTU sweep) is for udp only")
	}
	if len(m.Sizes) > probeMaxSizes {
		return nil, nil, bad("at most 5 sizes")
	}
	for _, sz := range m.Sizes {
		if sz < udpcheck.MinSize || sz > udpcheck.MaxSizeDF {
			return nil, nil, bad(fmt.Sprintf("a size is %d to %d", udpcheck.MinSize, udpcheck.MaxSizeDF))
		}
		if !slices.Contains(sizes, sz) {
			sizes = append(sizes, sz)
		}
	}
	return ports, sizes, nil
}

func (f *Fleet) runPathProbe(ctx context.Context, from, to store.NodeRow, kind string, requested []uint16, sizes []uint32) pathProbeResult {
	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for path probe", "code", "store_failed", "err", err)
		return pathProbeResult{Error: "failed"}
	}
	live := liveRowsByID(liveRows)
	caps := []string{capProbe, capTCPCheck}
	if kind == "udp" {
		caps = []string{capProbe, capUDPCheck}
	}
	fromLive, code := f.probeLive(live, from.ID, caps...)
	if code != "" {
		return pathProbeResult{Error: code}
	}
	if kind == "udp" {
		toLive, code := f.probeLive(live, to.ID, capUDPCheck)
		if code != "" {
			return pathProbeResult{Error: code}
		}
		ports, errCode := f.probeUDPPorts(ctx, to, requested)
		if errCode != "" {
			return pathProbeResult{Error: errCode}
		}
		if len(sizes) > 0 {
			return f.probeSweep(ctx, from, fromLive, to, toLive, ports[0], sizes)
		}
		return f.pathProbeUDP(ctx, from, fromLive, to, toLive, ports)
	}
	targets, errCode := f.probeTCPPorts(ctx, to, kind, requested)
	if errCode != "" {
		return pathProbeResult{Error: errCode}
	}
	return f.pathProbeTCP(ctx, from, fromLive, to, kind, targets)
}

// probeUDPPorts is the target's UDP ports a probe may use: its enabled UDP inbounds and 443, like the port check. requested
// narrows them; a port the target does not serve is refused.
func (f *Fleet) probeUDPPorts(ctx context.Context, to store.NodeRow, requested []uint16) ([]uint16, string) {
	inbounds, err := f.st.FleetInbounds(ctx, to.ID, false)
	if err != nil {
		f.log.Warn("list inbounds for path probe", "code", "store_failed", "err", err)
		return nil, "failed"
	}
	own := f.udpCheckPorts(to, nil, inbounds)
	if len(requested) == 0 {
		return own, ""
	}
	for _, p := range requested {
		if !slices.Contains(own, p) {
			return nil, "bad_port"
		}
	}
	return requested, ""
}

// probeTCPPorts is the target's TCP ports a probe may use, with how to probe each: tls needs a TLS listener (its SNI and
// the certificate pin the node reported), tcp takes every TCP port the node serves, the ACME port included.
func (f *Fleet) probeTCPPorts(ctx context.Context, to store.NodeRow, kind string, requested []uint16) ([]tcpTarget, string) {
	specs, pins, err := f.tcpSpecsOf(ctx, to)
	if err != nil {
		f.log.Warn("list inbounds for path probe", "code", "store_failed", "err", err)
		return nil, "failed"
	}
	var own []tcpTarget
	for _, t := range tcpTargets(specs, pins) {
		if kind == "tls" && t.Kind != tcpcheck.KindTLS {
			continue
		}
		own = append(own, t)
	}
	if len(requested) == 0 {
		if len(own) == 0 {
			return nil, "no_ports"
		}
		return own, ""
	}
	var out []tcpTarget
	for _, p := range requested {
		i := slices.IndexFunc(own, func(t tcpTarget) bool { return t.Port == p })
		if i < 0 {
			return nil, "bad_port"
		}
		out = append(out, own[i])
	}
	return out, ""
}

// udpRound is what one arm, send and stop produced.
type udpRound struct {
	sent    int
	packets map[uint16]uint64
	bytes   map[uint16]uint64
	family  string
}

// got is the datagrams of the given size that arrived on a port.
func (r udpRound) got(port uint16, size int) uint32 {
	header := uint64(28)
	if r.family == "6" {
		header = 48
	}
	return uint32(udpDelivered(r.packets[port], r.bytes[port], header, uint64(size), uint64(r.sent)))
}

// probeUDPRound is one delivery run: the target arms a counter on ports, the sender sends count datagrams of size to each at
// pps (don't-fragment with df), the target stops and reports. The counter is always stopped. The code is a short error word.
func (f *Fleet) probeUDPRound(ctx context.Context, from store.NodeRow, fromLive store.NodeLiveRow, to store.NodeRow, toLive store.NodeLiveRow,
	ports []uint16, count, pps, size int, df bool) (udpRound, string) {
	var tag [8]byte
	if _, err := rand.Read(tag[:]); err != nil {
		return udpRound{}, "failed"
	}
	armed, err := f.commandWithLive(ctx, to.ID, udpCheckWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpCount{UdpCount: &agentv1.UdpCount{
			RequestId: reqID, Tag: tag[:], Ports: uint32Ports(ports), HoldS: 30}}}
	}, &toLive)
	if err != nil {
		return udpRound{}, "failed"
	}
	if armed == nil || !armed.Ok {
		return udpRound{}, portCommandError(armed)
	}
	round := udpRound{}
	sendCode := ""
	sent, err := f.commandWithLive(ctx, from.ID, udpCheckWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpSend{UdpSend: &agentv1.UdpSend{
			RequestId: reqID, Host: to.Address, Ports: uint32Ports(ports), Tag: tag[:],
			Count: uint32(count), Pps: uint32(pps), Size: uint32(size), Df: df}}}
	}, &fromLive)
	switch {
	case err != nil:
		sendCode = "failed"
	case sent == nil || !sent.Ok:
		sendCode = portCommandError(sent)
	default:
		var ok bool
		round.family = sent.Params["family"]
		round.sent, ok = udpSentParam(sent.Params["sent"])
		if !ok || round.sent > count || (round.family != "4" && round.family != "6") {
			sendCode = "failed"
		}
	}
	if sendCode == "" {
		if err := f.settlePortCheck(ctx); err != nil {
			sendCode = "failed"
		}
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), udpCheckStopWait)
	defer cancel()
	counts, stopErr := f.commandWithLive(stopCtx, to.ID, udpCheckStopWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpCount{UdpCount: &agentv1.UdpCount{
			RequestId: reqID, Tag: tag[:], Stop: true}}}
	}, &toLive)
	if sendCode != "" {
		return udpRound{}, sendCode
	}
	if stopErr != nil {
		return udpRound{}, "failed"
	}
	if counts == nil || !counts.Ok {
		return udpRound{}, portCommandError(counts)
	}
	round.packets, round.bytes = map[uint16]uint64{}, map[uint16]uint64{}
	for _, port := range ports {
		p, okP := udpCountParam(counts.Params, "p"+strconv.Itoa(int(port)))
		b, okB := udpCountParam(counts.Params, "b"+strconv.Itoa(int(port)))
		if !okP || !okB {
			return udpRound{}, "failed"
		}
		round.packets[port], round.bytes[port] = p, b
	}
	return round, ""
}

func (f *Fleet) pathProbeUDP(ctx context.Context, from store.NodeRow, fromLive store.NodeLiveRow, to store.NodeRow, toLive store.NodeLiveRow, ports []uint16) pathProbeResult {
	round, code := f.probeUDPRound(ctx, from, fromLive, to, toLive, ports, udpCheckSendCount, udpCheckPPS, udpCheckSize, false)
	if code != "" {
		return pathProbeResult{Error: code}
	}
	res := pathProbeResult{}
	for _, port := range ports {
		got := round.got(port, udpCheckSize)
		res.Ports = append(res.Ports, probePortJSON{Port: port, Sent: uint32(round.sent), Got: got,
			Verdict: udpPortVerdict(uint64(round.sent), uint64(got))})
	}
	return res
}

// probeSweep sends probeSweepCount don't-fragment datagrams of each size to one port. A size the sender's own interface cannot
// carry is a row with nothing sent (the path's MTU is below it); the largest size where at least half arrived is the answer.
func (f *Fleet) probeSweep(ctx context.Context, from store.NodeRow, fromLive store.NodeLiveRow, to store.NodeRow, toLive store.NodeLiveRow, port uint16, sizes []uint32) pathProbeResult {
	res := pathProbeResult{}
	for _, size := range sizes {
		round, code := f.probeUDPRound(ctx, from, fromLive, to, toLive, []uint16{port}, probeSweepCount, probeSweepPPS, int(size), true)
		switch code {
		case "":
			got := round.got(port, int(size))
			res.Sizes = append(res.Sizes, probeSizeJSON{Size: size, Sent: uint32(round.sent), Got: got})
			if round.sent > 0 && int(got)*2 >= round.sent && size > res.Largest {
				res.Largest = size
			}
		case "no_route": // the local interface refused the datagram: EMSGSIZE
			res.Sizes = append(res.Sizes, probeSizeJSON{Size: size})
		default:
			return pathProbeResult{Error: code}
		}
	}
	return res
}

func (f *Fleet) pathProbeTCP(ctx context.Context, from store.NodeRow, fromLive store.NodeLiveRow, to store.NodeRow, kind string, targets []tcpTarget) pathProbeResult {
	wire := make([]*agentv1.TcpTarget, len(targets))
	for i, t := range targets {
		wire[i] = &agentv1.TcpTarget{Port: uint32(t.Port), Kind: tcpcheck.KindConnect}
		if kind == "tls" && t.Kind == tcpcheck.KindTLS {
			wire[i] = &agentv1.TcpTarget{Port: uint32(t.Port), Kind: tcpcheck.KindTLS, Sni: t.SNI, PinSha256: t.Pin}
		}
	}
	result, err := f.commandWithLive(ctx, from.ID, tcpProbeWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_TcpProbe{TcpProbe: &agentv1.TcpProbe{
			RequestId: reqID, Host: to.Address, Targets: wire}}}
	}, &fromLive)
	if err != nil {
		return pathProbeResult{Error: "failed"}
	}
	if result == nil || !result.Ok {
		return pathProbeResult{Error: portCommandError(result)}
	}
	res := pathProbeResult{}
	for _, t := range targets {
		key := strconv.Itoa(int(t.Port))
		row := probePortJSON{Port: t.Port, Verdict: result.Params["t"+key]}
		if !validTCPVerdict(row.Verdict) {
			row.Verdict = "failed"
		}
		if ms, err := strconv.ParseUint(result.Params["m"+key], 10, 32); err == nil {
			row.MS = uint32(ms)
		}
		res.Ports = append(res.Ports, row)
	}
	return res
}

func pathProbeMessage(fromName, toName, kind string, at time.Time, r pathProbeResult) *probev1.PathProbeResponse {
	out := &probev1.PathProbeResponse{FromName: fromName, ToName: toName, Kind: kind, AtUnix: at.Unix(), Error: r.Error, LargestSize: r.Largest}
	for _, p := range r.Ports {
		out.Ports = append(out.Ports, &probev1.PathProbePort{Port: uint32(p.Port), Sent: p.Sent, Got: p.Got, Verdict: p.Verdict, Ms: p.MS})
	}
	for _, sz := range r.Sizes {
		out.Sizes = append(out.Sizes, &probev1.PathProbeSize{Size: sz.Size, Sent: sz.Sent, Got: sz.Got})
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------------------
// node_trace

type traceHopJSON struct {
	TTL  uint32 `json:"ttl"`
	Kind string `json:"kind"` // icmp | silent | open | refused
	RTT  uint32 `json:"rtt_ms,omitempty"`
}

type traceJSON struct {
	Reached bool           `json:"reached"`
	Hops    []traceHopJSON `json:"hops"`
}

func validTraceKind(k string) bool {
	return k == "icmp" || k == "silent" || k == "open" || k == "refused"
}

// NodeTrace walks a TCP SYN TTL sweep from one node to another and compares it with the pair's previous trace.
func (s probeService) NodeTrace(ctx context.Context, req *connect.Request[probev1.NodeTraceRequest]) (*connect.Response[probev1.NodeTraceResponse], error) {
	f := s.f
	from, to, err := f.probePair(ctx, req.Msg.FromNodeId, req.Msg.ToNodeId)
	if err != nil {
		return nil, err
	}
	release, err := f.startProbe(ctx, from.ID, to.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	at := f.now().UTC()
	out := &probev1.NodeTraceResponse{FromName: from.Name, ToName: to.Name, AtUnix: at.Unix()}
	cur, code := f.runTrace(ctx, from, to)
	out.Error = code
	if code == "" {
		prev, err := f.st.NodeTraces(ctx, from.ID, to.ID, 1)
		if err != nil {
			return nil, internalErr(f.log.Error, "read previous trace", err)
		}
		var previous *traceJSON
		if len(prev) > 0 {
			var p traceJSON
			if json.Unmarshal([]byte(prev[0].ResultJSON), &p) == nil {
				previous = &p
				out.PreviousUnix = prev[0].At.Unix()
			}
		}
		out.ChangedAtHop, out.Diff = traceDiff(previous, cur)
		out.Reached = cur.Reached
		for _, h := range cur.Hops {
			label := "hop " + strconv.Itoa(int(h.TTL))
			if h.Kind == "open" || h.Kind == "refused" {
				label = to.Name // the destination itself answered
			}
			out.Hops = append(out.Hops, &probev1.TraceHop{Ttl: h.TTL, RttMs: h.RTT, Answered: h.Kind != "silent", Label: label})
		}
		if b, err := json.Marshal(cur); err == nil {
			if err := f.st.AddNodeTrace(ctx, from.ID, to.ID, at, string(b)); err != nil {
				f.log.Warn("store node trace", "code", "store_failed", "err", err)
			}
		}
		if all, err := f.st.NodeTraces(ctx, from.ID, to.ID, store.NodeTraceKeep); err == nil {
			out.Kept = uint32(len(all))
		}
	}
	f.audit(context.WithoutCancel(ctx), "node.trace", map[string]string{"from": from.Name, "to": to.Name, "error": out.Error})
	return connect.NewResponse(out), nil
}

func (f *Fleet) runTrace(ctx context.Context, from, to store.NodeRow) (traceJSON, string) {
	liveRows, err := f.Live(ctx)
	if err != nil {
		f.log.Warn("read live nodes for trace", "code", "store_failed", "err", err)
		return traceJSON{}, "failed"
	}
	fromLive, code := f.probeLive(liveRowsByID(liveRows), from.ID, capProbe)
	if code != "" {
		return traceJSON{}, code
	}
	// the port the SYNs go to: the target's TCP port 443 when it serves one, else its first TCP port, else 443 (a closed port
	// answers with a RST, which ends the walk as well)
	port := uint32(443)
	if specs, pins, err := f.tcpSpecsOf(ctx, to); err == nil {
		if ts := tcpTargets(specs, pins); len(ts) > 0 && !slices.ContainsFunc(ts, func(t tcpTarget) bool { return t.Port == 443 }) {
			port = uint32(ts[0].Port)
		}
	}
	result, err := f.commandWithLive(ctx, from.ID, traceWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_PathTrace{PathTrace: &agentv1.PathTrace{
			RequestId: reqID, Host: to.Address, Port: port, MaxTtl: traceMaxTTL}}}
	}, &fromLive)
	if err != nil {
		return traceJSON{}, "failed"
	}
	if result == nil || !result.Ok {
		return traceJSON{}, traceError(result)
	}
	cur := traceJSON{Reached: result.Params["reached"] == "1"}
	for ttl := 1; ttl <= 32; ttl++ {
		v, ok := result.Params["h"+strconv.Itoa(ttl)]
		if !ok {
			break
		}
		kind, ms, _ := strings.Cut(v, ":")
		if !validTraceKind(kind) {
			return traceJSON{}, "failed"
		}
		rtt, _ := strconv.ParseUint(ms, 10, 32)
		cur.Hops = append(cur.Hops, traceHopJSON{TTL: uint32(ttl), Kind: kind, RTT: uint32(rtt)})
	}
	if len(cur.Hops) == 0 {
		return traceJSON{}, "failed"
	}
	return cur, ""
}

func traceError(result *agentv1.CommandResult) string {
	if result == nil {
		return "failed"
	}
	switch strings.SplitN(result.Error, ":", 2)[0] {
	case "busy":
		return "busy"
	case "unsupported", "unsupported_host":
		return "unsupported"
	case "no_route":
		return "no_route"
	default:
		return "failed"
	}
}

// traceDiff compares a trace with the pair's previous one. The agent returns no router address (only that a hop answered
// and how fast), so a route change is read from the shape: the hop where the target was reached moved, or it is no longer
// reached. A hop that stops answering is probably a router limiting ICMP and is said so, not called a new route.
func traceDiff(prev *traceJSON, cur traceJSON) (changedAt uint32, text string) {
	if prev == nil {
		return 0, "first trace of this pair"
	}
	switch {
	case prev.Reached && cur.Reached && len(prev.Hops) != len(cur.Hops):
		at := min(len(prev.Hops), len(cur.Hops))
		return uint32(at), fmt.Sprintf("route changed at hop %d: the target is reached at hop %d, it was %d", at, len(cur.Hops), len(prev.Hops))
	case prev.Reached && !cur.Reached:
		return uint32(len(cur.Hops)), "the target was reached before and is not now: nothing past hop " + lastAnswered(cur.Hops)
	case !prev.Reached && cur.Reached:
		return uint32(len(cur.Hops)), fmt.Sprintf("the target is reached now, at hop %d; it was not before", len(cur.Hops))
	}
	n := min(len(prev.Hops), len(cur.Hops))
	for i := 0; i < n; i++ {
		if (prev.Hops[i].Kind == "silent") != (cur.Hops[i].Kind == "silent") {
			if cur.Hops[i].Kind == "silent" {
				return 0, fmt.Sprintf("same route length; hop %d stopped answering", i+1)
			}
			return 0, fmt.Sprintf("same route length; hop %d answers now", i+1)
		}
	}
	for i := 0; i < n; i++ {
		a, b := prev.Hops[i].RTT, cur.Hops[i].RTT
		if a == 0 || b == 0 {
			continue
		}
		if b >= 2*a && b-a >= 20 || a >= 2*b && a-b >= 20 {
			return 0, fmt.Sprintf("same route length; the round trip at hop %d went from %d to %d ms", i+1, a, b)
		}
	}
	return 0, "same as before"
}

func lastAnswered(hops []traceHopJSON) string {
	for i := len(hops) - 1; i >= 0; i-- {
		if hops[i].Kind != "silent" {
			return strconv.Itoa(int(hops[i].TTL))
		}
	}
	return "0"
}

// ---------------------------------------------------------------------------------------------------------------------
// node_dns_check

// NodeDnsCheck runs the doctor's resolver check on a node (run) and shows its timing with the history of the last 24 runs.
// The history is filled by this call, which also takes in the stored doctor result when it is newer than the last run kept.
func (s probeService) NodeDnsCheck(ctx context.Context, req *connect.Request[probev1.NodeDnsCheckRequest]) (*connect.Response[probev1.NodeDnsCheckResponse], error) {
	f := s.f
	node, err := f.probeNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	out := &probev1.NodeDnsCheckResponse{NodeName: node.Name, Trend: "unknown"}
	if req.Msg.Run {
		release, err := f.startProbe(ctx, node.ID, node.ID)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	runs, err := f.st.NodeDNSRuns(ctx, node.ID)
	if err != nil {
		return nil, internalErr(f.log.Error, "read resolver runs", err)
	}
	remember := func(r store.NodeDNSRun) {
		if len(runs) > 0 && !r.At.After(runs[len(runs)-1].At) {
			return
		}
		if err := f.st.AddNodeDNSRun(ctx, r); err != nil {
			f.log.Warn("store resolver run", "code", "store_failed", "err", err)
			return
		}
		runs = append(runs, r)
	}
	// the doctor's own periodic result, when the node ran it since the last run kept
	if rows, err := f.st.DoctorResults(ctx, node.ID); err == nil {
		for _, row := range rows {
			if r, ok := dnsRunOf(node.ID, row.CheckID, row.Params, row.Measured); ok {
				remember(r)
			}
		}
	}
	if req.Msg.Run {
		liveRows, err := f.Live(ctx)
		if err != nil {
			return nil, internalErr(f.log.Error, "read live nodes for resolver check", err)
		}
		if _, code := f.probeLive(liveRowsByID(liveRows), node.ID, capDoctor); code != "" {
			out.Error = code
		} else if report, err := f.RunDoctor(ctx, node.ID, []string{"resolver"}, dnsRunWait); err != nil || report == nil || report.Error != "" {
			out.Error = "failed"
		} else {
			for _, res := range report.Results {
				// the agent's own time of the measurement, which is what the stored doctor result carries too: the same
				// run is then one entry, not two
				measured := f.now()
				if res.MeasuredUnix > 0 {
					measured = time.Unix(res.MeasuredUnix, 0)
				}
				if r, ok := dnsRunOf(node.ID, res.Id, res.Params, measured); ok {
					remember(r)
				}
			}
		}
		f.audit(context.WithoutCancel(ctx), "node.dns_check", map[string]string{"node": node.Name, "error": out.Error})
	}
	for _, r := range runs {
		out.History = append(out.History, dnsRunMessage(r))
	}
	if n := len(runs); n > 0 {
		out.Latest = dnsRunMessage(runs[n-1])
	}
	out.Trend = dnsTrend(runs)
	return connect.NewResponse(out), nil
}

func dnsRunMessage(r store.NodeDNSRun) *probev1.DnsRun {
	return &probev1.DnsRun{AtUnix: r.At.Unix(), P50Ms: r.P50MS, P95Ms: r.P95MS, Failed: r.Failed, Domains: r.Total}
}

// dnsRunOf reads one resolver check result: p50_ms and p95_ms (median_ms from an agent that predates them), failed_count and
// domains.
func dnsRunOf(nodeID, checkID string, params map[string]string, at time.Time) (store.NodeDNSRun, bool) {
	if checkID != "resolver" || at.IsZero() {
		return store.NodeDNSRun{}, false
	}
	num := func(keys ...string) uint32 {
		for _, k := range keys {
			if v, err := strconv.ParseUint(params[k], 10, 32); err == nil {
				return uint32(v)
			}
		}
		return 0
	}
	total := num("domains")
	if total == 0 {
		return store.NodeDNSRun{}, false
	}
	return store.NodeDNSRun{NodeID: nodeID, At: at.UTC().Truncate(time.Second),
		P50MS: num("p50_ms", "median_ms"), P95MS: num("p95_ms"), Failed: num("failed_count"), Total: total}, true
}

// dnsTrend compares the median of the newest three runs with the earlier ones: slower or faster when it is at least half
// again and 20 ms apart, else steady; unknown with fewer than dnsTrendMin runs.
func dnsTrend(runs []store.NodeDNSRun) string {
	if len(runs) < dnsTrendMin {
		return "unknown"
	}
	recent, earlier := runs[len(runs)-3:], runs[:len(runs)-3]
	median := func(rs []store.NodeDNSRun) float64 {
		xs := make([]float64, len(rs))
		for i, r := range rs {
			xs[i] = float64(r.P50MS)
		}
		sort.Float64s(xs)
		return xs[len(xs)/2]
	}
	r, e := median(recent), median(earlier)
	switch {
	case r >= e*1.5 && r-e >= 20:
		return "slower"
	case e >= r*1.5 && e-r >= 20:
		return "faster"
	}
	return "steady"
}
