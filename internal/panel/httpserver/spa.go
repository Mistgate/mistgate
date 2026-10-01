package httpserver

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
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
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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
		if base != "/" {
			index = bytes.Replace(index, []byte(`<base href="/">`), []byte(`<base href="`+base+`">`), 1)
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write(index)
		}
	})
}
