package access

import (
	"context"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/plugin"
)

// The subscription host's 443 (design/adr/0018-panel-behind-nat.md): while a node is a host, a change of its inbounds that
// would make the role unfit there (plugin.SubHostRoutes: a REALITY server showing someone else's site, a Hysteria2 site holding
// TCP 443, a service that cannot share the port, one that asks for the subscription name) is refused, as the agent would refuse
// to run the role.

// subHostRoles is the role of a node, set by the subscription host (SetSubHostRoles).
type subHostRoles = func(nodeID string) (plugin.SubHostRole, bool)

// SetSubHostRoles tells access which nodes are subscription hosts, and with what role.
func (s *Service) SetSubHostRoles(roles func(nodeID string) (plugin.SubHostRole, bool)) {
	s.hostRoles.Store(&roles)
}

func (s *Service) hostRole(nodeID string) (plugin.SubHostRole, bool) {
	if p := s.hostRoles.Load(); p != nil && *p != nil {
		return (*p)(nodeID)
	}
	return plugin.SubHostRole{}, false
}

// enabledSpecs are the specs of the enabled inbounds among listens.
func enabledSpecs(listens []nodeListen, skip string) []plugin.InboundSpec {
	var out []plugin.InboundSpec
	for _, l := range listens {
		if l.enabled && l.spec.ID != "" && l.spec.ID != skip {
			out = append(out, l.spec)
		}
	}
	return out
}

// hostRefusal refuses a change that leaves the node's inbounds after (the enabled ones) unfit for its subscription host role,
// when the inbounds before it were not unfit for that same reason.
func (s *Service) hostRefusal(nodeID, node string, before, after []plugin.InboundSpec) error {
	role, ok := s.hostRole(nodeID)
	if !ok {
		return nil
	}
	_, now := plugin.SubHostRoutes(role, after)
	if now == "" {
		return nil
	}
	if _, was := plugin.SubHostRoutes(role, before); was == now {
		return nil
	}
	return coded(connect.CodeFailedPrecondition, "sub_host_inbound_unfit", "node", node, "reason", now)
}

// hostRefusalOf is hostRefusal for spec (new, changed, switched on or off) among the node's inbounds as they are stored.
func (s *Service) hostRefusalOf(ctx context.Context, nodeID, node string, spec plugin.InboundSpec, enabled bool) error {
	if _, ok := s.hostRole(nodeID); !ok {
		return nil
	}
	all, _, err := s.nodeInbounds(ctx, nodeID, "")
	if err != nil {
		return err
	}
	after := enabledSpecs(all, spec.ID)
	if enabled {
		after = append(after, spec)
	}
	return s.hostRefusal(nodeID, node, enabledSpecs(all, ""), after)
}

// hostRefusalOfDelete is hostRefusal for the removal of inbound id.
func (s *Service) hostRefusalOfDelete(ctx context.Context, nodeID, node, id string) error {
	if _, ok := s.hostRole(nodeID); !ok {
		return nil
	}
	all, _, err := s.nodeInbounds(ctx, nodeID, "")
	if err != nil {
		return err
	}
	return s.hostRefusal(nodeID, node, enabledSpecs(all, ""), enabledSpecs(all, id))
}
