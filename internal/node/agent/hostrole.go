package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/subhost"
)

// The panel's routing of this node (issue #48): the subscription-host role (NodeSettings.host_tunnel, Meta.Tunnel) and a
// new panel link address (NodeSettings.panel_link_url, Meta.NextLink).

func subhostConfig(t *TunnelMeta) subhost.Config {
	if t == nil {
		return subhost.Config{}
	}
	return subhost.Config{TunnelSNI: t.SNI, Names: slices.Clone(t.Names)}
}

// tunnelMetaOf is the role a HostTunnel asks for; nil = off (absent, or no tunnel SNI).
func tunnelMetaOf(t *pb.HostTunnel) *TunnelMeta {
	if t.GetTunnelSni() == "" {
		return nil
	}
	return &TunnelMeta{SNI: t.GetTunnelSni(), Names: slices.Clone(t.GetForwardNames())}
}

func sameTunnel(x, y *TunnelMeta) bool {
	if x == nil || y == nil {
		return x == y
	}
	return x.SNI == y.SNI && slices.Equal(x.Names, y.Names)
}

// linkURL is the link base address the agent dials (cfg.LinkURL, which follows Meta.Link once a new address works).
func (a *Agent) linkURL() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.cfg.LinkURL
}

func (a *Agent) nextLink() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.meta.NextLink
}

// tunnelRole says whether this node is the subscription host.
func (a *Agent) tunnelRole() bool {
	if a.subhost == nil {
		return false
	}
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.meta.Tunnel != nil
}

func (a *Agent) tunnelMeta() *TunnelMeta {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.Tunnel == nil {
		return nil
	}
	t := *a.meta.Tunnel
	t.Names = slices.Clone(t.Names)
	return &t
}

// applyPanelRouting takes in the routing part of NodeSettings. reconnect: the current session should end so that the next
// one tries the new link address (never for a session inside the tunnel, which does not depend on that address).
func (a *Agent) applyPanelRouting(st *pb.NodeSettings) (reconnect bool) {
	want := tunnelMetaOf(st.GetHostTunnel())
	a.metaMu.Lock()
	tunnelChanged := !sameTunnel(a.meta.Tunnel, want)
	dirty := tunnelChanged
	if tunnelChanged {
		a.meta.Tunnel = want
	}
	if raw := st.GetPanelLinkUrl(); raw != "" {
		switch _, err := parseLinkBase(raw); {
		case err != nil:
			a.log.Warn("panel link url rejected", "err", err) // the address itself holds a secret: it is not logged
		case raw == a.meta.Link:
			if a.meta.NextLink != "" {
				a.meta.NextLink = ""
				dirty = true
			}
		case raw != a.meta.NextLink:
			a.meta.NextLink = raw
			dirty = true
			reconnect = !a.inTunnel.Load()
		}
	}
	if dirty {
		if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
			a.log.Error("panel routing not saved", "err", err)
		}
	}
	a.metaMu.Unlock()
	if tunnelChanged && a.subhost != nil {
		a.log.Info("subscription host role changed", "on", want != nil)
		a.subhost.Configure(subhostConfig(want))
		a.refreshStandaloneSite()
	}
	if reconnect {
		a.log.Info("the panel sent a new link address; reconnecting")
	}
	return reconnect
}

// endForNewLink ends the current session, after what it has queued is written, so the next one tries the new link address.
func (a *Agent) endForNewLink() {
	s := a.cur.Load()
	if s == nil {
		return
	}
	a.switchPending.Store(true)
	go func() {
		wait := durOr(a.heldFlush, heldFlushWait)
		s.flush(wait)
		s.drain(wait)
		s.cancel()
	}()
}

// nextLinkSession runs a session over the new link address. Once its HelloAck arrives the address replaces the old one
// for good. A failure is an ordinary connection failure (backoff), and the next turn uses the old address.
func (a *Agent) nextLinkSession(ctx context.Context, next string) error {
	a.log.Info("trying the new panel link address")
	err := a.linkSession(ctx, func(ctx context.Context) (*websocketAgentTransport, func(), error) {
		t, cleanup, err := a.dialLinkBase(ctx, next)
		if err == nil {
			t.onEstablished = func() { a.commitNextLink(next) }
		}
		return t, cleanup, err
	})
	if errors.Is(err, errLinkNotEstablished) {
		return fmt.Errorf("new panel link address: %v", err) // not errLinkNotEstablished: no mTLS hold period for it
	}
	return err
}

func (a *Agent) commitNextLink(next string) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.NextLink != next {
		return
	}
	a.meta.Link, a.meta.NextLink = next, ""
	a.cfg.LinkURL = next
	if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
		a.log.Error("new panel link address not saved", "err", err)
		return
	}
	a.log.Info("new panel link address in use")
}

