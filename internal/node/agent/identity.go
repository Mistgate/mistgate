package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/buildinfo"
)

// apiVersion is the wire API version this agent speaks (Hello.api_version, EnrollRequest.api_version).
const apiVersion = 1

// Files in the state directory (directory 0700, files 0600). The key and the leaf certificate live in ONE
// file so that a renewal (a new key every time) is a single atomic rename.
const (
	fileIdentity   = "identity.pem"       // EC PRIVATE KEY + CERTIFICATE
	filePendingKey = "enroll-pending.pem" // private key kept only for a retried, interrupted enrollment
	fileCA         = "ca.pem"             // the panel CA; from enrollment on it is the only trust anchor
	fileMeta       = "agent.json"         // panel address, agent SNI, node id
	fileState      = "state.json"         // last applied desired state
)

// renewBefore is how long before expiry the agent renews (day 20 of 30).
const renewBefore = 10 * 24 * time.Hour

var (
	// ErrNotEnrolled is returned by New when the state directory holds no identity.
	ErrNotEnrolled = errors.New("agent is not enrolled (run `mistgate-node enroll` first)")
	// ErrRetired is returned by Run after the panel retired the node; the caller should exit 0.
	ErrRetired = errors.New("node retired by the panel")
)

// Meta is what enrollment leaves next to the key material.
type Meta struct {
	Panel  string `json:"panel"`   // host:port
	SNI    string `json:"sni"`     // secret agent SNI
	NodeID string `json:"node_id"` // informational; the certificate is authoritative
	// Link is the wss://host/<secret-prefix>/ the agent enrolled through. Set = link-only: the agent never dials mTLS
	// (Panel and SNI are empty), renews over its link session and lists no self-update.
	Link string `json:"link,omitempty"`
}

type identity struct {
	meta   Meta
	cert   tls.Certificate // Leaf is set
	ca     *x509.CertPool
	caCert *x509.Certificate
}

func (id *identity) notAfter() time.Time { return id.cert.Leaf.NotAfter }

// EnrollConfig is the input of the `enroll` command.
type EnrollConfig struct {
	StateDir         string
	Panel            string // host:port
	SNI              string
	CASHA256         string // hex fingerprint of the panel CA certificate (colons and case ignored)
	Token            string
	Version          string // defaults to buildinfo.Version
	Force            bool   // overwrite an existing identity
	ResumePendingKey bool   // persist and reuse the CSR key if enrollment is interrupted
	Timeout          time.Duration
	// LinkURL (wss://host/<secret-prefix>/) enrols over the panel's public link route instead of the pinned mTLS
	// endpoint: system-root TLS carries the token, the CA pin then vouches for the panel. Exclusive with Panel and SNI.
	LinkURL string

	httpClient *http.Client // test seam for LinkURL; nil = system roots
}

