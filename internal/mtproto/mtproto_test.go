package mtproto

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/mtproto/mtprototest"
)

const testDomain = "de1.example.com"

func testKey(b byte) [KeyLen]byte {
	var k [KeyLen]byte
	for i := range k {
		k[i] = b<<4 | byte(i)
	}
	return k
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func (c *clock) Unix() uint32        { return uint32(c.Now().Unix()) }

type recorder struct {
	mu             sync.Mutex
	opened, closed []SessionInfo
	up, down       map[string]uint64
}

func (r *recorder) Opened(i SessionInfo) { r.mu.Lock(); r.opened = append(r.opened, i); r.mu.Unlock() }
func (r *recorder) Closed(i SessionInfo) { r.mu.Lock(); r.closed = append(r.closed, i); r.mu.Unlock() }
func (r *recorder) AddTraffic(s SessionInfo, up, down uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.up == nil {
		r.up, r.down = map[string]uint64{}, map[string]uint64{}
	}
	r.up[s.CredentialID] += up
	r.down[s.CredentialID] += down
}

func (r *recorder) counts() (opened, closed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.opened), len(r.closed)
}

type harness struct {
	t         *testing.T
	srv       *Server
	addr      string
	clk       *clock
	dc        *mtprototest.DC
	dcSeen    chan [2]int64 // tag, DC as the fake DC decoded them
	obs       *recorder
	fallbacks chan []byte
	errs      chan error
}

// newHarness runs a server on loopback with credentials c1 and c2, the fake DC behind every DC, and a fallback that
// records the bytes it was handed and answers "decoy".
func newHarness(t *testing.T, mutate func(*Config, *harness), dcServe func(*mtprototest.DCConn)) *harness {
	t.Helper()
	h := &harness{
		t: t, clk: &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}, obs: &recorder{},
		dcSeen: make(chan [2]int64, 64), fallbacks: make(chan []byte, 64), errs: make(chan error, 256),
	}
	if dcServe == nil {
		dcServe = mtprototest.Echo
	}
	h.dc = mtprototest.NewDC(t, func(c *mtprototest.DCConn) {
		h.dcSeen <- [2]int64{int64(c.Tag), int64(c.DC)}
		dcServe(c)
	})
	cfg := Config{
		Credentials:      []Credential{{ID: "c1", Key: testKey(1)}, {ID: "c2", Key: testKey(2)}},
		Domains:          []string{testDomain},
		Now:              h.clk.Now,
		Observer:         h.obs,
		HandshakeTimeout: 5 * time.Second,
		StatsInterval:    20 * time.Millisecond,
		DCAddress:        func(int16) (string, bool) { return h.dc.Addr, true },
		Fallback: func(_ context.Context, conn net.Conn, initial []byte) error {
			h.fallbacks <- bytes.Clone(initial)
			_, _ = conn.Write([]byte("decoy"))
			_ = conn.(*net.TCPConn).CloseWrite()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.Copy(io.Discard, conn) // what the server did not read, so the close is not a reset
			return conn.Close()
		},
	}
	if mutate != nil {
		mutate(&cfg, h)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		srv.CloseAll()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.errs <- srv.Serve(ctx, c)
			}()
		}
	}()
	return h
}

func (h *harness) dial() net.Conn {
	h.t.Helper()
	c, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return c
}

func (h *harness) hello(key [KeyLen]byte) []byte {
	return mtprototest.Hello(key, testDomain, h.clk.Unix())
}

