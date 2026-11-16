package hosttunnel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func selfSigned(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// dialChrome runs Dial on one end of a pipe; the other end peeks the hello and serves TLS 1.3 with h2, as the node does.
func dialChrome(t *testing.T, secret string, at time.Time) (hello Hello, alpn []string) {
	t.Helper()
	cert, _ := selfSigned(t, "sub.example.com")
	panelCert, _ := selfSigned(t, "panel")
	// Over TCP, not net.Pipe: the server writes its session ticket while the client writes, which needs a buffer.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		tc, err := Dial(context.Background(), client, DialConfig{
			ServerName: "sub.example.com", Secret: secret, Certificate: panelCert,
			VerifyNode: func([][]byte) error { return nil }, Now: func() time.Time { return at },
		})
		if err != nil {
			done <- err
			return
		}
		_, err = tc.Write([]byte("ping"))
		done <- err
	}()
	hello, replay, err := PeekHello(server, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ts := tls.Server(replay, &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"},
		ClientAuth: tls.RequireAnyClientCert,
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			alpn = append([]string(nil), h.SupportedProtos...)
			return nil, nil
		},
	})
	if err := ts.Handshake(); err != nil {
		t.Fatalf("handshake after replay: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(ts, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return hello, alpn
}

func TestDialLooksLikeChromeAndCarriesTheMark(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	hello, alpn := dialChrome(t, "s", at)
	if hello.ServerName != "sub.example.com" {
		t.Fatalf("server name %q", hello.ServerName)
	}
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("alpn %q", alpn)
	}
	if bytes.Contains(hello.raw, []byte("mg-")) {
		t.Fatal("the hello carries the old ALPN mark")
	}
	if _, got, ok := hello.Mark("s"); !ok || !got.Equal(at) {
		t.Fatalf("mark = %v, %v", got, ok)
	}
	if _, _, ok := hello.Mark("other"); ok {
		t.Fatal("the mark opened with another secret")
	}
	// The mark is bound to the whole hello: a byte changed anywhere outside the session id breaks it.
	for _, i := range []int{4, helloRandom, helloRandom + 31, helloSIDEnd, len(hello.raw) / 2, len(hello.raw) - 1} {
		h := Hello{raw: append([]byte(nil), hello.raw...)}
		h.raw[i] ^= 1
		if _, _, ok := h.Mark("s"); ok {
			t.Fatalf("the mark survived a change at byte %d", i)
		}
	}
}

