//go:build !js

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	panelupdate "github.com/mistgate/mistgate/internal/panel/update"
)

const mcpOsRetryCount = "retry_count"

// OS updates of nodes (UpdateService.StartOsRollout / PauseOsRollout / ResumeOsRollout / CancelOsRollout): plans and applies
// need the owner's approval and a fresh step-up. The node's reboot checks are never overridden.

func (c *call) osUpdates() (*adminv1.GetOsUpdatesResponse, error) {
	r, err := c.cl.Update.GetOsUpdates(c.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg, nil
}

func osNodeOfID(r *adminv1.GetOsUpdatesResponse, id string) *adminv1.NodeOs {
	for _, n := range r.GetNodes() {
		if n.GetNodeId() == id {
			return n
		}
	}
	return nil
}

// osCancelable: the panel would accept a cancel. A stopped rollout is closed; a running one only while a step has not been sent. A
// job already on the node cannot be stopped, and the panel refuses (os_cancel_inflight): a plan that is certain to be refused is not
// offered for approval.
func osCancelable(r *adminv1.OsRollout) bool {
	switch r.GetStatus() {
	case adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_FAILED:
		return true
	case adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED:
		return true
	case adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING:
		inFlight := false
		for _, s := range r.GetSteps() {
			if s.GetState() == adminv1.OsStepState_OS_STEP_STATE_PENDING {
				return true
			}
			if s.GetState() == adminv1.OsStepState_OS_STEP_STATE_SENT || s.GetState() == adminv1.OsStepState_OS_STEP_STATE_REBOOTING ||
				s.GetState() == adminv1.OsStepState_OS_STEP_STATE_GATING {
				inFlight = true
			}
		}
		return !inFlight
	}
	return false
}

func osRolloutActive(r *adminv1.OsRollout) bool {
	return r.GetStatus() == adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING || r.GetStatus() == adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED
}

type osRolloutStartArgs struct {
	ReasonField
	NodeIDs   []string `json:"node_ids" jsonschema:"one to twenty node ids from os_updates_status"`
	Mode      string   `json:"mode" jsonschema:"security, all (ordinary and security packages), or reboot_only"`
	Reboot    string   `json:"reboot" jsonschema:"for security or all: never or if_required; for reboot_only: if_required or always"`
	BatchSize int32    `json:"batch_size,omitempty" jsonschema:"one to five nodes after the canary; default 1"`
}

// osModeOf maps the tool's words to the RPC's; ok is false for a combination the panel does not offer.
func osModeOf(mode, reboot string) (adminv1.OsRolloutMode, adminv1.OsRebootPolicy, bool) {
	switch {
	case mode == "security" && reboot == "never":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_NEVER, true
	case mode == "security" && reboot == "if_required":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED, true
	case mode == "all" && reboot == "never":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_ALL, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_NEVER, true
	case mode == "all" && reboot == "if_required":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_ALL, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED, true
	case mode == "reboot_only" && reboot == "if_required":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_REBOOT_ONLY, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED, true
	case mode == "reboot_only" && reboot == "always":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_REBOOT_ONLY, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_ALWAYS, true
	}
	return 0, 0, false
}

// osBlockFor is why the node cannot take the job now, "" when it can (the panel's own answer, GetOsUpdates).
func osBlockFor(n *adminv1.NodeOs, mode string) string {
	if mode == "reboot_only" {
		return n.GetRebootBlock()
	}
	if mode == "all" && n.GetUpdateBlock() == "nothing_to_do" && n.GetStatus() != nil && n.GetStatus().GetPending() > 0 {
		return ""
	}
	return n.GetUpdateBlock()
}

type osRolloutPlanNode struct {
	os     *adminv1.NodeOs
	update *adminv1.NodeUpdate
}

// osRolloutSnapshot loads the stored OS state and the agent rollout's canary inputs, then returns the selected nodes in the
// same fewest-online, fewest-inbound order used by SortRolloutCandidates.
func (c *call) osRolloutSnapshot(ids []string) (*adminv1.GetOsUpdatesResponse, *adminv1.GetUpdatesResponse, []*osRolloutPlanNode, error) {
	osUpdates, err := c.osUpdates()
	if err != nil {
		return nil, nil, nil, err
	}
	updates, err := c.updates()
	if err != nil {
		return nil, nil, nil, err
	}
	osByID := make(map[string]*adminv1.NodeOs, len(osUpdates.GetNodes()))
	for _, n := range osUpdates.GetNodes() {
		osByID[n.GetNodeId()] = n
	}
	updateByID := make(map[string]*adminv1.NodeUpdate, len(updates.GetNodes()))
	for _, n := range updates.GetNodes() {
		updateByID[n.GetNodeId()] = n
	}
	var candidates []*adminv1.NodeUpdate
	for _, id := range ids {
		n := osByID[id]
		u := updateByID[id]
		if n == nil || u == nil {
			return nil, nil, nil, errors.New("node_id is not an active node of this panel")
		}
		candidates = append(candidates, u)
	}
	panelupdate.SortRolloutCandidates(candidates)
	ordered := make([]*osRolloutPlanNode, 0, len(candidates))
	for _, u := range candidates {
		ordered = append(ordered, &osRolloutPlanNode{os: osByID[u.GetNodeId()], update: u})
	}
	return osUpdates, updates, ordered, nil
}

func osStartGlobalBlock(osUpdates *adminv1.GetOsUpdatesResponse, agentUpdates *adminv1.GetUpdatesResponse) string {
	if osRolloutActive(osUpdates.GetRollout()) {
		return "rollout_active"
	}
	if r := agentUpdates.GetRollout(); r != nil && (r.GetStatus() == adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING ||
		r.GetStatus() == adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED) {
		return "agent_rollout"
	}
	// the panel states a global blocker through the nodes' own block fields as well
	for _, n := range osUpdates.GetNodes() {
		for _, b := range []string{n.GetUpdateBlock(), n.GetRebootBlock()} {
			if b == "rollout_active" || b == "agent_rollout" || b == "panel_busy" {
				return b
			}
		}
	}
	return ""
}

func osSkipNow(n *adminv1.NodeOs, mode, reboot string) string {
	if reboot != "never" && n.GetStatus() != nil {
		for _, code := range n.GetStatus().GetRebootBlockers() {
			if slices.Contains([]string{"initramfs_missing", "boot_entry_missing", "awg_module_missing", "dpkg_interrupted", "kernel_older", "boot_default_custom"}, code) {
				return code
			}
		}
	}
	block := osBlockFor(n, mode)
	if mode == "all" && block == "nothing_to_do" && n.GetStatus() != nil && n.GetStatus().GetPending() > 0 {
		block = ""
	}
	switch block {
	case "offline", "agent_old", "unsupported", "panel_host", "no_status", "nothing_to_do", "awg_build":
		return block
	case "node_busy", "reboot_wait", "":
	default:
		return block
	}
	if mode == "reboot_only" && reboot == "if_required" && n.GetStatus() != nil && !n.GetStatus().GetRebootRequired() {
		return "not_required"
	}
	if mode == "security" && n.GetStatus() != nil && n.GetStatus().GetSecurity() == 0 && n.GetStatus().GetApt() != "half_configured" {
		return "up_to_date"
	}
	if mode == "all" && n.GetStatus() != nil && n.GetStatus().GetPending() == 0 && n.GetStatus().GetApt() != "half_configured" {
		return "up_to_date"
	}
	return ""
}

func osStartPlanFacts(ordered []*osRolloutPlanNode, mode, reboot string, batch int32) ([]Fact, string) {
	if batch == 0 {
		batch = 1
	}
	action, effect := "os_security_only", "install security updates"
	switch mode {
	case "security":
		if reboot == "if_required" {
			action, effect = "os_security_reboot", "install security updates and reboot when required"
		}
	case "all":
		action, effect = "os_all_updates", "install ordinary and security updates"
	case "reboot_only":
		action, effect = "os_reboot_"+reboot, "reboot according to the selected policy"
	}
	names := make([]string, 0, len(ordered))
	var skipped []string
	var pending, security uint64
	facts := []Fact{codedFact("action", effect, action), {Key: "mode", Value: mode}, {Key: "reboot", Value: reboot},
		{Key: "batch_size", Value: strconv.Itoa(int(batch))}}
	for _, item := range ordered {
		n, status := item.os, item.os.GetStatus()
		name := nm(n.GetName())
		names = append(names, name)
		facts = append(facts, Fact{Key: "node", Value: name, Untrusted: true})
		facts = append(facts,
			Fact{Key: "pending", Value: strconv.FormatUint(uint64(status.GetPending()), 10)},
			Fact{Key: "security", Value: strconv.FormatUint(uint64(status.GetSecurity()), 10)})
		pending += uint64(status.GetPending())
		security += uint64(status.GetSecurity())
		if status != nil {
			if status.GetKernelRunning() != "" {
				kernel := noAddr(clean(status.GetKernelRunning(), 128))
				if status.GetKernelNewest() != "" && status.GetKernelNewest() != status.GetKernelRunning() {
					kernel += " -> " + noAddr(clean(status.GetKernelNewest(), 128))
				}
				facts = append(facts, Fact{Key: "kernel", Value: kernel, Untrusted: true})
			} else {
				facts = append(facts, Fact{Key: "kernel", Value: "unknown"})
			}
			for _, pkg := range status.GetSecurityStuck() {
				facts = append(facts, Fact{Key: "security_stuck", Value: noAddr(clean(pkg, 160)), Untrusted: true})
			}
		} else {
			facts = append(facts, Fact{Key: "kernel", Value: "unknown"})
		}
		blockers := status.GetRebootBlockers()
		facts = append(facts, Fact{Key: "reboot_blockers", Value: strings.Join(blockers, ", "), Untrusted: len(blockers) > 0})
		facts = append(facts, Fact{Key: "reboot_required", Value: strconv.FormatBool(status.GetRebootRequired())},
			Fact{Key: "people_online", Value: strconv.FormatUint(uint64(n.GetOnlineUsers()), 10)})
		if b := osBlockFor(n, mode); b != "" {
			facts = append(facts, Fact{Key: "update_block", Value: b})
		}
		if status.GetAutoReboot() && mode != "reboot_only" {
			facts = append(facts, codedFact("warning", "unattended upgrades can reboot the node by themselves", "os_auto_reboot"))
		}
		if status.GetAwgModuleNext() == "missing" && reboot != "never" {
			facts = append(facts, codedFact("warning", "the AmneziaWG module for the next kernel is missing", "os_awg_module_missing"))
		}
		if reason := osSkipNow(n, mode, reboot); reason != "" {
			skipped = append(skipped, name+" ("+reason+")")
		}
	}
	if len(names) > 0 {
		facts = append(facts, Fact{Key: "canary", Value: names[0], Untrusted: true}, Fact{Key: "order", Value: strings.Join(names, " -> "), Untrusted: true})
	}
	if len(skipped) > 0 {
		facts = append(facts, Fact{Key: "nodes_skipped_now", Value: strings.Join(skipped, ", "), Untrusted: true})
	}
	summary := fmt.Sprintf("Start an OS rollout on %d nodes with batch size %d; the canary and node order are listed in the facts.", len(ordered), batch)
	switch mode {
	case "security":
		summary += fmt.Sprintf(" Selected nodes report %d pending security packages.", security)
	case "all":
		summary += fmt.Sprintf(" Selected nodes report %d pending packages, including %d security packages.", pending, security)
	case "reboot_only":
		summary += " Reboot according to the selected policy."
	}
	return facts, summary
}

// retryOSBlock classifies only the global, temporary blockers returned by GetOsUpdates and StartOsRollout.
func retryOSBlock(reason string) error {
	switch reason {
	case "rollout_active":
		return retryLater("rollout_active", "an OS rollout is active now", retryLaterDelay)
	case "agent_rollout":
		return retryLater("agent_rollout", "an agent rollout is active now", retryLaterDelay)
	case "panel_busy":
		return retryLater("panel_busy", "a panel update or node bundle sync is active now", retryLaterDelay)
	default:
		return nil
	}
}

func retryOsRolloutStart(err error) error {
	message, ok := failedPreconditionMessage(err)
	if !ok {
		return nil
	}
	const prefix = "os_blocked: reason="
	if !strings.HasPrefix(message, prefix) {
		return nil
	}
	return retryOSBlock(strings.TrimPrefix(message, prefix))
}

var osRolloutStart = changeSpec[osRolloutStartArgs]{
	name: "os_rollout_start", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.UpdateServiceGetOsUpdatesProcedure,
		adminv1connect.UpdateServiceStartOsRolloutProcedure),
	desc: "Start an OS rollout on one to twenty nodes: mode security installs security packages, all installs ordinary and security packages, " +
		"or reboot_only changes no packages. The canary runs first, then each batch waits for the previous batch's health gate; batch_size is one to five (default 1). " +
		"Nodes with a reboot blocker are skipped; busy nodes wait up to two hours. People on a rebooting node disconnect for about two minutes. " +
		"The node's reboot checks are never overridden. Needs the owner's approval in the admin panel.",
	plan: func(c *call, a osRolloutStartArgs) (*planned, error) {
		ids, err := uniqueIDs("node_ids", a.NodeIDs, false)
		if err != nil {
			return nil, err
		}
		if len(ids) < 1 || len(ids) > 20 {
			return nil, errors.New("node_ids must contain one to twenty nodes")
		}
		batch := a.BatchSize
		if batch == 0 {
			batch = 1
		}
		if batch < 1 || batch > 5 {
			return nil, errors.New("batch_size must be 1 to 5")
		}
		if _, _, ok := osModeOf(a.Mode, a.Reboot); !ok {
			return nil, errors.New("mode security or all takes reboot never or if_required; mode reboot_only takes if_required or always")
		}
		r, agentUpdates, ordered, err := c.osRolloutSnapshot(ids)
		if err != nil {
			return nil, err
		}
		if why := osStartGlobalBlock(r, agentUpdates); why != "" {
			if retry := retryOSBlock(why); retry != nil {
				return nil, retry
			}
			return nil, errors.New("another rollout is active: " + why)
		}
		facts, summary := osStartPlanFacts(ordered, a.Mode, a.Reboot, batch)
		ids = ids[:0]
		for _, item := range ordered {
			ids = append(ids, item.os.GetNodeId())
		}
		return &planned{
			Summary: summary + " The owner has to approve it first.",
			Facts:   facts, Danger: rolloutDanger, Params: osRolloutStartArgs{NodeIDs: ids, Mode: a.Mode, Reboot: a.Reboot, BatchSize: batch},
		}, nil
	},
	apply: func(c *call, a osRolloutStartArgs, pl Plan) (done, error) {
		r, agentUpdates, ordered, err := c.osRolloutSnapshot(a.NodeIDs)
		if err != nil {
			return done{}, err
		}
		if why := osStartGlobalBlock(r, agentUpdates); why != "" {
			if retry := retryOSBlock(why); retry != nil {
				return done{}, retry
			}
			return done{}, failure("changed_since_plan", "changed since the plan: make a new plan")
		}
		batch := a.BatchSize
		if batch == 0 {
			batch = 1
		}
		facts, _ := osStartPlanFacts(ordered, a.Mode, a.Reboot, batch)
		var plannedFacts []Fact
		if err := json.Unmarshal([]byte(pl.FactsJSON), &plannedFacts); err != nil || !reflect.DeepEqual(facts, plannedFacts) {
			return done{}, failure("changed_since_plan", "changed since the plan: node status or canary order changed; make a new plan")
		}
		mode, reboot, _ := osModeOf(a.Mode, a.Reboot)
		if _, err := c.cl.Update.StartOsRollout(c.ctx, connect.NewRequest(&adminv1.StartOsRolloutRequest{NodeIds: a.NodeIDs, Mode: mode, Reboot: reboot, BatchSize: batch})); err != nil {
			if retry := retryOsRolloutStart(err); retry != nil {
				return done{}, retry
			}
			return done{}, apiError(err)
		}
		return doneWith("os_rollout_started", fmt.Sprintf("The OS rollout was started on %d nodes.", len(a.NodeIDs))), nil
	},
}

