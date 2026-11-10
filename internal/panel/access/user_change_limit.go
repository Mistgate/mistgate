package access

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// TokenUserChangeLimit is how many users an API token may change without the owner in the fixed one-hour window that
// starts with its first counted change. A leaked token must not be able to rewrite every user before anyone looks.
const TokenUserChangeLimit = 10

var tokenUserChangeWindow = securitylimit.Window{Name: "token-user-changes", Limit: TokenUserChangeLimit, Span: time.Hour}

// TokenUserChangeWindow is the shared limiter window the MCP layer peeks at to decide, at plan time, whether a plan would
// be refused at apply and so must wait for the owner.
func TokenUserChangeWindow() securitylimit.Window { return tokenUserChangeWindow }

type tokenUserChangeReservation struct {
	service     *Service
	ctx         context.Context
	principal   auth.Principal
	asked       int
	touched     int
	windowStart time.Time
}

func (r *tokenUserChangeReservation) markTouched(n int) {
	if r != nil {
		r.touched += n
	}
}

// finish refunds with a context that outlives a cancelled request: a client that hangs up must not burn the token's units.
func (r *tokenUserChangeReservation) finish() {
	if r == nil || r.touched >= r.asked {
		return
	}
	if err := r.service.limiter.UndoN(context.WithoutCancel(r.ctx), tokenUserChangeWindow, r.principal.TokenID, r.asked-r.touched, r.windowStart); err != nil {
		r.service.log.Warn("refund unsuccessful token user-change reservation", "code", "token_user_change_refund_failed", "token_id", r.principal.TokenID, "err", err)
	}
}

// reserveTokenUserChanges reserves the whole call before its first write. A caller refunds units
// for users it did not change, while keeping units for writes that succeeded before a later error.
func (s *Service) reserveTokenUserChanges(ctx context.Context, procedure string, asked int) (*tokenUserChangeReservation, error) {
	p := auth.PrincipalFrom(ctx)
	if asked <= 0 || !p.Token || p.Assistant || p.AllowBulkUserChanges || auth.HasApprovedUserChangePlan(ctx) ||
		(p.Channel != auth.ChannelAPI && p.Channel != auth.ChannelMCP) {
		return nil, nil
	}
	if p.TokenID == "" || s.limiter == nil {
		s.refusedTokenUserChanges(ctx, p, procedure, -1, asked)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("user change limit unavailable"))
	}
	d, err := s.limiter.RecordN(ctx, tokenUserChangeWindow, p.TokenID, asked)
	if err != nil {
		s.log.Warn("check token user-change limit", "code", "token_user_change_limit_unavailable", "token_id", p.TokenID, "procedure", procedure, "err", err)
		s.refusedTokenUserChanges(ctx, p, procedure, -1, asked)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("user change limit unavailable"))
	}
	used := TokenUserChangeLimit - d.Remaining
	if !d.Allowed {
		retrySeconds := int64((d.RetryAfter + time.Second - 1) / time.Second)
		s.refusedTokenUserChanges(ctx, p, procedure, used, asked)
		kv := []string{"limit", strconv.Itoa(TokenUserChangeLimit), "used", strconv.Itoa(used), "asked", strconv.Itoa(asked),
			"retry_after_seconds", strconv.FormatInt(retrySeconds, 10)}
		if asked > TokenUserChangeLimit {
			// Waiting never helps a call that asks for more than the whole limit: it has to be split, or the owner asked.
			// The reason is a field of its own, so a script still parses every value.
			kv = append(kv, "reason", "over_hourly_limit")
		}
		return nil, coded(connect.CodeResourceExhausted, "token_user_change_limit", kv...)
	}
	return &tokenUserChangeReservation{service: s, ctx: ctx, principal: p, asked: asked, windowStart: d.WindowStart}, nil
}

// refusedTokenUserChanges records a refusal in the audit log with the token as the actor, and as a warning event at most
// once per token in a fixed one-hour window, so a token hammering the limit does not flood the event list. used is -1 when
// the limiter itself failed and the count is unknown.
func (s *Service) refusedTokenUserChanges(ctx context.Context, p auth.Principal, procedure string, used, asked int) {
	params := map[string]any{"procedure": procedure, "limit": TokenUserChangeLimit, "asked": asked}
	if used < 0 {
		params["reason"] = "limiter_unavailable"
	} else {
		params["used"] = used
	}
	b, _ := json.Marshal(params)
	e := store.AuditEntry{Actor: p.ActorID(), Action: "user_change_limit", Params: string(b), Result: "refused"}
	if ip := auth.ClientIPFrom(ctx); ip.IsValid() {
		e.IP = ip.String()
	}
	if err := s.st.Audit(ctx, s.now(), e); err != nil {
		s.log.Warn("audit token user-change refusal", "code", "token_user_change_audit_failed", "token_id", p.TokenID, "err", err)
	}
	if s.limiter == nil || p.TokenID == "" {
		return
	}
	gate := securitylimit.Window{Name: "token-user-change-event", Limit: 1, Span: time.Hour}
	d, err := s.limiter.Record(ctx, gate, p.TokenID)
	if err != nil {
		s.log.Warn("gate token user-change event", "code", "token_user_change_event_gate_failed", "token_id", p.TokenID, "err", err)
		return
	}
	if !d.Allowed {
		return
	}
	fields := map[string]string{
		"token_id": p.TokenID, "procedure": procedure, "limit": strconv.Itoa(TokenUserChangeLimit),
		"asked": strconv.Itoa(asked),
	}
	if used < 0 {
		fields["reason"] = "limiter_unavailable"
	} else {
		fields["used"] = strconv.Itoa(used)
	}
	if err := s.st.InsertEvent(ctx, store.EventRow{Time: s.now(), Severity: 2, Code: "user_change_limit", Source: "panel", Params: fields}); err != nil {
		s.log.Warn("write token user-change event", "code", "token_user_change_event_failed", "token_id", p.TokenID, "err", err)
	}
}