// Enroll generates a P-256 key and a CSR, exchanges the one-time token for a node certificate over TLS
// that trusts ONLY a CA with the given fingerprint, and stores key, certificate, CA and panel address.
func Enroll(ctx context.Context, cfg EnrollConfig) (Meta, error) {
	pin, err := parsePin(cfg.CASHA256)
	if err != nil {
		return Meta{}, err
	}
	var enrollBase string // the Connect base URL; the client is chosen below
	if cfg.LinkURL != "" {
		if cfg.Panel != "" || cfg.SNI != "" {
			return Meta{}, errors.New("--link-url cannot be combined with --panel or --sni")
		}
		u, err := parseLinkBase(cfg.LinkURL)
		if err != nil {
			return Meta{}, fmt.Errorf("--link-url: %w", err)
		}
		enrollBase = "https://" + u.Host + strings.TrimRight(u.Path, "/")
		if cfg.Token == "" || cfg.StateDir == "" {
			return Meta{}, errors.New("token and state dir are required")
		}
	} else {
		if _, _, err := net.SplitHostPort(cfg.Panel); err != nil {
			return Meta{}, fmt.Errorf("--panel must be host:port: %w", err)
		}
		if cfg.SNI == "" || cfg.Token == "" || cfg.StateDir == "" {
			return Meta{}, errors.New("panel SNI, token and state dir are required")
		}
		enrollBase = "https://" + cfg.Panel
	}
	if cfg.Version == "" {
		cfg.Version = buildinfo.Version
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if !cfg.Force {
		if _, err := os.Stat(filepath.Join(cfg.StateDir, fileIdentity)); err == nil {
			return Meta{}, errors.New("already enrolled; use --force to replace the identity")
		}
	}
	var key *ecdsa.PrivateKey
	var csr []byte
	if cfg.ResumePendingKey {
		key, csr, err = pendingKeyAndCSR(cfg.StateDir, cfg.Force)
	} else {
		key, csr, err = newKeyAndCSR()
	}
	if err != nil {
		return Meta{}, err
	}

	var client *http.Client
	if cfg.LinkURL != "" {
		client = cfg.httpClient
		if client == nil {
			client = newHTTPClient(&tls.Config{MinVersion: tls.VersionTLS12}, cfg.Timeout, nil)
		}
		// The token is in the body: never follow a redirect with it.
		c := *client
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &c
	} else {
		client = newHTTPClient(pinnedTLS(cfg.SNI, pin), cfg.Timeout, nil)
	}
	defer client.CloseIdleConnections()
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	resp, err := agentv1connect.NewEnrollmentServiceClient(client, enrollBase, connect.WithReadMaxBytes(1<<20)).Enroll(cctx,
		connect.NewRequest(&pb.EnrollRequest{
			EnrollmentToken: cfg.Token, CsrDer: csr, AgentVersion: cfg.Version, ApiVersion: apiVersion,
		}))
	if err != nil {
		var urlErr *url.Error
		if cfg.LinkURL != "" && errors.As(err, &urlErr) {
			err = urlErr.Err // the URL holds the secret path prefix: it must not reach a terminal or a log
		}
		return Meta{}, fmt.Errorf("enroll: %w", err)
	}
	m := resp.Msg

	// The TLS chain already ended in a CA with the pinned fingerprint; require the CA we are about to
	// store to be that very CA, and the leaf to be ours and to chain to it.
	caCert, err := parseOneCert([]byte(m.CaCertificatePem))
	if err != nil {
		return Meta{}, fmt.Errorf("panel CA certificate: %w", err)
	}
	if sum := sha256.Sum256(caCert.Raw); subtle.ConstantTimeCompare(sum[:], pin[:]) != 1 {
		return Meta{}, errors.New("the CA returned by the panel does not match --ca-sha256 (the enrolment token is used up: create a new enrolment token)")
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := checkLeaf([]byte(m.CertificatePem), key, pool); err != nil {
		return Meta{}, fmt.Errorf("issued certificate: %w", err)
	}

	meta := Meta{Panel: cfg.Panel, SNI: cfg.SNI, NodeID: m.NodeId, Link: cfg.LinkURL}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return Meta{}, err
	}
	_ = os.Chmod(cfg.StateDir, 0o700) // the directory may pre-exist with looser bits
	metaJSON, _ := json.Marshal(meta)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{fileCA, []byte(m.CaCertificatePem)},
		{fileMeta, metaJSON},
		{fileIdentity, identityPEM(key, m.CertificatePem)}, // last: its presence means "enrolled"
	} {
		if err := writeFileAtomic(filepath.Join(cfg.StateDir, f.name), f.data, 0o600); err != nil {
			return Meta{}, err
		}
	}
	if cfg.ResumePendingKey {
		if err := os.Remove(filepath.Join(cfg.StateDir, filePendingKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Meta{}, err
		}
	}
	return meta, nil
}

// IsEnrolled reports whether dir holds an identity (enroll writes identity.pem last).
func IsEnrolled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, fileIdentity))
	return err == nil
}

func parsePin(s string) ([32]byte, error) {
	var pin [32]byte
	b, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), ":", ""))
	if err != nil || len(b) != 32 {
		return pin, errors.New("--ca-sha256 must be 64 hex digits (SHA-256 of the CA certificate)")
	}
	copy(pin[:], b)
	return pin, nil
}