type osRolloutCancelArgs struct {
	ReasonField
	RolloutID string `json:"rollout_id" jsonschema:"the rollout id from os_updates_status"`
}

var osRolloutCancel = changeSpec[osRolloutCancelArgs]{
	name: "os_rollout_cancel", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetOsUpdatesProcedure, adminv1connect.UpdateServiceCancelOsRolloutProcedure),
	desc:  "Cancel a running, queued, paused, or stopped OS rollout. A job already running on a node finishes and is checked; no later jobs start. Needs the owner's approval in the admin panel.",
	plan: func(c *call, a osRolloutCancelArgs) (*planned, error) {
		if err := validID("rollout_id", a.RolloutID); err != nil {
			return nil, err
		}
		r, err := c.osUpdates()
		if err != nil {
			return nil, err
		}
		ro := r.GetRollout()
		if ro == nil || ro.GetId() != a.RolloutID {
			return nil, errors.New("no such OS rollout (only the active or last one is known)")
		}
		if !osRolloutActive(ro) && ro.GetStatus() != adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_FAILED {
			return nil, errors.New("the OS rollout is " + enumName("OS_ROLLOUT_STATUS_", ro.GetStatus().String()) + ": nothing to cancel")
		}
		if !osCancelable(ro) {
			return nil, errors.New("a job is running on the node and cannot be stopped; there is nothing to cancel until it ends or fails")
		}
		facts := []Fact{{Key: "rollout", Value: clean(ro.GetId(), 60)}, codedFact("action", "cancel", "cancel")}
		for _, s := range ro.GetSteps() {
			facts = append(facts, Fact{Key: "node", Value: nm(s.GetNodeName()), Untrusted: true})
		}
		return &planned{Summary: "Cancel the OS rollout. Any job already running on a node will finish before it closes. The owner has to approve it first.", Facts: facts, Danger: rolloutDanger}, nil
	},
	apply: func(c *call, a osRolloutCancelArgs, _ Plan) (done, error) {
		r, err := c.osUpdates()
		if err != nil {
			return done{}, err
		}
		if ro := r.GetRollout(); ro == nil || ro.GetId() != a.RolloutID || !osCancelable(ro) {
			return done{}, failure("changed_since_plan", "changed since the plan: make a new plan")
		}
		if _, err := c.cl.Update.CancelOsRollout(c.ctx, connect.NewRequest(&adminv1.CancelOsRolloutRequest{RolloutId: a.RolloutID})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("os_rollout_cancelled", "The OS update was closed."), nil
	},
}

