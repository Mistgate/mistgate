package trusttunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"golang.org/x/net/http2"
)

// shortTimings makes every timeout of the site and the tunnel tiny except the tunnel's own idle time, so that a tunnel that
// outlives them all has outlived each of them many times over.
func shortTimings() timings {
	ts := defaultTimings
	short := 20 * time.Millisecond
	ts.read, ts.write, ts.readHeader, ts.serverIdle, ts.connectionIdle = short, short, short, short, short
	ts.streamIdle = 30 * time.Second
	return ts
}

// A tunnel that is silent for many times the site's Read/Write/Idle timeouts, in both directions, stays up.
func TestQuietTunnelOutlivesEveryTimeout(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	ts := shortTimings()
	backend := startTCPBackend(t, func(conn net.Conn) {
		time.Sleep(15 * ts.write) // silent towards the client for a long time, then talks
		_, _ = io.WriteString(conn, "late")
		_, _ = io.Copy(conn, conn)
	})
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)

	time.Sleep(25 * ts.write) // the client is silent too
	late := make([]byte, 4)
	if _, err := io.ReadFull(response.Body, late); err != nil || string(late) != "late" {
		t.Fatalf("the upstream's late data = %q, %v", late, err)
	}
	exchange(t, writer, response.Body, "still here")
	_ = writer.Close()
	if !endsWithin(response.Body, 2*time.Second) {
		t.Fatal("the tunnel did not end after the client's half-close")
	}
}

// The same on a connection that carries only a quiet tunnel (no other stream keeps it busy), through the connection idle
// time and the server's IdleTimeout together.
func TestQuietConnectionWithOneTunnelStaysUpThroughBothIdleTimeouts(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	ts := shortTimings()
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	for range 5 {
		time.Sleep(10 * ts.serverIdle)
		exchange(t, writer, response.Body, "ping")
	}
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("connections = %d, want the one carrying the tunnel", count)
	}
	_ = writer.Close()
}

// Up is the client's upload, Down its download, and every byte is counted once; the sizes differ so a swap shows.
func TestUpAndDownAreNotSwappedAndEveryByteIsCountedOnce(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	const upload, download = 1000, 3000
	backend := startTCPBackend(t, func(conn net.Conn) {
		_, _ = io.ReadFull(conn, make([]byte, upload))
		_, _ = conn.Write(make([]byte, download))
	})
	ts := defaultTimings
	ts.eofGrace = 20 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	if _, err := writer.Write(make([]byte, upload)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil || len(got) != download {
		t.Fatalf("download = %d bytes, %v, want %d", len(got), err, download)
	}
	_ = writer.Close()
	stats := collect(t, h.e)
	if up, down := trafficFor(stats, "alice"); up != upload || down != download {
		t.Fatalf("traffic up/down = %d/%d, want %d/%d", up, down, upload, download)
	}
	if next := collect(t, h.e); len(next.Traffic) != 0 {
		t.Fatalf("a second Collect repeated %+v", next.Traffic)
	}
}

func measureRate(t *testing.T, name string, upload bool) {
	t.Helper()
	const payload = 600_000
	alice := makeCredential(t, "alice", "alice", "person-alice", 2_000_000, 0)
	var backend *tcpBackend
	if upload {
		backend = sinkBackend(t)
	} else {
		backend = startTCPBackend(t, func(conn net.Conn) { _, _ = conn.Write(make([]byte, payload)) })
	}
	ts := defaultTimings
	ts.eofGrace = 20 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	start := time.Now()
	if upload {
		if _, err := writer.Write(make([]byte, payload)); err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
	}
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start).Seconds()
	if !upload && len(got) != payload {
		t.Fatalf("%s: download = %d bytes, want %d", name, len(got), payload)
	}
	_ = writer.Close()
	const burstBytes = 250_000 // one second of the limit
	measured := float64(payload-burstBytes) * 8 / elapsed
	if measured < 1_800_000 || measured > 2_200_000 {
		t.Fatalf("%s: rate = %.0f bit/s over %.2fs, want 2,000,000 +/-10%%", name, measured, elapsed)
	}
}

// The device limit holds in both directions, measured on payload.
func TestDeviceLimitAppliesToUploadAndDownload(t *testing.T) {
	t.Run("upload", func(t *testing.T) { t.Parallel(); measureRate(t, "upload", true) })
	t.Run("download", func(t *testing.T) { t.Parallel(); measureRate(t, "download", false) })
}

func downCounter(h *testHarness, credID string) *credState { return h.in.idx.Load().byID[credID] }

