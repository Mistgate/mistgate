//go:build js

package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func mcpEndpoint(*auth.Service, *store.Store, *slog.Logger, func(tool, tokenName string), func() time.Time) func(api http.Handler) http.Handler {
	return nil
}

func mcpBackgroundJobs(*store.Store, *slog.Logger, func() time.Time) []BackgroundJob {
	return nil
}
