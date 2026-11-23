package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestReview01_ResetDuringUploadDoesNotRaceHandler(t *testing.T) {
	pki := newTestPKI(t)
	targetPort := startResetTarget(t)
	entry, dialer, _, relayPort := newBridgeFixture(t, targetPort, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	buf := make([]byte, 16<<10)
	for i := 0; i < 400; i++ {
		conn, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		for j := 0; j < 64; j++ {
			if _, err := conn.Write(buf); err != nil {
				break
			}
		}
		_ = conn.Close()
	}
	if states := entry.States(); len(states) > 1 {
		t.Fatalf("entry retained unexpected peers after reset stress: %+v", states)
	}
}

func startResetTarget(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				_, _ = io.CopyN(io.Discard, conn, 256<<10)
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				_ = conn.Close()
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return port
}

func TestReview02_200HeldClients(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	_, dialer, _, relayPort := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	const count = 200
	clients := make(chan net.Conn, count)
	errs := make(chan error, count)
	var starts sync.WaitGroup
	starts.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer starts.Done()
			conn, err := openRelayTLSClient(relayPort, pki, 20*time.Second)
			if err != nil {
				errs <- err
				return
			}
			clients <- conn
		}()
	}
	starts.Wait()
	close(clients)
	close(errs)
	for err := range errs {
		t.Errorf("held client failed: %v", err)
	}
	var held []net.Conn
	for conn := range clients {
		held = append(held, conn)
	}
	defer func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	}()
	if len(held) != count {
		t.Fatalf("held clients = %d, want %d", len(held), count)
	}
	deadline := time.After(15 * time.Second)
	for i := 0; i < count; i++ {
		select {
		case <-target.accepted:
		case <-deadline:
			t.Fatalf("target accepted only %d of %d held clients", i, count)
		}
	}
}

func TestReview03_SlowReaderDoesNotStallOtherStreams(t *testing.T) {
	pki := newTestPKI(t)
	targetPort := startStreamingTarget(t)
	_, dialer, _, relayPort := newBridgeFixture(t, targetPort, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	slow, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	if tcp, ok := slow.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(64 << 10)
	}
	time.Sleep(4 * time.Second)
	for i := 0; i < 6; i++ {
		conn, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := io.CopyN(io.Discard, conn, 2<<20)
		_ = conn.Close()
		if err != nil {
			t.Fatalf("reader %d got %d bytes of 2 MiB in 3 seconds: %v", i, n, err)
		}
	}
}

