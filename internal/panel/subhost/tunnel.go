//go:build !js

package subhost

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/hosttunnel"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	backoffMin = time.Second
	backoffMax = time.Minute
	// aliveReset: a tunnel that stayed up this long starts the next backoff from the beginning.
	aliveReset = time.Minute
	// confirmUp: a tunnel counts as up once the node opens a stream in it or keeps it this long. TLS 1.3 completes on the
	// panel's side before the node has judged the panel's certificate, so a refused panel would otherwise flicker up.
	confirmUp = 3 * time.Second
)

var errTunnelUnused = errors.New("the node closed the tunnel before using it")

// tunnel is the dialer of one host node.
type tunnel struct {
	nodeID, sni string
	cancel      context.CancelFunc
	up          bool
	since       time.Time // when it last went up or down (or started)
	reason      string    // why it is down: "" (up or not tried yet), reasonUnreachable or reasonNotRecognised
	downSent    bool      // the down event was recorded for the current outage
}

// Why a tunnel is down (SubscriptionHostTunnel.down_reason).
const (
	reasonUnreachable = "unreachable"
	// reasonNotRecognised: the node answered with a certificate that is not its own, i.e. as its site: it did not accept
	// the panel's mark (clocks apart, an older agent, another tunnel secret).
	reasonNotRecognised = "not_recognised"
)

// reconcile runs one dialer per wanted node (id -> tunnel SNI) and stops the others. Dialers start only once Run runs.
func (s *Service) reconcile(want map[string]string) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	for id, t := range s.tunnels {
		if sni, ok := want[id]; !ok || sni != t.sni {
			t.cancel()
			delete(s.tunnels, id)
		}
	}
	if s.runCtx == nil {
		return
	}
	for id, sni := range want {
		if _, ok := s.tunnels[id]; ok || id == "" {
			continue
		}
		ctx, cancel := context.WithCancel(s.runCtx)
		t := &tunnel{nodeID: id, sni: sni, cancel: cancel, since: s.cfg.Now()}
		s.tunnels[id] = t
		go s.dialLoop(ctx, t)
	}
}

// Run applies the stored state, dials the tunnels, serves the agent link they carry and watches them until ctx ends.
func (s *Service) Run(ctx context.Context) {
	linkSrv := &http.Server{
		Handler:           s.linkGuard(),
		ReadHeaderTimeout: 30 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if t, ok := c.(nodeConn); ok {
				return context.WithValue(ctx, nodeKey{}, t.nodeID)
			}
			return ctx
		},
	}
	go func() { _ = linkSrv.Serve(s.links) }()
	s.tmu.Lock()
	s.runCtx = ctx
	s.tmu.Unlock()
	if err := s.Load(ctx); err != nil {
		s.cfg.Log.Error("subscription host: read state", "code", "sub_host_state_read_failed", "err", err)
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.pub.Close()
			_ = linkSrv.Close()
			_ = s.links.Close()
			return
		case <-tick.C:
			s.checkTunnels(ctx)
		}
	}
}

// checkTunnels records sub_host_tunnel_down (or sub_host_tunnel_not_recognised) for a tunnel down for tunnelDownAfter
// straight, and sub_host_tunnel_up when one comes back after that.
func (s *Service) checkTunnels(ctx context.Context) {
	now := s.cfg.Now()
	type ev struct {
		node, code string
	}
	var evs []ev
	s.tmu.Lock()
	for _, t := range s.tunnels {
		switch {
		case !t.up && !t.downSent && now.Sub(t.since) >= tunnelDownAfter:
			t.downSent = true
			code := "sub_host_tunnel_down"
			if t.reason == reasonNotRecognised {
				code = "sub_host_tunnel_not_recognised"
			}
			evs = append(evs, ev{t.nodeID, code})
		case t.up && t.downSent:
			t.downSent = false
			evs = append(evs, ev{t.nodeID, "sub_host_tunnel_up"})
		}
	}
	s.tmu.Unlock()
	for _, e := range evs {
		row := store.EventRow{Time: now, Severity: 2, Code: e.code, Source: "panel", NodeID: e.node}
		if e.code == "sub_host_tunnel_up" {
			row.Severity = 1
		}
		if err := s.cfg.Store.InsertEvent(ctx, row); err != nil {
			s.cfg.Log.Warn("subscription host: record tunnel event", "code", "sub_host_event_failed", "err", err)
		}
	}
}

func (s *Service) setUp(t *tunnel, up bool, reason string) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if t.up != up {
		t.up, t.since = up, s.cfg.Now()
	}
	t.reason = reason
}

// TunnelDownReason is why the tunnel of nodeID is down: "" when it is up or unknown.
func (s *Service) TunnelDownReason(nodeID string) string {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if t, ok := s.tunnels[nodeID]; ok && !t.up {
		return t.reason
	}
	return ""
}

