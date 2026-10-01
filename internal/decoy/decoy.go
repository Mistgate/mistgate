// Package decoy is the neutral static site shown to everyone who is not a Hysteria2 client (or an admin):
// HTTP/3 masquerade, HTTPS on TCP 443, later the panel's public listener.
//
// It is a plain http.Handler with no state and no Server header. "/" answers 200 with a tiny page; every
// other path and every method other than GET/HEAD answers the identical 404, so probing cannot tell
// "unknown path" from "wrong method" from "auth failed".
//
// One fixed page for every install (recognisable across installs). A per-install site bundle
// pushed from the panel replaces this handler later (engine.Env.Masquerade already takes any http.Handler).
package decoy

import (
	"io"
	"net/http"
	"strconv"
)

const pageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Coming soon</title>
<style>html,body{height:100%;margin:0}body{display:flex;align-items:center;justify-content:center;font:16px/1.5 system-ui,sans-serif;color:#444;background:#fafafa}main{text-align:center;padding:1rem}h1{font-weight:500;margin:0 0 .5rem}p{margin:0;color:#888}</style>
</head><body><main><h1>Coming soon</h1><p>Something new is on its way.</p></main></body></html>
`

const notFoundHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>
`

// Handler serves the built-in page on "/" and the identical 404 everywhere else.
func Handler() http.Handler { return http.HandlerFunc(serve) }

// NotFound answers every request with the same 404 page (masquerade "none").
func NotFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNotFound, notFoundHTML)
	})
}

func serve(w http.ResponseWriter, r *http.Request) {
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == "/" {
		write(w, r, http.StatusOK, pageHTML)
		return
	}
	write(w, r, http.StatusNotFound, notFoundHTML)
}

func write(w http.ResponseWriter, r *http.Request, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		io.WriteString(w, body)
	}
}
