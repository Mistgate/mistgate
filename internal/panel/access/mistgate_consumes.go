//go:build !js

package access

import (
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// mistgateConsumes reports whether the first-party Mistgate app consumes the protocol (TrustTunnel is for that app alone). No
// switch of a link app gives such a protocol; the person's Mistgate switch does, and only to a device that holds a key.
func (s *Service) mistgateConsumes(protocol string) bool {
	p, ok := s.reg.Get(protocol)
	return ok && !protocols.IsPerDevice(p) && protocols.AllowedForApps(p, plugin.ClientMistgate)
}
