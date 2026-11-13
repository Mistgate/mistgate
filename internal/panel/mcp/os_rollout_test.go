//go:build !js

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// os_rollout_start and os_rollout_cancel: the owner approves, the apply runs exactly what the plan pinned, and nothing runs before.

func (e *testEnv) setOsRollout(ro *adminv1.OsRollout) {
	e.w.mu.Lock()
	e.w.osUpdates.Rollout = ro
	e.w.mu.Unlock()
}

func (e *testEnv) osStarts() []*adminv1.StartOsRolloutRequest {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return append([]*adminv1.StartOsRolloutRequest(nil), e.w.osStartReq...)
}

func (e *testEnv) osCancels() []*adminv1.CancelOsRolloutRequest {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return append([]*adminv1.CancelOsRolloutRequest(nil), e.w.osCancelReq...)
}

func TestOsRolloutStartNeedsApprovalAndRunsThePinnedNode(t *testing.T) {
	e := newTestEnv(t)
	e.w.mu.Lock()
	e.w.osUpdates.Nodes[0].Status.SecurityStuck = []string{"libfoo1"}
	e.w.mu.Unlock()
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	args := map[string]any{"node_ids": []any{nodeA}, "mode": "security", "reboot": "if_required", "batch_size": 2}

	p := planOf(t, s, "os_rollout_start", args)
	if !p.NeedsApproval || len(p.Danger) != 2 || p.Danger[0] != "step_up" || p.Danger[1] != "fleet" {
		t.Fatalf("plan: %+v", p)
	}
	var keys []string
	stuckFact := false
	for _, f := range p.Facts {
		keys = append(keys, f.Key)
		if f.Key == "security_stuck" {
			stuckFact = f.Untrusted && f.Value == "libfoo1"
		}
		if f.Key == "node" && (!f.Untrusted || f.Value != "de1") {
			t.Errorf("the node name is data: %+v", f)
		}
	}
	for _, want := range []string{"node", "action", "pending", "security", "kernel", "people_online", "reboot_required", "reboot_blockers", "batch_size", "mode", "reboot", "order", "security_stuck"} {
		if !strings.Contains(strings.Join(keys, ","), want) {
			t.Errorf("plan facts %v lack %q", keys, want)
		}
	}
	if !stuckFact {
		t.Errorf("plan did not list node-supplied security_stuck names as untrusted: %+v", p.Facts)
	}
	factValues := map[string]string{}
	for _, f := range p.Facts {
		if f.Key == "mode" || f.Key == "reboot" || f.Key == "batch_size" || f.Key == "canary" {
			factValues[f.Key] = f.Value
		}
	}
	if factValues["mode"] != "security" || factValues["reboot"] != "if_required" || factValues["batch_size"] != "2" || factValues["canary"] != "de1" {
		t.Errorf("plan pins %v", factValues)
	}

	// before the owner decides, the apply is refused and nothing runs
	applyError(t, s, "os_rollout_start", p.ConfirmToken)
	if got := e.osStarts(); len(got) != 0 {
		t.Fatalf("StartOsRollout ran before the approval: %+v", got)
	}
	e.plans.decide(p.PlanID, true)
	a := applyOf(t, s, "os_rollout_start", p.ConfirmToken)
	got := e.osStarts()
	if len(got) != 1 || len(got[0].GetNodeIds()) != 1 || got[0].GetNodeIds()[0] != nodeA ||
		got[0].GetMode() != adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY || got[0].GetReboot() != adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED || got[0].GetBatchSize() != 2 {
		t.Fatalf("StartOsRollout got %+v, want the planned node, mode and reboot", got)
	}
	if c := e.w.calls("/mistgate.admin.v1.UpdateService/StartOsRollout"); len(c) != 1 || c[0].Approved != p.PlanID || c[0].Planning {
		t.Errorf("the call did not run under the owner's grant for this plan: %+v", c)
	}
	// a replay returns the stored result and starts nothing
	if b := applyOf(t, s, "os_rollout_start", p.ConfirmToken); b != a || len(e.osStarts()) != 1 {
		t.Errorf("replay: %+v vs %+v, starts %d", b, a, len(e.osStarts()))
	}
}

