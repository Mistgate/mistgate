package bridge

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The pieces of a subscription host on loopback (design/adr/0018-panel-behind-nat.md), without the agent and the panel around
// them: the host's front on 443 with its site (the default route) and the subscription name's route, a host Entry that takes
// the panel only, and the panel as an exit whose target is a TLS listener in its own process.

const (
	loopSiteName = "msk1.example"
	loopSubName  = "sub.example.test"
	loopPath     = "/sub-host-bridge-path"
)

// chanListener is a net.Listener fed by its owner (the panel's listener of the host's connections).
type chanListener struct {
	ch   chan net.Conn
	once sync.Once
	done chan struct{}
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *chanListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// fromConn is a connection whose RemoteAddr is the person's, as the host told the panel.
type fromConn struct {
	net.Conn
	from net.Addr
}

func (c fromConn) RemoteAddr() net.Addr { return c.from }

type subHostLoop struct {
	pki      testPKI
	front    *front.Front
	addr     string
	host     *Entry
	panel    *Dialer
	diverted chan struct{}
}

func newSubHostLoop(t *testing.T) *subHostLoop {
	t.Helper()
	pki := newTestPKI(t)
	now := time.Now()
	l := &subHostLoop{pki: pki, diverted: make(chan struct{}, 16)}

	// The host: a front on loopback, its site, and the subscription name's route.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.addr = ln.Addr().String()
	l.front = front.New(ln, nil)
	l.front.SetRoutes([]front.Route{{ID: "inb_site", Names: []string{loopSiteName}}, {ID: plugin.SubHostRouteID, Names: []string{loopSubName}}}, "inb_site")
	l.host = NewEntry()
	if err := l.host.SetServe(ServeConfig{EntryNodeID: "nod_host", Path: loopPath, ExitNodeIDs: []string{PanelPeerID}, Roots: pki.roots,
		Peer: PeerPanel, PassClient: true}); err != nil {
		t.Fatal(err)
	}
	site := &http.Server{Handler: l.host.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ordinary site")
	})), TLSConfig: &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}}
	if err := sitehttp.ConfigureSiteHTTP2(site); err != nil {
		t.Fatal(err)
	}
	go func() { _ = site.Serve(tls.NewListener(l.front.Listener("inb_site"), site.TLSConfig)) }()
	intake := l.front.Listener(plugin.SubHostRouteID)
	intakeCtx, stopIntake := context.WithCancel(context.Background())
	var intakeWG sync.WaitGroup
	intakeWG.Add(1)
	go func() {
		defer intakeWG.Done()
		for {
			conn, err := intake.Accept()
			if err != nil {
				return
			}
			intakeWG.Add(1)
			go func() {
				defer intakeWG.Done()
				var addr netip.AddrPort
				if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
					addr = tcp.AddrPort()
				}
				err := ErrNoPair
				if l.host.Connected(PanelPeerID) {
					err = l.host.Attach(intakeCtx, PanelPeerID, Client{Conn: conn, RelayID: "sub", Addr: addr})
				}
				if err != nil { // never a reset: the person gets the node's site
					l.diverted <- struct{}{}
					if !l.front.Divert(conn) {
						_ = conn.Close()
					}
				}
			}()
		}
	}()

	// The panel: its short certificate, and its listener of the subscription name.
	panelKey, panelDER := issueURILeaf(t, pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageClientAuth, now.Add(-time.Hour), now.Add(time.Hour))
	subKey, subDER, subLeaf := issueTestLeaf(t, pki.root, pki.rootKey, loopSubName, now.Add(-time.Hour), now.Add(time.Hour), x509.ExtKeyUsageServerAuth, []string{loopSubName})
	panelLn := newChanListener()
	panelSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "panel saw "+r.RemoteAddr)
	})}
	go func() {
		_ = panelSrv.Serve(tls.NewListener(panelLn, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{subDER}, PrivateKey: subKey, Leaf: subLeaf}},
			MinVersion: tls.VersionTLS12}))
	}()
	l.panel = NewDialer(func() DialIdentity {
		return DialIdentity{NodeID: PanelPeerID, CertificateDER: panelDER, PrivateKey: panelKey, Panel: true}
	})
	l.panel.SetLocalDial(func(ctx context.Context, target plugin.BridgeTarget, client netip.AddrPort) (net.Conn, error) {
		mine, theirs := net.Pipe()
		var from net.Addr = &net.TCPAddr{}
		if client.IsValid() {
			from = net.TCPAddrFromAddrPort(client)
		}
		select {
		case panelLn.ch <- fromConn{Conn: theirs, from: from}:
			return mine, nil
		case <-ctx.Done():
			_ = mine.Close()
			_ = theirs.Close()
			return nil, ctx.Err()
		}
	})
	host, portText, _ := net.SplitHostPort(l.addr)
	port, _ := strconv.Atoi(portText)
	l.panel.Sync(context.Background(), []DialSpec{{Dial: plugin.BridgeDial{EntryNodeID: "nod_host", Host: host, Port: uint16(port), SNI: loopSiteName,
		PinSHA256: pki.pin, Path: loopPath, Pool: 2, Targets: []plugin.BridgeTarget{
			{RelayID: "sub", Network: LocalTargetNetwork}, {RelayID: "link", Network: LocalTargetNetwork}}}}})
	t.Cleanup(func() {
		l.panel.Close()
		stopIntake()
		_ = l.front.Close()
		intakeWG.Wait()
		l.host.Close()
		_ = site.Close()
		_ = panelSrv.Close()
		_ = panelLn.Close()
	})
	return l
}

