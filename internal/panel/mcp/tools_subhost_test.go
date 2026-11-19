//go:build !js

package mcp

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/gen/mistgate/subhost/v1/subhostv1connect"
)

// subHostPath stands for the settings value sub_host_path (the bridge path): the panel keeps it, and no tool may show it.
const subHostPath = "/c2VjcmV0LWJyaWRnZS1wYXRoLXg"

// fakeSubHost is the SubscriptionHostService of the test world: a state machine that records the changing calls.
type fakeSubHost struct {
	subhostv1connect.UnimplementedSubscriptionHostServiceHandler
	mu                  sync.Mutex
	state               subhostv1.SubscriptionHostState
	domain, node, next  string
	publicURL           string
	dnsMismatch         bool
	downReason          string // of the serving node's bridge; "" = up
	set, moves, disable []string
}

func (f *fakeSubHost) view() *subhostv1.SubscriptionHost {
	st := f.state
	if st == subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_UNSPECIFIED {
		st = subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF
	}
	h := &subhostv1.SubscriptionHost{State: st, Domain: f.domain, NodeId: f.node, NextNodeId: f.next, PublicUrl: f.publicURL}
	if f.node != "" {
		h.Bridges = []*subhostv1.SubscriptionHostBridge{{NodeId: f.node, Up: f.downReason == "", DownReason: f.downReason}}
		h.NotReady = []string{"panel_cert"}
	}
	return h
}

