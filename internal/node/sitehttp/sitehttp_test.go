package sitehttp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

type fakeConn struct {
	net.Conn
	addr   net.Addr
	closed atomic.Bool
}

func (c *fakeConn) RemoteAddr() net.Addr { return c.addr }
func (c *fakeConn) Close() error         { c.closed.Store(true); return nil }

func fakeSource(ip string) *fakeConn {
	return &fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}}
}

func addFake(l *AnonymousLimiter, c *fakeConn) {
	l.Add(c, c.RemoteAddr(), func() { _ = c.Close() })
}

func TestLimiterEvictsTheBusiestSourceAndSparesProvenConnections(t *testing.T) {
	l := NewAnonymousLimiter()
	var scanner []*fakeConn
	for i := 0; i < 3*maxAnonymousPerSource; i++ {
		c := fakeSource("198.51.100.7")
		scanner = append(scanner, c)
		addFake(l, c)
	}
	live := 0
	for _, c := range scanner {
		if !c.closed.Load() {
			live++
		}
	}
	if live != maxAnonymousPerSource {
		t.Fatalf("one source holds %d anonymous connections, want at most %d", live, maxAnonymousPerSource)
	}
	// The eviction takes the oldest of the source, not the newest.
	for i, c := range scanner {
		if want := i < len(scanner)-maxAnonymousPerSource; c.closed.Load() != want {
			t.Fatalf("scanner connection %d closed = %v, want %v", i, c.closed.Load(), want)
		}
	}

	// Four more sources fill the whole set; a fifth source still gets in, at the cost of the busiest source's oldest.
	for i := 0; len(l.anonymous) < maxAnonymousConnections; i++ {
		addFake(l, fakeSource(fmt.Sprintf("203.0.113.%d", 1+i%4)))
	}
	client := fakeSource("192.0.2.1")
	addFake(l, client)
	if len(l.anonymous) != maxAnonymousConnections {
		t.Fatalf("a connection at the limit left set size %d", len(l.anonymous))
	}
	if client.closed.Load() {
		t.Fatal("the new connection was refused")
	}
	var closedScanner int
	for _, c := range scanner {
		if c.closed.Load() {
			closedScanner++
		}
	}
	if closedScanner != len(scanner)-maxAnonymousPerSource+1 || !scanner[len(scanner)-maxAnonymousPerSource].closed.Load() || scanner[len(scanner)-maxAnonymousPerSource+1].closed.Load() {
		t.Fatalf("total-limit eviction closed %d scanner connections, want its oldest remaining connection", closedScanner)
	}
	// The client proves itself: it leaves the set, and no later flood can evict it.
	l.MarkAuthenticated(client)
	for i := 0; i < 500; i++ {
		addFake(l, fakeSource(fmt.Sprintf("100.64.%d.%d", i/200, i%200+1)))
	}
	if client.closed.Load() {
		t.Fatal("a proven connection was evicted by anonymous traffic")
	}
	if len(l.anonymous) > maxAnonymousConnections {
		t.Fatalf("anonymous set grew to %d", len(l.anonymous))
	}
}

func TestLimiterGroupsIPv6ByItsSlash64AndIPv4MappedWithIPv4(t *testing.T) {
	l := NewAnonymousLimiter()
	var conns []*fakeConn
	for i := 0; i < maxAnonymousPerSource+3; i++ {
		c := fakeSource(fmt.Sprintf("2001:db8:1:2:%x::1", i+1)) // one /64, a different address each time
		conns = append(conns, c)
		addFake(l, c)
	}
	live := 0
	for _, c := range conns {
		if !c.closed.Load() {
			live++
		}
	}
	if live != maxAnonymousPerSource {
		t.Fatalf("a /64 holds %d anonymous connections, want %d", live, maxAnonymousPerSource)
	}
	if connSource(fakeSource("::ffff:192.0.2.9").RemoteAddr()) != connSource(fakeSource("192.0.2.9").RemoteAddr()) {
		t.Fatal("an IPv4-mapped address is a different source than its IPv4 address")
	}
}

func TestLimiterGroupsUDPConnectionsByIPv4AndIPv6Slash64(t *testing.T) {
	l := NewAnonymousLimiter()
	var ipv6 []*fakeConn
	for i := 0; i < maxAnonymousPerSource+3; i++ {
		c := &fakeConn{addr: &net.UDPAddr{IP: net.ParseIP(fmt.Sprintf("2001:db8:1:2:%x::1", i+1)), Port: 40000 + i}}
		ipv6 = append(ipv6, c)
		addFake(l, c)
	}
	live := 0
	for _, c := range ipv6 {
		if !c.closed.Load() {
			live++
		}
	}
	if live != maxAnonymousPerSource {
		t.Fatalf("UDP connections from one IPv6 /64 alive = %d, want %d", live, maxAnonymousPerSource)
	}

	l = NewAnonymousLimiter()
	var ipv4 []*fakeConn
	for i := 0; i < maxAnonymousPerSource+2; i++ {
		c := &fakeConn{addr: &net.UDPAddr{IP: net.ParseIP("192.0.2.7"), Port: 41000 + i}}
		ipv4 = append(ipv4, c)
		addFake(l, c)
	}
	live = 0
	for _, c := range ipv4 {
		if !c.closed.Load() {
			live++
		}
	}
	if live != maxAnonymousPerSource {
		t.Fatalf("UDP connections from one IPv4 address alive = %d, want %d", live, maxAnonymousPerSource)
	}
}