// client runs the whole client handshake.
func (h *harness) client(key [KeyLen]byte, tag uint32, dc int16) *mtprototest.Client {
	h.t.Helper()
	c, err := mtprototest.Start(h.dial(), key, h.hello(key))
	if err != nil {
		h.t.Fatalf("handshake: %v", err)
	}
	if err := c.SendHeader(tag, dc); err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) serveErr() error {
	h.t.Helper()
	select {
	case err := <-h.errs:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("Serve did not return")
		return nil
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

func TestParseCredential(t *testing.T) {
	const key = "00112233445566778899aabbccddeeff"
	domain := hex.EncodeToString([]byte("de1.example.com"))
	for _, tc := range []struct {
		name, secret string
		ok           bool
	}{
		{"bare", key, true},
		{"bare upper case", strings.ToUpper(key), true},
		{"dd", "dd" + key, true},
		{"ee with domain", "ee" + key + domain, true},
		{"EE prefix", "EE" + key + domain, true},
		{"empty", "", false},
		{"short", key[:30], false},
		{"not hex", "zz" + key[2:], false},
		{"dd with extra", "dd" + key + "00", false},
		{"ee without domain", "ee" + key, false},
		{"ee odd domain hex", "ee" + key + "abc", false},
		{"ee domain not a host", "ee" + key + hex.EncodeToString([]byte("no dots here")), false},
		{"unknown prefix", "ff" + key, false},
	} {
		c, err := ParseCredential("c1", tc.secret)
		if tc.ok != (err == nil) {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), key) {
				t.Fatalf("%s: the error quotes the key", tc.name)
			}
			continue
		}
		if c.ID != "c1" || hex.EncodeToString(c.Key[:]) != key {
			t.Fatalf("%s: %s %x", tc.name, c.ID, c.Key)
		}
	}
	if _, err := ParseCredential("", key); err == nil {
		t.Fatal("empty id accepted")
	}
}

func TestHelloSNI(t *testing.T) {
	rec := mtprototest.Hello(testKey(1), "fi1.example.com", 0)
	if got := HelloSNI(rec); got != "fi1.example.com" {
		t.Fatalf("HelloSNI = %q", got)
	}
	if got := HelloSNI(rec[:len(rec)-1]); got != "" {
		t.Fatalf("truncated: %q", got)
	}
	if got := HelloSNI([]byte("GET / HTTP/1.1\r\n")); got != "" {
		t.Fatalf("http: %q", got)
	}
}

func TestNewRefusesNoDomain(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("a server without domains")
	}
	if _, err := New(Config{Domains: []string{"not a host"}}); err == nil {
		t.Fatal("a bad domain")
	}
	if _, err := New(Config{Domains: []string{testDomain}, Credentials: []Credential{{ID: "a", Key: testKey(1)}, {ID: "b", Key: testKey(1)}}}); err == nil {
		t.Fatal("two credentials with one key")
	}
}

// TestClientThroughProxy runs the spec-built client through the server to the fake DC for each transport tag and a
// main, a media and the extra DC.
func TestClientThroughProxy(t *testing.T) {
	for _, tc := range []struct {
		tag uint32
		dc  int16
	}{{mtprototest.TagPadded, 2}, {mtprototest.TagIntermediate, -4}, {mtprototest.TagAbridged, 203}} {
		var asked atomic.Int32
		h := newHarness(t, func(c *Config, h *harness) {
			c.DCAddress = func(dc int16) (string, bool) { asked.Store(int32(dc)); return h.dc.Addr, true }
		}, nil)
		c := h.client(testKey(2), tc.tag, tc.dc)
		if _, err := c.Write([]byte("hello dc")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len("dc:hello dc"))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != "dc:hello dc" {
			t.Fatalf("relay: %q %v", got, err)
		}
		_ = c.Conn.Close()
		waitFor(t, "session close", func() bool { _, closed := h.obs.counts(); return closed == 1 })

		info := h.obs.opened[0]
		if info.CredentialID != "c2" || info.DC != tc.dc || info.Tag != Tag(tc.tag) || info.SNI != testDomain || info.ID == 0 {
			t.Fatalf("session %+v", info)
		}
		if asked.Load() != int32(tc.dc) {
			t.Fatalf("DCAddress asked for %d, want %d", asked.Load(), tc.dc)
		}
		if h.obs.up["c2"] != uint64(len("hello dc")) || h.obs.down["c2"] != uint64(len("dc:hello dc")) {
			t.Fatalf("traffic up=%d down=%d", h.obs.up["c2"], h.obs.down["c2"])
		}
		seen := <-h.dcSeen
		if uint32(seen[0]) != tc.tag || int16(seen[1]) != tc.dc {
			t.Fatalf("the DC got tag %#x dc %d", seen[0], seen[1])
		}
		hdr := <-h.dc.Headers
		if bytes.Equal(hdr[8:56], c.HeaderKeys) || reservedStart(&hdr) {
			t.Fatal("the DC header reuses the client's keys or starts like another transport")
		}
		if len(h.fallbacks) != 0 {
			t.Fatal("an authenticated client reached the fallback")
		}
	}
}

