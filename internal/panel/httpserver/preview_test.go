package httpserver

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// roleSession signs in an admin of the given role by writing the session row, and returns the cookie.
func roleSession(t *testing.T, st *store.Store, role string) string {
	t.Helper()
	now := time.Now()
	id := "adm_" + role
	if _, err := st.W.ExecContext(context.Background(), `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, role, role, []byte(role), now.Unix()); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(role + "-token"))
	if err := st.CreateSession(context.Background(), store.Session{TokenHash: h[:], AdminID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return auth.CookieName + "=" + role + "-token"
}

// apiToken stores a live API token of the profile and returns its secret.
func apiToken(t *testing.T, st *store.Store, profile string) string {
	t.Helper()
	secret := auth.NewTokenSecret()
	sum := sha256.Sum256([]byte(secret))
	now := time.Now()
	if err := st.CreateAPIToken(context.Background(), store.APIToken{ID: store.NewID("tok_"), Name: "t-" + profile, Profile: profile,
		Hint: secret[len(secret)-4:], RatePerMin: 600, CreatedBy: "adm_owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, sum[:]); err != nil {
		t.Fatal(err)
	}
	return secret
}

// The preview of the user page shows the user's real subscription link (and opening it may issue the user's missing
// credentials), so it is open to whoever may fetch that link (GetSubscriptionLink: helper or owner), to nobody else:
// not a readonly admin, not an API token of any profile. The function behind it only ever sees a validated id.
func TestUserPagePreviewNeedsTheRightToTheLink(t *testing.T) {
	var calls atomic.Int32
	var lastID atomic.Value
	e := newTestEnv(t, func(c *Config) {
		c.UserPagePreview = func(w http.ResponseWriter, r *http.Request, id string) {
			calls.Add(1)
			lastID.Store(id)
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'self'")
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<html>page of " + id + "</html>"))
		}
	})
	owner, readonly, helper := roleSession(t, e.st, store.RoleOwner), roleSession(t, e.st, store.RoleReadonly), roleSession(t, e.st, store.RoleHelper)
	tokens := map[string]string{}
	for _, p := range []string{store.ProfileReadonly, store.ProfileOperator, store.ProfileAdmin} {
		tokens[p] = apiToken(t, e.st, p)
	}
	const path = "preview/user-page/usr_abc123"

	for name, base := range map[string]string{
		"admin listener": e.admin.URL + "/",
		"secret prefix":  e.public.URL + testPrefix,
	} {
		before := calls.Load()
		anon := do(t, "GET", base, path, nil)
		if anon.status != http.StatusUnauthorized || strings.Contains(anon.body, "page of") {
			t.Errorf("%s, no session: %d %q", name, anon.status, anon.body)
		}
		if bad := do(t, "GET", base, path, map[string]string{"Cookie": auth.CookieName + "=forged"}); bad.status != http.StatusUnauthorized {
			t.Errorf("%s, forged session: %d", name, bad.status)
		}
		if r := do(t, "GET", base, path, map[string]string{"Cookie": readonly}); r.status != http.StatusForbidden || strings.Contains(r.body, "page of") {
			t.Errorf("%s, readonly admin: %d %q", name, r.status, r.body)
		}
		for p, secret := range tokens {
			if r := do(t, "GET", base, path, map[string]string{"Authorization": "Bearer " + secret}); r.status != http.StatusForbidden || strings.Contains(r.body, "page of") {
				t.Errorf("%s, %s token: %d %q", name, p, r.status, r.body)
			}
		}
		if calls.Load() != before {
			t.Errorf("%s: the preview function ran for a caller who may not see the link", name)
		}
		for role, cookie := range map[string]string{"owner": owner, "helper": helper} {
			r := do(t, "GET", base, path, map[string]string{"Cookie": cookie})
			if r.status != 200 || r.body != "<html>page of usr_abc123</html>" {
				t.Errorf("%s as %s: %d %q", name, role, r.status, r.body)
			}
			if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self'") {
				t.Errorf("%s as %s: csp %q", name, role, csp)
			}
			if r.header.Get("X-Frame-Options") != "SAMEORIGIN" {
				t.Errorf("%s as %s: X-Frame-Options %q (the admin's DENY must not reach the frame)", name, role, r.header.Get("X-Frame-Options"))
			}
		}
	}

	// Only well-formed ids reach the function.
	before := calls.Load()
	for _, bad := range []string{"preview/user-page/", "preview/user-page/USR_ABC", "preview/user-page/usr_a%2Fb", "preview/user-page/usr abc", "preview/user-page/" + strings.Repeat("a", 65)} {
		if r := do(t, "GET", e.admin.URL+"/", bad, map[string]string{"Cookie": helper}); r.status != 404 && r.status != 200 || strings.Contains(r.body, "page of") {
			t.Errorf("%q: %d %q", bad, r.status, r.body)
		}
	}
	if calls.Load() != before {
		t.Errorf("the preview function saw a malformed id: %v", lastID.Load())
	}
}

// Without a preview function the route does not exist (the admin shell answers, as for any client route).
func TestUserPagePreviewAbsentWhenNotConfigured(t *testing.T) {
	e := newTestEnv(t)
	r := do(t, "GET", e.admin.URL+"/", "preview/user-page/usr_abc123", map[string]string{"Cookie": roleSession(t, e.st, store.RoleHelper)})
	if strings.Contains(r.header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("unexpected preview answer: %d %v", r.status, r.header)
	}
}
