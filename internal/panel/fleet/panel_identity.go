package fleet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"sync"
	"time"
)

// The panel's identity at a subscription host (design/adr/0018-panel-behind-nat.md): a short client certificate of the
// fleet CA with the URI spiffe://panel/<ca id>. The panel signs the TLS exporter of each connection to the host with it; the
// host checks the chain to its fleet CA. It lives in memory only: a restart issues a new one, and nothing can revoke it but
// its expiry, which is fine, since only the holder of the CA key can make one.

const (
	panelURIHost = "panel"
	panelCertTTL = 7 * 24 * time.Hour
	panelRenewAt = 2 * 24 * time.Hour // a new certificate when less than this remains
)

type panelIdentity struct {
	mu       sync.Mutex
	der      []byte
	key      *ecdsa.PrivateKey
	notAfter time.Time
}

// issuePanel signs a new certificate of the panel: a fresh P-256 key, ClientAuth only, a random-looking subject name.
func (c *ca) issuePanel(now time.Time) ([]byte, *ecdsa.PrivateKey, time.Time, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	u, err := parseURL(nodeURIScheme + "://" + panelURIHost + "/" + c.id)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: randomCN()},
		URIs:         u,
		NotBefore:    now.Add(-certBackdate),
		NotAfter:     now.Add(panelCertTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return der, key, tmpl.NotAfter, nil
}

// PanelIdentity is the panel's current certificate (DER) and key for a subscription host's bridge; a new one is issued when
// less than panelRenewAt remains.
func (f *Fleet) PanelIdentity(now time.Time) ([]byte, *ecdsa.PrivateKey, error) {
	if f.ca == nil || f.ca.key == nil {
		return nil, nil, errors.New("no fleet CA")
	}
	p := &f.panelID
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key == nil || p.notAfter.Sub(now) < panelRenewAt {
		der, key, notAfter, err := f.ca.issuePanel(now)
		if err != nil {
			return nil, nil, err
		}
		p.der, p.key, p.notAfter = der, key, notAfter
	}
	return p.der, p.key, nil
}