func readAll(t *testing.T, c net.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// TestFallbackGetsEveryByte: everything that is not an authenticated client of the domain goes to the fallback with
// exactly the bytes the server read.
func TestFallbackGetsEveryByte(t *testing.T) {
	h := newHarness(t, nil, nil)
	unix := h.clk.Unix()
	k := testKey(1)
	oversize := append([]byte{0x16, 3, 1, 0x13, 0x88}, make([]byte, 100)...) // claims 5000 bytes
	for _, tc := range []struct {
		name string
		sent []byte
		want int // bytes the fallback must get; 0: all of sent
	}{
		{"wrong sni", mtprototest.Hello(k, "fi1.example.com", unix), 0},
		{"wrong key", mtprototest.Hello(testKey(9), testDomain, unix), 0},
		{"clock an hour behind", mtprototest.Hello(k, testDomain, unix-3600), 0},
		{"clock an hour ahead", mtprototest.Hello(k, testDomain, unix+3600), 0},
		{"plain http", []byte("GET / HTTP/1.1\r\nHost: de1.example.com\r\n\r\n"), 5},
		{"oversize hello", oversize, 5},
	} {
		conn := h.dial()
		if _, err := conn.Write(tc.sent); err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, conn); got != "decoy" {
			t.Fatalf("%s: read %q", tc.name, got)
		}
		want := tc.sent
		if tc.want != 0 {
			want = tc.sent[:tc.want]
		}
		if got := <-h.fallbacks; !bytes.Equal(got, want) {
			t.Fatalf("%s: the fallback got %d bytes, want %d", tc.name, len(got), len(want))
		}
	}

	// A short probe that closes its side.
	conn := h.dial()
	_, _ = conn.Write([]byte("abc"))
	_ = conn.(*net.TCPConn).CloseWrite()
	if got := readAll(t, conn); got != "decoy" || string(<-h.fallbacks) != "abc" {
		t.Fatalf("short probe: %q", got)
	}

	// A replayed hello: the first copy authenticates, the second goes to the fallback whole.
	hello := h.hello(k)
	if _, err := mtprototest.Start(h.dial(), k, hello); err != nil {
		t.Fatal(err)
	}
	conn = h.dial()
	_, _ = conn.Write(hello)
	if got := readAll(t, conn); got != "decoy" || !bytes.Equal(<-h.fallbacks, hello) {
		t.Fatalf("replay: %q", got)
	}

	// A browser asking for the domain: its whole ClientHello reaches the fallback.
	raw := h.dial()
	_ = tls.Client(raw, &tls.Config{ServerName: testDomain, InsecureSkipVerify: true}).Handshake()
	if got := <-h.fallbacks; HelloSNI(got) != testDomain {
		t.Fatalf("browser: the fallback got %d bytes without the hello", len(got))
	}
}

