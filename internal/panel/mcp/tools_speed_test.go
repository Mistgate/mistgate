//go:build !js

package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

func (w *world) PathSpeed(_ context.Context, r *connect.Request[probev1.PathSpeedRequest]) (*connect.Response[probev1.PathSpeedResponse], error) {
	w.mu.Lock()
	w.speedReqs = append(w.speedReqs, r.Msg)
	code := w.speedCode
	w.mu.Unlock()
	out := &probev1.PathSpeedResponse{FromName: "nl1", ToName: "de1", Direction: r.Msg.GetDirection(), Mb: r.Msg.GetMb(), AtUnix: 1700000000, Error: code}
	if code == "" {
		out.MbitS, out.ConnectMs, out.FirstByteMs, out.DurationMs, out.Bytes = 412.3, 12, 31, 311, uint64(r.Msg.GetMb())<<20
	}
	return connect.NewResponse(out), nil
}

func (w *world) PathSpeedResults(_ context.Context, r *connect.Request[probev1.PathSpeedResultsRequest]) (*connect.Response[probev1.PathSpeedResultsResponse], error) {
	w.mu.Lock()
	w.speedResultReqs = append(w.speedResultReqs, r.Msg)
	w.mu.Unlock()
	return connect.NewResponse(&probev1.PathSpeedResultsResponse{Tests: []*probev1.PathSpeedResponse{
		{FromName: "nl1", ToName: "de1", Direction: "down", Mb: 32, AtUnix: 1700000000, MbitS: 308.3, ConnectMs: 37, FirstByteMs: 37, DurationMs: 870, Bytes: 32 << 20},
	}}), nil
}

// The figure of an approved test can be read back by anyone who reads the fleet, and it says what the figure is: one stream, a lower bound.
func TestPathSpeedResultsReadsTheKeptFigures(t *testing.T) {
	e := newTestEnv(t)
	for _, p := range []Profile{ProfileReadonly, ProfileOperator, ProfileAdmin} {
		_, secret := e.token(p)
		if !slices.Contains(toolNames(t, e.session(secret)), "path_speed_results") {
			t.Errorf("%s does not see path_speed_results", p)
		}
	}
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	out := mustOK(t, s, "path_speed_results", map[string]any{"node": "nl1"})
	noCanary(t, "path_speed_results", out)
	v := decode[pathSpeedResultsView](t, out)
	if len(v.Tests) != 1 || v.Tests[0].From != "nl1" || v.Tests[0].To != "de1" || v.Tests[0].MbitS != 308.3 || v.Tests[0].DurationMs != 870 || v.Tests[0].MB != 32 {
		t.Fatalf("tests = %+v", v.Tests)
	}
	if !strings.Contains(v.Note, "ONE TCP stream") || !strings.Contains(v.Note, "lower bound") {
		t.Errorf("the note does not say what the figure is: %q", v.Note)
	}
	e.w.mu.Lock()
	reqs := e.w.speedResultReqs
	e.w.mu.Unlock()
	if len(reqs) != 1 || reqs[0].NodeId != nodeB {
		t.Errorf("the node was not resolved to its id: %+v", reqs)
	}
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "path_speed_results" && !strings.Contains(tool.Description, "lower bound") {
			t.Errorf("the description does not say the figure is a lower bound: %s", tool.Description)
		}
	}
}

