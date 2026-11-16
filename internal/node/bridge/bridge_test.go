package bridge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

type testPKI struct {
	root       *x509.Certificate
	rootKey    *ecdsa.PrivateKey
	roots      *x509.CertPool
	exitKey    *ecdsa.PrivateKey
	exitDER    []byte
	serverCert tls.Certificate
	pin        string
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{SerialNumber: serialNumber(t), Subject: pkix.Name{CommonName: "mistgate-test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	exitKey, exitDER, _ := issueTestLeaf(t, root, rootKey, "nod_de1", now.Add(-time.Hour), now.Add(time.Hour), x509.ExtKeyUsageClientAuth, nil)
	serverKey, serverDER, serverLeaf := issueTestLeaf(t, root, rootKey, "msk1.example", now.Add(-time.Hour), now.Add(time.Hour), x509.ExtKeyUsageServerAuth, []string{"msk1.example"})
	serverCert := tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey, Leaf: serverLeaf}
	leafHash := sha256.Sum256(serverLeaf.Raw)
	return testPKI{root: root, rootKey: rootKey, roots: roots, exitKey: exitKey, exitDER: exitDER, serverCert: serverCert, pin: hex.EncodeToString(leafHash[:])}
}

func issueTestLeaf(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey, cn string, notBefore, notAfter time.Time, usage x509.ExtKeyUsage, dns []string) (*ecdsa.PrivateKey, []byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var uris []*url.URL
	if strings.HasPrefix(cn, "nod_") {
		uri, err := url.Parse("spiffe://agent/" + cn)
		if err != nil {
			t.Fatal(err)
		}
		uris = []*url.URL{uri}
	}
	template := &x509.Certificate{SerialNumber: serialNumber(t), Subject: pkix.Name{CommonName: cn}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, DNSNames: dns, URIs: uris}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, der, leaf
}

func serialNumber(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	return n.Add(n, big.NewInt(1))
}

func TestBridgeProofTrustAndConnectionBinding(t *testing.T) {
	pki := newTestPKI(t)
	allowed := map[string]struct{}{"nod_de1": {}}
	exporter := bytes.Repeat([]byte{0x31}, 32)
	proof, err := MakeProof("nod_msk1", exporter, pki.exitDER, pki.exitKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyProof("nod_msk1", exporter, proof, pki.roots, allowed, time.Now()); err != nil || got != "nod_de1" {
		t.Fatalf("valid proof: got %q, err %v", got, err)
	}

	wrongKey, _, _ := issueTestLeaf(t, pki.root, pki.rootKey, "nod_de1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth, nil)
	wrongSignature, err := MakeProof("nod_msk1", exporter, pki.exitDER, wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProof("nod_msk1", exporter, wrongSignature, pki.roots, allowed, time.Now()); err == nil {
		t.Fatal("a different exit key authenticated the certificate")
	}

	otherKey, otherDER, _ := issueTestLeaf(t, pki.root, pki.rootKey, "nod_de2", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth, nil)
	otherProof, err := MakeProof("nod_msk1", exporter, otherDER, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProof("nod_msk1", exporter, otherProof, pki.roots, allowed, time.Now()); err == nil {
		t.Fatal("an exit outside the allow-list authenticated")
	}

	if _, err := VerifyProof("nod_msk1", bytes.Repeat([]byte{0x32}, 32), proof, pki.roots, allowed, time.Now()); err == nil {
		t.Fatal("a proof replayed on another TLS exporter authenticated")
	}

	_, expiredDER, _ := issueTestLeaf(t, pki.root, pki.rootKey, "nod_de1", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour), x509.ExtKeyUsageClientAuth, nil)
	expired, err := MakeProof("nod_msk1", exporter, expiredDER, pki.exitKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProof("nod_msk1", exporter, expired, pki.roots, allowed, time.Now()); err == nil {
		t.Fatal("an expired certificate authenticated")
	}

	unknownRootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unknownRootTemplate := &x509.Certificate{SerialNumber: serialNumber(t), Subject: pkix.Name{CommonName: "unknown-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	unknownRootDER, err := x509.CreateCertificate(rand.Reader, unknownRootTemplate, unknownRootTemplate, &unknownRootKey.PublicKey, unknownRootKey)
	if err != nil {
		t.Fatal(err)
	}
	unknownRoot, err := x509.ParseCertificate(unknownRootDER)
	if err != nil {
		t.Fatal(err)
	}
	unknownKey, unknownDER, _ := issueTestLeaf(t, unknownRoot, unknownRootKey, "nod_de1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth, nil)
	unknownProof, err := MakeProof("nod_msk1", exporter, unknownDER, unknownKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyProof("nod_msk1", exporter, unknownProof, pki.roots, allowed, time.Now()); err == nil {
		t.Fatal("a certificate from an unknown CA authenticated")
	}
}

func TestUnauthenticatedBridgePathReturnsOrdinarySiteResponse(t *testing.T) {
	pki := newTestPKI(t)
	entry := NewEntry()
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Site", "ordinary")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "the same site body\n")
	})
	wrapped := entry.Handler(site)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		plain := httptest.NewRecorder()
		plainRequest := httptest.NewRequest(method, "https://msk1.example/private-bridge-path", nil)
		plainRequest.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		site.ServeHTTP(plain, plainRequest)
		got := httptest.NewRecorder()
		request := httptest.NewRequest(method, "https://msk1.example/private-bridge-path", nil)
		request.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		wrapped.ServeHTTP(got, request)
		if got.Code != plain.Code || !equalHeader(got.Header(), plain.Header()) || !bytes.Equal(got.Body.Bytes(), plain.Body.Bytes()) {
			t.Errorf("unauthenticated %s returned %d %v %q; ordinary site was %d %v %q", method, got.Code, got.Header(), got.Body.Bytes(), plain.Code, plain.Header(), plain.Body.Bytes())
		}
	}
}

func equalHeader(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv := b[key]
		if !bytes.Equal([]byte(joinStrings(av)), []byte(joinStrings(bv))) {
			return false
		}
	}
	return true
}

func joinStrings(in []string) string {
	return strings.Join(in, "\x00")
}

func TestDialerRefusesMissingVerificationInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		sni  string
		pin  string
	}{
		{"no SNI or pin", "", ""},
		{"malformed pin", "", "00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: "127.0.0.1", Port: 443, SNI: tc.sni, PinSHA256: tc.pin,
				Path: "/bridge", Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 443}}}
			d := NewDialer(func() DialIdentity { return DialIdentity{} })
			d.Sync(context.Background(), []DialSpec{{Dial: dial}})
			states := d.States()
			if len(states) != 1 || states[0].Error != "no_verification" || states[0].Conns != 0 {
				t.Fatalf("states = %+v", states)
			}
		})
	}
}