// TestRevocationDuringHandshake: a credential removed or kicked after a client authenticated with it must not open a
// session, whether the removal lands before the obfuscated2 header or during the DC dial.
func TestRevocationDuringHandshake(t *testing.T) {
	t.Run("removed before the header", func(t *testing.T) {
		h := newHarness(t, nil, nil)
		c, err := mtprototest.Start(h.dial(), testKey(1), h.hello(testKey(1)))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.srv.SetCredentials([]Credential{{ID: "c2", Key: testKey(2)}}); err != nil {
			t.Fatal(err)
		}
		_ = c.SendHeader(mtprototest.TagPadded, 2)
		if err := h.serveErr(); !errors.Is(err, errRevoked) {
			t.Fatalf("Serve = %v", err)
		}
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("the connection stayed open")
		}
		if h.dc.Accepted.Load() != 0 {
			t.Fatal("the DC was dialed")
		}
		if opened, _ := h.obs.counts(); opened != 0 {
			t.Fatal("a session opened")
		}
	})
	t.Run("kicked before the header", func(t *testing.T) {
		h := newHarness(t, nil, nil)
		c, err := mtprototest.Start(h.dial(), testKey(1), h.hello(testKey(1)))
		if err != nil {
			t.Fatal(err)
		}
		if n := h.srv.CloseCredential("c1"); n != 0 {
			t.Fatalf("CloseCredential = %d", n)
		}
		_ = c.SendHeader(mtprototest.TagPadded, 2)
		if err := h.serveErr(); !errors.Is(err, errRevoked) {
			t.Fatalf("Serve = %v", err)
		}
		// The credential is still accepted for a new connection.
		c = h.client(testKey(1), mtprototest.TagPadded, 2)
		_, _ = c.Write([]byte("x"))
		if _, err := io.ReadFull(c, make([]byte, 4)); err != nil {
			t.Fatalf("a new connection after the kick: %v", err)
		}
		if n := h.srv.CloseCredential("c1"); n != 1 {
			t.Fatalf("CloseCredential of the open session = %d", n)
		}
	})
	t.Run("removed during the dial", func(t *testing.T) {
		dialing, release := make(chan struct{}), make(chan struct{})
		h := newHarness(t, func(c *Config, _ *harness) {
			c.Dialer = dialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
				close(dialing)
				<-release
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			})
		}, nil)
		c := h.client(testKey(1), mtprototest.TagPadded, 2)
		<-dialing
		if err := h.srv.SetCredentials(nil); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := h.serveErr(); !errors.Is(err, errRevoked) {
			t.Fatalf("Serve = %v", err)
		}
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("the client connection stayed open")
		}
		if opened, _ := h.obs.counts(); opened != 0 {
			t.Fatal("a session opened")
		}
		select {
		case <-h.dc.Headers:
			t.Fatal("the DC got a header")
		case <-time.After(50 * time.Millisecond):
		}
	})
}

type dialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

func TestReplayCacheBound(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newReplayCache(time.Minute, 2, 2)
	k1, k2, k3 := [16]byte{1}, [16]byte{2}, [16]byte{3}
	if c.add(k1, "c1", now) != replayAdded || c.add(k2, "c2", now) != replayAdded {
		t.Fatal("add")
	}
	if c.add(k3, "c3", now) != replayFull {
		t.Fatal("a full cache took a new entry")
	}
	if !c.has(k1, now) || c.add(k1, "c1", now) != replaySeen {
		t.Fatal("an unexpired entry was dropped")
	}
	later := now.Add(time.Minute + time.Second)
	if c.add(k3, "c3", later) != replayAdded || c.has(k1, later) {
		t.Fatal("expired entries were not dropped")
	}

	// The server refuses a client while the cache is full of live entries, and takes it once they expire.
	h := newHarness(t, func(c *Config, _ *harness) { c.ReplayCacheSize = 1 }, nil)
	h.client(testKey(1), mtprototest.TagPadded, 2)
	if _, err := mtprototest.Start(h.dial(), testKey(2), h.hello(testKey(2))); !errors.Is(err, mtprototest.ErrNotProxy) {
		t.Fatalf("a client got through a full cache: %v", err)
	}
	<-h.fallbacks
	h.clk.Add(11 * time.Minute)
	h.client(testKey(2), mtprototest.TagPadded, 2)
}

