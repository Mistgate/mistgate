package vless

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	xtlsreality "github.com/xtls/reality"
	"golang.org/x/crypto/acme"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// selfTarget is the wire value of reality.target that makes REALITY forward everyone who is not a client to the node's own
// site (design/vless.md section 7.5).
const selfTarget = "self"

// site is the node's own HTTPS site on an internal socket: the REALITY target of a "self" inbound. It serves the same
// handler as the Hysteria2 site (Env.Masquerade), with the certificate of the node's name from the shared certificate
// source, so a visitor of TCP 443 sees one site whichever of the two protocols holds the port. REALITY splices every
// connection that is not a client (a browser, a scanner, Let's Encrypt's TLS-ALPN-01 validation) to this listener raw, so
// the node answers all of them itself.
type site struct {
	network, addr string
	ln            net.Listener
	srv           *http.Server
	cleanup       func()
}

// startSite listens on a fresh internal socket and serves the site. It must be up before the REALITY listener starts: the
// first thing that listener does is to measure the target's post-handshake records, and a target that does not answer
// leaves empty lengths for the life of the process (design/vless.md section 7.5).
func startSite(id string, cert func(*tls.ClientHelloInfo) (*tls.Certificate, error), h http.Handler) (*site, error) {
	network, addr, cleanup, err := siteSocketFn(id)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("site listener: %w", err)
	}
	srv := &http.Server{
		Handler: h,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: cert,
			// acme-tls/1 lets the node answer TLS-ALPN-01 here (the certificate source routes it by name).
			NextProtos: []string{"h2", "http/1.1", acme.ALPNProto},
		},
		// Same bounds as the Hysteria2 site: the listener faces whatever REALITY passes on, scanners included.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go srv.ServeTLS(ln, "", "")
	return &site{network: network, addr: addr, ln: ln, srv: srv, cleanup: cleanup}, nil
}

// Close frees the socket at once, even right after start (before the ServeTLS goroutine has run).
func (s *site) Close() {
	_ = s.srv.Close()
	_ = s.ln.Close()
	s.cleanup()
}

// siteSocket picks the internal address of an inbound's site: an abstract unix socket on Linux (nothing on disk, nothing to
// clean up, not reachable from another network namespace), a socket file in a temp directory elsewhere (the tests of
// non-Linux developers).
func siteSocket(id string) (network, addr string, cleanup func(), err error) {
	return internalSocket(id, "site")
}

func tlsSocket(id string) (network, addr string, cleanup func(), err error) {
	return internalSocket(id, "tls")
}

func internalSocket(id, purpose string) (network, addr string, cleanup func(), err error) {
	name := socketName(id)
	if runtime.GOOS == "linux" {
		return "unix", "@mistgate-vless-" + purpose + "-" + name, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "mgv"+purpose[:1])
	if err != nil {
		return "", "", nil, err
	}
	return "unix", filepath.Join(dir, name+".sock"), func() { _ = os.RemoveAll(dir) }, nil
}

// tlsFront terminates the public TLS connection and relays its decrypted bytes to Xray on a private Unix socket.
type tlsFront struct {
	ln          net.Listener
	unixNetwork string
	unixAddr    string
	cert        func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	mu     sync.Mutex
	conns  map[*tlsFrontConn]struct{}
	closed bool
	wg     sync.WaitGroup
}

type tlsFrontConn struct {
	client   net.Conn
	upstream net.Conn
}

func startTLSFront(id string, port uint16, cert func(*tls.ClientHelloInfo) (*tls.Certificate, error), unixNetwork, unixAddr string) (*tlsFront, error) {
	host := ""
	if BindIP != nil {
		host = BindIP.String()
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return nil, fmt.Errorf("TLS front for %q: %w", id, err)
	}
	front := &tlsFront{
		ln: ln, unixNetwork: unixNetwork, unixAddr: unixAddr, cert: cert,
		conns: make(map[*tlsFrontConn]struct{}),
	}
	front.wg.Add(1)
	go front.accept()
	return front, nil
}

func (f *tlsFront) accept() {
	defer f.wg.Done()
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		state := &tlsFrontConn{client: conn}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			_ = conn.Close()
			continue
		}
		f.conns[state] = struct{}{}
		f.wg.Add(1)
		f.mu.Unlock()
		go f.serve(state)
	}
}

func (f *tlsFront) serve(state *tlsFrontConn) {
	defer f.wg.Done()
	defer func() {
		f.mu.Lock()
		delete(f.conns, state)
		f.mu.Unlock()
		_ = state.client.Close()
		if state.upstream != nil {
			_ = state.upstream.Close()
		}
	}()

	client := tls.Server(state.client, &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: f.cert,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
	})
	_ = state.client.SetDeadline(time.Now().Add(10 * time.Second))
	if err := client.Handshake(); err != nil {
		return
	}
	_ = state.client.SetDeadline(time.Time{})
	if client.ConnectionState().NegotiatedProtocol == acme.ALPNProto {
		return
	}
	upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial(f.unixNetwork, f.unixAddr)
	if err != nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		_ = upstream.Close()
		return
	}
	state.upstream = upstream
	f.mu.Unlock()

	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		finished <- struct{}{}
	}()
	<-finished
	_ = state.client.Close()
	_ = upstream.Close()
	<-finished
}