// pinnedTLS accepts a server chain only if the CA in it has the pinned SHA-256 fingerprint and the chain
// verifies to that CA for the agent SNI. Used for enrollment, before the agent owns any CA file.
func pinnedTLS(sni string, pin [32]byte) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: sni,
		NextProtos: []string{"h2"},
		// Standard verification is replaced by VerifyPeerCertificate below, which is stricter: it trusts
		// nothing but the pinned CA. The default verifier cannot express a fingerprint pin.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, len(raw))
			for i, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			if len(certs) < 2 {
				return errors.New("panel did not present its CA certificate")
			}
			roots, inter := x509.NewCertPool(), x509.NewCertPool()
			pinned := false
			for _, c := range certs[1:] {
				if sum := sha256.Sum256(c.Raw); subtle.ConstantTimeCompare(sum[:], pin[:]) == 1 {
					roots.AddCert(c)
					pinned = true
				} else {
					inter.AddCert(c)
				}
			}
			if !pinned {
				return errors.New("no certificate in the chain matches the pinned CA fingerprint")
			}
			_, err := certs[0].Verify(x509.VerifyOptions{
				Roots: roots, Intermediates: inter, DNSName: sni, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}
}

func newKeyAndCSR() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csr, err := csrForKey(key)
	return key, csr, err
}

func csrForKey(key *ecdsa.PrivateKey) ([]byte, error) {
	// The panel ignores the subject and sets the URI SAN itself; the CN is only a label.
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "mistgate-node"}, SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, key)
}

func pendingKeyAndCSR(dir string, force bool) (*ecdsa.PrivateKey, []byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, filePendingKey)
	if !force {
		if raw, err := os.ReadFile(path); err == nil {
			block, _ := pem.Decode(raw)
			if block == nil || block.Type != "EC PRIVATE KEY" {
				return nil, nil, errors.New("pending enrollment key is invalid")
			}
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, nil, errors.New("pending enrollment key is invalid")
			}
			csr, err := csrForKey(key)
			return key, csr, err
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
	}
	key, csr, err := newKeyAndCSR()
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	var raw bytes.Buffer
	if err := pem.Encode(&raw, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}); err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(path, raw.Bytes(), 0o600); err != nil {
		return nil, nil, err
	}
	return key, csr, nil
}

func parseOneCert(p []byte) (*x509.Certificate, error) {
	blk, _ := pem.Decode(p)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(blk.Bytes)
}

// checkLeaf verifies that certPEM is a client certificate for key that chains to roots.
func checkLeaf(certPEM []byte, key *ecdsa.PrivateKey, roots *x509.CertPool) (*x509.Certificate, error) {
	leaf, err := parseOneCert(certPEM)
	if err != nil {
		return nil, err
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("certificate does not belong to our key")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, err
	}
	return leaf, nil
}

func identityPEM(key *ecdsa.PrivateKey, certPEM string) []byte {
	der, _ := x509.MarshalECPrivateKey(key)
	var b bytes.Buffer
	_ = pem.Encode(&b, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	b.WriteString(certPEM)
	return b.Bytes()
}

func loadIdentity(dir string) (*identity, error) {
	idPEM, err := os.ReadFile(filepath.Join(dir, fileIdentity))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	} else if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, fileCA))
	if err != nil {
		return nil, err
	}
	metaJSON, err := os.ReadFile(filepath.Join(dir, fileMeta))
	if err != nil {
		return nil, err
	}
	var meta Meta
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", fileMeta, err)
	}
	cert, err := tls.X509KeyPair(idPEM, idPEM) // key and certificate blocks are in the same file
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fileIdentity, err)
	}
	if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca.pem holds no certificate")
	}
	caCert, err := parseOneCert(caPEM)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fileCA, err)
	}
	return &identity{meta: meta, cert: cert, ca: pool, caCert: caCert}, nil
}

// mtlsConfig trusts only the stored panel CA and presents whatever certificate is current at handshake
// time, so a renewal takes effect on the next connection without rebuilding anything.
func (a *Agent) mtlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: a.meta.SNI,
		NextProtos: []string{"h2"},
		RootCAs:    a.id.Load().ca,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &a.id.Load().cert, nil
		},
	}
}

