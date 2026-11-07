//go:build js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// probeAdminHandlers serves nothing on the Cloudflare edition (roles_probe.go).
func probeAdminHandlers(*fleet.Fleet) []httpserver.AdminHandler { return nil }
