package httpserver

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rawExchange sends raw bytes to addr and returns everything the server answers until it
// closes the connection (or pauses for 500 ms).
func rawExchange(t *testing.T, addr string, send string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, send); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	b, _ := io.ReadAll(c)
	return string(b)
}

// parseRaw splits a raw HTTP/1.x reply into status line, headers (Date dropped) and body.
func parseRaw(t *testing.T, raw string) (status string, hdr http.Header, body string) {
	t.Helper()
	r := bufio.NewReader(strings.NewReader(raw))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("no status line in %q", raw)
	}
	hdr = http.Header{}
	for {
		l, err := r.ReadString('\n')
		if err != nil || strings.TrimSpace(l) == "" {
			break
		}
		k, v, _ := strings.Cut(strings.TrimSpace(l), ":")
		if k != "Date" {
			hdr.Add(k, strings.TrimSpace(v))
		}
	}
	b, _ := io.ReadAll(r)
	return strings.TrimSpace(line), hdr, string(b)
}

// serveWrapped runs the real public handler on a plain TCP listener wrapped the way Serve
// wraps it, and returns its address.
func serveWrapped(t *testing.T, e *testEnv, wrap bool, extra http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var l net.Listener = ln
	if wrap {
		l = decoyErrors(ln, e.srv.decoy)
	}
	h := e.srv.Public()
	if extra != nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/__test" {
				extra.ServeHTTP(w, r)
				return
			}
			e.srv.Public().ServeHTTP(w, r)
		})
	}
	srv := &http.Server{Handler: h, MaxHeaderBytes: 16 << 10, ErrorLog: nil}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// Requests net/http rejects before any handler runs get net/http's own plain-text error.
// On a plain-TCP listener that reply is swapped for the decoy's 404.
func TestStockErrorRepliesBecomeTheDecoy(t *testing.T) {
	bad := map[string]string{
		"garbage":                       "GARBAGE\r\n\r\n",
		"bad request line":              "GET /\r\n\r\n",
		"bad version":                   "GET / HTTP/9.9\r\nHost: x\r\n\r\n",
		"malformed header":              "GET / HTTP/1.1\r\nHost: x\r\nBad Header\r\n\r\n",
		"header with a space in name":   "GET / HTTP/1.1\r\nHost: x\r\nX Bad: 1\r\n\r\n",
		"missing host":                  "GET / HTTP/1.1\r\n\r\n",
		"bad host":                      "GET / HTTP/1.1\r\nHost: a b\r\n\r\n",
		"oversized headers":             "GET / HTTP/1.1\r\nHost: x\r\nX-Big: " + strings.Repeat("a", 40<<10) + "\r\n\r\n",
		"unsupported transfer coding":   "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip\r\n\r\n",
		"conflicting content lengths":   "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\nab",
		"negative content length":       "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: -1\r\n\r\n",
		"null byte in the request line": "GET /\x00 HTTP/1.1\r\nHost: x\r\n\r\n",
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "404.html"), []byte("<h1>Lost at sea</h1>"), 0o644)
	for name, decoyDir := range map[string]string{"built-in decoy": "", "decoy site with its own 404 page": dir} {
		e := newTestEnv(t, func(c *Config) { c.DecoyDir = decoyDir })
		addr := serveWrapped(t, e, true, nil)
		plain := serveWrapped(t, e, false, nil)

		// What every unknown URL answers: the reference.
		wantStatus, wantHdr, wantBody := parseRaw(t, rawExchange(t, addr, "GET /nothing-here HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
		if !strings.HasPrefix(wantStatus, "HTTP/1.1 404") || (decoyDir != "" && !strings.Contains(wantBody, "Lost at sea")) {
			t.Fatalf("%s: reference reply %q %q", name, wantStatus, wantBody)
		}
		for what, req := range bad {
			// Without the wrapper this is net/http's own reply: the thing being hidden.
			if st, h, _ := parseRaw(t, rawExchange(t, plain, req)); h.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(st, "HTTP/1.1 4") && !strings.HasPrefix(st, "HTTP/1.1 5") {
				t.Fatalf("%s / %s: test premise broken, unwrapped reply is %q %v", name, what, st, h)
			}
			raw := rawExchange(t, addr, req)
			st, h, body := parseRaw(t, raw)
			if st != wantStatus || body != wantBody {
				t.Errorf("%s / %s: got %q %q, want %q %q", name, what, st, body, wantStatus, wantBody)
			}
			for _, k := range []string{"Content-Type", "Content-Length"} {
				if h.Get(k) != wantHdr.Get(k) {
					t.Errorf("%s / %s: %s = %q, want %q", name, what, k, h.Get(k), wantHdr.Get(k))
				}
			}
			if h.Get("Content-Length") != strconv.Itoa(len(body)) {
				t.Errorf("%s / %s: Content-Length %q for a %d byte body", name, what, h.Get("Content-Length"), len(body))
			}
			if strings.Contains(raw, "text/plain") || strings.Contains(raw, "Bad Request") {
				t.Errorf("%s / %s: stock text left in %q", name, what, raw)
			}
		}
	}
}

// Only net/http's built-in error replies are replaced: a handler that answers 400 with
// the same words, and every ordinary response, pass through untouched.
func TestStockErrorWrapperLeavesHandlerRepliesAlone(t *testing.T) {
	e := newTestEnv(t)
	addr := serveWrapped(t, e, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "400 Bad Request", http.StatusBadRequest)
	}))
	st, h, body := parseRaw(t, rawExchange(t, addr, "GET /__test HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	if st != "HTTP/1.1 400 Bad Request" || body != "400 Bad Request\n" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Errorf("handler reply altered: %q %v %q", st, h, body)
	}
	if st, _, body := parseRaw(t, rawExchange(t, addr, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); st != "HTTP/1.1 200 OK" || !strings.Contains(body, "Coming soon") {
		t.Errorf("decoy page altered: %q %q", st, body)
	}
	// Keep-alive: a bad second request on a connection that served a good first one.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\nGARBAGE\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	all, _ := io.ReadAll(c)
	if n := bytes.Count(all, []byte("HTTP/1.1 ")); n != 2 || !bytes.Contains(all, []byte("HTTP/1.1 200 OK")) || !bytes.Contains(all, []byte("HTTP/1.1 404 Not Found")) || bytes.Contains(all, []byte("text/plain")) {
		t.Errorf("pipelined: %q", all)
	}
}

func TestIsStockError(t *testing.T) {
	for in, want := range map[string]bool{
		"HTTP/1.1 400 Bad Request" + stockErrorHeaders + "400 Bad Request":                                               true,
		"HTTP/1.1 431 Request Header Fields Too Large" + stockErrorHeaders + "431 Request Header Fields Too Large":       true,
		"HTTP/1.1 501 Not Implemented" + stockErrorHeaders + "Unsupported transfer encoding":                             true,
		"HTTP/1.1 400 Bad Request: malformed" + stockErrorHeaders + "400 Bad Request: malformed":                         true,
		"HTTP/1.1 400 Bad Request\r\nDate: x\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n400": false,
		"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi":                                                                 false,
		"HTTP/1.0 400 Bad Request\r\n\r\nClient sent an HTTP request to an HTTPS server.\n":                              false,
		"hello": false, "": false, "HTTP/1.1 ": false,
	} {
		if got := isStockError([]byte(in)); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
