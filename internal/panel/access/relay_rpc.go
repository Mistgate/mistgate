package access

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// RelayService (relay.proto): the owner's CRUD of the relays, the ports an entry node forwards to servers on other nodes. The
// forward itself is the agent's (agent.proto "RELAY FORWARDS"); the fleet module puts it into the entry's desired state
// (fleet/desired.go). Here live the data, the port rules and the rule that makes a relay usable.

// relayProtocol stands in the port checks for a neighbour that is a relay, not a profile.
const relayProtocol = "relay"

// relayLabel names a relay where a refusal names the profile that holds a port.
func relayLabel(r store.RelayLink) string { return "relay to " + r.ExitNodeName }

// relayPortOK: the ports an entry may forward are the ones the agent accepts (agent.proto): 443 or 1024 and above.
func relayPortOK(p uint32) bool { return p == 443 || (p >= 1024 && p <= 65535) }

// relayUnchecked is the gate of a relay until the panel can check the path: nothing passes. TODO(relay step 4): replace it
// with CheckRelay's verdict (the leg entry -> exit from the entry's agent, then the path from outside through the entry),
// so that UpdateRelay(enabled = true) switches on only a relay whose check passed, and a failed one stays off with the stage.
func relayUnchecked(context.Context, store.RelayLink) error {
	return coded(connect.CodeFailedPrecondition, "relay_unchecked")
}

// SetRelayGateForTest replaces the gate that lets a relay be switched on (nil restores the real one). Call it before the
// service takes requests.
func (s *Service) SetRelayGateForTest(gate func(context.Context, store.RelayLink) error) {
	if gate == nil {
		gate = relayUnchecked
	}
	s.relayGate = gate
}

// RelayHandler returns the Connect path and handler of RelayService.
func (s *Service) RelayHandler() (string, http.Handler) {
	return adminv1connect.NewRelayServiceHandler(s, handlerOpts...)
}

// relayUsable is the rule for a person's entry of a server: the relay is switched on, the exit's server is usable for the
// person (usable: group, node, app) and live, and the entry node is live and one of the person's nodes. The twin entries of
// the subscription follow it (a later step); the entry node's own forward follows only the part that is not about a
// person (store.RelayLink.Live).
func (s *Service) relayUsable(l store.RelayLink, exit store.AccessInboundFull, g store.AccessGroup, u store.AccessUser) bool {
	return l.Enabled && l.Live() && l.ViaNodeState == "active" && s.usable(exit, g, u) &&
		(u.AllNodes || slices.Contains(u.NodeIDs, l.ViaNodeID))
}

// relayAgentOK: the entry's agent can take a forward. A node that has not connected yet has no capabilities on record and
// is not refused: its first Hello decides.
func relayAgentOK(n store.AccessNode) bool {
	return n.State != "active" || slices.Contains(n.AgentCaps, plugin.CapRelayForward)
}

func (s *Service) relayMsg(ctx context.Context, l store.RelayLink) *adminv1.Relay {
	out := &adminv1.Relay{
		Id: l.ID, InboundId: l.InboundID, ExitNodeId: l.ExitNodeID, ExitNodeName: l.ExitNodeName,
		ProfileId: l.ProfileID, ProfileName: l.ProfileName, Protocol: l.Protocol,
		ViaNodeId: l.ViaNodeID, ViaNodeName: l.ViaNodeName, Network: l.Network, Port: uint32(l.Port),
		Enabled: l.Enabled, State: inboundStates[l.State], LastError: l.LastError, ToIp: l.ToIP, ExitLive: l.Live(),
		AgentSupported: slices.Contains(l.ViaNodeCaps, plugin.CapRelayForward), CreatedUnix: l.CreatedAt.Unix(),
	}
	if !l.AppliedAt.IsZero() {
		out.AppliedUnix = l.AppliedAt.Unix()
	}
	if f, merged, err := s.loadFull(ctx, l.InboundID); err == nil {
		if spec, err := s.buildSpec(f, merged); err == nil {
			out.ExitPort = uint32(spec.Listen.Port)
		}
	}
	return out
}

