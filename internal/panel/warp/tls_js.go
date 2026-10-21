//go:build js

package warp

import (
	"crypto/x509"
	"fmt"
	"net/http"
)

// On GOOS=js Go's net/http sends a request through the runtime's fetch when the Transport has no dialers
// (roundtrip_js.go); in a Worker that is Cloudflare's own fetch, so the ClientHello is the Worker platform's, not
// the Android app's. Whether the registration endpoint accepts it is only known from a real attempt.

const tlsSpecWgcf230 = "wgcf_v2.3.0"

var tlsSpecs = map[string]struct{}{
	tlsSpecWgcf230: {},
}

func tlsSpecIDs() []string { return []string{tlsSpecWgcf230} }

// sendConnectionHeader: a connection header belongs to the HTTP stack under fetch, not to the caller.
const sendConnectionHeader = false

// newTransport returns the transport that goes through fetch: it must keep every Dial* hook nil, or net/http falls
// back to its own (unavailable) sockets. The id is only checked, as on a VPS; roots do not apply to fetch.
func newTransport(specID string, _ *x509.CertPool) (*http.Transport, error) {
	if _, ok := tlsSpecs[specID]; !ok {
		return nil, fmt.Errorf("unknown tls spec %q", specID)
	}
	return &http.Transport{}, nil
}
