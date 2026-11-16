//go:build !js

package access

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	trusttunnelv1 "github.com/mistgate/mistgate/gen/mistgate/trusttunnel/v1"
	"github.com/mistgate/mistgate/gen/mistgate/trusttunnel/v1/trusttunnelv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The TrustTunnel key RPCs are a service of their own (proto/mistgate/trusttunnel/v1), so the Cloudflare edition, which does not
// offer TrustTunnel, links neither the messages nor this code (trusttunnel_js.go).

// TrustTunnelHandler returns the Connect path and handler of TrustTunnelService.
func (s *Service) TrustTunnelHandler() (string, http.Handler) {
	return trusttunnelv1connect.NewTrustTunnelServiceHandler(trustTunnelRPC{s}, handlerOpts...)
}

type trustTunnelRPC struct{ s *Service }

// userForTrustTunnel resolves the owner-selected person for one of the key RPCs.
func (s *Service) userForTrustTunnel(ctx context.Context, id string) (store.AccessUser, error) {
	if id == "" {
		return store.AccessUser{}, invalid("user is required")
	}
	u, err := s.st.Access().User(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.AccessUser{}, notFound("user")
	}
	if err != nil {
		return store.AccessUser{}, s.internal("get user", err)
	}
	return u, nil
}

func trustTunnelLinksProto(result TrustTunnelKeyLinks) *trusttunnelv1.GetTrustTunnelLinksResponse {
	resp := &trusttunnelv1.GetTrustTunnelLinksResponse{
		Links: make([]*trusttunnelv1.TrustTunnelLink, 0, len(result.Links)), HasKey: result.HasKey, KeyDeviceId: result.DeviceID,
	}
	if !result.IssuedAt.IsZero() {
		resp.KeyIssuedUnix = result.IssuedAt.Unix()
	}
	for _, srv := range result.Links {
		if srv.URI == "" && srv.Note == "" {
			continue
		}
		link := &trusttunnelv1.TrustTunnelLink{
			NodeId: srv.NodeID, NodeName: srv.Node, ProfileId: srv.ProfileID, ProfileName: srv.Profile,
			Url: srv.URI, HasIpv6: srv.IPv6Egress, Note: srv.Note,
		}
		if via, ok := strings.CutPrefix(srv.Mark, MarkVia); ok {
			link.Relay, link.Via = true, strings.TrimSpace(via)
		}
		resp.Links = append(resp.Links, link)
	}
	return resp
}

// GetTrustTunnelLinks returns the user's key links. A read only: it makes no device and emits no fetch event.
func (r trustTunnelRPC) GetTrustTunnelLinks(ctx context.Context, req *connect.Request[trusttunnelv1.GetTrustTunnelLinksRequest]) (*connect.Response[trusttunnelv1.GetTrustTunnelLinksResponse], error) {
	s := r.s
	u, err := s.userForTrustTunnel(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	links, err := s.trustTunnelKeyLinks(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(trustTunnelLinksProto(links)), nil
}

// IssueTrustTunnelKey makes the one counted key for an active person whose TrustTunnel switch is on.
func (r trustTunnelRPC) IssueTrustTunnelKey(ctx context.Context, req *connect.Request[trusttunnelv1.IssueTrustTunnelKeyRequest]) (*connect.Response[trusttunnelv1.IssueTrustTunnelKeyResponse], error) {
	s := r.s
	u, err := s.userForTrustTunnel(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	if !u.AppTrustTunnel {
		return nil, failed("app_disabled")
	}
	if _, status := currentSubscriptionUser(u, s.now()); status != StatusActive {
		if u.Disabled {
			return nil, failed("user_disabled")
		}
		return nil, failed("user_inactive")
	}
	if _, err := s.issueTrustTunnelKey(ctx, actor(ctx), u); err != nil {
		return nil, err
	}
	links, err := s.trustTunnelKeyLinks(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&trusttunnelv1.IssueTrustTunnelKeyResponse{TrustTunnel: trustTunnelLinksProto(links)}), nil
}

// RevokeTrustTunnelKey removes the key credential from all desired node states and frees its device-limit slot.
func (r trustTunnelRPC) RevokeTrustTunnelKey(ctx context.Context, req *connect.Request[trusttunnelv1.RevokeTrustTunnelKeyRequest]) (*connect.Response[trusttunnelv1.RevokeTrustTunnelKeyResponse], error) {
	s := r.s
	u, err := s.userForTrustTunnel(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	if err := s.revokeTrustTunnelKey(ctx, actor(ctx), u.ID); err != nil {
		return nil, err
	}
	u, err = s.userForTrustTunnel(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	links, err := s.trustTunnelKeyLinks(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&trusttunnelv1.RevokeTrustTunnelKeyResponse{TrustTunnel: trustTunnelLinksProto(links)}), nil
}
