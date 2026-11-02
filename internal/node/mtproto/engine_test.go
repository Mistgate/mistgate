package mtproto

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mp "github.com/mistgate/mistgate/internal/mtproto"
	"github.com/mistgate/mistgate/internal/mtproto/mtprototest"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	ownDomain   = "de1.example.com"
	otherDomain = "fi1.example.com"
)

func TestParseSpec(t *testing.T) {
	base := plugin.InboundSpec{
		ID: "i1", Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: 443},
		Settings: json.RawMessage(`{"domains":["De1.Example.com.","de1.example.com"]}`),
	}
	s, err := parseSpec(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.domains) != 1 || s.domains[0] != ownDomain || s.fallback != fallbackDecoy {
		t.Fatalf("settings %+v", s)
	}
	site := base
	site.Settings = json.RawMessage(`{"domains":["fi1.example.com"],"fallback":"site","idle_timeout_secs":300}`)
	if s, err := parseSpec(site); err != nil || s.fallback != fallbackSite || s.idleTimeout != 5*time.Minute {
		t.Fatalf("site: %+v %v", s, err)
	}
	for name, mutate := range map[string]func(*plugin.InboundSpec){
		"protocol":     func(s *plugin.InboundSpec) { s.Protocol = "vless" },
		"network":      func(s *plugin.InboundSpec) { s.Listen.Network = "udp" },
		"port":         func(s *plugin.InboundSpec) { s.Listen.Port = 0 },
		"hop":          func(s *plugin.InboundSpec) { s.Listen.HopFrom, s.Listen.HopTo = 1000, 2000 },
		"egress":       func(s *plugin.InboundSpec) { s.Egress = "warp" },
		"no domains":   func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{}`) },
		"empty domain": func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"domains":[" "]}`) },
		"fallback value": func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(`{"domains":["a.example.com"],"fallback":"x"}`)
		},
		"idle timeout": func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(`{"domains":["a.example.com"],"idle_timeout_secs":5}`)
		},
	} {
		s := base
		mutate(&s)
		if _, err := parseSpec(s); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	disabled := base
	disabled.Enabled = false
	disabled.Listen.Port = 0
	disabled.Settings = nil
	if _, err := parseSpec(disabled); err != nil {
		t.Fatalf("disabled: %v", err)
	}
}

func testCert(t *testing.T, cn string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeCerts hands out an "ACME" certificate that has not been issued yet and a self-signed one.
type fakeCerts struct {
	acme, self *tls.Certificate
	released   atomic.Int32
}

func (f *fakeCerts) Acquire(_ context.Context, _ string, t plugin.TLS) (engine.Cert, error) {
	c := f.self
	if t.Mode == plugin.TLSAcmeDomain {
		c = f.acme
	}
	return engine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c, nil },
		Info:           func() engine.CertInfo { return engine.CertInfo{} },
	}, nil
}

func (f *fakeCerts) Release(string) { f.released.Add(1) }

func credential(id string, key [16]byte) plugin.UserCred {
	return plugin.UserCred{CredID: id, Data: json.RawMessage(`{"key":"` + hex.EncodeToString(key[:]) + `"}`)}
}

func key(b byte) (k [16]byte) {
	for i := range k {
		k[i] = b<<4 | byte(i)
	}
	return k
}

type fixture struct {
	t    *testing.T
	e    *Engine
	spec plugin.InboundSpec
	dc   *mtprototest.DC
	addr string
}

// newFixture runs an engine on loopback with the fake DC behind every DC.
func newFixture(t *testing.T, env engine.Env, settings string, creds ...plugin.UserCred) *fixture {
	t.Helper()
	oldBind, oldDC := BindIP, dcAddress
	BindIP = net.IPv4(127, 0, 0, 1)
	dc := mtprototest.NewDC(t, mtprototest.Echo)
	dcAddress = func(int16) (string, bool) { return dc.Addr, true }
	t.Cleanup(func() { BindIP, dcAddress = oldBind, oldDC })
	env.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	e := eng.(*Engine)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	port := freePort(t)
	spec := plugin.InboundSpec{
		ID: "i1", Protocol: Protocol, Enabled: true,
		Listen: plugin.Listen{Network: "tcp", Port: port}, Settings: json.RawMessage(settings),
	}
	if _, err := e.Apply(context.Background(), spec, creds); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, e: e, spec: spec, dc: dc, addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}
}

