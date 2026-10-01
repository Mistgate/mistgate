package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"path"
	"strings"
)

// canonicalPath reports whether the request path is in canonical form, the only form
// the panel routes. Anything else is answered with the decoy 404 before any routing:
// a path whose percent-encoding differs from Go's default encoding of the decoded path
// (%2e, %2f, %5c, %00, and also needless escapes such as %34 for the digit 4), double
// slashes, dot segments, backslashes, semicolons and control bytes. That closes the
// class of bypass where "/API/x" or "/%2e%2e/x" slips past a path-based check.
func canonicalPath(r *http.Request) bool {
	p := r.URL.Path
	// RawPath is only set when the request's encoding differs from the default one.
	if p == "" || p[0] != '/' || r.URL.RawPath != "" {
		return false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c == 0x7f || c == '\\' || c == ';' {
			return false
		}
	}
	if strings.Contains(p, "//") {
		return false
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	return clean == p
}

// ctEqual compares two strings in time independent of where they differ or of their
// lengths (it compares digests).
func ctEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// hostOnly lowercases the Host header and strips port and trailing dot.
func hostOnly(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