// A client that stops reading must not pin its stream: a write stuck on its closed HTTP/2 window is cut by Kick.
func TestKickFreesAStreamStuckOnAClientThatStoppedReading(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := startTCPBackend(t, func(conn net.Conn) {
		chunk := make([]byte, 32<<10)
		for {
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	})
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)
	response, _ := client.connect(t, "example.com:80", authValue(t, alice), true)
	_ = response // never read: the client's window closes
	cs := downCounter(h, "alice")
	waitStuck(t, cs)
	if affected, _ := h.e.Kick(context.Background(), []string{"alice"}); affected != 1 {
		t.Fatalf("Kick affected %d, want 1", affected)
	}
	waitFor(t, time.Second, func() bool { return backendConnections(backend) == 0 })
	waitFor(t, time.Second, func() bool { cs.mu.Lock(); defer cs.mu.Unlock(); return cs.active == 0 })
}

// And without a Kick the idle timer frees it: the soft end first, the reset when the soft end cannot get through.
func TestIdleTimerFreesAStreamStuckOnAClientThatStoppedReading(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := startTCPBackend(t, func(conn net.Conn) {
		chunk := make([]byte, 32<<10)
		for {
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	})
	ts := defaultTimings
	ts.streamIdle, ts.eofGrace = 200*time.Millisecond, 100*time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, _ := client.connect(t, "example.com:80", authValue(t, alice), true)
	_ = response
	cs := downCounter(h, "alice")
	waitFor(t, 3*time.Second, func() bool { cs.mu.Lock(); defer cs.mu.Unlock(); return cs.active == 0 && cs.down.Load() > 0 })
	waitFor(t, time.Second, func() bool { return backendConnections(backend) == 0 })
}

func backendConnections(backend *tcpBackend) int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return len(backend.conns)
}

