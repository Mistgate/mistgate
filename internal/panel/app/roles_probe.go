//go:build !js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// probeAdminHandlers mounts the ProbeService (on-demand probes between nodes) on the admin surface of the panel VPS. The
// Cloudflare edition does not serve it: its Worker has no room for the generated code (roles_probe_js.go).
func probeAdminHandlers(fl *fleet.Fleet) []httpserver.AdminHandler {
	path, handler := fl.ProbeHandler()
	return []httpserver.AdminHandler{{Path: path, Handler: handler}}
}