func startStreamingTarget(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}(conn)
			go func(conn net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					if _, err := conn.Write(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return port
}

func TestReview04_RotateConnectionsWhileStreamsDrain(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, relayPort := newBridgeFixtureConfigured(t, target.port, pki, 0, nil, func(d *Dialer) {
		d.timing.connectionAge = time.Second
		d.timing.ageJitter = 0
		d.timing.rotateCheck = 10 * time.Millisecond
		d.timing.drainTimeout = 3 * time.Second
		d.timing.startStagger = 10 * time.Millisecond
	})
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	// Opened together, so that both pool connections carry long streams: the rotation of a busy connection must not wait
	// for its streams to end before a replacement takes new clients.
	const oldCount = 40
	old := make([]net.Conn, oldCount)
	var opened sync.WaitGroup
	for i := 0; i < oldCount; i++ {
		opened.Add(1)
		go func() {
			defer opened.Done()
			conn, err := openRelayTLSClient(relayPort, pki, 10*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			old[i] = conn
		}()
	}
	opened.Wait()
	if t.Failed() {
		return
	}
	defer func() {
		for _, conn := range old {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	time.Sleep(2500 * time.Millisecond)
	for i, conn := range old {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("old-" + strconv.Itoa(i))); err != nil {
			t.Fatalf("old stream %d after rotation: %v", i, err)
		}
		got := make([]byte, len("old-")+len(strconv.Itoa(i)))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != "old-"+strconv.Itoa(i) {
			t.Fatalf("old stream %d response %q: %v", i, got, err)
		}
	}
	if err := echoThroughRelay(relayPort, pki, []byte("new-after-rotation"), 5*time.Second); err != nil {
		t.Fatalf("new stream while old connections drain: %v; entry=%+v", err, entry.States())
	}
}

func TestReview06_CanceledAttachRetriesReplacement(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, _, _, relayPort := newBridgeFixture(t, target.port, pki)
	var attempts atomic.Int32
	var once sync.Once
	entry.beforeAttach = func(pair *bridgePair) {
		attempts.Add(1)
		once.Do(func() { entry.finishPair(pair, "closed", true) })
	}
	if err := echoThroughRelay(relayPort, pki, []byte("retry-attach"), 5*time.Second); err != nil {
		t.Fatalf("client after canceled attach: %v", err)
	}
	if attempts.Load() < 2 {
		t.Fatalf("attach attempts = %d, want canceled pair and replacement", attempts.Load())
	}
	waitFor(t, time.Second, func() bool {
		entry.mu.RLock()
		defer entry.mu.RUnlock()
		return entry.totalClients == 0
	}, "attached client slot leaked")
}

func TestReview07_StopListenerWithBlockedClient(t *testing.T) {
	pki := newTestPKI(t)
	targetPort := startStreamingTarget(t)
	entry, dialer, _, relayPort := newBridgeFixture(t, targetPort, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	client, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if tcp, ok := client.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(64 << 10)
	}
	start := time.Now()
	entry.SyncListeners(nil)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = io.Copy(io.Discard, client)
	_ = client.Close()
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("listener stop took %s", elapsed)
	}
}

func TestReview08_TargetSwapKeepsLiveStream(t *testing.T) {
	pki := newTestPKI(t)
	oldTarget := startBannerTarget(t, "old:")
	newTarget := startBannerTarget(t, "new:")
	entry, dialer, _, relayPort := newBridgeFixture(t, oldTarget.port, pki)
	oldClient, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	_ = oldClient.SetDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 4)
	if _, err := io.ReadFull(oldClient, got); err != nil || string(got) != "old:" {
		t.Fatalf("old stream banner %q: %v", got, err)
	}
	dialer.mu.Lock()
	current := dialer.runs["nod_msk1"].dial
	dialer.mu.Unlock()
	current.Targets = []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: newTarget.port}}
	dialer.Sync(context.Background(), []DialSpec{{Dial: current}})
	if _, err := oldClient.Write([]byte("still-old")); err != nil {
		t.Fatalf("live stream was cut during target swap: %v", err)
	}
	oldEcho := make([]byte, len("still-old"))
	if _, err := io.ReadFull(oldClient, oldEcho); err != nil || string(oldEcho) != "still-old" {
		t.Fatalf("live target response %q: %v", oldEcho, err)
	}
	newClient, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer newClient.Close()
	_ = newClient.SetDeadline(time.Now().Add(5 * time.Second))
	newBanner := make([]byte, 4)
	if _, err := io.ReadFull(newClient, newBanner); err != nil || string(newBanner) != "new:" {
		t.Fatalf("new target banner %q: %v", newBanner, err)
	}
	if states := entry.States(); len(states) != 1 || states[0].Error != "" {
		t.Fatalf("target update changed bridge state: %+v", states)
	}
}

type bannerTarget struct {
	port uint16
}

func startBannerTarget(t *testing.T, banner string) bannerTarget {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.WriteString(conn, banner)
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return bannerTarget{port: port}
}

func TestReview09_IdlePairsDoNotSetBridgeError(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, _ := newBridgeFixtureConfigured(t, target.port, pki, 0, func(e *Entry) {
		e.timing.pendingWait = 100 * time.Millisecond
		e.timing.pendingJitter = 0
	}, nil)
	time.Sleep(350 * time.Millisecond)
	for _, state := range entry.States() {
		if state.Error != "" {
			t.Fatalf("idle entry bridge error = %q", state.Error)
		}
	}
	for _, state := range dialer.States() {
		if state.Error != "" {
			t.Fatalf("idle exit bridge error = %q", state.Error)
		}
	}
}