func TestOsRolloutStartRebootOnlyPinsItsPolicy(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "os_rollout_start", map[string]any{"node_ids": []any{nodeA}, "mode": "reboot_only", "reboot": "always"})
	e.plans.decide(p.PlanID, true)
	applyOf(t, s, "os_rollout_start", p.ConfirmToken)
	got := e.osStarts()
	if len(got) != 1 || got[0].GetMode() != adminv1.OsRolloutMode_OS_ROLLOUT_MODE_REBOOT_ONLY || got[0].GetReboot() != adminv1.OsRebootPolicy_OS_REBOOT_POLICY_ALWAYS {
		t.Fatalf("StartOsRollout got %+v", got)
	}
}

func TestOsRolloutStartRefusedAtThePlan(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	for name, args := range map[string]map[string]any{
		"no node":           {"node_ids": []any{}, "mode": "security", "reboot": "never"},
		"an unknown node":   {"node_ids": []any{"nod_nobody"}, "mode": "security", "reboot": "never"},
		"security always":   {"node_ids": []any{nodeA}, "mode": "security", "reboot": "always"},
		"reboot only never": {"node_ids": []any{nodeA}, "mode": "reboot_only", "reboot": "never"},
		"batch too large":   {"node_ids": []any{nodeA}, "mode": "security", "reboot": "never", "batch_size": 6},
		"a missing mode":    {"node_ids": []any{nodeA}, "reboot": "never"},
	} {
		if got := mustFail(t, s, "os_rollout_start_plan", args); got == "" {
			t.Errorf("%s: no refusal", name)
		}
	}
}

func TestOsRolloutStartPlansMultipleNodesAndAllMode(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "os_rollout_start", map[string]any{"node_ids": []any{nodeA, nodeB}, "mode": "security", "reboot": "never"})
	if !strings.Contains(p.Summary, "2 nodes") {
		t.Fatalf("two-node plan: %+v", p)
	}
	skippedOffline := false
	for _, f := range p.Facts {
		if f.Key == "nodes_skipped_now" && strings.Contains(f.Value, "nl1 (offline)") && f.Untrusted {
			skippedOffline = true
		}
	}
	if !skippedOffline {
		t.Fatalf("plan did not identify the currently offline node: %+v", p.Facts)
	}

	all := planOf(t, s, "os_rollout_start", map[string]any{"node_ids": []any{nodeA}, "mode": "all", "reboot": "never", "batch_size": 3})
	if !strings.Contains(all.Summary, "3 pending packages") {
		t.Fatalf("mode all summary lacks the pending count: %q", all.Summary)
	}
	e.plans.decide(all.PlanID, true)
	applyOf(t, s, "os_rollout_start", all.ConfirmToken)
	got := e.osStarts()
	if len(got) != 1 || got[0].GetMode() != adminv1.OsRolloutMode_OS_ROLLOUT_MODE_ALL || got[0].GetReboot() != adminv1.OsRebootPolicy_OS_REBOOT_POLICY_NEVER || got[0].GetBatchSize() != 3 {
		t.Fatalf("all-mode StartOsRollout request = %+v", got)
	}
}

func TestOsRolloutStartAppliesNothingThatChangedSinceThePlan(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	args := map[string]any{"node_ids": []any{nodeA}, "mode": "security", "reboot": "if_required", "batch_size": 2}

	p := planOf(t, s, "os_rollout_start", args)
	e.plans.decide(p.PlanID, true)
	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING})
	if got := applyError(t, s, "os_rollout_start", p.ConfirmToken); !strings.Contains(got, "busy (rollout_active)") {
		t.Errorf("apply with another update running: %q", got)
	}
	if len(e.osStarts()) != 0 {
		t.Fatal("StartOsRollout ran while another OS update was active")
	}
	e.setOsRollout(nil)

	for _, tc := range []struct {
		block, want string
		status      string
	}{
		{block: "rollout_active", want: "busy (rollout_active)", status: StatusApproved},
		{block: "agent_rollout", want: "busy (agent_rollout)", status: StatusApproved},
		{block: "panel_busy", want: "busy (panel_busy)", status: StatusApproved},
		{block: "node_busy", want: "changed since the plan", status: StatusFailed},
	} {
		p = planOf(t, s, "os_rollout_start", args)
		e.plans.decide(p.PlanID, true)
		e.w.mu.Lock()
		e.w.osUpdates.Nodes[0].UpdateBlock = tc.block
		e.w.mu.Unlock()
		if got := applyError(t, s, "os_rollout_start", p.ConfirmToken); !strings.Contains(got, tc.want) {
			t.Errorf("block %s: apply refusal = %q, want %q", tc.block, got, tc.want)
		}
		e.w.mu.Lock()
		e.w.osUpdates.Nodes[0].UpdateBlock = ""
		e.w.mu.Unlock()
		stored, err := e.plans.GetMCPPlan(context.Background(), p.PlanID)
		if err != nil || stored.Status != tc.status {
			t.Errorf("block %s left plan %+v, %v; want %s", tc.block, stored, err, tc.status)
		}
	}
	if len(e.osStarts()) != 0 {
		t.Fatal("StartOsRollout ran while a refusal was active")
	}
}

