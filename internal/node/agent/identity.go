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
	fileIdentity = "identity.pem" // EC PRIVATE KEY + CERTIFICATE
	fileCA       = "ca.pem"       // the panel CA; from enrollment on it is the only trust anchor
	fileMeta     = "agent.json"   // panel address, agent SNI, node id
	fileState    = "state.json"   // last applied desired state
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
}

type identity struct {
	meta Meta
	cert tls.Certificate // Leaf is set
	ca   *x509.CertPool
}

func (id *identity) notAfter() time.Time { return id.cert.Leaf.NotAfter }

// EnrollConfig is the input of the `enroll` command.
type EnrollConfig struct {
	StateDir string
	Panel    string // host:port
	SNI      string
	CASHA256 string // hex fingerprint of the panel CA certificate (colons and case ignored)
	Token    string
	Version  string // defaults to buildinfo.Version
	Force    bool   // overwrite an existing identity
	Timeout  time.Duration
}

// Enroll generates a P-256 key and a CSR, exchanges the one-time token for a node certificate over TLS
// that trusts ONLY a CA with the given fingerprint, and stores key, certificate, CA and panel address.
func Enroll(ctx context.Context, cfg EnrollConfig) (Meta, error) {
	pin, err := parsePin(cfg.CASHA256)
	if err != nil {
		return Meta{}, err
	}
	if _, _, err := net.SplitHostPort(cfg.Panel); err != nil {
		return Meta{}, fmt.Errorf("--panel must be host:port: %w", err)
	}
	if cfg.SNI == "" || cfg.Token == "" || cfg.StateDir == "" {
		return Meta{}, errors.New("panel SNI, token and state dir are required")
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
	key, csr, err := newKeyAndCSR()
	if err != nil {
		return Meta{}, err
	}

	client := newHTTPClient(pinnedTLS(cfg.SNI, pin), cfg.Timeout, nil)
	defer client.CloseIdleConnections()
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	resp, err := agentv1connect.NewEnrollmentServiceClient(client, "https://"+cfg.Panel).Enroll(cctx,
		connect.NewRequest(&pb.EnrollRequest{
			EnrollmentToken: cfg.Token, CsrDer: csr, AgentVersion: cfg.Version, ApiVersion: apiVersion,
		}))
	if err != nil {
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
		return Meta{}, errors.New("the CA returned by the panel does not match --ca-sha256")
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := checkLeaf([]byte(m.CertificatePem), key, pool); err != nil {
		return Meta{}, fmt.Errorf("issued certificate: %w", err)
	}

	meta := Meta{Panel: cfg.Panel, SNI: cfg.SNI, NodeID: m.NodeId}
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
	// The panel ignores the subject and sets the URI SAN itself; the CN is only a label.
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "mistgate-node"}, SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, key)
	return key, csr, err
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
	return &identity{meta: meta, cert: cert, ca: pool}, nil
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
		if until := time.Until(a.id.Load().notAfter()); until < renewBefore {
			if err := a.renew(ctx); err != nil {
				a.log.Warn("certificate renewal failed", "err", err, "expires_in", until.Round(time.Minute))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.renewEvery):
		}
	}
}

func (a *Agent) renew(ctx context.Context) error {
	key, csr, err := newKeyAndCSR()
	if err != nil {
		return err
	}
	cur := a.id.Load()
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
	if _, err := checkLeaf([]byte(resp.Msg.CertificatePem), key, cur.ca); err != nil {
		return fmt.Errorf("renewed certificate: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(a.cfg.StateDir, fileIdentity), identityPEM(key, resp.Msg.CertificatePem), 0o600); err != nil {
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