// TestReplayQuotaPerCredential: one key holder making fresh hellos fills only its own share of the replay cache; the
// other credentials' clients still get in, and the warning is logged once, not per hello.
func TestReplayQuotaPerCredential(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newReplayCache(time.Minute, 100, 2)
	if c.add([16]byte{1}, "c1", now) != replayAdded || c.add([16]byte{2}, "c1", now) != replayAdded {
		t.Fatal("add")
	}
	if c.add([16]byte{3}, "c1", now) != replayQuota {
		t.Fatal("a credential went over its share")
	}
	if c.add([16]byte{4}, "c2", now) != replayAdded {
		t.Fatal("another credential was refused")
	}
	if c.add([16]byte{3}, "c1", now.Add(61*time.Second)) != replayAdded {
		t.Fatal("the share did not free up when the entries expired")
	}

	var logs syncBuffer
	h := newHarness(t, func(c *Config, _ *harness) {
		c.ReplayPerCredential = 2
		c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	}, nil)
	h.client(testKey(1), mtprototest.TagPadded, 2)
	h.client(testKey(1), mtprototest.TagPadded, 2)
	for range 3 {
		if _, err := mtprototest.Start(h.dial(), testKey(1), h.hello(testKey(1))); !errors.Is(err, mtprototest.ErrNotProxy) {
			t.Fatalf("a credential over its share got through: %v", err)
		}
		<-h.fallbacks
	}
	h.client(testKey(2), mtprototest.TagPadded, 2)
	if n := strings.Count(logs.String(), "share of the replay cache"); n != 1 {
		t.Fatalf("the quota warning was logged %d times", n)
	}
}

// TestFailureBudgetTwoSlots: an address limited by a spent neighbour only when both of its slots are spent.
func TestFailureBudgetTwoSlots(t *testing.T) {
	g := newGuard(1, 1)
	now := time.Unix(1_800_000_000, 0)
	attacker := netip.MustParsePrefix("2001:db8::/64")
	ai, aj := g.slots(attacker)
	var neighbour netip.Prefix
	for n := 1; n < 1<<22 && !neighbour.IsValid(); n++ {
		p := netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(n >> 16), byte(n >> 8), byte(n)}), 64)
		bi, bj := g.slots(p)
		shared := (bi == ai || bi == aj) != (bj == ai || bj == aj) // exactly one slot shared
		if shared && bi != bj {
			neighbour = p
		}
	}
	if !neighbour.IsValid() {
		t.Fatal("no address sharing one slot found")
	}
	for range failBurst {
		if _, ok := g.spend(attacker, now); !ok {
			t.Fatal("spend")
		}
	}
	if _, ok := g.spend(attacker, now); ok {
		t.Fatal("a spent budget allowed a scan")
	}
	if _, ok := g.spend(neighbour, now); !ok {
		t.Fatal("an address sharing one slot with a spent one was limited")
	}
}

// TestDCDialShares: a first DC address that drops packets gets only its share of the handshake time, so the next one
// is still tried.
func TestDCDialShares(t *testing.T) {
	var calls atomic.Int32
	var firstBudget atomic.Int64
	h := newHarness(t, func(c *Config, h *harness) {
		c.DCAddress = nil // the table: DC 2 has three addresses, all mapped to the fake DC below
		c.HandshakeTimeout = 3 * time.Second
		c.Dialer = dialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
			if calls.Add(1) == 1 {
				dl, _ := ctx.Deadline()
				firstBudget.Store(int64(time.Until(dl)))
				<-ctx.Done() // blackholed
				return nil, ctx.Err()
			}
			return (&net.Dialer{}).DialContext(ctx, network, h.dc.Addr)
		})
	}, nil)
	c := h.client(testKey(1), mtprototest.TagPadded, 2)
	_, _ = c.Write([]byte("x"))
	if _, err := io.ReadFull(c, make([]byte, 4)); err != nil {
		t.Fatalf("the second address was not tried in time: %v", err)
	}
	if b := time.Duration(firstBudget.Load()); b > 1100*time.Millisecond {
		t.Fatalf("the first address got %v of a 3 s handshake with three addresses", b)
	}
}

