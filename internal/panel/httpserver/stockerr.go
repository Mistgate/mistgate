package httpserver

import (
	"bytes"
	"net"
	"strconv"
	"time"
)

// net/http answers a request it cannot parse (bad request line, malformed headers,
// oversized headers, unsupported transfer encoding ...) itself, before any handler
// runs, with a fixed plain-text reply: "400 Bad Request" as text/plain. Nothing on
// *http.Server changes that, and it is what a scanner sending garbage would recognise
// as Go instead of the decoy site.
//
// On plain-TCP listeners the reply can be swapped: net/http writes it with a single
// Write on the connection, so a thin net.Conn wrapper recognises it by its exact shape
// and sends the decoy's 404 page instead (same status, body and content type as every
// other unknown URL). What this cannot reach:
//   - TLS listeners. There net/http writes through its own *tls.Conn, which it type-asserts
//     and therefore cannot be wrapped; the same goes for the plain-HTTP-to-HTTPS notice
//     Go writes on the raw connection, for HTTP/2 protocol errors and for TLS alerts.
//     Terminate TLS in a reverse proxy in front of the panel if those matter: the proxy
//     answers malformed requests with its own (common) error pages and the panel then
//     listens on plain TCP, where this applies.
//   - Connection-level behaviour (timeouts, when the connection is closed).

// stockErrorHeaders is what follows the status line of net/http's own error replies.
// Replies written by handlers carry a Date header first, so this never matches one.
const stockErrorHeaders = "\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n"

// isStockError reports whether p, one Write from net/http, is one of its built-in error replies.
func isStockError(p []byte) bool {
	if !bytes.HasPrefix(p, []byte("HTTP/1.1 ")) {
		return false
	}
	i := bytes.IndexByte(p, '\r')
	return i > 0 && bytes.HasPrefix(p[i:], []byte(stockErrorHeaders))
}

// decoyNotFoundReply is the full HTTP/1.1 reply for the decoy's 404, ready to write to a
// connection that is about to be closed.
func (d *decoy) notFoundReply(now time.Time) []byte {
	body := d.notFoundBody()
	var b bytes.Buffer
	b.WriteString("HTTP/1.1 404 Not Found\r\n")
	b.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n") // the order net/http writes them in
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("Date: " + now.UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT\r\n")
	b.WriteString("Connection: close\r\n\r\n")
	b.WriteString(body)
	return b.Bytes()
}

// decoyErrors wraps a plain-TCP listener so net/http's stock error replies become the
// decoy's 404. See the comment at the top of this file for the limits.
func decoyErrors(ln net.Listener, d *decoy) net.Listener { return &errListener{ln, d} }

type errListener struct {
	net.Listener
	d *decoy
}

func (l *errListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &errConn{Conn: c, d: l.d}, nil
}

type errConn struct {
	net.Conn
	d *decoy
}

func (c *errConn) Write(p []byte) (int, error) {
	if isStockError(p) {
		if _, err := c.Conn.Write(c.d.notFoundReply(time.Now())); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return c.Conn.Write(p)
}
