//go:build !js

package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// worldShare is who the fake panel hands a node out to: the mode and whether it is idle. Which groups receive the node is in
// the groups' disabled lists, as in the real panel.
type worldShare struct {
	mode adminv1.NodeShareMode
	idle bool
}

func (w *world) shareLocked(node string) *worldShare {
	if w.shares == nil {
		w.shares = map[string]*worldShare{}
	}
	if w.shares[node] == nil {
		w.shares[node] = &worldShare{mode: adminv1.NodeShareMode_NODE_SHARE_MODE_ALL}
	}
	return w.shares[node]
}

func (w *world) shareSummary(node string) *adminv1.NodeShareSummary {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shareSummaryLocked(node)
}

func (w *world) shareSummaryLocked(node string) *adminv1.NodeShareSummary {
	s := w.shareLocked(node)
	out := &adminv1.NodeShareSummary{Mode: s.mode, GroupsTotal: uint32(len(w.groups)), ChangedUnix: 1700000000, Idle: s.idle}
	for _, g := range w.groups {
		if !slices.Contains(g.GetDisabledNodeIds(), node) {
			out.GroupsReceiving++
		}
	}
	return out
}

func (w *world) SetNodeSharing(_ context.Context, r *connect.Request[adminv1.SetNodeSharingRequest]) (*connect.Response[adminv1.SetNodeSharingResponse], error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	node := r.Msg.GetNodeId()
	if node != nodeA && node != nodeB {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	w.shareReqs = append(w.shareReqs, r.Msg)
	out := &adminv1.SetNodeSharingResponse{Mode: r.Msg.GetMode()}
	for _, g := range w.groups {
		was := !slices.Contains(g.GetDisabledNodeIds(), node)
		will := r.Msg.GetMode() == adminv1.NodeShareMode_NODE_SHARE_MODE_ALL || slices.Contains(r.Msg.GetGroupIds(), g.GetId())
		if will {
			out.GroupIds = append(out.GroupIds, g.GetId())
			out.PeopleReceiving += g.GetUserCount()
		}
		switch {
		case will && !was:
			out.PeopleGain += g.GetUserCount()
		case was && !will:
			out.PeopleLose += g.GetUserCount()
		}
		if r.Msg.GetDryRun() {
			continue
		}
		g.DisabledNodeIds = slices.DeleteFunc(g.DisabledNodeIds, func(id string) bool { return id == node })
		if !will {
			g.DisabledNodeIds = append(g.DisabledNodeIds, node)
		}
	}
	slices.Sort(out.GroupIds)
	if !r.Msg.GetDryRun() {
		w.shareLocked(node).mode = r.Msg.GetMode()
	}
	return connect.NewResponse(out), nil
}

func TestNodeGetAndNodesGetShowWhoGetsTheNode(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	e.w.mu.Lock()
	held := e.w.shareLocked(nodeB)
	held.mode, held.idle = adminv1.NodeShareMode_NODE_SHARE_MODE_NONE, true
	for _, g := range e.w.groups { // a held node is on every group's disabled list
		g.DisabledNodeIds = append(g.DisabledNodeIds, nodeB)
	}
	e.w.mu.Unlock()

	a := decode[NodeGetV](t, mustOK(t, s, "node_get", map[string]any{"node": nodeA}))
	if a.Sharing == nil || a.Sharing.Mode != "all" || a.Sharing.GroupsReceiving != 2 || a.Sharing.GroupsTotal != 2 || a.Sharing.PeopleReceiving != 4 || a.Sharing.Idle || len(a.Sharing.Groups) != 2 {
		t.Fatalf("node_get of a node handed to everyone: %+v", a.Sharing)
	}
	all := decode[NodesGetV](t, mustOK(t, s, "nodes_get", map[string]any{}))
	var b *NodeGetV
	for _, n := range all.Nodes {
		if n.ID == nodeB {
			b = n
		}
	}
	if b == nil || b.Sharing == nil || b.Sharing.Mode != "none" || b.Sharing.GroupsReceiving != 0 || !b.Sharing.Idle || b.Sharing.PeopleReceiving != 0 {
		t.Fatalf("nodes_get of a held node: %+v", b)
	}
	for _, g := range b.Sharing.Groups {
		if g.Receives {
			t.Errorf("group %s receives a held node", g.GroupID)
		}
		if strings.Contains(g.Group, "\n") {
			t.Errorf("group name not cleaned: %q", g.Group)
		}
	}
	noCanary(t, "node_get", mustOK(t, s, "node_get", map[string]any{"node": nodeB}))
}

func TestNodeSharingSetPlansAndApplies(t *testing.T) {
	e := newTestEnv(t)
	_, readonly := e.token(ProfileReadonly)
	if slices.Contains(toolNames(t, e.session(readonly)), "node_sharing_set_plan") {
		t.Fatal("readonly sees the change")
	}
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)

	// invalid asks never reach the panel
	for name, args := range map[string]map[string]any{
		"unknown mode":      {"node": nodeA, "mode": "some"},
		"listed, no group":  {"node": nodeA, "mode": "listed"},
		"none with a group": {"node": nodeA, "mode": "none", "group_ids": []any{"grp_1"}},
		"missing group":     {"node": nodeA, "mode": "listed", "group_ids": []any{"grp_gone"}},
		"missing node":      {"node": "nod_gone", "mode": "none"},
	} {
		mustFail(t, s, "node_sharing_set_plan", args)
		if len(e.w.shareReqs) != 0 {
			t.Fatalf("%s: the panel was asked", name)
		}
	}

	plan := planOf(t, s, "node_sharing_set", map[string]any{"node": nodeA, "mode": "listed", "group_ids": []any{"grp_1"}})
	if !plan.NeedsApproval || len(plan.Danger) != 1 || plan.Danger[0] != dangerFleet {
		t.Fatalf("plan: %+v", plan)
	}
	if f := factIn(t, plan.Facts, "people"); f.Code != "node_sharing_people" || f.Params["gain"] != "0" || f.Params["lose"] != "1" {
		t.Errorf("people fact: %+v", f)
	}
	if f := factIn(t, plan.Facts, "groups_receiving"); !f.Untrusted || f.Value != "family" {
		t.Errorf("groups fact: %+v", f)
	}
	if f := factIn(t, plan.Facts, "sharing"); f.Params["from"] != "all" || f.Params["to"] != "listed" {
		t.Errorf("sharing fact: %+v", f)
	}
	if len(e.w.shareReqs) != 1 || !e.w.shareReqs[0].GetDryRun() {
		t.Fatalf("the plan changed something: %+v", e.w.shareReqs)
	}
	if got := e.w.shares[nodeA]; got != nil && got.mode != adminv1.NodeShareMode_NODE_SHARE_MODE_ALL {
		t.Fatal("planning wrote")
	}
	approvePlan(t, e, plan)
	applyOf(t, s, "node_sharing_set", plan.ConfirmToken)
	if calls := e.w.calls(adminv1connect.GroupServiceSetNodeSharingProcedure); len(calls) != 2 || !calls[0].Planning || calls[1].Approved != plan.PlanID {
		t.Fatalf("grants: %+v", calls)
	}
	if got := e.w.shares[nodeA]; got.mode != adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED || slices.Contains(e.w.groups["grp_1"].DisabledNodeIds, nodeA) || !slices.Contains(e.w.groups["grp_2"].DisabledNodeIds, nodeA) {
		t.Fatalf("applied: %+v %v %v", got, e.w.groups["grp_1"].DisabledNodeIds, e.w.groups["grp_2"].DisabledNodeIds)
	}

	// a plan made against one state is refused when the sharing changed in between
	stale := planOf(t, s, "node_sharing_set", map[string]any{"node": nodeA, "mode": "none"})
	e.w.mu.Lock()
	e.w.groups["grp_2"].DisabledNodeIds = nil // somebody gave the node to grp_2 after the plan
	e.w.mu.Unlock()
	approvePlan(t, e, stale)
	if out := applyError(t, s, "node_sharing_set", stale.ConfirmToken); !strings.Contains(out, "changed after the plan") {
		t.Fatalf("stale plan: %s", out)
	}
}