// TestSessionLimits: a credential's sessions are capped (others are not affected), and a session idle for
// IdleTimeout is closed while a busy one stays.
func TestSessionLimits(t *testing.T) {
	t.Run("per credential", func(t *testing.T) {
		h := newHarness(t, func(c *Config, _ *harness) { c.MaxSessionsPerCredential = 2 }, nil)
		echo := func(c *mtprototest.Client) error {
			_, _ = c.Write([]byte("x"))
			_ = c.Conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, err := io.ReadFull(c, make([]byte, 4))
			return err
		}
		first := h.client(testKey(1), mtprototest.TagPadded, 2)
		second := h.client(testKey(1), mtprototest.TagPadded, 2)
		if echo(first) != nil || echo(second) != nil {
			t.Fatal("sessions under the limit")
		}
		third := h.client(testKey(1), mtprototest.TagPadded, 2)
		if err := echo(third); err == nil {
			t.Fatal("a session over the credential's limit")
		}
		if err := echo(h.client(testKey(2), mtprototest.TagPadded, 2)); err != nil {
			t.Fatalf("another credential was limited: %v", err)
		}
		_ = first.Conn.Close()
		waitFor(t, "the session to end", func() bool { _, closed := h.obs.counts(); return closed == 1 })
		if err := echo(h.client(testKey(1), mtprototest.TagPadded, 2)); err != nil {
			t.Fatalf("a session after one ended: %v", err)
		}
	})
	t.Run("idle", func(t *testing.T) {
		h := newHarness(t, func(c *Config, _ *harness) { c.IdleTimeout = 150 * time.Millisecond }, nil)
		idle := h.client(testKey(1), mtprototest.TagPadded, 2)
		busy := h.client(testKey(2), mtprototest.TagPadded, 2)
		for range 8 {
			time.Sleep(50 * time.Millisecond)
			_, _ = busy.Write([]byte("x"))
			if _, err := io.ReadFull(busy, make([]byte, 4)); err != nil {
				t.Fatalf("a busy session was closed: %v", err)
			}
		}
		_ = idle.Conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := idle.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("an idle session stayed open: %v", err)
		}
	})
}

// TestServeListenerAdmitsFirst: connections over the handshake limits are closed by the accept loop, without a
// goroutine each.
func TestServeListenerAdmitsFirst(t *testing.T) {
	srv, err := New(Config{Domains: []string{testDomain}, MaxHandshakesPerAddr: 2, HandshakeTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(context.Background(), ln) }()
	var conns []net.Conn
	for range 22 {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	waitFor(t, "the refused connections to be closed", func() bool {
		closed := 0
		for _, c := range conns[2:] {
			_ = c.SetReadDeadline(time.Now().Add(time.Millisecond))
			if _, err := c.Read(make([]byte, 1)); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
				closed++
			}
		}
		return closed == 20
	})
	if n := srv.spawned.Load(); n != 2 {
		t.Fatalf("%d goroutines for 2 admitted connections", n)
	}
	_ = ln.Close()
	srv.CloseAll()
	for _, c := range conns {
		_ = c.Close()
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeListener = %v", err)
	}
}

func TestHandshakeLimits(t *testing.T) {
	refused := func(t *testing.T, h *harness) {
		t.Helper()
		start := time.Now()
		conn := h.dial()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err := conn.Read(make([]byte, 1))
		if err == nil || errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 2*time.Second {
			t.Fatalf("the connection over the limit was not closed at once: %v", err)
		}
	}
	t.Run("per address", func(t *testing.T) {
		h := newHarness(t, func(c *Config, _ *harness) { c.MaxHandshakesPerAddr = 2 }, nil)
		h.dial()
		h.dial()
		waitFor(t, "two handshakes", func() bool { h.srv.guard.mu.Lock(); defer h.srv.guard.mu.Unlock(); return h.srv.guard.total == 2 })
		refused(t, h)
		if err := h.serveErr(); !errors.Is(err, ErrBusy) {
			t.Fatalf("Serve = %v", err)
		}
	})
	t.Run("per server", func(t *testing.T) {
		h := newHarness(t, func(c *Config, _ *harness) { c.MaxHandshakes, c.MaxHandshakesPerAddr = 2, 10 }, nil)
		h.dial()
		h.dial()
		waitFor(t, "two handshakes", func() bool { h.srv.guard.mu.Lock(); defer h.srv.guard.mu.Unlock(); return h.srv.guard.total == 2 })
		refused(t, h)
	})
	t.Run("deadline", func(t *testing.T) {
		h := newHarness(t, func(c *Config, _ *harness) { c.HandshakeTimeout = 300 * time.Millisecond }, nil)
		conn := h.dial()
		_, _ = conn.Write([]byte{0x16, 3})
		start := time.Now()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err := conn.Read(make([]byte, 1))
		if err == nil || errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) < 200*time.Millisecond {
			t.Fatalf("a stalled handshake: %v after %v", err, time.Since(start))
		}
	})
}

