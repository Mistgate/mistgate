//go:build !js

package app

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/clientdebug"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// clientDebug is the intake of the logs people send for support (internal/panel/clientdebug) for the client API of the VPS edition.
// The edge edition keeps none: sync tells the app debug.intake false and the debug routes answer 403 debug_off (clientdebug_wiring_js.go).
func (b base) clientDebug() subs.ClientDebug {
	if b.edge {
		return nil
	}
	agentSNIPrefix, _, _ := strings.Cut(b.in.AgentSNI, ".")
	return clientdebug.New(clientdebug.Config{
		Store:   b.st,
		Secrets: []string{b.in.AgentSNI, agentSNIPrefix, b.in.AdminPrefix, b.in.SubPrefix, b.in.LinkPrefix},
		Log:     b.log,
	})
}
