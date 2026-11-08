package auth

import (
	"context"
	"encoding/json"
	"net/http"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type assistantIdentity struct {
	tokenID, profile, adminID, adminName string
	// scheduled: a scheduled task of the admin (WithScheduledTask), not a turn the admin is having
	scheduled bool
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

// WithScheduledTask marks a server-created MCP call made by a scheduled task of an admin (internal/panel/tasks): the hidden
// identity store.ScheduledTokenID with the operator profile, whatever the admin's role. Like WithAssistant it cannot come over
// HTTP; RequireSession verifies the row and applies the operator profile, the allow-list and the planning and approval checks.
// It does not need the assistant to be on: a check task runs no model.
func WithScheduledTask(ctx context.Context, tokenID, adminID, adminName string) context.Context {
	if tokenID == "" || adminID == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, assistantIdentityKey{}, assistantIdentity{
		tokenID: tokenID, profile: store.ProfileOperator, adminID: adminID, adminName: adminName, scheduled: true,
	})
	ctx = WithChannel(ctx, ChannelMCP)
	ctx = store.WithAuditSource(ctx, store.AuditAssistant)
	return store.WithAuditActor(ctx, adminID, scheduledActor(adminID, adminName))
}

func scheduledActor(adminID, adminName string) string {
	if adminName == "" {
		return "Scheduled tasks of " + adminID
	}
	return "Scheduled tasks of " + adminName
}

func assistantIdentityFrom(ctx context.Context) (assistantIdentity, bool) {
	a, ok := ctx.Value(assistantIdentityKey{}).(assistantIdentity)
	return a, ok
}

func (s *Service) serveAssistant(w http.ResponseWriter, r *http.Request, next http.Handler, id assistantIdentity) {
	tok, err := s.st.GetAssistantToken(r.Context(), id.tokenID)
	// the scheduled identity and the assistant's are two rows: neither may stand in for the other
	if err != nil || !tok.AssistantOnly || tok.CreatedBy != id.adminID || (tok.ID == store.ScheduledTokenID(id.adminID)) != id.scheduled {
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	current, err := s.st.Admin(r.Context(), id.adminID)
	if err != nil {
		_ = s.st.RevokeAssistantToken(r.Context(), id.tokenID, id.adminID, s.now())
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	profile := assistantProfile(current.Role)
	if id.scheduled {
		// a scheduled task acts with the operator profile, never more: the owner's tasks and a helper's alike (only the owner
		// creates tasks, but a demotion must not leave a task with more than its admin has); a read-only admin's none at all
		if profile != store.ProfileAdmin && profile != store.ProfileOperator {
			_ = s.st.RevokeAssistantToken(r.Context(), id.tokenID, id.adminID, s.now())
			writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
			return
		}
		profile = store.ProfileOperator
	} else {
		if profile == "" {
			_ = s.st.RevokeAssistantToken(r.Context(), id.tokenID, id.adminID, s.now())
			writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
			return
		}
		var enabled int
		var audience int64
		if err = s.st.R.QueryRowContext(r.Context(), `SELECT enabled,audience FROM assistant_settings WHERE id=1`).Scan(&enabled, &audience); err != nil || enabled == 0 || !assistantAudienceAllows(audience, current.Role) {
			_ = s.st.RevokeAssistantToken(r.Context(), id.tokenID, id.adminID, s.now())
			writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
			return
		}
	}
	if tok.Profile != profile || id.profile != profile {
		// A stale turn cannot keep its former privilege. A later turn will recreate the hidden identity at
		// the current profile after its request context has been rebuilt from the admin session.
		_ = s.st.RevokeAssistantToken(r.Context(), id.tokenID, id.adminID, s.now())
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	if tok.Revoked() || tok.Expired(s.now()) {
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	if channelFrom(r.Context()) != ChannelMCP {
		writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
		return
	}
	p := principalOf(tok, ChannelMCP)
	p.Assistant = true
	id.adminName = current.DisplayName
	p.Name = "Assistant of " + id.adminName
	if id.adminName == "" {
		p.Name = "Assistant of " + id.adminID
	}
	if id.scheduled {
		p.Name = scheduledActor(id.adminID, id.adminName)
	}
	admin := current
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

func assistantProfile(role string) string {
	switch role {
	case store.RoleOwner:
		return store.ProfileAdmin
	case store.RoleHelper:
		return store.ProfileOperator
	case store.RoleReadonly:
		return store.ProfileReadonly
	default:
		return ""
	}
}

func assistantAudienceAllows(audience int64, role string) bool {
	switch adminv1.AssistantAudience(audience) {
	case adminv1.AssistantAudience_ASSISTANT_AUDIENCE_EVERYONE:
		return assistantProfile(role) != ""
	case adminv1.AssistantAudience_ASSISTANT_AUDIENCE_ADMINS:
		return role == store.RoleOwner || role == store.RoleHelper
	case adminv1.AssistantAudience_ASSISTANT_AUDIENCE_OWNER:
		return role == store.RoleOwner
	default:
		return false
	}
}
