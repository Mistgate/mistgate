//go:build !js

// Package ops is the panel's own diagnostics and data lifecycle for the owner: the log ring and its level, retention of the
// tables the sweep prunes, a scrubbed export, and the /metrics endpoint; and two reads for every admin, the panel's own
// resources and the fleet's load (internal/panel/capacity). It is the OpsService of proto/mistgate/ops/v1, a package
// of its own so the Cloudflare edition's admin Worker does not link it; that edition mounts a stub that answers UNIMPLEMENTED
// (internal/panel/app/ops_wiring_js.go).
//
// Everything that leaves the process passes through Scrubber: addresses, the panel's secret names, key-shaped values, and only
// an allow-list of log attribute keys. The ring itself keeps raw records and never leaves this package unscrubbed.
package ops

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
	"github.com/mistgate/mistgate/internal/logring"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/capacity"
	"github.com/mistgate/mistgate/internal/panel/rpcstat"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Config configures Service.
type Config struct {
	Store *store.Store
	// Ring and Level are the process logger's (logring.New). A panel built without them (a test) has no logs to show.
	Ring         *logring.Ring
	Level        *slog.LevelVar
	DefaultLevel slog.Level
	Format       string // "text" or "json"
	// Secrets are the panel's secret names (the agent SNI and the secret path prefixes), removed from everything that leaves.
	Secrets []string
	// StepUp is the fresh sign-in check (auth.Service.RequireStepUp) that retention changes, the export and the metrics switch ask for.
	StepUp func(context.Context) error
	// Fleet, Health and Relay are the raw admin services /metrics reads (no session in front of them: the metrics handler has its
	// own token). nil leaves that part of the metrics out.
	Fleet, Health, Relay http.Handler
	// Debug starts and stops the on-demand debug captures of a node (the fleet). nil answers UNIMPLEMENTED for them.
	Debug DebugBackend
	// RPC is the admin API's call counter that the HTTP middleware fills (httpserver.Config.RPCStats); nil leaves the RPC
	// load of GetPanelResources empty.
	RPC *rpcstat.Window
	// Capacity reads the nodes for GetFleetLoad over a window of hours (fleet.CapacityNodes); nil answers with no nodes.
	Capacity func(ctx context.Context, hours int) ([]capacity.Node, error)
	Now      func() time.Time
	Log      *slog.Logger
}

// Service is the OpsService.
type Service struct {
	cfg   Config
	scrub *Scrubber
	m     metricsState
	res   resourcesState
	load  loadCache
}

// New returns the service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.StepUp == nil {
		cfg.StepUp = func(context.Context) error { return nil }
	}
	return &Service{cfg: cfg, scrub: NewScrubber(cfg.Secrets)}
}

// Handler is the Connect handler, mounted at /api/mistgate.ops.v1.OpsService/ behind the session middleware.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return opsv1connect.NewOpsServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(16 << 10)}, opts...)...)
}

type rpc struct{ s *Service }

// audit writes one row for a change the owner made. params never hold a secret.
func (s *Service) audit(ctx context.Context, action string, params map[string]string) {
	actor := "anonymous"
	if a, ok := auth.AdminFrom(ctx); ok {
		actor = a.ID
	}
	if err := s.cfg.Store.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: actor, Action: action, Params: jsonObject(params), Result: "ok"}); err != nil {
		s.cfg.Log.Warn("ops: audit", "code", "audit_unavailable", "action", action, "err", err)
	}
}

func internalErr(log *slog.Logger, what string, err error) error {
	log.Error("ops: "+what, "code", "ops_failed", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}
