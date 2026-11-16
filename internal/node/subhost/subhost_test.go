package subhost

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/hosttunnel"
)

const (
	tunnelSNI = "t-4f9a.example.com"
	subName   = "sub.example.com"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

func (ca *testCA) issue(t *testing.T, tmpl *x509.Certificate) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(time.Now().UnixNano())
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func (ca *testCA) server(t *testing.T, name string) *tls.Certificate {
	return ca.issue(t, &x509.Certificate{Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
}

func (ca *testCA) node(t *testing.T, id string) *tls.Certificate {
	u, _ := url.Parse("mistgate://node/" + id)
	return ca.issue(t, &x509.Certificate{Subject: pkix.Name{CommonName: id}, URIs: []*url.URL{u}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
}

type rig struct {
	ca      *testCA
	host    *Host
	addr    string
	site    chan net.Conn
	subCert *tls.Certificate
}

func newRig(t *testing.T, proxied bool) *rig {
	t.Helper()
	ca := newCA(t)
	nodeCert := ca.node(t, "de1")
	h := New(Identity{Cert: func() *tls.Certificate { return nodeCert }, CAPool: func() *x509.CertPool { return ca.pool }}, nil)
	h.Configure(Config{TunnelSNI: tunnelSNI, Names: []string{"SUB.example.com"}})
	t.Cleanup(h.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wl := h.WrapSite(ln, 443, proxied)
	t.Cleanup(func() { _ = wl.Close() })
	if h.Wrapped443() != 1 {
		t.Fatalf("Wrapped443 = %d", h.Wrapped443())
	}
	site := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := wl.Accept()
			if err != nil {
				return
			}
			site <- c
		}
	}()
	return &rig{ca: ca, host: h, addr: ln.Addr().String(), site: site, subCert: ca.server(t, subName)}
}

// panelConn is a test panel's tunnel connection: what it wrote to the node, the streams the node opened in it, and done
// closed once the tunnel ended.
type panelConn struct {
	net.Conn
	mu      sync.Mutex
	sent    []byte
	streams atomic.Int32
	done    chan struct{}
}

func (p *panelConn) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.sent = append(p.sent, b...)
	p.mu.Unlock()
	return p.Conn.Write(b)
}

// firstRecord is the first TLS record the panel sent: its ClientHello.
func (p *panelConn) firstRecord(t *testing.T) []byte {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) < 5 {
		t.Fatal("the panel sent no record")
	}
	return append([]byte(nil), p.sent[:5+(int(p.sent[3])<<8|int(p.sent[4]))]...)
}

// ended says whether the tunnel ended within d.
func (p *panelConn) ended(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// panel dials the tunnel with cert and serves it: every tls stream is answered with "hello <peer>\n" over TLS for subName.
func (r *rig) panel(t *testing.T, cert *tls.Certificate) *panelConn {
	return r.panelAs(t, cert, tunnelSNI, nil)
}

// panelAs is panel with the tunnel secret and the clock of the mark.
func (r *rig) panelAs(t *testing.T, cert *tls.Certificate, secret string, now func() time.Time) *panelConn {
	t.Helper()
	raw, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	p := &panelConn{Conn: raw, done: make(chan struct{})}
	t.Cleanup(func() { _ = raw.Close() })
	go func() {
		defer close(p.done)
		tc, err := hosttunnel.Dial(t.Context(), p, hosttunnel.DialConfig{
			ServerName: subName, Secret: secret, Certificate: *cert, Now: now,
			VerifyNode: func([][]byte) error { return nil },
		})
		if err != nil {
			return
		}
		(&http2.Server{}).ServeConn(tc, &http2.ServeConnOpts{Handler: hosttunnel.ServerHandler(func(kind string, c net.Conn) bool {
			p.streams.Add(1)
			go func() {
				defer c.Close()
				s := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*r.subCert}})
				if s.Handshake() != nil {
					return
				}
				_, _ = io.WriteString(s, "hello "+kind+" "+c.RemoteAddr().String()+"\n")
				_ = s.Close()
			}()
			return true
		})})
	}()
	return p
}

func (r *rig) waitTunnel(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.host.TunnelUp() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("TunnelUp never became %v", want)
}

// client opens TLS for name and returns the first line the server sends, or the connection that reached the site.
func (r *rig) clientLine(t *testing.T, conn net.Conn, name string) string {
	t.Helper()
	tc := tls.Client(conn, &tls.Config{ServerName: name, RootCAs: r.ca.pool})
	defer tc.Close()
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(tc).ReadString('\n')
	if err != nil {
		t.Fatalf("client for %s: %v", name, err)
	}
	return strings.TrimSpace(line)
}

func TestSubscriptionNameGoesToPanel(t *testing.T) {
	r := newRig(t, false)
	r.panel(t, r.ca.server(t, tunnelSNI))
	r.waitTunnel(t, true)
	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.clientLine(t, c, subName), "hello tls "+c.LocalAddr().String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// siteServes answers the next connection of the site over TLS for name.
func (r *rig) siteServes(t *testing.T, name string) {
	t.Helper()
	cert := r.ca.server(t, name)
	go func() {
		select {
		case c := <-r.site:
			s := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*cert}})
			defer s.Close()
			if s.Handshake() == nil {
				_, _ = io.WriteString(s, "site\n")
			}
		case <-time.After(5 * time.Second):
		}
	}()
}

