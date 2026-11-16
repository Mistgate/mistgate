package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Requests the subscription host's tunnel carries never reach the admin, by prefix or by host; the same requests on the
// public listener do.
func TestTunnelNeverReachesTheAdmin(t *testing.T) {
	e := newTestEnv(t)
	h := e.srv.Public()
	for _, c := range []struct{ host, path string }{{"example.com", testPrefix}, {testAdminHst, "/"}} {
		serve := func(tunnel bool) int {
			r := httptest.NewRequest(http.MethodGet, "http://"+c.host+c.path, nil)
			if tunnel {
				r = r.WithContext(context.WithValue(r.Context(), tunnelConnKey{}, true))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w.Code
		}
		if got := serve(false); got != http.StatusOK {
			t.Fatalf("%s%s on the public listener: %d", c.host, c.path, got)
		}
		if got := serve(true); got != http.StatusNotFound {
			t.Errorf("%s%s through the tunnel: %d, want the decoy's 404", c.host, c.path, got)
		}
	}
}
