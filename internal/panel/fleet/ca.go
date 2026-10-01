package fleet

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
	"math/big"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	caLifetime    = 10 * 365 * 24 * time.Hour
	nodeCertTTL   = 30 * 24 * time.Hour
	renewBefore   = 10 * 24 * time.Hour // the agent renews when less than this remains (agent.proto)
	serverCertTTL = 90 * 24 * time.Hour
	serverRenewAt = 30 * 24 * time.Hour // rebuild the in-memory server cert when less than this remains
	certBackdate  = 10 * time.Minute    // tolerate a node clock that runs a little behind
	// oldCertGrace is how long the certificate a node renewed away from stays valid: long enough for an
	// agent whose Renew response got lost to retry, short enough that a stolen key dies soon after.
	oldCertGrace = 10 * time.Minute
	// The certificates must not name the product: anyone who knows the agent SNI can handshake with the
	// listener and read the server certificate and its issuer. Node identity is a SPIFFE-style URI SAN.
	nodeURIScheme = "spiffe"
	nodeURIHost   = "agent"
)

// randomCN is a random-looking subject name: 12 lower-case hex digits, like the serial-number style names
// of commodity intermediate CAs. It is used only for the CA, whose name nothing depends on.
func randomCN() string {
	var b [6]byte
	rand.Read(b[:]) // never fails on supported platforms
	return hex.EncodeToString(b[:])
}

// ca is the panel CA: it signs node client certificates and the agent endpoint's server certificate.
// One active CA, no rotation flow (old rows would still verify, see store.CAs); add rotation when
// the 10-year CA approaches expiry.
type ca struct {
	id          string
	cert        *x509.Certificate
	pem         string
	fingerprint string // hex sha256 of the DER certificate
	key         *ecdsa.PrivateKey
	pool        *x509.CertPool // every CA row, so certificates of a previous CA keep verifying
}

// loadCA loads the CAs from the database, creating the first one (key sealed by the vault) if there is none.
func loadCA(ctx context.Context, st *store.Store, v *vault.Vault, now time.Time) (*ca, error) {
	rows, err := st.CAs(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || !rows[0].Active {
		row, err := newCARow(v, now)
		if err != nil {
			return nil, err
		}
		if err := st.InsertCA(ctx, row, now); err != nil {
			return nil, fmt.Errorf("store CA: %w", err)
		}
		rows = append([]store.CARow{row}, rows...)
	}
	c := &ca{pool: x509.NewCertPool()}
	for i, r := range rows {
		block, _ := pem.Decode([]byte(r.CertPEM))
		if block == nil {
			return nil, fmt.Errorf("CA %s: bad certificate PEM", r.ID)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CA %s: %w", r.ID, err)
		}
		c.pool.AddCert(cert)
		if i > 0 {
			continue
		}
		der, err := v.Open(r.KeyEnc, r.ID)
		if err != nil {
			return nil, fmt.Errorf("CA %s key: %w", r.ID, err)
		}
		k, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("CA %s key: %w", r.ID, err)
		}
		key, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("CA key is not ECDSA")
		}
		c.id, c.cert, c.pem, c.fingerprint, c.key = r.ID, cert, r.CertPEM, r.Fingerprint, key
	}
	return c, nil
}

func newCARow(v *vault.Vault, now time.Time) (store.CARow, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return store.CARow{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: randomCN()},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return store.CARow{}, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return store.CARow{}, err
	}
	id := store.NewID("cas_")
	sum := sha256.Sum256(der)
	return store.CARow{
		ID:          id,
		CertPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Fingerprint: hex.EncodeToString(sum[:]),
		KeyEnc:      v.Seal(pkcs8, id),
		NotBefore:   tmpl.NotBefore,
		NotAfter:    tmpl.NotAfter,
		Active:      true,
	}, nil
}

func newSerial() *big.Int {
	var b [16]byte
	rand.Read(b[:]) // never fails on supported platforms
	b[0] &= 0x7f    // positive
	b[15] |= 1      // non-zero
	return new(big.Int).SetBytes(b[:])
}

// nodeURI is the identity of a node inside its certificate.
func nodeURI(nodeID string) string { return nodeURIScheme + "://" + nodeURIHost + "/" + nodeID }

// issueNode signs a 30-day client certificate for the node. The subject of the CSR is ignored: the
// identity is the URI SAN set here.
func (c *ca) issueNode(nodeID string, pub *ecdsa.PublicKey, now time.Time) (store.CertRow, error) {
	u, err := parseURL(nodeURI(nodeID))
	if err != nil {
		return store.CertRow{}, err
	}
	serial := newSerial()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		URIs:         u,
		NotBefore:    now.Add(-certBackdate),
		NotAfter:     now.Add(nodeCertTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, pub, c.key)
	if err != nil {
		return store.CertRow{}, err
	}
	return store.CertRow{
		Serial: serial.Text(16), NodeID: nodeID, CAID: c.id,
		PEM:       string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		NotBefore: tmpl.NotBefore, NotAfter: tmpl.NotAfter, IssuedAt: now,
	}, nil
}

// issueServer creates the server certificate of the agent endpoint (DNS SAN = the agent SNI). It lives in
// memory only and is rebuilt when it nears expiry. The chain sent to the agent is [leaf, CA].
func (c *ca) issueServer(sni string, now time.Time) (tls.Certificate, time.Time, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, time.Time{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: sni}, // like any web server certificate: the host name
		DNSNames:     []string{sni},
		NotBefore:    now.Add(-certBackdate),
		NotAfter:     now.Add(serverCertTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, time.Time{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}, tmpl.NotAfter, nil
}
