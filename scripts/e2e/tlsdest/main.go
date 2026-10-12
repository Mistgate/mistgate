// Command tlsdest is the REALITY target of the VLESS end-to-end steps (scripts/e2e/m3.sh): a TLS 1.3 web server with a
// self-signed certificate for the given names. It keeps Go's default key exchange list, which offers X25519MLKEM768
// (xray 26.x with a chrome fingerprint fails against a target that cannot do it). It writes the certificate to -cert,
// so a test can add it to the trust file the node uses (REALITY's own measurement handshake to the target verifies the
// chain), and prints "ready <address>" once it listens.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// selfSigned makes a certificate valid for names, usable as its own trust anchor.
func selfSigned(names []string) (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: names[0]},
		DNSNames:              names,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// serve answers every request on l with a small page until l is closed. TLS 1.3 only; CurvePreferences stays nil.
func serve(l net.Listener, cert tls.Certificate) error {
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>Welcome</title><p>It works.</p>\n")
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
		ErrorLog:  log.New(io.Discard, "", 0),
	}
	return srv.ServeTLS(l, "", "")
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to listen on")
	names := flag.String("names", "www.example.com", "comma-separated host names the certificate is valid for")
	certOut := flag.String("cert", "", "file to write the certificate to (PEM)")
	flag.Parse()
	cert, certPEM, err := selfSigned(strings.Split(*names, ","))
	if err != nil {
		log.Fatal(err)
	}
	if *certOut != "" {
		if err := os.WriteFile(*certOut, certPEM, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("ready", l.Addr())
	log.Fatal(serve(l, cert))
}
