package update

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// osRefresh is the last refresh of one node's package lists that the panel asked for.
type osRefresh struct {
	at    time.Time
	jobID string
}

// maxOsRefreshes is how many nodes refresh their package lists at the same time, so a click on "check all" does not make the
// whole fleet reach for its mirrors at once. osRefreshJobAge is how long a refresh without a visible end counts as running.
const (
	maxOsRefreshes  = 3
	osRefreshJobAge = 10 * time.Minute
	// osRefreshQuiet: for this long after a refresh was asked of a node it counts as busy even before its snapshot shows the job.
	osRefreshQuiet = 2 * time.Minute
)

// osRefreshedAt: a refresh was asked of the node less than gap ago.
func (s *Service) osRefreshedAt(nodeID string, now time.Time, gap time.Duration) bool {
	s.osMu.Lock()
	defer s.osMu.Unlock()
	rf, ok := s.osRefreshes[nodeID]
	return ok && !rf.at.IsZero() && now.Sub(rf.at) < gap
}

func osRolloutStatusProto(s string) adminv1.OsRolloutStatus {
	switch s {
	case store.RolloutRunning:
		return adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING
	case store.RolloutPaused:
		return adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_PAUSED
	case store.RolloutDone:
		return adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_DONE
	case store.RolloutCancelled:
		return adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_CANCELLED
	case store.RolloutFailed:
		return adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_FAILED
	}
	return adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_UNSPECIFIED
}

func osModeProto(m string) adminv1.OsRolloutMode {
	switch m {
	case store.OsModeSecurity:
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY
	case store.OsModeRebootOnly:
		return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_REBOOT_ONLY
	}
	return adminv1.OsRolloutMode_OS_ROLLOUT_MODE_UNSPECIFIED
}

func osRebootProto(r string) adminv1.OsRebootPolicy {
	switch r {
	case store.OsRebootNever:
		return adminv1.OsRebootPolicy_OS_REBOOT_POLICY_NEVER
	case store.OsRebootIfRequired:
		return adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED
	case store.OsRebootAlways:
		return adminv1.OsRebootPolicy_OS_REBOOT_POLICY_ALWAYS
	}
	return adminv1.OsRebootPolicy_OS_REBOOT_POLICY_UNSPECIFIED
}

func osStepStateProto(s string) adminv1.OsStepState {
	switch s {
	case store.OsStepPending:
		return adminv1.OsStepState_OS_STEP_STATE_PENDING
	case store.OsStepSent:
		return adminv1.OsStepState_OS_STEP_STATE_SENT
	case store.OsStepRebooting:
		return adminv1.OsStepState_OS_STEP_STATE_REBOOTING
	case store.OsStepGating:
		return adminv1.OsStepState_OS_STEP_STATE_GATING
	case store.OsStepPassed:
		return adminv1.OsStepState_OS_STEP_STATE_PASSED
	case store.OsStepFailed:
		return adminv1.OsStepState_OS_STEP_STATE_FAILED
	case store.OsStepSkipped:
		return adminv1.OsStepState_OS_STEP_STATE_SKIPPED
	}
	return adminv1.OsStepState_OS_STEP_STATE_UNSPECIFIED
}

func osRolloutMsg(ro store.OsRolloutRow, steps []store.OsStepRow) *adminv1.OsRollout {
	m := &adminv1.OsRollout{Id: ro.ID, Status: osRolloutStatusProto(ro.Status), Mode: osModeProto(ro.Mode), Reboot: osRebootProto(ro.Reboot),
		CreatedUnix: ro.CreatedAt.Unix(), PauseKey: ro.PauseKey, PauseParams: ro.PauseParams, Steps: []*adminv1.OsStep{}}
	if !ro.FinishedAt.IsZero() {
		m.FinishedUnix = ro.FinishedAt.Unix()
	}
	for _, x := range steps {
		st := &adminv1.OsStep{NodeId: x.NodeID, NodeName: x.NodeName, State: osStepStateProto(x.State), JobId: x.JobID, Phase: x.Phase,
			KernelFrom: x.KernelFrom, KernelTo: x.KernelTo, ErrorKey: x.ErrorKey, Params: x.Params}
		for dst, t := range map[*int64]time.Time{&st.PhaseUnix: x.PhaseAt, &st.SentUnix: x.SentAt, &st.RebootUnix: x.RebootAt, &st.BackUnix: x.BackAt, &st.FinishedUnix: x.FinishedAt} {
			if !t.IsZero() {
				*dst = t.Unix()
			}
		}
		m.Steps = append(m.Steps, st)
	}
	return m
}

