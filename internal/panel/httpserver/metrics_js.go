//go:build js

package httpserver

import "net/http"

// The metrics endpoint is the VPS edition's; the Cloudflare edition's admin Worker has no code for it.
func (s *Server) serveMetrics(http.ResponseWriter, *http.Request) bool { return false }
