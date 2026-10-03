package hysteria2

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
	"github.com/apernet/quic-go/http3"
	"golang.org/x/net/dns/dnsmessage"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// ---- helpers ----

const serverName = "node.example.com"

// Every socket of these tests is on loopback: a test binary that listens on all interfaces makes Windows ask for a
// firewall rule on each run. The engine is told through BindIP, the tests bind the same address (a "busy port" test must
// hold the very address the engine wants).
func TestMain(m *testing.M) {
	BindIP = net.IPv4(127, 0, 0, 1)
	os.Exit(m.Run())
}

func bindAddr(port int) string { return net.JoinHostPort(bindHost(), strconv.Itoa(port)) }

// loopFactory is the client socket of a test client that has no obfuscation (the library default listens on all interfaces).
type loopFactory struct{}

func (loopFactory) New(net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", &net.UDPAddr{IP: BindIP})
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", bindAddr(0))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", bindAddr(0))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func tcpEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func udpEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], from)
		}
	}()
	return pc.LocalAddr().String()
}

// fakeDNS answers A queries for zone names, NXDOMAIN otherwise.
func fakeDNS(t *testing.T, zone map[string]string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if req.Unpack(buf[:n]) != nil || len(req.Questions) != 1 {
				continue
			}
			q := req.Questions[0]
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: req.ID, Response: true, Authoritative: true, RecursionAvailable: true},
				Questions: req.Questions,
			}
			if ip, ok := zone[q.Name.String()]; ok && q.Type == dnsmessage.TypeA {
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte(net.ParseIP(ip).To4())},
				}}
			} else if !ok {
				resp.Header.RCode = dnsmessage.RCodeNameError
			}
			out, _ := resp.Pack()
			pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().String()
}

func token(name string) string { return "token-" + name + "-0123456789abcdef" }

func cred(id string, mods ...func(*plugin.UserCred)) plugin.UserCred {
	sum := sha256.Sum256([]byte(token(id)))
	c := plugin.UserCred{
		CredID: "crd_" + id, UserID: "usr_" + id, DeviceID: "dev_" + id,
		Data: json.RawMessage(fmt.Sprintf(`{"auth_sha256":%q}`, hex.EncodeToString(sum[:]))),
	}
	for _, m := range mods {
		m(&c)
	}
	return c
}

type rig struct {
	t       *testing.T
	e       engine.Engine
	udpPort int
	tcpPort int
	echo    string
	spec    plugin.InboundSpec
	pin     string
	clock   atomic.Int64 // unix seconds; 0 = real time
}

func (r *rig) now() time.Time {
	if v := r.clock.Load(); v != 0 {
		return time.Unix(v, 0)
	}
	return time.Now()
}