// osRolloutOrLast is the active OS rollout, else the last one; nil when there never was one.
func (s *Service) osRolloutOrLast(ctx context.Context) (*adminv1.OsRollout, error) {
	ro, err := s.st.ActiveOsRollout(ctx)
	if errors.Is(err, store.ErrNotFound) {
		ro, err = s.st.LatestOsRollout(ctx)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	steps, err := s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		return nil, err
	}
	return osRolloutMsg(ro, steps), nil
}

func (r rpc) osRolloutResp(ctx context.Context, ro store.OsRolloutRow) (*adminv1.OsRollout, error) {
	steps, err := r.s.st.OsRolloutSteps(ctx, ro.ID)
	if err != nil {
		return nil, r.s.internal("load OS steps", err)
	}
	return osRolloutMsg(ro, steps), nil
}

func (r rpc) StartOsRollout(ctx context.Context, req *connect.Request[adminv1.StartOsRolloutRequest]) (*connect.Response[adminv1.StartOsRolloutResponse], error) {
	ctx, cancel := r.requestContext(ctx)
	defer cancel()
	if err := edition.Check(!r.s.OSUpdatesEnabled(), edition.OSUpdates); err != nil {
		return nil, err
	}
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	var mode, reboot string
	switch req.Msg.Mode {
	case adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY:
		mode = store.OsModeSecurity
	case adminv1.OsRolloutMode_OS_ROLLOUT_MODE_REBOOT_ONLY:
		mode = store.OsModeRebootOnly
	}
	switch req.Msg.Reboot {
	case adminv1.OsRebootPolicy_OS_REBOOT_POLICY_NEVER:
		reboot = store.OsRebootNever
	case adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED:
		reboot = store.OsRebootIfRequired
	case adminv1.OsRebootPolicy_OS_REBOOT_POLICY_ALWAYS:
		reboot = store.OsRebootAlways
	}
	ro, err := r.s.startOs(ctx, req.Msg.NodeIds, mode, reboot)
	if err != nil {
		return nil, err
	}
	m, err := r.osRolloutResp(ctx, ro)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.StartOsRolloutResponse{Rollout: m}), nil
}

func (r rpc) CancelOsRollout(ctx context.Context, req *connect.Request[adminv1.CancelOsRolloutRequest]) (*connect.Response[adminv1.CancelOsRolloutResponse], error) {
	ctx, cancel := r.requestContext(ctx)
	defer cancel()
	if err := edition.Check(!r.s.OSUpdatesEnabled(), edition.OSUpdates); err != nil {
		return nil, err
	}
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	ro, err := r.s.cancelOs(ctx, req.Msg.RolloutId)
	if err != nil {
		return nil, err
	}
	m, err := r.osRolloutResp(ctx, ro)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CancelOsRolloutResponse{Rollout: m}), nil
}

func (r rpc) RefreshOsUpdates(ctx context.Context, req *connect.Request[adminv1.RefreshOsUpdatesRequest]) (*connect.Response[adminv1.RefreshOsUpdatesResponse], error) {
	ctx, cancel := r.requestContext(ctx)
	defer cancel()
	if err := edition.Check(!r.s.OSUpdatesEnabled(), edition.OSUpdates); err != nil {
		return nil, err
	}
	results, err := r.s.refreshOs(ctx, req.Msg.NodeIds)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RefreshOsUpdatesResponse{Results: results}), nil
}

