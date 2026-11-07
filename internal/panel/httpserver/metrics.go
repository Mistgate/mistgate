//go:build !js

package httpserver

import "net/http"

// serveMetrics hands /metrics to Config.Metrics and reports whether it answered.
func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) bool {
	return s.cfg.Metrics != nil && s.cfg.Metrics(w, r)
}
