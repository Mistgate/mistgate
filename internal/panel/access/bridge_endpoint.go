package access

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"

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
	var owns443 bool
	vless443 := false
	for _, other := range others {
		if other.enabled && other.spec.Protocol == "vless" && other.spec.Listen.Network == "tcp" && other.spec.Listen.Port == 443 {
			vless443 = true
			break
		}
	}
	for _, other := range others {
		if other.protocol == "bridge" {
			continue
		}
		base = append(base, other)
		if other.enabled && hasOwnHTTPSite(other.spec) {
			// The Hysteria site cannot carry the bridge when a VLESS inbound owns its TCP port.
			if other.spec.Protocol == "hysteria2" && hy2SitePortForBridge(other.spec) == 443 && vless443 {
				continue
			}
			owns443 = true
		}
	}
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

func hasOwnHTTPSite(spec plugin.InboundSpec) bool {
	if spec.Protocol == "hysteria2" && spec.TLS.Mode != 0 && hy2SitePortForBridge(spec) == 443 {
		return true
	}
	if spec.Protocol != "vless" || spec.Listen.Network != "tcp" || spec.Listen.Port != 443 {
		return false
	}
	var settings struct {
		Security string `json:"security"`
		Reality  struct {
			Target string `json:"target"`
		} `json:"reality"`
	}
	if len(spec.Settings) > 0 && json.Unmarshal(spec.Settings, &settings) != nil {
		return false
	}
	return settings.Security == "tls" || settings.Security == "reality" && settings.Reality.Target == "self"
}

func hy2SitePortForBridge(spec plugin.InboundSpec) uint16 {
	var settings struct {
		Masquerade struct {
			TCPPort *uint16 `json:"tcp_port"`
		} `json:"masquerade"`
	}
	if len(spec.Settings) > 0 && json.Unmarshal(spec.Settings, &settings) != nil {
		return 0
	}
	if settings.Masquerade.TCPPort != nil {
		return *settings.Masquerade.TCPPort
	}
	return 443
}
