//go:build !js

package subhost

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The panel is the exit of each host's bridge: it calls the host's site on 443 with its short fleet certificate
// (Fleet.PanelIdentity) and keeps request pairs open there; the host hands each subscription connection to a pair, and the
// pair's bytes come out here, on an in-process listener (PublicListener) that the public server serves with the subdomain's
// certificate (PublicTLS), issued by Let's Encrypt through the host (TLS-ALPN-01).

// Relay ids of the two targets the host names (internal/node/subhost): both reach the same listener.
const (
	relaySub  = "sub"
	relayLink = "link"
)

// dialPool is how many TLS connections the panel keeps to a host: the most the bridge allows.
const dialPool = bridge.MaxPoolSize

// tunnel is the panel's side of the bridges to its hosts.
type tunnel struct {
	dialer *bridge.Dialer
	pub    *chanListener
	acme   *autocert.Manager
	tlsCfg *tls.Config

	ctx    context.Context
	cancel context.CancelFunc

	get func(*tls.ClientHelloInfo) (*tls.Certificate, error) // the subdomain's certificate: autocert, or Config.Certificate

	mu        sync.Mutex
	certSeen  map[string]bool // domains whose certificate the panel holds: handed to a visitor (not only an ACME challenge) or obtained
	obtaining bool            // obtainCert runs
	lastTry   time.Time       // when obtainCert last started
}

func (s *Service) newTunnel() {
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{pub: newChanListener(), ctx: ctx, cancel: cancel, certSeen: map[string]bool{}}
	t.dialer = bridge.NewDialer(func() bridge.DialIdentity {
		der, key, err := s.cfg.Fleet.PanelIdentity(s.cfg.Now())
		if err != nil {
			return bridge.DialIdentity{}
		}
		return bridge.DialIdentity{NodeID: bridge.PanelPeerID, CertificateDER: der, PrivateKey: key, Panel: true}
	})
	t.dialer.SetLocalDial(t.localDial)
	t.acme = &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: s.acmeHostPolicy}
	if s.cfg.ACMEDir != "" {
		t.acme.Cache = autocert.DirCache(s.cfg.ACMEDir)
	}
	if s.cfg.ACMEClient != nil {
		t.acme.Client = s.cfg.ACMEClient
	}
	t.get = t.acme.GetCertificate
	if s.cfg.Certificate != nil {
		t.get = s.cfg.Certificate
	}
	t.tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1", acme.ALPNProto},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := t.get(hello)
			if err == nil && !(len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto) {
				t.seen(hello.ServerName)
			}
			return cert, err
		}}
	s.tun = t
}

// acmeHostPolicy lets autocert ask for the subdomain only while a host serves it.
func (s *Service) acmeHostPolicy(_ context.Context, host string) error {
	st := s.current()
	if st.State == stateOff || !strings.EqualFold(strings.TrimSuffix(host, "."), st.Domain) {
		return errors.New("not the subscription host's domain")
	}
	return nil
}

// PublicListener yields the connections of the subdomain the hosts carry: the public server serves them with PublicTLS.
func (s *Service) PublicListener() net.Listener { return s.tun.pub }

// PublicTLS is the TLS of the subdomain: its Let's Encrypt certificate, and the TLS-ALPN-01 answers for it.
func (s *Service) PublicTLS() *tls.Config { return s.tun.tlsCfg }

// Close stops dialling the hosts and closes the listener.
func (s *Service) Close() {
	s.tun.cancel()
	s.tun.dialer.Close()
	_ = s.tun.pub.Close()
}

// localDial is the exit's target: a pipe whose other end the public server accepts, as from the person's address.
func (t *tunnel) localDial(ctx context.Context, target plugin.BridgeTarget, client netip.AddrPort) (net.Conn, error) {
	mine, theirs := duplexPipe()
	var from net.Addr = hostAddr{}
	if client.IsValid() {
		from = net.TCPAddrFromAddrPort(client)
	}
	if !t.pub.push(fromConn{Conn: theirs, from: from}, ctx) {
		_ = mine.Close()
		_ = theirs.Close()
		return nil, net.ErrClosed
	}
	return mine, nil
}

// syncDials makes the dialer call exactly the hosts of st. Call with mu held (from applyRouting) or from Run.
func (s *Service) syncDials(st state) {
	var specs []bridge.DialSpec
	add := func(nodeID, siteName, path, bootstrapPin string) {
		if nodeID == "" {
			return
		}
		host, sni, pin, _, ok := s.cfg.Fleet.SubHostTarget(s.tun.ctx, nodeID, siteName, bootstrapPin)
		if !ok {
			return
		}
		port := s.cfg.HostPort
		if port == 0 {
			port = plugin.SubHostPort
		}
		specs = append(specs, bridge.DialSpec{Dial: plugin.BridgeDial{EntryNodeID: nodeID, Host: host, Port: port, SNI: sni, PinSHA256: pin,
			Path: path, Pool: dialPool, Targets: []plugin.BridgeTarget{
				{RelayID: relaySub, Network: bridge.LocalTargetNetwork}, {RelayID: relayLink, Network: bridge.LocalTargetNetwork}}}})
	}
	if st.State != stateOff && st.Path != "" {
		add(st.Node, st.SiteName, st.Path, st.BootstrapPin)
		if st.State == stateMoving && st.NextPath != "" {
			add(st.Next, st.NextSiteName, st.NextPath, "") // the install's pin is the first host's, never the next one's
		}
	}
	s.tun.dialer.Sync(s.tun.ctx, specs)
}

