//go:build !js

package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

func channelFixture() *probev1.ChannelLast {
	return &probev1.ChannelLast{AtUnix: 1700000000, Partners: 2,
		Down: &probev1.ChannelDirection{MbitS: 812.4, Partner: "de1", Streams: 8},
		Up:   &probev1.ChannelDirection{MbitS: 455.1, Partner: "se1", Streams: 8, Capped: true}}
}

func (w *world) ChannelMeasurePlan(_ context.Context, r *connect.Request[probev1.ChannelMeasurePlanRequest]) (*connect.Response[probev1.ChannelMeasurePlanResponse], error) {
	w.mu.Lock()
	code := w.channelPlanCode
	w.mu.Unlock()
	out := &probev1.ChannelMeasurePlanResponse{NodeName: "nl1", Error: code, ConfiguredMbps: 250}
	if code == "too_soon" {
		out.RetryAfterS = 421
	}
	if code == "" {
		out.Partners = []*probev1.ChannelPartner{{Name: "de1", KnownMbps: 2000}, {Name: "se1"}}
		out.MaxMb, out.MaxGb, out.EstSeconds = 2048, 8, 48
	}
	return connect.NewResponse(out), nil
}

func (w *world) ChannelMeasureStart(_ context.Context, r *connect.Request[probev1.ChannelMeasureStartRequest]) (*connect.Response[probev1.ChannelMeasureStartResponse], error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.channelStarts = append(w.channelStarts, r.Msg.GetNodeId())
	st := &probev1.ChannelMeasureStatusResponse{NodeName: "nl1", State: "running"}
	if w.channelStartCode != "" {
		st = &probev1.ChannelMeasureStatusResponse{NodeName: "nl1", State: "idle", Error: w.channelStartCode}
	}
	return connect.NewResponse(&probev1.ChannelMeasureStartResponse{Status: st}), nil
}

func (w *world) ChannelMeasureStatus(_ context.Context, _ *connect.Request[probev1.ChannelMeasureStatusRequest]) (*connect.Response[probev1.ChannelMeasureStatusResponse], error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.channelPolls++
	st := &probev1.ChannelMeasureStatusResponse{NodeName: "nl1", State: "running"}
	if w.channelPolls < 2 {
		return connect.NewResponse(st), nil // the first look: still going
	}
	switch w.channelEndState {
	case "", "done":
		st.State, st.Result = "done", channelFixture()
	case "down-only":
		l := channelFixture()
		l.Up = &probev1.ChannelDirection{}
		st.State, st.Result = "done", l
	case "failed":
		st.State, st.Error = "failed", "unreachable"
	case "canceled":
		st.State = "canceled"
	}
	return connect.NewResponse(st), nil
}