type osRolloutActionArgs struct {
	ReasonField
	RolloutID string `json:"rollout_id" jsonschema:"the rollout id from os_updates_status"`
}

func findOsRollout(c *call, id string) (*adminv1.OsRollout, error) {
	if err := validID("rollout_id", id); err != nil {
		return nil, err
	}
	r, err := c.osUpdates()
	if err != nil {
		return nil, err
	}
	ro := r.GetRollout()
	if ro == nil || ro.GetId() != id {
		return nil, errors.New("no such OS rollout (only the active or last one is known)")
	}
	return ro, nil
}

func osRolloutProgress(r *adminv1.OsRollout) (decided, failed int) {
	for _, step := range r.GetSteps() {
		switch step.GetState() {
		case adminv1.OsStepState_OS_STEP_STATE_PASSED, adminv1.OsStepState_OS_STEP_STATE_FAILED,
			adminv1.OsStepState_OS_STEP_STATE_SKIPPED:
			decided++
		}
		if step.GetState() == adminv1.OsStepState_OS_STEP_STATE_FAILED {
			failed++
		}
	}
	return decided, failed
}

// osRolloutAction mirrors rolloutAction for the OS state machine. The RPC remains bound to this plan's exact rollout id and
// current status, with the same admin profile, owner approval, and step-up checks as os_rollout_cancel.
func osRolloutAction(name, verb, proc, desc string, fits func(adminv1.OsRolloutStatus) bool, doIt func(*call, string) error) changeSpec[osRolloutActionArgs] {
	find := func(c *call, id string) (*adminv1.OsRollout, error) {
		ro, err := findOsRollout(c, id)
		if err != nil {
			return nil, err
		}
		if !fits(ro.GetStatus()) {
			return nil, errors.New("the OS rollout is " + enumName("OS_ROLLOUT_STATUS_", ro.GetStatus().String()) + ": it cannot be " + verb + "ed now")
		}
		return ro, nil
	}
	return changeSpec[osRolloutActionArgs]{
		name: name, min: ProfileAdmin, danger: true,
		procs: procs(adminv1connect.UpdateServiceGetOsUpdatesProcedure, proc),
		desc:  desc + " Needs the owner's approval in the admin panel.",
		plan: func(c *call, a osRolloutActionArgs) (*planned, error) {
			ro, err := find(c, a.RolloutID)
			if err != nil {
				return nil, err
			}
			decided, failed := osRolloutProgress(ro)
			facts := []Fact{
				{Key: "rollout", Value: clean(ro.GetId(), 60)},
				codedFact("progress", fmt.Sprintf("%d of %d nodes decided", decided, len(ro.GetSteps())), "progress", "done", strconv.Itoa(decided), "total", strconv.Itoa(len(ro.GetSteps()))),
				{Key: "failed_steps", Value: strconv.Itoa(failed)},
				codedFact("action", verb, verb),
			}
			for _, step := range ro.GetSteps() {
				facts = append(facts, Fact{Key: "node", Value: nm(step.GetNodeName()), Untrusted: true})
			}
			return &planned{Summary: fmt.Sprintf("%s the OS rollout. The owner has to approve it first.", strings.ToUpper(verb[:1])+verb[1:]),
				Facts: facts, Danger: rolloutDanger, Params: a}, nil
		},
		apply: func(c *call, a osRolloutActionArgs, _ Plan) (done, error) {
			if _, err := find(c, a.RolloutID); err != nil {
				return done{}, failure("changed_since_plan", "changed since the plan: make a new plan ("+err.Error()+")")
			}
			if err := doIt(c, a.RolloutID); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("os_rollout_"+verb+"d", "OS rollout "+verb+"d."), nil
		},
	}
}