func TestOsRolloutStartRaceBlockCanRetrySameApproval(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "os_rollout_start", map[string]any{"node_ids": []any{nodeA}, "mode": "security", "reboot": "if_required"})
	e.plans.decide(p.PlanID, true)
	e.w.mu.Lock()
	e.w.osStartErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("os_blocked: reason=agent_rollout"))
	e.w.mu.Unlock()
	if got := applyError(t, s, "os_rollout_start", p.ConfirmToken); !strings.Contains(got, "busy (agent_rollout)") {
		t.Fatalf("raced agent rollout refusal = %q", got)
	}
	if got, _ := e.plans.GetMCPPlan(context.Background(), p.PlanID); got.Status != StatusApproved {
		t.Fatalf("retryable refusal left plan %s", got.Status)
	}
	e.w.mu.Lock()
	e.w.osStartErr = nil
	e.w.mu.Unlock()
	if out := applyOf(t, s, "os_rollout_start", p.ConfirmToken); out.Status != StatusApplied {
		t.Fatalf("retry with same approved plan = %+v", out)
	}
}

func TestOsRolloutRetryClassificationIsNarrow(t *testing.T) {
	for reason, code := range map[string]string{
		"rollout_active": "rollout_active",
		"agent_rollout":  "agent_rollout",
		"panel_busy":     "panel_busy",
	} {
		err := connect.NewError(connect.CodeFailedPrecondition, errors.New("os_blocked: reason="+reason))
		retry := retryOsRolloutStart(err)
		var typed *retryLaterFailure
		if !errors.As(retry, &typed) || typed.code != code {
			t.Errorf("%q classified as %v, want %q", reason, retry, code)
		}
	}
	if retry := retryOsRolloutStart(connect.NewError(connect.CodeFailedPrecondition, errors.New("os_blocked: reason=node_busy"))); retry != nil {
		t.Errorf("node busy classified as retryable: %v", retry)
	}
}

func TestOsRolloutCancelPlanIsOnlyOfferedWhenItCanWork(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	args := map[string]any{"rollout_id": "osr_1"}

	if got := mustFail(t, s, "os_rollout_cancel_plan", args); !strings.Contains(got, "no such OS rollout") {
		t.Errorf("no rollout: %q", got)
	}
	step := func(state adminv1.OsStepState) []*adminv1.OsStep {
		return []*adminv1.OsStep{{NodeId: nodeA, NodeName: "de1", State: state}}
	}
	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_DONE, Steps: step(adminv1.OsStepState_OS_STEP_STATE_PASSED)})
	if got := mustFail(t, s, "os_rollout_cancel_plan", args); !strings.Contains(got, "nothing to cancel") {
		t.Errorf("a finished rollout: %q", got)
	}
	// a job that is running on the node cannot be stopped: the panel would refuse, so the owner is not asked
	for _, st := range []adminv1.OsStepState{adminv1.OsStepState_OS_STEP_STATE_SENT, adminv1.OsStepState_OS_STEP_STATE_REBOOTING, adminv1.OsStepState_OS_STEP_STATE_GATING} {
		e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING, Steps: step(st)})
		if got := mustFail(t, s, "os_rollout_cancel_plan", args); !strings.Contains(got, "cannot be stopped") {
			t.Errorf("%v: %q", st, got)
		}
	}

	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED, Steps: step(adminv1.OsStepState_OS_STEP_STATE_FAILED)})
	p := planOf(t, s, "os_rollout_cancel", args)
	if !p.NeedsApproval || len(p.Danger) != 2 {
		t.Fatalf("plan: %+v", p)
	}
	applyError(t, s, "os_rollout_cancel", p.ConfirmToken)
	if n := len(e.osCancels()); n != 0 {
		t.Fatal("CancelOsRollout ran before the approval")
	}
	e.plans.decide(p.PlanID, true)
	applyOf(t, s, "os_rollout_cancel", p.ConfirmToken)
	if got := e.osCancels(); len(got) != 1 || got[0].GetRolloutId() != "osr_1" {
		t.Fatalf("CancelOsRollout got %+v", got)
	}
	if c := e.w.calls("/mistgate.admin.v1.UpdateService/CancelOsRollout"); len(c) != 1 || c[0].Approved != p.PlanID {
		t.Errorf("the call did not run under the owner's grant: %+v", c)
	}

	// the rollout changed between the plan and the approval
	p = planOf(t, s, "os_rollout_cancel", args)
	e.plans.decide(p.PlanID, true)
	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING, Steps: step(adminv1.OsStepState_OS_STEP_STATE_SENT)})
	if got := applyError(t, s, "os_rollout_cancel", p.ConfirmToken); !strings.Contains(got, "changed since the plan") {
		t.Errorf("apply after the rollout moved on: %q", got)
	}
}

