// Package builtin builds the registry of the protocol plugins shipped with the panel.
package builtin

import (
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
)

// Registry returns a registry with every built-in protocol (hysteria2, awg).
func Registry() *protocols.Registry {
	r, err := protocols.NewRegistry(hysteria2.New(), awg.New())
	if err != nil {
		panic(err) // duplicate ids among built-ins: a programming error
	}
	return r
}
