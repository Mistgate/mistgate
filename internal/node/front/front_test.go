package front

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// helloOf returns the ClientHello a Go client sends for sni (one record), by letting it write into a pipe.
func helloOf(t testing.TB, sni string) []byte {
	t.Helper()
	c, s := net.Pipe()
	defer s.Close()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: sni, InsecureSkipVerify: true}).Handshake()
		_ = c.Close()
	}()
	head := make([]byte, 5)
	if _, err := io.ReadFull(s, head); err != nil {
		t.Fatal(err)
	}
	rest := make([]byte, binary.BigEndian.Uint16(head[3:]))
	if _, err := io.ReadFull(s, rest); err != nil {
		t.Fatal(err)
	}
	return append(head, rest...)
}

// record wraps a handshake fragment in a TLS record.
func record(body []byte) []byte {
	return append([]byte{22, 3, 1, byte(len(body) >> 8), byte(len(body))}, body...)
}

// split puts the handshake message of a one-record hello into n records.
func split(hello []byte, n int) []byte {
	body, step := hello[5:], (len(hello)-5+n-1)/n
	var out []byte
	for len(body) > 0 {
		k := min(step, len(body))
		out = append(out, record(body[:k])...)
		body = body[k:]
	}
	return out
}

// withExt appends an extension to a one-record hello and fixes the three lengths.
func withExt(hello []byte, typ uint16, data []byte) []byte {
	ch := hello[9:] // after the record header and the handshake header
	off := 2 + 32
	off += 1 + int(ch[off])
	off += 2 + int(binary.BigEndian.Uint16(ch[off:]))
	off += 1 + int(ch[off])
	if int(binary.BigEndian.Uint16(ch[off:]))+off+2 != len(ch) {
		panic("extensions are not last")
	}
	ext := binary.BigEndian.AppendUint16(nil, typ)
	ext = binary.BigEndian.AppendUint16(ext, uint16(len(data)))
	ext = append(ext, data...)
	out := append(bytes.Clone(hello), ext...)
	grow := func(at, width int) {
		n := 0
		for _, b := range out[at : at+width] {
			n = n<<8 | int(b)
		}
		n += len(ext)
		for i := width - 1; i >= 0; i-- {
			out[at+i] = byte(n)
			n >>= 8
		}
	}
	grow(3, 2)     // record length
	grow(6, 3)     // handshake length
	grow(9+off, 2) // extensions length
	return out
}

// echOuter is an encrypted_client_hello extension of an outer hello (type 0xfe0d), with made-up key material.
func echOuter() []byte {
	b := []byte{0, 0, 1, 0, 1, 7, 0, 32}
	b = append(b, make([]byte, 32)...)
	b = append(b, 0, 160)
	return append(b, make([]byte, 160)...)
}

// rig is a front on loopback with the routes a (a.example.com, the default) and b (b.example.com).
type rig struct {
	t   *testing.T
	f   *Front
	ln  net.Listener
	lst map[string]net.Listener
}

func routesAB() []Route {
	return []Route{
		{ID: "a", Names: []string{"a.example.com"}},
		{ID: "b", Names: []string{"b.example.com", "B2.example.com."}},
	}
}

// newRig starts a front; tune and wrap (either may be nil) run before it starts.
func newRig(t *testing.T, tune func(*Front), wrap func(net.Conn) net.Conn) *rig {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var ln net.Listener = raw
	if wrap != nil {
		ln = wrapListener{raw, wrap}
	}
	f := newFront(ln, nil)
	if tune != nil {
		tune(f)
	}
	f.wg.Add(1)
	go f.acceptLoop()
	f.SetRoutes(routesAB(), "a")
	r := &rig{t: t, f: f, ln: raw, lst: map[string]net.Listener{"a": f.Listener("a"), "b": f.Listener("b")}}
	t.Cleanup(func() { _ = f.Close() })
	return r
}

type wrapListener struct {
	net.Listener
	wrap func(net.Conn) net.Conn
}

func (l wrapListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.wrap(c), nil
}