func TestReview12_CountersHaveDirectionAndCumulativeStreams(t *testing.T) {
	pki := newTestPKI(t)
	const greeting = "exit-to-client:"
	target := startBannerTarget(t, greeting)
	fixedTime := pki.serverCert.Leaf.NotBefore.Add(time.Minute)
	entry, dialer, _, relayPort := newBridgeFixtureConfigured(t, target.port, pki, 0, func(e *Entry) {
		e.mu.Lock()
		e.serve.Now = func() time.Time { return fixedTime }
		e.mu.Unlock()
	}, func(d *Dialer) { d.SetClock(func() time.Time { return fixedTime }) })
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2
	}, "dialer did not connect both TLS pool connections")
	first := []byte("client-to-exit-one")
	second := []byte("client-to-exit-two")
	if err := asymmetricEchoThroughRelay(relayPort, []byte(greeting), first, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := asymmetricEchoThroughRelay(relayPort, []byte(greeting), second, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		states := entry.States()
		return len(states) == 1 && states[0].Streams >= 2
	}, "entry stream counter did not advance cumulatively")
	entryState := entry.States()[0]
	dialState := dialer.States()[0]
	wantOut := uint64(len(first) + len(second))
	wantIn := wantOut + 2*uint64(len(greeting))
	if entryState.BytesIn != wantIn || dialState.BytesIn != wantIn || entryState.BytesOut != wantOut || dialState.BytesOut != wantOut {
		t.Fatalf("wrong byte direction or totals: entry=%+v exit=%+v want bytes_in=%d bytes_out=%d", entryState, dialState, wantIn, wantOut)
	}
	if entryState.Streams < 2 || dialState.Streams < 2 {
		t.Fatalf("stream count was not cumulative: entry=%+v exit=%+v", entryState, dialState)
	}
	if !entryState.Since.Equal(fixedTime) || !dialState.Since.Equal(fixedTime) {
		t.Fatalf("since values do not use the injected agent clock: entry=%v exit=%v", entryState.Since, dialState.Since)
	}
}

func asymmetricEchoThroughRelay(port uint16, greeting, request []byte, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", relayAddress(port), 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	gotGreeting := make([]byte, len(greeting))
	if _, err := io.ReadFull(conn, gotGreeting); err != nil {
		return err
	}
	if !bytes.Equal(gotGreeting, greeting) {
		return fmt.Errorf("greeting = %q, want %q", gotGreeting, greeting)
	}
	if _, err := conn.Write(request); err != nil {
		return err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.CloseWrite(); err != nil {
			return err
		}
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, request) {
		return fmt.Errorf("echo = %q, want %q", got, request)
	}
	return nil
}

