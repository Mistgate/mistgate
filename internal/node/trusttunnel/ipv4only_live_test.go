package trusttunnel

import (
	"net/http"
	"testing"

	"github.com/mistgate/mistgate/internal/node/egress"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The live scenario, as close as a test gets: the engine's real egress (private addresses allowed only so that the destination can
// be a local listener) with IPv6 off for clients, one HTTP/2 connection, _check, then two CONNECTs to an IPv4 literal and port, and an
// IPv6 literal. The IPv4 ones carry data; the IPv6 one is the engine's answer for a node without IPv6 (502, 301), counted on its own.
func TestRealEgressOnIPv4OnlyNodeServesTwoConnectsAfterCheckAndRefusesIPv6(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	direct := egress.New(nil, egress.AllowPrivate(), egress.IPv4OnlyWhen(func() bool { return true }))
	h := newHarnessEnv(t, []plugin.UserCred{alice.cred}, nil, timings{}, func(env *nodeengine.Env) {
		env.Egress = func(string) (nodeengine.Egress, error) { return direct, nil }
	})
	client := newHTTP2Client(t, h.addr)

	check, _ := client.connect(t, "_check", authValue(t, alice), false)
	_ = check.Body.Close()
	if check.StatusCode != http.StatusOK {
		t.Fatalf("_check = %d", check.StatusCode)
	}
	target := backend.listener.Addr().String() // 127.0.0.1:port
	for i := range 2 {
		response, writer := client.connect(t, target, authValue(t, alice), true)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT %d to %s = %d %q", i+1, target, response.StatusCode, response.Header.Get("X-Warning"))
		}
		exchange(t, writer, response.Body, "ping")
		_ = writer.Close()
		_ = response.Body.Close()
	}
	v6, _ := client.connect(t, "[2001:db8::1]:443", authValue(t, alice), false)
	_ = v6.Body.Close()
	if v6.StatusCode != http.StatusBadGateway || v6.Header.Get("X-Warning") != "301 - Destination is unavailable" {
		t.Fatalf("IPv6 CONNECT on an IPv4-only node = %d %q", v6.StatusCode, v6.Header.Get("X-Warning"))
	}
	only(t, inboundRefusals(t, collect(t, h.e), "de1"), plugin.RefusedNoIPv4, 1)
	// after the refusal the same connection still carries a tunnel
	response, writer := client.connect(t, target, authValue(t, alice), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT after the refusal = %d", response.StatusCode)
	}
	_ = writer.Close()
	_ = response.Body.Close()
}