// path_speed moves bulk traffic: admin profile only, always a plan the owner approves, and the arguments are node references
// and a size, never a host.
func TestPathSpeedIsAdminOnlyAndWaitsForTheOwner(t *testing.T) {
	e := newTestEnv(t)
	for _, p := range []Profile{ProfileReadonly, ProfileOperator} {
		_, secret := e.token(p)
		names := toolNames(t, e.session(secret))
		if slices.Contains(names, "path_speed_plan") || slices.Contains(names, "path_speed_apply") {
			t.Errorf("%s sees path_speed", p)
		}
	}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	names := toolNames(t, s)
	if !slices.Contains(names, "path_speed_plan") || !slices.Contains(names, "path_speed_apply") {
		t.Fatal("admin does not see path_speed_plan and path_speed_apply")
	}

	planJSON := mustOK(t, s, "path_speed_plan", map[string]any{"from": "nl1", "to": "de1", "mb": 24, "direction": "up", "reason": "the link to de1 feels slow"})
	noCanary(t, "path_speed_plan", planJSON)
	p := decode[PlanOut](t, planJSON)
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerTraffic {
		t.Fatalf("plan: %+v", p)
	}
	if f := factOf(t, p, "sender"); f.Value != "nl1" || !f.Untrusted {
		t.Errorf("sender fact: %+v", f)
	}
	if f := factOf(t, p, "target"); f.Value != "de1" || !f.Untrusted {
		t.Errorf("target fact: %+v", f)
	}
	if f := factOf(t, p, "direction"); f.Code != "path_speed_up" || !strings.Contains(f.Value, "from the sender to the target") {
		t.Errorf("direction fact: %+v", f)
	}
	if f := factOf(t, p, "size"); f.Code != "bytes" || f.Params["bytes"] != "25165824" {
		t.Errorf("size fact: %+v", f)
	}
	if eff := factOf(t, p, "effect").Value; !strings.Contains(eff, "no public server") || !strings.Contains(eff, "30 seconds") {
		t.Errorf("effect fact: %q", eff)
	}
	if st := storedPlan(t, e, p.PlanID); st.Status != StatusAwaiting || st.Tool != "path_speed" || !strings.Contains(st.ParamsJSON, nodeB) || !strings.Contains(st.ParamsJSON, nodeA) {
		t.Fatalf("stored plan: %+v", st)
	}
	if len(e.w.speedReqs) != 0 {
		t.Fatal("the plan ran the test")
	}
	if got := applyError(t, s, "path_speed", p.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("apply before approval: %q", got)
	}
	if len(e.w.speedReqs) != 0 {
		t.Fatal("apply before approval ran the test")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	ap := applyOf(t, s, "path_speed", p.ConfirmToken)
	if ap.Status != StatusApplied || !strings.Contains(ap.Result, "412.3 Mbit/s") || !strings.Contains(ap.Result, "24 MB from the sender to the target") {
		t.Fatalf("apply: %+v", ap)
	}
	noCanary(t, "path_speed_apply", ap.Result)
	// the owner's screen words the result from its code and values, in its language: the names, the size, the figures
	pl := storedPlan(t, e, p.PlanID)
	var params map[string]string
	if err := json.Unmarshal([]byte(pl.OutcomeParams), &params); err != nil {
		t.Fatal(err)
	}
	if pl.OutcomeCode != "path_speed_done" || params["from"] != "nl1" || params["to"] != "de1" || params["direction"] != "up" || params["mb"] != "24" || params["mbit_s"] != "412.3" || params["duration_ms"] != "311" || params["connect_ms"] != "12" {
		t.Errorf("outcome: %q %v", pl.OutcomeCode, params)
	}
	if len(e.w.speedReqs) != 1 {
		t.Fatalf("the panel was asked %d times", len(e.w.speedReqs))
	}
	if r := e.w.speedReqs[0]; r.GetFromNodeId() != nodeB || r.GetToNodeId() != nodeA || r.GetMb() != 24 || r.GetDirection() != "up" {
		t.Errorf("request = %v", r)
	}
	// the call goes out under the plan's grant, once, and a replay does not run it again
	if calls := e.w.calls("/mistgate.probe.v1.ProbeService/PathSpeed"); len(calls) != 1 || calls[0].Approved != p.PlanID {
		t.Errorf("calls: %+v", calls)
	}
	applyOf(t, s, "path_speed", p.ConfirmToken)
	if len(e.w.speedReqs) != 1 {
		t.Error("a replay ran the test again")
	}
}

func TestPathSpeedRefusesBadArgumentsBeforePlanning(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	for name, args := range map[string]map[string]any{
		"too small":      {"from": "nl1", "to": "de1", "mb": 7},
		"too big":        {"from": "nl1", "to": "de1", "mb": 33},
		"a host":         {"from": "nl1", "to": "203.0.113.5"},
		"a url":          {"from": "nl1", "to": "http://example.com/"},
		"the same node":  {"from": "nl1", "to": "nl1"},
		"a bad way":      {"from": "nl1", "to": "de1", "direction": "sideways"},
		"unknown sender": {"from": "nope", "to": "de1"},
	} {
		if got := mustFail(t, s, "path_speed_plan", args); got == "" {
			t.Errorf("%s: no error", name)
		}
	}
	if len(e.w.speedReqs) != 0 {
		t.Error("the panel was asked")
	}
	// the defaults: 16 MB, down
	p := planOf(t, s, "path_speed", map[string]any{"from": "nl1", "to": "de1"})
	if f := factOf(t, p, "size"); f.Params["bytes"] != "16777216" {
		t.Errorf("default size: %+v", f)
	}
	if f := factOf(t, p, "direction"); f.Code != "path_speed_down" {
		t.Errorf("default direction: %+v", f)
	}
}

func TestPathSpeedReportsWhyItDidNotRun(t *testing.T) {
	for code, want := range map[string]string{"agent_too_old": "update it", "unreachable": "firewall", "busy": "already running", "node_offline": "not connected"} {
		e := newTestEnv(t)
		e.w.speedCode = code
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "path_speed", map[string]any{"from": "nl1", "to": "de1"})
		if err := e.plans.decide(p.PlanID, true); err != nil {
			t.Fatal(err)
		}
		got := applyError(t, s, "path_speed", p.ConfirmToken)
		if !strings.Contains(got, code) || !strings.Contains(got, want) {
			t.Errorf("%s: error = %q, want the code and %q", code, got, want)
		}
		if st := storedPlan(t, e, p.PlanID); st.Status != StatusFailed || st.OutcomeCode != "path_speed_not_run" {
			t.Errorf("%s: stored plan %+v", code, st)
		}
	}
}
