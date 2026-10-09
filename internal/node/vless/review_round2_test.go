package vless

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestDisabledXHTTPRefusesNewSessionsOnExistingClient(t *testing.T) {
	f := newRuntimeFixture(t, false)
	egressCounter := &countingEgress{}
	f.engine.out = func(string) (engine.Egress, error) {
		return egressCounter, nil
	}
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("before disable")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	disabled := spec
	disabled.Enabled = false
	if _, err := f.engine.Apply(context.Background(), disabled, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	newConn, err := client.tryDial(echo.Addr().String())
	if err == nil {
		defer newConn.Close()
		_ = newConn.SetDeadline(time.Now().Add(time.Second))
		if _, err := newConn.Write([]byte("must be refused")); err != nil {
			t.Logf("disabled session write failed: %v", err)
		} else {
			got := make([]byte, len("must be refused"))
			if _, err := io.ReadFull(newConn, got); err == nil {
				t.Fatalf("disabled XHTTP inbound proxied a new session: %q", got)
			}
		}
	}
	if got := egressCounter.tcpDials.Load(); got != 1 {
		t.Fatalf("egress TCP dials after disable = %d, want only the pre-disable dial", got)
	}
}

func TestXHTTPRestartUsesNewEgressAndRecoversExistingClient(t *testing.T) {
	f := newRuntimeFixture(t, false)
	direct := &countingEgress{}
	warp := &countingEgress{}
	f.engine.out = func(name string) (engine.Egress, error) {
		if name == "warp" {
			return warp, nil
		}
		return direct, nil
	}
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	before := client.dial(t, echo.Addr().String())
	if _, err := exchange(before, []byte("before restart")); err != nil {
		t.Fatal(err)
	}
	if got := direct.tcpDials.Load(); got != 1 {
		t.Fatalf("old egress dials = %d, want 1", got)
	}

	changed := spec
	changed.Egress = "warp"
	started := time.Now()
	report, err := f.engine.Apply(context.Background(), changed, []plugin.UserCred{cred})
	if err != nil || !report.Restarted {
		t.Fatalf("restart report = %+v, error = %v", report, err)
	}
	assertConnectionEnds(t, before)

	after := client.dial(t, echo.Addr().String())
	defer after.Close()
	_ = after.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := exchange(after, []byte("after restart")); err != nil {
		t.Fatalf("existing Xray client did not recover after restart: %v", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("client did not recover within 10 seconds of the restart")
	}
	if got := warp.tcpDials.Load(); got != 1 {
		t.Fatalf("new egress dials = %d, want 1", got)
	}
}

func TestExpiredVLESSCredentialCannotSendPayload(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			clock := &manualClock{now: time.Now().Truncate(time.Second)}
			f.engine.now = func() time.Time { clock.mu.Lock(); defer clock.mu.Unlock(); return clock.now }
			f.engine.after = clock.After
			spec := f.spec("node-a", transport)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			cred.ValidUntil = clock.now.Add(time.Hour)
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			var received atomic.Uint64
			serverClosed := make(chan struct{}, 1)
			destination := startCountingEchoServer(t, &received, serverClosed)
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, stringID(cred))
			conn := client.dial(t, destination.Addr().String())
			beforeExpiry := []byte("payload before expiry")
			if _, err := exchange(conn, beforeExpiry); err != nil {
				t.Fatal(err)
			}

			clock.mu.Lock()
			clock.now = cred.ValidUntil
			clock.mu.Unlock()
			clock.FireAll()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write([]byte("payload after expiry")); err != nil {
				t.Logf("post-expiry write was refused: %v", err)
			}
			response, err := io.ReadAll(conn)
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					t.Fatalf("expired session remained open: %v", err)
				}
			}
			if len(response) != 0 {
				t.Fatalf("expired credential received %d response bytes", len(response))
			}
			select {
			case <-serverClosed:
			case <-time.After(3 * time.Second):
				t.Fatal("destination connection did not close after expiry")
			}
			if got := received.Load(); got != uint64(len(beforeExpiry)) {
				t.Fatalf("destination received %d bytes, want only the %d bytes sent before expiry", got, len(beforeExpiry))
			}
		})
	}
}

