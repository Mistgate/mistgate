package update

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The OS rollout: OS jobs (agent.proto "OS UPDATES") run on nodes from the panel, with the owner's approval. Like the agent
// rollout it is a state machine over database rows that the worker pass (tickOs) looks at every few seconds; commands run in
// its own goroutine because it waits for the node's answer and records only what it got. Everything else is decided from what
// the pass can observe (the node's snapshot and events of this job id, its connection, its boot time), so a panel restart in
// any step loses nothing.
//
//	pending   -> sent        UpdateOs went out under a new job id
//	sent      -> rebooting   the job reached its reboot
//	sent      -> gating      the job is over without a reboot (or the host rebooted unseen)
//	sent      -> failed      a hard refusal or job failure, or nothing was heard for OsJobWait
//	rebooting -> gating      the node is back on a new boot
//	rebooting -> failed      the job failed after the reboot, or the node did not return within OsBackWait
//	gating    -> passed      the job is settled and the node's health gate passes
//	gating    -> failed      the kernel is not the expected one, a profile broke, or the gate did not pass in OsGateWait
//	pending   -> pending     waiting_retry until a self-closing refusal ends, at most two hours
//	pending   -> skipped     offline, unsupported, the panel's own server, nothing to do, a hard blocker, timeout, or cancelled
//
// A failed step pauses the rollout (the alert os_update_failed is open) until the owner resumes or cancels it: packages cannot
// be rolled back, and no later batch starts meanwhile. A refusal where nothing was changed and the node only asks to wait
// (osRetryLater) stays pending and is retried for up to two hours.

// OsJobRequest is the enumerated content of UpdateOs (agent.proto): nothing else is ever sent to a node.
type OsJobRequest struct{ Action, Mode, Reboot, JobID string }

// The panel's own step error keys (the node's codes come through in params.code).
const (
	osErrPanelHost     = "panel_host"
	osErrBusy          = "busy"
	osErrNotRequired   = "not_required"
	osErrAgentRefused  = "agent_refused"
	osErrJobFailed     = "job_failed"
	osErrRebootHeld    = "reboot_held"
	osErrNotBack       = "not_back"
	osErrKernel        = "kernel_not_switched"
	osErrJobNotSettled = "job_not_settled"
	osErrNodeLost      = "node_lost" // a job without a reboot: the node dropped off while the panel was checking it
)

// Pause codes of an OS rollout (OsRollout.pause_key is "os.pause.<code>"); step_failed and gate_failed are the agent rollout's.
const (
	pauseOsRebootHeld = "reboot_held"
	pauseOsNotBack    = "not_back"
	pauseOsCancelWait = "cancel_wait"
)

const osErrSkippedAfterFailure = "skipped_after_failure"

// Why StartOsRollout is refused (OsNode.update_block); the same words in the SPA's "os.block.<key>".
const (
	blockOffline      = "offline"
	blockAgentOld     = "agent_old"
	blockUnsupported  = "unsupported"
	blockPanelHost    = "panel_host"
	blockNodeBusy     = "node_busy"
	blockNoStatus     = "no_status"
	blockNothingToDo  = "nothing_to_do"
	blockRolloutOn    = "rollout_active"
	blockAgentRollout = "agent_rollout"
	blockPanelBusy    = "panel_busy"
	blockAwgBuild     = "awg_build"
)

// osBootJitter: the agent derives the boot time from the uptime and it jitters by a second or two; only a bigger step is a boot.
const osBootJitter = time.Minute

// osResendWindow: the same job id is sent again after a lost answer only in the first minutes. Later the node may have run
// another job in between, which replaces the record the repeated id is answered from, and a repeated id would start again.
const osResendWindow = 5 * time.Minute

const maxOsResends = 3

// ---------------------------------------------------------------------------------------------------
// What may be started, and where

// osGlobal is what blocks every node alike: another rollout of any kind, or the panel itself being updated.
type osGlobal struct{ rollout, agentRollout, panelBusy bool }

// osGlobalLocked reads it; the caller holds s.mu.
func (s *Service) osGlobalLocked(ctx context.Context) (osGlobal, error) {
	g := osGlobal{panelBusy: s.bundleSyncing || s.panelBusy()}
	if _, err := s.st.ActiveRollout(ctx); err == nil {
		g.agentRollout = true
	} else if !errors.Is(err, store.ErrNotFound) {
		return g, err
	}
	if _, err := s.st.ActiveOsRollout(ctx); err == nil {
		g.rollout = true
	} else if !errors.Is(err, store.ErrNotFound) {
		return g, err
	}
	return g, nil
}

// osGlobalNow reads it for a caller that does not hold s.mu.
func (s *Service) osGlobalNow(ctx context.Context) (osGlobal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.osGlobalLocked(ctx)
}

// osNode is what the decision about one node needs.
type osNode struct {
	connected, updateCap, supported, onPanel, awgBuilding, busy bool
	status                                                      *agentv1.OsStatus
	// rebootAt is when a reboot becomes possible (osRebootWait), zero when it is possible now; rebootCode is the limit that holds it.
	rebootAt   time.Time
	rebootCode string
}

// osBlock is why an OS job cannot be started on the node now, "" when it can. reboot asks for a plain reboot, which needs no
// reading and has no "nothing to do" (the caller decides whether a reboot is wanted). The order is the order the owner
// should fix things in.
func osBlock(g osGlobal, n osNode, reboot bool) string {
	switch {
	case !n.connected:
		return blockOffline
	case !n.updateCap:
		return blockAgentOld
	case !n.supported:
		return blockUnsupported
	case n.onPanel:
		return blockPanelHost
	case g.rollout:
		return blockRolloutOn
	case g.agentRollout:
		return blockAgentRollout
	case g.panelBusy:
		return blockPanelBusy
	case n.awgBuilding:
		return blockAwgBuild
	case n.busy:
		return blockNodeBusy
	case reboot && !n.rebootAt.IsZero():
		return blockRebootWait
	case reboot:
		return ""
	case n.status == nil:
		return blockNoStatus
	case n.status.GetSecurity() == 0 && n.status.GetApt() != "half_configured":
		return blockNothingToDo
	}
	return ""
}

func osBlockForMode(g osGlobal, n osNode, mode string) string {
	reason := osBlock(g, n, mode == store.OsModeRebootOnly)
	if reason == blockNothingToDo && mode == store.OsModeAll && n.status != nil && n.status.GetPending() > 0 {
		return ""
	}
	return reason
}

// osNodeOf collects the facts of one node for osBlock. nodeHost is the node's host name, "" when unknown.
func (s *Service) osNodeOf(n store.NodeRow, connected bool, status *agentv1.OsStatus, host *agentv1.OsStatus, nodeHost string, nowUnix int64, reboots []time.Time) osNode {
	capable := slices.Contains(n.AgentCaps, osupdate.Capability)
	at, code := osRebootWait(n.BootAt, reboots, time.Unix(nowUnix, 0))
	return osNode{rebootAt: at, rebootCode: code,
		connected: connected, updateCap: slices.Contains(n.AgentCaps, osupdate.CapabilityUpdate),
		supported: osUpdateSupported(capable, status), onPanel: s.onPanelServer(status, host, nodeHost),
		awgBuilding: n.AwgPrepare().State == store.AwgPrepareRunning,
		busy: osUpdateState(capable, connected, status, nowUnix) == adminv1.OsUpdateState_OS_UPDATE_STATE_BUSY ||
			s.osRefreshBusy(n.ID, status, time.Unix(nowUnix, 0)),
		status: status,
	}
}

