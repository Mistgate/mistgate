package mcp

import (
	"encoding/base64"
	"strings"
	"testing"

	adminv1connect "github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/decoy"
)

func TestNodeDecoyPlanApplyRequiresOwnerApprovalAndUploadsBundle(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("one node's site")}})
	if err != nil {
		t.Fatal(err)
	}
	p := planOf(t, s, "node_decoy", map[string]any{
		"node": nodeA, "mode": decoy.ModeOwn, "bundle_base64": base64.StdEncoding.EncodeToString(bundle.Archive()),
	})
	if !p.NeedsApproval || len(p.Danger) == 0 || len(e.w.nodeDecoyReqs) != 0 {
		t.Fatalf("plan did not wait for owner approval or applied early: %+v requests=%d", p, len(e.w.nodeDecoyReqs))
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	out := applyOf(t, s, "node_decoy", p.ConfirmToken)
	if out.Status != "applied" || !strings.Contains(out.Result, "decoy") {
		t.Fatalf("apply = %+v", out)
	}
	if len(e.w.nodeDecoyReqs) != 1 {
		t.Fatalf("SetNodeDecoyMode called %d times", len(e.w.nodeDecoyReqs))
	}
	request := e.w.nodeDecoyReqs[0]
	if request.GetNodeId() != nodeA || request.GetMode() != decoy.ModeOwn {
		t.Fatalf("request = %+v", request)
	}
	stored, err := decoy.ParseArchive(request.GetBundleArchive())
	if err != nil || stored.Hash() != bundle.Hash() {
		t.Fatalf("uploaded bundle hash = %v, err=%v; want %s", stored, err, bundle.Hash())
	}
	const procedure = adminv1connect.NodeServiceSetNodeDecoyModeProcedure
	calls := e.w.calls(procedure)
	if len(calls) != 1 || calls[0].Approved != p.PlanID || calls[0].Token == "" {
		t.Fatalf("approved API calls = %+v", calls)
	}
	if len(e.audits("mcp_plan")) != 1 || len(e.audits("mcp_apply")) != 1 {
		t.Fatal("MCP plan/apply audit rows were not recorded")
	}
}

func TestNodeDecoyToolDescriptionAvoidsRepeatingOtherSites(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	found := false
	for tool, err := range s.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "node_decoy_plan" {
			found = true
			if !strings.Contains(tool.Description, "never repeat the subject or look") {
				t.Fatalf("node_decoy_plan description omits the site uniqueness rule: %q", tool.Description)
			}
		}
	}
	if !found {
		t.Fatal("node_decoy_plan is missing")
	}
}
