//go:build !js

package fleet

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Panel orchestration of the probes against fake agents: the order of the commands, what each carries, the capability gate
// (an agent without probe/1 is sent nothing), the limits, and that no address reaches a result, a stored row or the audit log.

const probeTargetAddress = "203.0.113.10" // the address portCheckTarget gives the target

var addressRe = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)

type probeRPCResult struct {
	resp *probev1.PathProbeResponse
	err  error
}

func startPathProbe(x *l3Env, from, to, kind string, ports, sizes []uint32) <-chan probeRPCResult {
	out := make(chan probeRPCResult, 1)
	go func() {
		r, err := (probeService{x.f}).PathProbe(x.ctx, connect.NewRequest(&probev1.PathProbeRequest{
			FromNodeId: from, ToNodeId: to, Kind: kind, Ports: ports, Sizes: sizes}))
		if err != nil {
			out <- probeRPCResult{err: err}
			return
		}
		out <- probeRPCResult{resp: r.Msg}
	}()
	return out
}

func awaitProbe(t *testing.T, ch <-chan probeRPCResult) *probev1.PathProbeResponse {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.resp
	case <-time.After(5 * time.Second):
		t.Fatal("the probe did not finish")
		return nil
	}
}

// noCommand fails when the connection is sent a probe command within d.
func noCommand(t *testing.T, c *conn, d time.Duration) {
	t.Helper()
	timer := time.After(d)
	for {
		select {
		case m := <-c.in:
			if m.GetUdpSend() != nil || m.GetUdpCount() != nil || m.GetTcpProbe() != nil || m.GetPathTrace() != nil || m.GetRunDoctor() != nil {
				t.Fatalf("an agent that cannot take the command was sent %v", m)
			}
		case <-timer:
			return
		}
	}
}

func probeCounts(port uint16, packets, size uint64) *agentv1.CommandResult {
	p := strconv.Itoa(int(port))
	return &agentv1.CommandResult{Ok: true, Params: map[string]string{"p" + p: strconv.FormatUint(packets, 10), "b" + p: strconv.FormatUint(packets*(size+28), 10)}}
}

// noAddress fails when text carries an IPv4 address.
func noAddress(t *testing.T, what, text string) {
	t.Helper()
	if m := addressRe.FindString(text); m != "" {
		t.Errorf("%s carries an address (%s): %s", what, m, text)
	}
}

