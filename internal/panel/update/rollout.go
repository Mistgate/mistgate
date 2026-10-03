package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The rollout state machine. Everything a rollout knows lives in the database; the
// worker pass (tick) looks at it every few seconds, advances what can advance and starts what may start. A
// command (UpdateAgent, RollbackAgent) runs in its own goroutine because it waits for the agent's answer, and only
// records what it got; decisions about reconnects and gates are made by the pass from observable state alone, so a
// panel restart in any step loses nothing.

// Pause codes (Rollout.pause_key is "updates.pause.<code>").
const (
	pauseOwner         = "owner"
	pauseStepFailed    = "step_failed"
	pauseGateFailed    = "gate_failed"
	pauseBundleChanged = "bundle_changed"
	pausePanelRestart  = "panel_restart_ambiguous"
)

// Step error codes the panel itself produces (the agent's own codes come through as they are).
const (
	errNoAnswer        = "no_answer"
	errNotReconnected  = "not_reconnected"
	errStateNotApplied = "state_not_applied"
	errInboundFailed   = "inbound_failed"
	errProbeFailed     = "probe_failed"
	errRolledBackAgent = "rolled_back_by_agent"
	errAgentFailed     = "agent_failed"
	errManual          = "manual"
	errRollbackFailed  = "rollback_failed"
	errOffline         = "offline"
	errUnsupported     = "unsupported"
	errUpToDate        = "up_to_date"
	errCancelled       = "cancelled"
)

const maxBatch = 10

func precondition(msg string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(msg))
}

func notFound(what string) error {
	return connect.NewError(connect.CodeNotFound, errors.New("no such "+what))
}

var errInternal = connect.NewError(connect.CodeInternal, errors.New("internal error"))

func (s *Service) internal(what string, err error) error {
	s.log.Error("update: "+what, "err", err)
	return errInternal
}

func jsonString(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func lowerLess(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }

// ---------------------------------------------------------------------------------------------------
// Start, pause, resume, cancel

// bundleMatches reports whether the bundle on disk is trusted and is the release the rollout ships.
func (s *Service) bundleMatches(ro store.RolloutRow) bool {
	b := s.current()
	return b != nil && b.trusted && bytes.Equal(b.raw, ro.Manifest)
}

// defaultBatch is the number of nodes updated at the same time after the canary when the owner gave none.
func defaultBatch(nodes int) int {
	if nodes < 5 {
		return 1
	}
	return 2
}

// canaryOrder puts the node with the fewest online users first, then the fewest inbounds, then by name.
func canaryOrder(v []nodeView) {
	sort.SliceStable(v, func(i, j int) bool {
		a, b := v[i], v[j]
		switch {
		case a.online != b.online:
			return a.online < b.online
		case a.inbounds != b.inbounds:
			return a.inbounds < b.inbounds
		}
		return lowerLess(a.row.Name, b.row.Name)
	})
}

// skipKey is why a node that cannot be updated is skipped.
func skipKey(st adminv1.NodeUpdateState) string {
	switch st {
	case adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE:
		return errUpToDate
	case adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED:
		return errUnsupported
	}
	return errOffline
}

func updatable(st adminv1.NodeUpdateState) bool {
	return st == adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED ||
		st == adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK || st == adminv1.NodeUpdateState_NODE_UPDATE_STATE_FAILED
}

// beginPanelUpdate reserves the same mutex used by rollout creation so the panel cannot begin an agent rollout
// while a panel binary replacement is being downloaded and scheduled.
func (s *Service) beginPanelUpdate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bundleSyncing {
		return precondition("a GitHub node bundle is being installed")
	}
	if s.panelInstalling {
		return precondition("a panel update is already being installed")
	}
	if _, err := s.st.ActiveRollout(ctx); err == nil {
		return precondition("finish or cancel the active node rollout before updating the panel")
	} else if !errors.Is(err, store.ErrNotFound) {
		return s.internal("check active rollout before panel update", err)
	}
	s.panelInstalling = true
	return nil
}

// finishPanelUpdate releases a failed reservation immediately. A successful systemd-run may still fail to start
// its detached helper, so the reservation expires after the helper's systemd timeout plus a margin.
func (s *Service) finishPanelUpdate(scheduled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelInstallGeneration++
	generation := s.panelInstallGeneration
	if s.panelInstallTimer != nil {
		s.panelInstallTimer.Stop()
		s.panelInstallTimer = nil
	}
	if !scheduled {
		s.panelInstalling = false
		return
	}
	s.panelInstallTimer = time.AfterFunc(panelUpdateGuardTimeout, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.panelInstallGeneration == generation {
			s.panelInstalling = false
			s.panelInstallTimer = nil
		}
	})
}

