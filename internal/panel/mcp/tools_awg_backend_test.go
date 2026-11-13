//go:build !js

package mcp

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

func awgNodeFixture(e *testEnv, fallback bool) {
	e.w.awgStatus = map[string]*adminv1.AwgInboundStatus{nodeA: {Backend: "userspace", Peers: 2000, PeersOnline: 7}}
	e.w.awgFallback = fallback
}

// node_awg_backend: operator profile and up, always the owner's approval, facts from the panel, one UpdateNode under the plan's grant.
func TestNodeAWGBackendWaitsForTheOwnerAndSetsTheBackend(t *testing.T) {
	e := newTestEnv(t)
	awgNodeFixture(e, true)
	_, ro := e.token(ProfileReadonly)
	if slices.Contains(toolNames(t, e.session(ro)), "node_awg_backend_plan") {
		t.Error("readonly sees node_awg_backend_plan")
	}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	p := planOf(t, s, "node_awg_backend", map[string]any{"node": "de1", "backend": "userspace", "reason": "the module is gone after a kernel update"})
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerStepUp {
		t.Fatalf("plan: %+v", p)
	}
	for key, want := range map[string]string{"current_mode": "auto", "new_mode": "userspace", "running_backend": "userspace", "userspace_fallback": "true", "awg_devices_online": "7"} {
		if got := factOf(t, p, key).Value; got != want {
			t.Errorf("fact %s = %q, want %q", key, got, want)
		}
	}
	if got := factOf(t, p, "reconnect").Value; !strings.Contains(got, "20 seconds") {
		t.Errorf("reconnect: %q", got)
	}
	// 2000 devices x 26 KiB = 51 MiB
	if got := factOf(t, p, "userspace_memory").Value; !strings.Contains(got, "51 MiB") || !strings.Contains(got, "2000 devices") {
		t.Errorf("userspace_memory: %q", got)
	}
	if st := storedPlan(t, e, p.PlanID); st.Status != StatusAwaiting || st.Tool != "node_awg_backend" || !strings.Contains(st.ParamsJSON, nodeA) {
		t.Fatalf("stored plan: %+v", st)
	}
	if len(e.w.nodeUpdateReq) != 0 {
		t.Fatal("the plan changed something")
	}
	if got := applyError(t, s, "node_awg_backend", p.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("apply before approval: %q", got)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	ap := applyOf(t, s, "node_awg_backend", p.ConfirmToken)
	if ap.Status != StatusApplied || !strings.Contains(ap.Result, "userspace") {
		t.Fatalf("apply: %+v", ap)
	}
	if len(e.w.nodeUpdateReq) != 1 || e.w.nodeUpdateReq[0].GetNodeId() != nodeA || e.w.nodeUpdateReq[0].GetAwgBackend() != "userspace" {
		t.Fatalf("UpdateNode: %+v", e.w.nodeUpdateReq)
	}
	if calls := e.w.calls(adminv1connect.NodeServiceUpdateNodeProcedure); len(calls) != 1 || calls[0].Approved != p.PlanID {
		t.Errorf("calls: %+v", calls)
	}
	applyOf(t, s, "node_awg_backend", p.ConfirmToken)
	if len(e.w.nodeUpdateReq) != 1 {
		t.Error("a replay changed the node again")
	}
	// it is userspace now: asking for the same again is refused at the plan, kernel is not
	if got := mustFail(t, s, "node_awg_backend_plan", map[string]any{"node": "de1", "backend": "userspace"}); !strings.Contains(got, "already userspace") {
		t.Errorf("same backend: %q", got)
	}
	k := planOf(t, s, "node_awg_backend", map[string]any{"node": "de1", "backend": "kernel"})
	if got := factOf(t, k, "module").Value; !strings.Contains(got, "older one stops AmneziaWG") {
		t.Errorf("module fact: %q", got)
	}
	for _, f := range k.Facts {
		if f.Key == "userspace_memory" {
			t.Errorf("a move to the module quotes userspace memory: %+v", f)
		}
	}
}

func TestNodeAWGBackendRefusesWhatItCannotDo(t *testing.T) {
	e := newTestEnv(t)
	awgNodeFixture(e, false)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	if got := mustFail(t, s, "node_awg_backend_plan", map[string]any{"node": "de1", "backend": "turbo"}); !strings.Contains(got, "auto, kernel or userspace") {
		t.Errorf("bad value: %q", got)
	}
	if got := mustFail(t, s, "node_awg_backend_plan", map[string]any{"node": "nope", "backend": "kernel"}); !strings.Contains(got, "node") {
		t.Errorf("unknown node: %q", got)
	}

	// the setting moved between the plan and the apply: the plan is void
	p := planOf(t, s, "node_awg_backend", map[string]any{"node": "de1", "backend": "kernel"})
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	other := "userspace"
	e.w.nodePatches[nodeA] = &adminv1.UpdateNodeRequest{NodeId: nodeA, AwgBackend: &other}
	if got := applyError(t, s, "node_awg_backend", p.ConfirmToken); !strings.Contains(got, "changed after the plan") {
		t.Fatalf("pin: %q", got)
	}
	if len(e.w.nodeUpdateReq) != 0 {
		t.Fatal("a stale plan changed the node")
	}

	// an agent too old to follow the setting: the panel refuses and the tool says so
	delete(e.w.nodePatches, nodeA)
	e.w.updateNodeErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
	p = planOf(t, s, "node_awg_backend", map[string]any{"node": "de1", "backend": "kernel"})
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if got := applyError(t, s, "node_awg_backend", p.ConfirmToken); !strings.Contains(got, "too old") {
		t.Fatalf("old agent: %q", got)
	}
}
