//go:build !js

package app

import (
	"context"
	"encoding/json"
	"errors"
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
		h, err := mcp.New(mcp.Config{
			Plans: planStore{st: st, onWaiting: onWaiting},
			Auth:  mcpAuth{authSvc},
			API:   api,
			Audit: func(ctx context.Context, e mcp.AuditEntry) {
				params := "{}"
				if b, err := json.Marshal(e.Params); err == nil {
					params = string(b)
				}
				ip := ""
				if a := auth.ClientIPFrom(ctx); a.IsValid() {
					ip = a.String()
				}
				if err := st.Audit(ctx, now(), store.AuditEntry{
					Actor: e.Actor, Action: e.Action, Params: params, Result: e.Result, Source: store.AuditMCP, IP: ip,
				}); err != nil {
					log.Warn("audit mcp row", "action", e.Action, "err", err)
				}
			},
			Log: log,
		})
		if err != nil {
			// New fails only on a missing part, a programming error: serve nothing rather than something unguarded.
			log.Error("mcp endpoint is off", "err", err)
			return http.NotFoundHandler()
		}
		return authSvc.RequireBearer(h, auth.ChannelMCP)
	}
}

func mcpBackgroundJobs(st *store.Store, log *slog.Logger, now func() time.Time) []BackgroundJob {
	return []BackgroundJob{{Name: "mcp-plan-sweep", Run: func(ctx context.Context) { sweepPlans(ctx, st, log, now) }}}
}

// sweepPlans expires old MCP plans once a minute until ctx ends.
func sweepPlans(ctx context.Context, st *store.Store, log *slog.Logger, now func() time.Time) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if n, err := st.ExpireMCPPlans(ctx, now()); err != nil && ctx.Err() == nil {
			log.Warn("mcp plan sweep", "err", err)
		} else if n > 0 {
			log.Debug("mcp plan sweep", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// mcpAuth gives the MCP layer the auth package's principal and grants.
type mcpAuth struct{ s *auth.Service }

func (a mcpAuth) Principal(ctx context.Context) (mcp.Principal, bool) {
	p := auth.PrincipalFrom(ctx)
	if !p.Token {
		return mcp.Principal{}, false
	}
	return mcp.Principal{TokenID: p.TokenID, Profile: mcp.Profile(p.Profile)}, true
}

func (a mcpAuth) WithChannel(ctx context.Context) context.Context {
	return auth.WithChannel(ctx, auth.ChannelMCP)
}

func (a mcpAuth) WithPlanning(ctx context.Context) context.Context { return auth.WithPlanning(ctx) }

func (a mcpAuth) WithApprovedStepUp(ctx context.Context, planID string) context.Context {
	return a.s.WithApprovedStepUp(ctx, planID)
}

// planStore is mcp.Plans over the store. mcp.Plan and store.MCPPlan have the same fields, so they convert.
type planStore struct {
	st        *store.Store
	onWaiting func(tool, tokenName string) // nil = nobody listens
}

func planErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return mcp.ErrNotFound
	case errors.Is(err, store.ErrTooManyPlans):
		return mcp.ErrTooManyPlans
	}
	return err
}

func (p planStore) CreateMCPPlan(ctx context.Context, pl mcp.Plan, maxOpen, maxAwaiting int) error {
	if err := p.st.CreateMCPPlan(ctx, store.MCPPlan(pl), maxOpen, maxAwaiting); err != nil {
		return planErr(err)
	}
	if pl.NeedsApproval && p.onWaiting != nil {
		name := ""
		if t, err := p.st.GetAPIToken(ctx, pl.TokenID); err == nil {
			name = t.Name
		}
		p.onWaiting(pl.Tool, name)
	}
	return nil
}

func (p planStore) MCPPlanByConfirm(ctx context.Context, tokenID string, h []byte) (mcp.Plan, error) {
	pl, err := p.st.MCPPlanByConfirm(ctx, tokenID, h)
	return mcp.Plan(pl), planErr(err)
}

func (p planStore) GetMCPPlan(ctx context.Context, id string) (mcp.Plan, error) {
	pl, err := p.st.GetMCPPlan(ctx, id)
	return mcp.Plan(pl), planErr(err)
}

func (p planStore) BeginApply(ctx context.Context, id, tokenID, tool string, paramsHash, confirmHash []byte, now time.Time) (mcp.Plan, error) {
	pl, err := p.st.BeginApply(ctx, id, tokenID, tool, paramsHash, confirmHash, now)
	return mcp.Plan(pl), planErr(err)
}

func (p planStore) FinishApply(ctx context.Context, id string, ok bool, result, errText, outcomeCode, outcomeParams string, now time.Time) error {
	return planErr(p.st.FinishApply(ctx, id, ok, result, errText, outcomeCode, outcomeParams, now))
}
