package agent

import (
	"cmp"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"github.com/mistgate/mistgate/internal/hosttunnel"
	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
)

// sessionLinkPanel is a panel's link endpoint that runs the whole v2 handshake (sealed) and answers the Hello with a
// HelloAck. It records the audience (the Host) of every session that got as far as the Hello.
type sessionLinkPanel struct {
	h        *harness
	hellos   chan string
	settings *pb.NodeSettings // the HelloAck's; nil = empty
}

func newSessionLinkPanel(h *harness) *sessionLinkPanel {
	return &sessionLinkPanel{h: h, hellos: make(chan string, 8)}
}

func (p *sessionLinkPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()
	audience := r.Host
	panelNonce := make([]byte, 32)
	_, _ = rand.Read(panelNonce)
	if agentlink.WriteFrame(ctx, ws, &pb.LinkChallenge{Nonce: panelNonce, Audience: audience}) != nil {
		return
	}
	_, frame, err := agentlink.ReadFrame(ctx, ws)
	var auth pb.LinkAuth
	if err != nil || proto.Unmarshal(frame, &auth) != nil {
		return
	}
	pub := &p.h.a.id.Load().cert.PrivateKey.(*ecdsa.PrivateKey).PublicKey
	if !agentlink.Verify(pub, agentlink.AgentDigestV2(auth.NodeId, audience, panelNonce, auth.Nonce), auth.Signature) {
		return
	}
	keys, err := agentlink.SealKeys(p.h.panel.caKey, pub, auth.NodeId, audience, panelNonce, auth.Nonce)
	if err != nil {
		return
	}
	sig, err := agentlink.Sign(p.h.panel.caKey, agentlink.SealedPanelDigest(auth.NodeId, audience, panelNonce, auth.Nonce))
	if err != nil || agentlink.WriteFrame(ctx, ws, &pb.LinkAccept{Signature: sig}) != nil {
		return
	}
	link := agentlink.NewLink(ws, keys, false)
	_, frame, err = link.Read(ctx)
	var req pb.ConnectRequest
	if err != nil || proto.Unmarshal(frame, &req) != nil || req.GetHello() == nil {
		return
	}
	p.hellos <- audience
	if link.Write(ctx, &pb.ConnectResponse{Message: &pb.ConnectResponse_HelloAck{HelloAck: &pb.HelloAck{
		ServerTimeUnix: time.Now().Unix(), Settings: cmp.Or(p.settings, &pb.NodeSettings{}),
	}}}) != nil {
		return
	}
	for {
		if _, _, err := link.Read(ctx); err != nil {
			return
		}
	}
}

func (p *sessionLinkPanel) hello(t *testing.T) string {
	t.Helper()
	select {
	case got := <-p.hellos:
		return got
	case <-time.After(8 * time.Second):
		t.Fatal("no Hello reached the link panel")
		return ""
	}
}

func linkBaseOf(server *httptest.Server, c byte) string {
	return "wss://" + strings.TrimPrefix(server.URL, "https://") + "/" + strings.Repeat(string(c), 24) + "/"
}