// start creates a rollout of the trusted bundle. nodeIDs empty = every OUTDATED node.
func (s *Service) start(ctx context.Context, nodeIDs []string, batch int) (store.RolloutRow, error) {
	return s.startWithActor(ctx, nodeIDs, batch, s.cfg.Actor(ctx))
}

func (s *Service) startWithActor(ctx context.Context, nodeIDs []string, batch int, actor string) (store.RolloutRow, error) {
	if batch < 0 || batch > maxBatch {
		return store.RolloutRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("batch_size must be 0 to 10"))
	}
	if s.cfg.Key == nil {
		return store.RolloutRow{}, precondition("no release key in this build")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bundleSyncing {
		return store.RolloutRow{}, precondition("the GitHub node bundle is being installed")
	}
	if s.panelInstalling {
		return store.RolloutRow{}, precondition("a panel update is being installed")
	}
	if _, err := s.st.ActiveRollout(ctx); err == nil {
		return store.RolloutRow{}, precondition("a rollout is already active")
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.RolloutRow{}, s.internal("active rollout", err)
	}
	b := s.rescan() // what is on disk now is what ships
	if !b.trusted {
		return store.RolloutRow{}, precondition("no trusted bundle")
	}
	views, err := s.nodes(ctx, b, nil)
	if err != nil {
		return store.RolloutRow{}, s.internal("list nodes", err)
	}
	var cands []nodeView
	var skipped []nodeView
	if len(nodeIDs) == 0 {
		for _, v := range views {
			if v.state == adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED {
				cands = append(cands, v)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, id := range nodeIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			i := slices.IndexFunc(views, func(v nodeView) bool { return v.row.ID == id })
			switch {
			case i < 0:
				return store.RolloutRow{}, notFound("node")
			case updatable(views[i].state):
				cands = append(cands, views[i])
			default:
				skipped = append(skipped, views[i])
			}
		}
	}
	if len(cands) == 0 {
		return store.RolloutRow{}, precondition("no node to update")
	}
	canaryOrder(cands)
	if batch == 0 {
		batch = defaultBatch(len(cands))
	}
	now := s.now()
	ro := store.RolloutRow{ID: store.NewID("rol_"), Status: store.RolloutRunning, ToVersion: b.manifest.Version, ToBuilt: b.manifest.Built,
		Manifest: b.raw, Signature: b.sig, BatchSize: batch, CreatedBy: actor, CreatedAt: now}
	var steps []store.StepRow
	lastStage := 0
	for i, v := range cands {
		stage := 0
		if i > 0 {
			stage = 1 + (i-1)/batch
		}
		lastStage = stage
		steps = append(steps, store.StepRow{NodeID: v.row.ID, NodeName: v.row.Name, Stage: stage, State: store.StepPending,
			FromVersion: v.row.AgentVersion, FromBuilt: v.row.AgentBuilt})
	}
	for _, v := range skipped { // decided from the start: a bucket after the last batch, so the canary stays alone in stage 0
		steps = append(steps, store.StepRow{NodeID: v.row.ID, NodeName: v.row.Name, Stage: lastStage + 1, State: store.StepSkipped,
			FromVersion: v.row.AgentVersion, FromBuilt: v.row.AgentBuilt, FinishedAt: now, ErrorKey: skipKey(v.state)})
	}
	if err := s.st.CreateRollout(ctx, ro, steps); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.RolloutRow{}, precondition("a rollout is already active")
		}
		return store.RolloutRow{}, s.internal("create rollout", err)
	}
	s.auditAs(ctx, actor, "update_rollout_start", map[string]string{"rollout_id": ro.ID, "version": ro.ToVersion, "nodes": fmt.Sprint(len(cands)),
		"batch_size": fmt.Sprint(batch)})
	s.loadUpdating(ctx)
	s.kick()
	return ro, nil
}

