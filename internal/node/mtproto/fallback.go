package mtproto

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/mistgate/mistgate/internal/decoy"
	mp "github.com/mistgate/mistgate/internal/mtproto"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The two fallbacks take what the core does not authenticate: another SNI, a failed HMAC, a replayed hello, plain HTTP, a
// browser, a scanner. The core hands each over with every byte it has read.

// decoySite is the "decoy" fallback: the domain is the node's own, and strangers get the node's site, the same handler
// Hysteria2 and VLESS "self" show (Env.Masquerade) with the certificate of the node's name from the shared source. So a
// visitor of the port sees one site whichever protocol holds it, and Let's Encrypt's TLS-ALPN-01 validation (acme-tls/1,
// which the certificate source routes by name) is answered here too: an MTProto inbound on 443 does not cost the node
// its certificate renewals. It is an http.Server whose listener is fed by the core.
type decoySite struct {
	srv   *http.Server
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (in *inbound) startDecoy(ctx context.Context) (*decoySite, error) {
	src := in.e.env.Certs
	if src == nil {
		return nil, errors.New("the decoy site needs a certificate source")
	}
	t := in.spec.TLS
	if t.Mode == 0 {
		t = plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: in.cfg.domains[0]}
	}
	main, err := src.Acquire(ctx, in.spec.ID, t)
	if err != nil {
		return nil, err
	}
	in.certHeld = true
	get := main.GetCertificate
	if t.Mode == plugin.TLSAcmeDomain {
		self, err := src.Acquire(ctx, in.spec.ID, plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: t.ServerName})
		if err != nil {
			return nil, err
		}
		get = certs.SiteCertificate(main, self)
	}
	var h http.Handler
	if in.e.env.Masquerade != nil {
		h = in.e.env.Masquerade(in.spec.ID)
	}
	if h == nil {
		h = decoy.Handler()
	}
	d := &decoySite{conns: make(chan net.Conn), done: make(chan struct{})}
	d.srv = &http.Server{
		Handler: h,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: get,
			NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
		},
		// The bounds of the other decoy sites: this one faces scanners too.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go d.srv.ServeTLS(d, "", "")
	return d, nil
}

// serve is the core's Fallback: the connection, with the bytes already read put back in front, goes to the site.
func (d *decoySite) serve(_ context.Context, conn net.Conn, initial []byte) error {
	select {
	case d.conns <- &prefixConn{Conn: conn, head: initial}:
		return nil
	case <-d.done:
		_ = conn.Close()
		return net.ErrClosed
	}
}

// Accept, Close and Addr make decoySite the listener of its own http.Server.
func (d *decoySite) Accept() (net.Conn, error) {
	select {
	case c := <-d.conns:
		return c, nil
	case <-d.done:
		return nil, net.ErrClosed
	}
}

func (d *decoySite) Close() error {
	d.once.Do(func() { close(d.done) })
	return nil
}

func (d *decoySite) Addr() net.Addr { return &net.TCPAddr{} }

// shutdown stops the site and closes its connections.
func (d *decoySite) shutdown() {
	_ = d.Close()
	_ = d.srv.Close()
}

