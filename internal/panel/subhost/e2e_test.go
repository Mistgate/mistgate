//go:build !js

package subhost

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	nodesubhost "github.com/mistgate/mistgate/internal/node/subhost"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The subscription host end to end on loopback (design/adr/0018-panel-behind-nat.md): the panel's service as it runs (state,
// its dialer with a short certificate of a real CA, its listener served with the subdomain's certificate) and a host made of the
// node's pieces (the front on "443" with the node's site and the subscription route, the host's bridge entry, the intake).

const (
	e2eDomain = "sub.example.test"
	e2eSite   = "de1.example.test"
)

type e2eCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newE2ECA(t *testing.T) e2eCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "7c1e5a90b2d4"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return e2eCA{cert: cert, key: key, pool: pool}
}

// leaf signs a certificate for a DNS name (a site) or, with uri, a client certificate.
func (c e2eCA) leaf(t *testing.T, name, uri string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "a91c"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	if uri != "" {
		u, _ := url.Parse(uri)
		tpl.URIs, tpl.ExtKeyUsage = []*url.URL{u}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tpl.DNSNames, tpl.ExtKeyUsage = []string{name}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// e2eFleet is the panel's fleet as the service sees it: the host online, its site verified by a pin, the panel's identity of
// the CA.
type e2eFleet struct {
	*fakeFleet
	panel tls.Certificate
	port  uint16
	pin   string
}

func (f *e2eFleet) PanelIdentity(time.Time) ([]byte, *ecdsa.PrivateKey, error) {
	return f.panel.Certificate[0], f.panel.PrivateKey.(*ecdsa.PrivateKey), nil
}

func (f *e2eFleet) SubHostTarget(context.Context, string, string, string) (string, string, string, bool, bool) {
	return "127.0.0.1", e2eSite, f.pin, false, true
}

// e2eHost is the node: its front, its site, the host entry and the intake, on loopback.
type e2eHost struct {
	front  *front.Front
	addr   string
	entry  *bridge.Entry
	intake *nodesubhost.Intake
}

func newE2EHost(t *testing.T, ca e2eCA, site tls.Certificate, nodeID, path string) *e2eHost {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &e2eHost{addr: ln.Addr().String(), front: front.New(ln, nil), entry: bridge.NewEntry()}
	h.front.SetRoutes([]front.Route{{ID: plugin.SubHostSiteRouteID, Names: []string{e2eSite}}, {ID: plugin.SubHostRouteID, Names: []string{e2eDomain}}}, plugin.SubHostSiteRouteID)
	if err := h.entry.SetServe(bridge.ServeConfig{EntryNodeID: nodeID, Path: path, ExitNodeIDs: []string{bridge.PanelPeerID}, Roots: ca.pool,
		Peer: bridge.PeerPanel, PassClient: true}); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h.entry.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "node site") })),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{site}, NextProtos: []string{"h2", "http/1.1"}}}
	if err := sitehttp.ConfigureSiteHTTP2(srv); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(tls.NewListener(h.front.Listener(plugin.SubHostSiteRouteID), srv.TLSConfig)) }()
	h.intake = nodesubhost.New(h.entry, h.front, plugin.SubHostRouteID, nil)
	h.intake.SetSources([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	go h.intake.Serve(h.front.Listener(plugin.SubHostRouteID))
	t.Cleanup(func() {
		h.intake.Close()
		_ = h.front.Close()
		h.entry.Close()
		_ = srv.Close()
	})
	return h
}

// get asks the host's 443 for name as a person would; it returns the name of the certificate it was shown and the body.
func (h *e2eHost) get(t *testing.T, name string, roots *x509.CertPool) (shown, body string) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", h.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	conn := tls.Client(raw, &tls.Config{ServerName: name, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err := conn.Handshake(); err != nil {
		t.Fatalf("TLS for %s: %v (never a reset)", name, err)
	}
	shown = conn.ConnectionState().PeerCertificates[0].DNSNames[0]
	if shown != name {
		return shown, ""
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+name+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(conn)
	if i := strings.Index(string(reply), "\r\n\r\n"); i >= 0 {
		reply = reply[i+4:]
	}
	return shown, string(reply)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestSubscriptionHostEndToEnd(t *testing.T) {
	ca := newE2ECA(t)
	site := ca.leaf(t, e2eSite, "")
	sub := ca.leaf(t, e2eDomain, "")
	sum := sha256.Sum256(site.Certificate[0])

	e := newEnv(t)
	a := e.node("a", "203.0.113.1", allCaps, true)
	fl := &e2eFleet{fakeFleet: e.fl, panel: ca.leaf(t, "", "spiffe://panel/ca_1"), pin: hex.EncodeToString(sum[:])}
	fl.status = map[string]*agentv1.SubHostStatus{a: {}}

	// The panel: its service and its public server on the listener of the subdomain.
	startPanel := func() *Service {
		svc := New(Config{Store: e.st, Fleet: fl, Addr: e.addr, StepUp: func(context.Context) error { return nil }, LinkPrefix: "/lnk0123456789abcdef/",
			Resolver: e.res, Now: time.Now, CAA: func(context.Context, string) ([]string, bool, error) { return nil, false, nil },
			Certificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &sub, nil }, HostPort: fl.port})
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "panel saw "+r.RemoteAddr) })}
		go func() { _ = srv.Serve(tls.NewListener(svc.PublicListener(), svc.PublicTLS())) }()
		t.Cleanup(func() { svc.Close(); _ = srv.Close() })
		return svc
	}
	svc := startPanel()
	if _, err := (rpc{svc}).SetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.SetSubscriptionHostRequest{NodeId: a, Domain: e2eDomain, SkipDnsCheck: true})); err != nil {
		t.Fatal(err)
	}
	path := e.state().Path
	host := newE2EHost(t, ca, site, a, path)
	_, portText, _ := net.SplitHostPort(host.addr)
	port, _ := strconv.Atoi(portText)
	fl.port = uint16(port)
	svc.cfg.HostPort = fl.port
	svc.mu.Lock()
	svc.syncDials(svc.current())
	svc.mu.Unlock()
	waitUntil(t, "the panel connects to the host", func() bool { return svc.bridgeUp(a) && host.entry.Connected(bridge.PanelPeerID) })

	// A person's subscription goes to the panel; the node's own name is its site. (The intake
	// looks at the panel's connection once a second: until then the subdomain was the site.)
	var shown, body string
	waitUntil(t, "the route comes up", func() bool {
		shown, body = host.get(t, e2eDomain, ca.pool)
		return shown == e2eDomain
	})
	// (a loopback visitor's address is not passed on: only a global unicast one is, internal/node/bridge passableClient)
	if shown != e2eDomain || body != "panel saw 0.0.0.0:0" {
		t.Fatalf("subscription: shown %q, body %q", shown, body)
	}
	if shown, body := host.get(t, e2eSite, ca.pool); shown != e2eSite || body != "node site" {
		t.Fatalf("site: %q %q", shown, body)
	}
	if s := host.intake.Stats(); s.Diverted != 0 {
		t.Fatalf("nothing should have been diverted: %+v", s)
	}

	// The host's own link goes through the bridge, as an agent's link (the panel sees no person's address).
	conn, err := host.intake.DialLink(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(conn, &tls.Config{ServerName: e2eDomain, RootCAs: ca.pool, NextProtos: []string{"http/1.1"}})
	_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(tc, "GET / HTTP/1.1\r\nHost: "+e2eDomain+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("the host's own link: %v", err)
	}
	reply, _ := io.ReadAll(tc)
	if !strings.Contains(string(reply), "panel saw 0.0.0.0:0") {
		t.Fatalf("the host's own link reached %q", reply)
	}
	_ = tc.Close()

	// Readiness: everything holds but the site's public certificate (a pin here), so the panel keeps its address.
	g, err := (rpc{svc}).GetSubscriptionHost(e.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
	if err != nil || g.Msg.Host.Ready || strings.Join(g.Msg.Host.NotReady, ",") != notReadySiteCert || len(g.Msg.Host.Bridges) != 1 || !g.Msg.Host.Bridges[0].Up {
		t.Fatalf("view: %v %v", g.Msg.GetHost(), err)
	}

	// The panel goes away: the subdomain is the node's site at once, never a reset.
	svc.Close()
	waitUntil(t, "the host notices", func() bool { return !host.entry.Connected(bridge.PanelPeerID) })
	waitUntil(t, "the route goes down", func() bool {
		shown, _ := host.get(t, e2eDomain, ca.pool)
		return shown == e2eSite
	})

	// The panel restarts and the subscriptions come back, with nothing done by hand.
	svc = startPanel()
	if err := svc.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the panel reconnects", func() bool { return svc.bridgeUp(a) })
	waitUntil(t, "subscriptions come back", func() bool {
		shown, body := host.get(t, e2eDomain, ca.pool)
		return shown == e2eDomain && strings.HasPrefix(body, "panel saw ")
	})
}
