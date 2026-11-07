//go:build !js

package fleet

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/prototext"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

// The panel side of path_speed against fake agents: the order of the commands (serve on the target, run on the sender, stop on
// the target), what each carries, the capability gate, the caps, and that no address reaches a result, a stored row or the audit log.

type speedRPCResult struct {
	resp *probev1.PathSpeedResponse
	err  error
}

func startPathSpeed(x *l3Env, from, to string, mb uint32, direction string) <-chan speedRPCResult {
	out := make(chan speedRPCResult, 1)
	go func() {
		r, err := (probeService{x.f}).PathSpeed(x.ctx, connect.NewRequest(&probev1.PathSpeedRequest{
			FromNodeId: from, ToNodeId: to, Mb: mb, Direction: direction}))
		if err != nil {
			out <- speedRPCResult{err: err}
			return
		}
		out <- speedRPCResult{resp: r.Msg}
	}()
	return out
}

func awaitSpeed(t *testing.T, ch <-chan speedRPCResult) *probev1.PathSpeedResponse {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.resp
	case <-time.After(5 * time.Second):
		t.Fatal("the speed test did not finish")
		return nil
	}
}

// waitSpeed is the next speed command of the given step (1 serve, 2 run, 3 stop) on a connection.
func waitSpeed(c *conn, step uint32) *agentv1.UdpSend {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetSpeedTest() != nil && m.GetSpeedTest().Size == step }).GetSpeedTest()
}

// speedPair is a sender and a target that both list speed/1; the target has the documentation address.
func speedPair(t *testing.T, targetCaps ...string) (x *l3Env, sender, target *agent, senderConn, targetConn *conn) {
	t.Helper()
	x, target = newL3Env(t)
	sender, senderConn = addPortCheckPeer(x, "sender", "provider-b", "DE", capSpeed)
	targetConn, _, _ = connectCaps(target, "speed-target", append([]string{capSpeed}, targetCaps...)...)
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, target.nodeID)
	return
}

