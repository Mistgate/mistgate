//go:build !js

package subhost

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	nodehost "github.com/mistgate/mistgate/internal/node/subhost"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/store/storetest"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/statehash"
)

const e2eDomain = "sub.test"

// onlineFleet is the real fleet with every node online: the agents of this test are only their tunnel ends.
type onlineFleet struct{ *fleet.Fleet }

func (onlineFleet) HasSession(string) bool { return true }

type e2e struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	fl    *fleet.Fleet
	mu    sync.Mutex
	addrs map[string]string // node address -> the loopback listener standing for its TCP 443
	now   func() time.Time  // the panel's clock; nil = time.Now
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, vault.KeySize)
	_, _ = rand.Read(key)
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := protocols.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	fl, err := fleet.New(st, v, reg, fleet.Config{AgentSNI: "agents.invalid",
		Desired: func(context.Context, string) ([]statehash.Inbound, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return &e2e{t: t, ctx: ctx, st: st, fl: fl, addrs: map[string]string{}}
}

// e2eNode is a host node: its TCP 443 (a loopback listener) wrapped by the real node subscription host, with a stub site.
type e2eNode struct {
	id   string
	host *nodehost.Host
	addr string
}

// node enrols a node in the fleet (as the SSH install does, inside a session) and starts its 443.
func (e *e2e) node(name, address string) *e2eNode {
	t := e.t
	t.Helper()
	id := store.NewID("nod_")
	now := time.Now()
	token, _, err := e.fl.CreateProvisionEnrollment(e.ctx, provision.NodeSpec{ID: id, Name: name, Address: address}, "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "mistgate-node"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	got, certPEM, caPEM, err := e.fl.EnrollInSession(e.ctx, token, csr)
	if err != nil || got != id {
		t.Fatalf("enrol %s: %q %v", name, got, err)
	}
	blk, _ := pem.Decode(certPEM)
	cert := &tls.Certificate{Certificate: [][]byte{blk.Bytes}, PrivateKey: key}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET agent_caps = ? WHERE id = ?`, strings.Join(allCaps, " "), id); err != nil {
		t.Fatal(err)
	}

	h := nodehost.New(nodehost.Identity{Cert: func() *tls.Certificate { return cert }, CAPool: func() *x509.CertPool { return pool }}, nil)
	t.Cleanup(h.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	site := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "site") }),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedFor(t, "node-site")}},
	}
	go func() { _ = site.ServeTLS(h.WrapSite(ln, 443, false), "", "") }()
	t.Cleanup(func() { _ = site.Close() })
	e.mu.Lock()
	e.addrs[address] = ln.Addr().String()
	e.mu.Unlock()
	return &e2eNode{id: id, host: h, addr: ln.Addr().String()}
}

func (e *e2e) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	target := e.addrs[host]
	e.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, target)
}

// panel is one run of the panel's subscription host service with its public HTTPS server (which answers RemoteAddr).
type e2ePanel struct {
	svc  *Service
	stop func()
}

func (e *e2e) panel() *e2ePanel {
	t := e.t
	t.Helper()
	svc := New(Config{Store: e.st, Fleet: onlineFleet{e.fl}, Addr: pubaddr.New("https://old.example"), StepUp: func(context.Context) error { return nil },
		LinkPrefix: "/lnk/", Resolver: fakeResolver{}, Dial: e.dial, Now: e.now,
		CAA: func(context.Context, string) ([]string, bool, error) { return nil, false, nil }})
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	pub := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.RemoteAddr) }),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedFor(t, e2eDomain)}},
	}
	go func() { _ = pub.ServeTLS(svc.PublicListener(), "", "") }()
	p := &e2ePanel{svc: svc}
	var once sync.Once
	p.stop = func() { once.Do(func() { cancel(); <-done; _ = pub.Close() }) }
	t.Cleanup(p.stop)
	return p
}

func selfSignedFor(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fetch opens TLS for sni on addr and returns the certificate's name, the body and the client's own address.
func fetch(t *testing.T, addr, sni string) (cn, body, local string) {
	t.Helper()
	d := &net.Dialer{Timeout: 5 * time.Second}
	c, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatalf("TLS to %s for %s: %v", addr, sni, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+sni+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("response for %s: %v", sni, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return c.ConnectionState().PeerCertificates[0].Subject.CommonName, string(b), c.LocalAddr().String()
}

func waitFor(t *testing.T, within time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wantPanel checks that sni on the node's 443 reaches the panel, which sees the client's own address.
func wantPanel(t *testing.T, n *e2eNode) {
	t.Helper()
	cn, body, local := fetch(t, n.addr, e2eDomain)
	if cn != e2eDomain || body != local {
		t.Fatalf("via %s: certificate %q, the panel saw %q, client is %q", n.id, cn, body, local)
	}
}

func wantSite(t *testing.T, n *e2eNode, sni string) {
	t.Helper()
	if cn, body, _ := fetch(t, n.addr, sni); cn != "node-site" || body != "site" {
		t.Fatalf("%s via %s: certificate %q body %q, want the node's site", sni, n.id, cn, body)
	}
}

// The subscription host end to end on loopback: the real panel service dials the real node subscription host, the
// subscription name reaches the panel's public server with the client's address, everything else stays on the node; the
// tunnel comes back after a panel restart, and a move hands the name to the second node.
func TestSubscriptionThroughHost(t *testing.T) {
	e := newE2E(t)
	a := e.node("a", "203.0.113.1")
	b := e.node("b", "203.0.113.2")
	p := e.panel()
	r := rpc{p.svc}
	if _, err := r.SetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.SetSubscriptionHostRequest{NodeId: a.id, Domain: e2eDomain, SkipDnsCheck: true})); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(e.ctx, e.st)
	if err != nil {
		t.Fatal(err)
	}
	// What the fleet would tell the node in its settings.
	a.host.Configure(nodehost.Config{TunnelSNI: st.SNI, Names: []string{e2eDomain}})
	waitFor(t, 5*time.Second, a.host.TunnelUp, "tunnel to a")

	wantPanel(t, a)
	wantSite(t, a, "other.test")

	// The panel stops: the name falls back to the node's own site.
	p.stop()
	waitFor(t, 5*time.Second, func() bool { return !a.host.TunnelUp() }, "tunnel to a down")
	wantSite(t, a, e2eDomain)

	// It starts again: the tunnel is back within 5 s.
	p = e.panel()
	r = rpc{p.svc}
	waitFor(t, 5*time.Second, a.host.TunnelUp, "tunnel to a after the restart")
	wantPanel(t, a)

	// A move: both nodes forward until it finishes, then only the second.
	if _, err := r.StartSubscriptionHostMove(e.ctx, connect.NewRequest(&adminv1.StartSubscriptionHostMoveRequest{NodeId: b.id})); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(e.ctx, e.st); err != nil {
		t.Fatal(err)
	}
	b.host.Configure(nodehost.Config{TunnelSNI: st.NextSNI, Names: []string{e2eDomain}})
	waitFor(t, 5*time.Second, b.host.TunnelUp, "tunnel to b")
	wantPanel(t, a)
	wantPanel(t, b)
	if _, err := r.FinishSubscriptionHostChange(e.ctx, connect.NewRequest(&adminv1.FinishSubscriptionHostChangeRequest{})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return !a.host.TunnelUp() }, "the panel to drop the tunnel to a")
	wantSite(t, a, e2eDomain)
	wantPanel(t, b)
}

// The panel tells a node that answered as its site (here: the panel's clock 5 minutes ahead, so the node refuses the
// mark) from one it cannot reach.
func TestTunnelDownReason(t *testing.T) {
	reason := func(p *e2ePanel) string {
		v := p.svc.tunnelViews()
		if len(v) != 1 || v[0].GetUp() {
			return ""
		}
		return v[0].GetDownReason()
	}
	for _, tc := range []struct {
		name, want string
		setup      func(e *e2e, n *e2eNode)
	}{
		{"clock", reasonNotRecognised, func(e *e2e, _ *e2eNode) { e.now = func() time.Time { return time.Now().Add(5 * time.Minute) } }},
		{"unreachable", reasonUnreachable, func(e *e2e, n *e2eNode) { e.addrs["203.0.113.1"] = "127.0.0.1:1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newE2E(t)
			a := e.node("a", "203.0.113.1")
			tc.setup(e, a)
			p := e.panel()
			if _, err := (rpc{p.svc}).SetSubscriptionHost(e.ctx, connect.NewRequest(&adminv1.SetSubscriptionHostRequest{NodeId: a.id, Domain: e2eDomain, SkipDnsCheck: true})); err != nil {
				t.Fatal(err)
			}
			st, err := loadState(e.ctx, e.st)
			if err != nil {
				t.Fatal(err)
			}
			a.host.Configure(nodehost.Config{TunnelSNI: st.SNI, Names: []string{e2eDomain}})
			waitFor(t, 5*time.Second, func() bool { return reason(p) == tc.want }, "down reason "+tc.want)
			if a.host.TunnelUp() {
				t.Fatal("tunnel up")
			}
			if got := p.svc.TunnelDownReason(a.id); got != tc.want {
				t.Fatalf("TunnelDownReason = %q", got)
			}
		})
	}
}