// tunnelLinkSession runs the agent's session inside the subscription-host tunnel: a link stream to the panel, the same
// signed (and sealed) link handshake with the tunnel SNI as its audience.
func (a *Agent) tunnelLinkSession(ctx context.Context) error {
	t := a.tunnelMeta()
	if t == nil {
		return errors.New("no subscription-host tunnel")
	}
	a.inTunnel.Store(true)
	defer a.inTunnel.Store(false)
	err := a.linkSession(ctx, func(ctx context.Context) (*websocketAgentTransport, func(), error) {
		client := &http.Client{Transport: &http.Transport{
			DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return a.subhost.DialPanel(ctx) },
			MaxIdleConns:    1,
			IdleConnTimeout: 30 * time.Second,
		}}
		endpoint := "ws://" + t.SNI + "/link/" + url.PathEscape(a.meta.NodeID)
		return a.dialLinkAt(ctx, endpoint, t.SNI, client, client.CloseIdleConnections)
	})
	if errors.Is(err, errLinkNotEstablished) {
		return fmt.Errorf("panel link in the tunnel: %v", err) // an ordinary failure: backoff, no mTLS hold period
	}
	return err
}

// standaloneSite is the subscription host's own site on TCP 443 while no engine serves a site there: the panel's tunnel
// and the subscription clients need the port, and everyone else gets the decoy.
type standaloneSite struct {
	mu     sync.Mutex
	held   bool // a reconcile keeps 443 free for the engines until syncStandaloneSite
	srv    *http.Server
	ln     net.Listener
	cert   *tls.Certificate
	failed string // the last bind error reported
}

// stopStandaloneSite frees 443 for the engines of a reconcile.
func (a *Agent) stopStandaloneSite() {
	if a.subhost == nil {
		return
	}
	a.standalone.mu.Lock()
	defer a.standalone.mu.Unlock()
	a.standalone.held = true
	a.standalone.closeLocked()
}

// syncStandaloneSite ends a reconcile: the site runs when the role is on and no engine's site took 443.
func (a *Agent) syncStandaloneSite() {
	if a.subhost == nil {
		return
	}
	a.standalone.mu.Lock()
	defer a.standalone.mu.Unlock()
	a.standalone.held = false
	a.syncStandaloneLocked()
}

// refreshStandaloneSite follows a role change outside a reconcile.
func (a *Agent) refreshStandaloneSite() {
	if a.subhost == nil {
		return
	}
	a.standalone.mu.Lock()
	defer a.standalone.mu.Unlock()
	if !a.standalone.held {
		a.syncStandaloneLocked()
	}
}

// closeHostRole stops the site and the tunnel when the agent stops.
func (a *Agent) closeHostRole() {
	if a.subhost == nil {
		return
	}
	a.standalone.mu.Lock()
	a.standalone.held = true
	a.standalone.closeLocked()
	a.standalone.mu.Unlock()
	a.subhost.Close()
}

func (s *standaloneSite) closeLocked() {
	if s.srv != nil {
		_ = s.srv.Close()
		_ = s.ln.Close()
		s.srv, s.ln = nil, nil
	}
}

func (a *Agent) syncStandaloneLocked() {
	s := &a.standalone
	if !a.tunnelRole() {
		s.closeLocked()
		s.failed = ""
		return
	}
	if s.srv != nil || a.subhost.Wrapped443() > 0 {
		return
	}
	listen := a.standaloneListen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", ":443")
	if err == nil && s.cert == nil {
		s.cert, err = standaloneCert()
		if err != nil {
			_ = ln.Close()
		}
	}
	if err != nil {
		if msg := shortMsg(err); s.failed != msg {
			s.failed = msg
			a.log.Warn("subscription host site not started", "code", "sub_host_site_failed", "err", err)
			a.event(pb.Severity_SEVERITY_WARNING, "sub_host_site_failed", "", map[string]string{"error": msg})
		}
		return
	}
	s.failed = ""
	srv := &http.Server{
		Handler: a.masquerade(""),
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{*s.cert},
			NextProtos:   []string{"h2", "http/1.1"},
		},
		// Same bounds as the engines' sites: the listener faces the open internet.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	s.srv, s.ln = srv, a.subhost.WrapSite(ln, 443, false)
	go func(ln net.Listener) { _ = srv.ServeTLS(ln, "", "") }(s.ln)
}

// standaloneCert is the site's self-signed certificate (in memory: the site is a decoy, nothing verifies it).
func standaloneCert() (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}
