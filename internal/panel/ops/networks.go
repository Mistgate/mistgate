//go:build !js

package ops

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (r rpc) GetNetworkSettings(ctx context.Context, _ *connect.Request[opsv1.GetNetworkSettingsRequest]) (*connect.Response[opsv1.GetNetworkSettingsResponse], error) {
	settings, err := r.s.networkSettings(ctx)
	if err != nil {
		return nil, internalErr(r.s.cfg.Log, "read network settings", err)
	}
	return connect.NewResponse(settings), nil
}

func (r rpc) SetNetworkSettings(ctx context.Context, req *connect.Request[opsv1.SetNetworkSettingsRequest]) (*connect.Response[opsv1.SetNetworkSettingsResponse], error) {
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	mode := req.Msg.GetMode()
	if mode > 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("network mode must be 0, 1 or 2"))
	}
	previousMode, err := s.cfg.Store.NetworkMode(ctx)
	if err != nil {
		return nil, internalErr(s.cfg.Log, "read network settings", err)
	}
	if s.cfg.NetworkSettingsChanged != nil {
		s.cfg.NetworkSettingsChanged(mode)
	}
	if err := s.cfg.Store.SaveNetworkMode(ctx, mode); err != nil {
		if s.cfg.NetworkSettingsChanged != nil {
			s.cfg.NetworkSettingsChanged(previousMode)
		}
		return nil, internalErr(s.cfg.Log, "save network settings", err)
	}
	s.audit(ctx, "network_settings_set", map[string]string{"mode": strconv.FormatUint(uint64(mode), 10)})
	settings, err := s.networkSettings(ctx)
	if err != nil {
		return nil, internalErr(s.cfg.Log, "read network settings", err)
	}
	return connect.NewResponse(&opsv1.SetNetworkSettingsResponse{Settings: settings}), nil
}

func (s *Service) networkSettings(ctx context.Context) (*opsv1.GetNetworkSettingsResponse, error) {
	mode, err := s.cfg.Store.NetworkMode(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.cfg.Store.FleetNetworkNodes(ctx)
	if err != nil {
		return nil, err
	}
	var bundleSHA string
	if s.cfg.NetworkBundle != nil {
		_, bundleSHA = s.cfg.NetworkBundle()
	}
	response := &opsv1.GetNetworkSettingsResponse{Mode: mode}
	for _, node := range nodes {
		if !store.CapabilityPresent(node.AgentCaps, "nets/1") {
			continue
		}
		response.NodesWithNets++
		if bundleSHA != "" && strings.EqualFold(node.NetworksDBSHA256, bundleSHA) {
			response.NodesWithMatchingTable++
		}
	}
	if s.cfg.NetworkTable != nil {
		if table := s.cfg.NetworkTable(); table != nil {
			response.TableBuiltUnix = table.Built().Unix()
			response.Ipv4Ranges = uint32(table.IPv4RangeCount())
			response.Ipv6Ranges = uint32(table.IPv6RangeCount())
			response.Asns = uint32(table.ASNCount())
		}
	}
	return response, nil
}
