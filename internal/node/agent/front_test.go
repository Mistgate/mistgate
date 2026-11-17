package agent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hysteria2"
	"github.com/mistgate/mistgate/internal/node/mtproto"
	"github.com/mistgate/mistgate/internal/node/trusttunnel"
	"github.com/mistgate/mistgate/internal/node/vless"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The agent with the real engines of the three protocols that take a route of a shared port, on loopback. A service
// answers a TLS client with a certificate of its own name, so the name in the certificate says who answered.

// shareCerts issues a self-signed certificate for the name of the inbound, whatever mode the spec asks for.
type shareCerts struct{}

func (shareCerts) Acquire(_ context.Context, id string, t plugin.TLS) (engine.Cert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return engine.Cert{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: t.ServerName}, DNSNames: []string{t.ServerName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return engine.Cert{}, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return engine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil },
		Info:           func() engine.CertInfo { return engine.CertInfo{PinSHA256: "pin-" + id, NotAfter: tpl.NotAfter} },
	}, nil
}

func (shareCerts) Release(string) {}

type shareRig struct {
	t    *testing.T
	h    *harness
	port uint16
	rev  uint64
}

func newShareRig(t *testing.T) *shareRig {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	direct := egress.New(func() []string { return nil }, egress.AllowPrivate())
	h := newHarness(t, harnessOpts{
		noStart: true,
		extra: map[string]engine.Factory{
			mtproto.Protocol: mtproto.Factory, trusttunnel.Protocol: trusttunnel.Factory, "vless": vless.Factory,
			"hysteria2": hysteria2.Factory,
		},
		cfg: func(c *Config) {
			c.Certs = shareCerts{}
			c.Egress = func(string) (engine.Egress, error) { return direct, nil }
		},
	})
	t.Cleanup(h.a.closeEngines)
	return &shareRig{t: t, h: h, port: port}
}

func (r *shareRig) spec(id, protocol, name, settings string, creds ...*pb.Credential) *pb.InboundState {
	return &pb.InboundState{InboundId: id, CredsReplace: true, Creds: creds, Spec: &pb.InboundSpec{
		InboundId: id, Protocol: protocol, ProfileId: "prf", SpecVersion: 1, Enabled: true,
		Listen: &pb.Listen{Network: "tcp", Port: uint32(r.port)},
		Tls:    &pb.Tls{Mode: pb.TlsMode_TLS_MODE_ACME_DOMAIN, ServerName: name}, Egress: "direct", SettingsJson: settings,
	}}
}

// mt is an MTProto inbound whose domain, and decoy certificate, is name.
func (r *shareRig) mt(id, name string) *pb.InboundState {
	return r.spec(id, "mtproto", name, `{"domains":["`+name+`"]}`,
		&pb.Credential{CredId: "crd_" + id, UserId: "alice", DeviceId: "dev", DataJson: `{"key":"00112233445566778899aabbccddeeff"}`})
}

func (r *shareRig) tt(id, name string) *pb.InboundState {
	return r.spec(id, trusttunnel.Protocol, name, `{}`)
}

func (r *shareRig) wsTLS(id, name string) *pb.InboundState {
	return r.spec(id, "vless", name, `{"transport":"ws","security":"tls","ws":{"path":"/w"}}`,
		&pb.Credential{CredId: "crd_" + id, UserId: "alice", DeviceId: "dev", DataJson: `{"id":"66ad4540-b58c-4ad2-9926-ea63445a9b57"}`})
}

// apply sends a full desired state and returns the results by inbound id.
func (r *shareRig) apply(inbounds ...*pb.InboundState) map[string]*pb.InboundResult {
	r.t.Helper()
	r.rev++
	res := r.h.a.applyDesired(context.Background(), fullState(r.rev, inbounds...))
	if res == nil {
		r.t.Fatal("the desired state was ignored")
	}
	return byInbound(res.Inbounds)
}

// sweep is what the agent's sweep does: reconcile the model it holds.
func (r *shareRig) sweep() map[string]*pb.InboundResult {
	r.t.Helper()
	return byInbound(r.h.a.reconcile(context.Background(), r.h.a.snapshotModel(), nil))
}