func (f *fixture) inbound() *inbound {
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	return f.e.inbounds[f.spec.ID]
}

func (f *fixture) dial() net.Conn {
	f.t.Helper()
	c, err := net.DialTimeout("tcp", f.addr, 2*time.Second)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return c
}

// client is an authenticated Telegram client relaying to the fake DC.
func (f *fixture) client(k [16]byte, domain string) *mtprototest.Client {
	f.t.Helper()
	c, err := mtprototest.Start(f.dial(), k, mtprototest.Hello(k, domain, uint32(time.Now().Unix())))
	if err != nil {
		f.t.Fatalf("handshake: %v", err)
	}
	if err := c.SendHeader(mtprototest.TagPadded, 2); err != nil {
		f.t.Fatal(err)
	}
	if _, err := c.Write([]byte("x")); err != nil {
		f.t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "dc:x" {
		f.t.Fatalf("relay: %q %v", got, err)
	}
	return c
}

// get fetches / over TLS with SNI name and returns the body and the certificate's name.
func (f *fixture) get(name string) (string, string) {
	f.t.Helper()
	var cn string
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: name, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
			cn = cs.PeerCertificates[0].Subject.CommonName
			return nil
		}},
	}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + f.addr + "/")
	if err != nil {
		f.t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), cn
}

func TestEngineLifecycle(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, ownDomain), self: testCert(t, ownDomain)}
	f := newFixture(t, engine.Env{Certs: certs}, `{"domains":["de1.example.com"]}`, credential("c1", key(1)))
	if h := f.e.Health(); len(h) != 1 || h[0].State != plugin.RunRunning {
		t.Fatalf("health %+v", h)
	}
	rep, err := f.e.Apply(context.Background(), f.spec, []plugin.UserCred{credential("c2", key(2))})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Restarted {
		t.Fatal("credential-only apply restarted the listener")
	}
	observed := f.e.Observed()
	if len(observed) != 1 || len(observed[0].Creds) != 1 || observed[0].Creds[0].CredID != "c2" {
		t.Fatalf("observed %+v", observed)
	}
	if n, err := f.e.Kick(context.Background(), []string{"c2"}); err != nil || n != 0 {
		t.Fatalf("kick n=%d err=%v", n, err)
	}
	if err := f.e.Remove(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := net.DialTimeout("tcp", f.addr, 100*time.Millisecond); err == nil {
		_ = raw.Close()
		t.Fatal("listener survived Remove")
	}
	if certs.released.Load() == 0 {
		t.Fatal("the certificate claim was not released")
	}
}

// TestDecoyFallback: with fallback "decoy" a stranger gets the node's site with the node's certificate, Let's Encrypt's
// TLS-ALPN-01 gets the ACME entry, and a client gets Telegram.
func TestDecoyFallback(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, "acme."+ownDomain), self: testCert(t, ownDomain)}
	var hits atomic.Int32
	env := engine.Env{Certs: certs, Masquerade: func(id string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_, _ = io.WriteString(w, "decoy of "+id)
		})
	}}
	f := newFixture(t, env, `{"domains":["de1.example.com"],"fallback":"decoy"}`, credential("c1", key(1)))

	if body, cn := f.get(ownDomain); body != "decoy of i1" || cn != ownDomain {
		t.Fatalf("a browser got %q from %q", body, cn)
	}
	if body, _ := f.get(otherDomain); body != "decoy of i1" {
		t.Fatalf("another SNI got %q", body)
	}
	conn, err := tls.Dial("tcp", f.addr, &tls.Config{ServerName: ownDomain, NextProtos: []string{"acme-tls/1"}, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS-ALPN-01: %v", err)
	}
	cs := conn.ConnectionState()
	_ = conn.Close()
	if cs.NegotiatedProtocol != "acme-tls/1" || cs.PeerCertificates[0].Subject.CommonName != "acme."+ownDomain {
		t.Fatalf("TLS-ALPN-01 got %q from %q", cs.NegotiatedProtocol, cs.PeerCertificates[0].Subject.CommonName)
	}

	before := hits.Load()
	c := f.client(key(1), ownDomain)
	if hits.Load() != before {
		t.Fatal("an authenticated client reached the site")
	}
	// Removing the credential closes its session.
	if _, err := f.e.Apply(context.Background(), f.spec, nil); err != nil {
		t.Fatal(err)
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the session of a removed credential stayed open: %v", err)
	}
}