// osRefreshBusy: a refresh of the package lists was asked of the node a moment ago and may still be running (the node shows
// the job in its snapshot, or has not shown it yet); an install started now would be refused as busy.
func (s *Service) osRefreshBusy(nodeID string, status *agentv1.OsStatus, now time.Time) bool {
	s.osMu.Lock()
	rf, ok := s.osRefreshes[nodeID]
	s.osMu.Unlock()
	if !ok || rf.jobID == "" {
		return false
	}
	age := now.Sub(rf.at)
	if age < 0 || age >= osRefreshJobAge {
		return false
	}
	j := status.GetJob()
	return age < osRefreshQuiet || j.GetJobId() == rf.jobID && j.GetState() == osupdate.StateRunning
}

// onPanelServer: this node's agent runs on the panel's own server. The boot id says "the same kernel", which a container host
// shares with its containers; so a node that reports itself as a container counts only when its host name is the panel's.
func (s *Service) onPanelServer(status, host *agentv1.OsStatus, nodeHost string) bool {
	// the panel's own reading when it has one, else the boot id it hashed itself at start (no root needed for that)
	own := s.bootHash
	if host != nil && host.GetBootIdHash() != "" {
		own = host.GetBootIdHash()
	}
	if status == nil || status.GetBootIdHash() == "" || status.GetBootIdHash() != own {
		return false
	}
	return status.GetContainer() == "" || nodeHost != "" && nodeHost == s.hostname
}

// ---------------------------------------------------------------------------------------------------
// Start and cancel

func osModeStrings(mode, reboot string) (action, jobMode string, err error) {
	switch {
	case (mode == store.OsModeSecurity || mode == store.OsModeAll) && (reboot == store.OsRebootNever || reboot == store.OsRebootIfRequired):
		jobMode := osupdate.ModeSecurity
		if mode == store.OsModeAll {
			jobMode = osupdate.ModeAll
		}
		return osupdate.ActionUpgrade, jobMode, nil
	case mode == store.OsModeRebootOnly && (reboot == store.OsRebootIfRequired || reboot == store.OsRebootAlways):
		return osupdate.ActionReboot, "", nil
	}
	return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("this combination of mode and reboot is not available"))
}

// startOs preserves the single-node call shape used by older callers.
func (s *Service) startOs(ctx context.Context, nodeIDs []string, mode, reboot string) (store.OsRolloutRow, error) {
	return s.startOsWithBatch(ctx, nodeIDs, mode, reboot, 1)
}

type osStartNode struct {
	row    store.NodeRow
	status *agentv1.OsStatus
}

// startOsWithBatch stores all selected steps in canary order. Stage zero contains only the canary; following stages use
// batchSize nodes. Node-level blockers are evaluated as their stage is reached so one blocked node never rejects the fleet.
func (s *Service) startOsWithBatch(ctx context.Context, nodeIDs []string, mode, reboot string, batchSize int) (store.OsRolloutRow, error) {
	if _, _, err := osModeStrings(mode, reboot); err != nil {
		return store.OsRolloutRow{}, err
	}
	if len(nodeIDs) < 1 || len(nodeIDs) > 20 {
		return store.OsRolloutRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("node_ids must contain 1 to 20 nodes"))
	}
	if batchSize == 0 {
		batchSize = 1
	}
	if batchSize < 1 || batchSize > 5 {
		return store.OsRolloutRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("batch_size must be 1 to 5"))
	}
	seen := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		if id == "" || seen[id] {
			return store.OsRolloutRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("node_ids must contain unique non-empty ids"))
		}
		seen[id] = true
	}
	actor := s.cfg.Actor(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.osGlobalLocked(ctx)
	if err != nil {
		return store.OsRolloutRow{}, s.internal("check running rollouts", err)
	}
	switch {
	case g.rollout:
		return store.OsRolloutRow{}, precondition("os_blocked: reason=" + blockRolloutOn)
	case g.agentRollout:
		return store.OsRolloutRow{}, precondition("os_blocked: reason=" + blockAgentRollout)
	case g.panelBusy:
		return store.OsRolloutRow{}, precondition("os_blocked: reason=" + blockPanelBusy)
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		return store.OsRolloutRow{}, s.internal("read live nodes", err)
	}
	liveByID := make(map[string]store.NodeLiveRow, len(liveRows))
	for _, row := range liveRows {
		liveByID[row.NodeID] = row
	}
	byID := make(map[string]osStartNode, len(nodeIDs))
	blocked := make(map[string]string, len(nodeIDs))
	ordered := make([]nodeView, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n, err := s.st.Node(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && n.State != "active") {
			return store.OsRolloutRow{}, notFound("node")
		}
		if err != nil {
			return store.OsRolloutRow{}, s.internal("load node", err)
		}
		live, err := s.fl.NodeLive(ctx, n.ID)
		if err != nil {
			return store.OsRolloutRow{}, s.internal("read node liveness", err)
		}
		var online int
		if liveRow := liveByID[n.ID]; live.Connected && liveRow.UsersJSON != "" {
			var users map[string]int64
			if json.Unmarshal([]byte(liveRow.UsersJSON), &users) == nil {
				online = len(users)
			}
		}
		inbounds, err := s.st.FleetInbounds(ctx, n.ID, true)
		if err != nil {
			return store.OsRolloutRow{}, s.internal("read node inbounds", err)
		}
		st := s.osStatusOf(ctx, n.ID)
		facts, err := s.st.NodeFacts(ctx, n.ID)
		if err != nil {
			return store.OsRolloutRow{}, s.internal("read node facts", err)
		}
		node := s.osNodeOf(n, live.Connected, st, s.PanelHostOsStatus(), facts.Hostname, s.now().Unix(), s.osRebootsOf(ctx, n.ID, s.now()))
		node.busy = node.busy || s.inflight[n.ID] // a rollback of the agent is on its way
		reason := osBlockForMode(osGlobal{}, node, mode)
		if reason == "" && mode == store.OsModeRebootOnly && reboot == store.OsRebootIfRequired && st != nil && !st.GetRebootRequired() {
			reason = blockNothingToDo
		}
		blocked[id] = reason
		byID[id] = osStartNode{row: n, status: st}
		ordered = append(ordered, nodeView{row: n, online: online, inbounds: len(inbounds)})
	}
	canaryOrder(ordered)
	// One blocked node never rejects the fleet (it waits or is skipped when its stage comes), but a start that could not
	// begin anywhere is refused, with the canary's reason.
	if !slices.ContainsFunc(ordered, func(v nodeView) bool { return blocked[v.row.ID] == "" }) {
		return store.OsRolloutRow{}, precondition("os_blocked: reason=" + blocked[ordered[0].row.ID])
	}
	now := s.now()
	ro := store.OsRolloutRow{ID: store.NewID("osr_"), Status: store.RolloutRunning, Mode: mode, Reboot: reboot, BatchSize: batchSize, CreatedBy: actor, CreatedAt: now}
	steps := make([]store.OsStepRow, 0, len(ordered))
	for i, view := range ordered {
		candidate := byID[view.row.ID]
		stage := 0
		if i > 0 {
			stage = 1 + (i-1)/batchSize
		}
		step := store.OsStepRow{NodeID: candidate.row.ID, NodeName: candidate.row.Name, Stage: stage, State: store.OsStepPending,
			Params: map[string]string{"mode": mode}}
		if candidate.status != nil {
			step.Params["pending"] = strconv.FormatUint(uint64(candidate.status.GetPending()), 10)
			step.Params["security"] = strconv.FormatUint(uint64(candidate.status.GetSecurity()), 10)
			if reboot != store.OsRebootNever {
				for _, code := range candidate.status.GetRebootBlockers() {
					if slices.Contains(osupdate.RebootBlockers, code) {
						step.State, step.ErrorKey, step.FinishedAt = store.OsStepSkipped, code, now
						step.Params["code"] = code
						break
					}
				}
			}
		}
		steps = append(steps, step)
	}
	if err := s.st.CreateOsRollout(ctx, ro, steps); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.OsRolloutRow{}, precondition("os_blocked: reason=" + blockRolloutOn)
		}
		return store.OsRolloutRow{}, s.internal("create OS rollout", err)
	}
	s.auditAs(ctx, actor, "os_rollout_start", map[string]string{"rollout_id": ro.ID, "nodes": strconv.Itoa(len(steps)), "mode": mode,
		"reboot": reboot, "batch_size": strconv.Itoa(batchSize), "canary": ordered[0].row.Name})
	s.loadOsUpdating(ctx)
	s.kick()
	return ro, nil
}