func readMeta(t *testing.T, dir string) Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestApplyPanelRouting(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	var listened atomic.Int32
	a.standaloneListen = func(network, _ string) (net.Listener, error) {
		listened.Add(1)
		return net.Listen(network, "127.0.0.1:0")
	}
	t.Cleanup(a.closeHostRole)

	// The role on: saved, the subscription host configured, its own site on 443 (no engine serves one).
	tun := &pb.HostTunnel{TunnelSni: "t-7c1d.example.com", ForwardNames: []string{"sub.example.com"}}
	if a.applyPanelRouting(&pb.NodeSettings{HostTunnel: tun}) {
		t.Fatal("a role change asked for a reconnect")
	}
	if got := readMeta(t, h.dir).Tunnel; got == nil || got.SNI != tun.TunnelSni || len(got.Names) != 1 || got.Names[0] != "sub.example.com" {
		t.Fatalf("saved tunnel = %+v", got)
	}
	if !a.tunnelRole() || listened.Load() != 1 || a.standalone.srv == nil || a.subhost.Wrapped443() != 1 {
		t.Fatalf("role on: tunnelRole=%v listened=%d site=%v wrapped=%d", a.tunnelRole(), listened.Load(), a.standalone.srv != nil, a.subhost.Wrapped443())
	}
	// The same role again changes nothing.
	a.applyPanelRouting(&pb.NodeSettings{HostTunnel: tun})
	if listened.Load() != 1 {
		t.Fatal("an unchanged role restarted the site")
	}
	// An empty tunnel SNI is "off".
	a.applyPanelRouting(&pb.NodeSettings{HostTunnel: &pb.HostTunnel{}})
	if a.tunnelRole() || readMeta(t, h.dir).Tunnel != nil || a.standalone.srv != nil || a.subhost.Wrapped443() != 0 {
		t.Fatal("role off: still on")
	}

	oldLink := "wss://old.example.com/" + strings.Repeat("o", 24) + "/"
	newLink := "wss://panel.example.com/" + strings.Repeat("n", 24) + "/"
	a.metaMu.Lock()
	a.meta.Link = oldLink
	a.metaMu.Unlock()

	if a.applyPanelRouting(&pb.NodeSettings{PanelLinkUrl: "https://panel.example.com/short"}) || a.nextLink() != "" {
		t.Fatal("a bad link address was taken")
	}
	if a.applyPanelRouting(&pb.NodeSettings{}) || a.nextLink() != "" {
		t.Fatal("an empty link address was taken")
	}
	if !a.applyPanelRouting(&pb.NodeSettings{PanelLinkUrl: newLink}) || a.nextLink() != newLink || readMeta(t, h.dir).NextLink != newLink {
		t.Fatal("a new link address: no reconnect, or not saved")
	}
	if a.applyPanelRouting(&pb.NodeSettings{PanelLinkUrl: newLink}) {
		t.Fatal("the same new address asked for a second reconnect")
	}
	if a.applyPanelRouting(&pb.NodeSettings{PanelLinkUrl: oldLink}) || a.nextLink() != "" || readMeta(t, h.dir).NextLink != "" {
		t.Fatal("the current address did not clear the new one")
	}
	a.inTunnel.Store(true)
	if a.applyPanelRouting(&pb.NodeSettings{PanelLinkUrl: newLink}) || a.nextLink() != newLink {
		t.Fatal("a session in the tunnel: the address is saved without a reconnect")
	}
	a.inTunnel.Store(false)
}

// A new link address that fails is tried every other turn, the old way to the panel in between.
func TestNextLinkAlternatesWithTheOldTransport(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	var hits atomic.Int32
	bad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(bad.Close)
	h.a.linkHTTPClient = bad.Client()
	h.a.meta.NextLink = linkBaseOf(bad, 'b')
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.start()

	waitConnect := func() {
		select {
		case <-h.panel.connects:
		case <-time.After(5 * time.Second):
			t.Fatal("no mTLS session after the failed new address")
		}
	}
	waitConnect()
	if hits.Load() != 1 {
		t.Fatalf("new address tried %d times before the old one, want 1", hits.Load())
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "mTLS session")
	h.a.cur.Load().cancel()
	waitConnect()
	if hits.Load() != 2 {
		t.Fatalf("new address tried %d times, want 2", hits.Load())
	}
	if h.a.nextLink() == "" || h.a.linkOnly() {
		t.Fatal("a failed new address was committed")
	}
}

