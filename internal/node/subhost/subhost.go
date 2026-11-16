// Package subhost is the node side of the subscription host (issue #48). A panel at home behind NAT dials the host
// node's TCP 443 with a secret SNI; this package takes that connection over as an HTTP/2 tunnel (the node is the client)
// and forwards the raw TLS of the subscription names, which arrive on the same port, into the panel through it. Every
// other connection on 443 goes on to the node's own site unchanged. The agent's own link to the panel can also run
// inside the tunnel (DialPanel).
package subhost

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/hosttunnel"
)

const (
	// peekTimeout bounds reading a ClientHello on a wrapped 443.
	peekTimeout = 5 * time.Second
	// handshakeTimeout bounds the tunnel's TLS handshake.
	handshakeTimeout = 10 * time.Second
	// openTimeout bounds opening one stream in the tunnel.
	openTimeout = 10 * time.Second
)

var errTunnelDown = errors.New("subhost: no tunnel to the panel")

// Identity is the node's TLS identity, read at every use (it renews).
type Identity struct {
	Cert   func() *tls.Certificate
	CAPool func() *x509.CertPool
}

// Config is the role. An empty TunnelSNI turns it off.
type Config struct {
	TunnelSNI string
	Names     []string
}

type config struct {
	sni   string
	names map[string]bool // lower case
}

type tunnel struct {
	cc   *http2.ClientConn
	conn net.Conn
	sni  string
}

func (t *tunnel) up() bool {
	st := t.cc.State()
	return !st.Closed && !st.Closing
}

func (t *tunnel) close() {
	_ = t.cc.Close()
	_ = t.conn.Close()
}

// Host routes the node's TCP 443 for the role.
type Host struct {
	id     Identity
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc

	cfg      atomic.Pointer[config]
	wrapped  atomic.Int32
	forwards atomic.Int32

	mu  sync.Mutex
	tun *tunnel
}

// New returns a Host with the role off.
func New(id Identity, log *slog.Logger) *Host {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{id: id, log: log, ctx: ctx, cancel: cancel}
	h.cfg.Store(&config{})
	return h
}

// Configure swaps the role. A changed tunnel SNI, or the role turned off, closes the current tunnel.
func (h *Host) Configure(c Config) {
	next := &config{sni: c.TunnelSNI, names: make(map[string]bool, len(c.Names))}
	if next.sni != "" {
		for _, n := range c.Names {
			if n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), ".")); n != "" {
				next.names[n] = true
			}
		}
	}
	prev := h.cfg.Swap(next)
	if prev.sni != next.sni {
		h.dropTunnel(nil)
	}
}

// dropTunnel closes the current tunnel, or only t when t is not nil and still current.
func (h *Host) dropTunnel(t *tunnel) {
	h.mu.Lock()
	cur := h.tun
	if t == nil || cur == t {
		h.tun = nil
	} else {
		cur = nil
	}
	h.mu.Unlock()
	if cur != nil {
		cur.close()
		h.log.Info("subscription host tunnel closed")
	}
}

func (h *Host) current() *tunnel {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tun != nil && !h.tun.up() {
		h.tun.close()
		h.tun = nil
		h.log.Info("subscription host tunnel lost")
	}
	return h.tun
}

// TunnelUp says whether the panel's tunnel is connected.
func (h *Host) TunnelUp() bool { return h.current() != nil }

// Wrapped443 is the number of live site listeners on public port 443 that this Host routes.
func (h *Host) Wrapped443() int { return int(h.wrapped.Load()) }

// DialPanel opens a link stream to the panel in the tunnel.
func (h *Host) DialPanel(ctx context.Context) (net.Conn, error) {
	t := h.current()
	if t == nil {
		return nil, errTunnelDown
	}
	return hosttunnel.OpenStream(ctx, t.cc, t.sni, hosttunnel.KindLink, netip.AddrPort{})
}

// Close closes the tunnel and stops forwarding. Wrapped listeners keep serving the site.
func (h *Host) Close() {
	h.cancel()
	h.Configure(Config{})
}

// ProxyListener strips an optional PROXY header from every connection of ln; RemoteAddr is then the address it carried.
func ProxyListener(ln net.Listener) net.Listener {
	return &proxyproto.Listener{Listener: ln, ConnPolicy: usePolicy}
}

func usePolicy(proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) { return proxyproto.USE, nil }

// WrapSite wraps the listener of a site on publicPort (engine.Env.SiteListener). On 443 it routes by SNI: the tunnel's
// own SNI to the tunnel, the subscription names to the panel while the tunnel is up, everything else to the site. On
// other ports it only strips the PROXY header of a proxied site.
func (h *Host) WrapSite(ln net.Listener, publicPort uint16, proxied bool) net.Listener {
	if publicPort != 443 {
		if proxied {
			return ProxyListener(ln)
		}
		return ln
	}
	h.wrapped.Add(1)
	l := &siteListener{Listener: ln, h: h, proxied: proxied, queue: make(chan net.Conn), done: make(chan struct{})}
	go l.loop()
	return l
}

type siteListener struct {
	net.Listener
	h       *Host
	proxied bool
	queue   chan net.Conn
	done    chan struct{}
	once    sync.Once
	errMu   sync.Mutex
	err     error
}

