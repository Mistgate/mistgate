package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// A ClientHello cut into single bytes, and one cut into three TLS records, must still be a hello that a plain Go TLS server completes
// a handshake with: the stand's "hello in pieces" probes are only worth anything if the pieces are valid.
func TestSplitHelloIsStillAHello(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pieces.example.com"}, DNSNames: []string{"pieces.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pieces.example.com"}}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _ = c.(*tls.Conn).Handshake() }()
		}
	}()
	for _, mode := range []string{"byte1", "records3"} {
		conn, err := dial(ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		tc, lines := handshake(&splitConn{Conn: conn, mode: mode}, "pieces.example.com", []string{"h2", "http/1.1"}, mode)
		if tc == nil {
			t.Fatalf("%s: %s", mode, lines[0])
		}
		if len(lines) != 2 || !strings.Contains(lines[1], `"pieces.example.com"`) || !strings.Contains(lines[1], "ecdsa P-256") {
			t.Fatalf("%s: the certificate line is %v", mode, lines)
		}
		_ = tc.Close()
		_ = conn.Close()
	}
}

// What an address-bearing error says must not differ between two runs of the same behaviour.
func TestCleanDropsAddresses(t *testing.T) {
	got := clean(errors.New("read tcp 203.0.113.50:40000->203.0.113.10:443: read: connection reset by peer"))
	if strings.Contains(got, "203.0.113") || !strings.Contains(got, "connection reset by peer") {
		t.Fatalf("clean left %q", got)
	}
}

// The first line of an answer is what the probes print of it; a long one is cut.
func TestFirstLine(t *testing.T) {
	if got := firstLine([]byte("HTTP/1.1 400 Bad Request\r\nx: y\r\n")); got != "HTTP/1.1 400 Bad Request\r" {
		t.Fatalf("firstLine = %q", got)
	}
}
