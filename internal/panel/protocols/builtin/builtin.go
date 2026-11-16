// Package builtin builds the registry of the protocol plugins shipped with the panel.
package builtin

import (
	"github.com/mistgate/mistgate/internal/panel/protocols"
)

// Registry returns a registry with every built-in protocol: hysteria2, vless, awg and mtproto everywhere, and on the VPS edition
// also the experimental trusttunnel (plugins).
func Registry() *protocols.Registry {
	r, err := protocols.NewRegistry(plugins()...)
	if err != nil {
		panic(err) // duplicate ids among built-ins: a programming error
	}
	return r
}