// osStatusOf is the node's stored snapshot, nil when there is none or it cannot be read.
func (s *Service) osStatusOf(ctx context.Context, nodeID string) *agentv1.OsStatus {
	row, err := s.st.OsStatus(ctx, nodeID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("update: read OS status", "code", "update_os_read_os_status", "node", nodeID, "err", err)
		}
		return nil
	}
	status := &agentv1.OsStatus{}
	if err := protojson.Unmarshal([]byte(row.StatusJSON), status); err != nil {
		s.log.Warn("stored OS status is invalid", "code", "os_status_decode_failed", "node", nodeID)
		return nil
	}
	return status
}

// cancelOs ends the OS rollout: its pending steps are skipped, a paused rollout ends at once (its alert clears), a step in flight
// finishes and is still checked. With none of those it would change nothing, and says so.
func (s *Service) cancelOs(ctx context.Context, id string) (store.OsRolloutRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ro store.OsRolloutRow
	var err error
	if id == "" {
		ro, err = s.st.ActiveOsRollout(ctx)
	} else {
		ro, err = s.st.OsRollout(ctx, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		if id == "" {
			return ro, precondition("rollout is not active")
		}
		return ro, notFound("rollout")
	}
	if err != nil {
		return ro, s.internal("load OS rollout", err)
	}
	if !ro.Active() {
		if ro.Status != store.RolloutFailed {
			return ro, precondition("rollout is not active")
		}
		ok, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutFailed}, store.RolloutCancelled, "", nil, s.now())
		if err != nil {
			return ro, s.internal("close failed OS rollout", err)
		}
		if !ok {
			return ro, precondition("rollout is not active")
		}
		s.audit(ctx, "os_rollout_cancel", map[string]string{"rollout_id": ro.ID})
		return s.st.OsRollout(ctx, ro.ID)
	}
	steps, err := s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		return ro, s.internal("load OS steps", err)
	}
	if ro.Status == store.RolloutRunning && !slices.ContainsFunc(steps, func(x store.OsStepRow) bool { return x.State == store.OsStepPending }) {
		inFlight := slices.ContainsFunc(steps, func(x store.OsStepRow) bool {
			return x.State == store.OsStepSent || x.State == store.OsStepRebooting || x.State == store.OsStepGating
		})
		if inFlight {
			return ro, precondition("os_cancel_inflight") // a node job cannot be stopped, and nothing else is queued
		}
		if _, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutRunning}, store.RolloutCancelled, "", nil, s.now()); err != nil {
			return ro, s.internal("cancel empty OS rollout", err)
		}
		s.audit(ctx, "os_rollout_cancel", map[string]string{"rollout_id": ro.ID})
		return s.st.OsRollout(ctx, ro.ID)
	}
	if err := s.st.SkipPendingOsSteps(ctx, ro.ID, errCancelled, s.now()); err != nil {
		return ro, s.internal("cancel OS rollout", err)
	}
	s.audit(ctx, "os_rollout_cancel", map[string]string{"rollout_id": ro.ID})
	if ro.Status == store.RolloutPaused {
		steps, err = s.st.OsRolloutSteps(ctx, ro.ID)
		if err != nil {
			return ro, s.internal("reload OS steps", err)
		}
		inFlight := slices.ContainsFunc(steps, func(x store.OsStepRow) bool {
			return x.State == store.OsStepSent || x.State == store.OsStepRebooting || x.State == store.OsStepGating
		})
		if inFlight {
			params := cloneStrings(ro.PauseParams)
			params["cancel_requested"] = "true"
			if _, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutPaused}, store.RolloutPaused, pauseOsCancelWait, params, time.Time{}); err != nil {
				return ro, s.internal("cancel OS rollout", err)
			}
		} else if _, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutPaused}, store.RolloutCancelled, "", nil, s.now()); err != nil {
			return ro, s.internal("cancel OS rollout", err)
		}
		if inFlight {
			got, err := s.st.OsRollout(ctx, ro.ID)
			if err != nil {
				return ro, s.internal("reload OS rollout", err)
			}
			s.loadOsUpdating(ctx)
			return got, nil
		}
		got, err := s.st.OsRollout(ctx, ro.ID)
		if err != nil {
			return ro, s.internal("reload OS rollout", err)
		}
		s.loadOsUpdating(ctx)
		return got, nil
	}
	if steps, err := s.st.OsRolloutSteps(ctx, ro.ID); err == nil {
		s.finishOsIfDone(ctx, ro, steps)
	}
	s.loadOsUpdating(ctx)
	got, err := s.st.OsRollout(ctx, ro.ID)
	if err != nil {
		return ro, s.internal("reload OS rollout", err)
	}
	return got, nil
}

func cloneStrings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Service) pauseOs(ctx context.Context, id string) (store.OsRolloutRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ro store.OsRolloutRow
	var err error
	if id == "" {
		ro, err = s.st.ActiveOsRollout(ctx)
	} else {
		ro, err = s.st.OsRollout(ctx, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		if id == "" {
			return ro, precondition("rollout is not active")
		}
		return ro, notFound("rollout")
	}
	if err != nil {
		return ro, s.internal("load OS rollout", err)
	}
	if ro.Status == store.RolloutPaused {
		return ro, nil
	}
	if ro.Status != store.RolloutRunning {
		return ro, precondition("rollout is not running")
	}
	if _, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutRunning}, store.RolloutPaused, pauseOwner, nil, time.Time{}); err != nil {
		return ro, s.internal("pause OS rollout", err)
	}
	s.audit(ctx, "os_rollout_pause", map[string]string{"rollout_id": ro.ID})
	return s.st.OsRollout(ctx, ro.ID)
}

