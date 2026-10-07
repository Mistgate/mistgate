package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Everything here acts on the signed-in admin's own data (except ListAudit, owner only);
// the admin comes from RequireSession.

func signedIn(ctx context.Context) (store.Admin, error) {
	a, ok := AdminFrom(ctx)
	if !ok {
		return a, errUnauthenticated
	}
	return a, nil
}

const flagBackupState = 0x10 // authenticator data flag BS

func toProtoPasskey(p store.Passkey) *adminv1.Passkey {
	out := &adminv1.Passkey{
		Id: p.ID, Name: p.Name, CreatedAtUnix: p.CreatedAt.Unix(), Transports: p.Transports,
		BackedUp: p.Flags&flagBackupState != 0,
	}
	if !p.LastUsedAt.IsZero() {
		out.LastUsedAtUnix = p.LastUsedAt.Unix()
	}
	return out
}

// ListPasskeys lists the signed-in admin's passkeys.
func (s *Service) ListPasskeys(ctx context.Context, _ *connect.Request[adminv1.ListPasskeysRequest]) (*connect.Response[adminv1.ListPasskeysResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	pks, err := s.st.PasskeysByAdmin(ctx, admin.ID)
	if err != nil {
		s.log.Error("list passkeys", "err", err)
		return nil, errInternal(err)
	}
	resp := &adminv1.ListPasskeysResponse{}
	for _, p := range pks {
		resp.Passkeys = append(resp.Passkeys, toProtoPasskey(p))
	}
	return connect.NewResponse(resp), nil
}

// BeginAddPasskey starts registering another passkey; the admin's existing ones are excluded.
func (s *Service) BeginAddPasskey(ctx context.Context, req *connect.Request[adminv1.BeginAddPasskeyRequest]) (*connect.Response[adminv1.BeginAddPasskeyResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	// Step-up guards the Begin: FinishAddPasskey needs the ceremony that only a Begin hands out.
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		name = "Passkey"
	}
	if utf8.RuneCountInString(name) > maxPasskeyName {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is too long"))
	}
	pks, err := s.st.PasskeysByAdmin(ctx, admin.ID)
	if err != nil {
		s.log.Error("list passkeys", "err", err)
		return nil, errInternal(err)
	}
	u := &waUser{admin: admin}
	var exclude []protocol.CredentialDescriptor
	for _, p := range pks {
		u.creds = append(u.creds, toCredential(p))
		exclude = append(exclude, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: p.CredentialID})
	}
	creation, data, err := s.wa.BeginRegistration(u, webauthn.WithAuthenticatorSelection(authSelection), webauthn.WithExclusions(exclude))
	if err != nil {
		s.log.Error("begin registration", "err", err)
		return nil, errInternal(err)
	}
	opts, err := json.Marshal(creation.Response)
	if err != nil {
		return nil, errInternal(err)
	}
	id, err := s.putCeremony(ctx, &ceremony{kind: ceremonyAddPasskey, src: SourceKey(s.clientIP(req)), data: *data, admin: admin, name: name})
	if err != nil {
		return nil, ceremonyPutError(err)
	}
	return connect.NewResponse(&adminv1.BeginAddPasskeyResponse{CeremonyId: id, OptionsJson: string(opts)}), nil
}

