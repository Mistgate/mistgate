package main

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
)

// The point of this target: a TLS 1.3 client that offers X25519MLKEM768 gets it, and the certificate it serves
// verifies for a requested name against its own PEM.
func TestNegotiatesMLKEM(t *testing.T) {
	cert, certPEM, err := selfSigned([]string{"www.example.com", "www.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serve(l, cert)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("the certificate PEM does not parse")
	}
	conn, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{
		ServerName: "www.example.net", RootCAs: pool, MinVersion: tls.VersionTLS13,
		CurvePreferences: []tls.CurveID{tls.X25519MLKEM768, tls.X25519},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st := conn.ConnectionState()
	if st.Version != tls.VersionTLS13 || st.CurveID != tls.X25519MLKEM768 {
		t.Fatalf("version %#x, curve %v: want TLS 1.3 with X25519MLKEM768", st.Version, st.CurveID)
	}
}