func (s *Service) tunnelViews() []*adminv1.SubscriptionHostTunnel {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	out := make([]*adminv1.SubscriptionHostTunnel, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		out = append(out, &adminv1.SubscriptionHostTunnel{NodeId: t.nodeID, Up: t.up, SinceUnix: t.since.Unix(), DownReason: t.reason})
	}
	return out
}

func (s *Service) dialLoop(ctx context.Context, t *tunnel) {
	backoff := backoffMin
	for ctx.Err() == nil {
		start := s.cfg.Now()
		err := s.serveTunnel(ctx, t)
		if err != nil && ctx.Err() == nil {
			s.cfg.Log.Debug("subscription host: tunnel", "node_id", t.nodeID, "err", err)
		}
		switch {
		case err == nil:
			s.setUp(t, false, "")
		case errors.Is(err, hosttunnel.ErrNotRecognised):
			s.setUp(t, false, reasonNotRecognised)
		default:
			s.setUp(t, false, reasonUnreachable)
		}
		if s.cfg.Now().Sub(start) >= aliveReset {
			backoff = backoffMin
		}
		wait := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// serveTunnel dials the node with the subscription name as SNI and Chrome's ClientHello carrying the mark of the tunnel
// secret, proves the panel with a certificate of the fleet CA for the tunnel secret, checks the node's own certificate,
// and serves the node's streams over HTTP/2 until the connection ends.
func (s *Service) serveTunnel(ctx context.Context, t *tunnel) error {
	s.tmu.Lock()
	domain := s.cur.Domain
	s.tmu.Unlock()
	n, err := s.cfg.Store.Node(ctx, t.nodeID)
	if err != nil {
		return err
	}
	cert, err := s.cfg.Fleet.TunnelCertificate(t.sni)
	if err != nil {
		return err
	}
	raw, err := s.cfg.Dial(ctx, "tcp", net.JoinHostPort(n.Address, "443"))
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	nodeID := t.nodeID
	hsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tc, err := hosttunnel.Dial(hsCtx, raw, hosttunnel.DialConfig{
		ServerName: domain, Secret: t.sni, Certificate: cert, Now: s.cfg.Now,
		VerifyNode: func(rawCerts [][]byte) error { return s.cfg.Fleet.VerifyNodeCertificate(nodeID, rawCerts) },
	})
	cancel()
	if err != nil {
		return err
	}
	var once sync.Once
	var confirmed bool
	up := func() {
		once.Do(func() {
			confirmed = true
			s.setUp(t, true, "")
		})
	}
	timer := time.AfterFunc(confirmUp, up)
	accept := s.accept(nodeID)
	srv := &http2.Server{MaxConcurrentStreams: hosttunnel.MaxStreams, ReadIdleTimeout: hosttunnel.ReadIdle, PingTimeout: hosttunnel.PingTimeout}
	srv.ServeConn(tc, &http2.ServeConnOpts{Context: ctx, Handler: hosttunnel.ServerHandler(func(kind string, c net.Conn) bool {
		up()
		return accept(kind, c)
	})})
	timer.Stop()
	once.Do(func() {}) // a timer that fired just now has finished; one that did not never will
	if !confirmed {
		return errTunnelUnused
	}
	return nil
}

// accept hands a stream of the node's tunnel on: TLS of the subdomain to the public listener, the agent link to the link server.
func (s *Service) accept(nodeID string) func(kind string, c net.Conn) bool {
	return func(kind string, c net.Conn) bool {
		s.tmu.Lock()
		off := s.cur.State == stateOff
		s.tmu.Unlock()
		if off {
			return false
		}
		switch kind {
		case hosttunnel.KindTLS:
			return s.pub.push(c)
		case hosttunnel.KindLink:
			return s.links.push(nodeConn{Conn: c, nodeID: nodeID})
		}
		return false
	}
}

type nodeKey struct{}

// nodeConn is a link stream with the node whose tunnel carried it.
type nodeConn struct {
	net.Conn
	nodeID string
}

// linkGuard lets a tunnel carry only its own node's agent link, /link/<node id>, to the fleet's link handler (whose paths
// are relative to the link prefix); anything else is not found. The tunnel is private and the node proved its
// certificate on it, so the path has no secret prefix: the agent dials ws://<tunnel SNI>/link/<node id>, and the link's
// audience is the tunnel SNI.
func (s *Service) linkGuard() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID, _ := r.Context().Value(nodeKey{}).(string)
		if nodeID == "" || r.URL.Path != "/link/"+nodeID {
			http.NotFound(w, r)
			return
		}
		s.link.ServeHTTP(w, r)
	})
}

// chanListener is a net.Listener fed by the tunnels.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

// push hands c to Accept; false once the listener is closed.
func (l *chanListener) push(c net.Conn) bool {
	select {
	case <-l.done:
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

func (l *chanListener) Addr() net.Addr { return tunnelAddr{} }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "tunnel" }
func (tunnelAddr) String() string  { return "subscription-host" }