// prefixConn reads head before the connection.
type prefixConn struct {
	net.Conn
	head []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.head) > 0 {
		n := copy(p, c.head)
		c.head = c.head[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// forwarder is the "site" fallback: the domain is someone else's site, and a stranger's connection is spliced raw to
// it, port 443, with the bytes already read sent first. A prober completes a real TLS handshake with that site and
// sees its certificate, the way REALITY treats its target. The target is the hello's SNI when it is one of the
// domains, else the first domain.
//
// It is bounded like the handshakes it follows: connections in total and per peer (IPv4 address or IPv6 /64), an idle
// limit (no byte either way) and a total one. It dials only public unicast addresses that are not the node's (the
// domain resolving to the node itself would feed the forward back into the listener; to a private or reserved address
// it would open the node's own networks to strangers), and forwards no connection that comes from the node; a loop
// through a NAT that hides the node's public address is cut by the per-peer limit, since every hop comes from one
// address.
type forwarder struct {
	domains []string
	log     *slog.Logger
	warned  atomic.Int64 // when (unix ns) a bad target was last logged: strangers can make it happen at line rate

	mu     sync.Mutex
	closed bool
	total  int
	peers  map[netip.Prefix]int
	conns  map[net.Conn]struct{}
}

const (
	siteMaxConns    = 256
	siteMaxPerPeer  = 16
	siteMaxDuration = 5 * time.Minute
	siteDialTimeout = 10 * time.Second
)

// Seams for the tests: a fake site on a loopback port, what counts as the node's own address and as a target the
// fallback may dial, a short idle limit.
var (
	siteLookup   = net.DefaultResolver.LookupNetIP
	sitePort     = uint16(443)
	localAddr    = isLocalAddr
	siteTargetOK = func(a netip.Addr) bool { return publicUnicast(a) && !localAddr(a) }
	siteIdle     = 60 * time.Second
)

var (
	errSiteBusy   = errors.New("site fallback: too many connections")
	errSiteLoop   = errors.New("site fallback: the connection comes from this node")
	errSiteTarget = errors.New("site fallback: the domain resolves to an address that is not public or is this node's")
)

func newForwarder(domains []string, log *slog.Logger) *forwarder {
	return &forwarder{domains: domains, log: log, peers: make(map[netip.Prefix]int), conns: make(map[net.Conn]struct{})}
}

// serve is the core's Fallback. It runs on the Serve goroutine, which the engine waits for on stop.
func (f *forwarder) serve(ctx context.Context, conn net.Conn, initial []byte) error {
	from := peerAddr(conn)
	peer := peerKey(from)
	if !f.enter(peer, conn) {
		_ = conn.Close()
		return errSiteBusy
	}
	defer f.leave(peer, conn)
	if localAddr(from) {
		return errSiteLoop
	}
	host := f.domains[0]
	if sni := strings.ToLower(strings.TrimSuffix(mp.HelloSNI(initial), ".")); slices.Contains(f.domains, sni) {
		host = sni
	}
	stop := time.Now().Add(siteMaxDuration)
	dctx, cancel := context.WithTimeout(ctx, siteDialTimeout)
	defer cancel()
	ips, err := siteLookup(dctx, "ip", host)
	if err != nil {
		return fmt.Errorf("site fallback: %w", err)
	}
	// Every address the name resolves to must be public and not the node's: a name pointed at the node would loop, one
	// pointed at a private, link-local, CGNAT or reserved address would make the node a door into its own networks.
	for _, ip := range ips {
		if !siteTargetOK(ip.Unmap()) {
			if t, prev := time.Now().UnixNano(), f.warned.Load(); t-prev >= int64(time.Minute) && f.warned.CompareAndSwap(prev, t) {
				f.log.Warn("mtproto site fallback: the domain resolves to an address that is not public or is this node's, not forwarding",
					"domain", host, "address", ip.String())
			}
			return errSiteTarget
		}
	}
	var up net.Conn
	for _, ip := range ips {
		if up, err = (&net.Dialer{}).DialContext(dctx, "tcp", netip.AddrPortFrom(ip.Unmap(), sitePort).String()); err == nil {
			break
		}
	}
	if up == nil {
		return fmt.Errorf("site fallback: %s has no reachable address: %v", host, err)
	}
	if !f.add(up) {
		return net.ErrClosed
	}
	defer f.remove(up)
	_ = up.SetWriteDeadline(stop)
	if _, err := up.Write(initial); err != nil {
		return err
	}
	return splice(conn, up, stop)
}

func (f *forwarder) enter(peer netip.Prefix, conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.total >= siteMaxConns || f.peers[peer] >= siteMaxPerPeer {
		return false
	}
	f.total++
	f.peers[peer]++
	f.conns[conn] = struct{}{}
	return true
}

func (f *forwarder) leave(peer netip.Prefix, conn net.Conn) {
	f.mu.Lock()
	f.total--
	if f.peers[peer]--; f.peers[peer] <= 0 {
		delete(f.peers, peer)
	}
	delete(f.conns, conn)
	f.mu.Unlock()
	_ = conn.Close()
}

func (f *forwarder) add(c net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = c.Close()
		return false
	}
	f.conns[c] = struct{}{}
	return true
}

func (f *forwarder) remove(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
	_ = c.Close()
}

// shutdown closes every forwarded connection; the Serve goroutines then return.
func (f *forwarder) shutdown() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for c := range f.conns {
		_ = c.Close()
	}
}

// splice copies both ways until both directions end, nothing moves for siteIdle, or stop passes. A clean end of one
// direction half-closes the other side's write.
func splice(a, b net.Conn, stop time.Time) error {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			deadline := time.Now().Add(siteIdle)
			if stop.Before(deadline) {
				deadline = stop
			}
			_ = src.SetReadDeadline(deadline)
			n, err := src.Read(buf)
			if n > 0 {
				last.Store(time.Now().UnixNano())
				_ = dst.SetWriteDeadline(stop)
				if _, werr := dst.Write(buf[:n]); werr != nil {
					done <- werr
					return
				}
			}
			switch {
			case errors.Is(err, os.ErrDeadlineExceeded) && time.Now().Before(stop) &&
				time.Since(time.Unix(0, last.Load())) < siteIdle:
				continue // this direction is quiet, the other one is not
			case errors.Is(err, io.EOF):
				if cw, ok := dst.(interface{ CloseWrite() error }); ok {
					done <- cw.CloseWrite()
				} else {
					done <- errors.New("cannot half-close")
				}
				return
			case err != nil:
				done <- err
				return
			}
		}
	}
	go copyOne(b, a)
	go copyOne(a, b)
	err := <-done
	if err != nil {
		_ = a.Close()
		_ = b.Close()
	}
	if err2 := <-done; err == nil {
		err = err2
	}
	return err
}

func peerAddr(c net.Conn) netip.Addr {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}

// peerKey is the limit key of a peer: the IPv4 address or the IPv6 /64.
func peerKey(addr netip.Addr) netip.Prefix {
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	p, _ := addr.Prefix(bits)
	return p
}

// publicUnicast reports an address on the public internet: not loopback, unspecified, multicast, link-local, private
// (RFC 1918, ULA), CGNAT, documentation, benchmarking or otherwise reserved.
func publicUnicast(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range reservedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // this network
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
	netip.MustParsePrefix("100::/64"),        // discard
	netip.MustParsePrefix("2001:2::/48"),     // benchmarking
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
}

// isLocalAddr reports an address of this node: loopback, unspecified or on one of its interfaces.
func isLocalAddr(a netip.Addr) bool {
	if a.IsLoopback() || a.IsUnspecified() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, ia := range addrs {
		if n, ok := ia.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == a {
				return true
			}
		}
	}
	return false
}
