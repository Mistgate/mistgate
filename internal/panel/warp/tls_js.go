//go:build js

package warp

import (
	"crypto/x509"
	"errors"
	"net/http"
)

const tlsSpecWgcf230 = "wgcf_v2.3.0"

var tlsSpecs = map[string]struct{}{
	tlsSpecWgcf230: {},
}

func tlsSpecIDs() []string { return []string{tlsSpecWgcf230} }

func newTransport(string, *x509.CertPool) (*http.Transport, error) {
	return nil, errors.New("WARP registration is unavailable in this edition")
}