func TestLimiterSourcesWithoutAnAddressShareOneKeyAndOnlyTheTotalLimitApplies(t *testing.T) {
	l := NewAnonymousLimiter()
	var conns []*fakeConn
	for i := 0; i < maxAnonymousConnections+5; i++ {
		c := &fakeConn{addr: &net.UnixAddr{Name: "x", Net: "unix"}}
		conns = append(conns, c)
		addFake(l, c)
	}
	live := 0
	for _, c := range conns {
		if !c.closed.Load() {
			live++
		}
	}
	// "" is no source: more than maxAnonymousPerSource of them are fine up to the total limit.
	if live != maxAnonymousConnections {
		t.Fatalf("unix-socket connections alive = %d, want %d", live, maxAnonymousConnections)
	}
}

// reference is the plain Go server with only the connection window raised (the bridge's first site wired it so); the
// fingerprint test pins ConfigureSiteHTTP2 to its SETTINGS.
func reference(server *http.Server) error {
	return http2.ConfigureServer(server, &http2.Server{MaxUploadBufferPerConnection: 16 << 20})
}

func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type serverFrames struct {
	settings []http2.Setting
	windows  []uint32
}

// handshakeFrames serves one h2 connection and returns the SETTINGS and WINDOW_UPDATE frames the server sent first.
func handshakeFrames(t *testing.T, configure func(*http.Server) error) serverFrames {
	t.Helper()
	cert := testCert(t)
	server := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}},
	}
	if err := configure(server); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(tls.NewListener(ln, server.TLSConfig))
	defer server.Close()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", NextProtos: []string{"h2"}})
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	var got serverFrames
	for len(got.settings) == 0 || len(got.windows) == 0 {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("reading the server's first frames: %v", err)
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if f.IsAck() {
				continue
			}
			_ = f.ForeachSetting(func(s http2.Setting) error { got.settings = append(got.settings, s); return nil })
		case *http2.WindowUpdateFrame:
			got.windows = append(got.windows, f.Increment)
		}
	}
	return got
}

// TestSettingsMatchTheBridgeSite pins the fingerprint: the SETTINGS and the connection WINDOW_UPDATE a site sends are the
// ones of a plain http2.ConfigureServer call, field for field, and not just numbers we believe are equal.
func TestSettingsMatchTheBridgeSite(t *testing.T) {
	want := handshakeFrames(t, reference)
	got := handshakeFrames(t, ConfigureSiteHTTP2)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("site frames differ from the bridge's:\n got  %+v\n want %+v", got, want)
	}
	if len(got.windows) != 1 || got.windows[0] != maxUploadBufferPerConn-65535 {
		t.Fatalf("connection window update = %v, want one raise to 16 MiB", got.windows)
	}
	byID := map[http2.SettingID]uint32{}
	for _, s := range got.settings {
		byID[s.ID] = s.Val
	}
	if byID[http2.SettingMaxConcurrentStreams] != 250 || byID[http2.SettingInitialWindowSize] != 1<<20 {
		t.Fatalf("settings = %v, want 250 streams and a 1 MiB stream window", byID)
	}
}

func TestConnAndMarkAuthenticatedThroughARealServer(t *testing.T) {
	cert := testCert(t)
	seen := make(chan [2]net.Conn, 1)
	server := &http.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- [2]net.Conn{Conn(r.Context()), MarkAuthenticated(r.Context())}
			_ = MarkAuthenticated(r.Context()) // twice is harmless
		}),
	}
	if err := ConfigureSiteHTTP2(server); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(tls.NewListener(ln, server.TLSConfig))
	defer server.Close()
	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "example.com"},
		DialTLSContext: func(ctx context.Context, network, _ string, c *tls.Config) (net.Conn, error) {
			return tls.Dial(network, ln.Addr().String(), c)
		}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	got := <-seen
	if got[0] == nil || got[0] != got[1] {
		t.Fatalf("Conn = %v, MarkAuthenticated = %v, want the same connection", got[0], got[1])
	}
	if Conn(context.Background()) != nil || MarkAuthenticated(context.Background()) != nil {
		t.Fatal("a context outside a configured server must yield no connection")
	}
}

func TestNilServerIsAnError(t *testing.T) {
	if err := ConfigureSiteHTTP2(nil); err == nil {
		t.Fatal("ConfigureSiteHTTP2(nil) returned no error")
	}
}

func TestLimiterIsCrowdedFromHalfFullAndNotForProvenConnections(t *testing.T) {
	l := NewAnonymousLimiter()
	if l.Crowded() {
		t.Fatal("an empty set is crowded")
	}
	var conns []*fakeConn
	for i := 0; i < maxAnonymousConnections/2; i++ {
		c := fakeSource(fmt.Sprintf("203.0.113.%d", 1+i)) // a source of its own each, so only the total counts
		conns = append(conns, c)
		if i < maxAnonymousConnections/2-1 && l.Crowded() {
			t.Fatalf("%d connections already count as crowded", i)
		}
		addFake(l, c)
	}
	if !l.Crowded() {
		t.Fatalf("%d anonymous connections do not count as crowded", maxAnonymousConnections/2)
	}
	l.MarkAuthenticated(conns[0])
	if l.Crowded() {
		t.Fatal("a connection that proved itself still counts towards the crowd")
	}
}

// The limits are the site's contract (32 anonymous connections, 8 from one source), not a figure a test can follow.
func TestAnonymousLimitsAreThoseOfTheSite(t *testing.T) {
	if maxAnonymousConnections != 32 || maxAnonymousPerSource != 8 {
		t.Fatalf("limits = %d total, %d per source, want 32 and 8", maxAnonymousConnections, maxAnonymousPerSource)
	}
}