// The panel sends a new link address in the HelloAck: the agent leaves the mTLS session, opens one over the address and
// keeps it as its link from then on.
func TestNextLinkCommitsAfterASession(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	lp := newSessionLinkPanel(h)
	server := httptest.NewTLSServer(lp)
	t.Cleanup(server.Close)
	next := linkBaseOf(server, 'n')
	h.a.linkHTTPClient = server.Client()
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.panel.settings.Store(&pb.NodeSettings{PanelLinkUrl: next})
	h.start()

	if got := lp.hello(t); got != strings.TrimPrefix(server.URL, "https://") {
		t.Fatalf("audience %q", got)
	}
	eventually(t, func() bool { return h.a.linkOnly() && h.a.nextLink() == "" && h.a.linkURL() == next }, "the new address committed")
	if m := readMeta(t, h.dir); m.Link != next || m.NextLink != "" {
		t.Fatalf("saved link=%q next=%q", m.Link, m.NextLink)
	}
}

func TestSetPanelLink(t *testing.T) {
	dir := t.TempDir()
	link := "wss://panel.example.com/" + strings.Repeat("s", 24) + "/"
	if err := SetPanelLink(dir, link); err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("not enrolled: %v", err)
	}
	h := newHarness(t, harnessOpts{noStart: true})
	if err := SetPanelLink(h.dir, "wss://panel.example.com/x"); err == nil {
		t.Fatal("a bad address was saved")
	}
	if err := SetPanelLink(h.dir, link); err != nil {
		t.Fatal(err)
	}
	if m := readMeta(t, h.dir); m.NextLink != link || m.Panel == "" {
		t.Fatalf("saved %+v", m)
	}
}

type offlineCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newOfflineCA(t *testing.T) *offlineCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "panel ca"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &offlineCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *offlineCA) pin() string {
	sum := sha256.Sum256(ca.cert.Raw)
	return hex.EncodeToString(sum[:])
}

