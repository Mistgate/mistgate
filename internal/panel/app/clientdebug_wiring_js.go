//go:build js

package app

import "github.com/mistgate/mistgate/internal/panel/subs"

// clientDebug is nil on the Cloudflare edition: it takes no logs (clientdebug_wiring.go).
func (b base) clientDebug() subs.ClientDebug { return nil }