func (s *Service) resumeOs(ctx context.Context, id string, skipFailed bool) (store.OsRolloutRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.st.ActiveOsRollout(ctx)
	if id != "" {
		ro, err = s.st.OsRollout(ctx, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		if id == "" {
			return ro, precondition("rollout is not active")
		}
		return ro, notFound("rollout")
	}
	if err != nil {
		return ro, s.internal("load OS rollout", err)
	}
	if ro.Status != store.RolloutPaused {
		return ro, precondition("rollout is not paused")
	}
	if ro.PauseParams["cancel_requested"] == "true" {
		return ro, precondition("rollout cancellation is waiting for a job on a node to finish")
	}
	steps, err := s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		return ro, s.internal("load OS steps", err)
	}
	if !skipFailed {
		for _, step := range steps {
			if step.State == store.OsStepFailed && step.Params[osRetryCount] != "" && step.Params[osRetryCount] != "0" {
				return ro, precondition("the failed step was already retried; resume with skip_failed or cancel")
			}
		}
	}
	for i := range steps {
		step := &steps[i]
		if step.State != store.OsStepFailed {
			continue
		}
		updated := *step
		if skipFailed {
			updated.State, updated.ErrorKey, updated.FinishedAt = store.OsStepSkipped, osErrSkippedAfterFailure, s.now()
			updated.Params = mergeParams(updated.Params, map[string]string{"failed_reason": step.ErrorKey})
		} else {
			updated.State, updated.JobID, updated.Sends = store.OsStepPending, "", 0
			updated.AckedAt, updated.PhaseAt, updated.BootBefore = time.Time{}, time.Time{}, time.Time{}
			updated.Phase, updated.KernelFrom, updated.KernelTo = "", "", ""
			updated.PreFailed = nil
			updated.SentAt, updated.RebootAt, updated.BackAt, updated.FinishedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
			updated.ErrorKey = ""
			updated.Params = clearOsRetryWait(updated.Params)
			updated.Params[osRetryCount] = "1"
		}
		if err := s.st.SaveOsStep(ctx, &updated); err != nil {
			return ro, s.internal("prepare OS step resume", err)
		}
		*step = updated
	}
	if _, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutPaused}, store.RolloutRunning, "", nil, time.Time{}); err != nil {
		return ro, s.internal("resume OS rollout", err)
	}
	s.audit(ctx, "os_rollout_resume", map[string]string{"rollout_id": ro.ID, "skip_failed": strconv.FormatBool(skipFailed)})
	s.kick()
	return s.st.OsRollout(ctx, ro.ID)
}

// finishOsIfDone ends a rollout with nothing left to do: DONE, FAILED (a step failed) or CANCELLED (the owner cancelled). A
// PAUSED rollout is only ended this way after a cancel: otherwise the pause (and its alert) waits for the owner.
func (s *Service) finishOsIfDone(ctx context.Context, ro store.OsRolloutRow, steps []store.OsStepRow) bool {
	cancelled, bad := ro.PauseParams["cancel_requested"] == "true", false
	for _, x := range steps {
		switch {
		case !x.Decided():
			return false
		case x.State == store.OsStepFailed:
			bad = true
		case x.State == store.OsStepSkipped && x.ErrorKey == errCancelled:
			cancelled = true
		}
	}
	if ro.Status == store.RolloutPaused && !cancelled {
		return false
	}
	status := store.RolloutDone
	switch {
	case cancelled:
		status = store.RolloutCancelled
	case bad:
		status = store.RolloutFailed
	}
	ok, err := s.st.SetOsRolloutStatus(ctx, ro.ID, []string{store.RolloutRunning, store.RolloutPaused}, status, "", nil, s.now())
	if err != nil {
		s.log.Warn("update: finish OS rollout", "code", "update_os_finish_os_rollout", "rollout", ro.ID, "err", err)
		return false
	}
	return ok
}

// ---------------------------------------------------------------------------------------------------
// The worker pass

// osPass is one look at the active OS rollout.
type osPass struct {
	s        *Service
	ctx      context.Context
	ro       store.OsRolloutRow
	steps    []store.OsStepRow
	live     map[string]store.NodeLiveRow
	statuses map[string]*agentv1.OsStatus
	now      time.Time
}

func (s *Service) tickOs(ctx context.Context) {
	if s.cfg.Edge {
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.st.ActiveOsRollout(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("update: load OS rollout", "code", "update_os_load_os_rollout", "err", err)
		} else {
			s.setOsUpdating(map[string]bool{})
		}
		return
	}
	steps, err := s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		s.log.Warn("update: load OS steps", "code", "update_os_load_os_steps", "err", err)
		return
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		s.log.Warn("update: load live nodes", "code", "update_os_load_live_nodes", "err", err)
		return
	}
	live := make(map[string]store.NodeLiveRow, len(liveRows))
	for _, row := range liveRows {
		live[row.NodeID] = row
	}
	p := &osPass{s: s, ctx: ctx, ro: ro, steps: steps, live: live, statuses: map[string]*agentv1.OsStatus{}, now: now}
	for i := range p.steps {
		switch p.steps[i].State {
		case store.OsStepSent:
			p.advanceSent(i)
		case store.OsStepRebooting:
			p.advanceRebooting(i)
		case store.OsStepGating:
			p.advanceGate(i)
		}
	}
	if p.ro.Status == store.RolloutRunning {
		p.launch()
	}
	s.finishOsIfDone(ctx, p.ro, p.steps)
	s.loadOsUpdating(ctx)
}

// OsStatusChanged is called when a node's snapshot or an OS event arrived: a step that follows its job looks now.
func (s *Service) OsStatusChanged(string) { s.kick() }

func (p *osPass) status(nodeID string) *agentv1.OsStatus {
	if st, ok := p.statuses[nodeID]; ok {
		return st
	}
	st := p.s.osStatusOf(p.ctx, nodeID)
	p.statuses[nodeID] = st
	return st
}

func (p *osPass) node(x *store.OsStepRow) (store.NodeRow, store.NodeLiveRow, bool) {
	n, err := p.s.st.Node(p.ctx, x.NodeID)
	if err != nil {
		p.s.log.Warn("update: load node", "code", "update_os_load_node", "node", x.NodeID, "err", err)
		return n, store.NodeLiveRow{}, false
	}
	return n, p.live[x.NodeID], true
}

func (p *osPass) save(x *store.OsStepRow) bool {
	if err := p.s.st.SaveOsStep(p.ctx, x); err != nil {
		if errors.Is(err, store.ErrConflict) {
			p.s.log.Debug("update: OS step changed concurrently", "node", x.NodeID)
			return false
		}
		p.s.log.Warn("update: save OS step", "code", "update_os_save_os_step", "node", x.NodeID, "err", err)
		return false
	}
	return true
}