// TestFailureBudget: hellos for the domain that fail the HMAC cost one scan per credential until the address's budget
// is spent, then nothing; hellos for another domain never cost a scan.
func TestFailureBudget(t *testing.T) {
	h := newHarness(t, nil, nil)
	send := func(hello []byte) {
		t.Helper()
		conn := h.dial()
		_, _ = conn.Write(hello)
		readAll(t, conn)
		<-h.fallbacks
	}
	for range 50 {
		send(mtprototest.Hello(testKey(1), "fi1.example.com", h.clk.Unix()))
	}
	if n := h.srv.scans.Load(); n != 0 {
		t.Fatalf("hellos for another domain cost %d HMACs", n)
	}
	for range failBurst {
		send(mtprototest.Hello(testKey(9), testDomain, h.clk.Unix()))
	}
	if n := h.srv.scans.Load(); n != failBurst*2 {
		t.Fatalf("scans = %d, want %d", n, failBurst*2)
	}
	send(mtprototest.Hello(testKey(9), testDomain, h.clk.Unix()))
	if n := h.srv.scans.Load(); n != failBurst*2 {
		t.Fatalf("a spent budget still scanned (%d)", n)
	}
	// A spent budget turns the address's clients away too, until it refills.
	if _, err := mtprototest.Start(h.dial(), testKey(1), h.hello(testKey(1))); !errors.Is(err, mtprototest.ErrNotProxy) {
		t.Fatalf("a client got through a spent budget: %v", err)
	}
	<-h.fallbacks
	h.clk.Add(failInterval)
	h.client(testKey(1), mtprototest.TagPadded, 2)
	// The scan that authenticated was refunded: one more failure fits.
	before := h.srv.scans.Load()
	send(mtprototest.Hello(testKey(9), testDomain, h.clk.Unix()))
	if h.srv.scans.Load() == before {
		t.Fatal("the authenticated scan was not refunded")
	}
}

