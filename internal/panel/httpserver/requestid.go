//go:build !js

package httpserver

import (
	"net/http"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/reqid"
)

// requestID stamps every admin request with a short id: in the request context (the log records written with it and the audit
// rows carry it), in the X-Request-Id response header (the admin UI shows it on a failed call so a person can quote it), and in
// one log line when the request ends in a server error. The header is sent only once the session or token middleware accepted
// the caller (reqid.MarkAuthed): the admin address must answer a stranger exactly like the decoy. An id the client sent is
// ignored. Only the path of an API, MCP or metrics call is logged: any other path may carry a secret prefix.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, rq := reqid.Start(r.Context(), reqid.New())
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w, rq: rq}
		next.ServeHTTP(rec, r.WithContext(ctx))
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		path := "other"
		if p := r.URL.Path; strings.HasPrefix(p, "/api/") || p == "/mcp" || p == "/metrics" {
			path = p
		}
		if status >= 500 {
			s.cfg.Log.WarnContext(ctx, "http: request failed", "code", "http_server_error", "status", status, "method", r.Method, "path", path, "ms", time.Since(start).Milliseconds())
		} else {
			s.cfg.Log.DebugContext(ctx, "http: request", "status", status, "method", r.Method, "path", path, "ms", time.Since(start).Milliseconds())
		}
	})
}

// statusWriter remembers the status code and adds the request id header when the headers go out. Unwrap and Flush keep streaming
// responses and http.ResponseController working.
type statusWriter struct {
	http.ResponseWriter
	rq     *reqid.Request
	status int
}

func (w *statusWriter) stamp(code int) {
	if w.status == 0 {
		w.status = code
		if w.rq.Authed() {
			w.Header().Set(reqid.Header, w.rq.ID)
		}
	}
}

func (w *statusWriter) WriteHeader(code int) {
	w.stamp(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.stamp(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	w.stamp(http.StatusOK)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
