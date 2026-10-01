package fleet

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// End to end: the public listener resolves the client address (here through a trusted proxy header) and the
// Enroll limiter counts failures per IPv4 address / IPv6 /64 of THAT address, not of the TCP peer.
func TestEnrollLimiterCountsTheResolvedClientNetwork(t *testing.T) {
	e := newEnv(t)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	proxies, _ := auth.ParseProxies([]string{"127.0.0.0/8"})
	as, err := auth.New(st, auth.Config{RPID: "localhost", Origins: []string{"http://localhost"}, TrustedProxies: proxies}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The public listener's job: put the resolved client address into the context of every request.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.f.AgentHandler().ServeHTTP(w, as.WithClientIP(r))
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{GetConfigForClient: e.f.AgentTLSConfig}
	srv.StartTLS()
	defer srv.Close()

	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), srv.URL)
	_, csr := newCSR(t)
	enroll := func(from string) error {
		r := connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: "guess", CsrDer: csr, ApiVersion: APIVersion})
		r.Header().Set("X-Forwarded-For", from)
		_, err := cli.Enroll(context.Background(), r)
		return err
	}

	// Ten failures from ten addresses of one /64 (all arriving from the same TCP peer, the proxy).
	for i := 0; i < 10; i++ {
		if err := enroll(fmt.Sprintf("2001:db8:7:7:%x::5", i+1)); code(err) != connect.CodeUnauthenticated {
			t.Fatalf("failure %d: %v", i, err)
		}
	}
	if err := enroll("2001:db8:7:7:ffff::1"); code(err) != connect.CodeResourceExhausted {
		t.Errorf("an 11th address of the same /64: %v, want RESOURCE_EXHAUSTED", err)
	}
	// Other clients behind the same proxy are not punished for it.
	if err := enroll("2001:db8:7:8::1"); code(err) != connect.CodeUnauthenticated {
		t.Errorf("a neighbouring /64: %v", err)
	}
	if err := enroll("203.0.113.77"); code(err) != connect.CodeUnauthenticated {
		t.Errorf("an IPv4 client: %v", err)
	}
	// An IPv4 address is one source.
	for i := 0; i < 10; i++ {
		enroll("198.51.100.9")
	}
	if err := enroll("198.51.100.9"); code(err) != connect.CodeResourceExhausted {
		t.Errorf("a repeat offender over IPv4: %v", err)
	}
	if err := enroll("198.51.100.10"); code(err) != connect.CodeUnauthenticated {
		t.Errorf("the next IPv4 address: %v", err)
	}
}
