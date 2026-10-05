package fleet

import (
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The panel side of the bandwidth test (agent.proto "BANDWIDTH TEST"): the RPC, the capability gate (an old agent is never
// sent the command and the panel never waits for an answer that cannot come), that the RPC never writes the capacity, and the
// one automatic write: the first start of a node whose capacity is still 0.

var bwCaps = []string{"doctor/1", "bandwidth/1"}

func measured(down, up string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Ok: true, Params: map[string]string{"down_mbps": down, "up_mbps": up, "server": "speed.cloudflare.com", "seconds": "29", "runs": "3", "down_people_mbps": "35", "up_people_mbps": "0"}}
}

// answerMeasure plays the agent: it answers the next MeasureBandwidth (after `after` is closed, when given) with res.
func answerMeasure(c *conn, res *agentv1.CommandResult, after <-chan struct{}) <-chan *agentv1.MeasureBandwidth {
	got := make(chan *agentv1.MeasureBandwidth, 1)
	go func() {
		// c.wait fails the test from this goroutine, which only ends the goroutine: closing the channel makes the test's
		// receive return nil instead of blocking until the 10-minute test timeout
		defer close(got)
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetMeasureBandwidth() != nil })
		p := m.GetMeasureBandwidth()
		got <- p
		if after != nil {
			<-after
		}
		r := proto.Clone(res).(*agentv1.CommandResult)
		r.RequestId = p.RequestId
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: r}})
	}()
	return got
}

// neverAsked fails if a MeasureBandwidth reaches the stream within d.
func neverAsked(c *conn, d time.Duration) {
	c.t.Helper()
	t := time.After(d)
	for {
		select {
		case m := <-c.in:
			if m.GetMeasureBandwidth() != nil {
				c.t.Fatalf("the node was asked to measure: %v", m.GetMeasureBandwidth())
			}
		case <-t:
			return
		}
	}
}

