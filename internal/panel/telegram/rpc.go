package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The admin TelegramService. The levels are in auth/policy.go: SetTelegramBot is owner-only, the rest is the admin's own
// account (any role). None of it is open to API tokens or MCP (they are not in auth.tokenProcedures), and every handler
// refuses a token principal again on its own.

var botTokenRe = regexp.MustCompile(`^[0-9]{5,16}:[A-Za-z0-9_-]{30,100}$`)

const testEvery = 10 * time.Second

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

var errInternal = fail(connect.CodeInternal, "internal error")

type rpc struct{ s *Service }

// self is the signed-in admin; a token (API or MCP) is refused.
func self(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token {
		return store.Admin{}, fail(connect.CodePermissionDenied, "a signed-in admin is required")
	}
	return a, nil
}

func owner(ctx context.Context) (store.Admin, error) {
	a, err := self(ctx)
	if err != nil {
		return a, err
	}
	if a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func (s *Service) stepUp(ctx context.Context) error {
	return s.cfg.StepUp(ctx)
}

// linkProto is the link as the screen shows it; the chat id goes only to the admin it belongs to.
func linkProto(l store.TelegramLinkRow, own bool) *adminv1.TelegramLink {
	m := &adminv1.TelegramLink{AdminId: l.AdminID, AdminName: l.AdminName, Role: l.Role, Enabled: l.Enabled, LinkedUnix: l.LinkedAt.Unix()}
	if own {
		m.ChatId = l.ChatID
	}
	return m
}

// status is what the screen shows to this admin.
func (s *Service) status(ctx context.Context, a store.Admin) (*adminv1.TelegramStatus, error) {
	out := &adminv1.TelegramStatus{Bot: &adminv1.TelegramBot{}, AdminUrlKnown: s.cfg.AdminURL != ""}
	switch b, err := s.st.TelegramBot(ctx); {
	case err == nil:
		out.Bot = &adminv1.TelegramBot{Configured: true, Username: b.Username, Error: s.botError()}
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	links, err := s.st.TelegramLinks(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.AdminID == a.ID {
			out.Mine = linkProto(l, true)
		}
		if a.Role == store.RoleOwner {
			out.Links = append(out.Links, linkProto(l, false))
		}
	}
	return out, nil
}

func (s *Service) respond(ctx context.Context, a store.Admin) (*adminv1.TelegramStatus, error) {
	st, err := s.status(ctx, a)
	if err != nil {
		s.log.Error("telegram: status", "err", err)
		return nil, errInternal
	}
	return st, nil
}

func (r rpc) GetTelegram(ctx context.Context, _ *connect.Request[adminv1.GetTelegramRequest]) (*connect.Response[adminv1.GetTelegramResponse], error) {
	a, err := self(ctx)
	if err != nil {
		return nil, err
	}
	st, err := r.s.respond(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetTelegramResponse{Status: st}), nil
}

func (r rpc) SetTelegramBot(ctx context.Context, req *connect.Request[adminv1.SetTelegramBotRequest]) (*connect.Response[adminv1.SetTelegramBotResponse], error) {
	a, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.s.stepUp(ctx); err != nil {
		return nil, err
	}
	s := r.s
	if req.Msg.Clear {
		dropped, err := s.st.ClearTelegramBot(ctx)
		if err != nil {
			s.log.Error("telegram: clear bot", "err", err)
			return nil, errInternal
		}
		s.dropCodes("")
		s.resetOffset(ctx)
		s.auditCtx(ctx, a, "telegram_bot_clear", map[string]any{"dropped_links": dropped})
		s.botChanged(true)
		return respondBot(ctx, s, a)
	}
	token := vault.Redacted(strings.TrimSpace(req.Msg.Token))
	if !botTokenRe.MatchString(token.Reveal()) {
		return nil, fail(connect.CodeInvalidArgument, "token_invalid")
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	me, err := s.api.getMe(cctx, token)
	if err != nil {
		if errCode(err) == "unauthorized" {
			return nil, fail(connect.CodeFailedPrecondition, "telegram_unauthorized")
		}
		return nil, fail(connect.CodeUnavailable, "telegram_unreachable")
	}
	if !me.IsBot || me.Username == "" {
		return nil, fail(connect.CodeInvalidArgument, "token_invalid")
	}
	// Long polling and a webhook exclude each other; the panel takes no webhook, so one left on this bot is removed.
	if err := s.api.deleteWebhook(cctx, token); err != nil {
		s.log.Warn("telegram: deleteWebhook", "code", errCode(err))
	}
	prev, prevErr := s.st.TelegramBot(ctx)
	dropped, err := s.st.SetTelegramBot(ctx, store.TelegramBotRow{Token: s.vault.Seal([]byte(token.Reveal()), tokenRecordID), BotID: me.ID, Username: me.Username}, s.now())
	if err != nil {
		s.log.Error("telegram: save bot", "err", err)
		return nil, errInternal
	}
	newBot := prevErr != nil || prev.BotID != me.ID
	if newBot {
		s.dropCodes("")
		s.resetOffset(ctx)
	}
	s.auditCtx(ctx, a, "telegram_bot_set", map[string]any{"bot": me.Username, "dropped_links": dropped})
	s.botChanged(newBot)
	return respondBot(ctx, s, a)
}

func respondBot(ctx context.Context, s *Service, a store.Admin) (*connect.Response[adminv1.SetTelegramBotResponse], error) {
	st, err := s.respond(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetTelegramBotResponse{Status: st}), nil
}

func (r rpc) BeginTelegramLink(ctx context.Context, _ *connect.Request[adminv1.BeginTelegramLinkRequest]) (*connect.Response[adminv1.BeginTelegramLinkResponse], error) {
	a, err := self(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.s.stepUp(ctx); err != nil {
		return nil, err
	}
	b, err := r.s.st.TelegramBot(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fail(connect.CodeFailedPrecondition, "telegram_not_configured")
	}
	if err != nil {
		r.s.log.Error("telegram: read bot", "err", err)
		return nil, errInternal
	}
	code, expires := r.s.newCode(a.ID)
	return connect.NewResponse(&adminv1.BeginTelegramLinkResponse{
		Code: code, DeepLink: "https://t.me/" + b.Username + "?start=" + code, ExpiresUnix: expires.Unix(),
	}), nil
}

func (r rpc) UnlinkTelegram(ctx context.Context, _ *connect.Request[adminv1.UnlinkTelegramRequest]) (*connect.Response[adminv1.UnlinkTelegramResponse], error) {
	a, err := self(ctx)
	if err != nil {
		return nil, err
	}
	had, err := r.s.st.UnlinkTelegram(ctx, a.ID)
	if err != nil {
		r.s.log.Error("telegram: unlink", "err", err)
		return nil, errInternal
	}
	r.s.dropCodes(a.ID)
	if had {
		r.s.auditCtx(ctx, a, "telegram_unlink", nil)
	}
	st, err := r.s.respond(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UnlinkTelegramResponse{Status: st}), nil
}

func (r rpc) SetTelegramAlerts(ctx context.Context, req *connect.Request[adminv1.SetTelegramAlertsRequest]) (*connect.Response[adminv1.SetTelegramAlertsResponse], error) {
	a, err := self(ctx)
	if err != nil {
		return nil, err
	}
	switch err := r.s.st.SetTelegramEnabled(ctx, a.ID, req.Msg.Enabled); {
	case errors.Is(err, store.ErrNotFound):
		return nil, fail(connect.CodeFailedPrecondition, "telegram_not_linked")
	case err != nil:
		r.s.log.Error("telegram: switch alerts", "err", err)
		return nil, errInternal
	}
	st, err := r.s.respond(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetTelegramAlertsResponse{Status: st}), nil
}

func (r rpc) SendTelegramTest(ctx context.Context, _ *connect.Request[adminv1.SendTelegramTestRequest]) (*connect.Response[adminv1.SendTelegramTestResponse], error) {
	a, err := self(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	link, err := s.st.TelegramLink(ctx, a.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fail(connect.CodeFailedPrecondition, "telegram_not_linked")
	}
	if err != nil {
		s.log.Error("telegram: read link", "err", err)
		return nil, errInternal
	}
	tok, ok := s.token(ctx)
	if !ok {
		return nil, fail(connect.CodeFailedPrecondition, "telegram_not_configured")
	}
	s.mu.Lock()
	tooSoon := s.now().Sub(s.lastTest[a.ID]) < testEvery
	if !tooSoon {
		s.lastTest[a.ID] = s.now()
	}
	s.mu.Unlock()
	if tooSoon {
		return nil, fail(connect.CodeResourceExhausted, "telegram_test_too_soon")
	}
	l, brand := s.panel(ctx)
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.api.sendMessage(sctx, tok, link.ChatID, testText(l, brand)); err != nil {
		switch errCode(err) {
		case "unauthorized":
			s.setBotErr("unauthorized")
			return nil, fail(connect.CodeFailedPrecondition, "telegram_unauthorized")
		case "blocked":
			return nil, fail(connect.CodeFailedPrecondition, "telegram_blocked")
		}
		return nil, fail(connect.CodeUnavailable, "telegram_unreachable")
	}
	s.setBotErr("")
	return connect.NewResponse(&adminv1.SendTelegramTestResponse{}), nil
}

// auditCtx writes an audit row for an admin's action. Params never hold a token.
func (s *Service) auditCtx(ctx context.Context, a store.Admin, action string, params map[string]any) {
	ip := ""
	if addr := auth.ClientIPFrom(ctx); addr.IsValid() {
		ip = addr.String()
	}
	if params == nil {
		params = map[string]any{}
	}
	b, err := json.Marshal(params)
	if err != nil {
		return
	}
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: a.ID, Action: action, Params: string(b), Result: "ok", IP: ip}); err != nil {
		s.log.Warn("telegram: audit", "action", action, "err", err)
	}
}