func (s *Service) ListRelays(ctx context.Context, req *connect.Request[adminv1.ListRelaysRequest]) (*connect.Response[adminv1.ListRelaysResponse], error) {
	links, err := s.st.RelayLinks(ctx, store.RelayFilter{NodeID: req.Msg.NodeId})
	if err != nil {
		return nil, s.internal("list relays", err)
	}
	out := &adminv1.ListRelaysResponse{Relays: make([]*adminv1.Relay, 0, len(links))}
	for _, l := range links {
		out.Relays = append(out.Relays, s.relayMsg(ctx, l))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) CreateRelay(ctx context.Context, req *connect.Request[adminv1.CreateRelayRequest]) (*connect.Response[adminv1.CreateRelayResponse], error) {
	m := req.Msg
	if m.InboundId == "" || m.ViaNodeId == "" {
		return nil, invalid("inbound_id and via_node_id are required")
	}
	if m.Port != 0 && !relayPortOK(m.Port) {
		return nil, invalid("port must be 443 or 1024-65535")
	}
	a := s.st.Access()
	f, merged, err := s.loadFull(ctx, m.InboundId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("inbound")
	case err != nil:
		return nil, s.internal("load inbound", err)
	}
	entry, err := a.Node(ctx, m.ViaNodeId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("node")
	case err != nil:
		return nil, s.internal("get node", err)
	}
	if entry.ID == f.Node.ID {
		return nil, coded(connect.CodeInvalidArgument, "same_node", "node", entry.Name)
	}
	if entry.State == "retired" || f.Node.State == "retired" {
		return nil, coded(connect.CodeFailedPrecondition, "node_retired")
	}
	if !relayAgentOK(entry) {
		return nil, coded(connect.CodeFailedPrecondition, "agent_old", "node", entry.Name)
	}
	spec, err := s.buildSpec(f, merged)
	if err != nil {
		return nil, buildRefusal(err, f.Node.Name)
	}
	if existing, err := s.st.RelayLinks(ctx, store.RelayFilter{InboundID: f.Inbound.ID, ViaNodeID: entry.ID}); err != nil {
		return nil, s.internal("list relays", err)
	} else if len(existing) > 0 {
		return nil, coded(connect.CodeAlreadyExists, "relay_exists", "node", entry.Name, "exit", f.Node.Name)
	}

	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	others, _, err := s.nodeInbounds(ctx, entry.ID, "")
	if err != nil {
		return nil, err
	}
	network := spec.Listen.Network
	port := uint16(m.Port)
	switch {
	case port != 0:
		if err := portRefusal(f.Profile.Protocol, entry.Name, plugin.Listen{Network: network, Port: port}, others, nil, s.now()); err != nil {
			return nil, err
		}
	case relayPortOK(uint32(spec.Listen.Port)) && clashOf(plugin.Listen{Network: network, Port: spec.Listen.Port}, others) == nil:
		port = spec.Listen.Port // the same port as on the exit, when it is free here
	default:
		free := freePorts(f.Profile.Protocol, 1, func(p uint16) bool {
			return relayPortOK(uint32(p)) && clashOf(plugin.Listen{Network: network, Port: p}, others) == nil
		})
		if len(free) == 0 {
			return nil, coded(connect.CodeFailedPrecondition, "no_clean_port")
		}
		port = free[0]
	}
	r := store.Relay{ID: store.NewID("rly_"), InboundID: f.Inbound.ID, ViaNodeID: entry.ID, Network: network, Port: port}
	if err := s.st.CreateRelay(ctx, r, s.now()); err != nil {
		return nil, s.relayStoreError(err, entry.Name, f.Node.Name, port, "create relay")
	}
	s.audit(ctx, actor(ctx), "relay.create", map[string]any{"relay": r.ID, "via": entry.Name, "exit": f.Node.Name,
		"inbound": f.Inbound.ID, "network": network, "port": port})
	link, err := s.st.RelayLink(ctx, r.ID)
	if err != nil {
		return nil, s.internal("read relay", err)
	}
	return connect.NewResponse(&adminv1.CreateRelayResponse{Relay: s.relayMsg(ctx, link)}), nil
}

// relayStoreError turns a refusal of the relay table into the coded error the caller sees.
func (s *Service) relayStoreError(err error, entry, exit string, port uint16, op string) error {
	switch {
	case errors.Is(err, store.ErrRelayExists):
		return coded(connect.CodeAlreadyExists, "relay_exists", "node", entry, "exit", exit)
	case errors.Is(err, store.ErrRelayPortTaken):
		return coded(connect.CodeAlreadyExists, "port_taken", "port", strconv.Itoa(int(port)), "node", entry, "profile", "relay")
	case errors.Is(err, store.ErrRelayNodes):
		return coded(connect.CodeFailedPrecondition, "node_retired")
	case errors.Is(err, store.ErrNotFound):
		return notFound("relay")
	}
	return s.internal(op, err)
}

func (s *Service) UpdateRelay(ctx context.Context, req *connect.Request[adminv1.UpdateRelayRequest]) (*connect.Response[adminv1.UpdateRelayResponse], error) {
	m := req.Msg
	if m.Port != nil && !relayPortOK(*m.Port) {
		return nil, invalid("port must be 443 or 1024-65535")
	}
	link, err := s.st.RelayLink(ctx, m.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("get relay", err)
	}
	var patch store.RelayPatch
	params := map[string]any{"relay": link.ID, "via": link.ViaNodeName, "exit": link.ExitNodeName}
	if m.Port != nil && uint16(*m.Port) != link.Port {
		p := uint16(*m.Port)
		patch.Port = &p
		params["port"] = p
	}
	if m.Enabled != nil && *m.Enabled != link.Enabled {
		on := *m.Enabled
		if on {
			entry, err := s.st.Access().Node(ctx, link.ViaNodeID)
			if err != nil {
				return nil, s.internal("get node", err)
			}
			if !relayAgentOK(entry) {
				return nil, coded(connect.CodeFailedPrecondition, "agent_old", "node", entry.Name)
			}
			if err := s.relayGate(ctx, link); err != nil {
				return nil, err
			}
		}
		patch.Enabled = &on
		params["enabled"] = on
	}
	if patch.Enabled == nil && patch.Port == nil {
		return connect.NewResponse(&adminv1.UpdateRelayResponse{Relay: s.relayMsg(ctx, link)}), nil
	}

	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	if patch.Port != nil {
		others, _, err := s.nodeInbounds(ctx, link.ViaNodeID, link.ID)
		if err != nil {
			return nil, err
		}
		if err := portRefusal(link.Protocol, link.ViaNodeName, plugin.Listen{Network: link.Network, Port: *patch.Port}, others, nil, s.now()); err != nil {
			return nil, err
		}
	}
	if err := s.st.UpdateRelay(ctx, link.ID, patch, s.now()); err != nil {
		port := link.Port
		if patch.Port != nil {
			port = *patch.Port
		}
		return nil, s.relayStoreError(err, link.ViaNodeName, link.ExitNodeName, port, "update relay")
	}
	s.audit(ctx, actor(ctx), "relay.update", params)
	s.notify.StateChanged() // the entry's desired state carries the forward only while it is switched on
	next, err := s.st.RelayLink(ctx, link.ID)
	if err != nil {
		return nil, s.internal("read relay", err)
	}
	return connect.NewResponse(&adminv1.UpdateRelayResponse{Relay: s.relayMsg(ctx, next)}), nil
}

func (s *Service) DeleteRelay(ctx context.Context, req *connect.Request[adminv1.DeleteRelayRequest]) (*connect.Response[adminv1.DeleteRelayResponse], error) {
	link, err := s.st.RelayLink(ctx, req.Msg.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("get relay", err)
	}
	switch err := s.st.DeleteRelay(ctx, link.ID); {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("delete relay", err)
	}
	s.audit(ctx, actor(ctx), "relay.delete", map[string]any{"relay": link.ID, "via": link.ViaNodeName, "exit": link.ExitNodeName, "reason": "admin"})
	s.notify.StateChanged()
	return connect.NewResponse(&adminv1.DeleteRelayResponse{}), nil
}

// auditRelaysGone writes the audit rows of relays that went with their server (DeleteInbound).
func (s *Service) auditRelaysGone(ctx context.Context, gone []store.RelayLink, reason string) {
	for _, l := range gone {
		s.audit(ctx, actor(ctx), "relay.delete", map[string]any{"relay": l.ID, "via": l.ViaNodeName, "exit": l.ExitNodeName, "reason": reason})
	}
}
