package agent

import (
	"errors"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/node/warp"
)

func askWarpReach(h *harness, id string, domains ...string) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_WarpReach{WarpReach: &pb.WarpReach{RequestId: id, Domains: domains}}})
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("warp reach answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

// The panel gates the command by this string; only an agent that runs WARP lists it.
func TestWarpReachCapabilityFollowsTheWarpManager(t *testing.T) {
	for _, withWarp := range []bool{true, false} {
		x := newL3(t, 3, withWarp)
		if got := contains((<-x.panel.hellos).Capabilities, agentcap.WarpReach); got != withWarp {
			t.Fatalf("WARP manager = %v but the capability is listed = %v", withWarp, got)
		}
	}
}

func TestWarpReachAnswersTheVerdictsAndTheColo(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	x.warp.reach = func(domains []string) (warp.ReachReport, error) {
		return warp.ReachReport{Colo: "FRA", Items: []warp.ReachItem{{Verdict: "ok", Code: 200, Ms: 310}, {Verdict: "blocked", Code: 403, Ms: 95}, {Verdict: "unreachable"}}}, nil
	}
	r := askWarpReach(x.harness, "reach", "de1.example.com", "nl1.example.com", "gone.example.com")
	want := map[string]string{"colo": "FRA", "r0": "ok:200:310", "r1": "blocked:403:95", "r2": "unreachable:0:0"}
	if !r.Ok || len(r.Params) != len(want) {
		t.Fatalf("result = %+v", r)
	}
	for k, v := range want {
		if r.Params[k] != v {
			t.Errorf("%s = %q, want %q", k, r.Params[k], v)
		}
	}
	x.warp.mu.Lock()
	defer x.warp.mu.Unlock()
	if len(x.warp.reachAsked) != 1 || len(x.warp.reachAsked[0]) != 3 || x.warp.reachAsked[0][1] != "nl1.example.com" {
		t.Fatalf("the manager was asked %v", x.warp.reachAsked)
	}
}

func TestWarpReachErrorsAreTheContractsWords(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	for refusal, want := range map[error]string{warp.ErrReachBusy: "busy", warp.ErrReachTooSoon: "too_soon", warp.ErrNotActive: "warp_down", warp.ErrReachBadName: "bad_params"} {
		x.warp.reach = func([]string) (warp.ReachReport, error) { return warp.ReachReport{}, refusal }
		if r := askWarpReach(x.harness, "e", "de1.example.com"); r.Ok || r.Error != want {
			t.Errorf("%v: result %+v, want error %q", refusal, r, want)
		}
	}
	x.warp.reach = func([]string) (warp.ReachReport, error) { return warp.ReachReport{}, errors.New("boom") }
	if r := askWarpReach(x.harness, "e", "de1.example.com"); r.Ok || r.Error != "failed: boom" {
		t.Errorf("another error: %+v", r)
	}
	// an empty list and a list over the limit never reach the manager
	x.warp.mu.Lock()
	x.warp.reachAsked = nil
	x.warp.mu.Unlock()
	if r := askWarpReach(x.harness, "none"); r.Ok || r.Error != "bad_params" {
		t.Errorf("no domains: %+v", r)
	}
	many := make([]string, 9)
	for i := range many {
		many[i] = "de1.example.com"
	}
	if r := askWarpReach(x.harness, "many", many...); r.Ok || r.Error != "bad_params" {
		t.Errorf("nine domains: %+v", r)
	}
	x.warp.mu.Lock()
	defer x.warp.mu.Unlock()
	if len(x.warp.reachAsked) != 0 {
		t.Fatalf("a refused list reached the manager: %v", x.warp.reachAsked)
	}
}