func TestPathSpeedServesRunsStopsAndStoresWithoutAnAddress(t *testing.T) {
	x, sender, target, senderConn, targetConn := speedPair(t)
	result := startPathSpeed(x, sender.nodeID, target.nodeID, 24, "down")

	serve := waitSpeed(targetConn, 1)
	if len(serve.Tag) != 16 || serve.Count != 24 || serve.Pps != speedMaxS || serve.Df || serve.Host != "" || len(serve.Ports) != 0 {
		t.Fatalf("serve = %+v", serve)
	}
	replyPortCommand(targetConn, serve.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"port": "40123"}})
	run := waitSpeed(senderConn, 2)
	if run.Host != probeTargetAddress || len(run.Ports) != 1 || run.Ports[0] != 40123 || !bytes.Equal(run.Tag, serve.Tag) || run.Count != 24 || run.Pps != speedMaxS || run.Df {
		t.Fatalf("run = %+v", run)
	}
	replyPortCommand(senderConn, run.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"mbit_s": "412.34", "connect_ms": "12", "first_byte_ms": "31", "duration_ms": "311", "bytes": strconv.Itoa(24 << 20)}})
	stop := waitSpeed(targetConn, 3)
	if !bytes.Equal(stop.Tag, serve.Tag) {
		t.Fatalf("stop carries another token: %+v", stop)
	}
	replyPortCommand(targetConn, stop.RequestId, &agentv1.CommandResult{Ok: true})

	got := awaitSpeed(t, result)
	if got.Error != "" || got.FromName != "sender" || got.ToName != "nodea" || got.Direction != "down" || got.Mb != 24 ||
		got.MbitS != 412.3 || got.ConnectMs != 12 || got.FirstByteMs != 31 || got.DurationMs != 311 || got.Bytes != 24<<20 {
		t.Fatalf("response = %+v", got)
	}
	noAddress(t, "the response", prototext.Format(got))
	rows, err := x.st.PathSpeeds(x.ctx, time.Unix(0, 0), target.nodeID, 0)
	if err != nil || len(rows) != 1 || rows[0].Direction != "down" || rows[0].FromID != sender.nodeID {
		t.Fatalf("stored speeds = %+v, %v", rows, err)
	}
	noAddress(t, "the stored result", rows[0].ResultJSON)
	var params string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params FROM audit WHERE action = 'node.path_speed'`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	noAddress(t, "the audit row", params)
	if !strings.Contains(params, `"from":"sender"`) || !strings.Contains(params, `"to":"nodea"`) {
		t.Errorf("audit row = %s, want node names", params)
	}
	last, err := x.f.LastSpeeds(x.ctx, time.Unix(0, 0), "")
	if err != nil || len(last) != 1 || last[0].MbitS != 412.3 {
		t.Errorf("LastSpeeds = %+v, %v", last, err)
	}
}

func TestPathSpeedUpDirectionAndTheTargetStopsAfterAFailure(t *testing.T) {
	x, sender, target, senderConn, targetConn := speedPair(t)
	result := startPathSpeed(x, sender.nodeID, target.nodeID, 8, "up")
	serve := waitSpeed(targetConn, 1)
	if !serve.Df {
		t.Fatalf("up must say so to the target: %+v", serve)
	}
	replyPortCommand(targetConn, serve.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"port": "40124"}})
	run := waitSpeed(senderConn, 2)
	if !run.Df {
		t.Fatalf("up must say so to the sender: %+v", run)
	}
	// the sender could not connect: the target's listener is closed anyway, and the word is the panel's own
	replyPortCommand(senderConn, run.RequestId, &agentv1.CommandResult{Error: "refused"})
	stop := waitSpeed(targetConn, 3)
	replyPortCommand(targetConn, stop.RequestId, &agentv1.CommandResult{Ok: true})
	got := awaitSpeed(t, result)
	if got.Error != "unreachable" || got.MbitS != 0 {
		t.Fatalf("response = %+v", got)
	}
	if rows, _ := x.st.PathSpeeds(x.ctx, time.Unix(0, 0), "", 0); len(rows) != 0 {
		t.Errorf("a test that did not run was stored: %+v", rows)
	}
}

func TestPathSpeedMapsAgentErrorsToShortCodes(t *testing.T) {
	for agentError, want := range map[string]string{"busy": "busy", "refused": "unreachable", "timeout": "timeout", "no_route": "no_route",
		"unsupported_host": "no_route", "failed: dial tcp 203.0.113.10:1: nope": "failed", "anything": "failed"} {
		if got := speedCommandError(&agentv1.CommandResult{Error: agentError}); got != want {
			t.Errorf("%q -> %q, want %q", agentError, got, want)
		}
	}
	if speedCommandError(nil) != "failed" {
		t.Error("nil is not failed")
	}
	// numbers that make no sense are a failed test, never a figure
	for name, p := range map[string]map[string]string{
		"empty":     {},
		"zero":      {"mbit_s": "0", "bytes": "1048576", "duration_ms": "10"},
		"nan":       {"mbit_s": "NaN", "bytes": "1048576", "duration_ms": "10"},
		"absurd":    {"mbit_s": "9999999", "bytes": "1048576", "duration_ms": "10"},
		"too many":  {"mbit_s": "100", "bytes": strconv.Itoa(9 << 20), "duration_ms": "10"},
		"no bytes":  {"mbit_s": "100", "bytes": "0", "duration_ms": "10"},
		"no length": {"mbit_s": "100", "bytes": "1048576", "duration_ms": "x"},
	} {
		if got := parseSpeedResult(p, 8); got.Error != "failed" || got.MbitS != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// the figures of an honest answer, with a runaway timing held to an hour
	got := parseSpeedResult(map[string]string{"mbit_s": "93.04", "bytes": strconv.Itoa(8 << 20), "duration_ms": "720", "connect_ms": "5", "first_byte_ms": "4000000"}, 8)
	if got.Error != "" || got.MbitS != 93 || got.FirstByteMS != 3_600_000 {
		t.Errorf("honest answer: %+v", got)
	}
}

// An agent without speed/1 is sent nothing, whichever end it is; the panel's request layer names the capability too.
func TestPathSpeedRefusesAnOldAgentOnEitherEndAndSendsItNothing(t *testing.T) {
	x, _ := newL3Env(t)
	modern, modernConn := addPortCheckPeer(x, "modern", "provider-a", "TR", capSpeed)
	old, oldConn := addPortCheckPeer(x, "old", "provider-b", "DE", capProbe, capUDPCheck)
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, modern.nodeID)
	for _, test := range []struct {
		name     string
		from, to string
	}{{"old sender", old.nodeID, modern.nodeID}, {"old target", modern.nodeID, old.nodeID}} {
		got := awaitSpeed(t, startPathSpeed(x, test.from, test.to, 16, "down"))
		if got.Error != "agent_too_old" || got.MbitS != 0 {
			t.Fatalf("%s: %+v", test.name, got)
		}
	}
	for _, c := range []*conn{oldConn, modernConn} {
		timer := time.After(200 * time.Millisecond)
	wait:
		for {
			select {
			case m := <-c.in:
				if m.GetSpeedTest() != nil {
					t.Fatalf("an agent was sent a speed command that one end cannot take: %v", m)
				}
			case <-timer:
				break wait
			}
		}
	}
	if capability, _ := requestCapability(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SpeedTest{SpeedTest: &agentv1.UdpSend{}}}, false); capability != capSpeed {
		t.Errorf("a speed command needs %q, got %q", capSpeed, capability)
	}
	if kind := requestKind(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SpeedTest{SpeedTest: &agentv1.UdpSend{}}}); kind != PendingCommand {
		t.Errorf("a speed command is %v, want a pending command", kind)
	}
	if rows, _ := x.st.PathSpeeds(x.ctx, time.Unix(0, 0), "", 0); len(rows) != 0 {
		t.Errorf("a refused test was stored: %+v", rows)
	}
}

func TestPathSpeedRefusesWhatIsOutsideTheCaps(t *testing.T) {
	x, sender, target, _, targetConn := speedPair(t)
	for name, req := range map[string]*probev1.PathSpeedRequest{
		"too small":   {FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Mb: 7, Direction: "down"},
		"too big":     {FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Mb: 33, Direction: "down"},
		"no size":     {FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Direction: "down"},
		"a direction": {FromNodeId: sender.nodeID, ToNodeId: target.nodeID, Mb: 16, Direction: "both"},
		"same node":   {FromNodeId: sender.nodeID, ToNodeId: sender.nodeID, Mb: 16, Direction: "down"},
		"unknown":     {FromNodeId: sender.nodeID, ToNodeId: "nod_missing", Mb: 16, Direction: "down"},
		"a host":      {FromNodeId: sender.nodeID, ToNodeId: "203.0.113.5", Mb: 16, Direction: "down"},
	} {
		if _, err := (probeService{x.f}).PathSpeed(x.ctx, connect.NewRequest(req)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	select {
	case m := <-targetConn.in:
		if m.GetSpeedTest() != nil {
			t.Fatalf("a refused request reached an agent: %v", m)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSpeedLimitsOnePerNodeAndADayPerNodeAndPerCaller(t *testing.T) {
	x, _, _, _, _ := speedPair(t)
	actor := "token-a"
	x.f.cfg.Actor = func(context.Context) string { return actor }
	clock := x.clock

	// one test per node at a time, whichever end it is
	release, err := x.f.startSpeed(x.ctx, "n1", "n2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.f.startSpeed(x.ctx, "n3", "n1"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a second test on a busy node: %v", err)
	}
	if _, err := x.f.startSpeed(x.ctx, "n2", "n4"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a second test on a busy node (as sender): %v", err)
	}
	if rel, err := x.f.startSpeed(x.ctx, "n5", "n6"); err != nil { // other nodes are not affected
		t.Fatalf("an unrelated pair: %v", err)
	} else {
		rel()
	}
	release()

	// a node is the end of at most speedNodeDayMax tests in a day (one is used above)
	for i := 0; i < speedNodeDayMax-1; i++ {
		rel, err := x.f.startSpeed(x.ctx, "n1", "peer"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("test %d: %v", i, err)
		}
		rel()
	}
	_, err = x.f.startSpeed(x.ctx, "n1", "fresh")
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "try again in") {
		t.Fatalf("the node limit: %v", err)
	}
	clock.add(speedDayWindow + time.Second)
	if rel, err := x.f.startSpeed(x.ctx, "n1", "fresh"); err != nil {
		t.Fatalf("the day did not roll: %v", err)
	} else {
		rel()
	}

	// a caller may start speedCallerDayMax tests a day on any nodes; another caller has its own
	clock.add(speedDayWindow + time.Second)
	for i := 0; i < speedCallerDayMax; i++ {
		rel, err := x.f.startSpeed(x.ctx, "a"+strconv.Itoa(i), "b"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("test %d of the caller: %v", i, err)
		}
		rel()
	}
	if _, err := x.f.startSpeed(x.ctx, "z1", "z2"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("the caller limit: %v", err)
	}
	actor = "token-b"
	rel, err := x.f.startSpeed(x.ctx, "z1", "z2")
	if err != nil {
		t.Fatalf("another caller shares the limit: %v", err)
	}
	rel()
}

// The caps the agent is told are the ones the panel holds itself to.
func TestSpeedCapsAreTheDocumentedOnes(t *testing.T) {
	if speedMinMB != 8 || speedMaxMB != 32 || speedMaxS != 30 {
		t.Errorf("caps are %d to %d MB and %d s, want 8 to 32 and 30", speedMinMB, speedMaxMB, speedMaxS)
	}
	if speedRunWait < speedMaxS*time.Second {
		t.Errorf("the panel stops waiting (%v) before the agent's own 30 s limit", speedRunWait)
	}
}