// TestHalfClose: the client ends its side cleanly; the DC sees EOF, still answers, and the answer reaches the client
// before its EOF. A reset on one side closes both.
func TestHalfClose(t *testing.T) {
	h := newHarness(t, nil, func(c *mtprototest.DCConn) {
		got, _ := io.ReadAll(c)
		_, _ = c.Write(append([]byte("pong:"), got...))
		_ = c.CloseWrite()
	})
	c := h.client(testKey(1), mtprototest.TagPadded, 2)
	_, _ = c.Write([]byte("ping"))
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "pong:ping" {
		t.Fatalf("after the half-close: %q %v", got, err)
	}
	waitFor(t, "session close", func() bool { _, closed := h.obs.counts(); return closed == 1 })

	h = newHarness(t, nil, func(c *mtprototest.DCConn) {
		_, _ = io.ReadFull(c, make([]byte, 4))
		_ = c.SetLinger(0)
		_ = c.Close()
	})
	c = h.client(testKey(1), mtprototest.TagPadded, 2)
	_, _ = c.Write([]byte("ping"))
	_ = c.Conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a reset DC left the client open: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestNoSecretsInErrorsOrLogs runs every failure that produces an error or a log line and checks none of them carries a
// key, a hello random (the digest) or bytes of the stream.
func TestNoSecretsInErrorsOrLogs(t *testing.T) {
	var logs syncBuffer
	h := newHarness(t, func(c *Config, h *harness) {
		c.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c.ReplayCacheSize = 1
		c.DCAddress = func(dc int16) (string, bool) { return h.dc.Addr, dc != 7 }
	}, nil)
	var secrets []string
	for _, k := range []([KeyLen]byte){testKey(1), testKey(2)} {
		secrets = append(secrets, hex.EncodeToString(k[:]), string(k[:]))
	}
	note := func(hello []byte) []byte {
		secrets = append(secrets, hex.EncodeToString(hello[11:43]), string(hello[11:43]))
		return hello
	}
	var errs []error
	collect := func() { errs = append(errs, h.serveErr()) }

	c, err := mtprototest.Start(h.dial(), testKey(1), note(h.hello(testKey(1))))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SendHeader(0x12345678, 2) // a header with no known tag
	collect()
	h.clk.Add(11 * time.Minute)
	c, err = mtprototest.Start(h.dial(), testKey(1), note(h.hello(testKey(1))))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SendHeader(mtprototest.TagPadded, 7) // an unknown DC
	collect()
	for _, hello := range [][]byte{
		note(h.hello(testKey(2))), // the replay cache is full
		note(mtprototest.Hello(testKey(9), testDomain, h.clk.Unix())),
		note(mtprototest.Hello(testKey(1), testDomain, h.clk.Unix()-7200)),
	} {
		conn := h.dial()
		_, _ = conn.Write(hello)
		readAll(t, conn)
		_ = conn.Close()
		<-h.fallbacks
		collect()
	}
	if !strings.Contains(logs.String(), "replay cache is full") {
		t.Fatalf("no warning for the full cache: %q", logs.String())
	}
	all := logs.String()
	for _, err := range errs {
		if err != nil {
			all += "\n" + err.Error()
		}
	}
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Fatalf("an error or a log line carries secret material: %q", all)
		}
	}
}

func TestDCTargets(t *testing.T) {
	is6 := func(a string) bool { return netip.MustParseAddrPort(a).Addr().Is6() }
	s := &Server{}
	main := s.dcTargets(2)
	if len(main) < 2 || is6(main[0]) || !is6(main[len(main)-1]) {
		t.Fatalf("dc 2: %v", main)
	}
	for _, dc := range []int16{-2, 0} {
		if got := s.dcTargets(dc); strings.Join(got, ",") != strings.Join(main, ",") {
			t.Fatalf("dc %d: %v", dc, got)
		}
	}
	for _, dc := range []int16{1, 3, 4, 5, 203, -203} {
		if len(s.dcTargets(dc)) == 0 {
			t.Fatalf("dc %d has no address", dc)
		}
	}
	for _, dc := range []int16{6, 10002, -32768} {
		if got := s.dcTargets(dc); got != nil {
			t.Fatalf("dc %d: %v", dc, got)
		}
	}
	if got := (&Server{cfg: Config{PreferIPv6: true}}).dcTargets(2); !is6(got[0]) {
		t.Fatalf("PreferIPv6: %v", got)
	}
	o := &Server{cfg: Config{DCAddress: func(dc int16) (string, bool) { return "192.0.2.10:443", dc == -2 }}}
	if got := o.dcTargets(-2); len(got) != 1 || got[0] != "192.0.2.10:443" {
		t.Fatalf("override: %v", got)
	}
	if got := o.dcTargets(2); strings.Join(got, ",") != strings.Join(main, ",") {
		t.Fatalf("override fall-through: %v", got)
	}
}
