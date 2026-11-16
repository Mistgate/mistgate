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

func TestPeekHelloThenHandshake(t *testing.T) {
	cert, pool := selfSigned(t, "sub.example.com")
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		tc := tls.Client(client, &tls.Config{ServerName: "sub.example.com", RootCAs: pool, NextProtos: []string{TunnelALPN("s"), "h2"}})
		if err := tc.Handshake(); err != nil {
			done <- err
			return
		}
		_, err := tc.Write([]byte("ping"))
		done <- err
	}()
	hello, replay, err := PeekHello(server, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if hello.ServerName != "sub.example.com" || !hello.Offers(TunnelALPN("s")) || hello.Offers(TunnelALPN("other")) {
		t.Fatalf("hello = %+v", hello)
	}
	ts := tls.Server(replay, &tls.Config{Certificates: []tls.Certificate{cert}})
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
	if hello.ServerName != "" || len(hello.ALPN) != 0 {
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

// The panel's HTTP server closes its TLS connection on a stream after the tunnel died and the stream's handler returned:
// the close-notify sets a write deadline, which must not reach the finished HTTP/2 response writer (it panics there).
func TestDeadlineAfterTunnelDeathIsHarmless(t *testing.T) {
	cert, _ := selfSigned(t, "sub.example.com")
	accepted := make(chan net.Conn, 1)
	a, b := net.Pipe()
	served := make(chan struct{})
	go func() {
		(&http2.Server{}).ServeConn(a, &http2.ServeConnOpts{Handler: ServerHandler(func(_ string, c net.Conn) bool { accepted <- c; return true })})
		close(served)
	}()
	cc, err := (&http2.Transport{}).NewClientConn(b)
	if err != nil {
		t.Fatal(err)
	}
	node, err := OpenStream(context.Background(), cc, "tunnel.example.com", KindTLS, netip.MustParseAddrPort("203.0.113.7:51234"))
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	panel := tls.Server(<-accepted, &tls.Config{Certificates: []tls.Certificate{cert}})
	_ = a.Close() // the tunnel dies: every handler returns
	_ = b.Close()
	<-served
	// The handler returns a moment after the connection is gone; from then on the response writer is finished.
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		_ = panel.SetWriteDeadline(time.Now().Add(time.Second))
	}
	_ = panel.Close()
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
