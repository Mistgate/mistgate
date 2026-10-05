package mcp

import (
	"strings"
	"testing"
)

// A retired node keeps its saved access and names are reused: a name means the live node, and a retired one's server is
// left alone. The panel generates the new password itself; it never passes through the MCP layer.
func TestPasswordRotationTargetsTheLiveNodeAndThePanelGenerates(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	p := planOf(t, s, "node_server_password_rotate", map[string]any{"node": "de1"})
	if !p.NeedsApproval {
		t.Fatalf("rotation without the owner: %+v", p)
	}
	if stored := e.plans.only(); !strings.Contains(stored.ParamsJSON, nodeA) || strings.Contains(stored.ParamsJSON, nodeRetired) {
		t.Fatalf("plan picked the wrong node: %s", stored.ParamsJSON)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if a := applyOf(t, s, "node_server_password_rotate", p.ConfirmToken); a.Status != StatusApplied {
		t.Fatalf("apply: %+v", a)
	}
	if len(e.w.rotateReq) != 1 || e.w.rotateReq[0].GetNodeId() != nodeA || !e.w.rotateReq[0].GetGenerate() || e.w.rotateReq[0].GetNewPassword() != "" {
		t.Fatalf("rotation request: %+v", e.w.rotateReq)
	}

	if out := mustFail(t, s, "node_server_password_rotate_plan", map[string]any{"node": nodeRetired}); !strings.Contains(out, "retired") {
		t.Fatalf("plan for a retired node: %s", out)
	}
}
