package app

import (
	"net/http"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// integrationHandlers are ApiTokenService and ApprovalService, owner only behind the session middleware.
func integrationHandlers(authSvc *auth.Service) []httpserver.AdminHandler {
	var out []httpserver.AdminHandler
	for _, h := range []func() (string, http.Handler){authSvc.TokenHandler, authSvc.ApprovalHandler} {
		path, handler := h()
		out = append(out, httpserver.AdminHandler{Path: path, Handler: handler})
	}
	return out
}
