package warp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// rpc is the admin WarpService (warp.proto). Roles are decided by auth/policy.go before a call gets here (all
// owner-only); register, import and delete also need a fresh step-up, checked first, before anything is read.
type rpc struct{ s *Service }

func (r rpc) GetWarp(ctx context.Context, req *connect.Request[adminv1.GetWarpRequest]) (*connect.Response[adminv1.GetWarpResponse], error) {
	s := r.s
	n, err := s.node(ctx, req.Msg.NodeId, false)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.GetWarpResponse{TosUrl: TOSURL, AgentSupports: s.supports(n)}
	live, err := s.liveNode(ctx, n.ID)
	if err != nil {
		return nil, s.internal("load live node", err)
	}
	a, err := s.st.WarpAccount(ctx, n.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewResponse(resp), nil
	case err != nil:
		return nil, s.internal("load account", err)
	}
	resp.Account = s.accountMsg(a)
	resp.Health = healthMsg(a)
	resp.NeedsAttention, resp.AttentionReason = a.Attention != "", a.Attention
	if resp.Inbounds, err = s.warpInbounds(ctx, n.ID); err != nil {
		return nil, s.internal("warp inbounds", err)
	}
	// Applied means the node confirmed the whole desired state that was computed (it carries the account); a
	// stream is needed for anything to be pending.
	resp.PendingApply = live.Connected && n.DesiredHash != n.AppliedHash
	return connect.NewResponse(resp), nil
}

func (r rpc) RegisterWarp(ctx context.Context, req *connect.Request[adminv1.RegisterWarpRequest]) (*connect.Response[adminv1.RegisterWarpResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	a, err := r.s.register(ctx, req.Msg.NodeId, req.Msg.AcceptTos, req.Msg.TosUrlShown, req.Msg.ReplaceExisting)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RegisterWarpResponse{Account: r.s.accountMsg(a)}), nil
}

func (r rpc) ImportWarp(ctx context.Context, req *connect.Request[adminv1.ImportWarpRequest]) (*connect.Response[adminv1.ImportWarpResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	a, err := r.s.importAccount(ctx, req.Msg.NodeId, req.Msg.ProfileConf, req.Msg.AccountToml)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ImportWarpResponse{Account: r.s.accountMsg(a)}), nil
}

func (r rpc) SetWarpEnabled(ctx context.Context, req *connect.Request[adminv1.SetWarpEnabledRequest]) (*connect.Response[adminv1.SetWarpEnabledResponse], error) {
	a, err := r.s.setEnabled(ctx, req.Msg.NodeId, req.Msg.Enabled)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetWarpEnabledResponse{Account: r.s.accountMsg(a)}), nil
}

func (r rpc) RestartWarp(ctx context.Context, req *connect.Request[adminv1.RestartWarpRequest]) (*connect.Response[adminv1.RestartWarpResponse], error) {
	a, confirmed, err := r.s.restart(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RestartWarpResponse{Account: r.s.accountMsg(a), Confirmed: confirmed}), nil
}

func (r rpc) RefreshWarp(ctx context.Context, req *connect.Request[adminv1.RefreshWarpRequest]) (*connect.Response[adminv1.RefreshWarpResponse], error) {
	s := r.s
	if _, err := s.node(ctx, req.Msg.NodeId, false); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.refreshLocked(ctx, req.Msg.NodeId, s.cfg.Actor(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RefreshWarpResponse{Account: s.accountMsg(a)}), nil
}

func (r rpc) DeleteWarp(ctx context.Context, req *connect.Request[adminv1.DeleteWarpRequest]) (*connect.Response[adminv1.DeleteWarpResponse], error) {
	if err := r.s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	remote, err := r.s.delete(ctx, req.Msg.NodeId, req.Msg.ConfirmName)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DeleteWarpResponse{RemoteDeleted: remote}), nil
}

func (r rpc) GetWarpRegistrationParams(ctx context.Context, _ *connect.Request[adminv1.GetWarpRegistrationParamsRequest]) (*connect.Response[adminv1.GetWarpRegistrationParamsResponse], error) {
	p := r.s.params(ctx)
	return connect.NewResponse(&adminv1.GetWarpRegistrationParamsResponse{Params: paramsMsg(p), Defaults: paramsMsg(Defaults()), Customised: p.Customised()}), nil
}

func (r rpc) UpdateWarpRegistrationParams(ctx context.Context, req *connect.Request[adminv1.UpdateWarpRegistrationParamsRequest]) (*connect.Response[adminv1.UpdateWarpRegistrationParamsResponse], error) {
	s := r.s
	in := req.Msg.Params
	if in == nil {
		return nil, fail(connect.CodeInvalidArgument, "params_required")
	}
	p := Params{APIVersion: in.ApiVersion, UserAgent: in.UserAgent, CFClientVersion: in.CfClientVersion, TLSSpecID: in.TlsSpecId,
		AutoReregister: in.AutoReregister}
	if err := p.Validate(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.AutoReregister && !s.params(ctx).AutoReregister { // it accepts the terms for accounts not made yet
		if err := s.cfg.StepUp(ctx); err != nil {
			return nil, err
		}
	}
	// Stored as given, empty fields meaning "the built-in value", so that a later release's defaults still apply.
	raw, _ := json.Marshal(p)
	if err := s.st.SetSettings(ctx, map[string]string{SettingKey: string(raw)}); err != nil {
		return nil, s.internal("store params", err)
	}
	p = p.fill()
	s.audit(ctx, s.cfg.Actor(ctx), "warp_params", map[string]string{"api": p.APIVersion, "customised": fmt.Sprint(p.Customised()),
		"auto_reregister": fmt.Sprint(p.AutoReregister)})
	return connect.NewResponse(&adminv1.UpdateWarpRegistrationParamsResponse{Params: paramsMsg(p), Customised: p.Customised()}), nil
}
