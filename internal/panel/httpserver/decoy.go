package httpserver

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Built-in decoy pages. Every install ships the same ones, so they are recognisable;
// --decoy-dir with your own site is the recommended setup.
const (
	comingSoonHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Coming soon</title>
<style>html,body{height:100%;margin:0}body{display:flex;align-items:center;justify-content:center;font:16px/1.5 system-ui,sans-serif;color:#444;background:#fafafa}main{text-align:center;padding:1rem}h1{font-weight:500;margin:0 0 .5rem}p{margin:0;color:#888}</style>
</head><body><main><h1>Coming soon</h1><p>Something new is on its way.</p></main></body></html>
`
	notFoundHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>
`
	tooManyHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>429 Too Many Requests</title></head>
<body><h1>Too Many Requests</h1><p>Too many requests. Please try again later.</p></body></html>
`
)

// robotsTXT is what the public listener answers for /robots.txt when the decoy dir has none: known AI
// crawlers are shut out, everyone else is welcome. It names no path of the panel.
var robotsTXT = func() string {
	var b strings.Builder
	for _, ua := range []string{"GPTBot", "ChatGPT-User", "OAI-SearchBot", "ClaudeBot", "Claude-Web", "anthropic-ai", "CCBot",
		"Google-Extended", "Bytespider", "PerplexityBot", "Amazonbot", "Applebot-Extended", "meta-externalagent", "cohere-ai", "Diffbot"} {
		b.WriteString("User-agent: " + ua + "\n")
	}
	b.WriteString("Disallow: /\n\nUser-agent: *\nAllow: /\n")
	return b.String()
}()

// notFoundFloor is the least time a decoy 404 takes. An unknown subscription token costs one database
// read before it is answered with the decoy and an unknown path costs none; padding both to the same
// floor hides the difference. It has to exceed a lookup and stay far below a human wait.
const notFoundFloor = 8 * time.Millisecond

type startKey struct{}

// withStart records when the request reached the public listener, for notFoundFloor.
func withStart(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), startKey{}, time.Now()))
}

// decoy is the site shown to everyone who is not the admin: files from dir, or the
// built-in page. Unknown paths, wrong admin prefixes and non-canonical paths all end
// up in the same two code paths (serve / notFound), so they cannot be told apart.
type decoy struct {
	dir fs.FS // nil means built-in
}

// NewDecoy returns the handler for a secret mount (the subscription endpoint) to answer with whatever is
// not its own: the same 404 the public listener gives every unknown path, for any path and method. It is
// not the decoy site itself: a mount sees its prefix stripped, so the site's "/" page would answer the
// bare prefix with 200 and tell a guessed prefix from a wrong one.
func NewDecoy(dir string) http.Handler {
	return http.HandlerFunc(newDecoy(dir).notFound)
}

func newDecoy(dir string) *decoy {
	if dir == "" {
		return &decoy{}
	}
	return &decoy{dir: os.DirFS(dir)}
}

func (d *decoy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		d.notFound(w, r)
		return
	}
	p := r.URL.Path
	if d.dir == nil {
		switch p {
		case "/":
			writeHTML(w, r, http.StatusOK, comingSoonHTML)
		case "/robots.txt":
			d.robots(w, r)
		default:
			d.notFound(w, r)
		}
		return
	}
	name := strings.TrimPrefix(p, "/")
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	if !fs.ValidPath(name) || hasDotSegment(name) {
		d.notFound(w, r)
		return
	}
	if st, err := fs.Stat(d.dir, name); err == nil && st.IsDir() {
		name = path.Join(name, "index.html")
	}
	f, err := d.dir.Open(name)
	if err != nil {
		if p == "/robots.txt" {
			d.robots(w, r)
			return
		}
		d.notFound(w, r)
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() {
		d.notFound(w, r)
		return
	}
	if !serveFile(w, r, name, f) {
		d.notFound(w, r)
	}
}

// robots is the built-in robots.txt, used when the decoy dir has none of its own.
func (d *decoy) robots(w http.ResponseWriter, r *http.Request) {
	writeBody(w, r, http.StatusOK, headerContentTypePlainText[:1:1], robotsTXT)
}

// notFound answers with 404.html from the decoy dir if there is one, else the built-in page. It does not
// return before notFoundFloor has passed since the request arrived.
func (d *decoy) notFound(w http.ResponseWriter, r *http.Request) {
	if start, ok := r.Context().Value(startKey{}).(time.Time); ok {
		if wait := notFoundFloor - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
	}
	writeHTML(w, r, http.StatusNotFound, d.notFoundBody())
}

// tooMany answers 429 in the style of the decoy (429.html from the decoy dir, else the built-in page),
// with Retry-After in whole seconds.
func (d *decoy) tooMany(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	secs := int((retry + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeHTML(w, r, http.StatusTooManyRequests, d.pageBody("429.html", tooManyHTML))
}

func (d *decoy) notFoundBody() string { return d.pageBody("404.html", notFoundHTML) }

// pageBody is the named file of the decoy dir if there is one, else builtin.
func (d *decoy) pageBody(name, builtin string) string {
	if d.dir != nil {
		if f, err := d.dir.Open(name); err == nil {
			defer f.Close()
			if b, err := io.ReadAll(io.LimitReader(f, 1<<20)); err == nil {
				return string(b)
			}
		}
	}
	return builtin
}

func hasDotSegment(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func writeHTML(w http.ResponseWriter, r *http.Request, status int, body string) {
	writeBody(w, r, status, headerContentTypeHTML[:1:1], body)
}

func writeBody(w http.ResponseWriter, r *http.Request, status int, contentType []string, body string) {
	h := w.Header()
	h["Content-Type"] = contentType[:1:1]
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		io.WriteString(w, body)
	}
}

// serveFile serves an opened file without Last-Modified/ETag, so nothing about the
// filesystem leaks and every decoy response carries the same header set.
func serveFile(w http.ResponseWriter, r *http.Request, name string, f fs.File) bool {
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		rs = bytes.NewReader(b)
	}
	http.ServeContent(w, r, name, time.Time{}, rs)
	return true
}

// swapErrors wraps a secret mount so that nothing it answers with 404 or 405 differs from the decoy's 404:
// the ServeMux and http.NotFound inside a mount write Go's own plain-text replies (and an Allow header),
// which would tell a guessed prefix from a wrong one (security review F6). Headers the handler set for
// the swapped reply are dropped with it.
func (d *decoy) swapErrors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&swapWriter{ResponseWriter: w, r: r, d: d}, r)
	})
}

type swapWriter struct {
	http.ResponseWriter
	r       *http.Request
	d       *decoy
	wrote   bool
	swapped bool
}

func (s *swapWriter) WriteHeader(code int) {
	if s.wrote {
		return
	}
	s.wrote = true
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		h := s.ResponseWriter.Header()
		for k := range h {
			delete(h, k)
		}
		s.swapped = true
		s.d.notFound(s.ResponseWriter, s.r)
		return
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *swapWriter) Write(p []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	if s.swapped {
		return len(p), nil
	}
	return s.ResponseWriter.Write(p)
}

func (s *swapWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