func (w *world) ChannelMeasureCancel(context.Context, *connect.Request[probev1.ChannelMeasureCancelRequest]) (*connect.Response[probev1.ChannelMeasureCancelResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func fastPolling(t *testing.T) {
	t.Helper()
	old := channelPollEvery
	channelPollEvery = 5 * time.Millisecond
	t.Cleanup(func() { channelPollEvery = old })
}

// channel_measure fills the links of a node and its partners: admin profile only, always a plan the owner approves, the argument is one
// node reference (the partners are the panel's choice), and the figures come back with their names and the caveat of an estimate.
func TestChannelMeasureIsAdminOnlyAndWaitsForTheOwner(t *testing.T) {
	fastPolling(t)
	e := newTestEnv(t)
	for _, p := range []Profile{ProfileReadonly, ProfileOperator} {
		_, secret := e.token(p)
		names := toolNames(t, e.session(secret))
		if slices.Contains(names, "channel_measure_plan") || slices.Contains(names, "channel_measure_apply") {
			t.Errorf("%s sees channel_measure", p)
		}
	}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	if names := toolNames(t, s); !slices.Contains(names, "channel_measure_plan") || !slices.Contains(names, "channel_measure_apply") {
		t.Fatal("admin does not see channel_measure_plan and channel_measure_apply")
	}
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "channel_measure_plan" {
			for _, want := range []string{"estimate", "not a guarantee of the tariff", "2 GiB", "owner's approval", "paid traffic"} {
				if !strings.Contains(tool.Description, want) {
					t.Errorf("the description does not say %q", want)
				}
			}
		}
	}

	planJSON := mustOK(t, s, "channel_measure_plan", map[string]any{"node": "nl1", "reason": "the number the owner wrote feels low"})
	noCanary(t, "channel_measure_plan", planJSON)
	p := decode[PlanOut](t, planJSON)
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerChannelTraffic {
		t.Fatalf("plan: %+v", p)
	}
	if f := factOf(t, p, "node"); f.Value != "nl1" || !f.Untrusted {
		t.Errorf("node fact: %+v", f)
	}
	if f := factOf(t, p, "partners"); !f.Untrusted || !strings.Contains(f.Value, "de1 (2000 Mbit/s known)") || !strings.Contains(f.Value, "se1 (channel not known)") {
		t.Errorf("partners fact: %+v", f)
	}
	if f := factOf(t, p, "size"); f.Code != "bytes" || f.Params["bytes"] != "8589934592" || !strings.Contains(f.Value, "8 GB") {
		t.Errorf("size fact: %+v", f)
	}
	if f := factOf(t, p, "duration"); f.Code != "seconds" || f.Params["n"] != "48" {
		t.Errorf("duration fact: %+v", f)
	}
	if f := factOf(t, p, "effect"); f.Code != "channel_measure_effect" || !strings.Contains(f.Value, "no public server") || !strings.Contains(f.Value, "paid traffic") || !strings.Contains(f.Value, "2 GiB and 15 seconds") {
		t.Errorf("effect fact: %+v", f)
	}
	if st := storedPlan(t, e, p.PlanID); st.Status != StatusAwaiting || st.Tool != "channel_measure" || !strings.Contains(st.ParamsJSON, nodeB) {
		t.Fatalf("stored plan: %+v", st)
	}
	if len(e.w.channelStarts) != 0 {
		t.Fatal("the plan started a measure")
	}
	if got := applyError(t, s, "channel_measure", p.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("apply before approval: %q", got)
	}
	if len(e.w.channelStarts) != 0 {
		t.Fatal("apply before approval started a measure")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	ap := applyOf(t, s, "channel_measure", p.ConfirmToken)
	if ap.Status != StatusApplied || !strings.Contains(ap.Result, "812.4 Mbit/s with de1") || !strings.Contains(ap.Result, "455.1 Mbit/s with se1") ||
		!strings.Contains(ap.Result, "not a guarantee of the tariff") || !strings.Contains(ap.Result, "at least that") {
		t.Fatalf("apply: %+v", ap)
	}
	noCanary(t, "channel_measure_apply", ap.Result)
	pl := storedPlan(t, e, p.PlanID)
	var params map[string]string
	if err := json.Unmarshal([]byte(pl.OutcomeParams), &params); err != nil {
		t.Fatal(err)
	}
	if pl.OutcomeCode != "channel_measure_done" || params["node"] != "nl1" || params["down_mbps"] != "812.4" || params["up_mbps"] != "455.1" || params["down_partner"] != "de1" || params["up_partner"] != "se1" || params["direction"] != "" {
		t.Errorf("outcome: %q %v", pl.OutcomeCode, params)
	}
	if len(e.w.channelStarts) != 1 || e.w.channelStarts[0] != nodeB {
		t.Fatalf("the panel was asked to start %v", e.w.channelStarts)
	}
	// the call goes out under the plan's grant, once, and a replay does not start another
	if calls := e.w.calls("/mistgate.probe.v1.ProbeService/ChannelMeasureStart"); len(calls) != 1 || calls[0].Approved != p.PlanID {
		t.Errorf("calls: %+v", calls)
	}
	applyOf(t, s, "channel_measure", p.ConfirmToken)
	if len(e.w.channelStarts) != 1 {
		t.Error("a replay started the measure again")
	}
}

func TestChannelMeasureRefusesBadArgumentsAndWhatCannotRun(t *testing.T) {
	fastPolling(t)
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	for name, args := range map[string]map[string]any{
		"a host":       {"node": "203.0.113.5"},
		"a url":        {"node": "http://example.com/"},
		"unknown node": {"node": "nope"},
		"no node":      {},
		"partners":     {"node": "nl1", "partners": []string{"de1"}},
	} {
		if got := mustFail(t, s, "channel_measure_plan", args); got == "" {
			t.Errorf("%s: no error", name)
		}
	}
	for code, want := range map[string]string{"no_partner": "no other node", "agent_too_old": "update it", "busy": "running a test", "node_offline": "not connected"} {
		e.w.channelPlanCode = code
		got := mustFail(t, s, "channel_measure_plan", map[string]any{"node": "nl1"})
		if !strings.Contains(got, code) || !strings.Contains(got, want) {
			t.Errorf("%s: error = %q, want the code and %q", code, got, want)
		}
	}
	e.w.channelPlanCode = "too_soon"
	if got := mustFail(t, s, "channel_measure_plan", map[string]any{"node": "nl1"}); !strings.Contains(got, "too_soon") || !strings.Contains(got, "8 minute") {
		t.Errorf("too_soon: %q", got)
	}
	if len(e.w.channelStarts) != 0 {
		t.Error("a refused plan started a measure")
	}
}

func TestChannelMeasureReportsWhyNothingWasMeasured(t *testing.T) {
	fastPolling(t)
	for name, set := range map[string]func(w *world){
		"start refused":        func(w *world) { w.channelStartCode = "too_soon" },
		"nothing worked":       func(w *world) { w.channelEndState = "failed" },
		"stopped by the owner": func(w *world) { w.channelEndState = "canceled" },
	} {
		e := newTestEnv(t)
		set(e.w)
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "channel_measure", map[string]any{"node": "nl1"})
		if err := e.plans.decide(p.PlanID, true); err != nil {
			t.Fatal(err)
		}
		got := applyError(t, s, "channel_measure", p.ConfirmToken)
		if !strings.Contains(got, "did not run") {
			t.Errorf("%s: error = %q", name, got)
		}
		if pl := storedPlan(t, e, p.PlanID); pl.OutcomeCode != "channel_measure_not_run" {
			t.Errorf("%s: outcome code %q", name, pl.OutcomeCode)
		}
	}
}

func TestChannelMeasureOneDirectionAndAStillRunningMeasureAreSaidSo(t *testing.T) {
	fastPolling(t)
	e := newTestEnv(t)
	e.w.channelEndState = "down-only"
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "channel_measure", map[string]any{"node": "nl1"})
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	ap := applyOf(t, s, "channel_measure", p.ConfirmToken)
	if !strings.Contains(ap.Result, "sending: did not work") || !strings.Contains(ap.Result, "receiving: 812.4") {
		t.Errorf("result: %q", ap.Result)
	}
	var params map[string]string
	json.Unmarshal([]byte(storedPlan(t, e, p.PlanID).OutcomeParams), &params)
	if params["direction"] != "down" {
		t.Errorf("a measure that got only the receiving figure says direction %q", params["direction"])
	}
	// a run that outlasts the apply is not a failure: it is said to be running and read from path_speed_results
	d, err := channelDone(&probev1.ChannelMeasureStatusResponse{NodeName: "nl1", State: "running"})
	if err != nil || !strings.Contains(d.text, "still running") || !strings.Contains(d.text, "path_speed_results") || d.out.Params["direction"] != "running" {
		t.Errorf("running: %+v, %v", d, err)
	}
}

func TestPathSpeedResultsCarriesTheChannelMeasuresAndTheCaveat(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	out := mustOK(t, s, "path_speed_results", map[string]any{})
	noCanary(t, "path_speed_results", out)
	v := decode[pathSpeedResultsView](t, out)
	if len(v.ChannelMeasures) != 1 {
		t.Fatalf("channel measures = %+v", v.ChannelMeasures)
	}
	c := v.ChannelMeasures[0]
	if c.Node != "nl1" || c.Receive.MbitS != 812.4 || c.Receive.Partner != "de1" || c.Send.MbitS != 455.1 || !c.Send.Capped || c.Partners != 2 {
		t.Errorf("channel measure = %+v", c)
	}
	if len(v.Tests) != 2 || v.Tests[1].Streams != 8 || !v.Tests[1].Capped {
		t.Errorf("tests = %+v", v.Tests)
	}
	for _, want := range []string{"estimate at the moment", "not a guarantee of the tariff", "ONE TCP stream", "lower bound"} {
		if !strings.Contains(v.Note, want) {
			t.Errorf("the note does not say %q: %q", want, v.Note)
		}
	}
}
