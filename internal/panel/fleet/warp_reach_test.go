//go:build !js

package fleet

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

var reachDomains = []string{"de1.example.com", "nl1.example.com"}

type reachAnswer struct {
	colo  string
	items []store.WarpReachItem
	err   error
}

// askReach calls WarpReach in the background and answers it on the stream with result.
func askReach(t *testing.T, e *env, a *agent, c *conn, result *agentv1.CommandResult) (*agentv1.WarpReach, reachAnswer) {
	t.Helper()
	done := make(chan reachAnswer, 1)
	go func() {
		colo, items, err := e.f.WarpReach(e.ctx, a.nodeID, reachDomains)
		done <- reachAnswer{colo, items, err}
	}()
	cmd := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetWarpReach() != nil }).GetWarpReach()
	replyPortCommand(c, cmd.RequestId, result)
	select {
	case got := <-done:
		return cmd, got
	case <-time.After(10 * time.Second):
		t.Fatal("WarpReach did not return")
		return nil, reachAnswer{}
	}
}

func TestWarpReachSendsTheDomainsAndParsesTheAnswer(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectCaps(a, "de1-1", capWarpReach)
	cmd, got := askReach(t, e, a, c, &agentv1.CommandResult{Ok: true, Params: map[string]string{"colo": "FRA", "r0": "ok:200:310", "r1": "blocked:403:95"}})
	if got.err != nil {
		t.Fatal(got.err)
	}
	if cmd.RequestId == "" || len(cmd.Domains) != 2 || cmd.Domains[0] != "de1.example.com" || cmd.Domains[1] != "nl1.example.com" {
		t.Fatalf("command = %+v", cmd)
	}
	if got.colo != "FRA" || len(got.items) != 2 || got.items[0] != (store.WarpReachItem{Verdict: "ok", HTTPStatus: 200, Millis: 310}) ||
		got.items[1] != (store.WarpReachItem{Verdict: "blocked", HTTPStatus: 403, Millis: 95}) {
		t.Fatalf("answer = %+v", got)
	}
}

// A refusal of the agent is an error that says which; an answer out of the contract is no answer at all.
func TestWarpReachRefusalsAndUnreadableAnswers(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectCaps(a, "de1-1", capWarpReach)
	for name, result := range map[string]*agentv1.CommandResult{
		"warp reach: busy":              {Error: "busy"},
		"warp reach: too_soon":          {Error: "too_soon"},
		"warp reach: warp_down":         {Error: "warp_down"},
		"warp reach: bad_params":        {Error: "bad_params"},
		"warp reach: failed":            {Error: "failed: some text of the agent"},
		"warp reach: unreadable answer": {Ok: true, Params: map[string]string{"colo": "FRA", "r0": "ok:200:310"}}, // r1 missing
	} {
		if _, got := askReach(t, e, a, c, result); got.err == nil || got.err.Error() != name {
			t.Errorf("%+v: error %v, want %q", result, got.err, name)
		}
	}
	for _, bad := range []string{"ours:200:1", "ok:200", "ok:x:1", "ok:200:y", "ok:70000:1", ""} {
		_, got := askReach(t, e, a, c, &agentv1.CommandResult{Ok: true, Params: map[string]string{"colo": "FRA", "r0": "ok:200:310", "r1": bad}})
		if got.err == nil || got.items != nil {
			t.Errorf("r1 = %q accepted: %+v", bad, got)
		}
	}
}

func TestWarpReachNeedsAnAgentThatListsTheCapability(t *testing.T) {
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_WarpReach{WarpReach: &agentv1.WarpReach{}}}
	if capability, message := requestCapability(frame, false); capability != "warp-reach/1" || message != "agent too old" {
		t.Fatalf("WarpReach capability = %q, %q", capability, message)
	}
	if requestKind(frame) != PendingCommand {
		t.Fatalf("WarpReach is not a pending command: %v", requestKind(frame))
	}
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectCaps(a, "old", "doctor/1", "warp/1") // an agent from before this feature
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	if _, _, err := e.f.WarpReach(ctx, a.nodeID, reachDomains); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Fatalf("an agent without warp-reach/1: %v", err)
	}
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case m := <-c.in:
			if m.GetWarpReach() != nil {
				t.Fatal("the old agent was sent a WarpReach")
			}
			continue
		case <-deadline:
		}
		break
	}
}