func byInbound(results []*pb.InboundResult) map[string]*pb.InboundResult {
	out := map[string]*pb.InboundResult{}
	for _, res := range results {
		out[res.InboundId] = res
	}
	return out
}

func (r *shareRig) addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(r.port))) }

// cn is the name in the certificate the port answers a TLS client with that asks for sni.
func (r *shareRig) cn(sni string) string {
	r.t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", r.addr(),
		&tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err != nil {
		r.t.Fatalf("TLS to the port as %q: %v", sni, err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
}

// keepConn is a keep-alive HTTP/1.1 connection to the decoy site behind sni, which must stay usable.
type keepConn struct {
	t    *testing.T
	conn *tls.Conn
	rd   *bufio.Reader
}

func (r *shareRig) open(sni string) *keepConn {
	r.t.Helper()
	conn, err := tls.Dial("tcp", r.addr(), &tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err != nil {
		r.t.Fatalf("TLS to the port as %q: %v", sni, err)
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return &keepConn{t: r.t, conn: conn, rd: bufio.NewReader(conn)}
}

// get makes one request on the connection; it fails when the connection is gone.
func (s *keepConn) get() {
	s.t.Helper()
	_ = s.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := s.conn.Write([]byte("GET / HTTP/1.1\r\nHost: site\r\n\r\n")); err != nil {
		s.t.Fatalf("the session is gone: %v", err)
	}
	resp, err := http.ReadResponse(s.rd, nil)
	if err != nil {
		s.t.Fatalf("the session is gone: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func (r *shareRig) front() *sharedPort {
	r.h.a.frontMu.Lock()
	defer r.h.a.frontMu.Unlock()
	return r.h.a.shared[r.port]
}

func (r *shareRig) wantRunning(results map[string]*pb.InboundResult, ids ...string) {
	r.t.Helper()
	for _, id := range ids {
		if res := results[id]; res == nil || res.Error != "" || res.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
			r.t.Fatalf("inbound %s: %+v, want running", id, res)
		}
	}
}

func (r *shareRig) wantFailed(results map[string]*pb.InboundResult, id, errPart string) {
	r.t.Helper()
	if res := results[id]; res == nil || res.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || !strings.Contains(res.Error, errPart) {
		r.t.Fatalf("inbound %s: %+v, want failed with %q", id, res, errPart)
	}
}

func (r *shareRig) wantNoFront() {
	r.t.Helper()
	if sp := r.front(); sp != nil {
		r.t.Fatalf("the port is shared: %+v", sp)
	}
}

// One service alone on the port listens itself; a second makes the port shared, a third joins; when they go the survivor
// listens itself again.
func TestSharedPortOneToManyAndBack(t *testing.T) {
	r := newShareRig(t)

	res := r.apply(r.mt("inb_a", "a.example.com"))
	r.wantRunning(res, "inb_a")
	r.wantNoFront()
	if got := r.cn("a.example.com"); got != "a.example.com" {
		t.Fatalf("alone, a's name got %q", got)
	}
	if got := r.cn("other.example.com"); got != "a.example.com" {
		t.Fatalf("alone, another name got %q", got)
	}

	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"), r.wsTLS("inb_c", "c.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_c")
	if sp := r.front(); sp == nil || sp.f == nil || sp.def != "inb_a" {
		t.Fatalf("the port is not shared with a as the default: %+v", sp)
	}
	for sni, want := range map[string]string{
		"a.example.com": "a.example.com", "b.example.com": "b.example.com", "c.example.com": "c.example.com",
		"B.Example.com.": "b.example.com", "other.example.com": "a.example.com", "": "a.example.com",
	} {
		if got := r.cn(sni); got != want {
			t.Fatalf("shared, %q got the certificate of %q, want %q", sni, got, want)
		}
	}
	batch := r.h.a.buildBatch(context.Background(), time.Now().Add(-time.Second), time.Now())
	if len(batch.SharedPorts) != 1 {
		t.Fatalf("shared_ports = %v", batch.SharedPorts)
	}
	row := batch.SharedPorts[0]
	delivered := map[string]uint64{}
	for _, route := range row.Routes {
		delivered[route.InboundId] = route.Delivered
	}
	if row.Port != uint32(r.port) || row.DefaultInboundId != "inb_a" || row.BindError != "" || row.Accepted != 6 ||
		delivered["inb_a"] != 3 || delivered["inb_b"] != 2 || delivered["inb_c"] != 1 || row.DefaultUnknownName != 1 || row.DefaultNoSni != 1 ||
		len(row.UnknownNames) != 1 || row.UnknownNames[0].Name != "other.example.com" {
		t.Fatalf("shared_ports row = %v", row)
	}

	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	r.wantRunning(res, "inb_a", "inb_b")
	if got := r.cn("b.example.com"); got != "b.example.com" {
		t.Fatalf("c gone, b got %q", got)
	}
	if got := r.cn("c.example.com"); got != "a.example.com" {
		t.Fatalf("c gone, its name got %q, want the default's", got)
	}

	res = r.apply(r.mt("inb_a", "a.example.com"))
	r.wantRunning(res, "inb_a")
	r.wantNoFront()
	if batch := r.h.a.buildBatch(context.Background(), time.Now().Add(-time.Second), time.Now()); len(batch.SharedPorts) != 0 {
		t.Fatalf("shared_ports = %v after the port stopped being shared", batch.SharedPorts)
	}
	if got := r.cn("b.example.com"); got != "a.example.com" {
		t.Fatalf("alone again, b's name got %q", got)
	}
	res = r.apply()
	if len(res) != 0 {
		t.Fatalf("results of an empty state: %v", res)
	}
	if c, err := net.DialTimeout("tcp", r.addr(), 300*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("the port still listens after every inbound is gone")
	}
}

// A service that is added, changed or removed restarts only itself: another service's open connection keeps working, and
// the others' engines are not applied again.
func TestSharedPortChangeOfOneRouteLeavesTheOthersAlone(t *testing.T) {
	r := newShareRig(t)
	res := r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"), r.mt("inb_c", "c.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_c")
	keep := r.open("a.example.com")
	keep.get()

	// b gets another name: only b restarts, and the old name goes to the default route
	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b2.example.com"), r.mt("inb_c", "c.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_c")
	if res["inb_a"].Restarted || res["inb_c"].Restarted || !res["inb_b"].Restarted {
		t.Fatalf("restarted: a %v, b %v, c %v; want only b", res["inb_a"].Restarted, res["inb_b"].Restarted, res["inb_c"].Restarted)
	}
	if got := r.cn("b2.example.com"); got != "b2.example.com" {
		t.Fatalf("the new name got %q", got)
	}
	if got := r.cn("b.example.com"); got != "a.example.com" {
		t.Fatalf("the old name got %q, want the default's", got)
	}
	keep.get()

	// c goes away, d comes
	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b2.example.com"), r.wsTLS("inb_d", "d.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_d")
	if got := r.cn("d.example.com"); got != "d.example.com" {
		t.Fatalf("the new service got %q", got)
	}
	if got := r.cn("c.example.com"); got != "a.example.com" {
		t.Fatalf("the removed service's name got %q", got)
	}
	keep.get()

	// the default route itself changes spec: the others stay up
	other := r.open("b2.example.com")
	other.get()
	res = r.apply(r.mt("inb_a", "a2.example.com"), r.tt("inb_b", "b2.example.com"), r.wsTLS("inb_d", "d.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_d")
	if got := r.cn("a2.example.com"); got != "a2.example.com" {
		t.Fatalf("the default's new name got %q", got)
	}
	other.get()

	// the restart command (the doctor's fix for a failed bind) starts one service again behind the front it keeps
	if cmd := r.h.a.restartInbound(context.Background(), &pb.RestartInbound{InboundId: "inb_d"}); !cmd.Ok {
		t.Fatalf("restart: %+v", cmd)
	}
	if got := r.cn("d.example.com"); got != "d.example.com" {
		t.Fatalf("the restarted service got %q", got)
	}
	other.get()
}

// A port another process holds fails every service of it with the bind error; the sweep that finds the port free brings the
// front up and the services start behind it.
func TestSharedPortBusyThenFree(t *testing.T) {
	r := newShareRig(t)
	foreign, err := net.Listen("tcp", r.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()

	res := r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	for _, id := range []string{"inb_a", "inb_b"} {
		if got := res[id]; got == nil || got.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || !strings.Contains(got.Error, "listen tcp") {
			t.Fatalf("inbound %s: %+v, want the bind error", id, got)
		}
	}
	if sp := r.front(); sp == nil || sp.f != nil || sp.err == "" {
		t.Fatalf("front state while the port is taken: %+v", sp)
	}
	batch := r.h.a.buildBatch(context.Background(), time.Now().Add(-time.Second), time.Now())
	if len(batch.SharedPorts) != 1 || batch.SharedPorts[0].BindError == "" || batch.SharedPorts[0].Accepted != 0 {
		t.Fatalf("shared_ports while the port is taken = %v", batch.SharedPorts)
	}
	res = r.sweep() // still taken: the same error, no new start
	r.wantFailed(res, "inb_a", "listen tcp")
	if got, want := r.h.a.observedHash(r.h.a.snapshotModel()), r.h.a.snapshotModel().hash(); got != want {
		t.Fatalf("the state hash the agent reports while the port is taken is %s, the panel's is %s", got, want)
	}

	_ = foreign.Close()
	res = r.sweep()
	r.wantRunning(res, "inb_a", "inb_b")
	if sp := r.front(); sp == nil || sp.f == nil || sp.err != "" {
		t.Fatalf("front state after the port was freed: %+v", sp)
	}
	if got := r.cn("b.example.com"); got != "b.example.com" {
		t.Fatalf("after the port was freed b's name got %q", got)
	}
}

// An agent that starts with the model it saved puts the front up before the services.
func TestSharedPortAfterAgentRestart(t *testing.T) {
	r := newShareRig(t)
	res := r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	r.wantRunning(res, "inb_a", "inb_b")
	r.h.a.closeEngines() // the process ends: the port is free again
	if c, err := net.DialTimeout("tcp", r.addr(), 300*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("the port still listens after the agent closed")
	}

	again := r.h.newAgent()
	t.Cleanup(again.closeEngines)
	again.restore(context.Background())
	r.h.a = again
	if sp := r.front(); sp == nil || sp.f == nil {
		t.Fatalf("no front after the restore: %+v", sp)
	}
	for sni, want := range map[string]string{"a.example.com": "a.example.com", "b.example.com": "b.example.com", "x.example.com": "a.example.com"} {
		if got := r.cn(sni); got != want {
			t.Fatalf("after the restore %q got %q, want %q", sni, got, want)
		}
	}
}

// A name two services of a port ask for goes to the lower id; the other does not run and says why, and gets its place back
// when its spec is fixed.
func TestSharedPortNameTakenAfterASpecChange(t *testing.T) {
	r := newShareRig(t)
	res := r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"), r.tt("inb_c", "c.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_c")
	keep := r.open("a.example.com")
	keep.get()

	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"), r.tt("inb_c", "A.example.com"))
	r.wantRunning(res, "inb_a", "inb_b")
	r.wantFailed(res, "inb_c", "sni_shadowed")
	if got := r.cn("a.example.com"); got != "a.example.com" {
		t.Fatalf("the holder of the name got %q", got)
	}
	if got := r.cn("c.example.com"); got != "a.example.com" {
		t.Fatalf("the shadowed service's old name got %q, want the default's", got)
	}
	if res["inb_a"].Restarted || res["inb_b"].Restarted {
		t.Fatal("the others were restarted")
	}
	keep.get()
	if got, want := r.h.a.observedHash(r.h.a.snapshotModel()), r.h.a.snapshotModel().hash(); got != want {
		t.Fatalf("the state hash the agent reports with a shadowed service is %s, the panel's is %s", got, want)
	}
	if res = r.sweep(); res["inb_c"].State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED || !strings.Contains(res["inb_c"].Error, "sni_shadowed") {
		t.Fatalf("a sweep changed the shadowed service: %+v", res["inb_c"])
	}

	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"), r.tt("inb_c", "c.example.com"))
	r.wantRunning(res, "inb_a", "inb_b", "inb_c")
	if got := r.cn("c.example.com"); got != "c.example.com" {
		t.Fatalf("the fixed service got %q", got)
	}
	keep.get()

	// a lower id that takes a name another service runs on wins it; the one that ran on it stops
	res = r.apply(r.mt("inb_a", "c.example.com"), r.tt("inb_b", "b.example.com"), r.tt("inb_c", "c.example.com"))
	r.wantFailed(res, "inb_c", "sni_shadowed")
	if got := r.cn("c.example.com"); got != "c.example.com" {
		t.Fatalf("the new holder got %q", got)
	}
}

// A Hysteria2 site gives its TCP port to a service on it and to a front alike, before the front binds it, and takes it back
// when the last service goes.
func TestSharedPortTakenFromAHysteria2Site(t *testing.T) {
	r := newShareRig(t)
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	_ = udp.Close()
	hy := &pb.InboundState{InboundId: "inb_h", CredsReplace: true,
		Creds: []*pb.Credential{{CredId: "crd_h", UserId: "alice", DeviceId: "dev", DataJson: `{"auth_sha256":"` + strings.Repeat("ab", 32) + `"}`}},
		Spec: &pb.InboundSpec{InboundId: "inb_h", Protocol: "hysteria2", ProfileId: "prf", SpecVersion: 1, Enabled: true,
			Listen: &pb.Listen{Network: "udp", Port: uint32(udpPort)}, Tls: &pb.Tls{Mode: pb.TlsMode_TLS_MODE_ACME_DOMAIN, ServerName: "h.example.com"},
			Egress: "direct", SettingsJson: `{"masquerade":{"tcp_port":` + strconv.Itoa(int(r.port)) + `}}`}}

	res := r.apply(hy)
	r.wantRunning(res, "inb_h")
	if got := r.cn("x.example.com"); got != "h.example.com" {
		t.Fatalf("the Hysteria2 site got %q", got)
	}
	res = r.apply(hy, r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	r.wantRunning(res, "inb_h", "inb_a", "inb_b")
	for sni, want := range map[string]string{"a.example.com": "a.example.com", "b.example.com": "b.example.com", "h.example.com": "a.example.com"} {
		if got := r.cn(sni); got != want {
			t.Fatalf("with Hysteria2 beside them, %q got %q, want %q", sni, got, want)
		}
	}
	res = r.apply(hy)
	r.wantRunning(res, "inb_h")
	r.wantNoFront()
	if got := r.cn("a.example.com"); got != "h.example.com" {
		t.Fatalf("the Hysteria2 site did not come back: %q", got)
	}
}

// With two services a lost name leaves one: the port is not shared any more, the lower id listens itself, the other fails
// with the reason, and the front comes back when the name is fixed.
func TestSharedPortNameTakenLeavesOneService(t *testing.T) {
	r := newShareRig(t)
	res := r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	r.wantRunning(res, "inb_a", "inb_b")

	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "a.example.com"))
	r.wantRunning(res, "inb_a")
	r.wantFailed(res, "inb_b", "sni_shadowed")
	r.wantNoFront()
	if got := r.cn("whatever.example.com"); got != "a.example.com" {
		t.Fatalf("the one that runs got %q", got)
	}

	res = r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	r.wantRunning(res, "inb_a", "inb_b")
	if got := r.cn("b.example.com"); got != "b.example.com" {
		t.Fatalf("after the fix b got %q", got)
	}
}

// Two services where none serves the node's own site keep the old rule: the lower id listens, the other cannot bind.
func TestSharedPortWithoutASiteKeepsTheOldRule(t *testing.T) {
	r := newShareRig(t)
	site := func(id, name string) *pb.InboundState {
		return r.spec(id, "mtproto", name, `{"domains":["`+name+`"],"fallback":"site"}`,
			&pb.Credential{CredId: "crd_" + id, UserId: "alice", DeviceId: "dev", DataJson: `{"key":"00112233445566778899aabbccddeeff"}`})
	}
	res := r.apply(site("inb_a", "a.example.com"), site("inb_b", "b.example.com"))
	r.wantNoFront()
	r.wantRunning(res, "inb_a")
	r.wantFailed(res, "inb_b", "listen tcp")
}

// The exit of a bridge calls 127.0.0.1:port with what a person sent, and the front chooses the service by the name in it: the
// target counts as local while any service of the port runs, and not when none does.
func TestSharedPortAsBridgeTarget(t *testing.T) {
	r := newShareRig(t)
	res := r.apply(r.mt("inb_a", "a.example.com"), r.tt("inb_b", "b.example.com"))
	r.wantRunning(res, "inb_a", "inb_b")
	target := plugin.BridgeTarget{RelayID: "rly_1", Network: "tcp", Port: r.port}
	if !bridgeTargetIsLocal(r.h.a.snapshotModel(), res, target) {
		t.Fatal("a shared port of running services is not a local target")
	}
	// what the exit does for a person's connection: a plain TCP connection to the loopback port
	raw, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(int(r.port)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn := tls.Client(raw, &tls.Config{ServerName: "b.example.com", InsecureSkipVerify: true})
	if err := conn.Handshake(); err != nil {
		t.Fatalf("handshake through the front: %v", err)
	}
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "b.example.com" {
		t.Fatalf("the person's connection reached %q", cn)
	}
	_ = conn.Close()

	failed := map[string]*pb.InboundResult{
		"inb_a": {InboundId: "inb_a", State: pb.InboundRunState_INBOUND_RUN_STATE_FAILED, Error: "x"},
		"inb_b": {InboundId: "inb_b", State: pb.InboundRunState_INBOUND_RUN_STATE_FAILED, Error: "x"},
	}
	if bridgeTargetIsLocal(r.h.a.snapshotModel(), failed, target) {
		t.Fatal("a shared port with no running service is a local target")
	}
	failed["inb_b"] = res["inb_b"]
	if !bridgeTargetIsLocal(r.h.a.snapshotModel(), failed, target) {
		t.Fatal("a shared port with one running service is not a local target")
	}
}

// Each service keeps the keepalive it has when it listens alone: TrustTunnel its own, REALITY none (Xray sets none), the
// rest the default of the front's listener.
func TestFrontRouteKeepAlive(t *testing.T) {
	tt := frontRoute(plugin.TCPRoute{ID: "a", Protocol: "trusttunnel", Names: []string{"a.example.com"}})
	if tt.KeepAlive == nil || *tt.KeepAlive != trusttunnel.KeepAlive || tt.ID != "a" || len(tt.Names) != 1 {
		t.Fatalf("TrustTunnel route = %+v", tt)
	}
	if re := frontRoute(plugin.TCPRoute{ID: "b", Protocol: "vless", Reality: true}); re.KeepAlive == nil || re.KeepAlive.Enable {
		t.Fatalf("REALITY route = %+v, want keepalive off", re)
	}
	for _, r := range []plugin.TCPRoute{{ID: "c", Protocol: "mtproto"}, {ID: "d", Protocol: "vless"}} {
		if got := frontRoute(r); got.KeepAlive != nil {
			t.Fatalf("%s route = %+v, want the listener's default", r.Protocol, got)
		}
	}
}

func TestTCPShareCapability(t *testing.T) {
	engines := func(protocols ...string) *Agent {
		a := &Agent{engines: map[string]engine.Engine{}}
		for _, p := range protocols {
			a.engines[p] = newFakeEngine(p)
		}
		return a
	}
	if caps := engines("vless", "trusttunnel", "mtproto", "hysteria2").capabilities(); !slices.Contains(caps, capTCPShare) {
		t.Fatalf("capabilities %v lack %s with every engine built in", caps, capTCPShare)
	}
	for _, protocols := range [][]string{nil, {"vless", "trusttunnel", "mtproto"}, {"vless", "mtproto", "hysteria2"}} {
		if caps := engines(protocols...).capabilities(); slices.Contains(caps, capTCPShare) {
			t.Fatalf("capabilities %v list %s with engines %v", caps, capTCPShare, protocols)
		}
	}
}
