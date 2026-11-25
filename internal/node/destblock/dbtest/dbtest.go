// Package dbtest makes first flights for tests of destblock and its callers: a real TLS ClientHello and the UDP datagrams
// of a real QUIC client, produced by crypto/tls and quic-go in memory, no network. Not linked into the node.
package dbtest

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/apernet/quic-go"
)

// ClientHello returns the first TLS record a Go client sends for the server name sni.
func ClientHello(t testing.TB, sni string) []byte {
	t.Helper()
	cc, sc := net.Pipe()
	defer sc.Close()
	go func() {
		defer cc.Close()
		_ = tls.Client(cc, &tls.Config{ServerName: sni, InsecureSkipVerify: true}).Handshake()
	}()
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(sc, hdr); err != nil {
		t.Fatal(err)
	}
	rec := make([]byte, 5+int(hdr[3])<<8+int(hdr[4]))
	copy(rec, hdr)
	if _, err := io.ReadFull(sc, rec[5:]); err != nil {
		t.Fatal(err)
	}
	return rec
}

// QUICInitial returns the datagrams of the first flight of a QUIC client for sni. With classic true the ClientHello offers
// only X25519 and fits one datagram; otherwise it carries the post-quantum key share and takes two.
func QUICInitial(t testing.TB, sni string, classic bool) [][]byte {
	t.Helper()
	pc := &capture{closed: make(chan struct{}), wake: make(chan struct{}, 1)}
	conf := &tls.Config{ServerName: sni, NextProtos: []string{"h3"}, InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
	if classic {
		conf.CurvePreferences = []tls.CurveID{tls.X25519}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = quic.Dial(ctx, pc, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 443}, conf, nil)
	pc.Close()
	pc.mu.Lock()
	defer pc.mu.Unlock()
	// Dial retransmits the flight after its probe timeout; the first flight is whatever came before the first repeat.
	n := 1
	if !classic {
		n = 2
	}
	if len(pc.pkts) < n {
		t.Fatalf("quic client sent %d datagrams, want at least %d", len(pc.pkts), n)
	}
	return pc.pkts[:n]
}

type capture struct {
	mu     sync.Mutex
	pkts   [][]byte
	closed chan struct{}
	wake   chan struct{} // a read deadline in the past: quic-go uses it to stop its read loop
	once   sync.Once
}

func (c *capture) ReadFrom([]byte) (int, net.Addr, error) {
	select {
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-c.wake:
		return 0, nil, os.ErrDeadlineExceeded
	}
}
func (c *capture) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	c.pkts = append(c.pkts, append([]byte(nil), b...))
	c.mu.Unlock()
	return len(b), nil
}
func (c *capture) Close() error                  { c.once.Do(func() { close(c.closed) }); return nil }
func (c *capture) LocalAddr() net.Addr           { return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 5000} }
func (c *capture) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }
func (c *capture) SetReadDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return nil
}
func (c *capture) SetWriteDeadline(time.Time) error { return nil }