type echoTarget struct {
	port     uint16
	accepted chan struct{}
	close    func()
}

func startEchoTarget(t *testing.T, certificate tls.Certificate) echoTarget {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
	accepted := make(chan struct{}, 512)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	closeFn := func() {
		_ = tlsLn.Close()
		<-done
	}
	t.Cleanup(closeFn)
	return echoTarget{port: port, accepted: accepted, close: closeFn}
}

// fixtureSiteTLS12 makes the fixture's site speak TLS 1.2 only (set by one test, which restores it).
var fixtureSiteTLS12 bool

func newBridgeFixture(t *testing.T, targetPort uint16, pki testPKI) (*Entry, *Dialer, *httptest.Server, uint16) {
	return newBridgeFixtureWithTimeout(t, targetPort, pki, 0)
}

func newBridgeFixtureWithTimeout(t *testing.T, targetPort uint16, pki testPKI, streamTimeout time.Duration) (*Entry, *Dialer, *httptest.Server, uint16) {
	return newBridgeFixtureConfigured(t, targetPort, pki, streamTimeout, nil, nil)
}

func newBridgeFixtureConfigured(t *testing.T, targetPort uint16, pki testPKI, streamTimeout time.Duration,
	configureEntry func(*Entry), configureDialer func(*Dialer), beforeDial ...func(*httptest.Server)) (*Entry, *Dialer, *httptest.Server, uint16) {
	t.Helper()
	entry := NewEntry()
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	if configureEntry != nil {
		configureEntry(entry)
	}
	port := freeTCPPort(t)
	listenerAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	if errs := entry.SyncListeners([]RelayListener{{RelayID: "rly_a", ExitNodeID: "nod_de1", Port: port, Address: listenerAddress}}); len(errs) != 0 {
		t.Fatalf("listener errors = %v", errs)
	}
	site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ordinary site")
	})
	server := httptest.NewUnstartedServer(entry.Handler(site))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS13}
	if fixtureSiteTLS12 {
		server.TLS.MinVersion, server.TLS.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	}
	server.Config.TLSConfig = server.TLS
	server.Config.ReadTimeout = streamTimeout
	server.Config.WriteTimeout = streamTimeout
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	dialHost, dialPortText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dialPort, err := strconv.ParseUint(dialPortText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	probe, _, probeErr := dialTLS(probeCtx, plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: dialHost, Port: uint16(dialPort), SNI: "msk1.example", PinSHA256: pki.pin},
		DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey})
	cancelProbe()
	if probeErr != nil {
		t.Fatalf("TLS probe: %v", probeErr)
	}
	_ = probe.Close()
	for _, hook := range beforeDial {
		hook(server)
	}
	dialer := NewDialer(func() DialIdentity {
		return DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey}
	})
	if configureDialer != nil {
		configureDialer(dialer)
	}
	spec := DialSpec{Dial: plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: dialHost, Port: uint16(dialPort), SNI: "msk1.example", PinSHA256: pki.pin,
		Path: "/private-bridge-path", Pool: 2, Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: targetPort}}}}
	dialer.Sync(context.Background(), []DialSpec{spec})
	t.Cleanup(func() {
		dialer.Close()
		entry.Close()
		server.Close()
	})
	return entry, dialer, server, port
}

