//go:build !js

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
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
	// the panel adds its key on an approved password install by itself and refuses the flag on a plan's install
	if e.w.installReq[0].GetInstallPanelKey() {
		t.Fatal("the MCP install sent install_panel_key, which the panel refuses for an approved plan")
	}
	if calls := e.w.calls("/mistgate.admin.v1.ProvisioningService/StartNodeProvision"); len(calls) != 1 || calls[0].Approved != p.PlanID {
		t.Fatalf("the install did not run under the plan's grant: %+v", calls)
	}
}

func TestNodeServerKeysUseOwnerApprovedPlans(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	// the whole fingerprint is key-shaped and scrubbed; the key goes out by its key_id, which the remove plan takes back
	keyID := strings.TrimPrefix(testPanelKeyFingerprint, "SHA256:")[:12]
	listed := mustOK(t, s, "node_server_keys_list", map[string]any{"node": nodeA})
	if !strings.Contains(listed, `"key_id":"`+keyID+`"`) || !strings.Contains(listed, `"panel_key":true`) {
		t.Fatalf("server key list omitted the panel key metadata: %s", listed)
	}

	add := planOf(t, s, "node_server_key_add", map[string]any{"node": nodeA, "panel_key": true})
	if !add.NeedsApproval {
		t.Fatalf("adding a server key did not require approval: %+v", add)
	}
	if err := e.plans.decide(add.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if result := applyOf(t, s, "node_server_key_add", add.ConfirmToken); result.Status != StatusApplied {
		t.Fatalf("add apply: %+v", result)
	}
	if len(e.w.addServerKeyReq) != 1 {
		t.Fatalf("AddNodeServerKey called %d times", len(e.w.addServerKeyReq))
	}
	added := e.w.addServerKeyReq[0]
	if added.GetNodeId() != nodeA || !added.GetPanelKey() || !added.GetConfirm() || added.GetPublicKey() != "" {
		t.Fatalf("panel key add request: %+v", added)
	}
	if calls := e.w.calls("/mistgate.admin.v1.ProvisioningService/AddNodeServerKey"); len(calls) != 1 || calls[0].Approved != add.PlanID {
		t.Fatalf("key addition did not use its exact approved plan: %+v", calls)
	}

	remove := planOf(t, s, "node_server_key_remove", map[string]any{"node": nodeA, "key": keyID})
	if !remove.NeedsApproval || !strings.Contains(strings.Join(remove.Danger, " "), "saved password") {
		t.Fatalf("panel key removal did not explain recovery and require approval: %+v", remove)
	}
	if err := e.plans.decide(remove.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if result := applyOf(t, s, "node_server_key_remove", remove.ConfirmToken); result.Status != StatusApplied {
		t.Fatalf("remove apply: %+v", result)
	}
	if len(e.w.removeServerKeyReq) != 1 {
		t.Fatalf("RemoveNodeServerKey called %d times", len(e.w.removeServerKeyReq))
	}
	removed := e.w.removeServerKeyReq[0]
	if removed.GetNodeId() != nodeA || removed.GetFingerprint() != testPanelKeyFingerprint || !removed.GetConfirm() || !removed.GetConfirmPanelKey() {
		t.Fatalf("panel key remove request: %+v", removed)
	}
	if calls := e.w.calls("/mistgate.admin.v1.ProvisioningService/RemoveNodeServerKey"); len(calls) != 1 || calls[0].Approved != remove.PlanID {
		t.Fatalf("key removal did not use its exact approved plan: %+v", calls)
	}

	if out := mustFail(t, s, "node_server_key_add_plan", map[string]any{"node": nodeA, "public_key": "-----BEGIN OPENSSH PRIVATE KEY-----"}); !strings.Contains(out, "private keys are never accepted") {
		t.Fatalf("private key input was not rejected: %s", out)
	}
}

func TestNodeServerKeysAddBulkPlanAndPartialApply(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "node_server_keys_add", map[string]any{"nodes": []any{nodeC, nodeA, nodeB}, "panel_key": true})
	if !p.NeedsApproval || !slices.Contains(p.Danger, dangerStepUp) || !slices.Contains(p.Danger, dangerBulk) {
		t.Fatalf("bulk SSH key plan did not require owner step-up approval: %+v", p)
	}
	var names []string
	var hasMaskedFingerprint, hasComment, hasSkip bool
	for _, fact := range p.Facts {
		switch fact.Key {
		case "node":
			names = append(names, fact.Value)
			if !fact.Untrusted {
				t.Errorf("node name is not marked untrusted: %+v", fact)
			}
		case "fingerprint":
			hasMaskedFingerprint = fact.Value == "SHA256:[key]"
		case "comment":
			hasComment = fact.Value == "mistgate-panel" && fact.Untrusted
		case "already_present":
			hasSkip = fact.Value == "skipped"
		}
	}
	if strings.Join(names, ",") != "fi1,de1,nl1" || !hasMaskedFingerprint || !hasComment || !hasSkip {
		t.Fatalf("bulk SSH key facts missed key metadata or node skip: names=%v fingerprint=%v comment=%v skip=%v facts=%+v", names, hasMaskedFingerprint, hasComment, hasSkip, p.Facts)
	}
	stored := storedPlan(t, e, p.PlanID)
	var storedFacts []Fact
	if err := json.Unmarshal([]byte(stored.FactsJSON), &storedFacts); err != nil || !slices.ContainsFunc(storedFacts, func(f Fact) bool { return f.Key == "fingerprint" && f.Value == testPanelKeyFingerprint }) {
		t.Fatalf("owner approval facts do not contain the full key fingerprint: %+v (%v)", storedFacts, err)
	}
	var pinned serverKeysAddParams
	if err := json.Unmarshal([]byte(stored.ParamsJSON), &pinned); err != nil || pinned.Fingerprint != testPanelKeyFingerprint || len(pinned.Nodes) != 3 ||
		pinned.Nodes[0].NodeID != nodeC || pinned.Nodes[1].NodeID != nodeA || pinned.Nodes[2].NodeID != nodeB {
		t.Fatalf("bulk plan did not pin the exact key and node set: %+v (%v)", pinned, err)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	e.w.mu.Lock()
	e.w.serverKeyAddErrNode = nodeB
	e.w.serverKeyAddErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	e.w.mu.Unlock()
	raw := mustOK(t, s, "node_server_keys_add_apply", map[string]any{"confirm_token": p.ConfirmToken})
	noCanary(t, "node_server_keys_add_apply", raw)
	out := decode[serverKeysAddApplyOut](t, raw)
	if out.Status != StatusApplied || out.PlanID != p.PlanID || !strings.Contains(out.Result, "1 added, 1 already present, 1 failed") {
		t.Fatalf("bulk SSH key apply result: %+v", out)
	}
	if len(out.Results) != 3 || out.Results[0] != (serverKeysAddNodeResult{Node: "fi1", Status: "added"}) ||
		out.Results[1] != (serverKeysAddNodeResult{Node: "de1", Status: "already_present"}) ||
		out.Results[2] != (serverKeysAddNodeResult{Node: "nl1", Status: "failed", ErrorCode: "node_retired"}) {
		t.Fatalf("bulk SSH key per-node results: %+v", out.Results)
	}
	if len(e.w.addServerKeyReq) != 2 || e.w.addServerKeyReq[0].GetNodeId() != nodeC || e.w.addServerKeyReq[1].GetNodeId() != nodeB ||
		!e.w.addServerKeyReq[0].GetPanelKey() || !e.w.addServerKeyReq[1].GetPanelKey() {
		t.Fatalf("bulk SSH key additions were not sequential or skipped the present key: %+v", e.w.addServerKeyReq)
	}
	if calls := e.w.calls("/mistgate.admin.v1.ProvisioningService/AddNodeServerKey"); len(calls) != 2 || calls[0].Approved != p.PlanID || calls[1].Approved != p.PlanID {
		t.Fatalf("bulk key additions did not use the approved plan grant: %+v", calls)
	}
	stored = storedPlan(t, e, p.PlanID)
	if stored.Status != StatusApplied || stored.OutcomeCode != "node_ssh_keys_add_completed" || !strings.Contains(stored.OutcomeParams, "node_retired") {
		t.Fatalf("partial key failure was not persisted as an applied plan: %+v", stored)
	}
	// A replay returns the same per-node results without adding a key again.
	replay := decode[serverKeysAddApplyOut](t, mustOK(t, s, "node_server_keys_add_apply", map[string]any{"confirm_token": p.ConfirmToken}))
	if replay.Status != StatusApplied || len(replay.Results) != 3 || len(e.w.addServerKeyReq) != 2 {
		t.Fatalf("bulk key result replay changed or repeated side effects: %+v calls=%d", replay, len(e.w.addServerKeyReq))
	}
}

func TestNodeServerKeysAddBulkLimitsAndSharedValidation(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	if got := mustFail(t, s, "node_server_keys_add_plan", map[string]any{"nodes": []any{}, "panel_key": true}); !strings.Contains(got, "at least one node id") {
		t.Fatalf("empty bulk key node list was accepted: %s", got)
	}
	if got := mustFail(t, s, "node_server_keys_add_plan", map[string]any{"nodes": []any{nodeA, nodeA}, "panel_key": true}); !strings.Contains(got, "duplicate node id") {
		t.Fatalf("duplicate bulk key node id was accepted: %s", got)
	}
	if got := mustFail(t, s, "node_server_keys_add_plan", map[string]any{"nodes": []any{"nod_unknown00000000000000000000"}, "panel_key": true}); !strings.Contains(got, "no saved SSH access") {
		t.Fatalf("unknown bulk key node id was accepted: %s", got)
	}
	tooMany := make([]any, maxMCPNodeTargets+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("mcp_keys_%02d", i)
	}
	if got := mustFail(t, s, "node_server_keys_add_plan", map[string]any{"nodes": tooMany, "panel_key": true}); !strings.Contains(got, "at most 20") {
		t.Fatalf("more than 20 bulk key nodes were accepted: %s", got)
	}
	if got := mustFail(t, s, "node_server_keys_add_plan", map[string]any{"nodes": []any{nodeA, nodeB}, "public_key": "-----BEGIN OPENSSH PRIVATE KEY-----"}); !strings.Contains(got, "private keys are never accepted") {
		t.Fatalf("bulk key plan did not use single-node public-key validation: %s", got)
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
