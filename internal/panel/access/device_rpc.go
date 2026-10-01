package access

import (
	"context"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// awgOnlineWindow is how fresh a handshake must be for the device to count as online (the node's own session window
// is 190 s; the public page uses the same 3 minutes).
const awgOnlineWindow = 3 * time.Minute

// deviceProto describes an AWG device the way the admin pages show it. The fleet writes the time of the newest
// handshake of the device's peer into device.last_seen_at (fleet/l3.go touchAwgDevices), so "online" is a handshake
// younger than awgOnlineWindow.
func (s *Service) deviceProto(d store.AccessAWGDevice) *adminv1.Device {
	return &adminv1.Device{
		Id: d.ID, Platform: d.Platform, Model: d.Model,
		FirstSeenUnix: unixOrZero(d.FirstSeenAt), LastSeenUnix: unixOrZero(d.LastSeenAt),
		Online:       !d.LastSeenAt.IsZero() && s.now().Sub(d.LastSeenAt) < awgOnlineWindow,
		Protocols:    []string{"awg"},
		AwgProfileId: d.ProfileID, AwgProfileName: d.ProfileName, AwgVersion: awgVersion([]byte(d.ProfileSettingsJSON)),
		Stale: d.Stale(), Address: deviceAddress(d.DataJSON), LastHandshakeUnix: unixOrZero(d.LastSeenAt),
	}
}

func configProtos(cfgs []DeviceConfig) []*adminv1.DeviceConfig {
	out := make([]*adminv1.DeviceConfig, len(cfgs))
	for i, c := range cfgs {
		m := &adminv1.DeviceConfig{
			InboundId: c.InboundID, NodeId: c.NodeID, NodeName: c.NodeName, CountryCode: c.CountryCode,
			ProfileName: c.ProfileName, AwgVersion: c.AWGVersion, Conf: c.Conf, VpnKey: c.VPNKey,
			Stale: c.Stale, Warnings: c.Warnings, ConfFilename: c.ConfFilename,
		}
		for _, r := range c.MinClients {
			m.MinClients = append(m.MinClients, &adminv1.ClientRequirement{App: r.App, MinVersion: r.Min})
		}
		out[i] = m
	}
	return out
}

func (s *Service) CreateAwgDevice(ctx context.Context, req *connect.Request[adminv1.CreateAwgDeviceRequest]) (*connect.Response[adminv1.CreateAwgDeviceResponse], error) {
	m := req.Msg
	dev, cfgs, err := s.AddAWGDevice(ctx, actor(ctx), m.UserId, m.ProfileId, m.Platform, m.Label)
	if err != nil {
		return nil, err
	}
	users, err := s.loadUsers(ctx, m.UserId)
	if err != nil || len(users) == 0 {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CreateAwgDeviceResponse{Device: s.deviceProto(dev), Configs: configProtos(cfgs), User: users[0]}), nil
}

func (s *Service) GetDeviceConfigs(ctx context.Context, req *connect.Request[adminv1.GetDeviceConfigsRequest]) (*connect.Response[adminv1.GetDeviceConfigsResponse], error) {
	dev, cfgs, err := s.DeviceConfigs(ctx, actor(ctx), "", req.Msg.DeviceId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetDeviceConfigsResponse{Device: s.deviceProto(dev), Configs: configProtos(cfgs)}), nil
}

func (s *Service) RotateDeviceKeys(ctx context.Context, req *connect.Request[adminv1.RotateDeviceKeysRequest]) (*connect.Response[adminv1.RotateDeviceKeysResponse], error) {
	dev, cfgs, err := s.RotateDevice(ctx, actor(ctx), "", req.Msg.DeviceId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RotateDeviceKeysResponse{Device: s.deviceProto(dev), Configs: configProtos(cfgs)}), nil
}

func (s *Service) RenameDevice(ctx context.Context, req *connect.Request[adminv1.RenameDeviceRequest]) (*connect.Response[adminv1.RenameDeviceResponse], error) {
	dev, err := s.RelabelDevice(ctx, "", req.Msg.DeviceId, req.Msg.Label)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RenameDeviceResponse{Device: s.deviceProto(dev)}), nil
}
