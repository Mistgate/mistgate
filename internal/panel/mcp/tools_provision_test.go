package mcp

import (
	"strings"
	"testing"
)

// node_install: the plan reads the host key under the planning grant and shows it with its type; the owner enters the
// password and confirms the key when approving; the apply carries neither, only the plan id the panel uses to find them.
func TestNodeInstallLeavesThePasswordToTheOwnersApproval(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	p := planOf(t, s, "node_install", map[string]any{"host": "203.0.113.30", "username": "root", "name": "edge-1", "address": "edge.example.com"})
	if !p.NeedsApproval {
		t.Fatalf("an install without the owner: %+v", p)
	}
	facts := map[string]Fact{}
	for _, f := range p.Facts {
		facts[f.Key] = f
	}
	// the agent's copy has the key-shaped fingerprint scrubbed; the owner's approval screen gets it whole
	if facts["host_key"].Value != "SHA256:[key]" || facts["host_key"].Untrusted || facts["host_key_algorithm"].Value != "ssh-ed25519" {
		t.Fatalf("host key facts: %+v", p.Facts)
	}
	if stored := e.plans.only(); !strings.Contains(stored.FactsJSON, testHostKey) || !strings.Contains(stored.FactsJSON, "host_key_algorithm") {
		t.Fatalf("the owner's facts lack the host key: %s", stored.FactsJSON)
	}
	if calls := e.w.calls("/mistgate.admin.v1.ProvisioningService/GetSSHFingerprint"); len(calls) != 1 || !calls[0].Planning {
		t.Fatalf("the host key was not read under the planning grant: %+v", calls)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if a := applyOf(t, s, "node_install", p.ConfirmToken); a.Status != StatusApplied {
		t.Fatalf("apply: %+v", a)
	}
	if len(e.w.installReq) != 1 {
		t.Fatalf("StartNodeProvision called %d times", len(e.w.installReq))
	}
	if r := e.w.installReq[0]; r.GetPassword() != "" || r.GetPlanId() != p.PlanID || r.GetFingerprint() != testHostKey || r.GetSshHost() != "203.0.113.30" {
		t.Fatalf("install request: %+v", r)
	}
	if calls := e.w.calls("/mistgate.admin.v1.ProvisioningService/StartNodeProvision"); len(calls) != 1 || calls[0].Approved != p.PlanID {
		t.Fatalf("the install did not run under the plan's grant: %+v", calls)
	}
}

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