func (l *siteListener) loop() {
	var delay time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && !errors.Is(err, net.ErrClosed) {
				delay = min(max(2*delay, 5*time.Millisecond), time.Second)
				select {
				case <-time.After(delay):
					continue
				case <-l.done:
					return
				}
			}
			l.errMu.Lock()
			l.err = err
			l.errMu.Unlock()
			_ = l.Close()
			return
		}
		delay = 0
		go l.handle(c)
	}
}

func (l *siteListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.queue:
		return c, nil
	case <-l.done:
		l.errMu.Lock()
		err := l.err
		l.errMu.Unlock()
		if err == nil {
			err = net.ErrClosed
		}
		return nil, err
	}
}

func (l *siteListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.Listener.Close()
		l.h.wrapped.Add(-1)
	})
	return err
}

// toSite hands c to the site's Accept.
func (l *siteListener) toSite(c net.Conn) {
	select {
	case l.queue <- c:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *siteListener) handle(c net.Conn) {
	if l.proxied {
		c = proxyproto.NewConn(c, proxyproto.WithPolicy(proxyproto.USE))
	}
	cfg := l.h.cfg.Load()
	if cfg.sni == "" {
		l.toSite(c)
		return
	}
	sni, replay, err := hosttunnel.PeekSNI(c, peekTimeout)
	if err != nil {
		return // PeekSNI closed it
	}
	name := strings.ToLower(sni)
	switch {
	case sni != "" && strings.EqualFold(sni, cfg.sni):
		l.h.acceptTunnel(replay, cfg.sni)
		return
	case cfg.names[name]:
		if t := l.h.current(); t != nil {
			l.h.forward(replay, t)
			return
		}
	}
	l.toSite(replay)
}

// acceptTunnel takes the panel's connection over: the node is the TLS server and the HTTP/2 client. Only a certificate
// of the panel's CA for ServerAuth that names the tunnel SNI is accepted: node certificates are ClientAuth with a URI
// SAN only, so another node cannot pose as the panel.
func (h *Host) acceptTunnel(c net.Conn, sni string) {
	conf := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{hosttunnel.ALPN},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if cert := h.id.Cert(); cert != nil {
				return cert, nil
			}
			return nil, errors.New("subhost: no node certificate")
		},
		ClientAuth: tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyPanel(raw, h.id.CAPool(), sni)
		},
	}
	tc := tls.Server(c, conf)
	ctx, cancel := context.WithTimeout(h.ctx, handshakeTimeout)
	err := tc.HandshakeContext(ctx)
	cancel()
	if err == nil && tc.ConnectionState().NegotiatedProtocol != hosttunnel.ALPN {
		err = errors.New("no h2")
	}
	if err != nil {
		_ = tc.Close()
		h.log.Debug("subscription host tunnel refused", "err", err)
		return
	}
	cc, err := (&http2.Transport{ReadIdleTimeout: hosttunnel.ReadIdle, PingTimeout: hosttunnel.PingTimeout}).NewClientConn(tc)
	if err != nil {
		_ = tc.Close()
		h.log.Debug("subscription host tunnel failed", "err", err)
		return
	}
	t := &tunnel{cc: cc, conn: tc, sni: sni}
	h.mu.Lock()
	if cur := h.cfg.Load(); cur.sni != sni || h.ctx.Err() != nil { // the role changed during the handshake
		h.mu.Unlock()
		t.close()
		return
	}
	old := h.tun
	h.tun = t
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	h.log.Info("subscription host tunnel up")
}

func verifyPanel(raw [][]byte, roots *x509.CertPool, sni string) error {
	if len(raw) == 0 || roots == nil {
		return errors.New("no panel certificate")
	}
	certs := make([]*x509.Certificate, len(raw))
	for i, b := range raw {
		c, err := x509.ParseCertificate(b)
		if err != nil {
			return err
		}
		certs[i] = c
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	leaf := certs[0]
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return err
	}
	for _, n := range leaf.DNSNames {
		if strings.EqualFold(n, sni) {
			return nil
		}
	}
	return errors.New("panel certificate does not name the tunnel")
}

// forward carries c, a subscription client's raw TLS, into the panel. Neither its bytes nor its address are logged.
func (h *Host) forward(c net.Conn, t *tunnel) {
	peer, ok := addrPort(c.RemoteAddr())
	if !ok {
		_ = c.Close()
		return
	}
	if h.forwards.Add(1) > hosttunnel.MaxStreams {
		h.forwards.Add(-1)
		_ = c.Close()
		return
	}
	defer h.forwards.Add(-1)
	ctx, cancel := context.WithTimeout(h.ctx, openTimeout)
	s, err := hosttunnel.OpenStream(ctx, t.cc, t.sni, hosttunnel.KindTLS, peer)
	cancel()
	if err != nil {
		_ = c.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(s, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, s); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = s.Close()
	<-done
}

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	var ap netip.AddrPort
	switch v := a.(type) {
	case *net.TCPAddr:
		ap = v.AddrPort()
	case nil:
		return ap, false
	default:
		var err error
		if ap, err = netip.ParseAddrPort(a.String()); err != nil {
			return ap, false
		}
	}
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	return ap, ap.IsValid()
}
