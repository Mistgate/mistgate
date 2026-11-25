package vless

import (
	"context"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

var xraySessionLine = regexp.MustCompile(`\[\d+\] proxy/vless/inbound: received request for`)

// A live engine under a debug capture: a connection that is accepted and one that is refused for an unknown key leave their
// lines (with the session number), and nothing in them names the client, the key, the account or the destination host.
func TestLiveEngineCaptureShowsAcceptedAndRefusedConnections(t *testing.T) {
	f := newRuntimeFixture(t, false)
	on := new(atomic.Bool)
	out := gatedXrayLog(t, on) // after the fixture's engine installed its own logger
	t.Cleanup(func() { installXrayLogging(f.engine.log) })

	spec := f.spec("node-a", "tcp")
	const knownKey, unknownKey = "66ad4540-b58c-4ad2-9926-ea63445a9b57", "7B2C9D10-0000-4000-8000-ABCDEF012345"
	cred := testCredential("alice", knownKey)
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	_, port, _ := net.SplitHostPort(echo.Addr().String())

	// outside a capture: nothing of the connection lines
	client := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, knownKey)
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, payload(1024)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := out.String(); strings.Contains(got, "received request for") || strings.Contains(got, "[Access]") {
		t.Fatalf("connection lines outside a capture:\n%s", got)
	}

	on.Store(true)
	conn = client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, payload(1024)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	stranger := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, unknownKey)
	if c, err := stranger.tryDial(echo.Addr().String()); err == nil { // the local SOCKS side may answer before the node refuses
		_, _ = c.Write([]byte("never answered"))
		_ = c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "invalid request user id") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	on.Store(false)

	got := out.String()
	for _, want := range []string{
		"received request for tcp:<ip>:" + port, // the accepted connection, with its session number: [<digits>] in front
		"[Access] accepted tcp:<ip>:" + port,
		"invalid request user id: [redacted]", // the refusal
	} {
		if !strings.Contains(got, want) {
			t.Errorf("capture lacks %q:\n%s", want, got)
		}
	}
	if !xraySessionLine.MatchString(got) {
		t.Errorf("no line carries a session number:\n%s", got)
	}
	for _, secret := range []string{"127.0.0.1", "alice", knownKey, unknownKey} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(secret)) {
			t.Errorf("%q reached the capture:\n%s", secret, got)
		}
	}
}