// addLive adds what the panel sees of its hosts now to h: its connections to them.
func (s *Service) addLive(_ context.Context, st state, h *subhostv1.SubscriptionHost) {
	byNode := map[string]bridge.DialState{}
	for _, d := range s.tun.dialer.States() {
		byNode[d.PeerNodeID] = d
	}
	for _, id := range []string{st.Node, st.Next} {
		if id == "" {
			continue
		}
		d := byNode[id]
		b := &subhostv1.SubscriptionHostBridge{NodeId: id, Up: d.Conns > 0, RttMs: uint32(d.RTT / time.Millisecond), DownReason: d.Error}
		if !d.Since.IsZero() {
			b.SinceUnix = d.Since.Unix()
		}
		if d.Conns > 0 {
			b.DownReason = ""
		}
		if b.DownReason == "" && !b.Up {
			b.DownReason = "unreachable"
		}
		h.Bridges = append(h.Bridges, b)
	}
	sort.Slice(h.Bridges, func(i, j int) bool { return h.Bridges[i].NodeId < h.Bridges[j].NodeId })
}

// chanListener is a net.Listener fed by the bridge.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

// push hands c to Accept; false once the listener is closed or ctx ends first.
func (l *chanListener) push(c net.Conn, ctx context.Context) bool {
	select {
	case <-l.done:
		return false
	case <-ctx.Done():
		return false
	case l.ch <- c:
		return true
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c := <-l.ch:
		return c, nil
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return hostAddr{} }

// hostAddr is the address of a connection whose person the host did not name (the host's own link).
type hostAddr struct{}

func (hostAddr) Network() string { return "tcp" }
func (hostAddr) String() string  { return "0.0.0.0:0" }

// fromConn is a connection whose RemoteAddr is the person's, as the host told the panel.
type fromConn struct {
	net.Conn
	from net.Addr
}

func (c fromConn) RemoteAddr() net.Addr { return c.from }

// seen records that the panel holds the certificate of domain.
func (t *tunnel) seen(domain string) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return
	}
	t.mu.Lock()
	t.certSeen[domain] = true
	t.mu.Unlock()
}

// certRetry is how long the panel waits before it asks for the subdomain's certificate again after a try.
const certRetry = time.Minute

// obtainCert asks for the subdomain's certificate in the background, at most once a minute: Let's Encrypt validates it through
// the host (TLS-ALPN-01), so the panel asks once its bridge is up instead of waiting for a visitor.
func (s *Service) obtainCert(domain string) {
	t := s.tun
	now := s.cfg.Now()
	t.mu.Lock()
	if t.obtaining || (!t.lastTry.IsZero() && now.Sub(t.lastTry) < certRetry) {
		t.mu.Unlock()
		return
	}
	t.obtaining, t.lastTry = true, now
	t.mu.Unlock()
	go func() {
		defer func() {
			t.mu.Lock()
			t.obtaining = false
			t.mu.Unlock()
		}()
		if t.ctx.Err() != nil {
			return
		}
		// an ECDSA-capable hello, so that autocert issues a P-256 certificate
		hello := &tls.ClientHelloInfo{ServerName: domain, SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedCurves: []tls.CurveID{tls.CurveP256}, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12}}
		if _, err := t.get(hello); err != nil {
			s.cfg.Log.Warn("subscription host: no certificate for the subdomain yet", "code", "sub_host_cert_pending", "domain", domain, "err", err)
			return
		}
		t.seen(domain)
		s.cfg.Log.Info("subscription host: the panel has the subdomain's certificate", "domain", domain)
	}()
}

// duplexPipe is net.Pipe with a half-close: two pipes, one a direction, so that a person who ends what they send (FIN, which
// the bridge passes on as CloseWrite) still gets the reply. Deadlines go to the pipe of their direction.
func duplexPipe() (a, b net.Conn) {
	aToB, bFromA := net.Pipe()
	bToA, aFromB := net.Pipe()
	return &duplexConn{r: aFromB, w: aToB}, &duplexConn{r: bFromA, w: bToA}
}

type duplexConn struct {
	r, w net.Conn // r: the pipe this side reads; w: the pipe it writes
}

func (c *duplexConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *duplexConn) Write(p []byte) (int, error) { return c.w.Write(p) }

// CloseWrite ends this side's writing: the other side reads EOF and can still answer.
func (c *duplexConn) CloseWrite() error { return c.w.Close() }

func (c *duplexConn) Close() error {
	err := c.w.Close()
	if rerr := c.r.Close(); err == nil {
		err = rerr
	}
	return err
}

func (c *duplexConn) LocalAddr() net.Addr  { return c.r.LocalAddr() }
func (c *duplexConn) RemoteAddr() net.Addr { return c.r.RemoteAddr() }
func (c *duplexConn) SetDeadline(t time.Time) error {
	err := c.r.SetReadDeadline(t)
	if werr := c.w.SetWriteDeadline(t); err == nil {
		err = werr
	}
	return err
}
func (c *duplexConn) SetReadDeadline(t time.Time) error  { return c.r.SetReadDeadline(t) }
func (c *duplexConn) SetWriteDeadline(t time.Time) error { return c.w.SetWriteDeadline(t) }
