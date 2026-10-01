// Package certs is the node's certificate source (engine.CertSource): ACME for a domain and a persisted
// self-signed certificate whose SHA-256 pin the panel puts into subscription URIs.
//
// acme_domain uses golang.org/x/crypto/acme/autocert, one Manager per server name, cache in the state
// dir. The HostPolicy accepts exactly that name. TLS-ALPN-01 is answered by whatever TLS listener asks
// Cert.GetCertificate (the engine's own TCP 443 listener, see hysteria2), so that listener must be
// reachable on 443. HTTP-01 is answered on :80 when it is free; otherwise only TLS-ALPN-01 is offered.
// acme_ip is not supported yet.
package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	selfSignedLifetime = 10 * 365 * 24 * time.Hour
	selfSignedRenewAt  = 30 * 24 * time.Hour // regenerate (new pin!) when less than this is left
)

// Source implements engine.CertSource. Create it with New; the exported fields may be set before the
// first Acquire.
type Source struct {
	// DirectoryURL is the ACME directory; empty means Let's Encrypt production (use staging first).
	DirectoryURL string
	// HTTPAddr is where HTTP-01 is served; default ":80".
	HTTPAddr string
	Log      *slog.Logger

	dir string

	mu   sync.Mutex
	acme map[string]*acmeEntry // by server name
	byID map[string]string     // inbound id -> acme server name
	http *httpServer
}

var _ engine.CertSource = (*Source)(nil)

// New keeps certificates and ACME state under stateDir (created 0700).
func New(stateDir string) *Source {
	return &Source{dir: stateDir, HTTPAddr: ":80", acme: map[string]*acmeEntry{}, byID: map[string]string{}}
}

func (s *Source) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Acquire returns the live certificate source for an inbound. Self-signed certificates are created
// on first use and reused afterwards (same pin). ACME certificates are fetched lazily on the first
// handshake and in the background right after Acquire; Info is zero until one exists.
func (s *Source) Acquire(ctx context.Context, inboundID string, t plugin.TLS) (engine.Cert, error) {
	switch t.Mode {
	case plugin.TLSSelfSigned:
		return s.selfSigned(t.ServerName)
	case plugin.TLSAcmeDomain:
		return s.acquireACME(inboundID, t.ServerName)
	case plugin.TLSAcmeIP:
		return engine.Cert{}, errors.New("certs: tls mode acme_ip is not supported yet")
	default:
		return engine.Cert{}, fmt.Errorf("certs: unknown tls mode %d", t.Mode)
	}
}

// Release forgets the inbound's ACME claim; the last release of a name stops its renewals and, when no
// ACME name is left, the :80 listener. Self-signed files stay.
func (s *Source) Release(inboundID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked(inboundID)
}

func (s *Source) releaseLocked(inboundID string) {
	name, ok := s.byID[inboundID]
	if !ok {
		return
	}
	delete(s.byID, inboundID)
	e := s.acme[name]
	if e == nil {
		return
	}
	if e.refs--; e.refs > 0 {
		return
	}
	e.cancel()
	delete(s.acme, name)
	if len(s.acme) == 0 && s.http != nil {
		s.http.close()
		s.http = nil
	}
}

// ---- self-signed ----

func (s *Source) selfSigned(name string) (engine.Cert, error) {
	if name == "" {
		return engine.Cert{}, errors.New("certs: self_signed needs a server name")
	}
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(s.dir, "selfsigned-"+hex.EncodeToString(sum[:8])+".pem")

	s.mu.Lock()
	defer s.mu.Unlock() // also serialises concurrent creation of the same file
	cert, err := loadSelfSigned(path, name)
	if err != nil {
		if cert, err = createSelfSigned(path, name); err != nil {
			return engine.Cert{}, fmt.Errorf("certs: self-signed certificate for %q: %w", name, err)
		}
	}
	info := infoOf(cert.Leaf)
	return engine.Cert{
		// The pinned client verifies the pin, not the name, so SNI is ignored on purpose.
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil },
		Info:           func() engine.CertInfo { return info },
	}, nil
}

func loadSelfSigned(path, name string) (*tls.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := tls.X509KeyPair(b, b) // one file holds both PEM blocks
	if err != nil {
		return nil, err
	}
	if c.Leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
		return nil, err
	}
	if time.Until(c.Leaf.NotAfter) < selfSignedRenewAt || c.Leaf.VerifyHostname(name) != nil {
		return nil, errors.New("stale")
	}
	return &c, nil
}