// renewLoop renews the certificate when less than renewBefore remains. A new key is generated every time.
func (a *Agent) renewLoop(ctx context.Context) {
	for {
		wait := a.renewEvery
		if until := time.Until(a.id.Load().notAfter()); until < renewBefore {
			if err := a.renew(ctx); err != nil {
				a.log.Warn("certificate renewal failed", "err", err, "expires_in", until.Round(time.Minute))
				if errors.Is(err, errNoSession) { // a link-only agent back from an outage: do not wait an hour for its session
					wait = min(wait, a.renewNoSessionWait())
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

var errNoSession = errors.New("no panel session to renew on")

func (a *Agent) renewNoSessionWait() time.Duration {
	if a.renewRetry > 0 {
		return a.renewRetry
	}
	return 20 * time.Second
}

func (a *Agent) renew(ctx context.Context) error {
	if a.linkOnly() {
		return a.renewOverLink(ctx)
	}
	key, csr, err := newKeyAndCSR()
	if err != nil {
		return err
	}
	st := a.settings.Load()
	client := newHTTPClient(a.mtlsConfig(), dialTimeout(st), nil)
	defer client.CloseIdleConnections()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := agentv1connect.NewEnrollmentServiceClient(client, "https://"+a.meta.Panel).Renew(cctx,
		connect.NewRequest(&pb.RenewRequest{CsrDer: csr}))
	if err != nil {
		return err
	}
	return a.installRenewed(key, resp.Msg.CertificatePem)
}

// renewOverLink asks for a new certificate on the current link session (a link-only agent has no mTLS Renew) and
// waits up to two minutes for the answer. With no session the hourly renewLoop tries again.
func (a *Agent) renewOverLink(ctx context.Context) error {
	s := a.cur.Load()
	if s == nil {
		return errNoSession
	}
	key, csr, err := newKeyAndCSR()
	if err != nil {
		return err
	}
	reply := make(chan *pb.RenewResponse, 1)
	a.renewMu.Lock()
	a.renewReply = reply
	a.renewMu.Unlock()
	defer func() {
		a.renewMu.Lock()
		a.renewReply = nil
		a.renewMu.Unlock()
	}()
	s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_Renew{Renew: &pb.RenewRequest{CsrDer: csr}}})
	t := time.NewTimer(a.renewWait())
	defer t.Stop()
	select {
	case r := <-reply:
		return a.installRenewed(key, r.CertificatePem)
	case <-s.ctx.Done():
		return errors.New("session ended before the renewed certificate arrived")
	case <-t.C:
		return errors.New("no renewed certificate in time")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) renewWait() time.Duration {
	if a.renewTimeout > 0 {
		return a.renewTimeout
	}
	return 2 * time.Minute
}

// onRenewReply hands a RenewResponse of the session to the renewal that waits for it (a late or unasked one is dropped).
func (a *Agent) onRenewReply(r *pb.RenewResponse) {
	a.renewMu.Lock()
	defer a.renewMu.Unlock()
	if a.renewReply != nil {
		select {
		case a.renewReply <- r:
		default:
		}
	}
}

// installRenewed checks a renewed certificate against our new key, stores it and makes it current.
func (a *Agent) installRenewed(key *ecdsa.PrivateKey, certPEM string) error {
	if _, err := checkLeaf([]byte(certPEM), key, a.id.Load().ca); err != nil {
		return fmt.Errorf("renewed certificate: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(a.cfg.StateDir, fileIdentity), identityPEM(key, certPEM), 0o600); err != nil {
		return err
	}
	next, err := loadIdentity(a.cfg.StateDir)
	if err != nil {
		return err
	}
	a.id.Store(next)
	a.log.Info("certificate renewed", "not_after", next.notAfter().UTC().Format(time.RFC3339))
	a.event(pb.Severity_SEVERITY_INFO, "cert_renewed", "", map[string]string{"not_after": next.notAfter().UTC().Format(time.RFC3339)})
	return nil
}

// newHTTPClient builds an HTTP/2 client for one purpose (enroll, renew, or one stream). It never uses
// proxies from the environment and has no overall timeout (streams are long-lived; callers use contexts).
func newHTTPClient(tc *tls.Config, dial time.Duration, h2 *http.HTTP2Config) *http.Client {
	tr := &http.Transport{
		TLSClientConfig:     tc,
		ForceAttemptHTTP2:   true,
		DialContext:         (&net.Dialer{Timeout: dial, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: dial,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConns:        1,
	}
	if h2 != nil {
		tr.HTTP2 = h2
	}
	return &http.Client{Transport: tr}
}

// writeFileAtomic writes data to a temp file in the same directory and renames it over path.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
