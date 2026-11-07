//go:build !js

package fleet

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The panel side of the debug capture (agent.proto "DEBUG CAPTURE"): the capability gate, the audit entry, the node's refusals,
// and the intake of the node's report (scrubbed again, bounded, only for a running capture of that node).

var debugCaps = []string{"doctor/1", "debug/1"}

// answerDebug plays the agent: it answers the next DebugCapture frame (start or stop) with res.
func answerDebug(c *conn, res *agentv1.CommandResult) <-chan *agentv1.DebugCapture {
	got := make(chan *agentv1.DebugCapture, 1)
	go func() {
		defer close(got)
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetDebugCapture() != nil })
		p := m.GetDebugCapture()
		got <- p
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
			RequestId: p.RequestId, Ok: res.Ok, Error: res.Error}}})
	}()
	return got
}

func TestDebugCaptureNeedsAnOnlineAgentThatListsTheCapability(t *testing.T) {
	x, a := newL3Env(t)
	in := DebugStart{NodeID: a.nodeID, Level: "debug", DurationS: 60}
	if _, err := x.f.StartDebugCapture(x.ctx, DebugStart{NodeID: "nod_nope"}); code(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
	if _, err := x.f.StartDebugCapture(x.ctx, in); err == nil || err.Error() != "failed_precondition: node_offline" {
		t.Errorf("offline: %v", err)
	}
	c, _, _ := connectCaps(a, "old", "doctor/1")
	if _, err := x.f.StartDebugCapture(x.ctx, in); err == nil || err.Error() != "failed_precondition: agent too old" {
		t.Errorf("an agent without debug/1: %v", err)
	}
	t0 := time.After(150 * time.Millisecond)
loop:
	for {
		select {
		case m := <-c.in:
			if m.GetDebugCapture() != nil {
				t.Fatal("an agent without debug/1 was asked")
			}
		case <-t0:
			break loop
		}
	}
	// the refused starts left no capture behind, and no audit row
	if n := x.count(`SELECT count(*) FROM debug_capture`); n != 0 {
		t.Errorf("%d captures after refusals", n)
	}
	if n := x.count(`SELECT count(*) FROM audit WHERE action = 'node.debug_capture'`); n != 0 {
		t.Errorf("%d audit rows after refusals", n)
	}
}

func TestDebugCaptureValidatesTheRequest(t *testing.T) {
	x, a := newL3Env(t)
	connectCaps(a, "new", debugCaps...)
	for name, in := range map[string]DebugStart{
		"level":    {NodeID: a.nodeID, Level: "trace", DurationS: 10},
		"duration": {NodeID: a.nodeID, Level: "debug", DurationS: 901},
		"source":   {NodeID: a.nodeID, Level: "debug", DurationS: 10, Sources: []string{"x y"}},
		"many":     {NodeID: a.nodeID, Level: "debug", DurationS: 10, Sources: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
	} {
		if _, err := x.f.StartDebugCapture(x.ctx, in); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDebugCaptureStartAuditsStoresAndTakesTheReport(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", debugCaps...)
	asked := answerDebug(c, &agentv1.CommandResult{Ok: true})
	row, err := x.f.StartDebugCapture(x.ctx, DebugStart{NodeID: a.nodeID, Level: "debug", Sources: []string{"hysteria2", "xray", "hysteria2"}, DurationS: 120})
	if err != nil {
		t.Fatal(err)
	}
	p := <-asked
	if p == nil || p.RequestId != row.ID || p.Level != "debug" || p.DurationS != 120 || strings.Join(p.Sources, ",") != "hysteria2,xray" || p.Stop {
		t.Fatalf("the node was sent %+v for %+v", p, row)
	}
	if row.State != "running" || row.Actor != "adm_test" {
		t.Errorf("row = %+v", row)
	}
	// the audit entry has the actor, the node, the sources and the duration
	var params string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params FROM audit WHERE action = 'node.debug_capture' AND actor = 'adm_test'`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{a.nodeID, `"sources":"hysteria2,xray"`, `"duration_s":"120"`, `"level":"debug"`} {
		if !strings.Contains(params, want) {
			t.Errorf("audit params %s lack %s", params, want)
		}
	}

	// a second start while it runs is refused before the node is asked
	if _, err := x.f.StartDebugCapture(x.ctx, DebugStart{NodeID: a.nodeID, DurationS: 10}); err == nil || err.Error() != "failed_precondition: debug_busy" {
		t.Errorf("second start: %v", err)
	}

	// the node's report: the panel cleans it again, with its secret names
	x.f.cfg.JournalSecrets = []string{"agent-secret.example.com"}
	send := func(rep *agentv1.DebugCaptureReport) {
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DebugReport{DebugReport: rep}})
	}
	send(&agentv1.DebugCaptureReport{RequestId: row.ID, Lines: []*agentv1.LogLine{
		{TimeUnixMs: 1, Level: agentv1.Severity_SEVERITY_DEBUG, Source: "hysteria2", Message: "sni agent-secret.example.com from 203.0.113.9:1234"},
		{TimeUnixMs: 2, Level: agentv1.Severity_SEVERITY_INFO, Source: "xray", Message: "to canary.example.net"},
	}})
	send(&agentv1.DebugCaptureReport{RequestId: row.ID, Eof: true, Dropped: 3, EndReason: "expired"})
	// a report for a capture nobody asked for is dropped
	send(&agentv1.DebugCaptureReport{RequestId: "req_unknown", Eof: true, Lines: []*agentv1.LogLine{{Message: "nope"}}})
	deadline := time.Now().Add(5 * time.Second)
	var got store.DebugCaptureRow
	for time.Now().Before(deadline) {
		got, _ = x.st.DebugCapture(x.ctx, row.ID)
		if got.State == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.State != "done" || got.EndReason != "expired" || got.Records != 2 || got.Dropped != 3 {
		t.Fatalf("capture after the report = %+v", got)
	}
	lines, _, _ := x.st.DebugLines(x.ctx, row.ID, store.DebugLineFilter{})
	var all string
	for _, l := range lines {
		all += l.Message + "\n"
	}
	for _, leak := range []string{"agent-secret.example.com", "203.0.113.9", "canary.example.net"} {
		if strings.Contains(all, leak) {
			t.Errorf("%q stored:\n%s", leak, all)
		}
	}
	if n := x.count(`SELECT count(*) FROM debug_capture WHERE id = 'req_unknown'`); n != 0 {
		t.Error("a capture appeared from a report")
	}
}

func TestDebugCaptureNodeRefusalsAreCodesAndLeaveNothingRunning(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", debugCaps...)
	for _, tc := range []struct{ node, want string }{{"busy", "failed_precondition: debug_busy"}, {"invalid", "invalid_argument: the node refused these settings"}, {"boom", "unavailable: the node could not start the capture"}} {
		asked := answerDebug(c, &agentv1.CommandResult{Error: tc.node})
		_, err := x.f.StartDebugCapture(x.ctx, DebugStart{NodeID: a.nodeID, Level: "info", DurationS: 30})
		if err == nil || err.Error() != tc.want {
			t.Errorf("node says %q: %v, want %q", tc.node, err, tc.want)
		}
		<-asked
	}
	if n := x.count(`SELECT count(*) FROM debug_capture`); n != 0 {
		t.Errorf("%d captures left after refusals", n)
	}
	if n := x.count(`SELECT count(*) FROM audit WHERE action = 'node.debug_capture'`); n != 0 {
		t.Errorf("a refused start was audited as started")
	}
}

func TestDebugCaptureStopAsksTheNodeToShipAndReturnsTheFinishedRow(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", debugCaps...)
	asked := answerDebug(c, &agentv1.CommandResult{Ok: true})
	row, err := x.f.StartDebugCapture(x.ctx, DebugStart{NodeID: a.nodeID, DurationS: 600})
	if err != nil {
		t.Fatal(err)
	}
	<-asked
	stopAsked := make(chan *agentv1.DebugCapture, 1)
	go func() { // the agent ships, then answers
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetDebugCapture() != nil })
		p := m.GetDebugCapture()
		stopAsked <- p
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DebugReport{DebugReport: &agentv1.DebugCaptureReport{
			RequestId: row.ID, Eof: true, EndReason: "stopped", Lines: []*agentv1.LogLine{{TimeUnixMs: 5, Level: agentv1.Severity_SEVERITY_INFO, Source: "agent", Message: "last"}}}}})
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{RequestId: p.RequestId, Ok: true}}})
	}()
	got, err := x.f.StopDebugCapture(x.ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := <-stopAsked
	if !p.Stop || p.CaptureId != row.ID || p.RequestId == row.ID || p.RequestId == "" {
		t.Errorf("stop frame = %+v", p)
	}
	if got.State != "done" || got.EndReason != "stopped" || got.Records != 1 {
		t.Errorf("row after stop = %+v", got)
	}
	if n := x.count(`SELECT count(*) FROM audit WHERE action = 'node.debug_capture_stop'`); n != 1 {
		t.Errorf("%d stop audit rows", n)
	}
	// stopping a finished capture asks nobody
	if again, err := x.f.StopDebugCapture(x.ctx, row.ID); err != nil || again.State != "done" {
		t.Errorf("second stop = %+v, %v", again, err)
	}
	if _, err := x.f.StopDebugCapture(x.ctx, "req_none"); code(err) != connect.CodeNotFound {
		t.Errorf("unknown capture: %v", err)
	}
}
