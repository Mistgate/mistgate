package httpserver

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// keyPair is a certificate loaded from files that is reloaded when the certificate file
// changes (certbot renewals and the like), checked at most every recheck.
type keyPair struct {
	certFile, keyFile string
	log               *slog.Logger

	mu      sync.Mutex
	cert    *tls.Certificate
	mtime   time.Time
	checked time.Time
}

const recheck = 30 * time.Second

func newKeyPair(certFile, keyFile string, log *slog.Logger) (*keyPair, error) {
	k := &keyPair{certFile: certFile, keyFile: keyFile, log: log}
	if err := k.load(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *keyPair) load() error {
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		return err
	}
	fi, err := os.Stat(k.certFile)
	if err != nil {
		return err
	}
	k.cert, k.mtime, k.checked = &cert, fi.ModTime(), time.Now()
	return nil
}

func (k *keyPair) get() *tls.Certificate {
	k.mu.Lock()
	defer k.mu.Unlock()
	if time.Since(k.checked) > recheck {
		k.checked = time.Now()
		if fi, err := os.Stat(k.certFile); err == nil && !fi.ModTime().Equal(k.mtime) {
			if err := k.load(); err != nil { // keep serving the old one; the new pair may be half-written
				k.log.Warn("reload tls certificate", "err", err)
			} else {
				k.log.Info("reloaded tls certificate", "file", k.certFile)
			}
		}
	}
	return k.cert
}

// publicTLS builds the TLS configuration of the main listener: the static certificate
// (--tls-cert) and/or Let's Encrypt certificates for the ACME domains, issued through
// TLS-ALPN-01 on this very listener. With both, the ACME domains use ACME and every
// other name the static pair. The agent SNI, when hooks are configured, is handed to
// the fleet's TLS config (mutual TLS) before any of that. It returns the autocert
// manager too (nil without ACME) for the HTTP-01 / redirect listener.
func (s *Server) publicTLS(o ServeOptions) (*tls.Config, *autocert.Manager, error) {
	if o.TLSCert == "" && len(o.ACMEDomains) == 0 {
		return nil, nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	var static *keyPair
	if o.TLSCert != "" {
		var err error
		if static, err = newKeyPair(o.TLSCert, o.TLSKey, s.cfg.Log); err != nil {
			return nil, nil, fmt.Errorf("tls certificate: %w", err)
		}
	}
	var mgr *autocert.Manager
	acmeNames := map[string]bool{}
	if len(o.ACMEDomains) > 0 {
		for _, d := range o.ACMEDomains {
			d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
			if d == "" || !strings.Contains(d, ".") || strings.ContainsAny(d, "*/: ") {
				return nil, nil, fmt.Errorf("acme domain %q: need a plain host name like example.com", d)
			}
			acmeNames[d] = true
		}
		names := make([]string, 0, len(acmeNames))
		for d := range acmeNames {
			names = append(names, d)
		}
		mgr = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(names...),
			Cache:      autocert.DirCache(o.ACMEDir),
			Email:      o.ACMEEmail,
		}
		cfg.NextProtos = append(cfg.NextProtos, acme.ALPNProto)
	}
	cfg.GetCertificate = func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		useACME := mgr != nil && (static == nil || acmeNames[normSNI(h.ServerName)] || wantsACMEChallenge(h))
		switch {
		case useACME:
			return mgr.GetCertificate(h)
		case static != nil:
			return static.get(), nil
		}
		return nil, errors.New("no certificate")
	}
	cfg.GetConfigForClient = s.clientHello
	return cfg, mgr, nil
}

func wantsACMEChallenge(h *tls.ClientHelloInfo) bool {
	for _, p := range h.SupportedProtos {
		if p == acme.ALPNProto {
			return true
		}
	}
	return false
}

func normSNI(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }

func (s *Server) agentHooks() bool {
	return s.agentSNI != "" && s.cfg.AgentTLS != nil && s.cfg.AgentHandler != nil
}

// clientHello is tls.Config.GetConfigForClient of the main listener: connections for
// the agent SNI get the fleet's configuration (its own server certificate, client
// certificates), all others the listener's default (nil, nil).
func (s *Server) clientHello(h *tls.ClientHelloInfo) (*tls.Config, error) {
	if !s.agentHooks() || !ctEqual(normSNI(h.ServerName), s.agentSNI) {
		return nil, nil
	}
	return s.agentConfig(h)
}

// agentConfig asks the fleet for the TLS config of one agent connection. HTTP/2 is
// added to its protocols if it did not list any: the agent API streams.
func (s *Server) agentConfig(h *tls.ClientHelloInfo) (*tls.Config, error) {
	c, err := s.cfg.AgentTLS(h)
	if err != nil {
		return nil, err
	}
	if c == nil { // crypto/tls would fall back to the public configuration: no client certificate, wrong server certificate
		return nil, errors.New("no agent TLS configuration")
	}
	c = c.Clone()
	if len(c.NextProtos) == 0 {
		c.NextProtos = []string{"h2", "http/1.1"}
	}
	return c, nil
}

// acmeHTTP is the handler of the port-80 listener: ACME HTTP-01 challenges for the
// configured domains, and a permanent redirect to https for everything else on those
// domains. Other hosts and non-canonical paths get the decoy's 404, like on the main
// listener. httpsPort is "" or "443" for the default port.
func (s *Server) acmeHTTP(mgr *autocert.Manager, domains []string, httpsPort string) http.Handler {
	known := map[string]bool{}
	for _, d := range domains {
		known[normSNI(d)] = true
	}
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if !known[host] || !canonicalPath(r) {
			s.decoy.notFound(w, r)
			return
		}
		target := "https://" + host
		if httpsPort != "" && httpsPort != "443" {
			target += ":" + httpsPort
		}
		w.Header().Set("Location", target+r.URL.RequestURI())
		w.WriteHeader(http.StatusPermanentRedirect)
	})
	return mgr.HTTPHandler(redirect)
}
