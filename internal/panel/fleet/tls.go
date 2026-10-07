package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func parseURL(s string) ([]*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	return []*url.URL{u}, nil
}

// CAFingerprint is the lowercase hex SHA-256 of the panel CA certificate (what install commands carry).
func (f *Fleet) CAFingerprint() string { return f.ca.fingerprint }

// CACertPEM is the panel CA certificate.
func (f *Fleet) CACertPEM() string { return f.ca.pem }

func (f *Fleet) sniMatches(name string) bool {
	return strings.EqualFold(strings.TrimSuffix(name, "."), f.cfg.AgentSNI)
}

// AgentTLSConfig is the per-SNI tls.Config of the agent endpoint, for tls.Config.GetConfigForClient of
// the panel's main listener. For any other server name it returns (nil, nil): the listener then keeps its
// normal behaviour, so a prober without the secret name sees only the decoy.
//
// For the agent SNI the panel presents a server certificate from its own CA and asks for a client
// certificate without demanding one (VerifyClientCertIfGiven): the Enroll RPC has no certificate yet.
// AgentHandler enforces per RPC that everything except Enroll carries a verified, non-revoked node
// certificate; VerifyConnection already rejects revoked or retired certificates at the handshake.
func (f *Fleet) AgentTLSConfig(h *tls.ClientHelloInfo) (*tls.Config, error) {
	if !f.sniMatches(h.ServerName) {
		return nil, nil
	}
	return f.agentTLS()
}

func (f *Fleet) agentTLS() (*tls.Config, error) {
	f.tlsMu.Lock()
	defer f.tlsMu.Unlock()
	now := f.now()
	if f.tlsCfg != nil && f.tlsExp.Sub(now) > serverRenewAt {
		return f.tlsCfg, nil
	}
	cert, exp, err := f.ca.issueServer(f.cfg.AgentSNI, now)
	if err != nil {
		return nil, err
	}
	f.tlsCfg = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    f.ca.pool,
		NextProtos:   []string{"h2"}, // the Connect stream needs HTTP/2
		// Every handshake is a full one: revocation is checked each time and a long-lived stream gains
		// nothing from resumption.
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return nil // Enroll; AgentHandler requires a certificate for everything else
			}
			_, _, err := f.nodeFromCert(context.Background(), cs.PeerCertificates[0])
			return err
		},
	}
	f.tlsExp = exp
	return f.tlsCfg, nil
}

var errBadCert = errors.New("fleet: certificate not accepted")

// nodeFromCert maps a chain-verified client certificate to its node id: the URI SAN names the node, the
// serial must be one the panel issued to that node, not revoked, and the node must not be retired.
func (f *Fleet) nodeFromCert(ctx context.Context, cert *x509.Certificate) (string, peerCert, error) {
	if len(cert.URIs) != 1 {
		return "", peerCert{}, errBadCert
	}
	u := cert.URIs[0]
	id := strings.TrimPrefix(u.Path, "/")
	// Certificates issued before the product-neutral SAN (2026-09-30) say mistgate://node/<id>;
	// they renew into the new form within 20 days, so drop this legacy branch after 2026-11-30.
	legacy := u.Scheme == "mistgate" && u.Host == "node"
	if !legacy && (u.Scheme != nodeURIScheme || u.Host != nodeURIHost) || id == "" || strings.Contains(id, "/") {
		return "", peerCert{}, errBadCert
	}
	serial := cert.SerialNumber.Text(16)
	st, err := f.st.CertStatus(ctx, serial, f.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			f.log.Error("certificate lookup failed", "err", err)
		}
		return "", peerCert{}, errBadCert
	}
	if st.Revoked || st.NodeState == "retired" || st.NodeID != id {
		return "", peerCert{}, errBadCert
	}
	return id, peerCert{serial: serial, notAfter: cert.NotAfter}, nil
}

// recheckCertAt reports why a stream's client certificate is no longer acceptable, nil if it still is.
func (f *Fleet) recheckCertAt(ctx context.Context, pc peerCert, now time.Time) error {
	if pc.serial == "" {
		return nil // not from the agent middleware (tests calling the handler directly)
	}
	if !now.Before(pc.notAfter) {
		return errors.New("client certificate expired")
	}
	st, err := f.st.CertStatus(ctx, pc.serial, now)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New("client certificate unknown")
	case err != nil:
		f.log.Warn("certificate recheck failed", "err", err)
		return nil // a database hiccup must not cut every node; the next tick retries
	case st.Revoked || st.NodeState == "retired":
		return errors.New("client certificate revoked")
	}
	return nil
}

type (
	nodeKey struct{}
	certKey struct{}
)

// peerCert identifies the client certificate a request arrived with, for the stream's periodic recheck.
type peerCert struct {
	serial   string
	notAfter time.Time
}

// nodeID returns the certificate-verified node id placed in ctx by the agent middleware.
func nodeID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(nodeKey{}).(string)
	return id, ok
}

func peerCertFrom(ctx context.Context) (peerCert, bool) {
	c, ok := ctx.Value(certKey{}).(peerCert)
	return c, ok
}

// agentMiddleware guards the agent endpoint: only the agent SNI is served, and every RPC except Enroll
// needs a node certificate that is valid, not revoked and whose node is not retired. The node id comes
// from the certificate, never from a message body.
func (f *Fleet) agentMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || !f.sniMatches(r.TLS.ServerName) {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == agentv1connect.EnrollmentServiceEnrollProcedure {
			next.ServeHTTP(w, r)
			return
		}
		if len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, pc, err := f.nodeFromCert(r.Context(), r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), nodeKey{}, id), certKey{}, pc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