func createSelfSigned(path, name string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(name); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{name}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	pemBytes := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	if err := writeFile0600(path, pemBytes); err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// writeFile0600 replaces path atomically so a crash never leaves half a key behind (CreateTemp is 0600).
func writeFile0600(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func infoOf(leaf *x509.Certificate) engine.CertInfo {
	if leaf == nil {
		return engine.CertInfo{}
	}
	sum := sha256.Sum256(leaf.Raw)
	return engine.CertInfo{PinSHA256: hex.EncodeToString(sum[:]), NotAfter: leaf.NotAfter}
}

// ---- ACME ----

type acmeEntry struct {
	name   string
	mgr    *autocert.Manager
	refs   int
	cancel context.CancelFunc
	leaf   atomic.Pointer[x509.Certificate] // last certificate handed to a normal handshake
	httpH  http.Handler                     // HTTP-01 handler, nil when :80 was not free
}

func (s *Source) acquireACME(inboundID, name string) (engine.Cert, error) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" || net.ParseIP(name) != nil || !strings.Contains(name, ".") {
		return engine.Cert{}, fmt.Errorf("certs: acme_domain needs a DNS name with a dot, got %q", name)
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "acme"), 0o700); err != nil {
		return engine.Cert{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byID[inboundID]; ok && old != name {
		s.releaseLocked(inboundID) // the inbound's server name changed
	}
	e := s.acme[name]
	if e == nil {
		e = s.newEntry(name)
		s.acme[name] = e
	}
	if _, ok := s.byID[inboundID]; !ok {
		s.byID[inboundID] = name
		e.refs++
	}
	return engine.Cert{
		GetCertificate: e.getCertificate,
		Info:           func() engine.CertInfo { return infoOf(e.leaf.Load()) },
	}, nil
}

// newEntry is called with s.mu held.
func (s *Source) newEntry(name string) *acmeEntry {
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(filepath.Join(s.dir, "acme")),
		HostPolicy: autocert.HostWhitelist(name),
	}
	if s.DirectoryURL != "" {
		m.Client = &acme.Client{DirectoryURL: s.DirectoryURL}
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &acmeEntry{name: name, mgr: m, cancel: cancel}
	if s.ensureHTTP() {
		// Non-challenge requests get the decoy 404 (identical to TCP 443), not autocert's https redirect or
		// its text/plain 400. A wrong challenge token on a known host still gets autocert's own
		// text error; only someone who already knows the exact name can see it.
		e.httpH = m.HTTPHandler(decoy.NotFound()) // also switches http-01 on for this manager
	}
	go e.warmUp(ctx, s.log())
	return e
}

// ensureHTTP starts the shared :80 listener once. If :80 is taken it is not retried until all
// ACME inbounds are released; TLS-ALPN-01 alone is enough when 443 is ours.
func (s *Source) ensureHTTP() bool {
	if s.http != nil {
		return true
	}
	ln, err := net.Listen("tcp", s.HTTPAddr)
	if err != nil {
		s.log().Info("certs: http-01 disabled, http port not free", "addr", s.HTTPAddr, "err", err)
		return false
	}
	s.http = serveHTTP(ln, s)
	return true
}

func (s *Source) entryFor(host string) *acmeEntry {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acme[host]
}

func (e *acmeEntry) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c, err := e.mgr.GetCertificate(hello)
	if err != nil {
		return nil, err
	}
	// A TLS-ALPN-01 answer is a throw-away challenge certificate, not the one clients get.
	if len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto {
		return c, nil
	}
	if c.Leaf != nil {
		e.leaf.Store(c.Leaf)
	} else if len(c.Certificate) > 0 {
		if l, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
			e.leaf.Store(l)
		}
	}
	return c, nil
}

// warmUp obtains the certificate now instead of at the first client handshake, and retries with
// backoff. The first try waits a little so the engine's TCP 443 listener is up for TLS-ALPN-01.
func (e *acmeEntry) warmUp(ctx context.Context, log *slog.Logger) {
	hello := &tls.ClientHelloInfo{
		ServerName:       e.name,
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		SupportedProtos:  []string{"h2"},
	}
	delay := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if _, err := e.getCertificate(hello); err == nil {
			log.Info("certs: acme certificate ready", "server_name", e.name)
			return
		} else {
			log.Warn("certs: acme certificate not obtained yet", "server_name", e.name, "err", err, "retry_in", delay*2)
		}
		if delay *= 2; delay > 10*time.Minute {
			delay = 10 * time.Minute
		}
	}
}

// ---- :80 (HTTP-01) ----

type httpServer struct {
	srv  *http.Server
	addr net.Addr
}

func serveHTTP(ln net.Listener, s *Source) *httpServer {
	notFound := decoy.NotFound()
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if e := s.entryFor(r.Host); e != nil && e.httpH != nil {
				e.httpH.ServeHTTP(w, r)
				return
			}
			notFound.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go srv.Serve(ln)
	return &httpServer{srv: srv, addr: ln.Addr()}
}

func (h *httpServer) close() { h.srv.Close() }
