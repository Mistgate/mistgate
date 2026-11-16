//go:build !js

package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// subHostSNI stands for the settings value sub_host_tunnel_sni: the panel keeps it, and no tool may show it.
const subHostSNI = "tun-7f3c9a1e5b.sni.invalid"

// fakeSubHost is the SubscriptionHostService of the test world: a state machine that records the changing calls.
type fakeSubHost struct {
	adminv1connect.UnimplementedSubscriptionHostServiceHandler
	mu                  sync.Mutex
	state               adminv1.SubscriptionHostState
	domain, node, next  string
	publicURL           string
	dnsMismatch         bool
	downReason          string // of the serving node's tunnel; "" = up
	set, moves, disable []string
}

func (f *fakeSubHost) view() *adminv1.SubscriptionHost {
	st := f.state
	if st == adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_UNSPECIFIED {
		st = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF
	}
	h := &adminv1.SubscriptionHost{State: st, Domain: f.domain, NodeId: f.node, NextNodeId: f.next, PublicUrl: f.publicURL}
	if f.node != "" {
		h.Tunnels = []*adminv1.SubscriptionHostTunnel{{NodeId: f.node, Up: f.downReason == "", DownReason: f.downReason}}
	}
	return h
}

func (f *fakeSubHost) GetSubscriptionHost(context.Context, *connect.Request[adminv1.GetSubscriptionHostRequest]) (*connect.Response[adminv1.GetSubscriptionHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&adminv1.GetSubscriptionHostResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) CheckSubscriptionHostDomain(_ context.Context, r *connect.Request[adminv1.CheckSubscriptionHostDomainRequest]) (*connect.Response[adminv1.CheckSubscriptionHostDomainResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dnsMismatch {
		return connect.NewResponse(&adminv1.CheckSubscriptionHostDomainResponse{Found: []string{"192.0.2.9"}, Expected: []string{"192.0.2.1"}}), nil
	}
	return connect.NewResponse(&adminv1.CheckSubscriptionHostDomainResponse{Matches: true, Found: []string{"192.0.2.1"}, Expected: []string{"192.0.2.1"}}), nil
}

func (f *fakeSubHost) SetSubscriptionHost(_ context.Context, r *connect.Request[adminv1.SetSubscriptionHostRequest]) (*connect.Response[adminv1.SetSubscriptionHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = append(f.set, r.Msg.GetNodeId()+" "+r.Msg.GetDomain())
	f.state, f.domain, f.node, f.publicURL = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON, r.Msg.GetDomain(), r.Msg.GetNodeId(), "https://"+r.Msg.GetDomain()
	return connect.NewResponse(&adminv1.SetSubscriptionHostResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) StartSubscriptionHostMove(_ context.Context, r *connect.Request[adminv1.StartSubscriptionHostMoveRequest]) (*connect.Response[adminv1.StartSubscriptionHostMoveResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, "start "+r.Msg.GetNodeId())
	f.state, f.next = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING, r.Msg.GetNodeId()
	return connect.NewResponse(&adminv1.StartSubscriptionHostMoveResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) FinishSubscriptionHostChange(context.Context, *connect.Request[adminv1.FinishSubscriptionHostChangeRequest]) (*connect.Response[adminv1.FinishSubscriptionHostChangeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, "finish")
	f.state, f.node, f.next = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON, f.next, ""
	return connect.NewResponse(&adminv1.FinishSubscriptionHostChangeResponse{Host: f.view()}), nil
}

func (f *fakeSubHost) DisableSubscriptionHost(_ context.Context, r *connect.Request[adminv1.DisableSubscriptionHostRequest]) (*connect.Response[adminv1.DisableSubscriptionHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disable = append(f.disable, r.Msg.GetPublicUrl())
	f.state, f.publicURL = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING, r.Msg.GetPublicUrl()
	return connect.NewResponse(&adminv1.DisableSubscriptionHostResponse{Host: f.view()}), nil
}

func noSubHostSNI(t *testing.T, what, out string) {
	t.Helper()
	if strings.Contains(out, subHostSNI) || strings.Contains(out, "sub_host_tunnel_sni") {
		t.Fatalf("%s shows the tunnel SNI: %s", what, out)
	}
}

// Every change waits for the owner, applies only with the confirm token it was made for, and never shows the tunnel SNI.
func TestSubscriptionHostPlansWaitForTheOwner(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	noSubHostSNI(t, "get", mustOK(t, s, "subscription_host_get", map[string]any{}))

	e.w.subhost.dnsMismatch = true
	if out := mustFail(t, s, "subscription_host_set_plan", map[string]any{"node_id": "de1", "domain": "sub.example.com"}); !strings.Contains(out, "skip_dns_check") {
		t.Fatalf("a DNS mismatch was planned: %s", out)
	}
	e.w.subhost.dnsMismatch = false

	set := planOf(t, s, "subscription_host_set", map[string]any{"node_id": "de1", "domain": "Sub.Example.com."})
	if !set.NeedsApproval {
		t.Fatalf("set without the owner: %+v", set)
	}
	stored := storedParamsText(t, e)
	if !strings.Contains(stored, nodeA) || !strings.Contains(stored, "sub.example.com") || !strings.Contains(stored, `"dns"`) {
		t.Fatalf("set plan: %s", stored)
	}
	noSubHostSNI(t, "set plan", stored)

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
	if a.Status != StatusApplied || len(e.w.subhost.set) != 1 || e.w.subhost.set[0] != nodeA+" sub.example.com" {
		t.Fatalf("set apply: %+v %v", a, e.w.subhost.set)
	}
	noSubHostSNI(t, "set apply", a.Result)

	if out := mustFail(t, s, "subscription_host_set_plan", map[string]any{"node_id": "de1", "domain": "sub.example.com"}); !strings.Contains(out, "already on") {
		t.Fatalf("set planned while on: %s", out)
	}
	if out := mustFail(t, s, "subscription_host_move_plan", map[string]any{"action": "finish"}); !strings.Contains(out, "no move") {
		t.Fatalf("finish planned while on: %s", out)
	}
	move := planOf(t, s, "subscription_host_move", map[string]any{"action": "start", "node_id": nodeB})
	approvePlan(t, e, move)
	if a := applyOf(t, s, "subscription_host_move", move.ConfirmToken); a.Status != StatusApplied || len(e.w.subhost.moves) != 1 || e.w.subhost.moves[0] != "start "+nodeB {
		t.Fatalf("move apply: %+v %v", a, e.w.subhost.moves)
	}
	finish := planOf(t, s, "subscription_host_move", map[string]any{"action": "finish"})
	approvePlan(t, e, finish)
	applyOf(t, s, "subscription_host_move", finish.ConfirmToken)

	disable := planOf(t, s, "subscription_host_disable", map[string]any{"public_url": "https://panel.example.com/"})
	approvePlan(t, e, disable)
	if a := applyOf(t, s, "subscription_host_disable", disable.ConfirmToken); a.Status != StatusApplied || len(e.w.subhost.disable) != 1 || e.w.subhost.disable[0] != "https://panel.example.com" {
		t.Fatalf("disable apply: %+v %v", a, e.w.subhost.disable)
	}
	noSubHostSNI(t, "get after", mustOK(t, s, "subscription_host_get", map[string]any{}))
	// A node that answered the panel's tunnel as its site is told apart from an unreachable one.
	e.w.subhost.mu.Lock()
	e.w.subhost.state, e.w.subhost.node, e.w.subhost.downReason = adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON, "de1", "not_recognised"
	e.w.subhost.mu.Unlock()
	if out := mustOK(t, s, "subscription_host_get", map[string]any{}); !strings.Contains(out, `"down_reason":"not_recognised"`) {
		t.Fatalf("get: %s", out)
	}
	for _, p := range e.plans.byID {
		noSubHostSNI(t, p.Tool, p.ParamsJSON+p.FactsJSON+p.Summary+p.Result)
	}
}
