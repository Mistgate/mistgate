package update

import (
	"context"
	"errors"
	"path/filepath"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// rpc is the admin UpdateService (update.proto). Roles are decided by auth/policy.go before a call gets here;
// every change also needs a fresh step-up (Config.StepUp), checked first, before anything is read or written.
type rpc struct{ s *Service }

var rolloutStatusProto = map[string]adminv1.RolloutStatus{
	store.RolloutRunning: adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING, store.RolloutPaused: adminv1.RolloutStatus_ROLLOUT_STATUS_PAUSED,
	store.RolloutDone: adminv1.RolloutStatus_ROLLOUT_STATUS_DONE, store.RolloutCancelled: adminv1.RolloutStatus_ROLLOUT_STATUS_CANCELLED,
	store.RolloutFailed: adminv1.RolloutStatus_ROLLOUT_STATUS_FAILED,
}

var stepStateProto = map[string]adminv1.StepState{
	store.StepPending: adminv1.StepState_STEP_STATE_PENDING, store.StepSent: adminv1.StepState_STEP_STATE_SENT,
	store.StepGating: adminv1.StepState_STEP_STATE_GATING, store.StepPassed: adminv1.StepState_STEP_STATE_PASSED,
	store.StepFailed: adminv1.StepState_STEP_STATE_FAILED, store.StepRolledBack: adminv1.StepState_STEP_STATE_ROLLED_BACK,
	store.StepSkipped: adminv1.StepState_STEP_STATE_SKIPPED,
}

func unixOrZero(t interface {
	IsZero() bool
	Unix() int64
}) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// rolloutMsg converts a rollout with its steps. Keys are sent whole ("updates.pause.<code>", "updates.step.err.<code>").
func rolloutMsg(ro store.RolloutRow, steps []store.StepRow) *adminv1.Rollout {
	m := &adminv1.Rollout{Id: ro.ID, Status: rolloutStatusProto[ro.Status], ToVersion: ro.ToVersion, ToBuilt: ro.ToBuilt,
		BatchSize: uint32(ro.BatchSize), CreatedUnix: ro.CreatedAt.Unix(), FinishedUnix: unixOrZero(ro.FinishedAt)}
	if ro.Status == store.RolloutPaused && ro.PauseKey != "" {
		m.PauseKey = "updates.pause." + ro.PauseKey
		m.PauseParams = map[string]string{}
		for k, v := range ro.PauseParams {
			if k != "node_id" { // internal: the alert uses it
				m.PauseParams[k] = v
			}
		}
	}
	for _, x := range steps {
		st := &adminv1.RolloutStep{NodeId: x.NodeID, NodeName: x.NodeName, Stage: uint32(x.Stage), State: stepStateProto[x.State],
			FromVersion: x.FromVersion, FromBuilt: x.FromBuilt, StartedUnix: unixOrZero(x.SentAt), FinishedUnix: unixOrZero(x.FinishedAt)}
		if x.ErrorKey != "" {
			st.ErrorKey = "updates.step.err." + x.ErrorKey
		}
		if len(x.Params) > 0 {
			st.Params = x.Params
		}
		m.Steps = append(m.Steps, st)
	}
	return m
}

func (r rpc) rolloutResp(ctx context.Context, ro store.RolloutRow) (*adminv1.Rollout, error) {
	steps, err := r.s.st.RolloutSteps(ctx, ro.ID)
	if err != nil {
		return nil, r.s.internal("load steps", err)
	}
	return rolloutMsg(ro, steps), nil
}

func (r rpc) GetUpdates(ctx context.Context, _ *connect.Request[adminv1.GetUpdatesRequest]) (*connect.Response[adminv1.GetUpdatesResponse], error) {
	s := r.s
	b := s.current()
	resp := &adminv1.GetUpdatesResponse{NowUnix: s.now().Unix(), Bundle: b.view, Panel: &adminv1.PanelBuild{
		Version: s.cfg.PanelVersion, Built: s.cfg.PanelBuilt, HasReleaseKey: s.cfg.Key != nil}}
	offset, err := s.scheduleTimezoneOffset(ctx)
	if err != nil {
		return nil, s.internal("load update timezone", err)
	}
	resp.ScheduleTimezoneOffsetMinutes = offset
	if s.cfg.PanelUpdater != nil {
		resp.Panel.Update = panelUpdateProto(s.cfg.PanelUpdater.Status())
	}
	if s.cfg.Key != nil {
		resp.Panel.ReleaseKeyFingerprint = fingerprint(s.cfg.Key)
	}
	ro, err := s.st.ActiveRollout(ctx)
	if err != nil {
		ro, err = s.st.LatestRollout(ctx)
	}
	var steps []store.StepRow
	switch {
	case err == nil:
		if steps, err = s.st.RolloutSteps(ctx, ro.ID); err != nil {
			return nil, s.internal("load steps", err)
		}
		resp.Rollout = rolloutMsg(ro, steps)
	case err != store.ErrNotFound:
		return nil, s.internal("load rollout", err)
	}
	if !ro.Active() {
		steps = nil // only an active rollout makes a node UPDATING
	}
	views, err := s.nodes(ctx, b, steps)
	if err != nil {
		return nil, s.internal("list nodes", err)
	}
	schedules, err := s.st.NodeUpdateSchedules(ctx)
	if err != nil {
		return nil, s.internal("load node update schedules", err)
	}
	senders, err := s.rollbackSenders(ctx)
	if err != nil {
		return nil, s.internal("rollback senders", err)
	}
	sortNodeViews(views)
	for _, v := range views {
		m := v.proto()
		if schedule, ok := schedules[v.row.ID]; ok {
			m.ScheduledUnix = schedule.ScheduledAt
			m.ScheduledVersion = schedule.ToVersion
			m.ScheduledBuilt = schedule.ToBuilt
			m.ScheduledTimezoneOffsetMinutes = schedule.TimezoneOffsetMinutes
		}
		if lu, ok := currentLastUpdate(v.row); ok && m.LastUpdate != nil {
			m.LastUpdate.Reason = senders.reason(v.row.ID, lu)
		}
		resp.Nodes = append(resp.Nodes, m)
	}
	if s.dist != "" {
		resp.DistDir = s.dist
		if abs, err := filepath.Abs(s.dist); err == nil {
			resp.DistDir = abs
		}
	}
	return connect.NewResponse(resp), nil
}

func panelUpdateProto(s PanelUpdateStatus) *adminv1.PanelUpdate {
	return &adminv1.PanelUpdate{
		Version: s.Version, Url: s.URL, PublishedUnix: s.PublishedUnix, CheckedUnix: s.CheckedUnix,
		Available: s.Available, Supported: s.Supported, Installable: s.Installable, Installing: s.Installing, ErrorKey: s.ErrorKey,
	}
}

func (r rpc) CheckPanelUpdate(ctx context.Context, _ *connect.Request[adminv1.CheckPanelUpdateRequest]) (*connect.Response[adminv1.CheckPanelUpdateResponse], error) {
	if r.s.cfg.PanelUpdater == nil {
		return connect.NewResponse(&adminv1.CheckPanelUpdateResponse{Update: &adminv1.PanelUpdate{ErrorKey: "unsupported"}}), nil
	}
	status := r.s.cfg.PanelUpdater.Check(ctx)
	return connect.NewResponse(&adminv1.CheckPanelUpdateResponse{Update: panelUpdateProto(status)}), nil
}

func (r rpc) InstallPanelUpdate(ctx context.Context, _ *connect.Request[adminv1.InstallPanelUpdateRequest]) (*connect.Response[adminv1.InstallPanelUpdateResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if r.s.cfg.PanelUpdater == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrPanelUnsupported)
	}
	if err := r.s.beginPanelUpdate(ctx); err != nil {
		return nil, err
	}
	if err := r.s.cfg.PanelUpdater.Install(ctx); err != nil {
		r.s.finishPanelUpdate(false)
		switch {
		case errors.Is(err, ErrPanelUnsupported), errors.Is(err, ErrNoPanelRelease), errors.Is(err, ErrNoPanelUpdate), errors.Is(err, ErrPanelAssetMissing):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		default:
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
	}
	r.s.finishPanelUpdate(true)
	status := r.s.cfg.PanelUpdater.Status()
	r.s.audit(ctx, "panel_update", map[string]string{"version": status.Version})
	status.Installing = true // The helper is detached; this process will exit when systemd restarts the panel.
	return connect.NewResponse(&adminv1.InstallPanelUpdateResponse{Update: panelUpdateProto(status)}), nil
}

func (r rpc) StartRollout(ctx context.Context, req *connect.Request[adminv1.StartRolloutRequest]) (*connect.Response[adminv1.StartRolloutResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	ro, err := r.s.start(ctx, req.Msg.NodeIds, int(req.Msg.BatchSize))
	if err != nil {
		return nil, err
	}
	m, err := r.rolloutResp(ctx, ro)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.StartRolloutResponse{Rollout: m}), nil
}

func (r rpc) PauseRollout(ctx context.Context, req *connect.Request[adminv1.PauseRolloutRequest]) (*connect.Response[adminv1.PauseRolloutResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	ro, err := r.s.pauseByOwner(ctx, req.Msg.RolloutId)
	if err != nil {
		return nil, err
	}
	m, err := r.rolloutResp(ctx, ro)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.PauseRolloutResponse{Rollout: m}), nil
}

func (r rpc) ResumeRollout(ctx context.Context, req *connect.Request[adminv1.ResumeRolloutRequest]) (*connect.Response[adminv1.ResumeRolloutResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	ro, err := r.s.resume(ctx, req.Msg.RolloutId)
	if err != nil {
		return nil, err
	}
	m, err := r.rolloutResp(ctx, ro)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ResumeRolloutResponse{Rollout: m}), nil
}

func (r rpc) CancelRollout(ctx context.Context, req *connect.Request[adminv1.CancelRolloutRequest]) (*connect.Response[adminv1.CancelRolloutResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	ro, err := r.s.cancel(ctx, req.Msg.RolloutId)
	if err != nil {
		return nil, err
	}
	m, err := r.rolloutResp(ctx, ro)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CancelRolloutResponse{Rollout: m}), nil
}

func (r rpc) RollbackNode(ctx context.Context, req *connect.Request[adminv1.RollbackNodeRequest]) (*connect.Response[adminv1.RollbackNodeResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	v, err := r.s.rollbackNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RollbackNodeResponse{Node: v.proto()}), nil
}

func (r rpc) RescanBundle(ctx context.Context, _ *connect.Request[adminv1.RescanBundleRequest]) (*connect.Response[adminv1.RescanBundleResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	b := r.s.rescan()
	r.s.audit(ctx, "update_rescan", map[string]string{"status": b.view.Status.String()})
	return connect.NewResponse(&adminv1.RescanBundleResponse{Bundle: b.view}), nil
}

func (r rpc) ScheduleNodeUpdate(ctx context.Context, req *connect.Request[adminv1.ScheduleNodeUpdateRequest]) (*connect.Response[adminv1.ScheduleNodeUpdateResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	row, err := r.s.scheduleNodeUpdate(ctx, req.Msg.NodeId, req.Msg.LocalDatetime, req.Msg.TimezoneOffsetMinutes, req.Msg.ExpectedVersion, req.Msg.ExpectedBuilt)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ScheduleNodeUpdateResponse{
		ScheduledUnix: row.ScheduledAt, Version: row.ToVersion, Built: row.ToBuilt, TimezoneOffsetMinutes: row.TimezoneOffsetMinutes,
	}), nil
}

func (r rpc) CancelNodeUpdateSchedule(ctx context.Context, req *connect.Request[adminv1.CancelNodeUpdateScheduleRequest]) (*connect.Response[adminv1.CancelNodeUpdateScheduleResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	cancelled, err := r.s.st.DeleteNodeUpdateSchedule(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, r.s.internal("cancel node update schedule", err)
	}
	if cancelled {
		r.s.audit(ctx, "node_update_schedule_cancel", map[string]string{"node_id": req.Msg.NodeId})
		r.s.kick()
	}
	return connect.NewResponse(&adminv1.CancelNodeUpdateScheduleResponse{Cancelled: cancelled}), nil
}

func (r rpc) SetUpdateTimezone(ctx context.Context, req *connect.Request[adminv1.SetUpdateTimezoneRequest]) (*connect.Response[adminv1.SetUpdateTimezoneResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if err := r.s.setScheduleTimezone(ctx, req.Msg.TimezoneOffsetMinutes); err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetUpdateTimezoneResponse{TimezoneOffsetMinutes: req.Msg.TimezoneOffsetMinutes}), nil
}

// Conditions is the health condition source (health.Service.AddConditionSource): while the active rollout is paused
// because a step failed, the gate failed or the panel lost track of a step, UPDATE_FAILED is raised; resuming,
// cancelling or a new rollout makes it go away. A pause by the owner or by a changed bundle raises nothing.
func (s *Service) Conditions(ctx context.Context) []health.ExtCond {
	ro, err := s.st.ActiveRollout(ctx)
	if err != nil || ro.Status != store.RolloutPaused {
		return nil
	}
	switch ro.PauseKey {
	case pauseStepFailed, pauseGateFailed, pausePanelRestart:
	default:
		return nil
	}
	params := map[string]string{"rollout_id": ro.ID, "node": ro.PauseParams["node"], "reason": ro.PauseParams["reason"]}
	return []health.ExtCond{{Kind: "update_failed", NodeID: ro.PauseParams["node_id"], Subject: ro.ID, Severity: 2,
		Why: "health.alert.update_failed.why." + ro.PauseKey, Params: params}}
}