func TestOsRolloutCancelClosesFailedRollout(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_FAILED,
		Steps: []*adminv1.OsStep{{NodeId: nodeA, NodeName: "de1", State: adminv1.OsStepState_OS_STEP_STATE_FAILED}}})
	p := planOf(t, s, "os_rollout_cancel", map[string]any{"rollout_id": "osr_1"})
	e.plans.decide(p.PlanID, true)
	applyOf(t, s, "os_rollout_cancel", p.ConfirmToken)
	if got := e.osCancels(); len(got) != 1 || got[0].GetRolloutId() != "osr_1" {
		t.Fatalf("failed rollout was not closed: %+v", got)
	}
}

func TestOsRolloutPauseResumeToolsNeedApprovalAndKeepOptions(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	step := func(state adminv1.OsStepState, params map[string]string) []*adminv1.OsStep {
		return []*adminv1.OsStep{{NodeId: nodeA, NodeName: "de1", State: state, Params: params}}
	}
	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING, Steps: step(adminv1.OsStepState_OS_STEP_STATE_PENDING, nil)})
	args := map[string]any{"rollout_id": "osr_1"}
	pause := planOf(t, s, "os_rollout_pause", args)
	applyError(t, s, "os_rollout_pause", pause.ConfirmToken)
	if len(e.w.osPauseReq) != 0 {
		t.Fatal("PauseOsRollout ran before owner approval")
	}
	e.plans.decide(pause.PlanID, true)
	applyOf(t, s, "os_rollout_pause", pause.ConfirmToken)
	if len(e.w.osPauseReq) != 1 || e.w.osPauseReq[0].GetRolloutId() != "osr_1" {
		t.Fatalf("pause requests %+v", e.w.osPauseReq)
	}

	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED,
		Steps: step(adminv1.OsStepState_OS_STEP_STATE_FAILED, nil)})
	resume := planOf(t, s, "os_rollout_resume", args)
	applyError(t, s, "os_rollout_resume", resume.ConfirmToken)
	if len(e.w.osResumeReq) != 0 {
		t.Fatal("ResumeOsRollout ran before owner approval")
	}
	e.plans.decide(resume.PlanID, true)
	applyOf(t, s, "os_rollout_resume", resume.ConfirmToken)
	if len(e.w.osResumeReq) != 1 || e.w.osResumeReq[0].GetRolloutId() != "osr_1" || e.w.osResumeReq[0].GetSkipFailed() {
		t.Fatalf("default resume request %+v", e.w.osResumeReq)
	}

	e.setOsRollout(&adminv1.OsRollout{Id: "osr_1", Status: adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED,
		Steps: step(adminv1.OsStepState_OS_STEP_STATE_FAILED, map[string]string{mcpOsRetryCount: "1"})})
	skip := planOf(t, s, "os_rollout_resume", map[string]any{"rollout_id": "osr_1", "skip_failed": true})
	e.plans.decide(skip.PlanID, true)
	applyOf(t, s, "os_rollout_resume", skip.ConfirmToken)
	if len(e.w.osResumeReq) != 2 || !e.w.osResumeReq[1].GetSkipFailed() {
		t.Fatalf("skip_failed resume request %+v", e.w.osResumeReq)
	}
}
