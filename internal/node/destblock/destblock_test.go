package destblock

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/destblock/dbtest"
)

func TestMatch(t *testing.T) {
	l := New([]string{"max.ru", "*.OneMe.ru", " .vk-test.example. "})
	for host, want := range map[string]bool{
		"max.ru":                  true,
		"api.max.ru":              true,
		"a.b.c.max.ru":            true,
		"MAX.RU":                  true,
		"Api.Max.Ru.":             true,
		"max.ru.":                 true,
		"oneme.ru":                true,
		"x.oneme.ru":              true,
		"vk-test.example":         true,
		"z.vk-test.example":       true,
		"3dmax.ru":                false,
		"xmax.ru":                 false,
		"max.ru.evil.example":     false,
		"max.ru.example.com":      false,
		"ru":                      false,
		"max.rux":                 false,
		"max":                     false,
		"":                        false,
		".":                       false,
		"xn--80ak6aa92e.com":      false,
		"192.0.2.1":               false,
		"2001:db8::1":             false,
		"::ffff:192.0.2.1":        false,
		"fe80::1%eth0":            false,
		"example.com":             false,
		"notvk-test.example":      false,
		"vk-test.example.com":     false,
		"api.xn--max-test.ru":     false,
		"sub.api.max.ru.example.": false,
	} {
		if got := l.Match(host); got != want {
			t.Errorf("Match(%q) = %v, want %v", host, got, want)
		}
	}
	// A punycode suffix is compared as written.
	if p := New([]string{"xn--e1afmkfd.test"}); !p.Match("www.xn--e1afmkfd.test") || p.Match("www.xn--e1afmkfd.example") {
		t.Error("punycode suffix")
	}
	// An IP literal never matches, even for an entry that looks like one.
	if New([]string{"192.0.2.1"}).Match("192.0.2.1") {
		t.Error("IP literal matched")
	}
}

func TestEmptyListBlocksNothing(t *testing.T) {
	for _, l := range []*List{nil, New(nil), New([]string{"", " ", "."})} {
		if !l.Empty() || l.Match("max.ru") {
			t.Errorf("list %+v blocks", l)
		}
	}
}

// pair returns the two ends of a loopback TCP connection; the server end is read by the test.
func pair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); acc <- c }()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server = <-acc
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// drain reads the server end until the client's end closes or half-closes.
func drain(t *testing.T, s net.Conn) []byte {
	t.Helper()
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(s)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("server read: %v", err)
	}
	return b
}

func chunks(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		k := min(n, len(b))
		out = append(out, b[:k])
		b = b[k:]
	}
	return out
}

func TestWrapTCPBlocksByName(t *testing.T) {
	list := New([]string{"max.ru"})
	for _, sni := range []string{"max.ru", "api.max.ru", "API.Max.RU"} {
		hello := dbtest.ClientHello(t, sni)
		for name, parts := range map[string][][]byte{
			"whole":  {hello},
			"two":    {hello[:len(hello)/2], hello[len(hello)/2:]},
			"header": {hello[:5], hello[5:]},
			"bytes":  chunks(hello, 1),
		} {
			t.Run(sni+"/"+name, func(t *testing.T) {
				c, s := pair(t)
				var blocked atomic.Int32
				w := WrapTCP(c, list, func() { blocked.Add(1) })
				for _, p := range parts {
					if n, err := w.Write(p); err != nil || n != len(p) {
						t.Fatalf("Write: %d, %v", n, err)
					}
				}
				if got := drain(t, s); len(got) != 0 {
					t.Fatalf("%d bytes reached the server", len(got))
				}
				if blocked.Load() != 1 {
					t.Fatalf("onBlock ran %d times", blocked.Load())
				}
				if _, err := w.Write([]byte("more")); err == nil {
					t.Fatal("write after a block succeeded")
				}
				if blocked.Load() != 1 {
					t.Fatal("onBlock ran again")
				}
			})
		}
	}
}

