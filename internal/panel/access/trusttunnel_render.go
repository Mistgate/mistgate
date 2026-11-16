//go:build !js

package access

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

func (s *Service) trustTunnelServers(ctx context.Context, u store.AccessUser, g store.AccessGroup, full []store.AccessInboundFull,
	relays []store.RelayLink, secret string, opt SubOptions) ([]SubServer, error) {
	out := make([]SubServer, 0)
	relaysByInbound := s.subRelays(relays, full, g, u, plugin.FormatTTLink)
	dnsState := s.newNodeDNS(ctx, u.ID)
	for _, f := range full {
		if !s.usableForFormat(f, g, u, plugin.FormatTTLink) {
			continue
		}
		proto, ok := s.reg.Get(f.Profile.Protocol)
		if !ok || !isTrustTunnel(proto) {
			continue
		}
		merged, err := s.mergedSettings(f.Profile)
		if err != nil {
			return nil, err
		}
		spec, err := s.buildSpec(f, merged)
		if err != nil {
			s.log.Error("access: cannot build TrustTunnel inbound", "code", "trusttunnel_inbound_render_failed", "inbound", f.Inbound.ID, "err", err)
			continue
		}
		if spec.Listen.Network != "tcp" {
			continue
		}
		srv := SubServer{
			NodeID: f.Node.ID, InboundID: f.Inbound.ID, PathKey: "direct", Node: f.Node.Name,
			CountryCode: f.Node.CountryCode, Location: f.Node.Location, PublicLabel: f.Node.PublicLabel,
			Profile: f.Profile.Name, Protocol: f.Profile.Protocol, ProfileID: f.Profile.ID, Exit: egressOf(merged),
		}
		srv.IPv6Egress = ipv6Egress(f.Node, srv.Exit)
		dnsServers, _ := dnsState.awg(f.Node.ID) // same person-per-node IPv4 DNS pair used by AmneziaWG
		input := protocols.RenderInput{
			Format: plugin.FormatTTLink, Settings: merged,
			Inbound: inboundView(nodeView(f.Node), spec, f.Inbound.CertPinSHA256),
			UserID:  u.ID, UserName: u.Name, Secret: secret, DNS: dnsServers,
			InboundPublic: json.RawMessage(f.Inbound.PluginPublicJSON), NoIPv6Egress: !srv.IPv6Egress,
		}
		if f.Inbound.DirectInSubscriptions {
			appendRendered := func(server SubServer, in protocols.RenderInput) {
				in.DisplayName = trustTunnelName(server, opt.Name)
				frag, rendered := proto.Render(in)
				if rendered {
					server.URI = string(frag.Data)
					out = append(out, server)
				} else if frag.Note != "" {
					server.Note = frag.Note
					out = append(out, server)
				}
			}
			appendRendered(srv, input)
		}
		if relayUnsupported(f.Profile.Protocol, spec) != "" {
			continue
		}
		for _, l := range relaysByInbound[f.Inbound.ID] {
			if l.Network != "tcp" || l.Port == 0 || l.ViaNodeAddress == "" {
				continue
			}
			twin := srv
			twin.Mark, twin.PathKey = ViaMark(l.ViaNodeCountry), "relay:"+l.ViaNodeID
			tin := twinInput(input, f, spec, l)
			tin.DisplayName = trustTunnelName(twin, opt.Name)
			frag, rendered := proto.Render(tin)
			if rendered {
				twin.URI = string(frag.Data)
				out = append(out, twin)
			} else if frag.Note != "" {
				twin.Note = frag.Note
				out = append(out, twin)
			}
		}
	}
	return out, nil
}

func trustTunnelName(server SubServer, name func(SubServer) string) string {
	if name != nil {
		if display := strings.TrimSpace(name(server)); display != "" {
			return display
		}
	}
	label := ServerLabeler("en")(server.CountryCode, server.Location)
	if server.Profile != "" {
		label += " · " + server.Profile
	}
	if via, ok := strings.CutPrefix(server.Mark, MarkVia); ok {
		label += " · via " + strings.ToUpper(strings.TrimSpace(via))
	} else if server.Mark == MarkIPv6 {
		label += " · IPv6"
	}
	return label
}