// activeByID loads the rollout the caller named (empty id = the active one) and requires it to be active.
func (s *Service) activeByID(ctx context.Context, id string) (store.RolloutRow, error) {
	var ro store.RolloutRow
	var err error
	if id == "" {
		ro, err = s.st.ActiveRollout(ctx)
	} else {
		ro, err = s.st.Rollout(ctx, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		if id == "" {
			return ro, precondition("rollout is not active")
		}
		return ro, notFound("rollout")
	}
	if err != nil {
		return ro, s.internal("load rollout", err)
	}
	if !ro.Active() {
		return ro, precondition("rollout is not active")
	}
	return ro, nil
}

func (s *Service) pauseByOwner(ctx context.Context, id string) (store.RolloutRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.activeByID(ctx, id)
	if err != nil {
		return ro, err
	}
	if ro.Status == store.RolloutPaused {
		return ro, nil // idempotent
	}
	if _, err := s.st.SetRolloutStatus(ctx, ro.ID, []string{store.RolloutRunning}, store.RolloutPaused, pauseOwner, nil, time.Time{}); err != nil {
		return ro, s.internal("pause rollout", err)
	}
	s.audit(ctx, "update_rollout_pause", map[string]string{"rollout_id": ro.ID})
	return s.reload(ctx, ro.ID)
}

func (s *Service) resume(ctx context.Context, id string) (store.RolloutRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.activeByID(ctx, id)
	if err != nil {
		return ro, err
	}
	if ro.Status != store.RolloutPaused {
		return ro, precondition("rollout is not paused")
	}
	steps, err := s.st.RolloutSteps(ctx, ro.ID)
	if err != nil {
		return ro, s.internal("load steps", err)
	}
	if slices.ContainsFunc(steps, func(x store.StepRow) bool { return x.State == store.StepPending }) && !s.bundleMatches(ro) {
		return ro, precondition("the bundle differs from this rollout: cancel it and start again")
	}
	if _, err := s.st.SetRolloutStatus(ctx, ro.ID, []string{store.RolloutPaused}, store.RolloutRunning, "", nil, time.Time{}); err != nil {
		return ro, s.internal("resume rollout", err)
	}
	s.audit(ctx, "update_rollout_resume", map[string]string{"rollout_id": ro.ID})
	s.kick()
	return s.reload(ctx, ro.ID)
}

func (s *Service) cancel(ctx context.Context, id string) (store.RolloutRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.activeByID(ctx, id)
	if err != nil {
		return ro, err
	}
	if err := s.st.SkipPendingSteps(ctx, ro.ID, errCancelled, s.now()); err != nil {
		return ro, s.internal("cancel rollout", err)
	}
	s.audit(ctx, "update_rollout_cancel", map[string]string{"rollout_id": ro.ID})
	if ro.Status == store.RolloutPaused {
		// finishIfDone ends a PAUSED rollout only when this cancel skipped a step. With nothing pending (a fleet of one,
		// or the last step failed) it would stay paused for ever, with its alert: a cancelled rollout waits for nobody.
		if _, err := s.st.SetRolloutStatus(ctx, ro.ID, []string{store.RolloutPaused}, store.RolloutRunning, "", nil, time.Time{}); err != nil {
			return ro, s.internal("cancel rollout", err)
		}
		ro.Status = store.RolloutRunning
	}
	if steps, err := s.st.RolloutSteps(ctx, ro.ID); err == nil {
		s.finishIfDone(ctx, ro, steps) // nothing in flight: ends now; else when the steps in flight are decided
	}
	s.loadUpdating(ctx)
	return s.reload(ctx, ro.ID)
}

func (s *Service) reload(ctx context.Context, id string) (store.RolloutRow, error) {
	ro, err := s.st.Rollout(ctx, id)
	if err != nil {
		return ro, s.internal("reload rollout", err)
	}
	return ro, nil
}

// finishIfDone ends a rollout that has nothing left to do: DONE, FAILED (a step failed or was rolled back) or
// CANCELLED (the owner cancelled). A PAUSED rollout is only ended this way after a cancel: otherwise the pause (and
// its alert) waits for the owner.
func (s *Service) finishIfDone(ctx context.Context, ro store.RolloutRow, steps []store.StepRow) bool {
	cancelled, bad := false, false
	for _, x := range steps {
		switch {
		case !x.Decided():
			return false
		case x.State == store.StepFailed || x.State == store.StepRolledBack:
			bad = true
		case x.State == store.StepSkipped && x.ErrorKey == errCancelled:
			cancelled = true
		}
	}
	if ro.Status == store.RolloutPaused && !cancelled {
		return false
	}
	status := store.RolloutDone
	switch {
	case bad:
		status = store.RolloutFailed
	case cancelled:
		status = store.RolloutCancelled
	}
	ok, err := s.st.SetRolloutStatus(ctx, ro.ID, []string{store.RolloutRunning, store.RolloutPaused}, status, "", nil, s.now())
	if err != nil {
		s.log.Warn("update: finish rollout", "rollout", ro.ID, "err", err)
		return false
	}
	return ok
}

// ---------------------------------------------------------------------------------------------------
// The worker pass

// pass is one look at the active rollout.
type pass struct {
	s     *Service
	ctx   context.Context
	ro    store.RolloutRow
	steps []store.StepRow
	now   time.Time
}

func (s *Service) tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.st.ActiveRollout(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("update: load rollout", "err", err)
		} else {
			s.setUpdating(map[string]bool{})
		}
		return
	}
	steps, err := s.st.RolloutSteps(ctx, ro.ID)
	if err != nil {
		s.log.Warn("update: load steps", "err", err)
		return
	}
	p := &pass{s: s, ctx: ctx, ro: ro, steps: steps, now: s.now()}
	for i := range p.steps {
		switch p.steps[i].State {
		case store.StepSent:
			p.advanceSent(i)
		case store.StepGating:
			p.advanceGate(i)
		}
	}
	if p.ro.Status == store.RolloutRunning {
		p.launch()
	}
	s.finishIfDone(ctx, p.ro, p.steps)
	s.loadUpdating(ctx)
}

