//go:build !edge_link

package access

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

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

func trustTunnelLinksProto(result TrustTunnelKeyLinks) *adminv1.GetTrustTunnelLinksResponse {
	resp := &adminv1.GetTrustTunnelLinksResponse{
		Links: make([]*adminv1.TrustTunnelLink, 0, len(result.Links)), HasKey: result.HasKey, KeyDeviceId: result.DeviceID,
	}
	if !result.IssuedAt.IsZero() {
		resp.KeyIssuedUnix = result.IssuedAt.Unix()
	}
	for _, srv := range result.Links {
		if srv.URI == "" && srv.Note == "" {
			continue
		}
		link := &adminv1.TrustTunnelLink{
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
func (s *Service) GetTrustTunnelLinks(ctx context.Context, req *connect.Request[adminv1.GetTrustTunnelLinksRequest]) (*connect.Response[adminv1.GetTrustTunnelLinksResponse], error) {
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
func (s *Service) IssueTrustTunnelKey(ctx context.Context, req *connect.Request[adminv1.IssueTrustTunnelKeyRequest]) (*connect.Response[adminv1.IssueTrustTunnelKeyResponse], error) {
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
	return connect.NewResponse(&adminv1.IssueTrustTunnelKeyResponse{TrustTunnel: trustTunnelLinksProto(links)}), nil
}

// RevokeTrustTunnelKey removes the key credential from all desired node states and frees its device-limit slot.
func (s *Service) RevokeTrustTunnelKey(ctx context.Context, req *connect.Request[adminv1.RevokeTrustTunnelKeyRequest]) (*connect.Response[adminv1.RevokeTrustTunnelKeyResponse], error) {
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
	return connect.NewResponse(&adminv1.RevokeTrustTunnelKeyResponse{TrustTunnel: trustTunnelLinksProto(links)}), nil
}