func TestAuthenticatedBridgeStreamOutlivesSiteTimeouts(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, relayPort := newBridgeFixtureWithTimeout(t, target.port, pki, 100*time.Millisecond)
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2
	}, "dialer did not connect both TLS pool connections")

	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(relayPort))), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(raw, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	defer client.Close()
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	select {
	case <-target.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("local inbound did not accept the bridged client")
	}
	time.Sleep(250 * time.Millisecond)

	payload := []byte("stream survived the site read and write deadlines")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("write after site timeout: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read after site timeout: %v; entry=%+v dialer=%+v counters=%+v", err, entry.States(), dialer.States(), entry.RelayCounters())
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo after site timeout = %q, want %q", got, payload)
	}
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func TestReview05_100MiBEchoBothDirectionsAndReview12Counters(t *testing.T) {
	// Review test 05 sends 100 MiB each way over the paired HTTP/2 streams and checks Review test 12's counters.
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, server, relayPort := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2
	}, "dialer did not connect both TLS pool connections: "+fmt.Sprint(dialer.States()))

	const parallel = 200
	var wg sync.WaitGroup
	errCh := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := []byte("de1-msk1-" + strconv.Itoa(i))
			if err := echoThroughRelay(relayPort, pki, payload, 20*time.Second); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	var parallelErrors []error
	for err := range errCh {
		if len(parallelErrors) < 5 {
			parallelErrors = append(parallelErrors, err)
		}
	}
	if len(parallelErrors) > 0 {
		t.Fatalf("parallel bridge errors: %v; entry=%+v dialer=%+v counters=%+v", parallelErrors, entry.States(), dialer.States(), entry.RelayCounters())
	}
	largePayload := bytes.Repeat([]byte("L"), 2<<20)
	if err := echoThroughRelay(relayPort, pki, largePayload, 15*time.Second); err != nil {
		t.Fatalf("2 MiB single-write echo: %v", err)
	}

	const transferBytes = int64(100 << 20)
	client, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(relayPort))), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tlsClient := tls.Client(client, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	client = tlsClient
	if err := tlsClient.Handshake(); err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(90 * time.Second))
	pattern := bytes.Repeat([]byte("de1-msk1-bridge-data-"), 1024)
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := io.CopyN(client, &repeatReader{pattern: pattern}, transferBytes)
		if tcp, ok := client.(*tls.Conn); ok {
			if closeErr := tcp.CloseWrite(); writeErr == nil {
				writeErr = closeErr
			}
		}
		writeDone <- writeErr
	}()
	wantHash := sha256.New()
	_, _ = io.CopyN(wantHash, &repeatReader{pattern: pattern}, transferBytes)
	gotHash := sha256.New()
	gotBytes, readErr := io.Copy(gotHash, client)
	writeErr := <-writeDone
	if writeErr != nil || readErr != nil || gotBytes != transferBytes || !bytes.Equal(gotHash.Sum(nil), wantHash.Sum(nil)) {
		t.Fatalf("100 MiB TLS transfer: wrote err=%v read err=%v bytes=%d hash_match=%v entry=%+v dialer=%+v", writeErr, readErr, gotBytes, bytes.Equal(gotHash.Sum(nil), wantHash.Sum(nil)), entry.States(), dialer.States())
	}
	counters := entry.RelayCounters()["rly_a"]
	if counters.Bytes < uint64(transferBytes) || counters.BytesBack < uint64(transferBytes) {
		t.Fatalf("entry counters = %+v, want at least %d in both directions", counters, transferBytes)
	}

	// Break the live HTTP/2 pool while another client is attached. The listener must close the client stream, then the pool reconnects.
	for {
		select {
		case <-target.accepted:
		default:
			goto drainedAccepts
		}
	}

