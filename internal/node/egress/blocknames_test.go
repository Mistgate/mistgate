package egress

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/destblock"
	"github.com/mistgate/mistgate/internal/node/destblock/dbtest"
)

// sinkServer collects every byte a client sends over TCP, per connection, until it closes.
type sinkServer struct {
	addr  string
	mu    sync.Mutex
	got   [][]byte
	conns atomic.Int32
}

func newSink(t *testing.T) *sinkServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &sinkServer{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.conns.Add(1)
			go func() {
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(10 * time.Second))
				b, _ := io.ReadAll(c)
				s.mu.Lock()
				s.got = append(s.got, b)
				s.mu.Unlock()
			}()
		}
	}()
	return s
}

// bytesAfter waits until n connections have been closed and returns what they sent.
func (s *sinkServer) bytesAfter(t *testing.T, n int) []byte {
	t.Helper()
	for i := 0; i < 500; i++ {
		s.mu.Lock()
		if len(s.got) >= n {
			out := bytes.Join(s.got, nil)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the server saw fewer than %d finished connections", n)
	return nil
}

type counter struct{ n atomic.Int32 }

func (c *counter) hit() { c.n.Add(1) }

func listOf(l *destblock.List) func() *destblock.List { return func() *destblock.List { return l } }

// A name on the list is refused before the resolver and the dialer: the DNS server is never asked, nothing connects.
func TestBlockNamesRefusesDomainBeforeResolve(t *testing.T) {
	dnsAddr, queries := fakeDNS(t, map[string]string{"max.ru.": "127.0.0.1", "api.max.ru.": "127.0.0.1", "example.com.": "127.0.0.1"})
	srv := newSink(t)
	_, port, _ := net.SplitHostPort(srv.addr)
	var hits counter
	e := New(func() []string { return []string{dnsAddr} }, AllowPrivate(),
		BlockNames(listOf(destblock.New([]string{"max.ru"})), hits.hit))

	for _, host := range []string{"max.ru", "api.max.ru", "API.MAX.RU", "max.ru."} {
		c, err := e.TCP(host + ":" + port)
		if c != nil {
			c.Close()
		}
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("TCP %s: want ErrBlocked, got %v", host, err)
		}
	}
	if err := e.CheckUDP("max.ru:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("CheckUDP: %v", err)
	}
	if _, err := e.UDP("api.max.ru:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("UDP: %v", err)
	}
	if q := queries.Load(); q != 0 {
		t.Fatalf("the resolver was asked %d times for blocked names", q)
	}
	if n := srv.conns.Load(); n != 0 {
		t.Fatalf("%d connections reached the server", n)
	}
	if hits.n.Load() != 6 {
		t.Fatalf("onBlock ran %d times, want once per refused request (6)", hits.n.Load())
	}

	// A neighbour of the name, and an unrelated one, go through and are resolved.
	for _, host := range []string{"3dmax.ru", "max.ru.example.com", "example.com"} {
		dnsAddr2, _ := fakeDNS(t, map[string]string{host + ".": "127.0.0.1"})
		e2 := New(func() []string { return []string{dnsAddr2} }, AllowPrivate(),
			BlockNames(listOf(destblock.New([]string{"max.ru"})), hits.hit))
		c, err := e2.TCP(host + ":" + port)
		if err != nil {
			t.Fatalf("%s must pass: %v", host, err)
		}
		c.Close()
	}
}

// Every datagram to a domain is checked, so a session that is told a new destination meets the list; onBlock runs once for the session.
func TestBlockNamesUDPDomainPerDatagram(t *testing.T) {
	dnsAddr, _ := fakeDNS(t, map[string]string{"example.com.": "127.0.0.1"})
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	_, port, _ := net.SplitHostPort(sink.LocalAddr().String())
	var hits counter
	e := New(func() []string { return []string{dnsAddr} }, AllowPrivate(),
		BlockNames(listOf(destblock.New([]string{"max.ru"})), hits.hit))

	c, err := e.UDP("example.com:" + port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.WriteTo([]byte("ok"), "example.com:"+port); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := c.WriteTo([]byte("no"), "api.max.ru:443"); !errors.Is(err, ErrBlocked) {
			t.Fatalf("WriteTo a listed name: %v", err)
		}
	}
	buf := make([]byte, 16)
	sink.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _, err := sink.ReadFrom(buf); err != nil || string(buf[:n]) != "ok" {
		t.Fatalf("sink got %q, %v", buf[:n], err)
	}
	sink.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _, err := sink.ReadFrom(buf); err == nil {
		t.Fatalf("a refused datagram arrived: %q", buf[:n])
	}
	if hits.n.Load() != 1 {
		t.Fatalf("onBlock ran %d times for one session, want 1", hits.n.Load())
	}
}

func TestBlockNamesTCPByIP(t *testing.T) {
	list := destblock.New([]string{"max.ru"})
	blockedHello := dbtest.ClientHello(t, "api.max.ru")
	okHello := dbtest.ClientHello(t, "example.com")

	t.Run("listed name never reaches the server", func(t *testing.T) {
		srv := newSink(t)
		var hits counter
		e := New(nil, AllowPrivate(), BlockNames(listOf(list), hits.hit))
		c, err := e.TCP(srv.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		for _, p := range [][]byte{blockedHello[:3], blockedHello[3:200], blockedHello[200:]} {
			if _, err := c.Write(p); err != nil {
				t.Fatal(err)
			}
		}
		if got := srv.bytesAfter(t, 1); len(got) != 0 {
			t.Fatalf("%d bytes reached the server", len(got))
		}
		if hits.n.Load() != 1 {
			t.Fatalf("onBlock ran %d times", hits.n.Load())
		}
	})
	t.Run("other name arrives whole and in order", func(t *testing.T) {
		srv := newSink(t)
		var hits counter
		e := New(nil, AllowPrivate(), BlockNames(listOf(list), hits.hit))
		c, err := e.TCP(srv.addr)
		if err != nil {
			t.Fatal(err)
		}
		want := append(append([]byte(nil), okHello...), "payload"...)
		c.Write(okHello[:100])
		c.Write(okHello[100:])
		c.Write([]byte("payload"))
		c.Close()
		if got := srv.bytesAfter(t, 1); !bytes.Equal(got, want) {
			t.Fatalf("server got %d bytes, want %d unchanged", len(got), len(want))
		}
		if hits.n.Load() != 0 {
			t.Fatal("onBlock ran")
		}
	})
	t.Run("plain bytes pass at once", func(t *testing.T) {
		srv := newSink(t)
		e := New(nil, AllowPrivate(), BlockNames(listOf(list), nil))
		c, err := e.TCP(srv.addr)
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("GET / HTTP/1.1\r\nHost: max.ru\r\n\r\n"))
		c.Close()
		if got := srv.bytesAfter(t, 1); string(got) != "GET / HTTP/1.1\r\nHost: max.ru\r\n\r\n" {
			t.Fatalf("server got %q", got)
		}
	})
}

// Without the option, with a nil list function and with an empty list the connection is the dialer's own, not wrapped.
func TestBlockNamesOffChangesNothing(t *testing.T) {
	srv := newSink(t)
	for name, e := range map[string]*Direct{
		"no option":      New(nil, AllowPrivate()),
		"nil list":       New(nil, AllowPrivate(), BlockNames(func() *destblock.List { return nil }, nil)),
		"empty list":     New(nil, AllowPrivate(), BlockNames(listOf(destblock.New(nil)), nil)),
		"nil func":       New(nil, AllowPrivate(), BlockNames(nil, nil)),
		"list then none": New(nil, AllowPrivate(), BlockNames(listOf(nil), nil)),
	} {
		c, err := e.TCP(srv.addr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := c.(*net.TCPConn); !ok {
			t.Errorf("%s: the connection is %T, not the dialer's own", name, c)
		}
		c.Close()
	}
}

// The list function is asked per dial: a list replaced while connections run applies to the next one.
func TestBlockNamesSwapUnderLoad(t *testing.T) {
	srv := newSink(t)
	hello := dbtest.ClientHello(t, "max.ru")
	var cur atomic.Pointer[destblock.List]
	var hits counter
	e := New(nil, AllowPrivate(), BlockNames(func() *destblock.List { return cur.Load() }, hits.hit))

	if c, err := e.TCP(srv.addr); err != nil { // no list yet: not wrapped, the hello goes out
		t.Fatal(err)
	} else {
		c.Write(hello)
		c.Close()
	}
	if got := srv.bytesAfter(t, 1); !bytes.Equal(got, hello) {
		t.Fatal("hello did not pass without a list")
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				cur.Store(destblock.New([]string{"max.ru"}))
			} else {
				cur.Store(nil)
			}
		}
	}()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := e.TCP(srv.addr); err == nil {
				c.Write(hello)
				c.Close()
			}
			e.CheckUDP("max.ru:443")
		}()
	}
	wg.Wait()
	close(stop)
	cur.Store(destblock.New([]string{"max.ru"}))
	before := hits.n.Load()
	if _, err := e.TCP("max.ru:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("after the swap: %v", err)
	}
	if hits.n.Load() != before+1 {
		t.Fatal("onBlock count after the swap")
	}
}