// refreshOs asks nodes to refresh their package lists (apt-get update, nothing installed). It is rate-limited in memory: one
// refresh per node per OsRefreshGap, three nodes at a time. A node that did its own refresh recently needs none.
func (s *Service) refreshOs(ctx context.Context, nodeIDs []string) ([]*adminv1.OsRefreshResult, error) {
	nodes, err := s.st.Nodes(ctx, false)
	if err != nil {
		return nil, s.internal("load nodes", err)
	}
	var targets []store.NodeRow
	for _, n := range nodes {
		if n.State == "active" && (len(nodeIDs) == 0 || slices.Contains(nodeIDs, n.ID)) {
			targets = append(targets, n)
		}
	}
	for _, id := range nodeIDs {
		if !slices.ContainsFunc(targets, func(n store.NodeRow) bool { return n.ID == id }) {
			return nil, notFound("node")
		}
	}
	type claim struct {
		node  store.NodeRow
		jobID string
		index int
	}
	results := make([]*adminv1.OsRefreshResult, len(targets))
	var claims []claim

	s.mu.Lock()
	now := s.now()
	running := 0
	s.osMu.Lock()
	for id, rf := range s.osRefreshes {
		if now.Sub(rf.at) >= osRefreshJobAge {
			delete(s.osRefreshes, id)
			continue
		}
		if j := s.osStatusOf(ctx, id).GetJob(); j == nil || j.GetJobId() != rf.jobID || j.GetState() == osupdate.StateRunning {
			running++
		}
	}
	s.osMu.Unlock()
	stepsOn := map[string]bool{}
	if ro, err := s.st.ActiveOsRollout(ctx); err == nil {
		if steps, err := s.st.OsRolloutSteps(ctx, ro.ID); err == nil {
			for _, x := range steps {
				stepsOn[x.NodeID] = !x.Decided()
			}
		}
	}
	for i, n := range targets {
		res := &adminv1.OsRefreshResult{NodeId: n.ID}
		results[i] = res
		live, err := s.fl.NodeLive(ctx, n.ID)
		if err != nil {
			s.mu.Unlock()
			return nil, s.internal("read node liveness", err)
		}
		status := s.osStatusOf(ctx, n.ID)
		capable := slices.Contains(n.AgentCaps, osupdate.Capability)
		switch {
		case !live.Connected:
			res.Outcome = "offline"
		case !slices.Contains(n.AgentCaps, osupdate.CapabilityUpdate) || !osUpdateSupported(capable, status):
			res.Outcome = "unsupported"
		case stepsOn[n.ID] || status.GetJob().GetState() == osupdate.StateRunning:
			res.Outcome = "running"
		case s.osRefreshedAt(n.ID, now, s.cfg.OsRefreshGap),
			status.GetListsUnix() > 0 && now.Unix()-status.GetListsUnix() < int64(s.cfg.OsRefreshGap/time.Second):
			res.Outcome = "recent"
		case running >= maxOsRefreshes:
			res.Outcome = "limit"
		default:
			c := claim{node: n, jobID: store.NewID("osj_"), index: i}
			s.osMu.Lock()
			s.osRefreshes[n.ID] = osRefresh{at: now, jobID: c.jobID}
			s.osMu.Unlock()
			claims = append(claims, c)
			running++
		}
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range claims {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := results[c.index]
			ans, err := s.fl.UpdateOs(ctx, c.node.ID, OsJobRequest{Action: osupdate.ActionRefresh, Reboot: osupdate.RebootNever, JobID: c.jobID}, s.cfg.OsAnswerWait)
			outcome, code := "started", ""
			switch {
			case err != nil && connect.CodeOf(err) == connect.CodeFailedPrecondition:
				outcome = "offline"
			case err != nil:
				outcome = "refused" // no answer: the job may still start; the claim keeps the next click away for a while
				s.log.Warn("update: refresh of OS package lists got no answer", "code", "update_os_refresh_of_os_package_lists_got_no_answer", "node", c.node.ID, "err", err)
			case !ans.Ok:
				outcome, code = "refused", agentCode(ans.Error)
			case ans.Params["state"] == osupdate.AnswerRunning && ans.Params["job_id"] != c.jobID:
				outcome = "running"
			case ans.Params["state"] == osupdate.AnswerUnsupported:
				outcome, code = "refused", osCode(ans.Params["code"])
			}
			res.Outcome, res.Code = outcome, code
			if outcome != "started" {
				if outcome != "refused" || code != "" {
					s.osMu.Lock()
					delete(s.osRefreshes, c.node.ID) // nothing started: the owner may try again
					s.osMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	started := 0
	for _, c := range claims {
		if results[c.index].Outcome == "started" {
			started++
		}
	}
	if started > 0 {
		s.audit(ctx, "os_refresh", map[string]string{"nodes": strconv.Itoa(started)})
	}
	return results, nil
}
