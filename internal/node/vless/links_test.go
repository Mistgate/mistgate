package vless

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// linkTotals adds up the links/3 rows by "inbound/cred" until ready holds.
func linkTotals(t *testing.T, e *eng, within time.Duration, ready func(map[string]plugin.LinkStat) bool) map[string]plugin.LinkStat {
	t.Helper()
	got := map[string]plugin.LinkStat{}
	deadline := time.Now().Add(within)
	for {
		collected, err := e.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range collected.Links {
			k := s.InboundID + "/" + s.CredID
			sum := got[k]
			sum.Connects += s.Connects
			sum.UnknownKeys += s.UnknownKeys
			sum.Hellos += s.Hellos
			sum.HellosForeign += s.HellosForeign
			sum.RealityFallback += s.RealityFallback
			sum.AuthFailed += s.AuthFailed
			sum.HsUnfinished += s.HsUnfinished
			sum.TLSAlertsSent += s.TLSAlertsSent
			sum.TLSAlertsRecv += s.TLSAlertsRecv
			sum.TLSCertRefused += s.TLSCertRefused
			sum.TCPStalls += s.TCPStalls
			sum.TCPStalls16K += s.TCPStalls16K
			got[k] = sum
		}
		if ready(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("link counters = %+v, still not there", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tlsHello dials the inbound with a plain TLS client (no REALITY key): REALITY passes it to the target.
func tlsHello(t *testing.T, port uint16, serverName string) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))),
		&tls.Config{ServerName: serverName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatalf("TLS through REALITY to the target: %v", err)
	}
	_ = conn.Close()
}

// REALITY over RAW: a client's connection counts as a hello and a connect of its key; a hello for another name and one
// with our name but no REALITY key go to the target and say why; bytes that are no TLS at all go there too, without a hello.
func TestRealityLinkCounters(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("reality-links", "tcp")
	cred := testCredential("links-user", "66ad4540-b58c-4ad2-9926-ea63445a9b60")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	known := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, stringID(cred))
	conn := known.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("a key the node holds")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	got := linkTotals(t, f.engine, 10*time.Second, func(m map[string]plugin.LinkStat) bool {
		return m["reality-links/links-user"].Connects >= 1 && m["reality-links/"].Hellos >= 1
	})
	if c := got["reality-links/links-user"]; c.Connects != 1 {
		t.Fatalf("key row = %+v; want one connection", c)
	}
	if in := got["reality-links/"]; in.Hellos != 1 || in.RealityFallback != 0 || in.AuthFailed != 0 || in.HsUnfinished != 0 {
		t.Fatalf("inbound row after one client = %+v; want one hello and no failure", in)
	}

	tlsHello(t, spec.Listen.Port, "foreign.example.net") // another server name
	tlsHello(t, spec.Listen.Port, "localhost")           // our name, no REALITY key
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Listen.Port))), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	_ = raw.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.Copy(io.Discard, raw)
	_ = raw.Close()

	got = linkTotals(t, f.engine, 10*time.Second, func(m map[string]plugin.LinkStat) bool {
		return m["reality-links/"].RealityFallback >= 3
	})
	in := got["reality-links/"]
	if in.Hellos != 2 || in.HellosForeign != 1 || in.RealityFallback != 3 || in.AuthFailed != 1 || in.HsUnfinished != 0 {
		t.Fatalf("inbound row = %+v; want 2 hellos, 1 foreign, 3 fallbacks of which 1 failed authentication", in)
	}
	if c := got["reality-links/links-user"]; c.Connects != 0 {
		t.Fatalf("a refused connection counted as the key's: %+v", c)
	}
}

// helloOnlyProxy passes the client's first TLS record (its ClientHello) to the node and nothing after, and the node's answer
// back; it closes the node's side shortly after the hello: a path on which the client's answer never arrives.
func helloOnlyProxy(t *testing.T, nodePort uint16) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				node, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(nodePort))))
				if err != nil {
					return
				}
				defer node.Close()
				go func() { _, _ = io.Copy(client, node) }()
				var head [5]byte
				if _, err := io.ReadFull(client, head[:]); err != nil {
					return
				}
				body := make([]byte, binary.BigEndian.Uint16(head[3:]))
				if _, err := io.ReadFull(client, body); err != nil {
					return
				}
				_, _ = node.Write(append(head[:], body...))
				time.Sleep(300 * time.Millisecond)
			}()
		}
	}()
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

// A client with the right key whose Finished never reaches the node: REALITY answered, the handshake never finished.
func TestRealityHandshakeUnfinished(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("reality-unfinished", "tcp")
	cred := testCredential("cut-user", "66ad4540-b58c-4ad2-9926-ea63445a9b61")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	client := newXrayClient(t, helloOnlyProxy(t, spec.Listen.Port), "tcp", cred.CredID, stringID(cred))
	echo := startEchoServer(t)
	if conn, err := client.tryDial(echo.Addr().String()); err == nil {
		_, _ = conn.Write([]byte("never arrives"))
		_ = conn.Close()
	}
	got := linkTotals(t, f.engine, 10*time.Second, func(m map[string]plugin.LinkStat) bool {
		return m["reality-unfinished/"].HsUnfinished >= 1
	})
	if in := got["reality-unfinished/"]; in.HsUnfinished != 1 || in.Hellos != 1 || in.RealityFallback != 0 || in.AuthFailed != 0 {
		t.Fatalf("inbound row = %+v; want one hello whose handshake did not finish", in)
	}
	if c := got["reality-unfinished/cut-user"]; c.Connects != 0 {
		t.Fatalf("an unfinished handshake counted as a connection: %+v", c)
	}
}