func TestBlockNamesUDPByIP(t *testing.T) {
	list := destblock.New([]string{"max.ru"})
	for _, classic := range []bool{true, false} {
		blocked := dbtest.QUICInitial(t, "max.ru", classic)
		allowed := dbtest.QUICInitial(t, "example.com", classic)

		sink, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer sink.Close()
		addr := sink.LocalAddr().String()
		var hits counter
		e := New(nil, AllowPrivate(), BlockNames(listOf(list), hits.hit))

		// read collects what reaches the sink for 300 ms.
		read := func() (out [][]byte) {
			buf := make([]byte, 2000)
			for {
				sink.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				n, _, err := sink.ReadFrom(buf)
				if err != nil {
					return out
				}
				out = append(out, append([]byte(nil), buf[:n]...))
			}
		}

		// A flow to a listed name: its last datagram, and everything after, is dropped without an error.
		c, err := e.UDP(addr)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range blocked {
			if n, err := c.WriteTo(p, addr); err != nil || n != len(p) {
				t.Fatalf("WriteTo: %d, %v", n, err)
			}
		}
		c.WriteTo([]byte{0x40, 1, 2, 3, 4, 5}, addr)
		got := read()
		if want := len(blocked) - 1; len(got) != want {
			t.Fatalf("classic=%v: %d datagrams reached the sink, want %d", classic, len(got), want)
		}
		if hits.n.Load() != 1 {
			t.Fatalf("onBlock ran %d times", hits.n.Load())
		}
		c.Close()

		// Another name, and not-QUIC UDP, pass whole and in order.
		c2, err := e.UDP(addr)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range allowed {
			c2.WriteTo(p, addr)
		}
		got = read()
		if len(got) != len(allowed) {
			t.Fatalf("classic=%v: %d datagrams of an allowed name reached the sink, want %d", classic, len(got), len(allowed))
		}
		for i := range got {
			if !bytes.Equal(got[i], allowed[i]) {
				t.Fatalf("datagram %d changed", i)
			}
		}
		c2.Close()

		c3, _ := e.UDP(addr)
		c3.WriteTo([]byte("hello, not quic"), addr)
		if got = read(); len(got) != 1 || string(got[0]) != "hello, not quic" {
			t.Fatalf("plain UDP: %q", got)
		}
		c3.Close()
		if hits.n.Load() != 1 {
			t.Fatalf("onBlock ran %d times in total", hits.n.Load())
		}
	}
}