type osRolloutResumeArgs struct {
	ReasonField
	RolloutID  string `json:"rollout_id" jsonschema:"the rollout id from os_updates_status"`
	SkipFailed bool   `json:"skip_failed,omitempty" jsonschema:"skip failed steps and continue instead of retrying each failed step once"`
}

var (
	osRolloutPause = osRolloutAction("os_rollout_pause", "pause", adminv1connect.UpdateServicePauseOsRolloutProcedure,
		"Pause an OS rollout. Jobs already running on nodes finish and pass through their health checks; later jobs wait.",
		func(s adminv1.OsRolloutStatus) bool { return s == adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING },
		func(c *call, id string) error {
			_, err := c.cl.Update.PauseOsRollout(c.ctx, connect.NewRequest(&adminv1.PauseOsRolloutRequest{RolloutId: id}))
			return err
		})
	osRolloutResume = changeSpec[osRolloutResumeArgs]{
		name: "os_rollout_resume", min: ProfileAdmin, danger: true,
		procs: procs(adminv1connect.UpdateServiceGetOsUpdatesProcedure, adminv1connect.UpdateServiceResumeOsRolloutProcedure),
		desc:  "Resume a paused OS rollout. Failed steps are retried once by default; set skip_failed to skip them and continue. Needs the owner's approval in the admin panel.",
		plan: func(c *call, a osRolloutResumeArgs) (*planned, error) {
			ro, err := findOsRollout(c, a.RolloutID)
			if err != nil {
				return nil, err
			}
			if ro.GetStatus() != adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED {
				return nil, errors.New("the OS rollout is not paused")
			}
			decided, failed := osRolloutProgress(ro)
			if !a.SkipFailed {
				for _, step := range ro.GetSteps() {
					if step.GetState() == adminv1.OsStepState_OS_STEP_STATE_FAILED && step.GetParams()[mcpOsRetryCount] != "" && step.GetParams()[mcpOsRetryCount] != "0" {
						return nil, errors.New("a failed step was already retried; set skip_failed to continue past it")
					}
				}
			}
			action := "retry failed steps once"
			if a.SkipFailed {
				action = "skip failed steps"
			}
			return &planned{Summary: "Resume the OS rollout and " + action + ". The owner has to approve it first.",
				Facts: []Fact{{Key: "rollout", Value: clean(ro.GetId(), 60)},
					codedFact("progress", fmt.Sprintf("%d of %d nodes decided", decided, len(ro.GetSteps())), "progress", "done", strconv.Itoa(decided), "total", strconv.Itoa(len(ro.GetSteps()))),
					{Key: "failed_steps", Value: strconv.Itoa(failed)}, codedFact("action", action, "resume")},
				Danger: rolloutDanger, Params: a}, nil
		},
		apply: func(c *call, a osRolloutResumeArgs, _ Plan) (done, error) {
			ro, err := findOsRollout(c, a.RolloutID)
			if err != nil || ro.GetStatus() != adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED {
				return done{}, failure("changed_since_plan", "changed since the plan: make a new plan")
			}
			if _, err := c.cl.Update.ResumeOsRollout(c.ctx, connect.NewRequest(&adminv1.ResumeOsRolloutRequest{RolloutId: a.RolloutID, SkipFailed: a.SkipFailed})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("os_rollout_resumed", "OS rollout resumed."), nil
		},
	}
)
