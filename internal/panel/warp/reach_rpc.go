//go:build !js

package warp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"

	warpreachv1 "github.com/mistgate/mistgate/gen/mistgate/warpreach/v1"
	"github.com/mistgate/mistgate/gen/mistgate/warpreach/v1/warpreachv1connect"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ReachHandler is the WarpReachService (proto/mistgate/warpreach/v1): the owner's list of sites. A package of its own, kept out of
// the Cloudflare Workers (the app mounts a refusing stub there). Mount it like Handler.
func (s *Service) ReachHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return warpreachv1connect.NewWarpReachServiceHandler(reachRPC{s}, opts...)
}

type reachRPC struct{ s *Service }

func (r reachRPC) GetWarpReachSettings(ctx context.Context, _ *connect.Request[warpreachv1.GetWarpReachSettingsRequest]) (*connect.Response[warpreachv1.GetWarpReachSettingsResponse], error) {
	domains, customised := r.s.reachDomains(ctx)
	return connect.NewResponse(&warpreachv1.GetWarpReachSettingsResponse{Settings: &warpreachv1.WarpReachSettings{Domains: domains},
		Defaults: &warpreachv1.WarpReachSettings{Domains: DefaultReachDomains}, Customised: customised}), nil
}

func (r reachRPC) UpdateWarpReachSettings(ctx context.Context, req *connect.Request[warpreachv1.UpdateWarpReachSettingsRequest]) (*connect.Response[warpreachv1.UpdateWarpReachSettingsResponse], error) {
	s := r.s
	if req.Msg.UseDefaults {
		if err := s.st.DeleteSettings(ctx, SettingReach); err != nil {
			return nil, s.internal("store reach settings", err)
		}
		s.audit(ctx, s.cfg.Actor(ctx), "warp_reach_settings", map[string]string{"customised": "false", "domains": strings.Join(DefaultReachDomains, ",")})
		return connect.NewResponse(&warpreachv1.UpdateWarpReachSettingsResponse{Settings: &warpreachv1.WarpReachSettings{Domains: DefaultReachDomains}}), nil
	}
	list := req.Msg.GetSettings().GetDomains()
	if len(list) > plugin.MaxWarpReachDomains {
		return nil, fail(connect.CodeInvalidArgument, "reach_domains_too_many")
	}
	for i, d := range list {
		if !plugin.ValidWarpReachDomain(d) {
			return nil, fail(connect.CodeInvalidArgument, "reach_domain_invalid")
		}
		if slices.Contains(list[:i], d) {
			return nil, fail(connect.CodeInvalidArgument, "reach_domain_duplicate")
		}
	}
	raw, _ := json.Marshal(append([]string{}, list...)) // [] and not null: an empty list is stored, and it means "off"
	if err := s.st.SetSettings(ctx, map[string]string{SettingReach: string(raw)}); err != nil {
		return nil, s.internal("store reach settings", err)
	}
	s.audit(ctx, s.cfg.Actor(ctx), "warp_reach_settings", map[string]string{"customised": "true", "domains": strings.Join(list, ",")})
	return connect.NewResponse(&warpreachv1.UpdateWarpReachSettingsResponse{Settings: &warpreachv1.WarpReachSettings{Domains: append([]string{}, list...)}, Customised: true}), nil
}
