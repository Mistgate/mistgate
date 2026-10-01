package httpserver

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/auth"
)

// Handlers mounted on the public listener (subscriptions) and the agent endpoint get the
// client address through auth.ClientIPFrom, resolved by the same trusted-proxy rules.
func TestClientIPReachesPublicMounts(t *testing.T) {
	report := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, auth.ClientIPFrom(r.Context()))
	})
	e := newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() }, func(c *Config) {
		c.PublicMounts = map[string]http.Handler{subPrefix: report}
	})
	for _, tc := range []struct{ hdr, want string }{
		{"", "127.0.0.1"},
		{"203.0.113.77", "203.0.113.77"},
		{"9.9.9.9, 2001:db8::5", "2001:db8::5"},
	} {
		hdr := map[string]string{}
		if tc.hdr != "" {
			hdr["X-Forwarded-For"] = tc.hdr
		}
		if r := do(t, http.MethodGet, e.public.URL, subPrefix+"x", hdr); r.body != tc.want {
			t.Errorf("X-Forwarded-For %q: %q, want %q", tc.hdr, r.body, tc.want)
		}
	}
	// Without a trusted proxy the header is not believed.
	e = newTestEnv(t, func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: report} })
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+"x", map[string]string{"X-Forwarded-For": "203.0.113.77"}); r.body != "127.0.0.1" {
		t.Errorf("untrusted header believed: %q", r.body)
	}
}
