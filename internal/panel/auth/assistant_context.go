package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/mistgate/mistgate/internal/panel/store"
)

type assistantIdentity struct {
	tokenID, profile, adminID, adminName string
}

type assistantIdentityKey struct{}

// WithAssistant marks a server-created MCP call made for an authenticated assistant turn. This context value cannot be
// supplied over HTTP. RequireSession verifies its hidden token row and still applies the token profile, allow-list,
// planning and approved-plan checks before a panel procedure runs.
func WithAssistant(ctx context.Context, tokenID, profile, adminID, adminName string) context.Context {
	if tokenID == "" || adminID == "" {
		return ctx
	}
	actor := "Assistant of " + adminName
	if adminName == "" {
		actor = "Assistant of " + adminID
	}
	ctx = context.WithValue(ctx, assistantIdentityKey{}, assistantIdentity{
		tokenID: tokenID, profile: profile, adminID: adminID, adminName: adminName,
	})
	ctx = WithChannel(ctx, ChannelMCP)
	ctx = store.WithAuditSource(ctx, store.AuditAssistant)
	return store.WithAuditActor(ctx, adminID, actor)
}

func assistantIdentityFrom(ctx context.Context) (assistantIdentity, bool) {
	a, ok := ctx.Value(assistantIdentityKey{}).(assistantIdentity)
	return a, ok
}

func (s *Service) serveAssistant(w http.ResponseWriter, r *http.Request, next http.Handler, id assistantIdentity) {
	tok, err := s.st.GetAssistantToken(r.Context(), id.tokenID)
	if err != nil || !tok.AssistantOnly || tok.CreatedBy != id.adminID || tok.Profile != id.profile || tok.Revoked() || tok.Expired(s.now()) {
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	if channelFrom(r.Context()) != ChannelMCP {
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	p := principalOf(tok, ChannelMCP)
	p.Assistant = true
	p.Name = "Assistant of " + id.adminName
	if id.adminName == "" {
		p.Name = "Assistant of " + id.adminID
	}
	admin := store.Admin{ID: id.adminID, DisplayName: id.adminName, Role: profileRole(tok.Profile)}
	ctx := context.WithValue(r.Context(), adminKey{}, admin)
	ctx = context.WithValue(ctx, principalKey{}, p)
	ctx = context.WithValue(ctx, procedureKey{}, r.URL.Path)
	ctx = store.WithAuditSource(ctx, store.AuditAssistant)
	r = r.WithContext(ctx)
	if msg := s.tokenDenied(r, tok, r.URL.Path); msg != "" {
		s.auditCall(ctx, p, r.URL.Path, http.StatusForbidden, ClientIPFrom(ctx), "")
		b, _ := json.Marshal(map[string]string{"code": "permission_denied", "message": msg})
		writeJSONError(w, http.StatusForbidden, string(b))
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		gateKey := ""
		if levelOf(r.URL.Path) == levelRead && status < 400 {
			gateKey = r.URL.Path
		}
		s.auditCall(ctx, p, r.URL.Path, status, ClientIPFrom(ctx), gateKey)
	}()
	next.ServeHTTP(sw, r)
}
