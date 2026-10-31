package httpserver

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// versionMeta names the <meta> that tells a tab which panel version served its index.html. The admin compares it with the
// versionHeader of its API answers (web/src/lib/stale.ts): a tab that outlives a panel update says so.
const (
	versionMeta   = "mistgate-version"
	versionHeader = "X-Mistgate-Version"
)

const uiNotBuiltHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Mistgate</title></head>
<body><p>The admin UI is not built. Run <code>pnpm install &amp;&amp; pnpm build</code> in web/, then rebuild the panel.</p></body></html>
`

// spa serves the embedded admin SPA: real files from dist, everything else that looks
// like a client-side route gets index.html with <base href> rewritten to base.
func (s *Server) spa(base string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// The API lives under /api/ only (served by the mux before we get here); never
		// answer a near-miss such as "/API/…" with the app shell.
		if first, _, _ := strings.Cut(name, "/"); strings.EqualFold(first, "api") || hasDotSegment(name) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if name != "" && name != "index.html" {
			if f, err := s.dist.Open(name); err == nil {
				defer f.Close()
				if st, err := f.Stat(); err == nil && !st.IsDir() {
					if strings.HasPrefix(name, "assets/") {
						w.Header()["Cache-Control"] = headerCacheImmutable[:1:1]
					}
					if serveFile(w, r, name, f) {
						return
					}
				}
			}
			if strings.Contains(path.Base(name), ".") { // a missing asset is a 404, not the app shell
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.dist, "index.html")
		if err != nil {
			writeHTML(w, r, http.StatusOK, uiNotBuiltHTML)
			return
		}
		if base != "/" { // escaped: the prefix is a stored setting, and a quote in it must not end the attribute
			index = bytes.Replace(index, []byte(`<base href="/">`), []byte(`<base href="`+html.EscapeString(base)+`">`), 1)
		}
		if v := buildinfo.Version; v != "" {
			index = bytes.Replace(index, []byte("<head>"), []byte(`<head><meta name="`+versionMeta+`" content="`+html.EscapeString(v)+`">`), 1)
		}
		h := w.Header()
		h["Content-Type"] = headerContentTypeHTML[:1:1]
		if _, ok := h["Cache-Control"]; !ok { // the admin stack already says no-store; this keeps the shell fresh on its own
			h["Cache-Control"] = headerCacheNoCache[:1:1]
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write(index)
		}
	})
}

// withVersion stamps the panel version on the API answers of a signed-in admin (the sign-in procedures stay silent: the
// version is not public). A tab compares it with the version of its own index.html.
func withVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.AdminFrom(r.Context()); ok && buildinfo.Version != "" {
			w.Header()[versionHeader] = []string{buildinfo.Version}
		}
		next.ServeHTTP(w, r)
	})
}
