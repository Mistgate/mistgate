package access

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ownHosts are the hosts that are the panel's own: what a REALITY target set on a node must not be, or REALITY would hand strangers'
// connections to a service of the fleet (the node itself, another node, the panel, a site with a certificate of its own) instead of a
// site of the internet. Names and addresses are compared as text; the node refuses what a name resolves to (internal/node/vless
// target_guard.go).
type ownHosts map[string]bool

// hostKey is the host of a host, a host:port or a URL, as the text the hosts are compared by.
func hostKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	host := raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host = u.Hostname()
	} else if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), "."))
	if a, err := netip.ParseAddr(host); err == nil {
		host = a.Unmap().String()
	}
	return host
}

func (o ownHosts) add(raw string) {
	if host := hostKey(raw); host != "" {
		o[host] = true
	}
}

// ownHostsOf is the panel's own hosts: the panel's addresses, the address of every node of the fleet, and the name of every service of
// a node that holds a certificate of its own (the node's site, TrustTunnel, Hysteria2, a TLS VLESS and its CDN host). The names REALITY
// accepts at another site are not in it: they are somebody else's. skip is the inbound that is being changed.
func (s *Service) ownHostsOf(ctx context.Context, skip string) (ownHosts, error) {
	own := ownHosts{}
	own.add(s.cfg.SubscriptionBaseURL)
	for _, h := range s.cfg.OwnHosts {
		own.add(h)
	}
	nodes, err := s.st.Nodes(ctx, false)
	if err != nil {
		return nil, s.internal("nodes", err)
	}
	for _, n := range nodes {
		own.add(n.Address)
		own.add(n.IPv6Entry)
		inbounds, err := s.st.Access().InboundsFull(ctx, n.ID)
		if err != nil {
			return nil, s.internal("inbounds", err)
		}
		for _, in := range inbounds {
			if in.Inbound.ID == skip {
				continue
			}
			merged, err := s.mergedSettings(in.Profile)
			if err != nil {
				return nil, s.internal("open profile secrets", err)
			}
			spec, err := s.buildSpec(in, merged)
			if err != nil {
				continue // a service that does not build holds nothing: it is reported on its own
			}
			if spec.TLS.Mode != 0 {
				own.add(spec.TLS.ServerName)
			}
			if v, ok := plugin.VLESSOf(spec); ok {
				own.add(v.CDNHost)
			}
		}
	}
	return own, nil
}

// realityTargetOwnRefusal refuses a REALITY target set on a node (the inbound's override, already built into spec) whose host is one of
// the panel's own (reality_target_own). Nothing to check without a target of the node's own.
func (s *Service) realityTargetOwnRefusal(ctx context.Context, f store.AccessInboundFull, spec plugin.InboundSpec) error {
	if f.Inbound.RealityTargetOverride == "" {
		return nil
	}
	target, _, ok := plugin.RealityTarget(spec)
	if !ok {
		return nil
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil
	}
	own, err := s.ownHostsOf(ctx, f.Inbound.ID)
	if err != nil {
		return err
	}
	if own[hostKey(host)] {
		return coded(connect.CodeInvalidArgument, "reality_target_own", "target", target, "node", f.Node.Name)
	}
	return nil
}

// bridgeSiteWarning (bridge_site_lost) says a VLESS server on 443 would leave the node with no site of its own there while a relay through
// the node may carry its reverse bridge on that site (the node has no bridge port of its own): the exit verifies the entry by the name and
// pin of that site, so with another site shown there it has nothing to verify and stops dialing. A warning, not a refusal: the relay
// falls back to the forward way unless the owner fixed it to reverse, and the owner may be moving the bridge elsewhere.
func (s *Service) bridgeSiteWarning(ctx context.Context, nodeID, nodeName string, spec plugin.InboundSpec, settings json.RawMessage, others []nodeListen) (*adminv1.StatusReason, error) {
	if spec.Protocol != "vless" || spec.Listen.Network != "tcp" || protocols.ClientPort(settings, spec.Listen.Port) != 443 {
		return nil, nil
	}
	candidates := append([]nodeListen{{enabled: true, spec: spec, settings: settings}}, others...)
	if publicBridgeSite(candidates) {
		return nil, nil
	}
	node, err := s.st.Node(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // a node that is not stored has no relays
	} else if err != nil {
		return nil, s.internal("get node", err)
	}
	if node.BridgePort != 0 {
		return nil, nil
	}
	links, err := s.st.RelayLinks(ctx, store.RelayFilter{ViaNodeID: nodeID})
	if err != nil {
		return nil, s.internal("relays", err)
	}
	for _, l := range links {
		if l.BridgeServed() {
			return &adminv1.StatusReason{Code: "bridge_site_lost", Params: map[string]string{"node": nodeName, "port": "443"}}, nil
		}
	}
	return nil, nil
}