// save writes a step, logging a failure (the next pass sees the old row and tries again).
func (p *pass) save(x *store.StepRow) {
	if err := p.s.st.SaveStep(p.ctx, *x); err != nil {
		p.s.log.Warn("update: save step", "node", x.NodeID, "err", err)
	}
}

// finish records the outcome of a step.
func (p *pass) finish(x *store.StepRow, state, key string, params map[string]string) {
	x.State, x.ErrorKey, x.Params, x.FinishedAt = state, key, params, p.now
	p.save(x)
	delete(p.s.failing, x.NodeID)
	p.event(x, state, key)
	if state == store.StepPassed {
		// An agent that raises update_committed with to_built records this itself (fleet); this covers an older one, so the
		// node shows its last update at once, not after its next Hello.
		lu := store.LastUpdateRow{Outcome: "ok", FromVersion: x.FromVersion, FromBuilt: x.FromBuilt, ToVersion: p.ro.ToVersion, ToBuilt: p.ro.ToBuilt, AtUnix: p.now.Unix()}
		if err := p.s.st.SetLastUpdateIfNewer(p.ctx, x.NodeID, lu); err != nil {
			p.s.log.Warn("update: record last update", "node", x.NodeID, "err", err)
		}
	}
}

// event puts the outcome of a step on the node's event list (the node page says "updated to ..." or why not). A skipped
// step is not an event: nothing happened to the node.
func (p *pass) event(x *store.StepRow, state, key string) {
	code, sev := "update_step_passed", 1
	switch state {
	case store.StepFailed:
		code, sev = "update_step_failed", 3
	case store.StepRolledBack:
		code, sev = "update_step_rolled_back", 2
	case store.StepPassed:
	default:
		return
	}
	params := map[string]string{"from_version": x.FromVersion, "to_version": p.ro.ToVersion}
	if key != "" {
		params["reason"] = key
	}
	if err := p.s.st.InsertEvent(p.ctx, store.EventRow{Time: p.now, Severity: sev, Code: code, Source: "panel", NodeID: x.NodeID, Params: params}); err != nil {
		p.s.log.Warn("update: step event", "node", x.NodeID, "err", err)
	}
}

// pause stops the rollout because of a step; a pause by the owner gives way to a failure, a failure stays as the first said it.
func (p *pass) pause(code string, x *store.StepRow, reason string) {
	if p.ro.Status != store.RolloutRunning && p.ro.PauseKey != pauseOwner {
		return
	}
	params := map[string]string{"node": x.NodeName, "node_id": x.NodeID, "reason": reason}
	ok, err := p.s.st.SetRolloutStatus(p.ctx, p.ro.ID, []string{store.RolloutRunning, store.RolloutPaused}, store.RolloutPaused, code, params, time.Time{})
	if err != nil {
		p.s.log.Warn("update: pause rollout", "rollout", p.ro.ID, "err", err)
		return
	}
	if ok {
		p.ro.Status, p.ro.PauseKey, p.ro.PauseParams = store.RolloutPaused, code, params
		p.s.log.Warn("update: rollout paused", "rollout", p.ro.ID, "reason", code, "node", x.NodeName, "detail", reason)
	}
}

