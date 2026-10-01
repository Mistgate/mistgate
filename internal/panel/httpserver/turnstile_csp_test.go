package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The admin CSP lets Cloudflare's script and frame in only while Turnstile is on; with it off nothing
// Cloudflare-related is allowed (and so nothing is loaded).
func TestAdminCSPAllowsCloudflareOnlyWhileTurnstileIsOn(t *testing.T) {
	e := newTestEnv(t)
	csp := func(path string) string {
		t.Helper()
		r := do(t, http.MethodGet, e.public.URL, testPrefix+path, nil)
		return r.header.Get("Content-Security-Policy")
	}
	set := func(on string) {
		t.Helper()
		if err := e.st.SetSettings(context.Background(), map[string]string{"turnstile_enabled": on}); err != nil {
			t.Fatal(err)
		}
	}

	for _, p := range []string{"", "login", "setup", "settings/interface", "settings/security/x", "api/settings/security"} {
		if c := csp(p); strings.Contains(c, "cloudflare") || strings.Contains(c, "frame-src") {
			t.Errorf("/%s with Turnstile off: %s", p, c)
		}
	}
	// The one page where the owner tries new keys before switching the check on lets the widget in while it is off.
	if c := csp("settings/security"); !strings.Contains(c, "script-src 'self' https://challenges.cloudflare.com;") || !strings.Contains(c, "frame-src 'self' https://challenges.cloudflare.com;") ||
		strings.Count(c, "cloudflare") != 2 || !strings.Contains(c, "connect-src 'self';") || !strings.Contains(c, "frame-ancestors 'none'") {
		t.Errorf("/settings/security with Turnstile off: %s", c)
	}

	set("1")
	for _, p := range []string{"", "login", "setup"} {
		c := csp(p)
		if !strings.Contains(c, "script-src 'self' https://challenges.cloudflare.com;") || !strings.Contains(c, "frame-src 'self' https://challenges.cloudflare.com;") {
			t.Errorf("/%s with Turnstile on: %s", p, c)
		}
		// nothing else loosens: one origin, no wildcard, no inline script, the rest as before
		if strings.Contains(c, "*") || strings.Contains(c, "unsafe-eval") || strings.Count(c, "cloudflare") != 2 ||
			!strings.Contains(c, "connect-src 'self';") || !strings.Contains(c, "frame-ancestors 'none'") {
			t.Errorf("/%s loosened too much: %s", p, c)
		}
	}
	if c := csp("assets/app-abc123.js"); strings.Contains(c, "cloudflare") {
		t.Errorf("a hashed asset does not need the exception: %s", c)
	}

	set("0") // the CLI kill switch, or the owner
	if c := csp("login"); strings.Contains(c, "cloudflare") {
		t.Errorf("after switching off: %s", c)
	}
}