func TestFlowMismatchLogsDoNotExposeCredentialOrClientAddress(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transport  string
		clientFlow string
	}{
		{name: "RAW without Vision", transport: "tcp", clientFlow: ""},
		{name: "XHTTP with Vision", transport: "xhttp", clientFlow: "xtls-rprx-vision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			spec := f.spec("node-a", tc.transport)
			uuid := "66ad4540-b58c-4ad2-9926-ea63445a9b57"
			cred := testCredential("cred-flow-mismatch", uuid)
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			client := newXrayClientWithFlow(t, spec.Listen.Port, tc.transport, cred.CredID, uuid, tc.clientFlow)
			conn, err := client.tryDial(echo.Addr().String())
			if err == nil {
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				_, _ = conn.Write([]byte("flow mismatch"))
				var response [1]byte
				_, _ = conn.Read(response[:])
				_ = conn.Close()
			}
			got := f.logs.String()
			for _, sensitive := range []string{uuid, cred.CredID, "127.0.0.1"} {
				if strings.Contains(got, sensitive) {
					t.Errorf("flow mismatch log exposed %q: %s", sensitive, got)
				}
			}
		})
	}
}

func TestAsymmetricVLESSTrafficCountersKeepClientDirections(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			spec := f.spec("node-a", transport)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			const uploadSize = 1 << 20
			const downloadSize = 3 << 20
			destination, readBytes := startAsymmetricServer(t, uploadSize, downloadSize)
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, stringID(cred))
			conn := client.dial(t, destination.Addr().String())
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			writeDone := make(chan error, 1)
			go func() {
				body := payload(uploadSize)
				for written := 0; written < len(body); {
					n, err := conn.Write(body[written:])
					written += n
					if err != nil {
						writeDone <- err
						return
					}
				}
				writeDone <- nil
			}()
			response, err := io.ReadAll(conn)
			if err != nil {
				t.Fatalf("read asymmetric response (%d bytes) to EOF: %v", len(response), err)
			}
			if len(response) != downloadSize {
				t.Fatalf("response bytes = %d, want %d", len(response), downloadSize)
			}
			if err := <-writeDone; err != nil {
				t.Fatalf("write asymmetric request: %v", err)
			}
			if got := <-readBytes; got != uploadSize {
				t.Fatalf("destination received %d upload bytes, want %d", got, uploadSize)
			}
			up, down, ended := collectTrafficUntilSessionEnds(t, f.engine, cred.CredID, 3*time.Second)
			if !ended {
				t.Fatal("Collect still listed the session after the destination closed")
			}
			if !withinPercent(up, uploadSize, 1) || !withinPercent(down, downloadSize, 1) {
				t.Fatalf("traffic counters swapped or out of tolerance: up=%d want=%d down=%d want=%d", up, uploadSize, down, downloadSize)
			}
		})
	}
}

func startAsymmetricServer(t *testing.T, uploadSize, downloadSize int) (net.Listener, <-chan int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	readBytes := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		got, err := io.CopyN(io.Discard, conn, int64(uploadSize))
		if err != nil {
			readBytes <- int(got)
			return
		}
		readBytes <- int(got)
		body := payload(downloadSize)
		for written := 0; written < len(body); {
			n, err := conn.Write(body[written:])
			written += n
			if err != nil {
				return
			}
		}
	}()
	return listener, readBytes
}

func collectTrafficUntilSessionEnds(t *testing.T, e *eng, credID string, timeout time.Duration) (up, down uint64, ended bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		collected, err := e.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, delta := range collected.Traffic {
			if delta.CredID == credID {
				up += delta.Up
				down += delta.Down
			}
		}
		if sessionsOf(collected, credID) == 0 {
			return up, down, true
		}
		select {
		case <-deadline.C:
			return up, down, false
		case <-tick.C:
		}
	}
}

func TestVLESSLinkIdleTimeoutClosesIdleSession(t *testing.T) {
	f := newRuntimeFixture(t, false)
	f.engine.idleTimeout = 200 * time.Millisecond
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("become idle")); err != nil {
		t.Fatal(err)
	}
	assertConnectionEndsWithin(t, conn, 2*time.Second)
}

