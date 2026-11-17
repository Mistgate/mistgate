package access

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ensureBridgeEndpoint picks a listener that does not overlap an inbound, hop range, or relay on the entry. The path is stable
// for the life of the node because an exit may be dialing it while another relay is added.
func (s *Service) ensureBridgeEndpoint(ctx context.Context, entryID string, extra *plugin.Listen) (store.NodeRow, error) {
	n, err := s.st.Node(ctx, entryID)
	if err != nil {
		return store.NodeRow{}, err
	}
	others, _, err := s.nodeInbounds(ctx, entryID, "")
	if err != nil {
		return store.NodeRow{}, err
	}
	base := make([]nodeListen, 0, len(others)+1)
	var specs []plugin.InboundSpec
	for _, other := range others {
		if other.protocol == "bridge" {
			continue
		}
		base = append(base, other)
		if other.enabled && other.spec.Protocol != "" {
			specs = append(specs, other.spec)
		}
	}
	// The bridge rides the node's own site on 443 when one answers there (plugin.TCPPortOwners): a Hysteria2 site that gave
	// the port up to a TCP inbound is not that site.
	owns443 := plugin.TCPPortOwners(specs)[443].Site
	if extra != nil {
		base = append(base, nodeListen{profile: "relay being created", protocol: relayProtocol, enabled: true, l: *extra})
	}

	port := uint16(0)
	if owns443 {
		port = 0
	} else if n.BridgePort == 443 && clashOf(plugin.Listen{Network: "tcp", Port: 443}, base) == nil {
		port = 443
	} else if n.BridgePort >= 1024 && clashOf(plugin.Listen{Network: "tcp", Port: n.BridgePort}, base) == nil {
		port = n.BridgePort
	} else if clashOf(plugin.Listen{Network: "tcp", Port: 443}, base) == nil {
		port = 443
	} else if free := freePorts(relayProtocol, 1, func(p uint16) bool {
		return relayPortOK(uint32(p)) && clashOf(plugin.Listen{Network: "tcp", Port: p}, base) == nil
	}); len(free) > 0 {
		port = free[0]
	} else {
		return store.NodeRow{}, coded(connect.CodeFailedPrecondition, "no_clean_port")
	}

	changed := false
	if n.BridgePort != port {
		if err := s.st.SetNodeBridgePort(ctx, entryID, port); err != nil {
			return store.NodeRow{}, err
		}
		n.BridgePort = port
		changed = true
	}
	if n.BridgePath == "" {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return store.NodeRow{}, err
		}
		path := "/.well-known/mistgate/bridge/" + base64.RawURLEncoding.EncodeToString(secret[:])
		n.BridgePath, err = s.st.EnsureNodeBridgePath(ctx, entryID, path)
		if err != nil {
			return store.NodeRow{}, err
		}
		changed = true
	}
	if changed {
		s.notify.StateChanged() // a probe of the old endpoint carries its port, so it cannot be mistaken for one of this
	}
	return n, nil
}
