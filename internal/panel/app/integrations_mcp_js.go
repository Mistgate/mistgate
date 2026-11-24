//go:build js

package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// mcpServerSet is empty on the Cloudflare edition: there is no MCP there.
type mcpServerSet struct{}

func newMCPServerSet() mcpServerSet { return mcpServerSet{} }

func mcpEndpoint(*auth.Service, *store.Store, securitylimit.Limiter, *slog.Logger, func(tool, tokenName string), func() time.Time, mcpServerSet) func(api http.Handler) http.Handler {
	return nil
}
