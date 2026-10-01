package hysteria2

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/apernet/hysteria/extras/v2/correctnet"
	"golang.org/x/crypto/acme"
)

// tcpMasq is the HTTPS half of the masquerade: a web server on TCP (normally 443) next to the QUIC port, serving
// the same handler and advertising HTTP/3 on the QUIC port via Alt-Svc, like the real sites it imitates.
//
// It replaces extras/v2/masq.MasqTCPServer.ListenAndServeHTTPS, which has no way to stop: its listener lives in a
// local variable, so an inbound restart could never rebind the port. Behaviour is the same (HTTPS on TCP,
// Alt-Svc for the QUIC port). Its plain-HTTP half is not used: :80 belongs to certs (ACME HTTP-01).
type tcpMasq struct {
	srv *http.Server
}

func startTCPMasq(port int, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), h http.Handler, quicPort uint16) (*tcpMasq, error) {
	ln, err := correctnet.Listen("tcp", net.JoinHostPort(bindHost(), strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	altSvc := fmt.Sprintf(`h3=":%d"; ma=2592000`, quicPort)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Alt-Svc", altSvc)
			h.ServeHTTP(w, r)
		}),
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: getCert,
			// acme-tls/1 lets this listener answer TLS-ALPN-01 (the cert source's GetCertificate does the work).
			NextProtos: []string{"h2", "http/1.1", acme.ALPNProto},
		},
		// The listener faces the open internet: bound every phase, keep scanner noise out of the logs.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go srv.ServeTLS(ln, "", "")
	return &tcpMasq{srv: srv}, nil
}

func (m *tcpMasq) Close() { m.srv.Close() }