// finish records the outcome of a step. extra is merged into its params (the node's code, package, counts).
func (p *osPass) finish(x *store.OsStepRow, state, key string, extra map[string]string) bool {
	updated := *x
	updated.State, updated.ErrorKey, updated.FinishedAt, updated.Phase, updated.PhaseAt = state, key, p.now, "", time.Time{}
	updated.Params = clearOsRetryWait(withoutGateWait(mergeParams(x.Params, extra)))
	if !p.save(&updated) {
		return false
	}
	*x = updated
	delete(p.s.failing, x.NodeID)
	p.event(x)
	return true
}

func mergeParams(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// finishAndPause ends a step as failed and pauses the rollout: the owner decides whether to retry, skip, or cancel.
func (p *osPass) finishAndPause(x *store.OsStepRow, key string, extra map[string]string, pauseCode string) {
	if p.finish(x, store.OsStepFailed, key, extra) {
		p.pause(pauseCode, x, key)
	}
}

// event puts the outcome of a step on the node's event list. A skipped step is not an event: nothing happened to the node.
func (p *osPass) event(x *store.OsStepRow) {
	code, sev := "os_step_passed", 1
	switch x.State {
	case store.OsStepFailed:
		code, sev = "os_step_failed", 3
	case store.OsStepPassed:
	default:
		return
	}
	params := map[string]string{"job_id": x.JobID}
	for _, k := range []string{"code", "package", "upgraded", "reboot", "kernel"} {
		if v := x.Params[k]; v != "" {
			params[k] = v
		}
	}
	if x.ErrorKey != "" {
		params["reason"] = x.ErrorKey
	}
	if err := p.s.st.InsertEvent(p.ctx, store.EventRow{Time: p.now, Severity: sev, Code: code, Source: "panel", NodeID: x.NodeID, Params: params}); err != nil {
		p.s.log.Warn("update: OS step event", "code", "update_os_step_event", "node", x.NodeID, "err", err)
	}
}

// pause stops the rollout because of a step. The first reason stays.
func (p *osPass) pause(code string, x *store.OsStepRow, reason string) {
	if p.ro.Status != store.RolloutRunning && !(p.ro.Status == store.RolloutPaused && p.ro.PauseKey == pauseOwner) {
		return
	}
	params := map[string]string{"node": x.NodeName, "node_id": x.NodeID, "reason": reason, "code": x.Params["code"], "mode": p.ro.Mode}
	previous := p.ro.Status
	ok, err := p.s.st.SetOsRolloutStatus(p.ctx, p.ro.ID, []string{previous}, store.RolloutPaused, code, params, time.Time{})
	if err != nil {
		p.s.log.Warn("update: pause OS rollout", "code", "update_os_pause_os_rollout", "rollout", p.ro.ID, "err", err)
		return
	}
	if ok {
		p.ro.Status, p.ro.PauseKey, p.ro.PauseParams = store.RolloutPaused, code, params
		p.s.log.Warn("update: OS rollout paused", "code", "update_os_rollout_paused", "rollout", p.ro.ID, "reason", code, "node", x.NodeName, "detail", reason)
	}
}

// ---------------------------------------------------------------------------------------------------
// Launch

// launch sends the pending steps in the earliest unfinished batch. A later stage waits for every earlier step to pass or skip.
func (p *osPass) launch() {
	s := p.s
	stage := -1
	for _, x := range p.steps {
		if x.State == store.OsStepPending && (stage < 0 || x.Stage < stage) {
			stage = x.Stage
		}
	}
	if stage < 0 {
		return
	}
	for _, x := range p.steps {
		if x.Stage < stage && !x.Decided() {
			return
		}
	}
	for i := range p.steps {
		x := &p.steps[i]
		if x.State != store.OsStepPending || x.Stage != stage {
			continue
		}
		n, live, ok := p.node(x)
		if !ok {
			continue
		}
		facts, err := s.st.NodeFacts(p.ctx, x.NodeID)
		if err != nil {
			s.log.Warn("update: read node facts", "code", "update_os_read_node_facts", "node", x.NodeID, "err", err)
			continue
		}
		status := p.status(x.NodeID)
		reboot := p.ro.Mode == store.OsModeRebootOnly
		// The rollout itself is the one running: only what is true of the node counts here.
		node := s.osNodeOf(n, live.Connected, status, s.PanelHostOsStatus(), facts.Hostname, p.now.Unix(), s.osRebootsOf(p.ctx, x.NodeID, p.now))
		node.busy = node.busy || s.inflight[x.NodeID]
		if retryAt, expired := p.retryWait(x); expired {
			code := x.Params[osRetryCode]
			if code == "" {
				code = osupdate.CodeBusy
			}
			p.finish(x, store.OsStepSkipped, osErrTryLater, map[string]string{"code": osCode(code)})
			continue
		} else if retryAt.After(p.now) {
			continue
		}
		if p.ro.Reboot != store.OsRebootNever && status != nil && len(status.GetRebootBlockers()) > 0 {
			code := status.GetRebootBlockers()[0]
			if slices.Contains(osupdate.RebootBlockers, code) {
				p.finish(x, store.OsStepSkipped, code, map[string]string{"code": code})
				continue
			}
		}
		if status != nil && status.GetApt() == "locked" {
			p.waitTryLater(x, osupdate.CodeAptLock, p.now.Add(osRetryInterval))
			continue
		}
		switch osBlockForMode(osGlobal{}, node, p.ro.Mode) {
		case blockOffline:
			p.finish(x, store.OsStepSkipped, errOffline, nil)
		case blockAgentOld, blockUnsupported:
			p.finish(x, store.OsStepSkipped, errUnsupported, nil)
		case blockPanelHost:
			p.finish(x, store.OsStepSkipped, osErrPanelHost, nil)
		case blockNodeBusy:
			p.waitTryLater(x, osupdate.CodeBusy, p.now.Add(osRetryInterval))
		case blockAwgBuild:
			p.finish(x, store.OsStepSkipped, osErrBusy, nil)
		case blockRebootWait:
			p.waitTryLater(x, node.rebootCode, node.rebootAt)
		case blockNothingToDo:
			if reboot {
				p.finish(x, store.OsStepSkipped, osErrNotRequired, nil)
			} else {
				p.finish(x, store.OsStepSkipped, errUpToDate, nil)
			}
		default:
			if reboot && p.ro.Reboot == store.OsRebootIfRequired && status != nil && !status.GetRebootRequired() {
				p.finish(x, store.OsStepSkipped, osErrNotRequired, nil)
				continue
			}
			p.send(x, n, status)
		}
	}
}

// send marks the step SENT under a new job id (with the inbounds that are FAILED right now) and hands the command to its goroutine.
func (p *osPass) send(x *store.OsStepRow, n store.NodeRow, status *agentv1.OsStatus) {
	s := p.s
	updated := *x
	updated.State, updated.JobID, updated.Sends, updated.SentAt = store.OsStepSent, store.NewID("osj_"), 1, p.now
	updated.Phase, updated.PhaseAt = "", time.Time{}
	updated.BootBefore, updated.PreFailed = n.BootAt, p.preFailed(x)
	updated.KernelFrom = status.GetKernelRunning()
	updated.Params = mergeParams(x.Params, map[string]string{"boot_hash": status.GetBootIdHash()})
	delete(updated.Params, osRetryAfter) // osRetrySince stays: the two-hour wait is one budget across resends
	delete(updated.Params, osRetryCode)
	if !p.save(&updated) {
		return
	}
	*x = updated
	s.setOsUpdatingAdd(x.NodeID) // before the node reboots: its dropped stream must not look like an outage
	p.sendCommand(x)
}

// sendCommand sends UpdateOs for the step's job id in its own goroutine and records the answer.
func (p *osPass) sendCommand(x *store.OsStepRow) {
	s := p.s
	action, mode, _ := osModeStrings(p.ro.Mode, p.ro.Reboot)
	req := OsJobRequest{Action: action, Mode: mode, Reboot: p.ro.Reboot, JobID: x.JobID}
	s.osInflight[x.NodeID] = true
	rolloutID, nodeID, runCtx := p.ro.ID, x.NodeID, s.runCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		res, err := s.fl.UpdateOs(runCtx, nodeID, req, s.cfg.OsAnswerWait)
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.osInflight, nodeID)
		s.onOsAnswer(rolloutID, nodeID, req.JobID, res, err)
	}()
}

