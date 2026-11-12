//go:build js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/telegram"
)

// clientDebug is nil on the Cloudflare edition: it takes no logs (clientdebug_wiring.go).
func (b base) clientDebug(*telegram.Service) subs.ClientDebug { return nil }
