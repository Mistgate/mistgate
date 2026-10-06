package subs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The page password (package pagepass). One decision, made where the page is chosen over the subscription (respond):
// a browser that is shown the user page gets the locked page (brand and a password form) until it holds the cookie;
// a client that is sent a subscription (Happ, Mihomo, base64) gets it as before, because an app cannot type a
// password. The self-service calls of the page answer 401 "locked" without the cookie.
//
//	POST <link>/unlock {"password"}   200 and the cookie | 401 wrong_password (+left) | 429 locked (+retry_after)
//
// Entries are counted per token and per client network (UnlockTries within UnlockWindow); the moment either runs out is
// one audit row, without the token and without an address.

// cookieName is the cookie that remembers an unlocked page; it is scoped to the path of the link, so it goes nowhere
// else on the site.
const cookieName = "mg_page"

// cookieMaxAge is how long a browser remembers the password: about 180 days.
const cookieMaxAge = 180 * 24 * 3600

// Auditor writes an audit row; *store.Store implements it. Optional: Config.Events that has it gets the lockouts.
type Auditor interface {
	Audit(ctx context.Context, now time.Time, e store.AuditEntry) error
}

// gated reports whether the page and its calls ask for the password: the panel gave a key and the settings are on.
func (h *handler) gated(set *adminv1.SubscriptionSettings) bool {
	return len(h.cfg.PageKey) > 0 && subsettings.PagePassword(set)
}

// unlocked reports whether the request holds the cookie of the token (an HMAC over the token, so a rotated link
// invalidates it).
func (h *handler) unlocked(r *http.Request, token string) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && pagepass.CheckCookie(h.cfg.PageKey, token, c.Value)
}

// tryKeys are the counters an entry for token from client is counted against.
func tryKeys(token, client string) []struct{ scope, key string } {
	keys := []struct{ scope, key string }{{"token", "t:" + token}}
	if client != "" {
		keys = append(keys, struct{ scope, key string }{"client", "c:" + client})
	}
	return keys
}

func lockedAnswer(w http.ResponseWriter, retry time.Duration) {
	secs := int((retry + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "locked", "retry_after": secs})
}

func (h *handler) serveUnlock(w http.ResponseWriter, r *http.Request, token, client string, now time.Time) {
	ctx := r.Context()
	retryOnFailure := h.cfg.UnlockWindow
	if retryOnFailure <= 0 {
		retryOnFailure = time.Minute
	}
	if h.cfg.UnlockTries > 0 { // a counter that already ran out answers before any database work
		for _, k := range tryKeys(token, client) {
			decision, err := h.cfg.Limiter.CheckWindow(ctx, "page-password", k.key, now,
				h.cfg.UnlockTries, h.cfg.UnlockWindow, h.cfg.MaxKeys, true)
			if err != nil {
				lockedAnswer(w, retryOnFailure)
				return
			}
			if !decision.Allowed && !decision.First {
				lockedAnswer(w, decision.RetryAfter)
				return
			}
		}
	}
	v, _, err := h.identify(ctx, token, false)
	if errors.Is(err, access.ErrUnknownToken) {
		h.tokens.Delete(token)
		h.miss(ctx, client, now)
		h.decoy.ServeHTTP(w, r)
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if err := h.cop.Check(r); err != nil {
		jsonError(w, http.StatusForbidden, "cross_origin", "")
		return
	}
	if !h.gated(h.settings(ctx)) { // nothing to unlock
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	left := h.cfg.UnlockTries
	if left > 0 {
		for _, k := range tryKeys(token, client) {
			decision, err := h.cfg.Limiter.RecordWindow(ctx, "page-password", k.key, now,
				h.cfg.UnlockTries, h.cfg.UnlockWindow, h.cfg.MaxKeys)
			if err != nil {
				lockedAnswer(w, retryOnFailure)
				return
			}
			if decision.First {
				h.lockout(v.UserName, k.scope, now)
			}
			if !decision.Allowed {
				lockedAnswer(w, decision.RetryAfter)
				return
			}
			left = min(left, decision.Remaining)
		}
	}
	var in struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Password) > 64 || !pagepass.Check(h.cfg.PageKey, token, in.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "wrong_password", "left": left})
		return
	}
	if err := h.cfg.Limiter.Reset(ctx, "page-password", "t:"+token); err != nil {
		lockedAnswer(w, retryOnFailure)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: pagepass.Cookie(h.cfg.PageKey, token), Path: h.linkPath(r, token), MaxAge: cookieMaxAge,
		// Secure unless the instance's public URL is plain http: a browser drops a Secure cookie from an http page.
		HttpOnly: true, Secure: !strings.HasPrefix(h.cfg.BaseURL, "http://"),
		// Lax, not Strict: a link opened from a messenger is a cross-site navigation and must still carry the cookie.
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// linkPath is the path of the user's link, what the cookie is scoped to.
func (h *handler) linkPath(r *http.Request, token string) string {
	if u, err := url.Parse(h.link(r, token)); err == nil && u.Path != "" {
		return u.Path
	}
	return "/"
}

// lockout writes the audit row of a lockout: who (the user's name, which the owner chose), which counter ran out, never
// the token or an address.
func (h *handler) lockout(user, scope string, now time.Time) {
	a, ok := h.cfg.Events.(Auditor)
	if !ok {
		h.cfg.Log.Warn("user page: too many password entries", "scope", scope)
		return
	}
	params, _ := json.Marshal(map[string]string{"user": user, "scope": scope})
	e := store.AuditEntry{Actor: "anonymous", Action: "page_unlock_lockout", Result: "locked", Params: string(params)}
	go func() { // off the request, like the other events of the public handler
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Audit(ctx, now, e); err != nil {
			h.cfg.Log.Warn("audit page unlock lockout", "err", err)
		}
	}()
}
