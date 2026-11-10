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

func mcpConfig(authSvc *auth.Service, st *store.Store, log *slog.Logger, onWaiting func(tool, tokenName string), now func() time.Time, api http.Handler) mcp.Config {
	return mcp.Config{
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
			source := e.Source
			if source == "" {
				source = store.AuditMCP
			}
			if err := st.Audit(ctx, now(), store.AuditEntry{
				Actor: e.Actor, Action: e.Action, Params: params, Result: e.Result, Source: source, IP: ip,
			}); err != nil {
				log.Warn("audit mcp row", "action", e.Action, "err", err)
			}
		},
		Now: now,
		Log: log,
	}
}

func mcpBackgroundJobs(st *store.Store, log *slog.Logger, now func() time.Time) []BackgroundJob {
	return []BackgroundJob{{Name: "mcp-plan-sweep", Run: func(ctx context.Context) { sweepPlans(ctx, st, log, now) }}}
}

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

// mcpAuth is shared by the network MCP endpoint and the assistant's in-process MCP session.
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

func (a mcpAuth) WithAssistant(ctx context.Context, identity mcp.AssistantIdentity) context.Context {
	if identity.Scheduled {
		return auth.WithScheduledTask(ctx, identity.TokenID, identity.AdminID, identity.AdminName)
	}
	if identity.FollowUp {
		return auth.WithAssistantFollowUp(ctx, identity.TokenID, identity.Profile, identity.AdminID, identity.AdminName)
	}
	return auth.WithAssistant(ctx, identity.TokenID, identity.Profile, identity.AdminID, identity.AdminName)
}

type planStore struct {
	st        *store.Store
	onWaiting func(tool, tokenName string)
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
		token, err := p.st.GetAPIToken(ctx, pl.TokenID)
		if err != nil {
			token, err = p.st.GetAssistantToken(ctx, pl.TokenID)
		}
		if err == nil {
			name = token.Name
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

var _ mcp.Auth = mcpAuth{}
var _ mcp.Plans = planStore{}