func TestWrapTCPPassesOtherNames(t *testing.T) {
	list := New([]string{"max.ru"})
	hello := dbtest.ClientHello(t, "3dmax.ru")
	for name, parts := range map[string][][]byte{
		"whole": {hello}, "two": {hello[:700], hello[700:]}, "bytes": chunks(hello, 1), "17": chunks(hello, 17),
	} {
		t.Run(name, func(t *testing.T) {
			c, s := pair(t)
			var blocked atomic.Int32
			w := WrapTCP(c, list, func() { blocked.Add(1) })
			for _, p := range parts {
				if n, err := w.Write(p); err != nil || n != len(p) {
					t.Fatalf("Write: %d, %v", n, err)
				}
			}
			tail := []byte("application data after the hello")
			if _, err := w.Write(tail); err != nil {
				t.Fatal(err)
			}
			w.(interface{ CloseWrite() error }).CloseWrite()
			if got := drain(t, s); !bytes.Equal(got, append(append([]byte(nil), hello...), tail...)) {
				t.Fatalf("server got %d bytes, want the hello and the tail unchanged", len(got))
			}
			if blocked.Load() != 0 {
				t.Fatal("onBlock ran")
			}
		})
	}
}

// What is not a TLS handshake goes out with the write that shows it, nothing is held back.
func TestWrapTCPNonTLSPassesAtOnce(t *testing.T) {
	list := New([]string{"max.ru"})
	for name, first := range map[string][]byte{
		"http":   []byte("GET / HTTP/1.1\r\nHost: max.ru\r\n\r\n"),
		"random": {0x01, 0x02, 0x03},
		"ssh":    []byte("SSH-2.0-test\r\n"),
		"badver": {0x16, 0x02, 0x00, 0x00, 0x10, 1, 2, 3},    // handshake type, but not TLS 3.x
		"tiny":   {0x16, 0x03, 0x01, 0x00, 0x02, 0x01, 0x02}, // a complete record that is no ClientHello
	} {
		t.Run(name, func(t *testing.T) {
			c, s := pair(t)
			w := WrapTCP(c, list, nil)
			if _, err := w.Write(first); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(first))
			s.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(s, got); err != nil || !bytes.Equal(got, first) {
				t.Fatalf("server got %q, %v", got, err)
			}
		})
	}
}

func TestWrapTCPHelloWithoutNamePasses(t *testing.T) {
	// A ClientHello with an IP-address server name carries no SNI extension.
	hello := dbtest.ClientHello(t, "192.0.2.7")
	c, s := pair(t)
	w := WrapTCP(c, New([]string{"max.ru"}), nil)
	w.Write(hello)
	got := make([]byte, len(hello))
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(s, got); err != nil || !bytes.Equal(got, hello) {
		t.Fatal("hello without a name did not pass")
	}
}

// A stream that stays a possible TLS record past the buffer limit is released as it is.
func TestWrapTCPBufferLimit(t *testing.T) {
	c, s := pair(t)
	w := WrapTCP(c, New([]string{"max.ru"}), nil)
	data := append([]byte{0x16, 0x03, 0x01, 0x7f, 0xff}, bytes.Repeat([]byte{'x'}, 2*maxSniff)...) // claims a 32 KiB record
	var want []byte
	for _, p := range chunks(data, 1000) {
		want = append(want, p...)
		if _, err := w.Write(p); err != nil {
			t.Fatal(err)
		}
		if len(want) < maxSniff {
			continue
		}
		break
	}
	got := make([]byte, len(want))
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(s, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("held bytes not released at the limit: %v", err)
	}
}

func TestWrapTCPHalfClose(t *testing.T) {
	list := New([]string{"max.ru"})
	hello := dbtest.ClientHello(t, "example.com")

	t.Run("before the hello is complete", func(t *testing.T) {
		c, s := pair(t)
		w := WrapTCP(c, list, nil)
		w.Write(hello[:10])
		if err := w.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if got := drain(t, s); !bytes.Equal(got, hello[:10]) {
			t.Fatalf("held bytes lost on half-close: %d", len(got))
		}
	})
	t.Run("answer still readable", func(t *testing.T) {
		c, s := pair(t)
		w := WrapTCP(c, list, nil)
		w.Write(hello)
		w.(interface{ CloseWrite() error }).CloseWrite()
		drain(t, s)
		if _, err := s.Write([]byte("reply")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 5)
		w.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(w, got); err != nil || string(got) != "reply" {
			t.Fatalf("read after CloseWrite: %q %v", got, err)
		}
	})
	t.Run("after a block", func(t *testing.T) {
		c, _ := pair(t)
		w := WrapTCP(c, New([]string{"example.com"}), nil)
		w.Write(hello)
		if err := w.(interface{ CloseWrite() error }).CloseWrite(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("CloseWrite after a block: %v", err)
		}
	})
}