// FinishAddPasskey verifies the new credential and stores it.
func (s *Service) FinishAddPasskey(ctx context.Context, req *connect.Request[adminv1.FinishAddPasskeyRequest]) (*connect.Response[adminv1.FinishAddPasskeyResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	c, ok, err := s.getCeremony(ctx, req.Msg.CeremonyId, ceremonyAddPasskey)
	if err != nil {
		return nil, errInternal(err)
	}
	expired := func() error {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("registration expired, start again"))
	}
	if !ok {
		return nil, expired()
	}
	if c.admin.ID != admin.ID { // another admin's ceremony is as good as unknown
		if _, err := s.consumeCeremony(ctx, req.Msg.CeremonyId); err != nil {
			s.log.Error("consume foreign add-passkey ceremony", "err", err)
		}
		return nil, expired()
	}
	consumed, err := s.consumeCeremony(ctx, req.Msg.CeremonyId)
	if err != nil {
		s.log.Error("consume add-passkey ceremony", "err", err)
		return nil, errInternal(err)
	}
	if !consumed {
		return nil, expired()
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(req.Msg.CredentialJson))
	if err != nil {
		s.log.Info("add passkey: bad credential json", "err", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid credential"))
	}
	cred, err := s.wa.CreateCredential(&waUser{admin: admin}, c.data, parsed)
	if err != nil {
		s.log.Info("add passkey: credential rejected", "err", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("credential rejected"))
	}
	if _, err := s.st.PasskeyByCredentialID(ctx, cred.ID); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("this passkey is already registered"))
	}
	pk := passkeyFromCredential(admin.ID, c.name, cred)
	now := s.now()
	if err := s.st.AddPasskey(ctx, pk, now); err != nil {
		s.log.Error("add passkey", "err", err)
		return nil, errInternal(err)
	}
	pk.CreatedAt = now
	ip := s.clientIP(req)
	s.audit(ctx, admin.ID, "passkey_add", "ok", ip, map[string]any{"passkey": pk.ID})
	s.emit(Event{Kind: EventPasskeyAdded, AdminID: admin.ID, Method: "passkey", IP: ip.String()})
	return connect.NewResponse(&adminv1.FinishAddPasskeyResponse{Passkey: toProtoPasskey(pk)}), nil
}