func (p *osPass) retryNotBefore(nodeID, code string) time.Time {
	if code == osupdate.CodeTooSoon || code == osupdate.CodeRebootLimit {
		if n, err := p.s.st.Node(p.ctx, nodeID); err == nil {
			at, _ := osRebootWait(n.BootAt, p.s.osRebootsOf(p.ctx, nodeID, p.now), p.now)
			if at.After(p.now) {
				return at
			}
		}
	}
	return p.now.Add(osRetryInterval)
}

func (p *osPass) preFailed(x *store.OsStepRow) []string {
	in, err := p.s.st.FleetInbounds(p.ctx, x.NodeID, true)
	if err != nil {
		return x.PreFailed
	}
	var failed []string
	for _, inbound := range in {
		if inbound.State == "failed" {
			failed = append(failed, inbound.ID)
		}
	}
	return failed
}

// stepPassOs loads the active OS rollout and the step of the node if it is still the one the goroutine started with.
func (s *Service) stepPassOs(ctx context.Context, rolloutID, nodeID string) (*osPass, *store.OsStepRow) {
	ro, err := s.st.ActiveOsRollout(ctx)
	if err != nil || ro.ID != rolloutID {
		return nil, nil
	}
	steps, err := s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		return nil, nil
	}
	p := &osPass{s: s, ctx: ctx, ro: ro, steps: steps, statuses: map[string]*agentv1.OsStatus{}, now: s.now()}
	for i := range p.steps {
		if p.steps[i].NodeID == nodeID {
			return p, &p.steps[i]
		}
	}
	return nil, nil
}

// onOsAnswer records what UpdateOs returned (the lock is held). Where the job goes from here is decided by the pass.
func (s *Service) onOsAnswer(rolloutID, nodeID, jobID string, res *agentv1.CommandResult, err error) {
	ctx := s.runCtx
	p, x := s.stepPassOs(ctx, rolloutID, nodeID)
	if p == nil || x.State != store.OsStepSent || x.JobID != jobID {
		return
	}
	p.applyAnswer(x, res, err)
	s.finishOsIfDone(ctx, p.ro, p.steps)
	s.loadOsUpdating(ctx)
	s.kick()
}

// applyAnswer is the outcome of the command. The node answers ok whatever it decided; params.state says what happened.
//
// A self-closing refusal returns the step to its durable retry wait. Other first-send refusals are conclusive failures and pause
// the rollout. After a resend the first send may have started the job, so the node's own record decides.
// "finished" only says the job is over: how it ended comes from the events and the snapshot, which carry what the node decided about
// the reboot (held, done).
func (p *osPass) applyAnswer(x *store.OsStepRow, res *agentv1.CommandResult, err error) {
	refused := func(code string) {
		if x.Sends > 1 {
			return
		}
		if c := osCode(code); slices.Contains(osRetryLater, c) {
			p.waitTryLater(x, c, p.retryNotBefore(x.NodeID, c))
			return
		}
		p.finishAndPause(x, osErrAgentRefused, map[string]string{"code": osCode(code)}, pauseStepFailed)
	}
	ack := func() {
		if x.AckedAt.IsZero() {
			x.AckedAt = p.now
			p.save(x)
		}
	}
	switch {
	case err != nil:
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || x.Sends > 1 { // no answer, or the link dropped: the pass looks at the node itself
			return
		}
		key := errUnsupported // nothing was sent: it went offline or lacks os-update/1
		var ce *connect.Error
		if errors.As(err, &ce) && strings.Contains(ce.Message(), "offline") {
			key = errOffline
		}
		p.finish(x, store.OsStepSkipped, key, nil)
	case !res.Ok:
		refused(agentCode(res.Error))
	default:
		switch res.Params["state"] {
		case osupdate.AnswerStarted, osupdate.AnswerRunning:
			if res.Params["job_id"] != x.JobID {
				refused(osupdate.CodeBusy) // another job is going on the node
				return
			}
			ack()
		case osupdate.AnswerFinished:
			if res.Params["job_id"] == "" || res.Params["job_id"] == x.JobID {
				ack()
			}
		default: // unsupported: the code says why (unsupported_os, bad_request, busy ...)
			refused(res.Params["code"])
		}
	}
}

// osCode is a node code of the vocabulary, "unknown" for anything else: a node's word is never stored as it came.
func osCode(c string) string {
	if slices.Contains(osupdate.Codes, c) {
		return c
	}
	return "unknown"
}

// ---------------------------------------------------------------------------------------------------
// Following the job

// osJobView is what the panel knows about one job from the node: the snapshot's job and the events raised for it.
type osJobView struct {
	state, phase, code, reason, pkg, reboot, heldReason, kernelTo, kernel, upgraded string
	// reasons are all the codes that blocked a reboot, comma separated (the event of a failed job carries them, the snapshot does not).
	reasons   string
	rebooting bool
	// snapshot: state, upgraded and reboot come from the node's snapshot of this job, which counts the packages it installed.
	snapshot bool
}

// job reads the job of the step. The snapshot says how far it is; the events are the reliable record of how it ended (the
// snapshot of the end is sent once and may be lost). An agent can raise an event again after a restart: the first end counts,
// and the job id keeps one job's events from another's.
func (p *osPass) job(x *store.OsStepRow, status *agentv1.OsStatus) osJobView {
	var v osJobView
	if j := status.GetJob(); j != nil && j.GetJobId() == x.JobID {
		v = osJobView{state: j.GetState(), phase: j.GetPhase(), code: j.GetCode(), reason: j.GetReason(), reboot: j.GetReboot(), heldReason: j.GetHeldReason(),
			kernelTo: j.GetKernelTo(), upgraded: strconv.FormatUint(uint64(j.GetUpgraded()), 10), rebooting: j.GetPhase() == osupdate.PhaseRebooting, snapshot: true}
	}
	events, err := p.s.st.OsJobEvents(p.ctx, x.NodeID, x.JobID, x.SentAt.Add(-10*time.Minute))
	if err != nil {
		p.s.log.Warn("update: read OS job events", "code", "update_os_read_os_job_events", "node", x.NodeID, "err", err)
		return v
	}
	ended := v.state == osupdate.StateDone || v.state == osupdate.StateFailed
	for _, e := range events {
		switch e.Code {
		case "os_reboot_started":
			v.rebooting = true
			if v.kernelTo == "" {
				v.kernelTo = e.Params["kernel_to"]
			}
			if v.state == "" {
				v.state = osupdate.StateRunning
			}
		case "os_update_done":
			if !ended {
				v.state, ended = osupdate.StateDone, true
				v.upgraded, v.reboot, v.kernel, v.heldReason = e.Params["upgraded"], e.Params["reboot"], e.Params["kernel"], e.Params["held_reason"]
			}
		case "os_update_failed":
			if v.reasons == "" {
				v.reasons = e.Params["reasons"]
			}
			if !ended {
				v.state, ended = osupdate.StateFailed, true
				v.code, v.reason, v.pkg, v.reboot, v.kernel = e.Params["code"], e.Params["reason"], e.Params["package"], e.Params["reboot"], e.Params["kernel"]
			}
		}
	}
	return v
}