// waitStuck returns once the credential's download counter stopped growing: the server is blocked on the client's window.
func waitStuck(t *testing.T, cs *credState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last, steady := uint64(0), 0
	for time.Now().Before(deadline) {
		now := cs.down.Load()
		if now > 0 && now == last {
			steady++
			if steady >= 4 {
				return
			}
		} else {
			steady = 0
		}
		last = now
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the stream never got stuck on the client's window")
}

// An upload the upstream does not take is held back by flow control: the server does not buffer it without bound.
func TestUnreadUploadIsBoundedByFlowControl(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	hold := make(chan struct{})
	backend := startTCPBackend(t, func(conn net.Conn) { <-hold })
	t.Cleanup(func() { close(hold) })
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	var accepted atomic.Int64
	go func() {
		chunk := make([]byte, 64<<10)
		for {
			n, err := writer.Write(chunk)
			accepted.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	stable, last := 0, int64(-1)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && stable < 6; time.Sleep(100 * time.Millisecond) {
		if now := accepted.Load(); now == last && now > 0 {
			stable++
		} else {
			stable = 0
		}
		last = accepted.Load()
	}
	if stable < 6 {
		t.Fatalf("the client was never held back: %d bytes accepted and still going", accepted.Load())
	}
	// The stream window is 1 MiB; the upstream socket and the transport's own buffers add a few more.
	if got := accepted.Load(); got > 12<<20 {
		t.Fatalf("the server took %d bytes of a stalled upload, want a few MiB at most", got)
	}
	_ = writer.CloseWithError(errors.New("done"))
	_ = response.Body.Close()
}

// The upstream's EOF cannot half-close the response, so the stream ends after the grace instead of hanging for the idle time.
func TestUpstreamCloseEndsTheResponseAfterTheGrace(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := startTCPBackend(t, func(conn net.Conn) { _, _ = io.WriteString(conn, "bye") })
	ts := defaultTimings
	ts.eofGrace = 100 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	start := time.Now()
	got, err := io.ReadAll(response.Body)
	if err != nil || string(got) != "bye" {
		t.Fatalf("response = %q, %v", got, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the response ended after %s, want about the grace", elapsed)
	}
	_ = writer.Close()
}

// While the client keeps sending after the upstream half-closed, the grace keeps being renewed and nothing is lost.
func TestClientUploadContinuesAfterUpstreamHalfClose(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	received := make(chan string, 1)
	backend := startTCPBackend(t, func(conn net.Conn) {
		_, _ = io.WriteString(conn, "hello")
		if half, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
		all, _ := io.ReadAll(conn)
		received <- string(all)
	})
	ts := defaultTimings
	ts.eofGrace = 300 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	want := ""
	for i := range 8 {
		part := "chunk" + strconv.Itoa(i)
		want += part
		if _, err := io.WriteString(writer, part); err != nil {
			t.Fatalf("upload stopped at %s: %v", part, err)
		}
		time.Sleep(100 * time.Millisecond) // longer in total than the grace
	}
	_ = writer.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	select {
	case got := <-received:
		if got != want {
			t.Fatalf("upstream received %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never saw the end of the upload")
	}
}

func TestDialErrorsMapToTheClientsWarnings(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	errs := map[string]error{
		"blocked.example:80": egress.ErrBlocked,
		"nov4.example:80":    egress.ErrNoIPv4,
		"refused.example:80": &net.OpError{Op: "dial", Err: errors.New("connection refused")},
		"dns.example:80":     &net.DNSError{Err: "no such host", Name: "dns.example"},
		"other.example:80":   errors.New("something else"),
	}
	outbound := &testEgress{tcpDial: func(target string) (net.Conn, error) { return nil, errs[target] }}
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP2Client(t, h.addr)
	want := map[string]string{
		"blocked.example:80": "310 - Destination address is blocked",
		"nov4.example:80":    "301 - Destination is unavailable",
		"refused.example:80": "301 - Destination is unavailable",
		"dns.example:80":     "301 - Destination is unavailable",
		"other.example:80":   "300 - Connection failed for some reason",
	}
	for target, warning := range want {
		response, _ := client.connect(t, target, authValue(t, alice), false)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadGateway || response.Header.Get("X-Warning") != warning {
			t.Fatalf("%s = %d %q, want 502 %q", target, response.StatusCode, response.Header.Get("X-Warning"), warning)
		}
	}
}

type testClock struct{ now atomic.Pointer[time.Time] }

func (c *testClock) set(t time.Time) { c.now.Store(&t) }
func (c *testClock) Now() time.Time  { return *c.now.Load() }

// The engine honours ValidUntil itself (HardExpiry): a tunnel open when the term ends stops at its next data.
func TestExpiryEndsAnOpenTunnelAndRefusesNewOnes(t *testing.T) {
	base := time.Now()
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	alice.cred.ValidUntil = base.Add(time.Minute)
	backend := echoBackend(t)
	clock := &testClock{}
	clock.set(base)
	h := newHarnessEnv(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{},
		func(env *nodeengine.Env) { env.Now = clock.Now })
	if !h.e.Capabilities().HardExpiry {
		t.Fatal("the engine enforces ValidUntil but does not say so")
	}
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, writer, response.Body, "within the term")
	clock.set(base.Add(2 * time.Minute))
	_, _ = io.WriteString(writer, "after the term")
	if !endsWithin(response.Body, time.Second) {
		t.Fatal("the tunnel outlived the credential's term")
	}
	if got := statusOf(t, client, "example.com:80", authValue(t, alice)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("CONNECT after the term = %d, want the decoy's answer", got.StatusCode)
	}
}

func TestApplyReportHealthAndRemove(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	report, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{alice.cred})
	if err != nil || report.Restarted || report.CredCount != 1 || report.Cert.PinSHA256 != "test-pin" || report.Cert.NotAfter.IsZero() {
		t.Fatalf("Apply report = %+v, %v", report, err)
	}
	health := h.e.Health()
	if len(health) != 1 || health[0].State != plugin.RunRunning || health[0].CertPinSHA256 != "test-pin" || health[0].CertNotAfter.IsZero() {
		t.Fatalf("Health = %+v", health)
	}
	client := newHTTP2Client(t, h.addr)
	_ = statusOf(t, client, "_check", "") // an unknown key
	if err := h.e.Remove(context.Background(), h.spec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", h.addr, 200*time.Millisecond); err == nil {
		t.Fatal("the listener is still open after Remove")
	}
	if got := h.e.Health(); len(got) != 0 {
		t.Fatalf("Health after Remove = %+v", got)
	}
	stats := collect(t, h.e)
	var unknown uint32
	for _, link := range stats.Links {
		unknown += link.UnknownKeys
	}
	if unknown != 1 {
		t.Fatalf("the removed inbound's unknown keys = %d, want 1 reported once", unknown)
	}
	if again := collect(t, h.e); len(again.Links) != 0 {
		t.Fatalf("reported again: %+v", again.Links)
	}
	if err := h.e.Remove(context.Background(), "no-such-inbound"); err != nil {
		t.Fatalf("Remove of an unknown id: %v", err)
	}
}

// Nothing a tunnel owns outlives it: not on RST_STREAM, not on a dropped connection, not on a refused or timed-out dial, not
// on a Kick or the engine's Close. Goroutines return to where they started and every socket is closed.
func TestNothingLeaksWhenTunnelsEndTheHardWay(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	var slow atomic.Bool
	outbound := &testEgress{udpEcho: true, tcpDial: func(target string) (net.Conn, error) {
		switch target {
		case "blocked.example:80":
			return nil, egress.ErrBlocked
		case "slow.example:80":
			time.Sleep(60 * time.Millisecond) // finishes after the connect timeout
			slow.Store(true)
			return net.Dial("tcp", backend.listener.Addr().String())
		}
		return net.Dial("tcp", backend.listener.Addr().String())
	}}
	ts := defaultTimings
	ts.connect = 20 * time.Millisecond
	ts.udpFlowIdle = time.Hour
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, outbound, ts)

	clients := []*testHTTP2Client{newHTTP2Client(t, h.addr), newHTTP2Client(t, h.addr), newHTTP2Client(t, h.addr)}
	// RST_STREAM in the middle of a transfer.
	for range 12 {
		response, writer := clients[0].connect(t, "example.com:80", authValue(t, alice), true)
		_, _ = writer.Write(make([]byte, 40<<10))
		_ = writer.CloseWithError(errors.New("reset"))
		_ = response.Body.Close()
	}
	// Refused and timed-out dials.
	for range 6 {
		_ = statusOf(t, clients[0], "blocked.example:80", authValue(t, alice))
		_ = statusOf(t, clients[0], "slow.example:80", authValue(t, alice))
	}
	// UDP streams with live flows, ended by the client.
	for i := range 4 {
		response, writer := clients[1].connect(t, "_udp2", authValue(t, bob), true)
		for j := range 5 {
			writeUDPFrame(t, writer, udpFrame("192.0.2.1", 2000+j, "192.0.2.9", 443+i, "x"))
		}
		_ = writer.CloseWithError(errors.New("reset"))
		_ = response.Body.Close()
	}
	// Open tunnels dropped with their connection.
	var open []*io.PipeWriter
	for range 5 {
		response, writer := clients[2].connect(t, "example.com:80", authValue(t, bob), true)
		exchange(t, writer, response.Body, "alive")
		open = append(open, writer)
	}
	clients[2].kill()
	// Open tunnels ended by Kick, and a UDP stream with it.
	response, writer := clients[0].connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, writer, response.Body, "alive")
	udpResponse, udpWriter := clients[0].connect(t, "_udp2", authValue(t, alice), true)
	writeUDPFrame(t, udpWriter, udpFrame("192.0.2.1", 3000, "192.0.2.9", 443, "x"))
	_, _ = h.e.Kick(context.Background(), []string{"alice", "bob"})
	for _, w := range append(open, writer, udpWriter) {
		_ = w.CloseWithError(errors.New("reset"))
	}
	_ = response.Body.Close()
	_ = udpResponse.Body.Close()
	for _, c := range clients {
		c.kill()
		c.close()
	}

	waitFor(t, 3*time.Second, func() bool { return backendConnections(backend) == 0 })
	outbound.mu.Lock()
	sockets := append([]*testUDP(nil), outbound.udpSockets...)
	outbound.mu.Unlock()
	waitFor(t, 3*time.Second, func() bool {
		for _, s := range sockets {
			if !s.isClosed.Load() {
				return false
			}
		}
		return true
	})
	for _, name := range []string{"alice", "bob"} {
		cs := downCounter(h, name)
		waitFor(t, 3*time.Second, func() bool {
			cs.mu.Lock()
			defer cs.mu.Unlock()
			return cs.active == 0 && len(cs.streams) == 0 && len(cs.connections) == 0
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	waitFor(t, 5*time.Second, func() bool { return runtime.NumGoroutine() <= baseline+2 })
	if !slow.Load() {
		t.Fatal("the slow dial never ran: the test did not exercise the late-dial path")
	}
}

// The listener of a tunnel port sends the SETTINGS and window of any other site of the node (bridge, Hysteria2's decoy,
// the VLESS site): a probe that reads them learns nothing.
func TestListenerFingerprintMatchesTheNodesOtherSites(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	certificate, err := testCertificate()
	if err != nil {
		t.Fatal(err)
	}
	// The site as Hysteria2's TCP decoy and the VLESS site build it.
	site := &http.Server{
		Handler:           http.NotFoundHandler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{*certificate}, NextProtos: []string{"h2", "http/1.1"}},
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	if err := http2.ConfigureServer(site, &http2.Server{MaxUploadBufferPerConnection: 16 << 20}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go site.Serve(tls.NewListener(ln, site.TLSConfig))
	t.Cleanup(func() { _ = site.Close() })

	got, want := serverPreface(t, h.addr), serverPreface(t, ln.Addr().String())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tunnel port frames differ from the site's:\n got  %+v\n want %+v", got, want)
	}
}

type prefaceFrames struct {
	Settings []http2.Setting
	Windows  []uint32
}

func serverPreface(t *testing.T, addr string) prefaceFrames {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", NextProtos: []string{"h2"}})
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	var got prefaceFrames
	for len(got.Settings) == 0 || len(got.Windows) == 0 {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("reading the server's first frames: %v", err)
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = f.ForeachSetting(func(s http2.Setting) error { got.Settings = append(got.Settings, s); return nil })
			}
		case *http2.WindowUpdateFrame:
			got.Windows = append(got.Windows, f.Increment)
		}
	}
	return got
}