func openRelayTLSClient(port uint16, pki testPKI, timeout time.Duration) (*tls.Conn, error) {
	raw, err := net.DialTimeout("tcp", relayAddress(port), 3*time.Second)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := conn.Handshake(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func relayAddress(port uint16) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
}

func TestBridgeProofCacheExpiresWithCertificate(t *testing.T) {
	pki := newTestPKI(t)
	now := time.Now()
	_, expiredDER, expiredLeaf := issueTestLeaf(t, pki.root, pki.rootKey, "nod_de1", now.Add(-2*time.Hour), now.Add(-time.Hour), x509.ExtKeyUsageClientAuth, nil)
	exporter := make([]byte, 32)
	proof, err := MakeProof("nod_msk1", exporter, expiredDER, pki.exitKey)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEntry()
	defer e.Close()
	key := sha256.Sum256(exporter)
	certHash := sha256.Sum256(proof.CertificateDER)
	sigHash := sha256.Sum256(proof.Signature)
	e.authCache[key] = cachedProof{exitID: "nod_de1", certHash: certHash, sigHash: sigHash, notAfter: expiredLeaf.NotAfter, last: now}
	if _, err := e.verifyRequest("nod_msk1", exporter, proof, pki.roots, map[string]struct{}{"nod_de1": {}}, now); err == nil {
		t.Fatal("an expired proof was accepted from the authentication cache")
	}
}

func TestBridgeFailuresAreRateLimitedAndCoded(t *testing.T) {
	pki := newTestPKI(t)
	entry := NewEntry()
	defer entry.Close()
	now := time.Unix(1_800_000_000, 0)
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots,
		Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	entry.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	entry.setPeerError("nod_de1", "stream")
	now = now.Add(30 * time.Second)
	entry.setPeerError("nod_de1", "stream")
	now = now.Add(31 * time.Second)
	entry.setPeerError("nod_de1", "stream")
	if got := bytes.Count(logs.Bytes(), []byte("code=stream")); got != 2 {
		t.Fatalf("coded failures logged %d times, want twice across two minutes: %s", got, logs.String())
	}
	if bytes.Contains(logs.Bytes(), []byte("private-bridge-path")) || bytes.Contains(logs.Bytes(), []byte("nod_de1")) {
		t.Fatalf("bridge log contains a path or peer identifier: %s", logs.String())
	}
}

func TestBridgeExitIdentityComesFromSPIFFEURI(t *testing.T) {
	for _, tc := range []struct {
		name string
		uri  string
		want string
	}{
		{name: "no URI", want: ""},
		{name: "valid URI", uri: "spiffe://agent/nod_de1", want: "nod_de1"},
		{name: "URI overrides common name", uri: "spiffe://agent/nod_de2", want: "nod_de2"},
		{name: "wrong scheme", uri: "https://agent/nod_de1", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "nod_de1"}}
			if tc.uri != "" {
				u, err := url.Parse(tc.uri)
				if err != nil {
					t.Fatal(err)
				}
				leaf.URIs = []*url.URL{u}
			}
			if got := nodeIDFromCertificate(leaf); got != tc.want {
				t.Fatalf("node ID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBridgeAllowedExitRemovalStopsLiveStreamAndListener(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, _, _, relayPort := newBridgeFixture(t, target.port, pki)
	client, err := openRelayTLSClient(relayPort, pki, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-target.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("exit did not attach the live stream")
	}
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de2"}, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(time.Second))
	if _, err := client.Write([]byte("after-removal")); err == nil {
		buf := make([]byte, 1)
		if _, readErr := client.Read(buf); readErr == nil {
			t.Fatal("client stream remained live after its exit was removed")
		}
	}
	entry.listenerMu.Lock()
	_, listenerStillOpen := entry.listeners["rly_a"]
	entry.listenerMu.Unlock()
	if listenerStillOpen || bridgePortOpen(relayPort) {
		t.Fatal("listener for a removed exit remained open")
	}
}

func bridgePortOpen(port uint16) bool {
	conn, err := net.DialTimeout("tcp", relayAddress(port), 75*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

type failingListener struct {
	calls  chan time.Time
	closed chan struct{}
	once   sync.Once
}

func (l *failingListener) Accept() (net.Conn, error) {
	select {
	case l.calls <- time.Now():
	default:
	}
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
		return nil, errors.New("temporary accept failure")
	}
}

func (l *failingListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *failingListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestEntryAcceptErrorsUseExponentialBackoff(t *testing.T) {
	e := NewEntry()
	e.timing.acceptRetryMin = 10 * time.Millisecond
	e.timing.acceptRetryMax = 40 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln := &failingListener{calls: make(chan time.Time, 8), closed: make(chan struct{})}
	state := &listenerState{ln: ln, ctx: ctx, cancel: cancel, queue: make(chan net.Conn, 1), active: map[net.Conn]struct{}{}}
	done := make(chan struct{})
	state.wg.Add(1)
	go func() { e.acceptLoop(state); close(done) }()
	var calls []time.Time
	for len(calls) < 4 {
		select {
		case at := <-ln.calls:
			calls = append(calls, at)
		case <-time.After(time.Second):
			t.Fatal("accept loop stopped retrying")
		}
	}
	first, second, third := calls[1].Sub(calls[0]), calls[2].Sub(calls[1]), calls[3].Sub(calls[2])
	if first < 8*time.Millisecond || second < first+5*time.Millisecond || third < second+10*time.Millisecond {
		t.Fatalf("accept retry intervals = %s, %s, %s", first, second, third)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not stop after cancellation")
	}
}

func TestReview11_DERPinEmptySNIAndTrustedRoots(t *testing.T) {
	pki := newTestPKI(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	var serverName chan string
	serverName = make(chan string, 4)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			serverName <- hello.ServerName
			return nil, nil
		}}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	identity := DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: host, Port: uint16(port), Path: "/bridge"}
	base.PinSHA256 = pki.pin
	conn, _, err := dialTLSWithTimeout(ctx, base, identity, time.Second, time.Second, nil)
	if err != nil {
		t.Fatalf("DER pin with empty SNI: %v", err)
	}
	_ = conn.Close()
	select {
	case got := <-serverName:
		if got != "" {
			t.Fatalf("server saw SNI %q for empty-SNI pinned dial", got)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not observe pinned dial")
	}

	base.PinSHA256 = ""
	base.SNI = "msk1.example"
	conn, _, err = dialTLSWithTimeout(ctx, base, identity, time.Second, time.Second, pki.roots)
	if err != nil {
		t.Fatalf("system-root path with trusted test roots: %v", err)
	}
	_ = conn.Close()
	if conn, _, err = dialTLSWithTimeout(ctx, base, identity, time.Second, time.Second, nil); err == nil {
		_ = conn.Close()
		t.Fatal("a non-system test CA was accepted without a pin or explicit roots")
	}
}

func TestReview13_StandardHTTP2SettingsExceptConnectionWindow(t *testing.T) {
	pki := newTestPKI(t)
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS13}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	defer server.Close()
	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	var settingsSeen bool
	var maxStreams, streamWindow, maxFrame uint32
	var connectionWindow uint32 = 65535
	deadline := time.Now().Add(2 * time.Second)
	_ = conn.SetDeadline(deadline)
	for !settingsSeen || connectionWindow <= 65535 {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				if err := frame.ForeachSetting(func(setting http2.Setting) error {
					switch setting.ID {
					case http2.SettingMaxConcurrentStreams:
						maxStreams = setting.Val
					case http2.SettingInitialWindowSize:
						streamWindow = setting.Val
					case http2.SettingMaxFrameSize:
						maxFrame = setting.Val
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				settingsSeen = true
			}
		case *http2.WindowUpdateFrame:
			if frame.StreamID == 0 {
				connectionWindow += frame.Increment
			}
		}
	}
	if streamWindow != 1<<20 || maxStreams != 250 || maxFrame != 1<<20 {
		t.Fatalf("HTTP/2 standard stream settings changed: window=%d streams=%d frame=%d", streamWindow, maxStreams, maxFrame)
	}
	if connectionWindow != 16<<20 {
		t.Fatalf("connection window = %d, want 16 MiB", connectionWindow)
	}
}

func TestReview14_ProofCannotCrossTLSConnections(t *testing.T) {
	pki := newTestPKI(t)
	entry := NewEntry()
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(entry.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Site", "ordinary")
		_, _ = io.WriteString(w, "ordinary response")
	})))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS13}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	defer func() { server.Close(); entry.Close() }()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: host, Port: uint16(port), SNI: "msk1.example", PinSHA256: pki.pin, Path: "/private-bridge-path"}
	identity := DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connA, proofA, err := dialTLS(ctx, dial, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, _, err := dialTLS(ctx, dial, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	client, err := (&http2.Transport{}).NewClientConn(connB)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+net.JoinHostPort(host, portText)+"/private-bridge-path", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(ProtocolHeader, "2")
	request.Header.Set(StreamHeader, string(pairDown))
	request.Header.Set(PairIDHeader, strings.Repeat("a", 32))
	proofA.setHeaders(request.Header)
	response, err := client.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("X-Site") != "ordinary" || string(body) != "ordinary response" {
		t.Fatalf("cross-connection proof did not fall back to the site: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
}

func TestReview15_FastDeadConnectionsBackOff(t *testing.T) {
	pki := newTestPKI(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan time.Time, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- time.Now()
			_ = conn.Close()
		}
	}()
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.ParseUint(portText, 10, 16)
	dialer := NewDialer(func() DialIdentity {
		return DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey}
	})
	dialer.timing.retryMin = 40 * time.Millisecond
	dialer.timing.retryMax = 160 * time.Millisecond
	// The delay the dialer asks for before each retry is recorded, not measured: a clock on a loaded machine cannot tell 80 ms
	// from 120 ms, but the requested delays are what the dialer decided.
	var waitsMu sync.Mutex
	var waits []time.Duration
	dialer.timing.retryJitter = func(wait time.Duration) time.Duration {
		waitsMu.Lock()
		defer waitsMu.Unlock()
		waits = append(waits, wait)
		return wait
	}
	dialer.timing.startStagger = 30 * time.Second
	dialer.timing.dialTimeout = time.Second
	dialer.timing.pendingWorkers = 1
	dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: host, Port: uint16(port), SNI: "msk1.example", PinSHA256: pki.pin,
		Path: "/bridge", Pool: 2, Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 1}}}
	dialer.Sync(context.Background(), []DialSpec{{Dial: dial}})
	defer dialer.Close()
	var seen []time.Time
	timeout := time.After(3 * time.Second)
	for len(seen) < 5 {
		select {
		case at := <-accepted:
			seen = append(seen, at)
		case <-timeout:
			t.Fatalf("only %d fast-dead TLS connections were accepted", len(seen))
		}
	}
	// Four failures came before the fifth accept; each asked for a wait, doubling up to the cap.
	waitsMu.Lock()
	got := append([]time.Duration(nil), waits...)
	waitsMu.Unlock()
	want := []time.Duration{40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond, 160 * time.Millisecond}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("fast-dead retries did not back off: asked waits %v, want %v first", got, want)
	}
	// A timer never fires early, so however loaded the machine is, those waits lie between the first and the last accept.
	if span := seen[4].Sub(seen[0]); span < 440*time.Millisecond {
		t.Fatalf("fast-dead retries did not wait what they asked: %s between the first and the fifth accept, want at least 440ms", span)
	}
}

