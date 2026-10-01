package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ApiTokenService: the owner creates, lists and revokes API tokens in the signed-in
// UI. Creating and revoking need a fresh step-up. A token can never reach this service (it is not on the token
// allow-list), and the handlers refuse a token principal again, so a mistake in one wall is not the end.

const (
	maxTokenName     = 64
	defaultTokenDays = 90
	maxTokenDays     = 365
	defaultTokenRate = 120
	maxTokenRate     = 600
	tokenHintLen     = 4
	maxTokenIDLen    = 64
	msgOwnerSignedIn = "only the owner, signed in to the admin panel, can do this"
)

type tokenRPC struct{ s *Service }

// TokenHandler returns the Connect path prefix and the handler of ApiTokenService. Like Handler it does not check
// sessions itself: mount it behind RequireSession.
func (s *Service) TokenHandler() (string, http.Handler) {
	return adminv1connect.NewApiTokenServiceHandler(&tokenRPC{s}, connect.WithReadMaxBytes(64<<10))
}

// ownerSession is the admin of a request that must come from the owner in a browser session, never from a token.
func ownerSession(ctx context.Context) (store.Admin, error) {
	if PrincipalFrom(ctx).Token {
		return store.Admin{}, connect.NewError(connect.CodePermissionDenied, errors.New(msgOwnerSignedIn))
	}
	return requireOwner(ctx)
}

func toProtoProfile(p string) adminv1.TokenProfile {
	switch p {
	case store.ProfileReadonly:
		return adminv1.TokenProfile_TOKEN_PROFILE_READONLY
	case store.ProfileOperator:
		return adminv1.TokenProfile_TOKEN_PROFILE_OPERATOR
	case store.ProfileAdmin:
		return adminv1.TokenProfile_TOKEN_PROFILE_ADMIN
	}
	return adminv1.TokenProfile_TOKEN_PROFILE_UNSPECIFIED
}

func fromProtoProfile(p adminv1.TokenProfile) (string, bool) {
	switch p {
	case adminv1.TokenProfile_TOKEN_PROFILE_READONLY:
		return store.ProfileReadonly, true
	case adminv1.TokenProfile_TOKEN_PROFILE_OPERATOR:
		return store.ProfileOperator, true
	case adminv1.TokenProfile_TOKEN_PROFILE_ADMIN:
		return store.ProfileAdmin, true
	}
	return "", false
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func toProtoToken(t store.APIToken) *adminv1.ApiToken {
	via := adminv1.TokenChannel_TOKEN_CHANNEL_UNSPECIFIED
	switch t.LastUsedVia {
	case "api":
		via = adminv1.TokenChannel_TOKEN_CHANNEL_API
	case "mcp":
		via = adminv1.TokenChannel_TOKEN_CHANNEL_MCP
	}
	return &adminv1.ApiToken{
		Id: t.ID, Name: t.Name, Profile: toProtoProfile(t.Profile),
		CreatedUnix: t.CreatedAt.Unix(), ExpiresUnix: t.ExpiresAt.Unix(), LastUsedUnix: unixOrZero(t.LastUsedAt),
		LastUsedIp: t.LastUsedIP, LastUsedVia: via, RevokedUnix: unixOrZero(t.RevokedAt),
		CreatedByName: t.CreatedByName, RateLimitPerMin: uint32(t.RatePerMin), Hint: t.Hint,
	}
}

func invalid(msg string) error { return connect.NewError(connect.CodeInvalidArgument, errors.New(msg)) }

// validTokenName trims name and checks it: 1..64 characters, no control characters.
func validTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", invalid("give the token a name")
	case !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", invalid("the name cannot contain control characters")
	case utf8.RuneCountInString(name) > maxTokenName:
		return "", invalid("the name is too long (at most 64 characters)")
	}
	return name, nil
}

// CreateApiToken makes a token and returns its secret, once. Needs a step-up.
func (r *tokenRPC) CreateApiToken(ctx context.Context, req *connect.Request[adminv1.CreateApiTokenRequest]) (*connect.Response[adminv1.CreateApiTokenResponse], error) {
	s := r.s
	admin, err := ownerSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	m := req.Msg
	name, err := validTokenName(m.Name)
	if err != nil {
		return nil, err
	}
	profile, ok := fromProtoProfile(m.Profile)
	if !ok {
		return nil, invalid("choose what the token may do (profile)")
	}
	days := int(m.TtlDays)
	if days == 0 {
		days = defaultTokenDays
	}
	if days > maxTokenDays {
		return nil, invalid("a token lives at most 365 days")
	}
	rate := int(m.RateLimitPerMin)
	if rate == 0 {
		rate = defaultTokenRate
	}
	if rate > maxTokenRate {
		return nil, invalid("the rate limit is at most 600 requests a minute")
	}

	secret := NewTokenSecret()
	now := s.now()
	tok := store.APIToken{
		ID: store.NewID("tok_"), Name: name, Profile: profile, Hint: secret[len(secret)-tokenHintLen:], RatePerMin: rate,
		CreatedBy: admin.ID, CreatedByName: admin.DisplayName, CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(time.Duration(days) * 24 * time.Hour),
	}
	switch err := s.st.CreateAPIToken(ctx, tok, hashToken(secret)); {
	case errors.Is(err, store.ErrNameTaken):
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a token with this name exists already"))
	case errors.Is(err, store.ErrTokenLimit):
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many tokens (50): revoke one you do not use"))
	case err != nil:
		s.log.Error("create api token", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "token_create", "ok", s.clientIP(req), map[string]any{
		"token_id": tok.ID, "name": tok.Name, "profile": tok.Profile, "ttl_days": days})
	return connect.NewResponse(&adminv1.CreateApiTokenResponse{Token: toProtoToken(tok), Secret: secret}), nil
}

// ListApiTokens lists every token, newest first, with the server's clock.
func (r *tokenRPC) ListApiTokens(ctx context.Context, _ *connect.Request[adminv1.ListApiTokensRequest]) (*connect.Response[adminv1.ListApiTokensResponse], error) {
	s := r.s
	if _, err := ownerSession(ctx); err != nil {
		return nil, err
	}
	toks, err := s.st.ListAPITokens(ctx)
	if err != nil {
		s.log.Error("list api tokens", "err", err)
		return nil, errInternal(err)
	}
	resp := &adminv1.ListApiTokensResponse{NowUnix: s.now().Unix()}
	for _, t := range toks {
		resp.Tokens = append(resp.Tokens, toProtoToken(t))
	}
	return connect.NewResponse(resp), nil
}

// RevokeApiToken revokes a token at once and cancels its open plans. Idempotent. Needs a step-up.
func (r *tokenRPC) RevokeApiToken(ctx context.Context, req *connect.Request[adminv1.RevokeApiTokenRequest]) (*connect.Response[adminv1.RevokeApiTokenResponse], error) {
	s := r.s
	admin, err := ownerSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	id := req.Msg.Id
	if id == "" || len(id) > maxTokenIDLen {
		return nil, invalid("which token?")
	}
	tok, err := s.st.RevokeAPIToken(ctx, id, admin.ID, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such token"))
	}
	if err != nil {
		s.log.Error("revoke api token", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "token_revoke", "ok", s.clientIP(req), map[string]any{"token_id": tok.ID})
	return connect.NewResponse(&adminv1.RevokeApiTokenResponse{Token: toProtoToken(tok)}), nil
}