func (r *rig) dial() net.Conn {
	r.t.Helper()
	c, err := net.Dial("tcp", r.ln.Addr().String())
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = c.Close() })
	return c
}

// accept takes the next connection of a listener, or fails after d.
func accept(t *testing.T, l net.Listener, d time.Duration) (net.Conn, error) {
	t.Helper()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := l.Accept()
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(d):
		return nil, os.ErrDeadlineExceeded
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// closedBy reads c until it ends and returns how: io.EOF is a FIN, anything else a reset.
func closedBy(t *testing.T, c net.Conn) error {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, c)
	if err == nil {
		return io.EOF
	}
	return err
}

func stillOpen(c net.Conn) bool {
	_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := c.Read(make([]byte, 1))
	return errors.Is(err, os.ErrDeadlineExceeded)
}

func TestDefaults(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := New(ln, nil)
	defer f.Close()
	if f.helloWait != 10*time.Second || f.routeWait != 5*time.Second || f.maxPending != 1024 {
		t.Fatalf("limits: %v %v %v", f.helloWait, f.routeWait, f.maxPending)
	}
}

// TestRouting walks the table of what the front does with what arrives. The route must get every byte the client sent,
// the head first, and the client's own address.
func TestRouting(t *testing.T) {
	hello := func(sni string) []byte { return helloOf(t, sni) }
	long := withExt(hello("a.example.com"), 21, make([]byte, 16000)) // one record of 17 KiB: over the read limit
	huge := split(withExt(hello("a.example.com"), 21, make([]byte, 20000)), 3)
	sslv2 := append([]byte{0x80, 0x2e, 1, 0, 2, 0, 0x15, 0, 0, 0, 0x10}, make([]byte, 40)...)
	http := []byte("GET / HTTP/1.1\r\nHost: a.example.com\r\n\r\n")
	cases := []struct {
		name  string
		send  []byte
		slow  bool // one byte per write
		route string
		why   func(Stats) uint64 // the counter that says why the default route got it; nil: a named route
	}{
		{name: "name of b", send: hello("b.example.com"), route: "b"},
		{name: "name in other case", send: hello("B.Example.COM"), route: "b"},
		{name: "second name of b", send: hello("b2.example.com"), route: "b"},
		{name: "three records", send: split(hello("b.example.com"), 3), route: "b"},
		{name: "one byte at a time", send: hello("b.example.com"), slow: true, route: "b"},
		{name: "three records, one byte at a time", send: split(hello("b.example.com"), 3), slow: true, route: "b"},
		{name: "ECH outer name", send: withExt(hello("b.example.com"), 0xfe0d, echOuter()), route: "b"},
		{name: "unknown name", send: hello("c.example.com"), route: "a", why: func(s Stats) uint64 { return s.UnknownName }},
		{name: "no name", send: hello(""), route: "a", why: func(s Stats) uint64 { return s.NoSNI }},
		{name: "HTTP", send: http, route: "a", why: func(s Stats) uint64 { return s.NotTLS }},
		{name: "SSLv2 hello", send: sslv2, route: "a", why: func(s Stats) uint64 { return s.NotTLS }},
		{name: "noise", send: bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 30), route: "a", why: func(s Stats) uint64 { return s.NotTLS }},
		{name: "five bytes of HTTP", send: []byte("GET /"), route: "a", why: func(s Stats) uint64 { return s.NotTLS }},
		{name: "handshake record of noise", send: record(bytes.Repeat([]byte{7}, 60)), route: "a", why: func(s Stats) uint64 { return s.BadHello }},
		{name: "hello with a short body", send: record([]byte{1, 0, 0, 4, 1, 2, 3, 4}), route: "a", why: func(s Stats) uint64 { return s.BadHello }},
		{name: "hello in a record over the limit", send: long, route: "a", why: func(s Stats) uint64 { return s.HelloTooLarge }},
		{name: "hello over 16 KiB in three records", send: huge, route: "a", why: func(s Stats) uint64 { return s.HelloTooLarge }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil, nil)
			c := r.dial()
			go func() {
				if tc.slow {
					for i := range tc.send {
						if _, err := c.Write(tc.send[i : i+1]); err != nil {
							return
						}
					}
					return
				}
				_, _ = c.Write(tc.send)
			}()
			got, err := accept(t, r.lst[tc.route], 5*time.Second)
			if err != nil {
				t.Fatalf("route %s: %v", tc.route, err)
			}
			defer got.Close()
			if got.RemoteAddr().String() != c.LocalAddr().String() {
				t.Fatalf("remote address %v, client is %v", got.RemoteAddr(), c.LocalAddr())
			}
			buf := make([]byte, len(tc.send))
			_ = got.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(got, buf); err != nil || !bytes.Equal(buf, tc.send) {
				t.Fatalf("the route got %d of %d bytes (%v), or other bytes", len(buf), len(tc.send), err)
			}
			s := r.f.Stats()
			if s.Delivered[tc.route] != 1 || s.Accepted != 1 {
				t.Fatalf("stats %+v", s)
			}
			reasons := map[string]uint64{"NoSNI": s.NoSNI, "UnknownName": s.UnknownName, "NotTLS": s.NotTLS, "BadHello": s.BadHello, "HelloTooLarge": s.HelloTooLarge}
			var total uint64
			for _, n := range reasons {
				total += n
			}
			switch {
			case tc.why == nil && total != 0:
				t.Fatalf("a named route, yet default reasons %v", reasons)
			case tc.why != nil && (tc.why(s) != 1 || total != 1):
				t.Fatalf("want exactly one reason, got %v", reasons)
			}
		})
	}
}