func (p *pass) node(x *store.StepRow) (n store.NodeRow, connected bool, ok bool) {
	n, err := p.s.st.Node(p.ctx, x.NodeID)
	if err != nil {
		p.s.log.Warn("update: load node", "node", x.NodeID, "err", err)
		return n, false, false
	}
	connected, _, _ = p.s.fl.Live(x.NodeID)
	return n, connected, true
}

// recentLast returns the node's reported last update when it is about this rollout's build and newer than the step.
func (p *pass) recentLast(n store.NodeRow, x *store.StepRow) (store.LastUpdateRow, bool) {
	lu, ok := n.LastUpdate()
	if !ok || lu.ToBuilt != p.ro.ToBuilt || lu.AtUnix < x.SentAt.Unix() {
		return store.LastUpdateRow{}, false
	}
	return lu, true
}

// advanceSent watches a step whose command went out: the node comes back with the new build (gate), or rolled
// itself back, or the deadline passes.
func (p *pass) advanceSent(i int) {
	x := &p.steps[i]
	n, connected, ok := p.node(x)
	if !ok {
		return
	}
	if connected && n.AgentBuilt >= p.ro.ToBuilt {
		x.State, x.ReconnectedAt = store.StepGating, p.now
		p.save(x)
		return
	}
	if connected {
		if lu, ok := p.recentLast(n, x); ok && lu.Outcome == "rolled_back" {
			p.finish(x, store.StepRolledBack, errRolledBackAgent, map[string]string{"reason": lu.Reason})
			p.pause(pauseGateFailed, x, errRolledBackAgent)
			return
		} else if ok && lu.Outcome == "failed" {
			p.finish(x, store.StepFailed, errAgentFailed, map[string]string{"reason": lu.Reason})
			p.pause(pauseStepFailed, x, errAgentFailed)
			return
		}
	}
	if p.s.inflight[x.NodeID] {
		return // the command goroutine is still waiting for the answer (its own timeout ends that)
	}
	cfg := p.s.cfg
	switch {
	case !x.AckedAt.IsZero():
		if p.now.After(x.AckedAt.Add(cfg.ReconnectWait)) {
			p.finish(x, store.StepFailed, errNotReconnected, nil)
			p.pause(pauseGateFailed, x, errNotReconnected)
		}
	case p.now.After(x.SentAt.Add(cfg.AnswerWait + cfg.ReconnectWait)):
		p.finish(x, store.StepFailed, errNoAnswer, nil)
		if p.s.orphans[x.NodeID] { // nobody has been waiting for this answer since the panel restarted
			p.pause(pausePanelRestart, x, errNoAnswer)
		} else {
			p.pause(pauseGateFailed, x, errNoAnswer)
		}
	}
}

// gateResult is the outcome of one evaluation of the gate.
type gateResult struct {
	pass bool
	hard string // a failure that ends the gate now
	soft string // what is still missing; it fails the gate only at the deadline
	with map[string]string
}

// gate evaluates the rollout gate for a node that is back with the new build.
func (p *pass) gate(x *store.StepRow, n store.NodeRow, drift bool) gateResult {
	s := p.s
	var soft string
	with := map[string]string{}
	miss := func(key string) {
		if soft == "" {
			soft = key
		}
	}
	committed := false
	if lu, ok := p.recentLast(n, x); ok && lu.Outcome == "ok" {
		committed = true
	}
	if !committed {
		// Only an event newer than this step's command, for this build: the one of an earlier update of the same node (rolled
		// back and started again) must not pass the gate. No slack here: a commit comes minutes after the command.
		if ok, err := s.st.NodeEventSince(p.ctx, x.NodeID, "update_committed", x.SentAt, p.ro.ToBuilt); err == nil && ok {
			committed = true
		}
	}
	if !committed {
		miss(errStateNotApplied)
	}
	if drift || (n.DesiredHash != "" && n.AppliedHash != n.DesiredHash) {
		miss(errStateNotApplied)
	}
	inbounds, err := s.st.FleetInbounds(p.ctx, x.NodeID, true)
	if err != nil {
		miss(errStateNotApplied)
	}
	var newlyFailed []string
	for _, in := range inbounds {
		if in.State == "failed" && !slices.Contains(x.PreFailed, in.ID) {
			newlyFailed = append(newlyFailed, in.ID)
		}
	}
	if len(newlyFailed) > 0 {
		with["inbound"] = newlyFailed[0]
		first, seen := s.failing[x.NodeID]
		if !seen {
			s.failing[x.NodeID] = p.now
		} else if p.now.Sub(first) >= s.cfg.InboundGrace {
			return gateResult{hard: errInboundFailed, with: with}
		}
		miss(errInboundFailed)
	} else {
		delete(s.failing, x.NodeID)
	}
	if s.hl != nil {
		probeable, ok, _ := s.hl.NodeChecksSince(p.ctx, x.NodeID, x.ReconnectedAt)
		if probeable > 0 && ok < probeable {
			miss(errProbeFailed)
			if _, _, _, err := s.hl.RunChecksNow(p.ctx, x.NodeID); err != nil {
				s.log.Warn("update: run checks", "node", x.NodeID, "err", err)
			}
		}
	}
	return gateResult{pass: soft == "", soft: soft, with: with}
}

