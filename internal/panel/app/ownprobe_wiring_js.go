//go:build js

package app

import (
	"context"
	"log/slog"

	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// The Cloudflare edition has no probes: a probe is a long-lived client that keeps a secret and its place in a network, and the
// Workers have neither the room for its code nor a process to serialize its reports. edition.go lists own_probes as
// unavailable, the admin hides the function, and no endpoint is mounted.
func newOwnProbes(*core) ownProbeService { return nil }

func ownProbeAdminHandlers(ownProbeService) []httpserver.AdminHandler { return nil }

func ownProbeStates(context.Context, ownProbeService, *slog.Logger) []health.ExternalState {
	return nil
}