// issue signs a node certificate for the CSR as the panel does (ClientAuth, a URI SAN).
func (ca *offlineCA) issue(t *testing.T, csrDER []byte, nodeID string) []byte {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("mistgate://node/" + nodeID)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: nodeID}, URIs: []*url.URL{u},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestEnrollOfflineRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	ca := newOfflineCA(t)
	link := "wss://panel.example.com/" + strings.Repeat("l", 24) + "/"
	cfg := OfflineEnrollConfig{StateDir: dir, NodeID: "de1", CASHA256: ca.pin(), LinkURL: link, TunnelSNI: "t-5e2a.example.com", TunnelName: "sub.example.com"}

	if _, err := EnrollOffline(cfg); err == nil || !strings.Contains(err.Error(), "--offline-csr") {
		t.Fatalf("finish without a pending key: %v", err)
	}
	csr, err := OfflineCSR(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := OfflineCSR(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := x509.ParseCertificateRequest(csr)
	second, _ := x509.ParseCertificateRequest(again)
	if !first.PublicKey.(*ecdsa.PublicKey).Equal(second.PublicKey) {
		t.Fatal("a second --offline-csr changed the key")
	}
	certPEM := ca.issue(t, csr, "de1")

	bad := cfg
	bad.PEM = append(append([]byte{}, certPEM...), newOfflineCA(t).pem...)
	if _, err := EnrollOffline(bad); err == nil {
		t.Fatal("a CA that is not the pinned one was taken")
	}
	bad.PEM = append(append([]byte{}, ca.issue(t, mustCSR(t), "de1")...), ca.pem...)
	if _, err := EnrollOffline(bad); err == nil {
		t.Fatal("a certificate for another key was taken")
	}

	cfg.PEM = append(append([]byte{}, certPEM...), ca.pem...)
	meta, err := EnrollOffline(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if meta.NodeID != "de1" || meta.Link != link || meta.Tunnel == nil || meta.Tunnel.SNI != cfg.TunnelSNI || meta.Tunnel.Names[0] != cfg.TunnelName {
		t.Fatalf("meta %+v", meta)
	}
	id, err := loadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id.meta.Link != link || id.meta.Tunnel == nil || !id.caCert.Equal(ca.cert) {
		t.Fatalf("stored identity %+v", id.meta)
	}
	if _, err := os.Stat(filepath.Join(dir, filePendingKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending key left: %v", err)
	}
	if _, err := OfflineCSR(dir, false); err == nil {
		t.Fatal("--offline-csr over an identity")
	}
}

func mustCSR(t *testing.T) []byte {
	t.Helper()
	_, csr, err := newKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	return csr
}

// oneConnListener serves a single connection.
type oneConnListener struct {
	c    chan net.Conn
	once sync.Once
	done chan struct{}
	addr net.Addr
}

func newOneConnListener(c net.Conn) *oneConnListener {
	l := &oneConnListener{c: make(chan net.Conn, 1), done: make(chan struct{}), addr: c.LocalAddr()}
	l.c <- c
	return l
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.c:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *oneConnListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *oneConnListener) Addr() net.Addr { return l.addr }

// The subscription host's session runs inside the panel's tunnel: a link stream, the signed and sealed handshake with
// the tunnel SNI as its audience.
func TestTunnelLinkSession(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	const tunnelSNI = "t-9b3e.example.com"
	a.meta.Tunnel = &TunnelMeta{SNI: tunnelSNI, Names: []string{"sub.example.com"}}
	a.subhost.Configure(subhostConfig(a.meta.Tunnel))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wl := a.subhost.WrapSite(ln, 443, false)
	t.Cleanup(func() { _ = wl.Close() })

	// The panel's tunnel certificate: the panel CA, ServerAuth, the tunnel SNI.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: tunnelSNI}, DNSNames: []string{tunnelSNI},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, h.panel.caCert, &key.PublicKey, h.panel.caKey)
	if err != nil {
		t.Fatal(err)
	}
	lp := newSessionLinkPanel(h)
	// The panel names the role in every HelloAck: a HelloAck without it would turn the role (and the tunnel) off.
	lp.settings = &pb.NodeSettings{HostTunnel: &pb.HostTunnel{TunnelSni: tunnelSNI, ForwardNames: []string{"sub.example.com"}}}
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	go func() {
		tc, err := hosttunnel.Dial(t.Context(), raw, hosttunnel.DialConfig{
			ServerName: "sub.example.com", Secret: tunnelSNI, VerifyNode: func([][]byte) error { return nil },
			Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		})
		if err != nil {
			return
		}
		(&http2.Server{}).ServeConn(tc, &http2.ServeConnOpts{Handler: hosttunnel.ServerHandler(func(kind string, c net.Conn) bool {
			if kind != hosttunnel.KindLink {
				return false
			}
			go func() { _ = (&http.Server{Handler: lp}).Serve(newOneConnListener(c)) }()
			return true
		})})
	}()
	eventually(t, a.subhost.TunnelUp, "tunnel up")
	h.start()

	if got := lp.hello(t); got != tunnelSNI {
		t.Fatalf("audience %q, want the tunnel SNI", got)
	}
	eventually(t, func() bool { return a.cur.Load() != nil && a.inTunnel.Load() }, "session in the tunnel")
	select {
	case <-h.panel.connects:
		t.Fatal("the agent dialled the mTLS panel although the tunnel is up")
	default:
	}
}

// The subscription host's own site on a free 443 sends the same first HTTP/2 frames as every other site of the node.
func TestStandaloneSiteSpeaksHTTP2LikeEverySite(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	a.standaloneListen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
	t.Cleanup(a.closeHostRole)
	a.applyPanelRouting(&pb.NodeSettings{HostTunnel: &pb.HostTunnel{TunnelSni: "t-7c1d.example.com", ForwardNames: []string{"sub.example.com"}}})
	if a.standalone.ln == nil {
		t.Fatal("no standalone site")
	}
	conn, err := net.Dial("tcp", a.standalone.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	sitehttptest.Check(t, "subscription host", conn)
}
