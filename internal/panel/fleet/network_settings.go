package fleet

import (
	"encoding/hex"
	"net/netip"
	"slices"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

type networkBundleInfo interface {
	NetworkTableFile() (name, sha256 string)
}

type networkTableReader interface {
	Lookup(netip.Addr) uint32
	Name(asn uint32) (country, name string, ok bool)
	Built() time.Time
}

// nodeSettings is what one agent is sent: the node's own settings, the operating-system status request for agents
// with os-status/1 and the network settings for agents with nets/1. An agent without a capability gets nothing of it,
// so its settings and their signature stay what they were.
func (f *Fleet) nodeSettings(n store.NodeRow, caps []string) *agentv1.NodeSettings {
	s := nodeSettingsFor(n, caps, f.osStatusAllowed())
	routing := f.panelRouting()
	if t, ok := routing.Tunnels[n.ID]; ok && store.CapabilityPresent(caps, plugin.CapHostTunnel) {
		s.HostTunnel = &agentv1.HostTunnel{TunnelSni: t.SNI, ForwardNames: slices.Clone(t.Names)}
	}
	if routing.LinkURL != "" && store.CapabilityPresent(caps, plugin.CapPanelLink) {
		s.PanelLinkUrl = routing.LinkURL
	}
	if !store.CapabilityPresent(caps, capNetworks) {
		return s
	}
	s.Networks = f.NetworkMode()
	if s.Networks != 0 {
		if updater := f.updates(); updater != nil {
			if bundle, ok := updater.(networkBundleInfo); ok {
				s.NetworksDbName, s.NetworksDbSha256 = bundle.NetworkTableFile()
			}
		}
	}
	return s
}

// NetworkMode returns the in-memory operator setting. The edge edition always returns zero.
func (f *Fleet) NetworkMode() uint32 { return f.networkMode.Load() }

// SetNetworkMode updates the stream setting cache and reports whether it changed.
func (f *Fleet) SetNetworkMode(mode uint32) bool {
	if mode > 2 {
		mode = 1
	}
	if f.cfg.Remote != nil {
		mode = 0
	}
	return f.networkMode.Swap(mode) != mode
}

func validNetworksDigest(value string) string {
	if len(value) != 64 || value != strings.ToLower(value) {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}