func TestForeignNameGoesToSite(t *testing.T) {
	r := newRig(t, false)
	r.panel(t, r.ca.server(t, tunnelSNI))
	r.waitTunnel(t, true)
	r.siteServes(t, "www.example.com")
	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.clientLine(t, c, "www.example.com"); got != "site" {
		t.Fatalf("got %q", got)
	}
}

func TestSubscriptionNameWithoutTunnelGoesToSite(t *testing.T) {
	r := newRig(t, false)
	r.siteServes(t, subName)
	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.clientLine(t, c, subName); got != "site" {
		t.Fatalf("got %q", got)
	}
}

func TestNodeCertificateCannotOpenTunnel(t *testing.T) {
	r := newRig(t, false)
	if !r.panel(t, r.ca.node(t, "nl1")).ended(5 * time.Second) {
		t.Fatal("a tunnel with a node certificate stayed open")
	}
	if r.host.TunnelUp() {
		t.Fatal("tunnel up with a node certificate")
	}
	// Another CA's certificate for the right name fails too.
	r.panel(t, newCA(t).server(t, tunnelSNI))
	time.Sleep(200 * time.Millisecond)
	if r.host.TunnelUp() {
		t.Fatal("tunnel up with a foreign CA")
	}
}

func TestProxiedSite(t *testing.T) {
	r := newRig(t, true)
	r.panel(t, r.ca.server(t, tunnelSNI))
	r.waitTunnel(t, true)

	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	src := &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 4444}
	dst := &net.TCPAddr{IP: net.ParseIP("198.51.100.1"), Port: 443}
	if _, err := proxyproto.HeaderProxyFromAddrs(2, src, dst).WriteTo(c); err != nil {
		t.Fatal(err)
	}
	if got := r.clientLine(t, c, subName); got != "hello tls 203.0.113.9:4444" {
		t.Fatalf("got %q", got)
	}

	// A connection without the header (the engine's own measurement) still reaches the site.
	r.siteServes(t, "www.example.com")
	c2, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.clientLine(t, c2, "www.example.com"); got != "site" {
		t.Fatalf("got %q", got)
	}
}

func TestRoleOffAndTunnelReplaced(t *testing.T) {
	r := newRig(t, false)
	first := r.panel(t, r.ca.server(t, tunnelSNI))
	r.waitTunnel(t, true)
	// The new dial carries the subscription name as SNI too: it must replace the tunnel, never be forwarded through it.
	r.panel(t, r.ca.server(t, tunnelSNI))
	if !first.ended(5 * time.Second) {
		t.Fatal("the first tunnel stayed up: the second dial was not taken as the tunnel")
	}
	r.waitTunnel(t, true)
	r.host.Configure(Config{})
	r.waitTunnel(t, false)
	if _, err := r.host.DialPanel(t.Context()); err == nil {
		t.Fatal("DialPanel without a tunnel")
	}
}

// A recorded hello of the panel opens no second tunnel: the node treats it as any connection for the subscription name
// (forwarded into the live tunnel), and the live tunnel stays.
func TestReplayedHelloOpensNoTunnel(t *testing.T) {
	r := newRig(t, false)
	first := r.panel(t, r.ca.server(t, tunnelSNI))
	r.waitTunnel(t, true)
	hello := first.firstRecord(t)
	// The recorded bytes are a whole hello with a valid mark: only the replay cache can refuse them.
	a, b := net.Pipe()
	defer a.Close()
	go func() { _, _ = a.Write(hello) }()
	peeked, _, err := hosttunnel.PeekHello(b, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := peeked.Mark(tunnelSNI); !ok {
		t.Fatal("the recorded hello carries no valid mark")
	}
	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(hello); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for first.streams.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the replay was not forwarded into the tunnel: %d streams", first.streams.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.ended(300 * time.Millisecond) {
		t.Fatal("the replayed hello replaced the tunnel")
	}
	c2, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.clientLine(t, c2, subName), "hello tls "+c2.LocalAddr().String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A mark from a clock out of the window, or sealed with another secret, is no mark: the connection goes to the site.
func TestStaleOrForeignMarkGoesToSite(t *testing.T) {
	r := newRig(t, false)
	for name, dial := range map[string]func() *panelConn{
		"skew": func() *panelConn {
			return r.panelAs(t, r.ca.server(t, tunnelSNI), tunnelSNI, func() time.Time { return time.Now().Add(5 * time.Minute) })
		},
		"secret": func() *panelConn { return r.panelAs(t, r.ca.server(t, tunnelSNI), "t-other.example.com", nil) },
	} {
		p := dial()
		select {
		case c := <-r.site:
			_ = c.Close()
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the connection never reached the site", name)
		}
		if !p.ended(5 * time.Second) {
			t.Fatalf("%s: the dial did not end", name)
		}
		if r.host.TunnelUp() {
			t.Fatalf("%s: tunnel up", name)
		}
	}
}

func TestOtherPortUntouched(t *testing.T) {
	h := New(Identity{}, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if h.WrapSite(ln, 8443, false) != ln {
		t.Fatal("a site on 8443 was wrapped")
	}
	if h.Wrapped443() != 0 {
		t.Fatal("counted")
	}
}