// fakeSite is someone else's HTTPS site on loopback, counting the connections it accepts.
type fakeSite struct {
	addr     string
	accepted atomic.Int32
}

func newFakeSite(t *testing.T, hold bool) *fakeSite {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSite{addr: ln.Addr().String()}
	cert := testCert(t, otherDomain)
	var conns sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); conns.Wait() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			conns.Add(1)
			go func() {
				defer conns.Done()
				defer c.Close()
				if hold {
					_, _ = io.Copy(io.Discard, c)
					return
				}
				tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*cert}})
				_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := http.ReadRequest(bufio.NewReader(tc)); err != nil {
					return
				}
				_, _ = io.WriteString(tc, "HTTP/1.1 200 OK\r\nContent-Length: 13\r\nConnection: close\r\n\r\nthe real site")
				_ = tc.CloseWrite()
			}()
		}
	}()
	return s
}

// siteSeams points the site fallback at the fake site: the domain resolves to target, local says what is the node's,
// and loopbackOK lets the fake site on loopback count as a public target.
func siteSeams(t *testing.T, site *fakeSite, local func(netip.Addr) bool, target netip.Addr, loopbackOK bool) {
	t.Helper()
	oldLookup, oldPort, oldLocal, oldOK, oldIdle := siteLookup, sitePort, localAddr, siteTargetOK, siteIdle
	t.Cleanup(func() {
		siteLookup, sitePort, localAddr, siteTargetOK, siteIdle = oldLookup, oldPort, oldLocal, oldOK, oldIdle
	})
	siteLookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host != otherDomain {
			return nil, errors.New("unknown host")
		}
		return []netip.Addr{target}, nil
	}
	sitePort = netip.MustParseAddrPort(site.addr).Port()
	localAddr = local
	if loopbackOK {
		siteTargetOK = func(a netip.Addr) bool { return a.IsLoopback() || oldOK(a) }
	}
}

// TestSiteFallback: with fallback "site" a stranger is spliced to the domain's own site and sees its certificate; a
// client is never forwarded.
func TestSiteFallback(t *testing.T) {
	site := newFakeSite(t, false)
	siteSeams(t, site, func(netip.Addr) bool { return false }, netip.MustParseAddr("127.0.0.1"), true)
	f := newFixture(t, engine.Env{}, `{"domains":["fi1.example.com"],"fallback":"site"}`, credential("c1", key(1)))

	if body, cn := f.get(otherDomain); body != "the real site" || cn != otherDomain {
		t.Fatalf("a browser got %q from %q", body, cn)
	}
	before := site.accepted.Load()
	f.client(key(1), otherDomain)
	if site.accepted.Load() != before {
		t.Fatal("an authenticated client was forwarded to the site")
	}
}

// TestSiteFallbackRefusesTargets: a domain that resolves to the node itself (a loop) or to a private, link-local,
// CGNAT or reserved address is not forwarded to.
func TestSiteFallbackRefusesTargets(t *testing.T) {
	own := netip.MustParseAddr("11.22.33.44") // a public address, here one of the node's
	for _, target := range []string{"11.22.33.44", "10.0.0.5", "172.16.1.1", "192.168.1.1", "169.254.1.1", "100.64.0.1",
		"192.0.2.10", "fd00::1", "fe80::1", "2001:db8::1", "0.1.2.3", "240.0.0.1"} {
		t.Run(target, func(t *testing.T) { // its own cleanup: the engine stops before the next one changes the seams
			site := newFakeSite(t, false)
			siteSeams(t, site, func(a netip.Addr) bool { return a == own }, netip.MustParseAddr(target), false)
			f := newFixture(t, engine.Env{}, `{"domains":["fi1.example.com"],"fallback":"site"}`)
			conn := f.dial()
			_, _ = conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
			if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("the forward was not refused: %v", err)
			}
			if site.accepted.Load() != 0 {
				t.Fatal("the forward was dialed")
			}
		})
	}
}