// RemovePasskey deletes one of the signed-in admin's passkeys, never the last way in.
func (s *Service) RemovePasskey(ctx context.Context, req *connect.Request[adminv1.RemovePasskeyRequest]) (*connect.Response[adminv1.RemovePasskeyResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	switch err := s.st.DeletePasskey(ctx, admin.ID, req.Msg.Id); {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such passkey"))
	case errors.Is(err, store.ErrLastMethod):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this is your only way to sign in"))
	case err != nil:
		s.log.Error("delete passkey", "err", err)
		return nil, errInternal(err)
	}
	ip := s.clientIP(req)
	s.audit(ctx, admin.ID, "passkey_remove", "ok", ip, map[string]any{"passkey": req.Msg.Id})
	s.emit(Event{Kind: EventPasskeyRemoved, AdminID: admin.ID, Method: "passkey", IP: ip.String()})
	return connect.NewResponse(&adminv1.RemovePasskeyResponse{}), nil
}

func currentHash(req connect.AnyRequest) []byte {
	if tok := sessionToken(req.Header()); tok != "" {
		return hashToken(tok)
	}
	return nil
}

// ListSessions lists the signed-in admin's live sessions.
func (s *Service) ListSessions(ctx context.Context, req *connect.Request[adminv1.ListSessionsRequest]) (*connect.Response[adminv1.ListSessionsResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.st.SessionsByAdmin(ctx, admin.ID)
	if err != nil {
		s.log.Error("list sessions", "err", err)
		return nil, errInternal(err)
	}
	cur, now := currentHash(req), s.now()
	resp := &adminv1.ListSessionsResponse{}
	for _, sess := range all {
		if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) >= SessionIdleTTL {
			continue
		}
		resp.Sessions = append(resp.Sessions, &adminv1.AdminSession{
			Id: hex.EncodeToString(sess.TokenHash), CreatedAtUnix: sess.CreatedAt.Unix(), LastSeenAtUnix: sess.LastSeenAt.Unix(),
			Ip: sess.IP, UserAgent: sess.UserAgent, Current: bytes.Equal(sess.TokenHash, cur),
		})
	}
	return connect.NewResponse(resp), nil
}

// EndSession ends one of the signed-in admin's sessions (the current one too: it logs out).
func (s *Service) EndSession(ctx context.Context, req *connect.Request[adminv1.EndSessionRequest]) (*connect.Response[adminv1.EndSessionResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	h, err := hex.DecodeString(req.Msg.Id)
	if err != nil || len(h) != len(hashToken("")) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such session"))
	}
	if !bytes.Equal(h, currentHash(req)) { // ending the current session is a logout; ending another one needs a step-up
		if err := s.RequireStepUp(ctx); err != nil {
			return nil, err
		}
	}
	ended, err := s.st.DeleteAdminSession(ctx, admin.ID, h)
	if err != nil {
		s.log.Error("delete session", "err", err)
		return nil, errInternal(err)
	}
	if !ended {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such session"))
	}
	s.audit(ctx, admin.ID, "session_end", "ok", s.clientIP(req), nil)
	resp := connect.NewResponse(&adminv1.EndSessionResponse{})
	if bytes.Equal(h, currentHash(req)) {
		resp.Header().Add("Set-Cookie", clearedCookie().String())
	}
	return resp, nil
}

// EndOtherSessions ends every session of the signed-in admin except this one.
func (s *Service) EndOtherSessions(ctx context.Context, req *connect.Request[adminv1.EndOtherSessionsRequest]) (*connect.Response[adminv1.EndOtherSessionsResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	n, err := s.st.DeleteOtherSessions(ctx, admin.ID, currentHash(req))
	if err != nil {
		s.log.Error("delete other sessions", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "sessions_end_others", "ok", s.clientIP(req), map[string]any{"count": n})
	return connect.NewResponse(&adminv1.EndOtherSessionsResponse{Ended: uint32(n)}), nil
}

// The actions of AUDIT_KIND_SIGN_IN and AUDIT_KIND_CHANGES (auth.proto AuditKind). An action that is in neither (an API
// read "call", an agent's "mcp_plan") is still listed under "all".
var (
	auditSignInActions = []string{
		"setup", "login", "login_password", "logout", "lockout", "stepup", "captcha", "turnstile_off", "page_unlock_lockout",
		"passkey_add", "passkey_remove", "session_end", "sessions_end_others", "password_change", "password_add", "totp_rebind", "reset_login",
	}
	auditChangeActions = []string{"instance_update", "security_update", "subscription_settings_update", "mcp_apply", "panel_update",
		"node_update_schedule", "node_update_schedule_cancel", "node_dns_options", "page_dns_choice"}
	auditChangePrefixes = []string{"user_", "users_", "group_", "profile_", "inbound_", "preset_", "device_", "node.", "warp_", "update_", "health.", "token_", "approval_",
		"backup_"}
)

var auditSources = map[adminv1.AuditSource]string{
	adminv1.AuditSource_AUDIT_SOURCE_PANEL: store.AuditPanel,
	adminv1.AuditSource_AUDIT_SOURCE_BOT:   store.AuditBot,
	adminv1.AuditSource_AUDIT_SOURCE_MCP:   store.AuditMCP,
	adminv1.AuditSource_AUDIT_SOURCE_API:   store.AuditAPI,
}

// ListAudit pages through the audit log (owner only), optionally for one source.
func (s *Service) ListAudit(ctx context.Context, req *connect.Request[adminv1.ListAuditRequest]) (*connect.Response[adminv1.ListAuditResponse], error) {
	if _, err := requireOwner(ctx); err != nil {
		return nil, err
	}
	source := ""
	if req.Msg.Source != adminv1.AuditSource_AUDIT_SOURCE_UNSPECIFIED {
		var ok bool
		if source, ok = auditSources[req.Msg.Source]; !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown audit source"))
		}
	}
	limit := int(req.Msg.PageSize)
	if limit == 0 {
		limit = 50
	}
	limit = min(limit, 200)
	q := store.AuditQuery{Source: source, BeforeID: req.Msg.BeforeId, Limit: limit + 1}
	switch req.Msg.Kind {
	case adminv1.AuditKind_AUDIT_KIND_UNSPECIFIED:
	case adminv1.AuditKind_AUDIT_KIND_SIGN_IN:
		q.Actions = auditSignInActions
	case adminv1.AuditKind_AUDIT_KIND_CHANGES:
		q.Actions, q.Prefixes = auditChangeActions, auditChangePrefixes
	case adminv1.AuditKind_AUDIT_KIND_FAILURES:
		q.Failed = true
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown audit kind"))
	}
	rows, err := s.st.ListAuditQuery(ctx, q)
	if err != nil {
		s.log.Error("list audit", "err", err)
		return nil, errInternal(err)
	}
	resp := &adminv1.ListAuditResponse{}
	if len(rows) > limit {
		rows = rows[:limit]
		resp.NextBeforeId = rows[limit-1].ID
	}
	for _, r := range rows {
		src := adminv1.AuditSource_AUDIT_SOURCE_UNSPECIFIED
		for k, v := range auditSources {
			if v == r.Source {
				src = k
			}
		}
		resp.Entries = append(resp.Entries, &adminv1.AuditEntry{
			Id: r.ID, TimeUnix: r.Time.Unix(), Source: src, ActorId: r.Actor, ActorName: r.ActorName,
			Action: r.Action, ParamsJson: r.Params, Result: r.Result, Ip: r.IP,
		})
	}
	return connect.NewResponse(resp), nil
}