func TestUnknownNamesAreCounted(t *testing.T) {
	r := newRig(t, nil, nil)
	for _, sni := range []string{"x.example.com", "y.example.com", "x.example.com", "x.example.com"} {
		c := r.dial()
		_, _ = c.Write(helloOf(t, sni))
		got, err := accept(t, r.lst["a"], 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = got.Close()
	}
	u := r.f.Stats().Unknown
	if len(u) != 2 || u[0] != (NameCount{"x.example.com", 3}) || u[1] != (NameCount{"y.example.com", 1}) {
		t.Fatalf("unknown names %+v", u)
	}
}

func TestUnknownNamesAreBounded(t *testing.T) {
	f := newFront(nopListener{}, nil)
	for i := range 100 {
		f.noteUnknown("h" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".example.com")
	}
	f.noteUnknown("bad name.example.com")
	f.noteUnknown("UPPER.example.com")
	if n := len(f.Stats().Unknown); n != maxUnknown {
		t.Fatalf("%d names kept", n)
	}
}

type nopListener struct{ net.Listener }

func (nopListener) Close() error { return nil }

func TestSilentConnectionGetsFIN(t *testing.T) {
	r := newRig(t, func(f *Front) { f.helloWait = 200 * time.Millisecond }, nil)
	c := r.dial()
	_, _ = c.Write([]byte{22, 3, 1, 0}) // the start of a record, then silence
	start := time.Now()
	if err := closedBy(t, c); err != io.EOF {
		t.Fatalf("want FIN, got %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("closed after %v", d)
	}
	waitFor(t, "the timeout to be counted", func() bool { return r.f.Stats().Timeouts == 1 })
}

// TestUnreadDataGetsRST: the client's byte arrives after the front's last read and before its Close, so the kernel resets.
// The wrapper makes the order certain.
func TestUnreadDataGetsRST(t *testing.T) {
	client := make(chan net.Conn, 1)
	r := newRig(t, func(f *Front) { f.helloWait = 200 * time.Millisecond }, func(c net.Conn) net.Conn {
		return &hookConn{Conn: c, onTimeout: func() {
			cc := <-client
			_, _ = cc.Write([]byte{1})
			time.Sleep(100 * time.Millisecond) // the byte reaches the socket
		}}
	})
	c := r.dial()
	client <- c
	_, _ = c.Write([]byte{22, 3, 1, 0})
	if err := closedBy(t, c); err == io.EOF {
		t.Fatal("want RST, got FIN")
	}
}

type hookConn struct {
	net.Conn
	onTimeout func()
}

func (c *hookConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) && c.onTimeout != nil {
		c.onTimeout()
	}
	return n, err
}

func TestOldestUnfinishedGiveWay(t *testing.T) {
	r := newRig(t, func(f *Front) { f.maxPending = 3 }, nil)
	var conns []net.Conn
	for i := 1; i <= 4; i++ {
		conns = append(conns, r.dial())
		waitFor(t, "accept", func() bool { return r.f.Stats().Accepted == uint64(i) })
	}
	if err := closedBy(t, conns[0]); err != io.EOF {
		t.Fatalf("the oldest: %v", err)
	}
	for _, c := range conns[1:] {
		if !stillOpen(c) {
			t.Fatal("a younger connection was closed")
		}
	}
	if s := r.f.Stats(); s.Evicted != 1 || s.Timeouts != 0 {
		t.Fatalf("stats %+v", s)
	}
	// a finished hello leaves the set at once: only the first of these evicts (conns[1])
	for range 3 {
		c := r.dial()
		_, _ = c.Write(helloOf(t, "a.example.com"))
		got, err := accept(t, r.lst["a"], 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = got.Close()
	}
	if s := r.f.Stats(); s.Evicted != 2 {
		t.Fatalf("evicted %d", s.Evicted)
	}
}

func TestRestartingRouteIsWaitedFor(t *testing.T) {
	r := newRig(t, func(f *Front) { f.routeWait = 2 * time.Second }, nil)
	_ = r.lst["b"].Close() // the engine of b is restarting
	c := r.dial()
	hello := helloOf(t, "b.example.com")
	_, _ = c.Write(hello)
	time.Sleep(200 * time.Millisecond)
	if !stillOpen(c) {
		t.Fatal("closed while the route was away")
	}
	next := r.f.Listener("b")
	got, err := accept(t, next, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	buf := make([]byte, len(hello))
	if _, err := io.ReadFull(got, buf); err != nil || !bytes.Equal(buf, hello) {
		t.Fatalf("replay: %v", err)
	}
	if _, err := r.lst["b"].Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("the old listener: %v", err)
	}
}

func TestRouteThatNeverReturnsIsGivenUpOn(t *testing.T) {
	r := newRig(t, func(f *Front) { f.routeWait = 300 * time.Millisecond }, nil)
	_ = r.lst["b"].Close()
	c := r.dial()
	_, _ = c.Write(helloOf(t, "b.example.com"))
	if err := closedBy(t, c); err != io.EOF {
		t.Fatalf("want FIN, got %v", err)
	}
	if s := r.f.Stats(); s.Refused != 1 || s.Delivered["b"] != 0 {
		t.Fatalf("stats %+v", s)
	}
	if _, err := accept(t, r.f.Listener("b"), 300*time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a connection that was given up on came back: %v", err)
	}
}

// A route whose service is not running (SetDown) sends its names to the default route at once, with the bytes read.
func TestDownRouteGoesToTheDefaultAtOnce(t *testing.T) {
	r := newRig(t, func(f *Front) { f.routeWait = 5 * time.Second }, nil)
	r.f.SetDown("b", true)
	hello := helloOf(t, "b.example.com")
	c := r.dial()
	_, _ = c.Write(hello)
	got, err := accept(t, r.lst["a"], time.Second)
	if err != nil {
		t.Fatalf("the default route got nothing: %v", err)
	}
	defer got.Close()
	buf := make([]byte, len(hello))
	if _, err := io.ReadFull(got, buf); err != nil || !bytes.Equal(buf, hello) {
		t.Fatalf("replay: %v", err)
	}
	if s := r.f.Stats(); s.RouteDown != 1 || s.Delivered["a"] != 1 || s.Delivered["b"] != 0 || s.Refused != 0 || s.UnknownName != 0 {
		t.Fatalf("stats %+v", s)
	}
	r.f.SetDown("b", false) // the service is back: its name is its own again
	_, _ = r.dial().Write(helloOf(t, "b.example.com"))
	if _, err := accept(t, r.lst["b"], time.Second); err != nil {
		t.Fatalf("b did not get its name back: %v", err)
	}
}

// A route that is up but does not Accept in time (an engine that never came back) is given up on for the default route
// instead of a closed connection; nowhere to go, and it is closed.
func TestRouteThatDoesNotAcceptFallsBackToTheDefault(t *testing.T) {
	r := newRig(t, func(f *Front) { f.routeWait = 300 * time.Millisecond }, nil)
	_ = r.lst["b"].Close()
	hello := helloOf(t, "b.example.com")
	c := r.dial()
	_, _ = c.Write(hello)
	got, err := accept(t, r.lst["a"], 2*time.Second)
	if err != nil {
		t.Fatalf("the default route got nothing: %v", err)
	}
	defer got.Close()
	buf := make([]byte, len(hello))
	if _, err := io.ReadFull(got, buf); err != nil || !bytes.Equal(buf, hello) {
		t.Fatalf("replay: %v", err)
	}
	if s := r.f.Stats(); s.RouteDown != 1 || s.Refused != 0 || s.Delivered["a"] != 1 || s.Delivered["b"] != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// The default route itself is down: nothing to fall back to, closed at once, not after a wait.
func TestDownDefaultRouteClosesAtOnce(t *testing.T) {
	r := newRig(t, func(f *Front) { f.routeWait = 5 * time.Second }, nil)
	r.f.SetDown("a", true)
	c := r.dial()
	_, _ = c.Write(helloOf(t, "a.example.com"))
	start := time.Now()
	if err := closedBy(t, c); err != io.EOF {
		t.Fatalf("want FIN, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("waited for a route that is down")
	}
}

func TestRoutesAreReplacedOnTheFly(t *testing.T) {
	r := newRig(t, nil, nil)
	a := r.lst["a"]
	// b goes, c comes, a stays with another name
	r.f.SetRoutes([]Route{{ID: "a", Names: []string{"a.example.com", "a2.example.com"}}, {ID: "c", Names: []string{"c.example.com"}}}, "a")
	if _, err := r.lst["b"].Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("the listener of a removed route: %v", err)
	}
	if r.f.Listener("b") != nil {
		t.Fatal("a removed route has a listener")
	}
	for sni, route := range map[string]net.Listener{"a2.example.com": a, "c.example.com": r.f.Listener("c"), "b.example.com": a} {
		c := r.dial()
		_, _ = c.Write(helloOf(t, sni))
		got, err := accept(t, route, 5*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", sni, err)
		}
		_ = got.Close()
	}
	s := r.f.Stats()
	if s.Delivered["a"] != 2 || s.Delivered["c"] != 1 || s.UnknownName != 1 {
		t.Fatalf("stats %+v", s)
	}
	if _, ok := s.Delivered["b"]; ok {
		t.Fatal("a removed route is still counted")
	}
}

func TestNoDefaultRouteClosesTheConnection(t *testing.T) {
	r := newRig(t, nil, nil)
	r.f.SetRoutes(routesAB(), "")
	c := r.dial()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\n"))
	if err := closedBy(t, c); err != io.EOF {
		t.Fatalf("got %v", err)
	}
	if s := r.f.Stats(); s.NoRoute != 1 || s.NotTLS != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestPanicOnOneConnectionLeavesTheFront(t *testing.T) {
	var n atomic.Int32
	r := newRig(t, nil, func(c net.Conn) net.Conn {
		if n.Add(1) == 1 {
			return panicConn{c}
		}
		return c
	})
	c := r.dial()
	if err := closedBy(t, c); err != io.EOF {
		t.Fatalf("the connection that made the handler panic: %v", err)
	}
	waitFor(t, "the panic to be counted", func() bool { return r.f.Stats().Panics == 1 })
	c = r.dial()
	_, _ = c.Write(helloOf(t, "b.example.com"))
	got, err := accept(t, r.lst["b"], 5*time.Second)
	if err != nil {
		t.Fatalf("the front is gone: %v", err)
	}
	_ = got.Close()
}

type panicConn struct{ net.Conn }

func (panicConn) SetReadDeadline(time.Time) error { panic("boom") }

// TestTrafficWhileRoutesAreReset: connections keep arriving at the right routes while the table is replaced.
func TestTrafficWhileRoutesAreReset(t *testing.T) {
	r := newRig(t, nil, nil)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				r.f.SetRoutes(routesAB(), "a")
			}
		}
	}()
	const n = 100
	for _, id := range []string{"a", "b"} {
		go func() {
			for range n {
				c, err := r.lst[id].Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
	}
	var clients []net.Conn
	for i := range 2 * n {
		c := r.dial()
		clients = append(clients, c)
		sni := []string{"a.example.com", "b.example.com"}[i%2]
		_, _ = c.Write(helloOf(t, sni))
	}
	waitFor(t, "every connection to be handed over", func() bool {
		s := r.f.Stats()
		return s.Delivered["a"] == n && s.Delivered["b"] == n
	})
}

func TestClose(t *testing.T) {
	r := newRig(t, nil, nil)
	quiet := r.dial()
	waitFor(t, "accept", func() bool { return r.f.Stats().Accepted == 1 })
	done := make(chan error, 1)
	go func() {
		_, err := r.lst["a"].Accept()
		done <- err
	}()
	if err := r.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close: %v", err)
	}
	if err := closedBy(t, quiet); err != io.EOF {
		t.Fatalf("a connection with no route yet: %v", err)
	}
	if err := r.f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplayConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	rc := newReplayConn(srv, []byte("abc"))
	if rc.Splice() {
		t.Fatal("unwrapped with the head unread")
	}
	buf := make([]byte, 2)
	if n, _ := rc.Read(buf); string(buf[:n]) != "ab" || rc.Splice() {
		t.Fatalf("first read %q", buf[:n])
	}
	if n, _ := rc.Read(buf); string(buf[:n]) != "c" || !rc.Splice() {
		t.Fatalf("second read %q", buf[:n])
	}
	if rc.RawConn() != srv {
		t.Fatal("RawConn is not the socket")
	}
	if _, ok := rc.RawConn().(*net.TCPConn); !ok {
		t.Fatal("not a TCP connection")
	}
	if _, err := rc.SyscallConn(); err != nil {
		t.Fatal(err)
	}
	_, _ = client.Write([]byte("de"))
	if n, _ := io.ReadFull(rc, buf); string(buf[:n]) != "de" {
		t.Fatalf("socket read %q", buf[:n])
	}
	if err := rc.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := closedBy(t, client); err != io.EOF {
		t.Fatalf("half-close: %v", err)
	}
}

// A service that took a connection and cannot serve it hands it, unread, to the default route (Divert): the default gets the
// hello and what followed. With no default route the caller keeps it.
func TestDivertGoesToTheDefault(t *testing.T) {
	r := newRig(t, nil, nil)
	hello := helloOf(t, "b.example.com")
	c := r.dial()
	_, _ = c.Write(append(bytes.Clone(hello), "after"...))
	took, err := accept(t, r.lst["b"], time.Second)
	if err != nil {
		t.Fatal(err)
	}
	diverted := make(chan bool, 1)
	go func() { diverted <- r.f.Divert(took) }() // it waits for the default route's Accept
	got, err := accept(t, r.lst["a"], time.Second)
	if err != nil {
		t.Fatalf("the default route got nothing: %v", err)
	}
	defer got.Close()
	want := append(bytes.Clone(hello), "after"...)
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(got, buf); err != nil || !bytes.Equal(buf, want) {
		t.Fatalf("the default route read %q, %v", buf, err)
	}
	if !<-diverted {
		t.Fatal("Divert refused with a default route")
	}
	if s := r.f.Stats(); s.Diverted != 1 || s.Delivered["b"] != 1 || s.Delivered["a"] != 1 || s.Refused != 0 {
		t.Fatalf("stats %+v", s)
	}

	r.f.SetRoutes(routesAB(), "")
	_, _ = r.dial().Write(helloOf(t, "b.example.com"))
	took, err = accept(t, r.lst["b"], time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer took.Close()
	if r.f.Divert(took) {
		t.Fatal("Divert took a connection with no default route")
	}
	if s := r.f.Stats(); s.NoRoute != 1 {
		t.Fatalf("stats %+v", s)
	}
}