func TestPublicUnicast(t *testing.T) {
	for addr, want := range map[string]bool{
		"11.22.33.44": true, "2a01:1::1": true,
		"127.0.0.1": false, "0.0.0.0": false, "10.1.2.3": false, "172.31.0.1": false, "192.168.0.1": false,
		"169.254.0.1": false, "100.127.0.1": false, "192.0.0.8": false, "198.18.0.1": false, "198.51.100.1": false,
		"203.0.113.1": false, "224.0.0.1": false, "255.255.255.255": false, "::1": false, "::": false, "fc00::1": false,
		"fe80::1": false, "ff02::1": false, "2001:db8::1": false, "::ffff:10.0.0.1": false, "100::1": false,
	} {
		if got := publicUnicast(netip.MustParseAddr(addr)); got != want {
			t.Fatalf("publicUnicast(%s) = %v", addr, got)
		}
	}
}

func TestSiteFallbackBounds(t *testing.T) {
	site := newFakeSite(t, true)
	siteSeams(t, site, func(netip.Addr) bool { return false }, netip.MustParseAddr("127.0.0.1"), true)
	siteIdle = 300 * time.Millisecond
	f := newFixture(t, engine.Env{}, `{"domains":["fi1.example.com"],"fallback":"site"}`)
	fw := f.inbound().forwarder
	busy := func() int { fw.mu.Lock(); defer fw.mu.Unlock(); return fw.total }

	// Idle: a forward where nothing moves is closed.
	conn := f.dial()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	start := time.Now()
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("an idle forward: %v after %v", err, time.Since(start))
	}
	waitFor(t, "the idle forward to end", func() bool { return busy() == 0 })

	// Per peer: the forwards one address may hold.
	siteIdle = time.Minute
	for range siteMaxPerPeer {
		c := f.dial()
		_, _ = c.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	}
	waitFor(t, "the forwards", func() bool { return busy() == siteMaxPerPeer })
	conn = f.dial()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a forward over the per-peer limit: %v", err)
	}
	if n := site.accepted.Load(); n != siteMaxPerPeer+1 {
		t.Fatalf("the site accepted %d connections", n)
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

// TestCollectRacesObserver: Collect and Apply replace the credential index while observer callbacks read it (go test
// -race finds an in-place edit).
func TestCollectRacesObserver(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, ownDomain), self: testCert(t, ownDomain)}
	f := newFixture(t, engine.Env{Certs: certs}, `{"domains":["de1.example.com"]}`, credential("c1", key(1)), credential("c2", key(2)))
	in := f.inbound()
	obs := inboundObserver{in: in}
	var ids atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				info := mp.SessionInfo{ID: ids.Add(1), CredentialID: "c1"}
				obs.Opened(info)
				obs.AddTraffic(info, 1, 1)
				obs.Closed(info)
			}
		}()
	}
	sets := [][]plugin.UserCred{
		{credential("c1", key(1)), credential("c2", key(2))},
		{credential("c1", key(3))},
		{credential("c2", key(2))},
	}
	for i := range 300 {
		if _, err := f.e.Apply(context.Background(), f.spec, sets[i%len(sets)]); err != nil {
			t.Fatal(err)
		}
		if _, err := f.e.Collect(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	_, _ = f.e.Collect(context.Background())
	if n := len(in.idx.Load().retired); n != 0 {
		t.Fatalf("%d retired credentials kept with no session open", n)
	}
	for id, cs := range in.idx.Load().current {
		if n := cs.sessions.Load(); n != 0 {
			t.Fatalf("%s counts %d sessions", id, n)
		}
	}
}

// TestSessionKeepsItsCredState: a session opened under a key that is then replaced is counted on the state it opened
// with until it closes, so neither state's count goes wrong and the old one is dropped afterwards.
func TestSessionKeepsItsCredState(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, ownDomain), self: testCert(t, ownDomain)}
	f := newFixture(t, engine.Env{Certs: certs}, `{"domains":["de1.example.com"]}`, credential("c1", key(1)))
	in := f.inbound()
	obs := inboundObserver{in: in}
	info := mp.SessionInfo{ID: 1, CredentialID: "c1"}
	obs.Opened(info)
	old := in.idx.Load().current["c1"]
	if _, err := f.e.Apply(context.Background(), f.spec, []plugin.UserCred{credential("c1", key(2))}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.e.Collect(context.Background())
	if r := in.idx.Load().retired; len(r) != 1 || r[0] != old {
		t.Fatalf("the replaced state with an open session was not kept: %v", r)
	}
	obs.Closed(info)
	if old.sessions.Load() != 0 || in.idx.Load().current["c1"].sessions.Load() != 0 {
		t.Fatalf("counts old=%d new=%d", old.sessions.Load(), in.idx.Load().current["c1"].sessions.Load())
	}
	_, _ = f.e.Collect(context.Background())
	if r := in.idx.Load().retired; len(r) != 0 {
		t.Fatalf("a closed state was kept: %v", r)
	}
}

