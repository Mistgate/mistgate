//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// OS updates of a node (UpdateService.StartOsRollout / CancelOsRollout): the plan and the apply of one node, with the owner's
// approval and a fresh step-up. The MCP tool starts exactly one node; the node itself checks that a reboot is safe and refuses
// otherwise, and there is no way around that here.

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
	case adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED:
		return true
	case adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING:
		for _, s := range r.GetSteps() {
			if s.GetState() == adminv1.OsStepState_OS_STEP_STATE_PENDING {
				return true
			}
		}
	}
	return false
}

func osRolloutActive(r *adminv1.OsRollout) bool {
	return r.GetStatus() == adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING || r.GetStatus() == adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED
}

type osRolloutStartArgs struct {
	ReasonField
	NodeIDs []string `json:"node_ids" jsonschema:"exactly one node id from os_updates_status"`
	Mode    string   `json:"mode" jsonschema:"security (install the security updates) or reboot_only (no package change)"`
	Reboot  string   `json:"reboot" jsonschema:"for security: never or if_required; for reboot_only: if_required or always"`
}

// osModeOf maps the tool's words to the RPC's; ok is false for a combination the panel does not offer.
func osModeOf(mode, reboot string) (adminv1.OsRolloutMode, adminv1.OsRebootPolicy, bool) {
	switch {
	case mode == "security" && reboot == "never":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_NEVER, true
	case mode == "security" && reboot == "if_required":
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY, adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED, true
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
	return n.GetUpdateBlock()
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
	procs: procs(adminv1connect.UpdateServiceGetOsUpdatesProcedure, adminv1connect.UpdateServiceStartOsRolloutProcedure),
	desc: "Install the security updates on ONE node now (mode security, optionally rebooting when a reboot is needed), or only reboot it " +
		"(mode reboot_only). People on the node are disconnected while it reboots, about two minutes. The node refuses a reboot it cannot do safely; " +
		"there is no override. A reboot soon after the node booted, or after the panel last rebooted it, is refused when planning (reboot_block reboot_wait; reboot_possible_unix says when). For a fleet or a time window there is no tool yet. Needs the owner's approval in the admin panel.",
	plan: func(c *call, a osRolloutStartArgs) (*planned, error) {
		ids, err := uniqueIDs("node_ids", a.NodeIDs, true)
		if err != nil {
			return nil, err
		}
		if len(ids) != 1 {
			return nil, errors.New("node_ids must contain exactly one node")
		}
		if _, _, ok := osModeOf(a.Mode, a.Reboot); !ok {
			return nil, errors.New("mode security takes reboot never or if_required; mode reboot_only takes if_required or always")
		}
		r, err := c.osUpdates()
		if err != nil {
			return nil, err
		}
		n := osNodeOfID(r, ids[0])
		if n == nil {
			return nil, errors.New("node_id is not a node of this panel")
		}
		if why := osBlockFor(n, a.Mode); why != "" {
			return nil, errors.New("this node cannot take the job now: " + why)
		}
		s := n.GetStatus()
		if a.Mode == "reboot_only" && a.Reboot == "if_required" && s != nil && !s.GetRebootRequired() {
			return nil, errors.New("this node does not need a reboot; use reboot always to reboot it anyway")
		}
		action, effect := "os_security_only", "installs the security updates and does not reboot"
		switch {
		case a.Mode == "security" && a.Reboot == "if_required":
			action, effect = "os_security_reboot", "installs the security updates and reboots the node when a reboot is needed"
		case a.Mode == "reboot_only" && a.Reboot == "if_required":
			action, effect = "os_reboot_if_required", "reboots the node because a reboot is needed"
		case a.Mode == "reboot_only":
			action, effect = "os_reboot_always", "reboots the node"
		}
		facts := []Fact{
			{Key: "node", Value: nm(n.GetName()), Untrusted: true},
			codedFact("action", effect, action),
		}
		if a.Mode == "security" {
			facts = append(facts,
				Fact{Key: "pending", Value: strconv.FormatUint(uint64(s.GetPending()), 10)},
				Fact{Key: "security", Value: strconv.FormatUint(uint64(s.GetSecurity()), 10)})
			var stuck []string
			for _, name := range s.GetSecurityStuck() {
				if mcpOSPendingPackage.MatchString(name) {
					stuck = append(stuck, noAddr(clean(name, 96)))
				}
			}
			if len(stuck) > 0 {
				facts = append(facts, Fact{Key: "security_stuck", Value: strings.Join(stuck, ", "), Untrusted: true})
			}
		}
		if s.GetKernelRunning() != "" {
			kernel := noAddr(clean(s.GetKernelRunning(), 128))
			if s.GetKernelNewest() != "" && s.GetKernelNewest() != s.GetKernelRunning() {
				kernel += " -> " + noAddr(clean(s.GetKernelNewest(), 128))
			}
			facts = append(facts, Fact{Key: "kernel", Value: kernel, Untrusted: true})
		}
		if s.GetRebootRequired() {
			facts = append(facts, codedFact("reboot_required", "a reboot is needed", "os_reboot_needed"))
		}
		if a.Reboot != "never" {
			facts = append(facts,
				Fact{Key: "people_online", Value: strconv.FormatUint(uint64(n.GetOnlineUsers()), 10)},
				codedFact("effect", "people on this node are disconnected while it reboots, about two minutes, and reconnect by themselves", "os_reboot_disconnects"),
				codedFact("effect", "old kernels stay installed: if the node does not boot, choose the previous kernel in the provider's console", "os_old_kernels"))
		}
		if s.GetAutoReboot() && a.Mode == "security" {
			facts = append(facts, codedFact("warning", "unattended-upgrades is set to reboot by itself: the node refuses this job until that is turned off", "os_auto_reboot"))
		}
		if s.GetAwgModuleNext() == "missing" && a.Reboot != "never" {
			facts = append(facts, codedFact("warning", "the AmneziaWG module is not built for the new kernel: the node will refuse to reboot", "os_awg_module_missing"))
		}
		return &planned{
			Summary: "Run an operating-system job on node " + nm(n.GetName()) + ": " + effect + ". The owner has to approve it first.",
			Facts:   facts, Danger: rolloutDanger, Params: osRolloutStartArgs{NodeIDs: ids, Mode: a.Mode, Reboot: a.Reboot},
		}, nil
	},
	apply: func(c *call, a osRolloutStartArgs, _ Plan) (done, error) {
		r, err := c.osUpdates()
		if err != nil {
			return done{}, err
		}
		if osRolloutActive(r.GetRollout()) {
			return done{}, retryLater("rollout_active", "an OS rollout is active now", retryLaterDelay)
		}
		n := osNodeOfID(r, a.NodeIDs[0])
		if n == nil {
			return done{}, failure("changed_since_plan", "changed since the plan (the node cannot take the job now): make a new plan")
		}
		if why := osBlockFor(n, a.Mode); why != "" {
			if retry := retryOSBlock(why); retry != nil {
				return done{}, retry
			}
			return done{}, failure("changed_since_plan", "changed since the plan (the node cannot take the job now): make a new plan")
		}
		mode, reboot, _ := osModeOf(a.Mode, a.Reboot)
		if _, err := c.cl.Update.StartOsRollout(c.ctx, connect.NewRequest(&adminv1.StartOsRolloutRequest{NodeIds: a.NodeIDs, Mode: mode, Reboot: reboot})); err != nil {
			if retry := retryOsRolloutStart(err); retry != nil {
				return done{}, retry
			}
			return done{}, apiError(err)
		}
		return doneWith("os_rollout_started", fmt.Sprintf("The OS job on node %s was started.", nm(n.GetName()))), nil
	},
}

type osRolloutCancelArgs struct {
	ReasonField
	RolloutID string `json:"rollout_id" jsonschema:"the rollout id from os_updates_status"`
}

var osRolloutCancel = changeSpec[osRolloutCancelArgs]{
	name: "os_rollout_cancel", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.UpdateServiceGetOsUpdatesProcedure, adminv1connect.UpdateServiceCancelOsRolloutProcedure),
	desc: "Close the OS update of a node: a stopped one (its alert clears), or one that has not started yet. A job already running on the node " +
		"cannot be stopped and is refused here. Needs the owner's approval in the admin panel.",
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
		if !osRolloutActive(ro) {
			return nil, errors.New("the OS rollout is " + enumName("OS_ROLLOUT_STATUS_", ro.GetStatus().String()) + ": nothing to cancel")
		}
		if !osCancelable(ro) {
			return nil, errors.New("a job is running on the node and cannot be stopped; there is nothing to cancel until it ends or fails")
		}
		facts := []Fact{{Key: "rollout", Value: clean(ro.GetId(), 60)}, codedFact("action", "cancel", "cancel")}
		for _, s := range ro.GetSteps() {
			facts = append(facts, Fact{Key: "node", Value: nm(s.GetNodeName()), Untrusted: true})
		}
		return &planned{Summary: "Close the OS update of a node. The owner has to approve it first.", Facts: facts, Danger: rolloutDanger}, nil
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