func callMeasure(x *l3Env, nodeID string) (*adminv1.MeasureBandwidthResponse, error) {
	r, err := (nodeService{x.f}).MeasureBandwidth(x.ctx, connect.NewRequest(&adminv1.MeasureBandwidthRequest{NodeId: nodeID}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (x *l3Env) capacity() int {
	x.t.Helper()
	n, err := x.st.Node(x.ctx, x.nodeIDOf())
	if err != nil {
		x.t.Fatal(err)
	}
	return n.BandwidthMbps
}

func TestMeasureBandwidthNeedsAConnectedAgentThatListsTheCapability(t *testing.T) {
	x, a := newL3Env(t)
	if _, err := callMeasure(x, "nod_nope"); code(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
	if _, err := callMeasure(x, a.nodeID); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_offline" {
		t.Errorf("offline: %v", err)
	}
	c, _, _ := connectCaps(a, "old", "doctor/1", "awg/1") // an agent from before this feature
	if _, err := callMeasure(x, a.nodeID); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Errorf("an agent without bandwidth/1: %v", err)
	}
	neverAsked(c, 150*time.Millisecond) // and nothing was sent to it, so nothing is waited for
	if x.count(`SELECT count(*) FROM audit WHERE action = 'node.bandwidth_measure'`) != 0 {
		t.Error("a refused request left an audit row")
	}
}

func TestMeasureBandwidthReturnsTheResultAndNeverWritesTheCapacity(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = time.Hour // the automatic first measurement is another test
	c, _, _ := connectCaps(a, "new", bwCaps...)
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{BandwidthMbps: new(500)}); err != nil {
		t.Fatal(err)
	}
	asked := answerMeasure(c, measured("940", "871"), nil)
	r, err := callMeasure(x, a.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if p := <-asked; p == nil || p.RequestId == "" {
		t.Error("the request carried no id")
	}
	if r.ErrorCode != "" || r.DownMbps != 940 || r.UpMbps != 871 || r.Server != "speed.cloudflare.com" || r.Seconds != 29 || r.Runs != 3 || r.PeopleDownMbps != 35 || r.PeopleUpMbps != 0 {
		t.Errorf("response = %v", r)
	}
	if got := x.capacity(); got != 500 {
		t.Errorf("the capacity the admin typed became %d: the RPC must only return the result", got)
	}
	if n := x.count(`SELECT count(*) FROM audit WHERE action = 'node.bandwidth_measure' AND actor = 'adm_test' AND result = 'ok'`); n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
	if n := x.count(`SELECT count(*) FROM event WHERE code = 'bandwidth_measured'`); n != 0 {
		t.Error("a measurement the admin asked for is not an automatic one: no event")
	}
}

// How the runs went: new fields are passed on (cleaned), and an agent that does not send them reads as "all runs worked".
func TestMeasureBandwidthSaysHowManyRunsWorkedAndWhy(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = time.Hour
	c, _, _ := connectCaps(a, "new", bwCaps...)
	ask := func(mut func(p map[string]string)) *adminv1.MeasureBandwidthResponse {
		t.Helper()
		res := measured("525", "160")
		mut(res.Params)
		answerMeasure(c, res, nil)
		r, err := callMeasure(x, a.nodeID)
		if err != nil || r.ErrorCode != "" {
			t.Fatalf("%v %v", r, err)
		}
		return r
	}

	r := ask(func(p map[string]string) {
		p["runs"], p["runs_total"], p["run_failures"], p["server"], p["server_detail"] = "1", "3", "rate_limited,http_503", "Ookla", "МТС, Москва"
	})
	if r.Runs != 1 || r.RunsTotal != 3 || !slices.Equal(r.RunFailures, []string{"rate_limited", "http_503"}) || r.Server != "Ookla" || r.ServerDetail != "МТС, Москва" {
		t.Errorf("response = %v", r)
	}

	// an agent from before these fields: runs only
	r = ask(func(p map[string]string) {
		p["runs"], p["runs_total"] = "2", ""
		delete(p, "run_failures")
		delete(p, "server_detail")
	})
	if r.Runs != 2 || r.RunsTotal != 2 || len(r.RunFailures) != 0 || r.ServerDetail != "" {
		t.Errorf("an old agent: %v", r)
	}

	// what the agent writes is held to the known words: no text of its own reaches the UI, no more reasons than failed runs
	r = ask(func(p map[string]string) {
		p["runs"], p["runs_total"], p["run_failures"] = "1", "3", "<script>,http_99999"
	})
	if !slices.Equal(r.RunFailures, []string{"failed", "failed"}) {
		t.Errorf("unknown reasons: %v", r.RunFailures)
	}
	r = ask(func(p map[string]string) {
		p["runs"], p["runs_total"], p["run_failures"] = "2", "3", "timeout,unreachable,rate_limited"
	})
	if !slices.Equal(r.RunFailures, []string{"timeout"}) {
		t.Errorf("more reasons than failed runs: %v", r.RunFailures)
	}
	// a total below the runs that worked is not believed
	if r = ask(func(p map[string]string) { p["runs"], p["runs_total"], p["run_failures"] = "3", "1", "timeout" }); r.RunsTotal != 3 || len(r.RunFailures) != 0 {
		t.Errorf("total below runs: %v", r)
	}
}

func TestMeasureBandwidthSaysWhatTheNodeAnsweredAsACode(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = time.Hour
	c, _, _ := connectCaps(a, "new", bwCaps...)
	for name, tc := range map[string]struct {
		res  *agentv1.CommandResult
		want string
	}{
		"busy":              {&agentv1.CommandResult{Error: "busy"}, "busy"},
		"nothing reachable": {&agentv1.CommandResult{Error: "unreachable"}, "unreachable"},
		"unsupported":       {&agentv1.CommandResult{Error: "unsupported_host"}, "unsupported"},
		"anything else":     {&agentv1.CommandResult{Error: "failed: dial tcp 10.0.0.1:443: boom"}, "failed"},
		"no throughput":     {measured("0", "0"), "failed"},
		"not a number":      {measured("fast", "0"), "failed"},
	} {
		answerMeasure(c, tc.res, nil)
		r, err := callMeasure(x, a.nodeID)
		if err != nil || r.ErrorCode != tc.want || r.DownMbps != 0 || r.UpMbps != 0 {
			t.Errorf("%s: %v %v, want code %q", name, r, err, tc.want)
		}
	}
	if x.count(`SELECT count(*) FROM audit WHERE action = 'node.bandwidth_measure'`) != 0 {
		t.Error("a measurement that did not work left an audit row")
	}
}

func TestMeasureBandwidthHoldsANodesNumbersToTheLimit(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = time.Hour
	c, _, _ := connectCaps(a, "new", bwCaps...)
	res := measured("99999999", "-5")
	res.Params["down_people_mbps"], res.Params["up_people_mbps"] = "999999999", "7"
	answerMeasure(c, res, nil)
	r, err := callMeasure(x, a.nodeID)
	if err != nil || r.DownMbps != 1_000_000 || r.UpMbps != 0 || r.PeopleDownMbps != 1_000_000 || r.PeopleUpMbps != 0 {
		t.Errorf("%v %v: an agent's number must not exceed what UpdateNode accepts, nor be negative, and the people's share is a part of the figure, never more", r, err)
	}
}

func TestMeasureBandwidthEndsWhenTheNodeDoesNotAnswer(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = time.Hour
	x.f.measureWait = 100 * time.Millisecond
	connectCaps(a, "new", bwCaps...) // never answers
	if _, err := callMeasure(x, a.nodeID); code(err) != connect.CodeDeadlineExceeded {
		t.Errorf("a silent node: %v", err)
	}
}

// The first start of an enrolled node: measured once, stored only while the capacity is 0.
func TestTheFirstStartMeasuresTheLinkOnceAndStoresItWhileItIsZero(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = 10 * time.Millisecond
	c, _, _ := connectCaps(a, "first", bwCaps...)
	answerMeasure(c, measured("940", "871"), nil)
	within(t, "the capacity", func() bool { return x.capacity() == 871 }) // the slower direction
	within(t, "the event", func() bool { return x.count(`SELECT count(*) FROM event WHERE code = 'bandwidth_measured'`) == 1 })
	if n := x.count(`SELECT count(*) FROM audit WHERE action = 'node.bandwidth_auto' AND actor = 'system'`); n != 1 {
		t.Errorf("%d automatic audit rows, want 1", n)
	}

	// a reconnect of an active node is not a first start: nobody asks again, even with the capacity back at 0
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{BandwidthMbps: new(0)}); err != nil {
		t.Fatal(err)
	}
	c2, _, _ := connectCaps(a, "second", bwCaps...)
	neverAsked(c2, 200*time.Millisecond)
	if got := x.capacity(); got != 0 {
		t.Errorf("a reconnect measured again: %d", got)
	}
}

func TestTheFirstStartNeverOverwritesWhatTheAdminTypedMeanwhile(t *testing.T) {
	x, a := newL3Env(t)
	x.f.measureDelay = 10 * time.Millisecond
	c, _, _ := connectCaps(a, "first", bwCaps...)
	release := make(chan struct{})
	asked := answerMeasure(c, measured("940", "871"), release)
	if <-asked == nil { // the node is measuring
		t.Fatal("the node was never asked to measure")
	}
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{BandwidthMbps: new(300)}); err != nil {
		t.Fatal(err)
	}
	close(release)
	time.Sleep(300 * time.Millisecond)
	if got := x.capacity(); got != 300 {
		t.Errorf("capacity = %d, the typed 300 must stay", got)
	}
	if x.count(`SELECT count(*) FROM event WHERE code = 'bandwidth_measured'`) != 0 {
		t.Error("an event for a value that was not stored")
	}
}

