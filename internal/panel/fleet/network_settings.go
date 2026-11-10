package fleet

import (
	"encoding/hex"
	"net/netip"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type networkBundleInfo interface {
	NetworkTableFile() (name, sha256 string)
}

type networkTableReader interface {
	Lookup(netip.Addr) uint32
	Name(asn uint32) (country, name string, ok bool)
	Built() time.Time
}

// nodeSettings includes network settings only for agents that advertise nets/1.
func (f *Fleet) nodeSettings(n store.NodeRow, caps []string) *agentv1.NodeSettings {
	s := nodeSettings(n, caps)
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