// jobParams is what the step keeps of the job for the screen and the history; every value is checked on the way in.
func jobParams(v osJobView) map[string]string {
	out := map[string]string{}
	if v.code != "" {
		out["code"] = osCode(v.code)
	}
	if n, err := strconv.ParseUint(v.upgraded, 10, 32); err == nil && n > 0 {
		out["upgraded"] = v.upgraded
	}
	if slices.Contains([]string{osupdate.RebootDone, osupdate.RebootNotRequired, osupdate.RebootHeld, osupdate.RebootNotAsked}, v.reboot) {
		out["reboot"] = v.reboot
	}
	if v.heldReason != "" {
		out["held_reason"] = osCode(v.heldReason)
	}
	if v.pkg != "" && osPackageName.MatchString(v.pkg) {
		out["package"] = v.pkg
	}
	if v.kernel != "" && osKernelName.MatchString(v.kernel) {
		out["kernel"] = v.kernel
	}
	return out
}

// applyJob moves a step along with what the job says. It is idempotent: the same view twice changes nothing the second time.
func (p *osPass) applyJob(x *store.OsStepRow, v osJobView) {
	switch v.state {
	case osupdate.StateRunning:
		changed := false
		if x.AckedAt.IsZero() {
			x.AckedAt, changed = p.now, true
		}
		if v.phase != "" && v.phase != x.Phase {
			x.Phase, x.PhaseAt, changed = v.phase, p.now, true
		}
		if v.kernelTo != "" && x.KernelTo == "" && osKernelName.MatchString(v.kernelTo) {
			x.KernelTo, changed = v.kernelTo, true
		}
		if v.rebooting && x.State == store.OsStepSent {
			x.State, x.RebootAt, changed = store.OsStepRebooting, p.now, true
		}
		if changed {
			p.save(x)
		}
	case osupdate.StateDone:
		extra := jobParams(v)
		if code, ok := p.osNothingChanged(v); ok && v.reboot == osupdate.RebootHeld {
			p.waitTryLater(x, code, p.retryNotBefore(x.NodeID, code))
			return
		}
		if v.reboot == osupdate.RebootHeld { // a node that finished with a reboot held
			p.finishAndPause(x, osErrRebootHeld, extra, pauseOsRebootHeld)
			return
		}
		x.Params = mergeParams(x.Params, extra)
		if v.kernelTo != "" && x.KernelTo == "" && osKernelName.MatchString(v.kernelTo) {
			x.KernelTo = v.kernelTo
		}
		x.State, x.Phase, x.BackAt = store.OsStepGating, "", p.now
		if v.reboot == osupdate.RebootDone && x.RebootAt.IsZero() {
			x.RebootAt = p.now
		}
		p.save(x)
	case osupdate.StateFailed:
		extra := jobParams(v)
		if code, ok := p.osNothingChanged(v); ok {
			p.waitTryLater(x, code, p.retryNotBefore(x.NodeID, code))
			return
		}
		switch {
		case v.reboot == osupdate.RebootHeld:
			p.finishAndPause(x, osErrRebootHeld, extra, pauseOsRebootHeld)
		case v.reboot == osupdate.RebootDone: // the host rebooted and then the node found something wrong
			p.finishAndPause(x, osErrJobFailed, extra, pauseGateFailed)
		default:
			p.finishAndPause(x, osErrJobFailed, extra, pauseStepFailed)
		}
	}
}

func (p *osPass) advanceSent(i int) {
	x, s := &p.steps[i], p.s
	n, live, ok := p.node(x)
	if !ok {
		return
	}
	if v := p.job(x, p.status(x.NodeID)); v.state != "" {
		p.applyJob(x, v)
		if x.State != store.OsStepSent {
			return
		}
	}
	if p.rebooted(x, n, live) {
		// the host restarted under the job and the panel did not see the reboot start: check it as a reboot
		x.State, x.RebootAt, x.BackAt, x.Phase = store.OsStepGating, p.now, p.now, ""
		p.save(x)
		return
	}
	if !live.Connected && !n.LastDisconnectedAt.IsZero() && !n.LastDisconnectedAt.Before(x.SentAt) && p.now.Sub(n.LastDisconnectedAt) > s.cfg.OsBackWait {
		// the node dropped off while the job ran and has not come back: after an acknowledged job that is a node that did not return
		if x.AckedAt.IsZero() {
			p.finishAndPause(x, errNoAnswer, nil, pauseStepFailed)
		} else {
			p.finishAndPause(x, osErrNotBack, nil, pauseOsNotBack)
		}
		return
	}
	if s.osInflight[x.NodeID] {
		return // the command goroutine is still waiting for the answer (its own timeout ends that)
	}
	cfg := s.cfg
	if x.AckedAt.IsZero() && x.Sends < maxOsResends && live.Connected && p.now.Before(x.SentAt.Add(osResendWindow)) &&
		!p.now.Before(x.SentAt.Add(time.Duration(x.Sends)*cfg.OsResendAfter)) {
		// the answer was lost and the node showed nothing: the same job id is sent again, and is answered with the job or starts it
		x.Sends++
		if p.save(x) {
			p.sendCommand(x)
		}
		return
	}
	if p.now.After(x.SentAt.Add(cfg.OsJobWait)) {
		p.finishAndPause(x, errNoAnswer, nil, pauseStepFailed)
	}
}

// rebooted: the node is connected on another boot than the one the job was sent on. The boot id its snapshot carries is the first
// word (it is the kernel's own); the boot time of its Hello, which the agent derives from the uptime and which jitters, the second.
func (p *osPass) rebooted(x *store.OsStepRow, n store.NodeRow, live store.NodeLiveRow) bool {
	if !live.Connected {
		return false
	}
	if before, now := x.Params["boot_hash"], p.status(x.NodeID).GetBootIdHash(); before != "" && now != "" && before != now {
		return true
	}
	return !x.BootBefore.IsZero() && n.BootAt.After(x.BootBefore.Add(osBootJitter))
}