// advanceGate runs the gate of a node that reconnected with the new build.
func (p *pass) advanceGate(i int) {
	x := &p.steps[i]
	s := p.s
	if s.inflight[x.NodeID] {
		return // a RollbackAgent is on its way
	}
	n, connected, ok := p.node(x)
	if !ok {
		return
	}
	deadline := !p.now.Before(x.ReconnectedAt.Add(s.cfg.GateWait))
	if connected && n.AgentBuilt < p.ro.ToBuilt { // the old build is running again: it rolled itself back (or the guard did)
		if lu, ok := p.recentLast(n, x); ok && lu.Outcome == "rolled_back" {
			p.finish(x, store.StepRolledBack, errRolledBackAgent, map[string]string{"reason": lu.Reason})
			p.pause(pauseGateFailed, x, errRolledBackAgent)
			return
		}
		if deadline {
			p.finish(x, store.StepFailed, errNotReconnected, nil)
			p.pause(pauseGateFailed, x, errNotReconnected)
		}
		return
	}
	if !connected { // gone again after coming back with the new build: nothing to roll back over the stream
		if deadline {
			p.finish(x, store.StepFailed, errNotReconnected, nil)
			p.pause(pauseGateFailed, x, errNotReconnected)
		}
		return
	}
	_, _, drift := s.fl.Live(x.NodeID)
	g := p.gate(x, n, drift)
	switch {
	case g.pass:
		p.finish(x, store.StepPassed, "", nil)
	case g.hard != "":
		p.failGate(x, g.hard, g.with)
	case deadline:
		p.failGate(x, g.soft, g.with)
	}
}

// failGate undoes a node whose gate failed: RollbackAgent (in its own goroutine), then the step ends ROLLED_BACK and
// the rollout pauses.
func (p *pass) failGate(x *store.StepRow, key string, with map[string]string) {
	s := p.s
	s.inflight[x.NodeID] = true
	rolloutID, nodeID, runCtx := p.ro.ID, x.NodeID, s.runCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		res, err := s.fl.RollbackAgent(runCtx, nodeID, s.cfg.RollbackWait)
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.inflight, nodeID)
		s.onRollbackDone(rolloutID, nodeID, key, with, res, err)
	}()
}

// onRollbackDone ends the step of a gate failure once the agent answered (the lock is held).
func (s *Service) onRollbackDone(rolloutID, nodeID, gateKey string, with map[string]string, res *agentv1.CommandResult, err error) {
	ctx := s.runCtx
	p, x := s.stepPass(ctx, rolloutID, nodeID)
	if p == nil {
		return
	}
	defer s.settled(ctx, p)
	params := map[string]string{}
	for k, v := range with {
		params[k] = v
	}
	switch {
	case err == nil && res.Ok:
		p.finish(x, store.StepRolledBack, gateKey, params)
		p.pause(pauseGateFailed, x, gateKey)
	default: // the node could not be rolled back (no_previous, gone): it needs a look
		if err != nil {
			params["reason"] = errText(err)
		} else {
			params["reason"] = agentCode(res.Error)
		}
		params["gate"] = gateKey
		p.finish(x, store.StepFailed, errRollbackFailed, params)
		p.pause(pauseGateFailed, x, gateKey)
	}
}

