//go:build !js

package app

import "github.com/mistgate/mistgate/internal/panel/httpserver"

// addDecoySite mounts the DecoySiteService (the library of decoy sites) behind the session. The Cloudflare edition keeps no
// bundles (decoysite_wiring_js.go).
func (k *core) addDecoySite(cfg *httpserver.Config) {
	path, handler := k.fl.DecoySiteHandler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
}