func TestTheFirstStartSkipsWhatCannotOrNeedNotBeMeasured(t *testing.T) {
	t.Run("an old agent, silently", func(t *testing.T) {
		x, a := newL3Env(t)
		x.f.measureDelay = 10 * time.Millisecond
		c, _, _ := connectCaps(a, "old", "doctor/1", "awg/1")
		neverAsked(c, 200*time.Millisecond)
		if x.capacity() != 0 || x.count(`SELECT count(*) FROM event WHERE code = 'bandwidth_measured'`) != 0 {
			t.Error("something was recorded for an agent that cannot measure")
		}
	})
	t.Run("a node whose capacity is known", func(t *testing.T) {
		x, a := newL3Env(t)
		x.f.measureDelay = 10 * time.Millisecond
		if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{BandwidthMbps: new(1000)}); err != nil {
			t.Fatal(err)
		}
		c, _, _ := connectCaps(a, "new", bwCaps...)
		neverAsked(c, 200*time.Millisecond)
		if x.capacity() != 1000 {
			t.Error("the capacity changed")
		}
	})
	t.Run("a measurement that failed leaves the capacity alone and does not retry", func(t *testing.T) {
		x, a := newL3Env(t)
		x.f.measureDelay = 10 * time.Millisecond
		c, _, _ := connectCaps(a, "new", bwCaps...)
		asked := answerMeasure(c, &agentv1.CommandResult{Error: "unreachable"}, nil)
		<-asked
		neverAsked(c, 300*time.Millisecond)
		if x.capacity() != 0 || x.count(`SELECT count(*) FROM event WHERE code = 'bandwidth_measured'`) != 0 {
			t.Error("a failed measurement was recorded")
		}
	})
	t.Run("a node that drops before the wait is over is not asked", func(t *testing.T) {
		x, a := newL3Env(t)
		x.f.measureDelay = 300 * time.Millisecond
		c, _, _ := connectCaps(a, "new", bwCaps...)
		c.st.CloseRequest()
		c.st.CloseResponse()
		time.Sleep(500 * time.Millisecond)
		if x.capacity() != 0 {
			t.Error("the capacity changed")
		}
	})
}