// settled runs after a command goroutine recorded its result (the lock is held): the rollout may be over, the
// updating set changed, and the worker has a next stage to look at.
func (s *Service) settled(ctx context.Context, p *pass) {
	s.finishIfDone(ctx, p.ro, p.steps)
	s.loadUpdating(ctx)
	s.kick()
}

// stepPass loads the active rollout and one of its steps if it is still the one the goroutine started with.
func (s *Service) stepPass(ctx context.Context, rolloutID, nodeID string) (*pass, *store.StepRow) {
	ro, err := s.st.ActiveRollout(ctx)
	if err != nil || ro.ID != rolloutID {
		return nil, nil
	}
	steps, err := s.st.RolloutSteps(ctx, ro.ID)
	if err != nil {
		return nil, nil
	}
	p := &pass{s: s, ctx: ctx, ro: ro, steps: steps, now: s.now()}
	for i := range p.steps {
		if p.steps[i].NodeID == nodeID {
			return p, &p.steps[i]
		}
	}
	return nil, nil
}

// errText is the short message of an error from the fleet (a Connect error carries one).
func errText(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return store.Clip(ce.Message(), 120)
	}
	return store.Clip(err.Error(), 120)
}

var codeRe = regexp.MustCompile(`^[a-z][a-z_]{0,31}`)

// agentCode is the error code at the start of a CommandResult.error ("no_previous", "failed: exec" -> "failed").
func agentCode(e string) string {
	if c := codeRe.FindString(e); c != "" {
		return c
	}
	return "failed"
}

// launch starts the next stage when every step of the earlier ones is decided.
func (p *pass) launch() {
	s := p.s
	stage := -1
	for _, x := range p.steps {
		if x.State == store.StepPending && (stage < 0 || x.Stage < stage) {
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
	if !s.bundleMatches(p.ro) { // the owner replaced the bundle under a running rollout
		for i := range p.steps {
			if p.steps[i].State == store.StepPending {
				p.pause(pauseBundleChanged, &p.steps[i], "bundle_changed")
				return
			}
		}
	}
	for i := range p.steps {
		x := &p.steps[i]
		if x.State != store.StepPending || x.Stage != stage {
			continue
		}
		n, connected, ok := p.node(x)
		if !ok {
			continue
		}
		_, caps, _ := s.fl.Live(x.NodeID)
		switch {
		case !connected:
			p.finish(x, store.StepSkipped, errOffline, nil)
			continue
		case !slices.Contains(caps, capUpdate):
			p.finish(x, store.StepSkipped, errUnsupported, nil)
			continue
		case n.AgentBuilt >= p.ro.ToBuilt:
			p.finish(x, store.StepSkipped, errUpToDate, nil)
			continue
		}
		p.send(x, n)
	}
}

// send marks the step SENT (with the inbounds that are FAILED right now) and hands the command to its goroutine.
func (p *pass) send(x *store.StepRow, n store.NodeRow) {
	s := p.s
	if in, err := s.st.FleetInbounds(p.ctx, x.NodeID, true); err == nil {
		x.PreFailed = nil
		for _, i := range in {
			if i.State == "failed" {
				x.PreFailed = append(x.PreFailed, i.ID)
			}
		}
	}
	x.State, x.SentAt, x.FromVersion, x.FromBuilt = store.StepSent, p.now, n.AgentVersion, n.AgentBuilt
	p.save(x)
	delete(s.orphans, x.NodeID)
	s.inflight[x.NodeID] = true
	s.setUpdatingAdd(x.NodeID) // before the agent re-executes: its dropped stream must not look like an outage
	rolloutID, nodeID, manifest, sig, runCtx := p.ro.ID, x.NodeID, p.ro.Manifest, p.ro.Signature, s.runCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		res, err := s.fl.UpdateAgent(runCtx, nodeID, manifest, sig, s.cfg.AnswerWait)
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.inflight, nodeID)
		s.onUpdateAnswer(rolloutID, nodeID, res, err)
	}()
}

func (s *Service) setUpdatingAdd(nodeID string) {
	s.updMu.Lock()
	s.updating[nodeID] = true
	s.updMu.Unlock()
}