// The close of a client's connection goes to its key's stall counters (the TCP_INFO reading itself is Linux's and has its
// own root test); a connection REALITY refused has no key and no stall.
func TestRealityStallGoesToTheKey(t *testing.T) {
	var mu sync.Mutex
	var reads int
	old := tcpStall
	tcpStall = func(net.Conn) (bool, bool) {
		mu.Lock()
		reads++
		mu.Unlock()
		return true, true
	}
	t.Cleanup(func() { tcpStall = old })

	f := newRuntimeFixture(t, false)
	spec := f.spec("reality-stall", "tcp")
	cred := testCredential("stall-user", "66ad4540-b58c-4ad2-9926-ea63445a9b62")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("then the path stops")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	tlsHello(t, spec.Listen.Port, "foreign.example.net")
	got := linkTotals(t, f.engine, 15*time.Second, func(m map[string]plugin.LinkStat) bool {
		return m["reality-stall/stall-user"].TCPStalls >= 1 && m["reality-stall/"].RealityFallback >= 1
	})
	if c := got["reality-stall/stall-user"]; c.TCPStalls != 1 || c.TCPStalls16K != 1 || c.Connects != 1 {
		t.Fatalf("key row = %+v; want one connection that stalled after the first bytes", c)
	}
	if in := got["reality-stall/"]; in.TCPStalls != 0 {
		t.Fatalf("inbound row = %+v; a refused connection has no stall", in)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Fatalf("TCP_INFO read %d times; want once, for the client's connection", reads)
	}
}

// clientHelloBytes is a real ClientHello for serverName, as a TLS client writes it.
func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	a, b := net.Pipe()
	go func() {
		_ = tls.Client(a, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
	}()
	var head [5]byte
	if _, err := io.ReadFull(b, head[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(head[3:]))
	if _, err := io.ReadFull(b, body); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	_ = b.Close()
	return append(head[:], body...)
}

// The TLS front does its own handshakes: a hello for another name, an app that refuses the certificate, an app the node
// refuses (no common ALPN), and a client that sends its hello and leaves.
func TestTLSFrontLinkCounters(t *testing.T) {
	f, certs := newTLSRuntimeFixture(t)
	certs.setIssued()
	spec := f.tlsSpec("tls-links", "xhttp", "auto")
	if _, err := f.engine.Apply(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Listen.Port)))
	dial := func(conf *tls.Config) error {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr, conf)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
	if err := dial(&tls.Config{ServerName: "foreign.example.net", InsecureSkipVerify: true, NextProtos: []string{"h2"}}); err != nil {
		t.Fatalf("hello for another name: %v", err)
	}
	if err := dial(&tls.Config{ServerName: tlsTestServerName, NextProtos: []string{"h2"}}); err == nil {
		t.Fatal("a client that verifies the test certificate trusted it")
	}
	if err := dial(&tls.Config{ServerName: tlsTestServerName, InsecureSkipVerify: true, NextProtos: []string{"not-http"}}); err == nil {
		t.Fatal("the front accepted an ALPN it does not offer")
	}
	raw, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write(clientHelloBytes(t, tlsTestServerName))
	_ = raw.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = io.Copy(io.Discard, io.LimitReader(raw, 1)) // the node's answer starts
	_ = raw.Close()

	got := linkTotals(t, f.engine, 10*time.Second, func(m map[string]plugin.LinkStat) bool {
		in := m["tls-links/"]
		return in.HsUnfinished >= 1 && in.TLSAlertsSent >= 1 && in.TLSAlertsRecv >= 1
	})
	in := got["tls-links/"]
	if in.Hellos != 3 || in.HellosForeign != 1 || in.TLSAlertsRecv != 1 || in.TLSCertRefused != 1 || in.TLSAlertsSent != 1 || in.HsUnfinished != 1 { // the refused ALPN never reaches the certificate: no hello
		t.Fatalf("front counters = %+v; want 3 hellos (one foreign), one refused certificate, one refused ALPN, one unfinished", in)
	}
	if in.RealityFallback != 0 || in.AuthFailed != 0 {
		t.Fatalf("the TLS front counted REALITY verdicts: %+v", in)
	}
}