// Two XHTTP sessions of one client (no mux: TestNaturalVLESSUDPLinkEndPreservesSibling covers that): one ending at its
// destination leaves the other working.
func TestNaturalVLESSLinkEndKeepsOtherXHTTPSession(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	reset, resetDone := startResetServer(t)
	client := newXrayClient(t, spec.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	sibling := client.dial(t, echo.Addr().String())
	defer sibling.Close()
	if _, err := exchange(sibling, []byte("sibling before reset")); err != nil {
		collected, collectErr := f.engine.Collect(context.Background())
		t.Fatalf("sibling exchange before reset: %v; collected=%+v collectErr=%v health=%+v logs=%s", err, collected, collectErr, f.engine.Health(), f.logs.String())
	}
	ended := client.dial(t, reset.Addr().String())
	if _, err := ended.Write([]byte("trigger reset")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resetDone:
	case <-time.After(3 * time.Second):
		t.Fatal("reset destination did not close")
	}
	assertConnectionEndsWithin(t, ended, 3*time.Second)
	if _, err := exchange(sibling, []byte("sibling after reset")); err != nil {
		t.Fatalf("natural end of one XHTTP session killed the other: %v", err)
	}
}

func startResetServer(t *testing.T) (net.Listener, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		var first [1]byte
		_, _ = io.ReadFull(conn, first[:])
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
		done <- struct{}{}
	}()
	return listener, done
}

func TestRemoveAndCloseEndVLESSSessionsForBothTransports(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		for _, operation := range []string{"remove", "close"} {
			t.Run(transport+"/"+operation, func(t *testing.T) {
				f := newRuntimeFixture(t, false)
				spec := f.spec("node-a", transport)
				cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
				if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
					t.Fatal(err)
				}
				echo := startEchoServer(t)
				client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, stringID(cred))
				conn := client.dial(t, echo.Addr().String())
				if _, err := exchange(conn, []byte("stop session")); err != nil {
					t.Fatal(err)
				}
				if operation == "remove" {
					if err := f.engine.Remove(context.Background(), spec.ID); err != nil {
						t.Fatal(err)
					}
				} else if err := f.engine.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				assertConnectionEnds(t, conn)
			})
		}
	}
}

func TestKickClosesStalledXHTTPClient(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	destination, accepted := startLargeResponseServer(t)
	client := newXrayClient(t, spec.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	conn := client.dial(t, destination.Addr().String())
	serverConn := <-accepted
	defer serverConn.Close()

	defer conn.Close()

	// The app never reads: the destination writes until every buffer on the way is full and the server-side XHTTP write
	// blocks on the client. Only then is the writer's lock held, which a Close fallback in the kick would wait for forever.
	var written atomic.Int64
	writeDone := make(chan error, 1)
	go func() {
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		const responseBytes = 128 << 20
		for written.Load() < responseBytes {
			n, err := serverConn.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()
	for last, still, deadline := int64(-1), time.Now(), time.Now().Add(15*time.Second); ; time.Sleep(50 * time.Millisecond) {
		if now := written.Load(); now != last {
			last, still = now, time.Now()
		} else if time.Since(still) >= 500*time.Millisecond {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the destination's writes never stalled: the client is reading after all")
		}
	}
	select {
	case err := <-writeDone:
		t.Fatalf("large response finished before kick: %v", err)
	default:
	}
	kicked := make(chan error, 1)
	go func() {
		n, err := f.engine.Kick(context.Background(), []string{cred.CredID})
		if err == nil && n != 1 {
			err = fmt.Errorf("kicked %d sessions, want 1", n)
		}
		kicked <- err
	}()
	select {
	case err := <-kicked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Kick did not return within 1 s: it waits for the blocked XHTTP writer")
	}
	if _, _, ended := collectTrafficUntilSessionEnds(t, f.engine, cred.CredID, 3*time.Second); !ended {
		t.Fatal("Collect still lists the kicked session")
	}
	// The client's own connection may stay open until it reads again or its TCP gives up: the server-side write is blocked on
	// it, and only an inbound stop closes the raw connection (design/vless.md, R2 notes).
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("destination completed its full response after the stalled client was kicked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("kick did not unblock the destination writer")
	}
}

func startLargeResponseServer(t *testing.T) (net.Listener, <-chan net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	return listener, accepted
}

func TestRetiredVLESSCredentialTrafficIsCollectedOnlyOnce(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "xhttp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("retire these bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Apply(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	assertConnectionEnds(t, conn)
	first, err := f.engine.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	up, down := trafficOf(first, cred.CredID)
	if up == 0 || down == 0 {
		t.Fatalf("retired credential traffic missing: up=%d down=%d", up, down)
	}
	second, err := f.engine.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotUp, gotDown := trafficOf(second, cred.CredID); gotUp != 0 || gotDown != 0 {
		t.Fatalf("retired traffic replayed: up=%d down=%d", gotUp, gotDown)
	}
}

func startCountingEchoServer(t *testing.T, received *atomic.Uint64, closed chan<- struct{}) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var buf [4096]byte
				for {
					n, err := conn.Read(buf[:])
					received.Add(uint64(n))
					for written := 0; written < n; {
						count, writeErr := conn.Write(buf[written:n])
						written += count
						if writeErr != nil {
							return
						}
					}
					if err != nil {
						closed <- struct{}{}
						return
					}
				}
			}()
		}
	}()
	return listener
}

