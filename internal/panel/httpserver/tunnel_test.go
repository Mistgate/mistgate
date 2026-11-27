package httpserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A connection a subscription host carries gets the public surface only: the admin by prefix and by host are the decoy there,
// as for any path the panel does not serve.
func TestTunnelConnectionsNeverReachTheAdmin(t *testing.T) {
	e := newTestEnv(t)
	tunnel := httptest.NewUnstartedServer(e.srv.Public())
	tunnel.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, tunnelConnKey{}, true)
	}
	tunnel.Start()
	t.Cleanup(tunnel.Close)

	if got := do(t, http.MethodGet, e.public.URL, testPrefix, nil); got.status != http.StatusOK {
		t.Fatalf("control: the admin by prefix on the public listener answers %d", got.status)
	}
	decoy := do(t, http.MethodGet, tunnel.URL, "/nothing", nil)
	sameResponse(t, "admin by prefix through a host", decoy, do(t, http.MethodGet, tunnel.URL, testPrefix, nil))
	sameResponse(t, "admin by host through a host", decoy, do(t, http.MethodGet, tunnel.URL, "/nothing", map[string]string{"Host": testAdminHst}))
}