func TestReview11_TLS12SiteSupport(t *testing.T) {
	pki := newTestPKI(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "tls12") }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	defer server.Close()
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	conn, _, err := dialTLS(context.Background(), plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: host, Port: uint16(port), SNI: "msk1.example", PinSHA256: pki.pin},
		DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
}

func TestSiteScannersCannotLockOutTheBridge(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	var held []net.Conn
	var heldMu sync.Mutex
	t.Cleanup(func() {
		heldMu.Lock()
		defer heldMu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	_, dialer, _, relayPort := newBridgeFixtureConfigured(t, target.port, pki, 0, nil, nil, func(server *httptest.Server) {
		// 200 idle sockets that never speak TLS: more than twice the whole anonymous set, all from one address.
		for i := 0; i < 200; i++ {
			c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			heldMu.Lock()
			held = append(held, c)
			heldMu.Unlock()
		}
	})
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "the exit was locked out by idle scanner connections")
	if err := echoThroughRelay(relayPort, pki, []byte("through-the-scanners"), 5*time.Second); err != nil {
		t.Fatalf("relay while scanners hold the site: %v", err)
	}
}

// delayedProxy forwards to target and delays every chunk by oneWay in each direction: a link with a 2*oneWay round trip.
func delayedProxy(t *testing.T, target string, oneWay time.Duration) (host string, port uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	type chunk struct {
		data []byte
		at   time.Time
	}
	pipe := func(dst, src net.Conn) {
		queue := make(chan chunk, 4096)
		go func() {
			defer dst.Close()
			for c := range queue {
				time.Sleep(time.Until(c.at))
				if _, err := dst.Write(c.data); err != nil {
					for range queue { // drain, so the reader never blocks
					}
					return
				}
			}
		}()
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				queue <- chunk{data: append([]byte(nil), buf[:n]...), at: time.Now().Add(oneWay)}
			}
			if err != nil {
				close(queue)
				return
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go pipe(s, c)
			go pipe(c, s)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", uint16(addr.Port)
}

// A burst of people over a link with a real round trip: the idle pairs the entry can hand out are replaced one round trip
// later, so the supply has to grow with the demand or most of the burst gets a reset.
func TestBurstOverLatencyLinkIsServed(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	_, dialer, server, relayPort := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	host, port := delayedProxy(t, server.Listener.Addr().String(), 40*time.Millisecond)
	dialer.mu.Lock()
	current := dialer.runs["nod_msk1"].dial
	dialer.mu.Unlock()
	current.Host, current.Port = host, port
	dialer.Sync(context.Background(), []DialSpec{{Dial: current}})
	waitFor(t, 15*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not reconnect over the slow link")
	time.Sleep(time.Second) // the idle pairs come up

	const count = 150
	errs := make(chan error, count)
	var wg sync.WaitGroup
	conns := make(chan net.Conn, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := openRelayTLSClient(relayPort, pki, 15*time.Second)
			if err != nil {
				errs <- err
				return
			}
			conns <- c
		}()
	}
	wg.Wait()
	close(errs)
	close(conns)
	for c := range conns {
		_ = c.Close()
	}
	failed := 0
	var first error
	for err := range errs {
		if failed == 0 {
			first = err
		}
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d of %d people of a burst were refused over a 80 ms link; first: %v", failed, count, first)
	}
}

// The site of an agent's own bridge accepts TLS 1.2 like any server. The proof is bound to the connection through the TLS
// exporter, which Go gives on TLS 1.2 only with the extended master secret; without it the request is an ordinary one.
func TestBridgeAuthenticatesOverTLS12(t *testing.T) {
	fixtureSiteTLS12 = true
	t.Cleanup(func() { fixtureSiteTLS12 = false })
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	_, dialer, _, relayPort := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "the exit did not connect over TLS 1.2")
	if err := echoThroughRelay(relayPort, pki, []byte("over-tls12"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// A TLS 1.2 connection without the extended master secret has no safe exporter (triple handshake): the entry must not
// build a proof on it, so the request is an ordinary one for the site. A client with the extension (every browser, Go,
// the exit) gets the exporter.
func TestBridgeProofNeedsExtendedMasterSecretOnTLS12(t *testing.T) {
	pki := newTestPKI(t)
	exporterErr := make(chan error, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := exporterFor(r.TLS)
		exporterErr <- err
		_, _ = io.WriteString(w, "ok")
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	defer server.Close()

	get := func(conn net.Conn) {
		t.Helper()
		client, err := (&http2.Transport{}).NewClientConn(conn)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		request, _ := http.NewRequest(http.MethodGet, "https://msk1.example/", nil)
		response, err := client.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	withEMS, err := tls.Dial("tcp", server.Listener.Addr().String(), &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example",
		NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer withEMS.Close()
	get(withEMS)
	if err := <-exporterErr; err != nil {
		t.Fatalf("no exporter on TLS 1.2 with the extended master secret: %v", err)
	}

	raw, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	withoutEMS := utls.UClient(raw, &utls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}, utls.HelloCustom)
	if err := withoutEMS.ApplyPreset(&utls.ClientHelloSpec{
		TLSVersMin: tls.VersionTLS12, TLSVersMax: tls.VersionTLS12,
		CipherSuites:       []uint16{utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.CurveP256}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{utls.ECDSAWithP256AndSHA256}},
			&utls.ALPNExtension{AlpnProtocols: []string{"h2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := withoutEMS.Handshake(); err != nil {
		t.Fatal(err)
	}
	defer withoutEMS.Close()
	get(withoutEMS)
	if err := <-exporterErr; err == nil {
		t.Fatal("an exporter was produced on TLS 1.2 without the extended master secret")
	}
}

// The lifetime of an idle pair ends when a person takes it: a session must outlive the idle time of a waiting request.
func TestAttachedStreamOutlivesTheIdleTimeOfAPair(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	_, dialer, _, relayPort := newBridgeFixtureConfigured(t, target.port, pki, 0, func(e *Entry) {
		e.timing.pendingWait = 300 * time.Millisecond
		e.timing.pendingJitter = 0
	}, nil)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	client, err := openRelayTLSClient(relayPort, pki, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	time.Sleep(time.Second) // more than three idle times of a waiting pair
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("still here")); err != nil {
		t.Fatalf("session was cut after the idle time of its pair: %v", err)
	}
	got := make([]byte, len("still here"))
	if _, err := io.ReadFull(client, got); err != nil || string(got) != "still here" {
		t.Fatalf("echo after the idle time of its pair: %q, %v", got, err)
	}
}

// The progress timer watches one blocked write to the person. A session that is quiet towards the person (nothing to write)
// is not stalled and is left to the idle time of the connection.
func TestQuietStreamIsNotAStalledOne(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	_, dialer, _, relayPort := newBridgeFixtureConfigured(t, target.port, pki, 0, func(e *Entry) {
		e.timing.progress = 300 * time.Millisecond
	}, nil)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	client, err := openRelayTLSClient(relayPort, pki, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	echo := func(text string) error {
		_ = client.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := client.Write([]byte(text)); err != nil {
			return err
		}
		got := make([]byte, len(text))
		if _, err := io.ReadFull(client, got); err != nil {
			return err
		}
		if string(got) != text {
			return fmt.Errorf("echo %q, want %q", got, text)
		}
		return nil
	}
	if err := echo("first"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // three progress times of silence
	if err := echo("second"); err != nil {
		t.Fatalf("a quiet session was cut as a stalled one: %v", err)
	}
}

// A person who does not read at all holds a stream only for the progress time of a blocked write, then it is reset and
// its credit returns to the connection.
func TestStalledReaderIsCutAfterTheProgressTime(t *testing.T) {
	pki := newTestPKI(t)
	targetPort := startStreamingTarget(t)
	entry, dialer, _, relayPort := newBridgeFixtureConfigured(t, targetPort, pki, 0, func(e *Entry) {
		e.timing.progress = 500 * time.Millisecond
	}, nil)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	slow, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	if tcp, ok := slow.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(64 << 10)
	}
	waitFor(t, 5*time.Second, func() bool {
		entry.mu.RLock()
		defer entry.mu.RUnlock()
		return entry.totalClients == 1
	}, "the reader was not taken in")
	waitFor(t, 10*time.Second, func() bool {
		entry.mu.RLock()
		defer entry.mu.RUnlock()
		return entry.totalClients == 0
	}, "the stalled reader still holds its stream after the progress time")
	_ = slow.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, slow); err == nil || func() bool { ne, ok := err.(net.Error); return ok && ne.Timeout() }() {
		t.Fatalf("the stalled reader was not reset: %v", err)
	}
}

// Resets of people and of the exit's server are the weather of a relay, not an error of the bridge: the bridge health that
// the panel gates on must not flap with them.
func TestSessionResetsAreNotBridgeErrors(t *testing.T) {
	pki := newTestPKI(t)
	resetPort := startResetTarget(t)
	echo := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, relayPort := newBridgeFixture(t, resetPort, pki)
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	buf := make([]byte, 16<<10)
	for i := 0; i < 30; i++ { // the exit's server resets while the person uploads
		conn, err := net.DialTimeout("tcp", relayAddress(relayPort), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		for j := 0; j < 32; j++ {
			if _, err := conn.Write(buf); err != nil {
				break
			}
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	// ... and people reset mid-session against a server that is fine.
	dialer.mu.Lock()
	current := dialer.runs["nod_msk1"].dial
	dialer.mu.Unlock()
	current.Targets = []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: echo.port}}
	dialer.Sync(context.Background(), []DialSpec{{Dial: current}})
	for i := 0; i < 30; i++ {
		client, err := openRelayTLSClient(relayPort, pki, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = client.Write(buf)
		if tcp, ok := client.NetConn().(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = client.NetConn().Close()
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	for _, state := range entry.States() {
		if state.Error != "" {
			t.Fatalf("entry bridge error after resets = %q", state.Error)
		}
	}
	for _, state := range dialer.States() {
		if state.Error != "" {
			t.Fatalf("exit bridge error after resets = %q", state.Error)
		}
	}
}

// Every timer of a bridge at once, short: waiting requests expire and are replaced, writes are watched, PINGs run, and a
// session that is quiet for a while between short exchanges lives through all of it.
func TestSessionSurvivesTheTimersOfTheBridge(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, relayPort := newBridgeFixtureConfigured(t, target.port, pki, 0, func(e *Entry) {
		e.timing.pendingWait = 150 * time.Millisecond
		e.timing.pendingJitter = 0
		e.timing.progress = 150 * time.Millisecond
		e.timing.pairWait = 300 * time.Millisecond
	}, func(d *Dialer) {
		d.timing.pingInterval = 50 * time.Millisecond
		d.timing.pingJitter = 0
		d.timing.pingTimeout = time.Second
	})
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "pool did not connect")
	client, err := openRelayTLSClient(relayPort, pki, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for i := 0; i < 8; i++ {
		text := "round-" + strconv.Itoa(i)
		_ = client.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := client.Write([]byte(text)); err != nil {
			t.Fatalf("round %d write: %v", i, err)
		}
		got := make([]byte, len(text))
		if _, err := io.ReadFull(client, got); err != nil || string(got) != text {
			t.Fatalf("round %d echo %q: %v", i, got, err)
		}
		time.Sleep(400 * time.Millisecond) // quiet, longer than every timer above
	}
	for _, state := range append(entryStates(entry), dialerErrors(dialer)...) {
		if state != "" {
			t.Fatalf("bridge error after the soak: %q", state)
		}
	}
}

func entryStates(e *Entry) []string {
	var out []string
	for _, s := range e.States() {
		out = append(out, s.Error)
	}
	return out
}

func dialerErrors(d *Dialer) []string {
	var out []string
	for _, s := range d.States() {
		out = append(out, s.Error)
	}
	return out
}
