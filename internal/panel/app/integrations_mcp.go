//go:build !js

package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// mcpEndpoint is httpserver.Config.MCP: the MCP handler behind the bearer middleware of auth.
// onWaiting (may be nil) is told when a plan starts waiting for the owner's approval (the Telegram alert).
func mcpEndpoint(authSvc *auth.Service, st *store.Store, log *slog.Logger, onWaiting func(tool, tokenName string), now func() time.Time) func(api http.Handler) http.Handler {
	return func(api http.Handler) http.Handler {
		h, err := mcp.New(mcpConfig(authSvc, st, log, onWaiting, now, api))
		if err != nil {
			// New fails only on a missing part, a programming error: serve nothing rather than something unguarded.
			log.Error("mcp endpoint is off", "err", err)
			return http.NotFoundHandler()
		}
		return authSvc.RequireBearer(h, auth.ChannelMCP)
	}
}