// newRig builds an engine (self-signed certs, loopback-friendly egress, a fake DNS that knows app.example.test)
// and a spec for one inbound. Nothing is applied yet.
func newRig(t *testing.T, settingsJSON string) *rig {
	t.Helper()
	r := &rig{t: t, echo: tcpEcho(t), udpPort: freeUDPPort(t), tcpPort: freeTCPPort(t)}
	dns := fakeDNS(t, map[string]string{"app.example.test.": "127.0.0.1"})
	resolvers := func() []string { return []string{dns} }
	direct := egress.New(resolvers, egress.AllowPrivate())
	e, err := New(engine.Env{
		Certs:  certs.New(t.TempDir()),
		Egress: func(string) (engine.Egress, error) { return direct, nil },
		DNS:    resolvers,
		Now:    r.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.e = e
	t.Cleanup(func() { e.Close(context.Background()) })
	if settingsJSON == "" {
		settingsJSON = fmt.Sprintf(`{"masquerade":{"tcp_port":%d}}`, r.tcpPort)
	}
	r.spec = plugin.InboundSpec{
		ID: "inb_1", Protocol: Protocol, ProfileID: "prf_1", Version: 1, Enabled: true,
		Listen:   plugin.Listen{Network: "udp", Port: uint16(r.udpPort)},
		TLS:      plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: serverName},
		Egress:   "direct",
		Settings: json.RawMessage(settingsJSON),
	}
	return r
}

func (r *rig) apply(creds ...plugin.UserCred) engine.ApplyReport {
	r.t.Helper()
	rep, err := r.e.Apply(context.Background(), r.spec, creds)
	if err != nil {
		r.t.Fatalf("apply: %v", err)
	}
	if rep.Cert.PinSHA256 != "" {
		r.pin = rep.Cert.PinSHA256
	}
	return rep
}

func (r *rig) collect() engine.Collected {
	r.t.Helper()
	c, err := r.e.Collect(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// dial connects a real hysteria client (pinned to the certificate the engine reported).
func (r *rig) dial(id string, factory client.ConnFactory) (client.Client, error) {
	if factory == nil {
		factory = loopFactory{}
	}
	c, _, err := client.NewClient(&client.Config{
		ConnFactory: factory,
		ServerAddr:  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.udpPort},
		Auth:        token(id),
		TLSConfig: client.TLSConfig{
			ServerName:         serverName,
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				sum := sha256.Sum256(raw[0])
				if got := hex.EncodeToString(sum[:]); got != r.pin {
					return fmt.Errorf("certificate pin %s does not match reported %s", got, r.pin)
				}
				return nil
			},
		},
	})
	if err == nil {
		r.t.Cleanup(func() { c.Close() })
	}
	return c, err
}

func (r *rig) mustDial(id string) client.Client {
	r.t.Helper()
	c, err := r.dial(id, nil)
	if err != nil {
		r.t.Fatalf("dial %s: %v", id, err)
	}
	return c
}

// echoOnce opens a stream to addr through c, sends payload and expects it back.
func echoOnce(c client.Client, addr string, payload []byte) error {
	conn, err := c.TCP(addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	return exchange(conn, payload)
}

func exchange(conn net.Conn, payload []byte) error {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	go conn.Write(payload)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return fmt.Errorf("echo mismatch")
	}
	return nil
}

// payload starts with a byte the sniffer recognises as neither HTTP nor TLS, so only 3 bytes are "put back".
func payload(n int) []byte {
	b := make([]byte, n)
	b[0], b[1], b[2] = 0x01, 0x02, 0x03
	for i := 3; i < n; i++ {
		b[i] = byte(i)
	}
	return b
}

func trafficOf(c engine.Collected, credID string) (up, down uint64) {
	for _, t := range c.Traffic {
		if t.CredID == credID {
			up, down = up+t.Up, down+t.Down
		}
	}
	return
}

func sessionsOf(c engine.Collected, credID string) int {
	n := 0
	for _, s := range c.Sessions {
		if s.CredID == credID {
			n++
		}
	}
	return n
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- tests ----

// lateCerts hides the certificate's description until ready: an ACME certificate is issued after Apply returned.
type lateCerts struct {
	engine.CertSource
	ready atomic.Bool
}

func (l *lateCerts) Acquire(ctx context.Context, id string, t plugin.TLS) (engine.Cert, error) {
	c, err := l.CertSource.Acquire(ctx, id, t)
	info := c.Info
	c.Info = func() engine.CertInfo {
		if !l.ready.Load() {
			return engine.CertInfo{}
		}
		return info()
	}
	return c, err
}

// Health says what the inbound serves right now, so a certificate that appeared (or was renewed) after Apply still reaches
// the panel; without it the node page showed "certificate -" for every ACME inbound.
func TestHealthReportsTheServedCertificate(t *testing.T) {
	r := newRig(t, "")
	en := r.e.(*eng)
	late := &lateCerts{CertSource: en.env.Certs}
	en.env.Certs = late
	rep := r.apply(cred("a"))
	if rep.Cert.PinSHA256 != "" {
		t.Fatalf("the report must not know the certificate yet: %+v", rep.Cert)
	}
	if h := r.e.Health(); len(h) != 1 || h[0].CertPinSHA256 != "" || !h[0].CertNotAfter.IsZero() {
		t.Fatalf("health before the certificate exists: %+v", h)
	}
	late.ready.Store(true)
	h := r.e.Health()
	if len(h) != 1 || len(h[0].CertPinSHA256) != 64 || !h[0].CertNotAfter.After(time.Now()) {
		t.Fatalf("health after issuance: %+v", h)
	}
}

func TestTorrentSettingToggleRestartsExistingOutboundFlows(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("a"))
	en := r.e.(*eng)

	if !en.NodeSettings(context.Background(), &agentpb.NodeSettings{TorrentBlockerEnabled: true}) {
		t.Fatal("enabling torrent blocking must request an inbound reapply")
	}
	if en.NodeSettings(context.Background(), &agentpb.NodeSettings{TorrentBlockerEnabled: true}) {
		t.Fatal("an unchanged torrent setting must not request another reapply")
	}
	rep, err := r.e.Apply(context.Background(), r.spec, []plugin.UserCred{cred("a")})
	if err != nil || !rep.Restarted {
		t.Fatalf("enabling must replace existing outbound flows: report=%+v err=%v", rep, err)
	}

	if !en.NodeSettings(context.Background(), &agentpb.NodeSettings{TorrentBlockerEnabled: false}) {
		t.Fatal("disabling torrent blocking must request an inbound reapply")
	}
	rep, err = r.e.Apply(context.Background(), r.spec, []plugin.UserCred{cred("a")})
	if err != nil || !rep.Restarted {
		t.Fatalf("disabling must replace existing outbound flows: report=%+v err=%v", rep, err)
	}
}

func TestEndToEnd(t *testing.T) {
	r := newRig(t, "")
	a, b := cred("a"), cred("b")
	rep := r.apply(a)
	if rep.Restarted || rep.CredCount != 1 || len(rep.Cert.PinSHA256) != 64 || rep.SpecHash != statehash.Spec(r.spec) {
		t.Fatalf("unexpected first report: %+v", rep)
	}

	// Wrong and unknown tokens are refused (the client sees the decoy's 404 on /auth).
	if _, err := r.dial("nobody", nil); err == nil || !strings.Contains(err.Error(), "authentication error") {
		t.Fatalf("unknown token must fail authentication, got %v", err)
	}

	// Traffic through a real client, counted per credential from the user's side.
	ca := r.mustDial("a")
	connA, err := ca.TCP(r.echo)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	if err := exchange(connA, payload(5000)); err != nil {
		t.Fatal(err)
	}
	col := r.collect()
	up, down := trafficOf(col, a.CredID)
	if down != 5000 || up < 5000-3 || up > 5000 { // the sniffer's 3 "put back" bytes bypass the traffic callback
		t.Fatalf("traffic up=%d down=%d, want ~5000/5000", up, down)
	}
	if n := sessionsOf(col, a.CredID); n != 1 {
		t.Fatalf("want 1 session for a, got %d", n)
	}
	for _, s := range col.Sessions {
		if !s.RemoteIP.IsLoopback() || s.InboundID != r.spec.ID || s.Since.IsZero() {
			t.Fatalf("bad session %+v", s)
		}
	}
	if up2, down2 := trafficOf(r.collect(), a.CredID); up2 != 0 || down2 != 0 {
		t.Fatal("Collect must reset the deltas")
	}

	// A user-only Apply: same listener, same hash, the open session and stream keep working.
	rep = r.apply(a, b)
	if rep.Restarted || rep.CredCount != 2 {
		t.Fatalf("users-only apply must not restart: %+v", rep)
	}
	if err := exchange(connA, payload(100)); err != nil {
		t.Fatalf("session must survive a users-only Apply: %v", err)
	}
	cb := r.mustDial("b") // the new user authenticates on the very next connection
	if err := echoOnce(cb, r.echo, payload(64)); err != nil {
		t.Fatal(err)
	}
	if h := r.e.Health(); len(h) != 1 || h[0].State != plugin.RunRunning || h[0].Restarts != 0 {
		t.Fatalf("health: %+v", h)
	}

	// Removing a credential kicks it at its next traffic; it cannot come back.
	r.apply(b)
	if err := exchange(connA, payload(100)); err == nil {
		t.Fatal("removed credential must be cut off at its next traffic")
	}
	if _, err := r.dial("a", nil); err == nil {
		t.Fatal("removed credential must not authenticate")
	}
	eventually(t, "a's session to disappear", func() bool { return sessionsOf(r.collect(), a.CredID) == 0 })
	// Traffic of the removed credential that was not yet reported is still delivered once.
	if err := echoOnce(cb, r.echo, payload(64)); err != nil {
		t.Fatal(err)
	}

	// Explicit Kick: b's connection dies at its next packet, the credential stays valid.
	cb2 := r.mustDial("b")
	if err := echoOnce(cb2, r.echo, payload(64)); err != nil {
		t.Fatal(err)
	}
	n, err := r.e.Kick(context.Background(), []string{b.CredID, "crd_unknown"})
	if err != nil || n != 1 {
		t.Fatalf("Kick = %d, %v; want 1 credential with open sessions", n, err)
	}
	// cb and cb2 are two connections of one credential: both get cut, in whatever order they speak first.
	e1, e2 := echoOnce(cb, r.echo, payload(64)), echoOnce(cb2, r.echo, payload(64))
	if e1 == nil || e2 == nil {
		t.Fatalf("kick must cut both open connections of the credential: %v / %v", e1, e2)
	}
	eventually(t, "kicked sessions to go", func() bool { return sessionsOf(r.collect(), b.CredID) == 0 })
	cb3 := r.mustDial("b")
	if err := echoOnce(cb3, r.echo, payload(64)); err != nil {
		t.Fatalf("a kicked credential must be able to reconnect: %v", err)
	}
	if n, _ := r.e.Kick(context.Background(), []string{"crd_unknown"}); n != 0 {
		t.Fatalf("unknown credential kicked: %d", n)
	}

	// A spec change restarts just this inbound and reports it.
	r.spec.Version = 2
	rep = r.apply(b)
	if !rep.Restarted {
		t.Fatalf("spec change must restart: %+v", rep)
	}
	if err := exchange(connA, payload(10)); err == nil {
		t.Fatal("old sessions die with a restart")
	}
	cb4 := r.mustDial("b")
	if err := echoOnce(cb4, r.echo, payload(64)); err != nil {
		t.Fatalf("restarted inbound must serve: %v", err)
	}
	if h := r.e.Health(); h[0].Restarts != 1 || h[0].State != plugin.RunRunning {
		t.Fatalf("health after restart: %+v", h)
	}

	// Observed is exactly what the panel hashes.
	want := statehash.State([]statehash.Inbound{{Spec: r.spec, Creds: []plugin.UserCred{b}}})
	if got := statehash.State(r.e.Observed()); got != want {
		t.Fatalf("observed state hash %s != desired %s", got, want)
	}

	// Remove frees the port.
	if err := r.e.Remove(context.Background(), r.spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.e.Remove(context.Background(), "unknown"); err != nil {
		t.Fatal("unknown id is not an error")
	}
	eventually(t, "udp port to be free", func() bool {
		pc, err := net.ListenPacket("udp", bindAddr(r.udpPort))
		if err == nil {
			pc.Close()
		}
		return err == nil
	})
	if len(r.e.Observed()) != 0 {
		t.Fatal("removed inbound must not be observed")
	}
}

func TestDecoyOnBothTransports(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("a"))

	get := func(rt http.RoundTripper, url string) (*http.Response, string) {
		t.Helper()
		cl := &http.Client{Transport: rt, Timeout: 10 * time.Second}
		resp, err := cl.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: true, ServerName: serverName}
	https := &http.Transport{TLSClientConfig: tlsCfg}
	h3 := &http3.Transport{TLSClientConfig: tlsCfg}
	defer https.CloseIdleConnections()
	defer h3.Close()

	tcpURL := fmt.Sprintf("https://127.0.0.1:%d", r.tcpPort)
	udpURL := fmt.Sprintf("https://127.0.0.1:%d", r.udpPort)
	page := map[string]string{}
	for name, c := range map[string]struct {
		rt  http.RoundTripper
		url string
	}{"https/tcp": {https, tcpURL}, "http3/udp": {h3, udpURL}} {
		resp, body := get(c.rt, c.url+"/")
		if resp.StatusCode != 200 || !strings.Contains(body, "Coming soon") {
			t.Fatalf("%s: decoy page expected, got %d %q", name, resp.StatusCode, body)
		}
		if resp.Header.Get("Server") != "" {
			t.Fatalf("%s: Server header must be absent", name)
		}
		nf, nfBody := get(c.rt, c.url+"/login.php")
		if nf.StatusCode != 404 {
			t.Fatalf("%s: want 404, got %d", name, nf.StatusCode)
		}
		page[name] = nfBody
		if name == "https/tcp" && !strings.Contains(resp.Header.Get("Alt-Svc"), fmt.Sprintf(`h3=":%d"`, r.udpPort)) {
			t.Fatalf("TCP decoy must advertise HTTP/3 on the UDP port, got %q", resp.Header.Get("Alt-Svc"))
		}
	}
	if page["https/tcp"] != page["http3/udp"] {
		t.Fatal("both transports must serve the identical 404")
	}
	// A failed Hysteria auth attempt looks the same as any other POST.
	resp, err := (&http.Client{Transport: h3, Timeout: 10 * time.Second}).Post(udpURL+"/auth", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("POST /auth from a stranger: want 404, got %d", resp.StatusCode)
	}
}

func TestObfsVariants(t *testing.T) {
	for _, typ := range []string{"salamander", "gecko"} {
		t.Run(typ, func(t *testing.T) {
			r := newRig(t, "")
			r.spec.Settings = json.RawMessage(fmt.Sprintf(`{"obfs":{"type":%q,"password":"open-sesame"},"masquerade":{"tcp_port":0}}`, typ))
			r.apply(cred("a"))
			if typ == "salamander" { // the idle timeout makes this check slow, once is enough
				if c, _, err := client.NewClient(&client.Config{
					ConnFactory: loopFactory{},
					ServerAddr:  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.udpPort},
					Auth:        token("a"),
					TLSConfig:   client.TLSConfig{ServerName: serverName, InsecureSkipVerify: true},
					QUICConfig:  client.QUICConfig{MaxIdleTimeout: 4 * time.Second},
				}); err == nil {
					c.Close()
					t.Fatal("plain client must not get through an obfuscated inbound")
				}
			}
			c, err := r.dial("a", obfsFactory{typ, "open-sesame"})
			if err != nil {
				t.Fatalf("obfuscated client: %v", err)
			}
			if err := echoOnce(c, r.echo, payload(2000)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type obfsFactory struct{ typ, pw string }

func (f obfsFactory) New(net.Addr) (net.PacketConn, error) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: BindIP})
	if err != nil {
		return nil, err
	}
	if f.typ == "gecko" {
		return obfs.WrapPacketConnGecko(pc, obfs.GeckoOptions{Password: []byte(f.pw)})
	}
	return obfs.WrapPacketConnSalamander(pc, []byte(f.pw))
}

func TestSniffedDomainIsResolvedByTheNode(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("a"))
	c := r.mustDial("a")
	_, echoPort, _ := net.SplitHostPort(r.echo)

	// The client asks for an unroutable IP (TEST-NET-1) but the HTTP request names app.example.test. The
	// sniffer swaps the IP for the name and the node's resolver (the fake DNS) sends it to the echo server.
	req := "GET / HTTP/1.1\r\nHost: app.example.test\r\n\r\n"
	conn, err := c.TCP("192.0.2.1:" + echoPort)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := exchange(conn, []byte(req)); err != nil {
		t.Fatalf("sniffed domain was not used: %v", err)
	}
}

func TestUDPThroughTheNode(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("a"))
	c := r.mustDial("a")
	echo := udpEcho(t)
	uc, err := c.UDP()
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	msg := []byte("udp through hysteria2")
	if err := uc.Send(msg, echo); err != nil {
		t.Fatal(err)
	}
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() { b, _, err := uc.Receive(); ch <- res{b, err} }()
	select {
	case got := <-ch:
		if got.err != nil || string(got.b) != string(msg) {
			t.Fatalf("udp echo: %q %v", got.b, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("udp echo timed out")
	}
	up, down := trafficOf(r.collect(), "crd_a")
	if up != uint64(len(msg)) || down != uint64(len(msg)) {
		t.Fatalf("udp traffic up=%d down=%d want %d each", up, down, len(msg))
	}
}

func TestRateLimit(t *testing.T) {
	r := newRig(t, "")
	// 800 kbit/s = 100 kB/s per direction; the bucket starts with a 64 KiB burst.
	r.apply(cred("slow", func(c *plugin.UserCred) { c.RateLimitBps = 800_000 }), cred("fast"))
	if !r.e.Capabilities().RateLimitPerCred {
		t.Fatal("capability must be advertised")
	}
	const size = 300_000
	timed := func(id string) time.Duration {
		c := r.mustDial(id)
		conn, err := c.TCP(r.echo)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		start := time.Now()
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		go conn.Write(payload(size))
		if _, err := io.ReadFull(conn, make([]byte, size)); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	slow, fast := timed("slow"), timed("fast")
	if slow < 1500*time.Millisecond {
		t.Fatalf("rate limit not applied: %v for %d bytes", slow, size)
	}
	if fast > slow/2 {
		t.Fatalf("unlimited credential should be much faster: slow=%v fast=%v", slow, fast)
	}
	// Changing the limit is a users-only change and takes effect on the open state.
	r.apply(cred("slow"), cred("fast"))
	if d := timed("slow"); d > slow/2 {
		t.Fatalf("limit not lifted: %v", d)
	}
}

func TestTermExpiry(t *testing.T) {
	r := newRig(t, "")
	base := time.Now().Unix()
	r.clock.Store(base)
	until := time.Unix(base+60, 0)
	r.apply(cred("a", func(c *plugin.UserCred) { c.ValidUntil = until }), cred("old", func(c *plugin.UserCred) { c.ValidUntil = time.Unix(base-1, 0) }))
	if !r.e.Capabilities().HardExpiry {
		t.Fatal("capability must be advertised")
	}
	if _, err := r.dial("old", nil); err == nil {
		t.Fatal("an already expired credential must not authenticate")
	}
	c := r.mustDial("a")
	conn, err := c.TCP(r.echo)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := exchange(conn, payload(100)); err != nil {
		t.Fatal(err)
	}
	r.clock.Store(base + 61)
	if err := exchange(conn, payload(100)); err == nil {
		t.Fatal("traffic after valid_until must be cut even without the panel")
	}
	if _, err := r.dial("a", nil); err == nil {
		t.Fatal("expired credential must not reconnect")
	}
}

func TestApplyValidationAndLifecycle(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()

	bad := map[string]func(*plugin.InboundSpec){
		"wrong protocol":    func(s *plugin.InboundSpec) { s.Protocol = "awg" },
		"no id":             func(s *plugin.InboundSpec) { s.ID = "" },
		"tcp network":       func(s *plugin.InboundSpec) { s.Listen.Network = "tcp" },
		"port 0":            func(s *plugin.InboundSpec) { s.Listen.Port = 0 },
		"settings not json": func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{`) },
		"unknown obfs": func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(`{"obfs":{"type":"xor","password":"abcdef"}}`)
		},
		"short obfs key": func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(`{"obfs":{"type":"salamander","password":"ab"}}`)
		},
		"bad masquerade": func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"masquerade":{"type":"proxy"}}`) },
		"bad bbr":        func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"bbr_profile":"turbo"}`) },
		"bad tcp port":   func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"masquerade":{"tcp_port":70000}}`) },
		"huge rate":      func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"up_mbps":99999999}`) },
		"acme_ip":        func(s *plugin.InboundSpec) { s.TLS = plugin.TLS{Mode: plugin.TLSAcmeIP, ServerName: "203.0.113.10"} },
	}
	for name, mod := range bad {
		spec := r.spec
		mod(&spec)
		if _, err := r.e.Apply(ctx, spec, nil); err == nil {
			t.Errorf("%s: Apply must fail", name)
		}
	}
	r.e.Remove(ctx, r.spec.ID)

	// Bad credentials are rejected as a whole and change nothing.
	for name, c := range map[string]plugin.UserCred{
		"no data":    {CredID: "crd_x"},
		"short hash": {CredID: "crd_x", Data: json.RawMessage(`{"auth_sha256":"abcd"}`)},
		"not hex":    {CredID: "crd_x", Data: json.RawMessage(`{"auth_sha256":"` + strings.Repeat("zz", 32) + `"}`)},
		"no id":      func() plugin.UserCred { c := cred("x"); c.CredID = ""; return c }(),
	} {
		if _, err := r.e.Apply(ctx, r.spec, []plugin.UserCred{c}); err == nil {
			t.Errorf("credential %s must be rejected", name)
		}
	}
	dup := cred("d")
	dup2 := cred("d")
	dup2.CredID = "crd_d2"
	if _, err := r.e.Apply(ctx, r.spec, []plugin.UserCred{dup, dup2}); err == nil {
		t.Error("two credentials with one token must be rejected")
	}
	if _, err := r.e.Apply(ctx, r.spec, []plugin.UserCred{dup, dup}); err == nil {
		t.Error("duplicate credential id must be rejected")
	}
	r.e.Remove(ctx, r.spec.ID)

	// A busy UDP port fails that inbound only; once the port is free the same Apply succeeds (retry).
	hold, err := net.ListenPacket("udp", bindAddr(r.udpPort))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Apply(ctx, r.spec, []plugin.UserCred{cred("a")}); err == nil {
		t.Fatal("busy port must fail the inbound")
	}
	if h := r.e.Health(); len(h) != 1 || h[0].State != plugin.RunFailed || h[0].Detail == "" {
		t.Fatalf("health must show the failure: %+v", h)
	}
	hold.Close()
	r.pin = ""
	rep, err := r.e.Apply(ctx, r.spec, []plugin.UserCred{cred("a")})
	if err != nil {
		t.Fatalf("retry after the port was freed: %v", err)
	}
	r.pin = rep.Cert.PinSHA256
	if err := echoOnce(r.mustDial("a"), r.echo, payload(32)); err != nil {
		t.Fatal(err)
	}

	// Disabled: nothing listens, but the engine still reports the spec so the state hash matches.
	off := r.spec
	off.Enabled = false
	rep, err = r.e.Apply(ctx, off, []plugin.UserCred{cred("a")})
	if err != nil || !rep.Restarted {
		t.Fatalf("disabling a running inbound: %+v %v", rep, err)
	}
	if h := r.e.Health(); h[0].State != plugin.RunStopped {
		t.Fatalf("health: %+v", h)
	}
	if pc, err := net.ListenPacket("udp", bindAddr(r.udpPort)); err != nil {
		t.Fatalf("a disabled inbound must release its port: %v", err)
	} else {
		pc.Close()
	}
	want := statehash.State([]statehash.Inbound{{Spec: off, Creds: []plugin.UserCred{cred("a")}}})
	if got := statehash.State(r.e.Observed()); got != want {
		t.Fatal("a disabled inbound must still be observed with its spec")
	}
	// Re-enabling starts it again.
	if _, err := r.e.Apply(ctx, r.spec, []plugin.UserCred{cred("a")}); err != nil {
		t.Fatal(err)
	}

	// Close stops everything and further Apply fails.
	if err := r.e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Apply(ctx, r.spec, nil); err == nil {
		t.Fatal("Apply after Close must fail")
	}
}

func TestBusyTCPPortIsNotFatal(t *testing.T) {
	r := newRig(t, "")
	ln, err := net.Listen("tcp", bindAddr(r.tcpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	r.apply(cred("a"))
	h := r.e.Health()
	if len(h) != 1 || h[0].State != plugin.RunRunning || !strings.HasPrefix(h[0].Detail, "masq_tcp:") {
		t.Fatalf("QUIC must run and the decoy problem must be visible: %+v", h)
	}
	if err := echoOnce(r.mustDial("a"), r.echo, payload(32)); err != nil {
		t.Fatal(err)
	}
}

func TestVersionAndFactory(t *testing.T) {
	e, err := Factory(engine.Env{Certs: certs.New(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	if e.Protocol() != "hysteria2" || !strings.HasPrefix(e.Version(), "hysteria core v2.") {
		t.Fatalf("%q %q", e.Protocol(), e.Version())
	}
	if _, err := New(engine.Env{}); err == nil {
		t.Fatal("Env.Certs is required")
	}
}
