//go:build !js

package fleet

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/prototext"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

// The channel measure against fake agents: which nodes it picks, the order and the content of the commands on both ends, what is
// stored, the pause, the exclusions, a cancel, and that nothing but an explicit call starts one.

type channelEnv struct {
	x                *l3Env
	node             *agent
	nodeConn         *conn
	partners         map[string]*agent // name -> agent
	partnerConns     map[string]*conn
	partnerAddresses map[string]string
}

// newChannelEnv has a node "sender" and the given partners (name -> the Mbit/s the owner wrote for it), all with speed-channel/1.
// The partners get addresses of 203.0.113.0/24.
func newChannelEnv(t *testing.T, partners map[string]int, nodeCaps ...string) *channelEnv {
	t.Helper()
	x, _ := newL3Env(t) // "nodea" stays unconnected: never a partner
	e := &channelEnv{x: x, partners: map[string]*agent{}, partnerConns: map[string]*conn{}, partnerAddresses: map[string]string{}}
	if nodeCaps == nil {
		nodeCaps = []string{capSpeed, capSpeedChannel}
	}
	e.node, e.nodeConn = addPortCheckPeer(x, "sender", "provider-b", "DE", nodeCaps...)
	x.exec(`UPDATE node SET address = '203.0.113.2' WHERE id = ?`, e.node.nodeID)
	i := 10
	for name, mbps := range partners {
		p, c := addPortCheckPeer(x, name, "provider-c", "NL", capSpeed, capSpeedChannel)
		addr := "203.0.113." + strconv.Itoa(i)
		i++
		x.exec(`UPDATE node SET address = ?, bandwidth_mbps = ? WHERE id = ?`, addr, mbps, p.nodeID)
		e.partners[name], e.partnerConns[name], e.partnerAddresses[name] = p, c, addr
	}
	return e
}