drainedAccepts:
	cutClient, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(relayPort))), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cutTLSClient := tls.Client(cutClient, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	if err := cutTLSClient.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := cutTLSClient.Write([]byte("cut-stream")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-target.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the exit did not connect to the local target")
	}
	server.CloseClientConnections()
	cutAt := time.Now()
	_ = cutTLSClient.SetReadDeadline(cutAt.Add(3 * time.Second))
	_, cutErr := io.Copy(io.Discard, cutTLSClient)
	if timeoutErr, ok := cutErr.(net.Error); ok && timeoutErr.Timeout() {
		t.Fatalf("in-flight TLS client did not close after the bridge connection was cut: %v", cutErr)
	}
	if time.Since(cutAt) >= 3*time.Second {
		t.Fatalf("in-flight TLS client closed after %s", time.Since(cutAt))
	}
	_ = cutTLSClient.Close()
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2
	}, "the TLS pool did not reconnect after its connections were cut")
	// A dead pool fails people fast (RST after a second) by design, and the pool is back only after its backoff, so retry until it serves.
	var lastErr error
	waitFor(t, 15*time.Second, func() bool {
		lastErr = echoThroughRelay(relayPort, pki, []byte("reconnected"), 5*time.Second)
		return lastErr == nil
	}, "no stream after the pool reconnected")
}

type repeatReader struct {
	pattern []byte
	offset  int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := copy(p, r.pattern[r.offset:])
	if n < len(p) {
		m := copy(p[n:], r.pattern)
		n += m
	}
	r.offset = (r.offset + n) % len(r.pattern)
	return n, nil
}

func echoThroughRelay(port uint16, pki testPKI, payload []byte, timeout time.Duration) error {
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 3*time.Second)
	if err != nil {
		return err
	}
	conn := tls.Client(raw, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := conn.Handshake(); err != nil {
		return err
	}
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	if err := conn.CloseWrite(); err != nil {
		return err
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func TestBridgeWithoutWaitingStreamClosesWithinBound(t *testing.T) {
	pki := newTestPKI(t)
	entry := NewEntry()
	port := freeTCPPort(t)
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/bridge", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	if errs := entry.SyncListeners([]RelayListener{{RelayID: "rly_a", ExitNodeID: "nod_de1", Port: port, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}}); len(errs) != 0 {
		t.Fatal(errs)
	}
	entry.listenerMu.Lock()
	queueCapacity := cap(entry.listeners["rly_a"].queue)
	entry.listenerMu.Unlock()
	if queueCapacity != AcceptQueueSize {
		t.Fatalf("accept queue capacity = %d, want %d", queueCapacity, AcceptQueueSize)
	}
	defer entry.Close()
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, readErr := io.Copy(io.Discard, conn)
	_ = conn.Close()
	if !isTCPReset(readErr) {
		t.Fatalf("read error = %v, want TCP reset", readErr)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("closed after %s; expected RST in less than 2s", elapsed)
	}
}

func isTCPReset(err error) bool {
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	// Windows reports WSAECONNRESET as "forcibly closed by the remote host".
	message := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(message, "connection reset") || strings.Contains(message, "forcibly closed by the remote host")
}

// The entry says how many people it carries RIGHT NOW (PeerState.Clients), which Streams (a total since the start) cannot: two
// sessions open make 2, and a session that ends takes one off while the waiting pairs the exit keeps open never count.
func TestEntryCountsPeopleCarriedNow(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, relayPort := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2
	}, "dialer did not connect both TLS pool connections")
	clients := func() uint32 {
		for _, s := range entry.States() {
			if s.PeerNodeID == "nod_de1" {
				return s.Clients
			}
		}
		return 0
	}
	if got := clients(); got != 0 {
		t.Fatalf("clients with nobody connected = %d (the waiting pairs are not people)", got)
	}
	open := func() *tls.Conn {
		raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(relayPort))), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c := tls.Client(raw, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
		if err := c.Handshake(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := open(), open()
	defer b.Close()
	waitFor(t, 5*time.Second, func() bool { return clients() == 2 }, "two people were not counted: "+fmt.Sprint(entry.States()))
	a.Close()
	waitFor(t, 10*time.Second, func() bool { return clients() == 1 }, "a person who left was still counted: "+fmt.Sprint(entry.States()))
	if streams := entry.States()[0].Streams; streams < 2 {
		t.Fatalf("streams = %d, the total since the start must not fall with the people who leave", streams)
	}
}
