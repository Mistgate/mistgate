//go:build !js

package app

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/clientdebug"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/telegram"
)

// clientDebug is the intake of the logs people send for support (internal/panel/clientdebug) for the client API of the VPS edition.
// The edge edition keeps none: sync tells the app debug.intake false and the debug routes answer 403 debug_off (clientdebug_wiring_js.go).
func (b base) clientDebug(tg *telegram.Service) subs.ClientDebug {
	if b.edge {
		return nil
	}
	agentSNIPrefix, _, _ := strings.Cut(b.in.AgentSNI, ".")
	return clientdebug.New(clientdebug.Config{
		Store:   b.st,
		Secrets: []string{b.in.AgentSNI, agentSNIPrefix, b.in.AdminPrefix, b.in.SubPrefix, b.in.LinkPrefix},
		// the owner's Telegram: logs came, or the person refused (the same module and audience as the other news of the panel)
		Notify: func(n clientdebug.Notice) {
			tg.ClientLogs(telegram.ClientLogsNews{Kind: n.Kind, UserID: n.UserID, Person: n.Person, Device: n.Device, Lines: n.Lines})
		},
		Log: b.log,
	})
}