func (e *channelEnv) start(t *testing.T) *probev1.ChannelMeasureStatusResponse {
	t.Helper()
	r, err := (probeService{e.x.f}).ChannelMeasureStart(e.x.ctx, connect.NewRequest(&probev1.ChannelMeasureStartRequest{NodeId: e.node.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg.Status
}

func (e *channelEnv) status(t *testing.T) *probev1.ChannelMeasureStatusResponse {
	t.Helper()
	r, err := (probeService{e.x.f}).ChannelMeasureStatus(e.x.ctx, connect.NewRequest(&probev1.ChannelMeasureStatusRequest{NodeId: e.node.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg
}

func (e *channelEnv) plan(t *testing.T) *probev1.ChannelMeasurePlanResponse {
	t.Helper()
	r, err := (probeService{e.x.f}).ChannelMeasurePlan(e.x.ctx, connect.NewRequest(&probev1.ChannelMeasurePlanRequest{NodeId: e.node.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg
}

// finished waits until the run is no longer running.
func (e *channelEnv) finished(t *testing.T) *probev1.ChannelMeasureStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st := e.status(t); st.State != "running" {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatal("the channel measure did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// channelReply is what a sender answers for one step.
func channelReply(mbit string, bytesMoved int64, ms int) *agentv1.CommandResult {
	return &agentv1.CommandResult{Ok: true, Params: map[string]string{"mbit_s": mbit, "connect_ms": "4", "first_byte_ms": "9",
		"duration_ms": strconv.Itoa(ms), "bytes": strconv.FormatInt(bytesMoved, 10), "streams": "8", "capped": "0"}}
}

// step drives one direction of one partner: serve on the partner, run on the node, stop on the partner. It checks what each
// command carries and returns the serve and run messages.
func (e *channelEnv) step(t *testing.T, partner string, up bool, reply *agentv1.CommandResult) (serve, run *agentv1.UdpSend) {
	t.Helper()
	pc := e.partnerConns[partner]
	serve = waitSpeed(pc, speedStepServeChannel)
	if len(serve.Tag) != 16 || serve.Count != channelMaxMB || serve.Pps != channelServeS || serve.Df != up || serve.Host != "" || len(serve.Ports) != 0 {
		t.Fatalf("serve = %+v", serve)
	}
	replyPortCommand(pc, serve.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"port": "40123"}})
	run = waitSpeed(e.nodeConn, speedStepRunChannel)
	if run.Host != e.partnerAddresses[partner] || len(run.Ports) != 1 || run.Ports[0] != 40123 || !bytes.Equal(run.Tag, serve.Tag) ||
		run.Count != channelMaxMB || run.Pps != channelRunS || run.Df != up {
		t.Fatalf("run = %+v", run)
	}
	replyPortCommand(e.nodeConn, run.RequestId, reply)
	stop := waitSpeed(pc, 3)
	if !bytes.Equal(stop.Tag, serve.Tag) {
		t.Fatalf("stop carries another token: %+v", stop)
	}
	replyPortCommand(pc, stop.RequestId, &agentv1.CommandResult{Ok: true})
	return serve, run
}

func TestChannelMeasureMeasuresTheWidestPartnersBothWaysAndKeepsTheBest(t *testing.T) {
	e := newChannelEnv(t, map[string]int{"de1": 1000, "nl1": 500, "se1": 2000, "fi1": 100})
	plan := e.plan(t)
	if plan.Error != "" || len(plan.Partners) != 3 || plan.Partners[0].Name != "se1" || plan.Partners[1].Name != "de1" || plan.Partners[2].Name != "nl1" {
		t.Fatalf("plan = %+v, want the three widest: se1, de1, nl1", plan)
	}
	if plan.MaxMb != 2048 || plan.MaxGb != 12 || plan.EstSeconds != 72 {
		t.Errorf("plan figures: max %d MiB, %d GB, %d s", plan.MaxMb, plan.MaxGb, plan.EstSeconds)
	}
	st := e.start(t)
	if st.State != "running" || len(st.Steps) != 6 || st.Error != "" {
		t.Fatalf("start = %+v", st)
	}
	figures := map[string][2]string{"se1": {"700.0", "300.0"}, "de1": {"900.0", "250.0"}, "nl1": {"400.0", "600.0"}}
	for _, p := range []string{"se1", "de1", "nl1"} {
		for i, up := range []bool{false, true} {
			f := figures[p][i]
			mbit, _ := strconv.ParseFloat(f, 64)
			e.step(t, p, up, channelReply(f, int64(mbit*1e6/8*10), 10000))
		}
	}
	got := e.finished(t)
	if got.State != "done" || got.Error != "" || got.Result == nil {
		t.Fatalf("finished = %+v", got)
	}
	if got.Result.Down.Partner != "de1" || got.Result.Down.MbitS != 900 || got.Result.Up.Partner != "nl1" || got.Result.Up.MbitS != 600 || got.Result.Partners != 3 {
		t.Errorf("result = %+v, want down 900 from de1 and up 600 from nl1", got.Result)
	}
	for i, s := range got.Steps {
		if s.State != "done" || s.MbitS <= 0 || s.Streams != 8 {
			t.Errorf("step %d = %+v", i, s)
		}
	}
	if got.Last == nil || got.Last.Down.MbitS != 900 {
		t.Errorf("the stored last = %+v", got.Last)
	}
	noAddress(t, "the status", prototext.Format(got))

	// stored as path speed tests, 7 days: the capacity report reads them as it reads a path test
	rows, err := e.x.st.PathSpeeds(e.x.ctx, time.Unix(0, 0), e.node.nodeID, 0)
	if err != nil || len(rows) != 6 {
		t.Fatalf("stored tests = %d, %v", len(rows), err)
	}
	for _, r := range rows {
		noAddress(t, "a stored result", r.ResultJSON)
		if !strings.Contains(r.ResultJSON, `"streams":8`) {
			t.Errorf("a stored channel test does not say its streams: %s", r.ResultJSON)
		}
	}
	nodes, err := e.x.f.CapacityNodes(e.x.ctx, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Name == "sender" && n.MeasuredMbps != 900 {
			t.Errorf("the capacity report reads the node's measured speed as %d, want 900", n.MeasuredMbps)
		}
	}
	// the hours of every node that took part are the measure's, not people's
	for _, id := range []string{e.node.nodeID, e.partners["se1"].nodeID, e.partners["de1"].nodeID, e.partners["nl1"].nodeID} {
		if at, err := e.x.st.SpeedTestTimes(e.x.ctx, id, time.Unix(0, 0)); err != nil || len(at) == 0 {
			t.Errorf("speed test times of %s = %v, %v", id, at, err)
		}
	}
	if at, _ := e.x.st.SpeedTestTimes(e.x.ctx, e.partners["fi1"].nodeID, time.Unix(0, 0)); len(at) != 0 {
		t.Error("a node that was not used has its hours marked")
	}
	var params string
	if err := e.x.st.R.QueryRowContext(e.x.ctx, `SELECT params FROM audit WHERE action = 'node.channel_measure'`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	noAddress(t, "the audit row", params)
	if !strings.Contains(params, `"node":"sender"`) || !strings.Contains(params, "se1,de1,nl1") {
		t.Errorf("audit row = %s, want node names", params)
	}
	// and the pause: one more is refused for ten minutes, with the time left
	again := e.plan(t)
	if again.Error != "too_soon" || again.RetryAfterS == 0 || again.RetryAfterS > 600 {
		t.Errorf("plan right after = %+v, want too_soon", again)
	}
	if st := e.start(t); st.Error != "too_soon" || st.RetryAfterS == 0 || st.State == "running" {
		t.Errorf("start right after = %+v, want too_soon", st)
	}
}

func TestChannelMeasurePauseEndsAfterTenMinutesAndSurvivesARestart(t *testing.T) {
	e := newChannelEnv(t, map[string]int{"de1": 1000})
	e.start(t)
	e.step(t, "de1", false, channelReply("100.0", 125_000_000, 10000))
	e.step(t, "de1", true, channelReply("100.0", 125_000_000, 10000))
	e.finished(t)
	// time passes: the start of the measure moves back in the database
	startedAgo := func(d time.Duration) {
		e.x.exec(`UPDATE node_channel_measure SET started_unix = ? WHERE node_id = ?`, time.Now().Add(-d).Unix(), e.node.nodeID)
	}
	startedAgo(9 * time.Minute)
	if p := e.plan(t); p.Error != "too_soon" || p.RetryAfterS == 0 || p.RetryAfterS > 61 {
		t.Fatalf("after 9 minutes: %+v", p)
	}
	// a restart of the panel: the run in memory is gone, the pause is in the database
	e.x.f.probes.mu.Lock()
	e.x.f.probes.channel.runs = nil
	e.x.f.probes.mu.Unlock()
	if p := e.plan(t); p.Error != "too_soon" {
		t.Fatalf("after a restart the pause is gone: %+v", p)
	}
	startedAgo(11 * time.Minute)
	if p := e.plan(t); p.Error != "" || p.Last == nil {
		t.Fatalf("after 11 minutes: %+v", p)
	}
}

func TestChannelMeasureRefusesWhatCannotRun(t *testing.T) {
	t.Run("no partner", func(t *testing.T) {
		e := newChannelEnv(t, nil)
		if p := e.plan(t); p.Error != "no_partner" {
			t.Fatalf("plan = %+v, want no_partner", p)
		}
		if st := e.start(t); st.Error != "no_partner" || st.State != "idle" {
			t.Fatalf("start = %+v", st)
		}
	})
	t.Run("an old agent on the node", func(t *testing.T) {
		e := newChannelEnv(t, map[string]int{"de1": 1000}, capSpeed) // speed/1 only
		if p := e.plan(t); p.Error != "agent_too_old" {
			t.Fatalf("plan = %+v, want agent_too_old", p)
		}
	})
	t.Run("a partner with an old agent is not used", func(t *testing.T) {
		e := newChannelEnv(t, map[string]int{"de1": 1000})
		old, _ := addPortCheckPeer(e.x, "old1", "provider-c", "NL", capSpeed)
		e.x.exec(`UPDATE node SET bandwidth_mbps = 9000, address = '203.0.113.99' WHERE id = ?`, old.nodeID)
		p := e.plan(t)
		if p.Error != "" || len(p.Partners) != 1 || p.Partners[0].Name != "de1" {
			t.Fatalf("plan = %+v, want only de1", p)
		}
	})
	t.Run("a retired or disconnected partner is not used", func(t *testing.T) {
		e := newChannelEnv(t, map[string]int{"de1": 1000, "nl1": 500})
		e.x.exec(`UPDATE node SET state = 'retired' WHERE id = ?`, e.partners["de1"].nodeID)
		e.partnerConns["nl1"].st.CloseRequest()
		deadline := time.Now().Add(5 * time.Second)
		for {
			p := e.plan(t)
			if p.Error == "no_partner" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("plan = %+v, want no_partner", p)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	t.Run("the node itself is never its own partner", func(t *testing.T) {
		e := newChannelEnv(t, map[string]int{"de1": 1000})
		e.x.exec(`UPDATE node SET bandwidth_mbps = 99999 WHERE id = ?`, e.node.nodeID)
		for _, c := range e.plan(t).Partners {
			if c.Name == "sender" {
				t.Fatal("the node is its own partner")
			}
		}
	})
	t.Run("a partner that runs a test is left out, and a node that runs one is busy", func(t *testing.T) {
		e := newChannelEnv(t, map[string]int{"de1": 1000, "nl1": 500})
		e.x.f.probes.speedBusy = map[string]struct{}{e.partners["de1"].nodeID: {}}
		if p := e.plan(t); len(p.Partners) != 1 || p.Partners[0].Name != "nl1" {
			t.Fatalf("plan = %+v, want only nl1", p)
		}
		e.x.f.probes.speedBusy[e.node.nodeID] = struct{}{}
		if p := e.plan(t); p.Error != "busy" {
			t.Fatalf("plan = %+v, want busy", p)
		}
	})
	t.Run("a known channel ranks, an unknown one comes last", func(t *testing.T) {
		e := newChannelEnv(t, map[string]int{"de1": 0, "nl1": 300, "se1": 0})
		// a kept path test is as good as a number the owner wrote
		raw := `{"mb":24,"mbit_s":800,"bytes":25165824,"duration_ms":250}`
		if err := e.x.st.AddPathSpeed(e.x.ctx, e.node.nodeID, e.partners["se1"].nodeID, "down", time.Now(), raw); err != nil {
			t.Fatal(err)
		}
		p := e.plan(t)
		if len(p.Partners) != 3 || p.Partners[0].Name != "se1" || p.Partners[0].KnownMbps != 800 || p.Partners[1].Name != "nl1" || p.Partners[2].KnownMbps != 0 {
			t.Fatalf("plan = %+v", p)
		}
	})
}

func TestChannelMeasureOnlyOneAtATimePerNode(t *testing.T) {
	e := newChannelEnv(t, map[string]int{"de1": 1000})
	e.start(t)
	serve := waitSpeed(e.partnerConns["de1"], speedStepServeChannel)
	if st := e.start(t); st.Error != "busy" {
		t.Fatalf("a second start while one runs = %+v, want busy", st)
	}
	// the nodes are taken for a path test too
	_, err := (probeService{e.x.f}).PathSpeed(e.x.ctx, connect.NewRequest(&probev1.PathSpeedRequest{FromNodeId: e.node.nodeID, ToNodeId: e.partners["de1"].nodeID, Mb: 8, Direction: "down"}))
	if err == nil || connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a path test during a channel measure: %v, want resource exhausted", err)
	}
	// let it end: the partner says it is busy, and then the other direction of that partner is not tried
	replyPortCommand(e.partnerConns["de1"], serve.RequestId, &agentv1.CommandResult{Error: "busy"})
	if got := e.finished(t); got.State != "failed" || got.Error != "busy" || got.Steps[1].Error != "busy" {
		t.Fatalf("finished = %+v", got)
	}
	// and the nodes are free again
	e.x.f.probes.mu.Lock()
	busy := len(e.x.f.probes.speedBusy)
	active := e.x.f.probes.active
	e.x.f.probes.mu.Unlock()
	if busy != 0 || active != 0 {
		t.Errorf("after the run: %d nodes still taken, %d probes active", busy, active)
	}
}

func TestChannelMeasureFailureCodesAndNothingStoredWhenNothingWorked(t *testing.T) {
	e := newChannelEnv(t, map[string]int{"de1": 1000})
	e.start(t)
	// the sender cannot connect to the partner's port: the other direction of that partner is not tried
	e.step(t, "de1", false, &agentv1.CommandResult{Error: "refused"})
	got := e.finished(t)
	if got.State != "failed" || got.Error != "unreachable" || got.Result != nil && got.Result.Down.MbitS != 0 {
		t.Fatalf("finished = %+v, want failed/unreachable", got)
	}
	if got.Steps[0].State != "failed" || got.Steps[0].Error != "unreachable" || got.Steps[1].State != "failed" || got.Steps[1].Error != "unreachable" {
		t.Errorf("steps = %+v", got.Steps)
	}
	if got.Last != nil {
		t.Errorf("a failed run stored %+v", got.Last)
	}
	if rows, _ := e.x.st.PathSpeeds(e.x.ctx, time.Unix(0, 0), e.node.nodeID, 0); len(rows) != 0 {
		t.Errorf("a failed run stored %d tests", len(rows))
	}
	// the pause counts a failed run too
	if p := e.plan(t); p.Error != "too_soon" {
		t.Errorf("plan after a failed run = %+v, want too_soon", p)
	}
}

func TestChannelMeasureKeepsTheOneDirectionThatWorked(t *testing.T) {
	e := newChannelEnv(t, map[string]int{"de1": 1000})
	e.start(t)
	e.step(t, "de1", false, channelReply("640.0", 800_000_000, 10000))
	e.step(t, "de1", true, &agentv1.CommandResult{Error: "timeout"})
	got := e.finished(t)
	if got.State != "done" || got.Result.Down.MbitS != 640 || got.Result.Up.MbitS != 0 || got.Steps[1].Error != "timeout" {
		t.Fatalf("finished = %+v", got)
	}
}

func TestChannelMeasureCancelStopsBothEndsAndStoresNoLastResult(t *testing.T) {
	e := newChannelEnv(t, map[string]int{"de1": 1000, "nl1": 500})
	e.start(t)
	pc := e.partnerConns["de1"]
	serve := waitSpeed(pc, speedStepServeChannel)
	replyPortCommand(pc, serve.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"port": "40123"}})
	run := waitSpeed(e.nodeConn, speedStepRunChannel)
	done := make(chan *probev1.ChannelMeasureCancelResponse, 1)
	go func() {
		r, err := (probeService{e.x.f}).ChannelMeasureCancel(e.x.ctx, connect.NewRequest(&probev1.ChannelMeasureCancelRequest{NodeId: e.node.nodeID}))
		if err != nil {
			t.Error(err)
			done <- nil
			return
		}
		done <- r.Msg
	}()
	// both ends are told to stop with the token of the step: the partner's listener and the sender's streams
	stopPartner := waitSpeed(pc, 3)
	if !bytes.Equal(stopPartner.Tag, serve.Tag) {
		t.Fatalf("the partner was told to stop with another token: %+v", stopPartner)
	}
	// the sender is told too, without waiting for the partner's answer
	stopSender := waitSpeed(e.nodeConn, 3)
	if !bytes.Equal(stopSender.Tag, run.Tag) {
		t.Fatalf("the sender was told to stop with another token: %+v", stopSender)
	}
	replyPortCommand(pc, stopPartner.RequestId, &agentv1.CommandResult{Ok: true})
	replyPortCommand(e.nodeConn, stopSender.RequestId, &agentv1.CommandResult{Ok: true})
	var r *probev1.ChannelMeasureCancelResponse
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancel did not return")
	}
	if r == nil || r.Status.State != "canceled" {
		t.Fatalf("cancel = %+v", r)
	}
	for _, s := range r.Status.Steps {
		if s.State == "waiting" || s.State == "running" {
			t.Errorf("a step is still %s after the cancel", s.State)
		}
	}
	if r.Status.Last != nil {
		t.Errorf("a cancelled run stored %+v", r.Status.Last)
	}
	// nothing is sent to the second partner and the nodes are free
	expectNoSpeedCommand(t, e.partnerConns["nl1"], 200*time.Millisecond)
	e.x.f.probes.mu.Lock()
	busy, active := len(e.x.f.probes.speedBusy), e.x.f.probes.active
	e.x.f.probes.mu.Unlock()
	if busy != 0 || active != 0 {
		t.Errorf("after the cancel: %d nodes still taken, %d probes active", busy, active)
	}
}

func TestParseChannelResultHoldsTheAgentToTheCapsAndToItsOwnNumbers(t *testing.T) {
	ok := map[string]string{"mbit_s": "800.0", "bytes": "1000000000", "duration_ms": "10000", "streams": "8", "connect_ms": "3", "first_byte_ms": "5", "capped": "0"}
	if got := parseChannelResult(ok, 2048); got.Error != "" || got.MbitS != 800 || got.Streams != 8 || got.Capped {
		t.Fatalf("honest answer: %+v", got)
	}
	capped := map[string]string{"mbit_s": "8589.9", "bytes": strconv.Itoa(2048 << 20), "duration_ms": "2000", "streams": "8", "capped": "1"}
	if got := parseChannelResult(capped, 2048); got.Error != "" || !got.Capped {
		t.Fatalf("a run that hit the cap: %+v", got)
	}
	mutate := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range ok {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for name, p := range map[string]map[string]string{
		"more than the cap":           mutate("bytes", strconv.Itoa(2049<<20)),
		"no bytes":                    mutate("bytes", "0"),
		"no streams":                  mutate("streams", "0"),
		"nine streams":                mutate("streams", "9"),
		"no time":                     mutate("duration_ms", "0"),
		"a minute and a bit":          mutate("duration_ms", "61000"),
		"a figure its bytes disagree": mutate("mbit_s", "8000.0"),
		"nan":                         mutate("mbit_s", "NaN"),
		"absurd":                      mutate("mbit_s", "99999999"),
		"empty":                       {},
	} {
		if got := parseChannelResult(p, 2048); got.Error != "failed" || got.MbitS != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestChannelMeasureCapsAreTheDocumentedOnes(t *testing.T) {
	if channelMaxMB != 2048 || channelServeS != 20 || channelRunS != 15 || channelPause != 10*time.Minute || channelPartners != 3 {
		t.Errorf("caps: %d MiB, serve %d s, run %d s, pause %v, %d partners", channelMaxMB, channelServeS, channelRunS, channelPause, channelPartners)
	}
	if channelRunWait < channelRunS*time.Second {
		t.Errorf("the panel stops waiting (%v) before the agent's own limit", channelRunWait)
	}
	if channelJobMax < 6*channelRunWait {
		t.Errorf("the job's time limit %v is shorter than its steps", channelJobMax)
	}
}

// Nothing starts a channel measure by itself: the only callers of the start, outside the generated code and tests, are the RPC of
// this file and the owner-approved MCP tool. No schedule, job, health check or relay reads the start.
func TestNothingButTheOwnersCallStartsAChannelMeasure(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := map[string]bool{
		filepath.Join("internal", "panel", "fleet", "channel_measure.go"): true,
		filepath.Join("internal", "panel", "mcp", "tools_channel.go"):     true,
		filepath.Join("internal", "panel", "auth", "policy.go"):           true,
		filepath.Join("internal", "panel", "auth", "policy_tokens.go"):    true,
	}
	var seen []string
	for _, dir := range []string{"internal", "cmd"} {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil || !(bytes.Contains(b, []byte("ChannelMeasureStart")) || bytes.Contains(b, []byte("runChannelJob"))) {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			seen = append(seen, rel)
			if !allowed[rel] {
				t.Errorf("%s refers to the channel measure start: only the owner's call may start one", rel)
			}
			return nil
		})
	}
	if len(seen) == 0 {
		t.Fatal("the scan found nothing: the test looks in the wrong place")
	}
}

func expectNoSpeedCommand(t *testing.T, c *conn, d time.Duration) {
	t.Helper()
	timer := time.After(d)
	for {
		select {
		case m := <-c.in:
			if m.GetSpeedTest() != nil {
				t.Fatalf("a speed command was sent where none was due: %v", m)
			}
		case <-timer:
			return
		}
	}
}
