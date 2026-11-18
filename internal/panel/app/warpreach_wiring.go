//go:build !js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// addWarpReachConditions lets the health module raise WARP_REACH from the WARP module's stored results. The Cloudflare edition
// runs no such check (warpreach_wiring_js.go).
func (k *core) addWarpReachConditions(hl *health.Service) { hl.AddConditionSource(k.wsv.Conditions) }

// addWarpReach mounts the WarpReachService (the owner's list of sites) behind the session.
func (k *core) addWarpReach(cfg *httpserver.Config) {
	path, handler := k.wsv.ReachHandler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
}