func (f *tlsFront) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	conns := make([]*tlsFrontConn, 0, len(f.conns))
	for conn := range f.conns {
		conns = append(conns, conn)
	}
	f.mu.Unlock()
	_ = f.ln.Close()
	for _, conn := range conns {
		_ = conn.client.Close()
		if conn.upstream != nil {
			_ = conn.upstream.Close()
		}
	}
	f.wg.Wait()
}

// socketName is the inbound id when it is short and plain, else a hash of it: an abstract name has 107 bytes at most.
func socketName(id string) string {
	plain := id != "" && len(id) <= 48
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			plain = false
		}
	}
	if plain {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:12])
}

// siteCertificate is what the site presents. A TLS-ALPN-01 hello and, once a certificate has been issued for the name, every
// hello go to the ACME entry (autocert returns the cached certificate and renews it in the background). Before the first
// issuance a persisted self-signed certificate of the same name serves instead: autocert blocks a first handshake for
// the whole issuance, and that wait would hold up every visitor.
//
// "Issued" is "Info reports a certificate": the leaf last handed out. It stays set after the leaf's expiry, on purpose: with
// no visitor in months the reported leaf is old while the cache already holds the renewed one, and only the ACME entry
// reads it.
func siteCertificate(acmeCert, fallback engine.Cert) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto {
			return acmeCert.GetCertificate(hello)
		}
		if acmeCert.Info != nil && !acmeCert.Info().NotAfter.IsZero() {
			return acmeCert.GetCertificate(hello)
		}
		return fallback.GetCertificate(hello)
	}
}

// startSelfSite acquires the certificates of the inbound's name and starts the site. The returned Cert is the ACME one: its
// Info is what Health reports (zero until issued). The self-signed fallback is not reported, or a 10-year certificate
// would read as a healthy one before Let's Encrypt has answered.
func (in *inbound) startSelfSite(ctx context.Context) (*site, engine.Cert, error) {
	certs := in.e.env.Certs
	if certs == nil {
		return nil, engine.Cert{}, errors.New("a REALITY target of this node's own site needs a certificate source")
	}
	tlsSpec := in.spec.TLS
	acmeCert, err := certs.Acquire(ctx, in.spec.ID, tlsSpec)
	if err != nil {
		return nil, engine.Cert{}, err
	}
	in.certHeld = true
	fallback, err := certs.Acquire(ctx, in.spec.ID, plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: tlsSpec.ServerName})
	if err != nil {
		return nil, engine.Cert{}, err
	}
	var h http.Handler
	if in.e.env.Masquerade != nil {
		h = in.e.env.Masquerade(in.spec.ID)
	}
	if h == nil {
		h = decoy.Handler()
	}
	s, err := startSite(in.spec.ID, siteCertificate(acmeCert, fallback), h)
	if err != nil {
		return nil, engine.Cert{}, err
	}
	return s, acmeCert, nil
}

// siteSocketFn is a seam for the tests (an address that cannot be listened on).
var siteSocketFn = siteSocket

// measureSiteRecords learns the site's post-handshake record lengths and stores them where REALITY looks them up, over
// whatever is stored there.
//
// REALITY measures its target itself when the listener starts, but with a client that verifies the certificate against the
// system roots, so it can never measure the self-signed certificate the site shows before Let's Encrypt has issued one: it
// stores "no records" for the life of the process, and clients then lack the session tickets a real site sends. This
// measurement does not verify (the socket is our own). The lengths do not depend on the certificate, so one measurement at
// start is enough and nothing is measured again after the first issuance (TestSiteRecordLengthsDoNotDependOnTheCertificate).
// It is the same exchange as REALITY's, with the same client fingerprints, run once per ALPN class: none, http/1.1, h2.
// The old keys are overwritten, never deleted: a deleted key would send every authenticated handshake to sleep 5 s.
func measureSiteRecords(ctx context.Context, s *site, serverName string) {
	var wg sync.WaitGroup
	for alpn := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			measureSiteRecordsOnce(ctx, s, serverName, alpn)
		}()
	}
	wg.Wait()
}

func measureSiteRecordsOnce(ctx context.Context, s *site, serverName string, alpn int) {
	key := s.addr + " " + serverName + " " + strconv.Itoa(alpn)
	if v, ok := xtlsreality.GlobalPostHandshakeRecordsLens.Load(key); ok {
		if lens, ok := v.([]int); ok && len(lens) > 0 {
			return // measured already (an inbound restart on the same socket)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, s.network, s.addr)
	if err != nil {
		return
	}
	defer conn.Close()
	go func() { // the handshake and the read below end when the inbound stops
		<-ctx.Done()
		_ = conn.Close()
	}()
	fingerprint, protos := utls.HelloChrome_Auto, []string{"h2", "http/1.1"}
	if alpn != 2 {
		fingerprint = utls.HelloGolang
	}
	switch alpn {
	case 1:
		protos = []string{"http/1.1"}
	case 0:
		protos = nil
	}
	detect := &xtlsreality.PostHandshakeRecordDetectConn{Conn: conn, Key: key} // stores the lengths when it has read them
	client := utls.UClient(detect, &utls.Config{ServerName: serverName, NextProtos: protos, InsecureSkipVerify: true}, fingerprint)
	if client.Handshake() != nil {
		return
	}
	_, _ = io.Copy(io.Discard, client)
}
