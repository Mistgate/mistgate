package fleet

import (
	"encoding/hex"
	"net/netip"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
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
// with os-status/1, the network settings for agents with nets/1 and a set panel link address for agents with
// panel-link/1. An agent without a capability gets nothing of it, so its settings and their signature stay what they were.
func (f *Fleet) nodeSettings(n store.NodeRow, caps []string) *agentv1.NodeSettings {
	s := nodeSettingsFor(n, caps, f.osStatusAllowed())
	if l := f.panelLink.Load(); l != nil && *l != "" && store.CapabilityPresent(caps, agentcap.PanelLink) {
		s.PanelLinkUrl = *l
	}
	if roles := f.subHostRoles.Load(); roles != nil && store.CapabilityPresent(caps, plugin.CapSubHost) {
		if r := (*roles)[n.ID]; r != nil {
			s.SubHost = proto.Clone(r).(*agentv1.SubHostRole)
		}
	}
	if !store.CapabilityPresent(caps, agentcap.Nets) {
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

// SetSubHostRoles sets the subscription host roles by node id (internal/panel/subhost): agents with sub-host/1 are sent
// theirs in NodeSettings.sub_host. nil or empty: no node is a host. The caller calls StateChanged.
func (f *Fleet) SetSubHostRoles(roles map[string]*agentv1.SubHostRole) {
	if len(roles) == 0 {
		f.subHostRoles.Store(nil)
		return
	}
	f.subHostRoles.Store(&roles)
}

// SetPanelLink sets the link address agents with panel-link/1 are told to move to; "" (the default) sends nothing.
// The caller calls StateChanged so connected agents get their new settings.
func (f *Fleet) SetPanelLink(linkURL string) { f.panelLink.Store(&linkURL) }

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