func TestWrapTCPDeadlines(t *testing.T) {
	c, _ := pair(t)
	w := WrapTCP(c, New([]string{"max.ru"}), nil)
	if err := w.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline: %v", err)
	}
	w.Write([]byte("plain")) // decided
	w.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, err := w.Write([]byte("late")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write deadline: %v", err)
	}
	if w.LocalAddr() == nil || w.RemoteAddr() == nil {
		t.Fatal("addresses not passed through")
	}
}

// The list a connection was wrapped with is fixed, a list function may change under running connections; Close from
// another goroutine unblocks everything.
func TestWrapTCPConcurrent(t *testing.T) {
	hello := dbtest.ClientHello(t, "max.ru")
	other := dbtest.ClientHello(t, "example.com")
	var cur atomic.Pointer[List]
	cur.Store(New([]string{"max.ru"}))
	var wg sync.WaitGroup
	var blocked atomic.Int32
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, s := pair(t)
			w := WrapTCP(c, cur.Load(), func() { blocked.Add(1) })
			h := hello
			if i%2 == 1 {
				h = other
			}
			go func() { // a reader on the wrapper, and a concurrent Close
				io.Copy(io.Discard, w)
			}()
			for _, p := range chunks(h, 100) {
				w.Write(p)
			}
			w.Close()
			drain(t, s)
		}()
		cur.Store(New([]string{"max.ru", "example.test"})) // replaced while connections run
	}
	wg.Wait()
	if blocked.Load() != 8 {
		t.Fatalf("blocked %d connections, want 8", blocked.Load())
	}
}

func TestQUICGuard(t *testing.T) {
	list := New([]string{"max.ru"})
	for _, classic := range []bool{true, false} {
		for _, tc := range []struct {
			sni  string
			drop bool
		}{{"max.ru", true}, {"api.max.ru", true}, {"3dmax.ru", false}, {"example.com", false}} {
			pkts := dbtest.QUICInitial(t, tc.sni, classic)
			if want := map[bool]int{true: 1, false: 2}[classic]; len(pkts) != want {
				t.Fatalf("vector for %s: %d datagrams, want %d", tc.sni, len(pkts), want)
			}
			var g QUICGuard
			var dropped []bool
			for _, p := range pkts {
				before := append([]byte(nil), p...)
				dropped = append(dropped, g.Drop(list, "192.0.2.1", 443, p))
				if !bytes.Equal(p, before) {
					t.Fatal("the guard changed the datagram it was given")
				}
			}
			// A ClientHello in two datagrams is known only at the second; later datagrams of the flow follow it.
			if last := dropped[len(dropped)-1]; last != tc.drop {
				t.Errorf("%s classic=%v: dropped=%v, want %v", tc.sni, classic, dropped, tc.drop)
			}
			if got := g.Drop(list, "192.0.2.1", 443, []byte{0x40, 1, 2, 3, 4, 5, 6, 7, 8}); got != tc.drop {
				t.Errorf("%s: the next datagram dropped=%v, want %v", tc.sni, got, tc.drop)
			}
		}
	}
}

func TestQUICGuardNonQUICPasses(t *testing.T) {
	list := New([]string{"max.ru"})
	var g QUICGuard
	for _, p := range [][]byte{
		bytes.Repeat([]byte{0xC1}, 1200),                 // long header, unknown version
		{0xC0, 0, 0, 0, 1},                               // QUIC v1 header cut short
		append([]byte{0x12, 0x34, 0x01, 0x00}, "dns"...), // a DNS query
		nil,
	} {
		g = QUICGuard{}
		if g.Drop(list, "192.0.2.1", 53, p) || g.Drop(list, "192.0.2.1", 53, p) {
			t.Errorf("dropped %x", p)
		}
	}
	// Flows are bounded: past the limit datagrams pass unseen.
	for i := range maxQUICFlows + 5 {
		g.Drop(list, "192.0.2.9", uint16(1000+i), []byte("x"))
	}
	if len(g.flows) > maxQUICFlows {
		t.Fatalf("%d flows followed", len(g.flows))
	}
}
