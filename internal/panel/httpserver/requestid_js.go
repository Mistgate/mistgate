//go:build js

package httpserver

import "net/http"

// The Cloudflare edition does not stamp request ids: the middleware (and the log line it writes) stays out of the admin Worker's
// wasm, whose size budget is nearly spent (scripts/check-edge-sizes.sh). The VPS edition has it (requestid.go).
func (s *Server) requestID(next http.Handler) http.Handler { return next }