// visit opens a TLS connection to the host's 443 for name, as a person would, and returns the certificate it was shown and
// the body of GET / (empty when the certificate is not for name).
func (l *subHostLoop) visit(t *testing.T, name string) (shown string, body string, local string) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", l.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	conn := tls.Client(raw, &tls.Config{ServerName: name, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err := conn.Handshake(); err != nil {
		t.Fatalf("handshake for %s: %v (a reset tells a prober the name is special)", name, err)
	}
	shown = conn.ConnectionState().PeerCertificates[0].DNSNames[0]
	if shown != name {
		return shown, "", raw.LocalAddr().String()
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+name+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(conn)
	if i := bytes.Index(reply, []byte("\r\n\r\n")); i >= 0 {
		reply = reply[i+4:]
	}
	return shown, string(reply), raw.LocalAddr().String()
}

func TestSubHostLoopback(t *testing.T) {
	l := newSubHostLoop(t)
	waitFor(t, 10*time.Second, func() bool {
		s := l.panel.States()
		return len(s) == 1 && s[0].Conns == 2 && l.host.Connected(PanelPeerID)
	}, "the panel did not connect to the host")

	// The subscription name goes to the panel. A loopback visitor's address is not passed on (only a global unicast one is:
	// TestLocalDialAndClientAddress), so the panel sees none.
	shown, body, _ := l.visit(t, loopSubName)
	if shown != loopSubName || body != "panel saw :0" {
		t.Fatalf("subscription name: shown %q, body %q, want the panel to see no address", shown, body)
	}
	// Any other name is the node's site, untouched.
	if shown, _, _ := l.visit(t, loopSiteName); shown != loopSiteName {
		t.Fatalf("site name: shown %q", shown)
	}

	// A prober on the bridge path without the panel's proof, with an agent's proof, or with a proof made for another
	// connection, gets the site.
	probe := func(sign func(exporter []byte) Proof) string {
		t.Helper()
		conn, err := tls.Dial("tcp", l.addr, &tls.Config{ServerName: loopSiteName, RootCAs: l.pki.roots, NextProtos: []string{"h2"}})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		state := conn.ConnectionState()
		exporter, err := state.ExportKeyingMaterial("mistgate-bridge/1", nil, 32)
		if err != nil {
			t.Fatal(err)
		}
		cc, err := (&http2.Transport{}).NewClientConn(conn)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, "https://"+loopSiteName+loopPath, nil)
		req.Header.Set(ProtocolHeader, protocolVersion)
		req.Header.Set(StreamHeader, string(pairUp))
		req.Header.Set(PairIDHeader, "0123456789abcdef0123456789abcdef")
		if p := sign(exporter); len(p.CertificateDER) > 0 {
			p.setHeaders(req.Header)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := cc.RoundTrip(req.WithContext(ctx))
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	now := time.Now()
	panelKey, panelDER := issueURILeaf(t, l.pki.root, l.pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageClientAuth, now.Add(-time.Hour), now.Add(time.Hour))
	for name, sign := range map[string]func([]byte) Proof{
		"no proof": func([]byte) Proof { return Proof{} },
		"an agent's proof": func(exporter []byte) Proof {
			p, _ := MakeProof("nod_host", exporter, l.pki.exitDER, l.pki.exitKey)
			return p
		},
		"a proof of another connection": func([]byte) Proof {
			other := make([]byte, 32)
			_, _ = rand.Read(other)
			p, _ := MakePanelProof("nod_host", other, panelDER, panelKey)
			return p
		},
	} {
		if got := probe(sign); got != "ordinary site" {
			t.Errorf("%s: %q, want the site", name, got)
		}
	}

	// The panel goes away: the subscription name gets the node's site, never a reset.
	l.panel.Close()
	waitFor(t, 10*time.Second, func() bool { return !l.host.Connected(PanelPeerID) }, "the host did not notice the panel left")
	if shown, _, _ := l.visit(t, loopSubName); shown != loopSiteName {
		t.Fatalf("panel away: shown %q, want the site's certificate", shown)
	}
	select {
	case <-l.diverted:
	default:
		t.Fatal("the intake did not divert")
	}
	if s := l.front.Stats(); s.Diverted == 0 {
		t.Fatalf("front stats %+v", s)
	}
}