type countingEgress struct {
	tcpDials atomic.Uint64
	udpDials atomic.Uint64
}

func (e *countingEgress) TCP(addr string) (net.Conn, error) {
	e.tcpDials.Add(1)
	return net.Dial("tcp", addr)
}

func (e *countingEgress) UDP(addr string) (engine.EgressUDP, error) {
	e.udpDials.Add(1)
	return localEgress{}.UDP(addr)
}

func (e *countingEgress) CheckUDP(addr string) error { return localEgress{}.CheckUDP(addr) }

func TestREALITYProbeClassifiesLocalTargets(t *testing.T) {
	refusedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := refusedListener.Addr().String()
	_ = refusedListener.Close()

	hangingListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hangingListener.Close() })
	go func() {
		for {
			conn, err := hangingListener.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, conn); _ = conn.Close() }()
		}
	}()

	tls12 := startProbeTLSServer(t, tls.VersionTLS12, tls.VersionTLS12, []tls.CurveID{tls.X25519})
	x25519 := startProbeTLSServer(t, tls.VersionTLS13, tls.VersionTLS13, []tls.CurveID{tls.X25519})
	mlkem := startProbeTLSServer(t, tls.VersionTLS13, tls.VersionTLS13, []tls.CurveID{tls.X25519MLKEM768})
	for _, tc := range []struct {
		name    string
		target  string
		timeout time.Duration
		want    string
	}{
		{name: "refused", target: refused, timeout: time.Second, want: "unreachable"},
		{name: "hanging listener", target: hangingListener.Addr().String(), timeout: 100 * time.Millisecond, want: "unreachable"},
		{name: "TLS 1.2 only", target: tls12, timeout: 3 * time.Second, want: "no_tls13"},
		{name: "X25519 only", target: x25519, timeout: 3 * time.Second, want: "no_mlkem"},
		{name: "MLKEM", target: mlkem, timeout: 3 * time.Second, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if got := probeRealityTarget(ctx, tc.target, "localhost"); got != tc.want {
				t.Fatalf("probeRealityTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}

func startProbeTLSServer(t *testing.T, minVersion, maxVersion uint16, curves []tls.CurveID) string {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.TLS = &tls.Config{MinVersion: minVersion, MaxVersion: maxVersion, CurvePreferences: curves}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "https://")
}

func TestTorrentGuardToggleDoesNotRestartVLESS(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	before := f.engine.inbounds[spec.ID]
	if reset := f.engine.NodeSettings(context.Background(), &agentpb.NodeSettings{TorrentBlockerEnabled: true}); reset {
		t.Fatal("torrent guard toggle requested an inbound restart")
	}
	report, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred})
	if err != nil {
		t.Fatal(err)
	}
	if report.Restarted || f.engine.inbounds[spec.ID] != before {
		t.Fatalf("torrent guard toggle restarted VLESS: report=%+v", report)
	}
}

var _ engine.Egress = (*countingEgress)(nil)