// onUpdateAnswer records what UpdateAgent returned (the lock is held). The reconnect and the gate are decided by the
// pass, not here.
func (s *Service) onUpdateAnswer(rolloutID, nodeID string, res *agentv1.CommandResult, err error) {
	ctx := s.runCtx
	p, x := s.stepPass(ctx, rolloutID, nodeID)
	if p == nil || x.State != store.StepSent {
		return
	}
	defer s.settled(ctx, p)
	switch {
	case err != nil:
		switch connect.CodeOf(err) {
		case connect.CodeFailedPrecondition: // nothing was sent: it went offline or cannot update
			key := errUnsupported
			var ce *connect.Error
			if errors.As(err, &ce) && strings.Contains(ce.Message(), "offline") {
				key = errOffline
			}
			p.finish(x, store.StepSkipped, key, nil)
		case connect.CodeDeadlineExceeded: // no answer in AnswerWait: the node may still have updated; look before judging
			if n, connected, ok := p.node(x); ok && connected && n.AgentBuilt >= p.ro.ToBuilt {
				return
			}
			p.finish(x, store.StepFailed, errNoAnswer, nil)
			p.pause(pauseGateFailed, x, errNoAnswer)
		default: // the link dropped after the send, or shutdown: the pass decides from what it sees
			return
		}
	case !res.Ok:
		code := agentCode(res.Error)
		params := map[string]string{}
		if code == "failed" && res.Error != "" {
			params["detail"] = store.Clip(res.Error, 200)
		}
		p.finish(x, store.StepFailed, code, params)
		p.pause(pauseStepFailed, x, code)
	case res.Params["noop"] == "1":
		p.finish(x, store.StepSkipped, errUpToDate, nil)
	default:
		x.AckedAt = p.now
		p.save(x)
	}
}

// ---------------------------------------------------------------------------------------------------
// Start-up and manual rollback

// recover prepares a rollout found in the database after a restart: a step in SENT has nobody waiting for its
// answer, a step in GATING gets its window again.
func (s *Service) recover(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ro, err := s.st.ActiveRollout(ctx)
	if err != nil {
		return
	}
	steps, err := s.st.RolloutSteps(ctx, ro.ID)
	if err != nil {
		return
	}
	now := s.now()
	for i := range steps {
		switch steps[i].State {
		case store.StepSent:
			s.orphans[steps[i].NodeID] = true
		case store.StepGating:
			steps[i].ReconnectedAt = now
			if err := s.st.SaveStep(ctx, steps[i]); err != nil {
				s.log.Warn("update: restart gate window", "node", steps[i].NodeID, "err", err)
			}
		}
	}
	s.setUpdating(updatingSet(steps))
}

// rollbackNode asks one node to put its previous binary back and records it in a running rollout.
func (s *Service) rollbackNode(ctx context.Context, nodeID string) (nodeView, error) {
	n, err := s.st.Node(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && n.State != "active") {
		return nodeView{}, notFound("node")
	}
	if err != nil {
		return nodeView{}, s.internal("load node", err)
	}
	s.mu.Lock()
	if s.inflight[nodeID] {
		s.mu.Unlock()
		return nodeView{}, precondition("a command is already running on this node")
	}
	s.inflight[nodeID] = true // the pass leaves the step of this node alone meanwhile
	s.mu.Unlock()

	res, err := s.fl.RollbackAgent(ctx, nodeID, s.cfg.RollbackWait)

	s.mu.Lock()
	delete(s.inflight, nodeID)
	defer s.mu.Unlock()
	if err != nil {
		return nodeView{}, err
	}
	if !res.Ok {
		return nodeView{}, precondition("agent refused: " + agentCode(res.Error))
	}
	s.audit(ctx, "update_rollback_node", map[string]string{"node_id": nodeID, "node": n.Name})
	if ro, err := s.st.ActiveRollout(ctx); err == nil {
		if steps, err := s.st.RolloutSteps(ctx, ro.ID); err == nil {
			p := &pass{s: s, ctx: ctx, ro: ro, steps: steps, now: s.now()}
			for i := range steps {
				if steps[i].NodeID == nodeID && (steps[i].State == store.StepSent || steps[i].State == store.StepGating || steps[i].State == store.StepPassed) {
					p.finish(&steps[i], store.StepRolledBack, errManual, nil)
					p.pause(pauseOwner, &steps[i], errManual)
				}
			}
		}
	}
	s.loadUpdating(ctx)
	views, err := s.nodes(ctx, s.current(), nil)
	if err != nil {
		return nodeView{}, s.internal("list nodes", err)
	}
	for _, v := range views {
		if v.row.ID == nodeID {
			return v, nil
		}
	}
	return nodeView{}, notFound("node")
}