// TestTrafficStaysWithItsSession: bytes a session reports after its credential's key changed under the same id land on
// the state it was opened with, not on the new key's.
func TestTrafficStaysWithItsSession(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, ownDomain), self: testCert(t, ownDomain)}
	f := newFixture(t, engine.Env{Certs: certs}, `{"domains":["de1.example.com"]}`, credential("c1", key(1)))
	in := f.inbound()
	obs := inboundObserver{in: in}
	info := mp.SessionInfo{ID: 1, CredentialID: "c1"}
	obs.Opened(info)
	old := in.idx.Load().current["c1"]
	if _, err := f.e.Apply(context.Background(), f.spec, []plugin.UserCred{credential("c1", key(2))}); err != nil {
		t.Fatal(err)
	}
	obs.AddTraffic(info, 5, 7)
	obs.Closed(info)
	if fresh := in.idx.Load().current["c1"]; old.up.Load() != 5 || old.down.Load() != 7 || fresh.up.Load() != 0 {
		t.Fatalf("old %d/%d, new %d", old.up.Load(), old.down.Load(), fresh.up.Load())
	}
	got, _ := f.e.Collect(context.Background())
	if len(got.Traffic) != 1 || got.Traffic[0].Up != 5 || got.Traffic[0].Down != 7 {
		t.Fatalf("collected %+v", got.Traffic)
	}
}

// TestRestartKeepsTraffic: a spec change restarts the inbound; the traffic of its sessions (their last flush included)
// is not lost but handed out by the next Collect.
func TestRestartKeepsTraffic(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, ownDomain), self: testCert(t, ownDomain)}
	f := newFixture(t, engine.Env{Certs: certs}, `{"domains":["de1.example.com"]}`, credential("c1", key(1)))
	c := f.client(key(1), ownDomain) // "x" up, "dc:x" down, not flushed yet (StatsInterval is 10 s)
	spec := f.spec
	spec.Settings = json.RawMessage(`{"domains":["de1.example.com"],"handshake_timeout_secs":20}`)
	rep, err := f.e.Apply(context.Background(), spec, []plugin.UserCred{credential("c1", key(1))})
	if err != nil || !rep.Restarted {
		t.Fatalf("restart: %+v %v", rep, err)
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the session survived the restart: %v", err)
	}
	got, _ := f.e.Collect(context.Background())
	if len(got.Traffic) != 1 || got.Traffic[0].CredID != "c1" || got.Traffic[0].Up != 1 || got.Traffic[0].Down != 4 {
		t.Fatalf("collected %+v", got.Traffic)
	}
	if got, _ := f.e.Collect(context.Background()); len(got.Traffic) != 0 {
		t.Fatalf("collected twice: %+v", got.Traffic)
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return uint16(port)
}