func (f *fakeSubHost) GetSubscriptionHost(context.Context, *connect.Request[subhostv1.GetSubscriptionHostRequest]) (*connect.Response[subhostv1.GetSubscriptionHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&subhostv1.GetSubscriptionHostResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) CheckSubscriptionHostDomain(_ context.Context, r *connect.Request[subhostv1.CheckSubscriptionHostDomainRequest]) (*connect.Response[subhostv1.CheckSubscriptionHostDomainResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dnsMismatch {
		return connect.NewResponse(&subhostv1.CheckSubscriptionHostDomainResponse{Found: []string{"192.0.2.9"}, Expected: []string{"192.0.2.1"}}), nil
	}
	return connect.NewResponse(&subhostv1.CheckSubscriptionHostDomainResponse{Matches: true, Found: []string{"192.0.2.1"}, Expected: []string{"192.0.2.1"}}), nil
}

func (f *fakeSubHost) SetSubscriptionHost(_ context.Context, r *connect.Request[subhostv1.SetSubscriptionHostRequest]) (*connect.Response[subhostv1.SetSubscriptionHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = append(f.set, r.Msg.GetNodeId()+" "+r.Msg.GetDomain()+" "+r.Msg.GetSiteName())
	f.state, f.domain, f.node, f.publicURL = subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON, r.Msg.GetDomain(), r.Msg.GetNodeId(), "https://"+r.Msg.GetDomain()
	return connect.NewResponse(&subhostv1.SetSubscriptionHostResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) StartSubscriptionHostMove(_ context.Context, r *connect.Request[subhostv1.StartSubscriptionHostMoveRequest]) (*connect.Response[subhostv1.StartSubscriptionHostMoveResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, "start "+r.Msg.GetNodeId()+" "+r.Msg.GetSiteName())
	f.state, f.next = subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING, r.Msg.GetNodeId()
	return connect.NewResponse(&subhostv1.StartSubscriptionHostMoveResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) FinishSubscriptionHostChange(context.Context, *connect.Request[subhostv1.FinishSubscriptionHostChangeRequest]) (*connect.Response[subhostv1.FinishSubscriptionHostChangeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, "finish")
	f.state, f.node, f.next = subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON, f.next, ""
	return connect.NewResponse(&subhostv1.FinishSubscriptionHostChangeResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) DisableSubscriptionHost(_ context.Context, r *connect.Request[subhostv1.DisableSubscriptionHostRequest]) (*connect.Response[subhostv1.DisableSubscriptionHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disable = append(f.disable, r.Msg.GetPublicUrl()+" "+strconv.FormatBool(r.Msg.GetForce()))
	f.state, f.publicURL = subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING, r.Msg.GetPublicUrl()
	return connect.NewResponse(&subhostv1.DisableSubscriptionHostResponse{Host: f.view()}), nil
}

func noSubHostSecret(t *testing.T, what, out string) {
	t.Helper()
	if strings.Contains(out, subHostPath) || strings.Contains(out, "sub_host_path") {
		t.Fatalf("%s shows the bridge path: %s", what, out)
	}
}

// Every change waits for the owner, applies only with the confirm token it was made for, and never shows the bridge path.
func TestSubscriptionHostPlansWaitForTheOwner(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	noSubHostSecret(t, "get", mustOK(t, s, "subscription_host_get", map[string]any{}))

	e.w.subhost.dnsMismatch = true
	if out := mustFail(t, s, "subscription_host_set_plan", map[string]any{"node_id": "de1", "domain": "sub.example.com"}); !strings.Contains(out, "skip_dns_check") {
		t.Fatalf("a DNS mismatch was planned: %s", out)
	}
	e.w.subhost.dnsMismatch = false

	if out := mustFail(t, s, "subscription_host_set_plan", map[string]any{"node_id": "de1", "domain": "sub.example.com", "site_name": "not a name"}); !strings.Contains(out, "site_name") {
		t.Fatalf("a bad site name was planned: %s", out)
	}
	set := planOf(t, s, "subscription_host_set", map[string]any{"node_id": "de1", "domain": "Sub.Example.com.", "site_name": "www.example.com"})
	if !set.NeedsApproval {
		t.Fatalf("set without the owner: %+v", set)
	}
	stored := storedParamsText(t, e)
	if !strings.Contains(stored, nodeA) || !strings.Contains(stored, "sub.example.com") || !strings.Contains(stored, `"dns"`) {
		t.Fatalf("set plan: %s", stored)
	}
	noSubHostSecret(t, "set plan", stored)

	// before approval nothing runs
	if out := applyError(t, s, "subscription_host_set", set.ConfirmToken); len(e.w.subhost.set) != 0 {
		t.Fatalf("set ran before approval: %s", out)
	}
	// a plan of another tool is refused like an unknown one
	if out := applyError(t, s, "subscription_host_disable", set.ConfirmToken); !strings.Contains(out, "unknown confirm token") {
		t.Fatalf("a foreign plan applied: %s", out)
	}
	approvePlan(t, e, set)
	// arguments changed behind the approval: refused
	e.plans.mu.Lock()
	pl := e.plans.byID[set.PlanID]
	original := pl.ParamsJSON
	pl.ParamsJSON = strings.Replace(original, "sub.example.com", "evil.example.com", 1)
	e.plans.mu.Unlock()
	applyError(t, s, "subscription_host_set", set.ConfirmToken)
	if len(e.w.subhost.set) != 0 {
		t.Fatal("a changed plan applied")
	}
	e.plans.mu.Lock()
	pl.ParamsJSON = original
	e.plans.mu.Unlock()
	a := applyOf(t, s, "subscription_host_set", set.ConfirmToken)
	if a.Status != StatusApplied || len(e.w.subhost.set) != 1 || e.w.subhost.set[0] != nodeA+" sub.example.com www.example.com" {
		t.Fatalf("set apply: %+v %v", a, e.w.subhost.set)
	}
	noSubHostSecret(t, "set apply", a.Result)

	if out := mustFail(t, s, "subscription_host_set_plan", map[string]any{"node_id": "de1", "domain": "sub.example.com"}); !strings.Contains(out, "already on") {
		t.Fatalf("set planned while on: %s", out)
	}
	if out := mustFail(t, s, "subscription_host_move_plan", map[string]any{"action": "finish"}); !strings.Contains(out, "no move") {
		t.Fatalf("finish planned while on: %s", out)
	}
	move := planOf(t, s, "subscription_host_move", map[string]any{"action": "start", "node_id": nodeB, "site_name": "next.example.com"})
	approvePlan(t, e, move)
	if a := applyOf(t, s, "subscription_host_move", move.ConfirmToken); a.Status != StatusApplied || len(e.w.subhost.moves) != 1 || e.w.subhost.moves[0] != "start "+nodeB+" next.example.com" {
		t.Fatalf("move apply: %+v %v", a, e.w.subhost.moves)
	}
	finish := planOf(t, s, "subscription_host_move", map[string]any{"action": "finish"})
	approvePlan(t, e, finish)
	applyOf(t, s, "subscription_host_move", finish.ConfirmToken)

	if out := mustFail(t, s, "subscription_host_disable_plan", map[string]any{}); !strings.Contains(out, "force") {
		t.Fatalf("a disable without an address or force was planned: %s", out)
	}
	disable := planOf(t, s, "subscription_host_disable", map[string]any{"public_url": "https://panel.example.com/"})
	approvePlan(t, e, disable)
	if a := applyOf(t, s, "subscription_host_disable", disable.ConfirmToken); a.Status != StatusApplied || len(e.w.subhost.disable) != 1 || e.w.subhost.disable[0] != "https://panel.example.com false" {
		t.Fatalf("disable apply: %+v %v", a, e.w.subhost.disable)
	}
	noSubHostSecret(t, "get after", mustOK(t, s, "subscription_host_get", map[string]any{}))
	// The bridge's down reason and what readiness lacks reach the agent.
	e.w.subhost.mu.Lock()
	e.w.subhost.state, e.w.subhost.node, e.w.subhost.downReason = subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON, "de1", "pin_mismatch"
	e.w.subhost.mu.Unlock()
	if out := mustOK(t, s, "subscription_host_get", map[string]any{}); !strings.Contains(out, `"down_reason":"pin_mismatch"`) || !strings.Contains(out, `"not_ready":["panel_cert"]`) {
		t.Fatalf("get: %s", out)
	}
	for _, p := range e.plans.byID {
		noSubHostSecret(t, p.Tool, p.ParamsJSON+p.FactsJSON+p.Summary+p.Result)
	}
}