func TestRealityVerdictLines(t *testing.T) {
	now := time.Now()
	c := &inboundCounters{}
	for i, reason := range []string{
		"server name mismatch: other.example.net",
		"authentication failed or validation criteria not met",
		"failed to read client hello",
		"unsupported TLS version: 303",
		"handshake did not complete successfully",
		"target sent incorrect server hello or handshake incomplete",
	} {
		addr := "[2001:db8::7]:4430" + strconv.Itoa(i)
		rememberClose(addr, c, now)
		if !realityVerdict("transport/internet/tcp: REALITY: processed invalid connection from "+addr+": "+reason, now) {
			t.Fatalf("%q was not taken as a REALITY verdict", reason)
		}
	}
	if c.realityFallback.Load() != 4 || c.hellosForeign.Load() != 1 || c.authFailed.Load() != 1 || c.hsUnfinished.Load() != 1 {
		t.Fatalf("verdicts = fallback %d, foreign %d, auth %d, unfinished %d", c.realityFallback.Load(), c.hellosForeign.Load(),
			c.authFailed.Load(), c.hsUnfinished.Load())
	}
	// two connections close, the newer one's verdict comes first: the older one still finds its own entry
	a, b := &inboundCounters{}, &inboundCounters{}
	rememberClose("192.0.2.1:1001", a, now)
	rememberClose("192.0.2.2:1002", b, now)
	realityVerdict("REALITY: processed invalid connection from 192.0.2.2:1002: failed to read client hello", now)
	realityVerdict("REALITY: processed invalid connection from 192.0.2.1:1001: authentication failed or validation criteria not met", now)
	if a.authFailed.Load() != 1 || b.realityFallback.Load() != 1 || b.authFailed.Load() != 0 {
		t.Fatalf("out-of-order verdicts: a auth %d, b fallback %d auth %d", a.authFailed.Load(), b.realityFallback.Load(), b.authFailed.Load())
	}
	// an address seen long ago, or never, counts nowhere; a line of another kind is not a verdict
	rememberClose("198.51.100.4:1000", c, now.Add(-time.Minute))
	realityVerdict("REALITY: processed invalid connection from 198.51.100.4:1000: authentication failed", now)
	realityVerdict("REALITY: processed invalid connection from 198.51.100.5:1000: authentication failed", now)
	if c.authFailed.Load() != 1 {
		t.Fatalf("a stale or unknown address counted: auth %d", c.authFailed.Load())
	}
	if realityVerdict("[42] proxy/vless/inbound: firstLen = 517", now) {
		t.Fatal("an ordinary Info line was taken as a verdict")
	}
}

func TestTCPStallRule(t *testing.T) {
	for _, c := range []struct {
		name                      string
		retrans                   uint32
		sent, retransBytes, acked uint64
		unacked                   uint32
		stalled, first16K         bool
	}{
		{"all acknowledged", 5, 200 << 10, 10 << 10, 190<<10 + 1, 0, false, false},
		{"few retransmissions", 2, 64 << 10, 4 << 10, 16 << 10, 3, false, false},
		{"frozen after 16 KiB", 6, 40 << 10, 20 << 10, 16 << 10, 3, true, true},
		{"stalled late", 8, 9 << 20, 1 << 20, 6 << 20, 10, true, false},
		{"cut before 8 KiB", 4, 12 << 10, 6 << 10, 3 << 10, 2, true, false},
		{"old kernel, segments in flight", 4, 0, 0, 16 << 10, 2, true, true},
		{"old kernel, nothing in flight", 4, 0, 0, 16 << 10, 0, false, false},
	} {
		stalled, first := tcpStallRule(c.retrans, c.sent, c.retransBytes, c.acked, c.unacked)
		if stalled != c.stalled || first != c.first16K {
			t.Errorf("%s: stalled %v, first16K %v; want %v, %v", c.name, stalled, first, c.stalled, c.first16K)
		}
	}
}

func TestTLSHandshakeFailureClasses(t *testing.T) {
	// The real crypto/tls error of a server whose client refuses the certificate (a TCP pair: net.Pipe has no buffer, and
	// both sides of a failing TLS 1.3 handshake write at once).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cert := selfSignedCert(t, "pipe.example.com")
	done := make(chan error, 1)
	go func() {
		server, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(5 * time.Second))
		done <- tls.Server(server, &tls.Config{Certificates: []tls.Certificate{*cert}, NextProtos: []string{"h2"}}).Handshake()
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	cerr := tls.Client(client, &tls.Config{ServerName: "pipe.example.com", NextProtos: []string{"h2"}}).Handshake()
	serr := <-done
	if cerr == nil || serr == nil {
		t.Fatalf("handshake with an untrusted certificate: client %v, server %v", cerr, serr)
	}
	if sent, recv, certificate := tlsHandshakeFailure(serr); sent || !recv || !certificate {
		t.Fatalf("server error %v: sent %v recv %v certificate %v; want a received certificate alert", serr, sent, recv, certificate)
	}
	if sent, recv, _ := tlsHandshakeFailure(io.EOF); sent || recv {
		t.Fatal("EOF classified as an alert")
	}
	if sent, recv, _ := tlsHandshakeFailure(&net.OpError{Op: "local error", Err: errors.New("x")}); sent || recv {
		t.Fatal("a local error without an alert classified as one")
	}
}