func (p *osPass) advanceRebooting(i int) {
	x, s := &p.steps[i], p.s
	n, live, ok := p.node(x)
	if !ok {
		return
	}
	if v := p.job(x, p.status(x.NodeID)); v.state == osupdate.StateDone || v.state == osupdate.StateFailed {
		p.applyJob(x, v)
		if x.State != store.OsStepRebooting {
			return
		}
	}
	if p.rebooted(x, n, live) {
		x.State, x.BackAt, x.Phase = store.OsStepGating, p.now, ""
		p.save(x)
		return
	}
	if p.now.After(x.RebootAt.Add(s.cfg.OsBackWait)) {
		p.finishAndPause(x, osErrNotBack, nil, pauseOsNotBack)
	}
}

func (p *osPass) advanceGate(i int) {
	x, s := &p.steps[i], p.s
	n, live, ok := p.node(x)
	if !ok {
		return
	}
	v := p.job(x, p.status(x.NodeID))
	if v.state == osupdate.StateFailed {
		p.applyJob(x, v)
		return
	}
	deadline := !p.now.Before(x.BackAt.Add(s.cfg.OsGateWait))
	if !live.Connected {
		if deadline {
			if x.RebootAt.IsZero() { // no reboot was involved: "did not come back after the reboot" would say what did not happen
				p.finishAndPause(x, osErrNodeLost, nil, pauseGateFailed)
			} else {
				p.finishAndPause(x, osErrNotBack, nil, pauseOsNotBack)
			}
		}
		return
	}
	expect := x.KernelTo
	if expect == "" && osKernelName.MatchString(v.kernelTo) {
		expect = v.kernelTo
	}
	if !x.RebootAt.IsZero() && expect != "" {
		facts, err := s.st.NodeFacts(p.ctx, x.NodeID)
		if err == nil && facts.Kernel != "" && facts.Kernel != expect {
			p.finishAndPause(x, osErrKernel, map[string]string{"kernel": facts.Kernel}, pauseGateFailed)
			return
		}
	}
	// The checks look at what the node does since it was back; a job without a reboot has no "back", so since the job began.
	since := x.BackAt
	if x.RebootAt.IsZero() {
		since = x.SentAt
	}
	g := s.healthGate(p.ctx, x.NodeID, n, live.Drift, since, x.PreFailed, p.now, v.state == osupdate.StateDone)
	if g.hard == "" && g.soft == "" && v.state != osupdate.StateDone {
		// How the job ended is not known yet (it is still looking at itself after the reboot, or its end has not reached the
		// panel): a pass now could be followed by "failed" for a node that was called done.
		g.pass, g.soft = false, osErrJobNotSettled
	}
	var extra map[string]string
	if v.state == osupdate.StateDone {
		extra = jobParams(v)
	}
	switch {
	case g.pass:
		p.finish(x, store.OsStepPassed, "", mergeParams(extra, g.with))
	case g.hard != "":
		p.finishAndPause(x, g.hard, g.with, pauseGateFailed)
	case deadline && !g.wait:
		p.finishAndPause(x, g.soft, g.with, pauseGateFailed)
	default:
		p.noteGateWait(x, g) // what the screen says while the step sits here
	}
}

// ---------------------------------------------------------------------------------------------------
// The updating set, restart and retention

func (s *Service) setOsUpdating(ids map[string]bool) {
	s.updMu.Lock()
	s.osUpdating = ids
	s.updMu.Unlock()
}

func (s *Service) setOsUpdatingAdd(nodeID string) {
	s.updMu.Lock()
	if s.osUpdating == nil {
		s.osUpdating = map[string]bool{}
	}
	s.osUpdating[nodeID] = true
	s.updMu.Unlock()
}

// loadOsUpdating rebuilds the set of nodes with an OS step in flight from the database.
func (s *Service) loadOsUpdating(ctx context.Context) {
	if s.cfg.Edge {
		return
	}
	ro, err := s.st.ActiveOsRollout(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.setOsUpdating(map[string]bool{})
		}
		return
	}
	steps, err := s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		return
	}
	out := map[string]bool{}
	for _, x := range steps {
		if x.State == store.OsStepSent || x.State == store.OsStepRebooting || x.State == store.OsStepGating {
			out[x.NodeID] = true
		}
	}
	s.setOsUpdating(out)
}

// recoverOs prepares an OS rollout found in the database after a restart. A step in SENT needs nothing: nobody waits for its
// answer, and the pass follows the job by its id. A step in GATING gets its window again.
func (s *Service) recoverOs(ctx context.Context) {
	if s.cfg.Edge {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ro, err := s.st.ActiveOsRollout(ctx); err == nil {
		if steps, err := s.st.OsRolloutSteps(ctx, ro.ID); err == nil {
			for i := range steps {
				if steps[i].State == store.OsStepGating {
					steps[i].BackAt = s.now()
					if err := s.st.SaveOsStep(ctx, &steps[i]); err != nil {
						s.log.Warn("update: restart OS gate window", "code", "update_os_restart_os_gate_window", "node", steps[i].NodeID, "err", err)
					}
				}
			}
		}
	}
	s.loadOsUpdating(ctx)
}

// osRolloutRunning reports an OS rollout that is running: a job is on a node, or about to be. Callers that start another kind
// of rollout wait for it. A paused one (a step failed, the owner has not closed it) holds only the next OS update: nothing is
// going on any node, and the agent updates, the panel update and the bundle sync have no reason to wait for a click.
func (s *Service) osRolloutRunning(ctx context.Context) (bool, error) {
	if s.cfg.Edge {
		return false, nil
	}
	ro, err := s.st.ActiveOsRollout(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && ro.Status == store.RolloutRunning, err
}

func (s *Service) pruneOsOld(ctx context.Context) {
	if s.cfg.Edge {
		return
	}
	if _, err := s.st.PruneOsRollouts(ctx, keepRollouts, s.now().Add(-rolloutAge)); err != nil && ctx.Err() == nil {
		s.log.Warn("update: prune OS rollouts", "code", "update_os_prune_os_rollouts", "err", err)
	}
}

// osConditions is the health condition of a paused OS rollout (Conditions): critical when the node did not come back.
func (s *Service) osConditions(ctx context.Context) []health.ExtCond {
	if s.cfg.Edge {
		return nil
	}
	ro, err := s.st.ActiveOsRollout(ctx)
	if err != nil || ro.Status != store.RolloutPaused {
		return nil
	}
	sev := 2
	switch ro.PauseKey {
	case pauseOsNotBack:
		sev = 3
	case pauseStepFailed, pauseGateFailed, pauseOsRebootHeld:
	default:
		return nil
	}
	params := map[string]string{"rollout_id": ro.ID, "node": ro.PauseParams["node"], "reason": ro.PauseParams["reason"], "code": ro.PauseParams["code"], "mode": ro.PauseParams["mode"]}
	return []health.ExtCond{{Kind: "os_update_failed", NodeID: ro.PauseParams["node_id"], Subject: ro.ID, Severity: sev,
		Why: "health.alert.os_update_failed.why." + ro.PauseKey, Params: params}}
}

var (
	osPackageName = regexp.MustCompile("^[a-z0-9][a-z0-9.+-]{0,63}$")
	osKernelName  = regexp.MustCompile("^[A-Za-z0-9.+_-]{1,128}$")
)
