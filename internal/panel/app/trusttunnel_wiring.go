//go:build !js

package app

import "github.com/mistgate/mistgate/internal/panel/httpserver"

// addTrustTunnel mounts the TrustTunnelService (the tt:// keys of the official apps) behind the session. The Cloudflare edition
// does not offer TrustTunnel (trusttunnel_wiring_js.go).
func (k *core) addTrustTunnel(cfg *httpserver.Config) {
	path, handler := k.acc.TrustTunnelHandler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
}
