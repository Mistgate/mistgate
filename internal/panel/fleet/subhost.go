//go:build !js

package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
)

// TunnelCertificate is the certificate the panel presents when it dials the subscription host node: signed by the fleet
// CA, ServerAuth, DNS SAN = sni (the node checks exactly that). It is cached per SNI until half its lifetime.
func (f *Fleet) TunnelCertificate(sni string) (tls.Certificate, error) {
	now := f.now()
	f.tunnelMu.Lock()
	defer f.tunnelMu.Unlock()
	if c, ok := f.tunnelCerts[sni]; ok && now.Before(c.renewAt) {
		return c.cert, nil
	}
	cert, notAfter, err := f.ca.issueServer(sni, now.UTC())
	if err != nil {
		return tls.Certificate{}, err
	}
	if f.tunnelCerts == nil {
		f.tunnelCerts = map[string]tunnelCert{}
	}
	f.tunnelCerts[sni] = tunnelCert{cert: cert, renewAt: now.Add(notAfter.Sub(now) / 2)}
	return cert, nil
}

// VerifyNodeCertificate checks a chain a node presented (leaf first): it chains to the fleet CA as a client certificate,
// names nodeID, is a current certificate of that node, and the node is not retired.
func (f *Fleet) VerifyNodeCertificate(nodeID string, raw [][]byte) error {
	if len(raw) == 0 {
		return errBadCert
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return errBadCert
	}
	inter := x509.NewCertPool()
	for _, der := range raw[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return errBadCert
		}
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: f.ca.pool, Intermediates: inter, CurrentTime: f.now(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return errBadCert
	}
	id, _, err := f.nodeFromCert(context.Background(), leaf)
	if err != nil || id != nodeID {
		return errBadCert
	}
	return nil
}