func targetName(t *testing.T, x *l3Env, id string) string {
	t.Helper()
	n, err := x.st.Node(x.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return n.Name
}

func TestPathProbeUDPArmsSendsAndStoresWithoutAnAddress(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capProbe)
	result := startPathProbe(x, sender.nodeID, target.nodeID, "udp", []uint32{443}, nil)

	arm := waitUDPCount(targetConn, false)
	if len(arm.Tag) != 8 || len(arm.Ports) != 1 || arm.Ports[0] != 443 {
		t.Fatalf("arm = %+v", arm)
	}
	replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
	send := waitUDPSend(senderConn)
	if send.Host != probeTargetAddress || send.Count != udpCheckSendCount || send.Pps != udpCheckPPS || send.Size != udpCheckSize || send.Df || len(send.Ports) != 1 {
		t.Fatalf("send = %+v", send)
	}
	replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "150"}})
	stop := waitUDPCount(targetConn, true)
	replyPortCommand(targetConn, stop.RequestId, probeCounts(443, 120, udpCheckSize))

	got := awaitProbe(t, result)
	if got.Error != "" || got.Kind != "udp" || got.FromName != "sender" || got.ToName != "nodea" || len(got.Ports) != 1 ||
		got.Ports[0].Port != 443 || got.Ports[0].Sent != 150 || got.Ports[0].Got != 120 || got.Ports[0].Verdict != "lossy" {
		t.Fatalf("response = %+v", got)
	}
	noAddress(t, "the response", prototext.Format(got))
	rows, err := x.st.PathProbes(x.ctx, time.Unix(0, 0), target.nodeID, 0)
	if err != nil || len(rows) != 1 || rows[0].Kind != "udp" || rows[0].FromID != sender.nodeID {
		t.Fatalf("stored probes = %+v, %v", rows, err)
	}
	noAddress(t, "the stored result", rows[0].ResultJSON)
	var params string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params FROM audit WHERE action = 'node.path_probe'`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	noAddress(t, "the audit row", params)
	if !strings.Contains(params, `"from":"sender"`) || !strings.Contains(params, `"to":"nodea"`) {
		t.Errorf("audit row = %s, want node names", params)
	}
}

func TestPathProbeMTUSweepSendsDontFragmentAndFindsTheLargestSize(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capProbe)
	result := startPathProbe(x, sender.nodeID, target.nodeID, "udp", []uint32{443}, []uint32{1200, 1452, 1200})

	// 1200 arrives whole; 1452 is refused by the sender's own interface (EMSGSIZE, no_route): the counter is still stopped
	for _, size := range []uint32{1200, 1452} {
		arm := waitUDPCount(targetConn, false)
		replyPortCommand(targetConn, arm.RequestId, &agentv1.CommandResult{Ok: true})
		send := waitUDPSend(senderConn)
		if !send.Df || send.Size != size || send.Count != probeSweepCount || send.Pps != probeSweepPPS || len(send.Ports) != 1 || send.Ports[0] != 443 {
			t.Fatalf("sweep send for %d = %+v", size, send)
		}
		if size == 1200 {
			replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "sent": "10"}})
		} else {
			replyPortCommand(senderConn, send.RequestId, &agentv1.CommandResult{Error: "no_route"})
		}
		stop := waitUDPCount(targetConn, true)
		packets := uint64(10)
		if size != 1200 {
			packets = 0
		}
		replyPortCommand(targetConn, stop.RequestId, probeCounts(443, packets, uint64(size)))
	}
	got := awaitProbe(t, result)
	if got.Error != "" || got.LargestSize != 1200 || len(got.Sizes) != 2 ||
		got.Sizes[0].Size != 1200 || got.Sizes[0].Got != 10 || got.Sizes[1].Size != 1452 || got.Sizes[1].Sent != 0 || got.Sizes[1].Got != 0 {
		t.Fatalf("sweep = %+v", got)
	}
}

func TestPathProbeRefusesAnOldAgentAndSendsItNothing(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	// an agent that has the UDP check but predates probe/1 must not be sent the don't-fragment field or any new command
	old, oldConn := addPortCheckPeer(x, "old", "provider-b", "DE", capUDPCheck)
	got := awaitProbe(t, startPathProbe(x, old.nodeID, target.nodeID, "udp", []uint32{443}, []uint32{1200}))
	if got.Error != "agent_too_old" || len(got.Sizes) != 0 {
		t.Fatalf("old sender: %+v", got)
	}
	noCommand(t, oldConn, 200*time.Millisecond)
	noCommand(t, targetConn, 50*time.Millisecond)
	if rows, _ := x.st.PathProbes(x.ctx, time.Unix(0, 0), "", 0); len(rows) != 0 {
		t.Errorf("a refused probe was stored: %+v", rows)
	}

	// the trace is gated the same way, at the panel's request layer too
	if capability, _ := requestCapability(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_PathTrace{PathTrace: &agentv1.PathTrace{}}}, false); capability != capProbe {
		t.Errorf("PathTrace needs %q, got %q", capProbe, capability)
	}
	traced, err := (probeService{x.f}).NodeTrace(x.ctx, connect.NewRequest(&probev1.NodeTraceRequest{FromNodeId: old.nodeID, ToNodeId: target.nodeID}))
	if err != nil || traced.Msg.Error != "agent_too_old" || len(traced.Msg.Hops) != 0 {
		t.Fatalf("trace from an old agent: %+v, %v", traced, err)
	}
	noCommand(t, oldConn, 100*time.Millisecond)
}

func TestPathProbeOnlyNamesFleetPortsAndNodes(t *testing.T) {
	x, target, _ := portCheckTarget(t)
	sender, _ := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck, capProbe, capTCPCheck)
	for _, test := range []struct {
		name, kind, want string
		ports            []uint32
	}{
		{"a port the target does not serve", "udp", "bad_port", []uint32{9999}},
		{"no TCP port to probe on a target with only UDP inbounds", "tcp", "no_ports", nil},
		{"tls without a TLS listener", "tls", "no_ports", nil},
		{"a TCP port the target does not serve", "tcp", "bad_port", []uint32{22}},
	} {
		got := awaitProbe(t, startPathProbe(x, sender.nodeID, target.nodeID, test.kind, test.ports, nil))
		if got.Error != test.want {
			t.Errorf("%s: error = %q, want %q", test.name, got.Error, test.want)
		}
	}
	for _, test := range []struct {
		name string
		req  *probev1.PathProbeRequest
		code connect.Code
	}{
		{"a kind that is no probe", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Kind: "icmp"}, connect.CodeInvalidArgument},
		{"the same node twice", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: sender.nodeID, Kind: "udp"}, connect.CodeInvalidArgument},
		{"an address as the target", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: "203.0.113.5", Kind: "udp"}, connect.CodeNotFound},
		{"a URL as the sender", &probev1.PathProbeRequest{FromNodeId: "http://example.com/", ToNodeId: target.nodeID, Kind: "tcp"}, connect.CodeNotFound},
		{"sizes on tcp", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Kind: "tcp", Sizes: []uint32{1200}}, connect.CodeInvalidArgument},
		{"a size over the cap", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Kind: "udp", Sizes: []uint32{1473}}, connect.CodeInvalidArgument},
		{"six sizes", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Kind: "udp", Sizes: []uint32{100, 200, 300, 400, 500, 600}}, connect.CodeInvalidArgument},
		{"port zero", &probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Kind: "udp", Ports: []uint32{0}}, connect.CodeInvalidArgument},
	} {
		_, err := (probeService{x.f}).PathProbe(x.ctx, connect.NewRequest(test.req))
		if connect.CodeOf(err) != test.code {
			t.Errorf("%s: code = %v (%v), want %v", test.name, connect.CodeOf(err), err, test.code)
		}
	}
}

func TestPathProbeTCPAndTLSWireTargetsAndTimings(t *testing.T) {
	x, target, _ := portCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capTCPCheck, capProbe)
	from, _ := x.st.Node(x.ctx, sender.nodeID)
	to, _ := x.st.Node(x.ctx, target.nodeID)
	liveRows, err := x.f.Live(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	fromLive := liveRowsByID(liveRows)[sender.nodeID]
	pin := strings.Repeat("ab", 32)
	targets := []tcpTarget{{Port: 443, Role: tcpRoleSite, Kind: "tls", SNI: "de1.example.com", Pin: pin}, {Port: 80, Role: tcpRoleACME, Kind: "http_mark"}}

	run := func(kind string) (pathProbeResult, *agentv1.TcpProbe) {
		out := make(chan pathProbeResult, 1)
		go func() { out <- x.f.pathProbeTCP(x.ctx, from, fromLive, to, kind, targets) }()
		probe := senderConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetTcpProbe() != nil }).GetTcpProbe()
		replyPortCommand(senderConn, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{
			"family": "4", "t443": "ours", "m443": "31", "t80": "refused", "m80": "2"}})
		return <-out, probe
	}
	res, probe := run("tls")
	if probe.Host != probeTargetAddress || len(probe.Targets) != 2 || probe.Targets[0].Kind != "tls" || probe.Targets[0].Sni != "de1.example.com" || probe.Targets[0].PinSha256 != pin ||
		probe.Targets[1].Kind != "connect" || probe.Targets[1].Sni != "" {
		t.Fatalf("tls wire targets = %+v", probe.Targets)
	}
	if len(res.Ports) != 2 || res.Ports[0].Verdict != "ours" || res.Ports[0].MS != 31 || res.Ports[1].Verdict != "refused" || res.Ports[1].MS != 2 {
		t.Fatalf("tls result = %+v", res)
	}
	res, probe = run("tcp")
	if probe.Targets[0].Kind != "connect" || probe.Targets[0].Sni != "" || probe.Targets[0].PinSha256 != "" || probe.Targets[1].Kind != "connect" {
		t.Fatalf("tcp wire targets = %+v: a tcp probe is a plain connect", probe.Targets)
	}
	if res.Ports[0].MS != 31 {
		t.Errorf("tcp result = %+v", res)
	}
	// an agent that answers an unknown verdict does not make one up
	go func() {
		p := senderConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetTcpProbe() != nil }).GetTcpProbe()
		replyPortCommand(senderConn, p.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t443": "<script>"}})
	}()
	if res := x.f.pathProbeTCP(x.ctx, from, fromLive, to, "tcp", targets[:1]); res.Ports[0].Verdict != "failed" || res.Ports[0].MS != 0 {
		t.Errorf("an unknown verdict came through: %+v", res)
	}
}

func TestProbeRateLimitsPerCallerPerNodeAndPerPair(t *testing.T) {
	x, _, _ := portCheckTarget(t)
	var actor = "token-a"
	x.f.cfg.Actor = func(context.Context) string { return actor }
	clock := x.clock

	// one probe per pair at a time, in either direction
	release, err := x.f.startProbe(x.ctx, "n1", "n2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.f.startProbe(x.ctx, "n2", "n1"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a second probe on a running pair: %v", err)
	}
	release()
	rel, err := x.f.startProbe(x.ctx, "n2", "n1")
	if err != nil {
		t.Fatalf("the pair was not given back: %v", err)
	}
	rel()

	// a node is the end of at most probeNodeMax probes in its window (two are used above)
	for i := 0; i < probeNodeMax-2; i++ {
		rel, err := x.f.startProbe(x.ctx, "n1", "peer"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		rel()
	}
	_, err = x.f.startProbe(x.ctx, "n1", "fresh")
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "try again in") {
		t.Fatalf("the node limit: %v", err)
	}
	if rel, err := x.f.startProbe(x.ctx, "fresh", "other"); err != nil { // another pair of other nodes is not affected
		t.Fatalf("an unrelated pair: %v", err)
	} else {
		rel()
	}
	clock.add(probeNodeWindow + time.Second)
	if rel, err := x.f.startProbe(x.ctx, "n1", "fresh"); err != nil {
		t.Fatalf("the node window did not roll: %v", err)
	} else {
		rel()
	}

	// a caller may start probeCallerMax probes in its window, on any nodes; another caller has its own
	clock.add(probeCallerWindow + time.Second)
	for i := 0; i < probeCallerMax; i++ {
		rel, err := x.f.startProbe(x.ctx, "a"+strconv.Itoa(i), "b"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("probe %d of the caller: %v", i, err)
		}
		rel()
	}
	if _, err := x.f.startProbe(x.ctx, "z1", "z2"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("the token limit: %v", err)
	}
	actor = "token-b"
	rel, err = x.f.startProbe(x.ctx, "z1", "z2")
	if err != nil {
		t.Fatalf("another token shares the limit: %v", err)
	}
	rel()
	actor = "token-a"
	clock.add(probeCallerWindow + time.Second)
	if rel, err := x.f.startProbe(x.ctx, "z1", "z2"); err != nil {
		t.Fatalf("the token window did not roll: %v", err)
	} else {
		rel()
	}
}

func TestProbeAndSpeedShareFleetActiveLimit(t *testing.T) {
	x, _, _ := portCheckTarget(t)
	actor := ""
	x.f.cfg.Actor = func(context.Context) string { return actor }
	releases := make([]func(), 0, maxActiveProbes)
	for i := 0; i < maxActiveProbes-1; i++ {
		actor = "probe-" + strconv.Itoa(i)
		release, err := x.f.startProbe(x.ctx, "from"+strconv.Itoa(i), "to"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	actor = "speed"
	release, err := x.f.startSpeed(x.ctx, "speed-from", "speed-to")
	if err != nil {
		t.Fatalf("speed test filled the fleet limit: %v", err)
	}
	releases = append(releases, release)

	actor = "another-operator"
	if _, err := x.f.startProbe(x.ctx, "last-from", "last-to"); connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "fleet probe limit") {
		t.Fatalf("ninth active probe = %v, want fleet limit", err)
	}
	for _, release := range releases {
		release()
	}
	if release, err := x.f.startProbe(x.ctx, "last-from", "last-to"); err != nil {
		t.Fatalf("a released fleet slot stayed busy: %v", err)
	} else {
		release()
	}
}

func TestPathProbeRateLimitReachesTheRPC(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	sender, _ := addPortCheckPeer(x, "sender", "provider-b", "DE", capUDPCheck) // too old: each probe ends at once but counts
	for i := 0; i < probeNodeMax; i++ {
		got := awaitProbe(t, startPathProbe(x, sender.nodeID, target.nodeID, "udp", []uint32{443}, nil))
		if got.Error != "agent_too_old" {
			t.Fatalf("probe %d: %+v", i, got)
		}
	}
	_, err := (probeService{x.f}).PathProbe(x.ctx, connect.NewRequest(&probev1.PathProbeRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Kind: "udp"}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("the seventh probe on a node in 5 minutes: %v", err)
	}
	noCommand(t, targetConn, 20*time.Millisecond)
}

func TestNodeTraceReturnsHopsWithoutRoutersAndDiffsAgainstThePrevious(t *testing.T) {
	x, target, _ := portCheckTarget(t)
	sender, senderConn := addPortCheckPeer(x, "sender", "provider-b", "DE", capProbe)
	trace := func(params map[string]string) *probev1.NodeTraceResponse {
		out := make(chan *probev1.NodeTraceResponse, 1)
		go func() {
			r, err := (probeService{x.f}).NodeTrace(x.ctx, connect.NewRequest(&probev1.NodeTraceRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID}))
			if err != nil {
				t.Error(err)
				out <- &probev1.NodeTraceResponse{}
				return
			}
			out <- r.Msg
		}()
		cmd := senderConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetPathTrace() != nil }).GetPathTrace()
		if cmd.Host != probeTargetAddress || cmd.Port != 443 || cmd.MaxTtl != traceMaxTTL {
			t.Errorf("PathTrace = %+v", cmd)
		}
		replyPortCommand(senderConn, cmd.RequestId, &agentv1.CommandResult{Ok: true, Params: params})
		select {
		case r := <-out:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("the trace did not finish")
			return nil
		}
	}
	first := trace(map[string]string{"family": "4", "reached": "1", "h1": "icmp:1", "h2": "silent:0", "h3": "icmp:9", "h4": "open:12"})
	if first.Error != "" || !first.Reached || len(first.Hops) != 4 || first.Diff != "first trace of this pair" || first.Kept != 1 {
		t.Fatalf("first trace = %+v", first)
	}
	if first.Hops[0].Label != "hop 1" || !first.Hops[0].Answered || first.Hops[1].Answered || first.Hops[3].Label != "nodea" || first.Hops[2].RttMs != 9 {
		t.Errorf("hops = %v", first.Hops)
	}
	noAddress(t, "the trace", prototext.Format(first))

	x.clock.add(5 * time.Second)
	again := trace(map[string]string{"family": "4", "reached": "1", "h1": "icmp:1", "h2": "silent:0", "h3": "icmp:9", "h4": "open:12"})
	if again.Diff != "same as before" || again.ChangedAtHop != 0 || again.Kept != 2 || again.PreviousUnix == 0 {
		t.Fatalf("same route: %+v", again)
	}
	x.clock.add(5 * time.Second)
	moved := trace(map[string]string{"family": "4", "reached": "1", "h1": "icmp:1", "h2": "icmp:4", "h3": "refused:11"})
	if moved.ChangedAtHop != 3 || !strings.Contains(moved.Diff, "route changed at hop 3") || moved.Hops[2].Label != "nodea" {
		t.Fatalf("shorter route: %+v", moved)
	}
	rows, err := x.st.NodeTraces(x.ctx, sender.nodeID, target.nodeID, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("stored traces = %d, %v", len(rows), err)
	}
	for _, row := range rows {
		noAddress(t, "a stored trace", row.ResultJSON)
	}
	// a refusal from the agent is a code, not a hop list
	go func() {
		cmd := senderConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetPathTrace() != nil }).GetPathTrace()
		replyPortCommand(senderConn, cmd.RequestId, &agentv1.CommandResult{Error: "unsupported"})
	}()
	x.clock.add(5 * time.Second)
	r, err := (probeService{x.f}).NodeTrace(x.ctx, connect.NewRequest(&probev1.NodeTraceRequest{FromNodeId: sender.nodeID, ToNodeId: target.nodeID}))
	if err != nil || r.Msg.Error != "unsupported" || len(r.Msg.Hops) != 0 {
		t.Fatalf("an unsupported host: %+v, %v", r, err)
	}
}

func TestTraceDiff(t *testing.T) {
	hops := func(kinds string, rtts ...uint32) []traceHopJSON {
		var out []traceHopJSON
		for i, k := range strings.Fields(kinds) {
			h := traceHopJSON{TTL: uint32(i + 1), Kind: k}
			if i < len(rtts) {
				h.RTT = rtts[i]
			}
			out = append(out, h)
		}
		return out
	}
	base := &traceJSON{Reached: true, Hops: hops("icmp silent icmp open", 1, 0, 9, 12)}
	for _, test := range []struct {
		name string
		cur  traceJSON
		at   uint32
		want string
	}{
		{"same", traceJSON{Reached: true, Hops: hops("icmp silent icmp open", 1, 0, 9, 12)}, 0, "same as before"},
		{"shorter", traceJSON{Reached: true, Hops: hops("icmp icmp open")}, 3, "route changed at hop 3"},
		{"longer", traceJSON{Reached: true, Hops: hops("icmp silent icmp icmp refused")}, 4, "route changed at hop 4"},
		{"lost", traceJSON{Reached: false, Hops: hops("icmp icmp silent silent")}, 4, "was reached before and is not now"},
		{"icmp limited", traceJSON{Reached: true, Hops: hops("icmp icmp icmp open")}, 0, "hop 2 answers now"},
		{"rtt jump", traceJSON{Reached: true, Hops: hops("icmp silent icmp open", 1, 0, 90, 95)}, 0, "round trip at hop 3 went from 9 to 90 ms"},
	} {
		at, text := traceDiff(base, test.cur)
		if at != test.at || !strings.Contains(text, test.want) {
			t.Errorf("%s: (%d, %q), want (%d, ...%s...)", test.name, at, text, test.at, test.want)
		}
	}
	if _, text := traceDiff(nil, *base); text != "first trace of this pair" {
		t.Errorf("no previous: %q", text)
	}
	if _, text := traceDiff(&traceJSON{Reached: false, Hops: hops("icmp silent")}, *base); !strings.Contains(text, "reached now") {
		t.Errorf("newly reached: %q", text)
	}
}

func TestNodeDNSCheckRunsTheDoctorKeepsAHistoryAndTrends(t *testing.T) {
	x, target, targetConn := portCheckTarget(t)
	// the node lists doctor/1 only in a hello that says so; connect it again with the capability
	_ = targetConn
	a, aConn := addPortCheckPeer(x, "dnsnode", "provider-b", "DE", capDoctor)
	_ = target
	at := x.clock.now().Add(-time.Hour)
	stored := func(p50 string, when time.Time) {
		rows := []store.DoctorRow{{NodeID: a.nodeID, CheckID: "resolver", Status: 1, Measured: when,
			Params: map[string]string{"domains": "3", "failed_count": "0", "p50_ms": p50, "p95_ms": "30", "median_ms": p50}}}
		if err := x.st.PutDoctor(x.ctx, a.nodeID, rows, false, when); err != nil {
			t.Fatal(err)
		}
	}
	call := func(run bool) *probev1.NodeDnsCheckResponse {
		r, err := (probeService{x.f}).NodeDnsCheck(x.ctx, connect.NewRequest(&probev1.NodeDnsCheckRequest{NodeId: a.nodeID, Run: run}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	stored("10", at)
	got := call(false)
	if got.NodeName != "dnsnode" || got.Latest == nil || got.Latest.P50Ms != 10 || got.Latest.P95Ms != 30 || got.Latest.Domains != 3 || len(got.History) != 1 || got.Trend != "unknown" {
		t.Fatalf("stored read = %+v", got)
	}
	for i, p50 := range []string{"11", "9", "10"} {
		stored(p50, at.Add(time.Duration(i+1)*10*time.Minute))
		got = call(false)
	}
	if len(got.History) != 4 || got.Trend != "steady" || got.History[0].AtUnix >= got.History[3].AtUnix {
		t.Fatalf("four runs = %+v", got)
	}
	// reading again adds nothing
	if again := call(false); len(again.History) != 4 {
		t.Fatalf("a repeated read grew the history: %d", len(again.History))
	}
	// a run asks the doctor for the resolver check only and keeps its result
	go func() {
		m := aConn.wait(func(m *agentv1.ConnectResponse) bool { return m.GetRunDoctor() != nil }).GetRunDoctor()
		if len(m.Checks) != 1 || m.Checks[0] != "resolver" {
			t.Errorf("checks = %v", m.Checks)
		}
		aConn.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DoctorReport{DoctorReport: &agentv1.DoctorReport{
			RequestId: m.RequestId, Partial: true, Results: []*agentv1.DoctorResult{{Id: "resolver", Params: map[string]string{
				"domains": "3", "failed_count": "1", "p50_ms": "400", "p95_ms": "900"}}}}}})
	}()
	x.clock.add(5 * time.Second)
	got = call(true)
	if got.Error != "" || got.Latest == nil || got.Latest.P50Ms != 400 || got.Latest.P95Ms != 900 || got.Latest.Failed != 1 || len(got.History) != 5 || got.Trend != "unknown" && got.Trend != "steady" && got.Trend != "slower" {
		t.Fatalf("after a run = %+v", got)
	}
	// a node with an agent that predates the doctor is refused, nothing sent
	old, oldConn := addPortCheckPeer(x, "nodoc", "provider-b", "DE")
	r, err := (probeService{x.f}).NodeDnsCheck(x.ctx, connect.NewRequest(&probev1.NodeDnsCheckRequest{NodeId: old.nodeID, Run: true}))
	if err != nil || r.Msg.Error != "agent_too_old" {
		t.Fatalf("old agent: %+v, %v", r, err)
	}
	noCommand(t, oldConn, 100*time.Millisecond)
	noAddress(t, "the dns answer", prototext.Format(got))
}

func TestDNSTrendAndRunParsing(t *testing.T) {
	runs := func(p50 ...uint32) []store.NodeDNSRun {
		var out []store.NodeDNSRun
		for _, p := range p50 {
			out = append(out, store.NodeDNSRun{P50MS: p})
		}
		return out
	}
	for _, test := range []struct {
		runs []store.NodeDNSRun
		want string
	}{
		{runs(10, 10, 10), "unknown"},
		{runs(10, 11, 9, 10, 12), "steady"},
		{runs(10, 12, 9, 80, 90, 85), "slower"},
		{runs(100, 120, 90, 10, 12, 9), "faster"},
		{runs(2, 2, 2, 4, 4, 4), "steady"}, // doubled but under 20 ms: noise
	} {
		if got := dnsTrend(test.runs); got != test.want {
			t.Errorf("dnsTrend(%v) = %s, want %s", test.runs, got, test.want)
		}
	}
	now := time.Unix(1700000000, 0)
	// an agent that predates p50_ms and p95_ms still has the median
	r, ok := dnsRunOf("n", "resolver", map[string]string{"domains": "3", "median_ms": "15"}, now)
	if !ok || r.P50MS != 15 || r.P95MS != 0 || r.Total != 3 {
		t.Errorf("old params = %+v, %v", r, ok)
	}
	if _, ok := dnsRunOf("n", "disk_space", map[string]string{"domains": "3"}, now); ok {
		t.Error("another check was taken for the resolver")
	}
	if _, ok := dnsRunOf("n", "resolver", map[string]string{}, now); ok {
		t.Error("a result without domains was taken as a run")
	}
}

// No admin response of the three probes may carry what an address would look like; the messages have no field for one.
func TestProbeResponsesHaveNoAddressField(t *testing.T) {
	for _, m := range []proto.Message{&probev1.PathProbeResponse{}, &probev1.PathProbePort{}, &probev1.NodeTraceResponse{}, &probev1.TraceHop{}, &probev1.NodeDnsCheckResponse{}, &probev1.DnsRun{}} {
		fields := m.ProtoReflect().Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			if n := string(fields.Get(i).Name()); strings.Contains(n, "addr") || strings.Contains(n, "ip") && n != "ms" && !strings.Contains(n, "ttl") {
				t.Errorf("%s has a field %q that could carry an address", m.ProtoReflect().Descriptor().Name(), n)
			}
		}
	}
}