func TestPlainHelloHasNoMark(t *testing.T) {
	cert, pool := selfSigned(t, "sub.example.com")
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		tc := tls.Client(client, &tls.Config{ServerName: "sub.example.com", RootCAs: pool})
		if tc.Handshake() == nil {
			_, _ = tc.Write([]byte("ping"))
		}
	}()
	hello, replay, err := PeekHello(server, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := hello.Mark(""); ok || hello.raw == nil {
		t.Fatalf("raw %d bytes, mark %v", len(hello.raw), ok)
	}
	ts := tls.Server(replay, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err := ts.Handshake(); err != nil {
		t.Fatalf("handshake after replay: %v", err)
	}
}

func TestReplayFresh(t *testing.T) {
	var r Replay
	now := time.Unix(1_800_000_000, 0)
	a, b := [32]byte{1}, [32]byte{2}
	if !r.Fresh(a, now, now) {
		t.Fatal("first use refused")
	}
	if r.Fresh(a, now, now.Add(time.Second)) {
		t.Fatal("replay accepted")
	}
	if r.Fresh(b, now.Add(-3*time.Minute), now) || r.Fresh(b, now.Add(3*time.Minute), now) {
		t.Fatal("a mark out of the window accepted")
	}
	if !r.Fresh(b, now.Add(-MarkWindow), now) {
		t.Fatal("a mark at the edge of the window refused")
	}
	later := now.Add(2*MarkWindow + time.Second)
	r.Fresh([32]byte{3}, later, later)
	if _, ok := r.seen[a]; ok {
		t.Fatal("an expired mark is still remembered")
	}
}

func TestPeekHelloGarbageIsReplayed(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	msg := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	go func() { _, _ = client.Write(msg) }()
	hello, replay, err := PeekHello(server, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if hello.ServerName != "" || hello.raw != nil {
		t.Fatalf("hello = %+v", hello)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(replay, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("replayed %q", got)
	}
}

func TestPeekHelloReadErrorCloses(t *testing.T) {
	client, server := net.Pipe()
	_ = client.Close()
	if _, _, err := PeekHello(server, time.Second); err == nil {
		t.Fatal("want a read error")
	}
}

// tunnelPair runs ServerHandler over an HTTP/2 server on one end of a pipe and returns the node's client connection.
func tunnelPair(t *testing.T, accept func(kind string, c net.Conn) bool) *http2.ClientConn {
	t.Helper()
	a, b := net.Pipe()
	srv := &http2.Server{}
	go srv.ServeConn(a, &http2.ServeConnOpts{Handler: ServerHandler(accept)})
	cc, err := (&http2.Transport{ReadIdleTimeout: ReadIdle, PingTimeout: PingTimeout}).NewClientConn(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close(); _ = a.Close() })
	return cc
}

func TestStreamMovesBytesBothWays(t *testing.T) {
	type got struct {
		kind string
		conn net.Conn
	}
	accepted := make(chan got, 1)
	cc := tunnelPair(t, func(kind string, c net.Conn) bool {
		accepted <- got{kind, c}
		return true
	})
	peer := netip.MustParseAddrPort("203.0.113.7:51234")
	node, err := OpenStream(context.Background(), cc, "tunnel.example.com", KindTLS, peer)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	g := <-accepted
	panel := g.conn
	defer panel.Close()
	if g.kind != KindTLS {
		t.Fatalf("kind = %q", g.kind)
	}
	if ra := panel.RemoteAddr().String(); ra != peer.String() {
		t.Fatalf("remote = %q", ra)
	}

	const size = 1 << 20
	up := make([]byte, size)
	down := make([]byte, size)
	_, _ = rand.Read(up)
	_, _ = rand.Read(down)
	errs := make(chan error, 4)
	go func() { _, err := node.Write(up); errs <- err }()
	go func() { _, err := panel.Write(down); errs <- err }()
	gotUp := make([]byte, size)
	gotDown := make([]byte, size)
	go func() { _, err := io.ReadFull(panel, gotUp); errs <- err }()
	go func() { _, err := io.ReadFull(node, gotDown); errs <- err }()
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(up, gotUp) || !bytes.Equal(down, gotDown) {
		t.Fatal("bytes differ")
	}
}

// After the tunnel died and a stream's handler returned, the panel's HTTP server still sets a write deadline on the
// connection it closes (TLS close-notify). That must not reach the finished HTTP/2 response writer: it panics there.
func TestDeadlineAfterTunnelDeathIsHarmless(t *testing.T) {
	accepted := make(chan net.Conn, 1)
	returned := make(chan struct{})
	inner := ServerHandler(func(_ string, c net.Conn) bool { accepted <- c; return true })
	a, b := net.Pipe()
	go (&http2.Server{}).ServeConn(a, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		close(returned)
	})})
	cc, err := (&http2.Transport{}).NewClientConn(b)
	if err != nil {
		t.Fatal(err)
	}
	node, err := OpenStream(context.Background(), cc, "tunnel.example.com", KindTLS, netip.MustParseAddrPort("203.0.113.7:51234"))
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	panel := <-accepted
	_ = a.Close() // the tunnel dies
	_ = b.Close()
	<-returned // the response writer is finished from here on
	if err := panel.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Write([]byte("x")); err == nil {
		t.Fatal("a write after the stream ended succeeded")
	}
}

// A read deadline that passed fails the pending read only: net/http does that to a hijacked connection (it aborts its
// background read), and the stream must go on afterwards with no byte lost.
func TestReadDeadlineCanBeReset(t *testing.T) {
	accepted := make(chan net.Conn, 1)
	cc := tunnelPair(t, func(_ string, c net.Conn) bool { accepted <- c; return true })
	node, err := OpenStream(context.Background(), cc, "tunnel.example.com", KindLink, netip.AddrPort{})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	panel := <-accepted
	defer panel.Close()
	for _, c := range []net.Conn{panel, node} {
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		_, err := c.Read(make([]byte, 1))
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("read past the deadline: %v", err)
		}
		_ = c.SetReadDeadline(time.Time{})
	}
	go func() { _, _ = node.Write([]byte("to panel")); _, _ = panel.Write([]byte("to node")) }()
	buf := make([]byte, 8)
	if _, err := io.ReadFull(panel, buf); err != nil || string(buf) != "to panel" {
		t.Fatalf("panel read %q, %v", buf, err)
	}
	buf = buf[:7]
	if _, err := io.ReadFull(node, buf); err != nil || string(buf) != "to node" {
		t.Fatalf("node read %q, %v", buf, err)
	}
}

func TestRefusedStream(t *testing.T) {
	cc := tunnelPair(t, func(string, net.Conn) bool { return false })
	c, err := OpenStream(context.Background(), cc, "tunnel.example.com", KindLink, netip.AddrPort{})
	if err != nil {
		return // the stream ended before its headers were read: refused just as well
	}
	defer c.Close()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("a refused stream reads data")
	}
}

func TestBadKindRefused(t *testing.T) {
	cc := tunnelPair(t, func(string, net.Conn) bool { return true })
	if _, err := OpenStream(context.Background(), cc, "tunnel.example.com", "other", netip.AddrPort{}); err == nil {
		t.Fatal("want refusal")
	}
	if _, err := OpenStream(context.Background(), cc, "tunnel.example.com", KindTLS, netip.AddrPort{}); err == nil {
		t.Fatal("a tls stream without peer was accepted")
	}
}
