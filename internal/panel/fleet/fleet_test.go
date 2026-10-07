package fleet

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func code(err error) connect.Code {
	if err == nil {
		return 0
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

func (e *env) count(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.st.R.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

type messageGateHandler struct {
	message string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *messageGateHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *messageGateHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == h.message {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return nil
}
func (h *messageGateHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *messageGateHandler) WithGroup(string) slog.Handler      { return h }

// connectFull connects the agent, takes the full state and confirms it. It returns the stream and the model.
func connectFull(a *agent, instance string) (*conn, *model, *agentv1.DesiredState) {
	a.e.t.Helper()
	c := a.open()
	c.send(0, hello(instance, 0, ""))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	m := newModel()
	if !m.apply(ds) {
		a.e.t.Fatal("full state did not apply")
	}
	if m.hash() != ds.StateHash {
		a.e.t.Fatalf("hash of the full state: model %s, panel %s", m.hash(), ds.StateHash)
	}
	c.send(0, applied(ds, m.hash()))
	return c, m, ds
}

func TestEnrollTokenRules(t *testing.T) {
	e := newEnv(t)
	id, token, cmd := e.createEnrollment("nodea", "nodea.example.com")

	// The install command is one line that runs the binary where the admin put it (no PATH, no lost execute bit after
	// scp): enroll, then install. It carries the pin, and the token nowhere else.
	want := "chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni " + testSNI +
		" --ca-sha256 " + e.f.CAFingerprint() + " --token " + token + " && /root/mistgate-node install"
	if cmd != want {
		t.Fatalf("install command:\n%s", cmd)
	}
	if strings.HasPrefix(token, "-") || len(token) < 40 {
		t.Errorf("token %q", token)
	}
	var cnt int64
	e.st.R.QueryRow(`SELECT count(*) FROM enrollment_token WHERE token_hash = ?`, []byte(token)).Scan(&cnt)
	if cnt != 0 {
		t.Error("the plain token is stored")
	}

	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)
	key, csr := newCSR(t)
	req := func(tok string, csr []byte, api uint32) *connect.Request[agentv1.EnrollRequest] {
		return connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: tok, CsrDer: csr, ApiVersion: api})
	}

	if _, err := cli.Enroll(e.ctx, req(token, csr, 7)); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("wrong api_version: %v", err)
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	badCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, rsaKey)
	if _, err := cli.Enroll(e.ctx, req(token, badCSR, APIVersion)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("RSA CSR: %v", err)
	}
	if _, err := cli.Enroll(e.ctx, req(token, []byte("junk"), APIVersion)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("junk CSR: %v", err)
	}

	first, err := cli.Enroll(e.ctx, req(token, csr, APIVersion))
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.NodeId != id || first.Msg.CaCertificatePem != e.f.CACertPEM() {
		t.Errorf("response %+v", first.Msg)
	}
	ident := e.identity(id, key, first.Msg.CertificatePem)
	if len(ident.leaf.URIs) != 1 || ident.leaf.URIs[0].String() != "spiffe://agent/"+id {
		t.Errorf("URI SAN %v", ident.leaf.URIs)
	}
	if d := ident.leaf.NotAfter.Sub(ident.leaf.NotBefore); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("validity %v", d)
	}
	if first.Msg.RenewAfterUnix >= first.Msg.NotAfterUnix || first.Msg.NotAfterUnix-first.Msg.RenewAfterUnix != int64(renewBefore/time.Second) {
		t.Errorf("renew_after %d not_after %d", first.Msg.RenewAfterUnix, first.Msg.NotAfterUnix)
	}

	// Retry with the same token AND key: the same certificate. Same token, other key: refused.
	again, err := cli.Enroll(e.ctx, req(token, csr, APIVersion))
	if err != nil || again.Msg.CertificatePem != first.Msg.CertificatePem {
		t.Errorf("idempotent retry: %v", err)
	}
	_, csr2 := newCSR(t)
	if _, err := cli.Enroll(e.ctx, req(token, csr2, APIVersion)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("reuse with another key: %v", err)
	}
	// Outside the 10-minute window even the same key is refused.
	e.exec(`UPDATE enrollment_token SET used_at = used_at - 700`)
	if _, err := cli.Enroll(e.ctx, req(token, csr, APIVersion)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("retry after the window: %v", err)
	}

	// Unknown and expired tokens.
	if _, err := cli.Enroll(e.ctx, req("nope", csr, APIVersion)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("unknown token: %v", err)
	}
	_, tok2, _ := e.createEnrollment("nodeb", "nodeb.example.com")
	e.exec(`UPDATE enrollment_token SET expires_at = expires_at - 7200 WHERE used_at IS NULL`)
	if _, err := cli.Enroll(e.ctx, req(tok2, csr, APIVersion)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("expired token: %v", err)
	}
}

// The add-node window offers a ready scp of the trusted bundle's binary, run on the panel's server; without a bundle it
// says where to put the file instead (empty command).
func TestEnrollmentCopyCommand(t *testing.T) {
	e := newEnv(t)
	create := func(req *adminv1.CreateEnrollmentRequest) string {
		t.Helper()
		resp, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.CopyCommand
	}
	if c := create(&adminv1.CreateEnrollmentRequest{Name: "nodea", Address: "203.0.113.10"}); c != "" {
		t.Errorf("without the updates module: %q", c)
	}
	u := &fakeUpdates{}
	e.f.SetUpdates(u)
	if c := create(&adminv1.CreateEnrollmentRequest{Name: "nodeb", Address: "nodeb.example.com"}); c != "" {
		t.Errorf("without a trusted bundle: %q", c)
	}
	u.mu.Lock()
	u.binary = map[string]string{"linux/amd64": "/var/lib/mistgate/dist/mistgate-node-linux-amd64"}
	u.mu.Unlock()
	if c := create(&adminv1.CreateEnrollmentRequest{Name: "nodec", Address: "nodec.example.com"}); c != "scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@nodec.example.com:/root/mistgate-node" {
		t.Errorf("copy command: %q", c)
	}
	if c := create(&adminv1.CreateEnrollmentRequest{Name: "noded", Address: "2001:db8::10"}); c != "scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@[2001:db8::10]:/root/mistgate-node" {
		t.Errorf("IPv6 address: %q", c)
	}
	// a new command for a known node goes to its stored address
	id, _, _ := e.createEnrollment("nodee", "nodee.example.com")
	if c := create(&adminv1.CreateEnrollmentRequest{NodeId: id}); !strings.HasSuffix(c, " root@nodee.example.com:/root/mistgate-node") {
		t.Errorf("re-enroll: %q", c)
	}
}

func TestEnrollFailuresAreRateLimited(t *testing.T) {
	e := newEnv(t)
	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)
	_, csr := newCSR(t)
	var last error
	for i := 0; i < 12; i++ {
		_, last = cli.Enroll(e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: "guess", CsrDer: csr, ApiVersion: APIVersion}))
	}
	if code(last) != connect.CodeResourceExhausted {
		t.Errorf("after many failures: %v", last)
	}
}

func TestAgentEndpointAuth(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")

	// Other server names get the listener's normal behaviour.
	if cfg, err := e.f.AgentTLSConfig(&tls.ClientHelloInfo{ServerName: "www.example.com"}); cfg != nil || err != nil {
		t.Errorf("foreign SNI: %v %v", cfg, err)
	}
	if cfg, _ := e.f.AgentTLSConfig(&tls.ClientHelloInfo{}); cfg != nil {
		t.Error("no SNI must not get the agent config")
	}
	if cfg, _ := e.f.AgentTLSConfig(&tls.ClientHelloInfo{ServerName: strings.ToUpper(testSNI)}); cfg == nil {
		t.Error("the agent SNI is case-insensitive")
	}

	// Everything except Enroll needs a node certificate.
	noCert := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)
	_, csr := newCSR(t)
	if _, err := noCert.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("Renew without a certificate: %v", err)
	}
	st := agentv1connect.NewAgentServiceClient(e.httpClient(nil, testSNI), e.srv.URL).Connect(e.ctx)
	st.Send(hello("i", 0, ""))
	if _, err := st.Receive(); code(err) != connect.CodeUnauthenticated {
		t.Errorf("Connect without a certificate: %v", err)
	}

	// A certificate from another CA is refused at the handshake.
	other := newEnv(t)
	foreign := other.enroll("nodea")
	fc := agentv1connect.NewEnrollmentServiceClient(e.httpClient(&foreign.cert, testSNI), e.srv.URL)
	if _, err := fc.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr})); err == nil {
		t.Error("a certificate of another panel CA was accepted")
	}

	// Renew: new serial, both certificates work until the old one expires.
	key2, csr2 := newCSR(t)
	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(&a.cert, testSNI), e.srv.URL)
	rr, err := cli.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr2}))
	if err != nil {
		t.Fatal(err)
	}
	renewed := e.identity(a.nodeID, key2, rr.Msg.CertificatePem)
	if renewed.leaf.SerialNumber.Cmp(a.leaf.SerialNumber) == 0 || renewed.leaf.URIs[0].String() != a.leaf.URIs[0].String() {
		t.Error("renewed certificate")
	}
	for _, id := range []*agent{a, renewed} {
		c := id.open()
		c.send(0, hello("i", 0, ""))
		c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	}
	// The old certificate is scheduled for revocation (grace), so both are valid right now.
	if n := e.count(`SELECT count(*) FROM node_cert WHERE node_id = ? AND (revoked_at IS NULL OR revoked_at > ?)`, a.nodeID, time.Now().Unix()); n != 2 {
		t.Errorf("live certificates: %d", n)
	}
}

func TestConnectDesiredStateContent(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)

	c := a.open()
	c.send(0, hello("inst1", 0, ""))
	ha := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil }).GetHelloAck()
	if ha.AckedSeq != 0 || time.Since(time.Unix(ha.ServerTimeUnix, 0)) > time.Minute || ha.Settings.StatsIntervalS != 10 || ha.Settings.DialTimeoutS != 15 {
		t.Errorf("HelloAck %+v", ha)
	}
	ds := c.desired()
	if ds.BaseRevision != 0 || ds.Revision == 0 {
		t.Errorf("full state: base %d revision %d", ds.BaseRevision, ds.Revision)
	}
	m := newModel()
	m.apply(ds)
	if m.hash() != ds.StateHash {
		t.Fatalf("state_hash %s does not match the content (%s)", ds.StateHash, m.hash())
	}

	if len(m.in) != 2 || m.in[ids.i3] != nil {
		t.Fatalf("inbounds: %v (node B's inbound must not be here)", keys(m.in))
	}
	// Access rule: disabled/expired users out, node selection, group profiles, app toggles, revoked credential out.
	wantI1 := []string{"crd_alice_hy", "crd_erin_hy", "crd_gina_hy", "crd_hank_hy"}
	wantI2 := []string{"crd_alice_wg", "crd_frank_wg", "crd_hank_wg"}
	if got := m.credIDs(ids.i1); !reflect.DeepEqual(got, wantI1) {
		t.Errorf("I1 credentials: %v, want %v", got, wantI1)
	}
	if got := m.credIDs(ids.i2); !reflect.DeepEqual(got, wantI2) {
		t.Errorf("I2 credentials: %v, want %v", got, wantI2)
	}

	i1 := m.in[ids.i1]
	if i1.spec.Protocol != "fakehy" || i1.spec.ProfileID != "prf_1" || i1.spec.Version != 1 || !i1.spec.Enabled ||
		i1.spec.Listen.Port != 443 || i1.spec.TLS.ServerName != "nodea.example.com" {
		t.Errorf("I1 spec %+v", i1.spec)
	}
	if !strings.Contains(string(i1.spec.Settings), `"password":"pw-secret"`) || !strings.Contains(string(i1.spec.Settings), `"type":"salamander"`) {
		t.Errorf("vault secret not merged into the settings: %s", i1.spec.Settings)
	}
	if erin := i1.creds["crd_erin_hy"]; erin.ValidUntil.Unix() != ids.erinExpires || erin.UserID != "usr_erin" || erin.DeviceID != "dev_erin" || string(erin.Data) != `{"v":"erin-hy"}` {
		t.Errorf("erin credential %+v", erin)
	}
	if alice := i1.creds["crd_alice_hy"]; !alice.ValidUntil.IsZero() || alice.RateLimitBps != 0 {
		t.Errorf("alice credential %+v", alice)
	}
	if frank := m.in[ids.i2].creds["crd_frank_wg"]; frank.RateLimitBps != 1_000_000 {
		t.Errorf("frank rate limit %d", frank.RateLimitBps)
	}

	// Bookkeeping: the node is active, facts stored, revision and hash persisted.
	c.send(0, applied(ds, m.hash()))
	n, err := e.st.Node(e.ctx, a.nodeID)
	if err != nil || n.State != "active" || n.DesiredRevision != ds.Revision || n.DesiredHash != ds.StateHash || n.AgentInstanceID != "inst1" {
		t.Errorf("node row %+v (%v)", n, err)
	}
	if f, _ := e.st.NodeFacts(e.ctx, a.nodeID); f.Hostname != "h" || len(f.Engines) != 1 {
		t.Errorf("facts %+v", f)
	}

	// Reconnecting with the state it already runs: nothing is resent.
	c.st.CloseRequest()
	c.ended()
	c2 := a.open()
	c2.send(0, hello("inst1", ds.Revision, ds.StateHash))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	c2.quiet(300 * time.Millisecond)
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func statsBatch(start, end int64, traffic []*agentv1.TrafficDelta, sessions []*agentv1.Session) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: start, IntervalEndUnix: end, Traffic: traffic, Sessions: sessions,
		Host: &agentv1.HostMetrics{CpuPct: 12.5, RamUsedBytes: 512, RamTotalBytes: 1024, UptimeS: 99},
	}}}
}

func TestStatsSequenceDedup(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")

	now := time.Now().Unix()
	hour := now - now%3600
	batch := func(up, down uint64) *agentv1.ConnectRequest {
		return statsBatch(now-10, now,
			[]*agentv1.TrafficDelta{
				{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: up, BytesDown: down},
				{CredId: "crd_alice_wg", InboundId: ids.i2, BytesUp: 1, BytesDown: 2},
				{CredId: "crd_nobody", InboundId: ids.i1, BytesUp: 999, BytesDown: 999},   // unknown credential
				{CredId: "crd_alice_hy", InboundId: ids.i3, BytesUp: 999, BytesDown: 999}, // inbound of another node
			},
			[]*agentv1.Session{
				{CredId: "crd_alice_hy", InboundId: ids.i1, RemoteIp: "198.51.100.7", ConnectedAtUnix: now - 60},
				{CredId: "crd_erin_hy", InboundId: ids.i1, RemoteIp: "198.51.100.8", ConnectedAtUnix: now - 30},
			})
	}
	bytes := func() (up, down int64) {
		e.st.R.QueryRow(`SELECT coalesce(sum(bytes_up),0), coalesce(sum(bytes_down),0) FROM traffic_bucket WHERE user_id = 'usr_alice' AND hour_start = ?`, hour).Scan(&up, &down)
		return
	}

	c.send(1, batch(1000, 5000))
	if ack := c.ack(); ack.UpToSeq != 1 {
		t.Errorf("ack %d", ack.UpToSeq)
	}
	if up, down := bytes(); up != 1001 || down != 5002 {
		t.Errorf("alice bucket up %d down %d (two protocols, foreign data skipped)", up, down)
	}
	var used int64
	e.st.R.QueryRow(`SELECT used_bytes FROM user WHERE id = 'usr_alice'`).Scan(&used)
	if used != 6003 {
		t.Errorf("used_bytes %d", used)
	}
	var nu, nd, peakU, peakD int64
	e.st.R.QueryRow(`SELECT bytes_up + bytes_down, bytes_up, peak_users, peak_devices FROM node_traffic_hour WHERE node_id = ? AND protocol = 'fakehy' AND hour_start = ?`, a.nodeID, hour).Scan(&nu, &nd, &peakU, &peakD)
	if nu != 6000 || nd != 1000 || peakU != 2 || peakD != 2 {
		t.Errorf("node_traffic_hour total %d up %d peak users %d devices %d", nu, nd, peakU, peakD)
	}
	on := e.f.Online()
	if len(on) != 2 {
		t.Fatalf("online %v", on)
	}
	if ou := e.f.OnlineUsers(); len(ou) != 2 || ou["usr_alice"] != a.nodeID || ou["usr_erin"] != a.nodeID {
		t.Errorf("OnlineUsers %v", ou)
	}

	// The same batch resent after a reconnect of the same instance: counted once, acked again.
	c.st.CloseRequest()
	c.ended()
	c2 := a.open()
	c2.send(0, hello("inst1", 1, ""))
	if ha := c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil }).GetHelloAck(); ha.AckedSeq != 1 {
		t.Errorf("HelloAck.acked_seq %d, want 1", ha.AckedSeq)
	}
	c2.desired()
	c2.send(1, batch(1000, 5000))
	if ack := c2.ack(); ack.UpToSeq != 1 {
		t.Errorf("ack of the duplicate %d", ack.UpToSeq)
	}
	if up, down := bytes(); up != 1001 || down != 5002 {
		t.Errorf("duplicate was counted: up %d down %d", up, down)
	}
	// A new message continues the count.
	c2.send(2, batch(10, 20))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq == 2 })
	if up, down := bytes(); up != 1012 || down != 5024 {
		t.Errorf("after seq 2: up %d down %d", up, down)
	}

	// A restarted agent (new instance id) starts at seq 1 again: not a duplicate, and HelloAck says 0.
	c2.st.CloseRequest()
	c2.ended()
	c3 := a.open()
	c3.send(0, hello("inst2", 0, ""))
	if ha := c3.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil }).GetHelloAck(); ha.AckedSeq != 0 {
		t.Errorf("acked_seq for a new instance: %d", ha.AckedSeq)
	}
	c3.desired()
	c3.send(1, batch(100, 200))
	c3.ack()
	if up, down := bytes(); up != 1113 || down != 5226 {
		t.Errorf("new instance seq 1: up %d down %d", up, down)
	}
	n, _ := e.st.Node(e.ctx, a.nodeID)
	if n.AgentInstanceID != "inst2" || n.LastSeq != 1 {
		t.Errorf("node dedup state %q %d", n.AgentInstanceID, n.LastSeq)
	}
}

func TestEventIngestionDedup(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	ev := func(code string) *agentv1.ConnectRequest {
		return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
			Severity: agentv1.Severity_SEVERITY_WARNING, Code: code, InboundId: "inb_1", TimeUnix: time.Now().Unix(), Params: map[string]string{"k": "v"}}}}
	}
	c.send(1, ev("engine_failed"))
	c.send(1, ev("engine_failed")) // a resend
	c.send(2, ev("engine_started"))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq == 2 })
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: a.nodeID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		if r.Source == "agent" {
			got = append(got, r.Code)
		}
	}
	if !reflect.DeepEqual(got, []string{"engine_started", "engine_failed"}) {
		t.Errorf("agent events %v", got)
	}
	if rows[len(rows)-1].Params["k"] != "v" && rows[0].Params["k"] != "v" {
		t.Error("params lost")
	}
}

func TestDeltaAfterStateChanged(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, m, _ := connectFull(a, "inst1")

	next := func() *agentv1.DesiredState {
		t.Helper()
		e.f.StateChanged()
		ds := c.desired()
		if ds.BaseRevision != m.rev {
			t.Fatalf("delta base %d, agent at %d", ds.BaseRevision, m.rev)
		}
		if !m.apply(ds) {
			t.Fatal("delta did not apply")
		}
		if m.hash() != ds.StateHash {
			t.Fatalf("delta result hash differs: model %s panel %s", m.hash(), ds.StateHash)
		}
		c.send(0, applied(ds, m.hash()))
		return ds
	}

	// Nothing changed: nothing is sent.
	e.f.StateChanged()
	c.quiet(300 * time.Millisecond)

	// A user is disabled: one removal, no spec resent, no other credential touched.
	e.exec(`UPDATE user SET status = 'disabled', disabled = 1 WHERE id = 'usr_erin'`)
	ds := next()
	if len(ds.Inbounds) != 1 || ds.Inbounds[0].InboundId != ids.i1 || ds.Inbounds[0].Spec != nil || ds.Inbounds[0].CredsReplace ||
		len(ds.Inbounds[0].Creds) != 0 || !reflect.DeepEqual(ds.Inbounds[0].RemovedCredIds, []string{"crd_erin_hy"}) {
		t.Errorf("removal delta %v", ds)
	}

	// A new user (like the UserService would create it): upserts on both inbounds.
	e.addUser(testUser{name: "zed", group: "grp_1", happ: true, amnezia: true, all: true})
	ds = next()
	if len(ds.Inbounds) != 2 || len(ds.RemovedInboundIds) != 0 {
		t.Fatalf("new user delta %v", ds)
	}
	for _, is := range ds.Inbounds {
		if is.Spec != nil || len(is.Creds) != 1 || is.Creds[0].UserId != "usr_zed" {
			t.Errorf("new user upsert %v", is)
		}
	}

	// An app toggle off removes that protocol's credential only.
	e.exec(`UPDATE user SET app_amnezia = 0 WHERE id = 'usr_zed'`)
	ds = next()
	if len(ds.Inbounds) != 1 || ds.Inbounds[0].InboundId != ids.i2 || !reflect.DeepEqual(ds.Inbounds[0].RemovedCredIds, []string{"crd_zed_wg"}) {
		t.Errorf("toggle delta %v", ds)
	}

	// An inbound whose spec changes is sent whole (the agent restarts it); the other one stays untouched.
	e.exec(`UPDATE inbound SET port_override = 8443, spec_version = 2 WHERE id = ?`, ids.i2)
	ds = next()
	if len(ds.Inbounds) != 1 || ds.Inbounds[0].Spec == nil || ds.Inbounds[0].Spec.Listen.Port != 8443 || !ds.Inbounds[0].CredsReplace {
		t.Errorf("spec change delta %v", ds)
	}

	// An inbound switched off leaves the desired state.
	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, ids.i2)
	ds = next()
	if !reflect.DeepEqual(ds.RemovedInboundIds, []string{ids.i2}) || len(m.in) != 1 {
		t.Errorf("inbound removal %v", ds)
	}

	// Revisions only grow and were persisted.
	n, _ := e.st.Node(e.ctx, a.nodeID)
	if n.DesiredRevision != m.rev || n.DesiredHash != m.hash() {
		t.Errorf("persisted revision %d/%s, agent %d", n.DesiredRevision, n.DesiredHash, m.rev)
	}
	if n.AppliedRevision != m.rev-0 && n.AppliedRevision == 0 {
		t.Error("applied revision not recorded")
	}
}

func TestBaseMismatchAndDrift(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, m, ds := connectFull(a, "inst1")

	// Agent claims a base mismatch: the panel resends a full state.
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_erin'`)
	e.f.StateChanged()
	delta := c.desired()
	if delta.BaseRevision == 0 {
		t.Fatal("expected a delta")
	}
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: delta.Revision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH, Error: "base"}}})
	full := c.desired()
	if full.BaseRevision != 0 || full.Revision <= delta.Revision {
		t.Fatalf("expected a full state after BASE_MISMATCH: %v", full)
	}
	m.apply(full)
	if m.hash() != full.StateHash {
		t.Error("full resend hash")
	}

	// The agent reports another hash than it was sent: state_drift and one full resend ...
	c.send(0, applied(full, "deadbeef"))
	again := c.desired()
	if again.BaseRevision != 0 || again.Revision <= full.Revision {
		t.Fatalf("expected a full resend after drift: %v", again)
	}
	// ... and a second drift right after only raises the error event.
	c.send(0, applied(again, "deadbeef"))
	c.quiet(300 * time.Millisecond)
	rows, _, _ := e.st.Events(e.ctx, store.EventFilter{NodeID: a.nodeID, Limit: 50})
	var sev []int
	for _, r := range rows {
		if r.Code == "state_drift" {
			sev = append(sev, r.Severity)
		}
	}
	if !reflect.DeepEqual(sev, []int{3, 2}) {
		t.Errorf("state_drift severities %v, want [3 2]", sev)
	}
	ns, _ := nodeService{e.f}.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	for _, n := range ns.Msg.Nodes {
		if n.Id == a.nodeID && (n.Reason == nil || n.Reason.Code != "state_drift") {
			t.Errorf("node reason %v", n.Reason)
		}
	}
	_ = ds
}

func TestApplyResultBaseMismatchResendPrecedesDesiredChange(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, m, initial := connectFull(a, "inst1")

	e.f.mu.Lock()
	s := e.f.sessions[a.nodeID]
	e.f.mu.Unlock()
	if s == nil {
		t.Fatal("connected session was not registered")
	}

	previousDesired := e.f.cfg.Desired
	preparedEntered, preparedRelease := make(chan struct{}), make(chan struct{})
	var blockPreparation atomic.Bool
	blockPreparation.Store(true)
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		in, err := previousDesired(ctx, nodeID)
		if blockPreparation.CompareAndSwap(true, false) {
			close(preparedEntered)
			<-preparedRelease
		}
		return in, err
	}
	defer func() {
		select {
		case <-preparedRelease:
		default:
			close(preparedRelease)
		}
		e.f.cfg.Desired = previousDesired
	}()

	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: initial.Revision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}})
	select {
	case <-preparedEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("full resend did not prepare desired state")
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_erin'`)
	e.f.StateChanged()
	full := c.desired()
	if full.BaseRevision != 0 || full.Revision <= initial.Revision {
		t.Fatalf("base mismatch resend = %v", full)
	}
	if !m.apply(full) || m.hash() != full.StateHash {
		t.Fatal("base mismatch resend did not contain the newest desired state")
	}
	close(preparedRelease)
	e.exec(`UPDATE user SET status = 'active' WHERE id = 'usr_erin'`)
	e.f.StateChanged()
	delta := c.desired()
	if delta.BaseRevision != full.Revision || delta.Revision <= full.Revision {
		t.Fatalf("desired-state delta after full resend = %v, full revision %d", delta, full.Revision)
	}
	if !m.apply(delta) || m.hash() != delta.StateHash {
		t.Fatal("full resend followed by delta did not reproduce the newest state")
	}
	n, err := e.st.Node(e.ctx, a.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if n.DesiredRevision != delta.Revision || n.DesiredHash != delta.StateHash {
		t.Fatalf("stored desired revision/hash = %d/%s, newest delta = %d/%s", n.DesiredRevision, n.DesiredHash, delta.Revision, delta.StateHash)
	}
}

func TestSessionTeardownUnregistersWithoutWaitingForCoreStep(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()
	drain := statsBatch(now-10, now, nil, nil)
	drain.Seq = 1
	c.send(1, drain)
	c.ack()

	e.f.mu.Lock()
	s := e.f.sessions[a.nodeID]
	e.f.mu.Unlock()
	if s == nil {
		t.Fatal("connected session was not registered")
	}

	entered, release := make(chan struct{}), make(chan struct{})
	e.f.cfg.OnUsage = func(context.Context, []string) {
		close(entered)
		<-release
	}
	stepDone := make(chan error, 1)
	stepReleased := false
	releaseStep := func() {
		if !stepReleased {
			close(release)
			stepReleased = true
		}
	}
	defer func() {
		releaseStep()
		<-stepDone
	}()
	frame := statsBatch(now-10, now, []*agentv1.TrafficDelta{{CredId: "crd_hank_hy", InboundId: ids.i1, BytesUp: 600, BytesDown: 600}}, nil)
	frame.Seq = 2
	go func() {
		_, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAgentFrame, At: time.Now().UTC(), Frame: frame})
		stepDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("test step did not reach the usage hook")
	}

	s.cancel(context.Canceled)
	select {
	case <-c.errc:
	case <-time.After(5 * time.Second):
		t.Fatal("session teardown waited for the in-flight core step")
	}

	e.f.mu.Lock()
	registered := e.f.sessions[a.nodeID] == s
	e.f.mu.Unlock()
	if registered {
		t.Fatal("session remained registered after teardown")
	}
	var disconnected int64
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT last_disconnected_at FROM node WHERE id = ?`, a.nodeID).Scan(&disconnected); err != nil {
		t.Fatal(err)
	}
	if disconnected == 0 {
		t.Fatal("NodeDisconnected was not recorded")
	}
}

func TestUsageHookLetsAccessEnforceQuota(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	hooked := make(chan []string, 4)
	// The access module's Recompute: flips the status and notifies the fleet.
	e.f.cfg.OnUsage = func(_ context.Context, users []string) {
		hooked <- append([]string(nil), users...)
		for _, u := range users {
			e.exec(`UPDATE user SET status = 'limited' WHERE id = ? AND quota_bytes > 0 AND used_bytes >= quota_bytes`, u)
		}
		e.f.StateChanged()
	}
	c, m, _ := connectFull(a, "inst1")

	now := time.Now().Unix()
	c.send(1, statsBatch(now-10, now, []*agentv1.TrafficDelta{
		{CredId: "crd_hank_hy", InboundId: ids.i1, BytesUp: 600, BytesDown: 600},
		{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 1, BytesDown: 1},
		{CredId: "crd_nobody", InboundId: ids.i1, BytesUp: 5, BytesDown: 5},
	}, nil))
	select {
	case users := <-hooked:
		sort.Strings(users)
		if !reflect.DeepEqual(users, []string{"usr_alice", "usr_hank"}) {
			t.Errorf("hook users %v", users)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnUsage not called")
	}
	ds := c.desired()
	if !m.apply(ds) || m.hash() != ds.StateHash {
		t.Fatal("quota delta")
	}
	if got := m.credIDs(ids.i1); contains(got, "crd_hank_hy") || !contains(got, "crd_alice_hy") {
		t.Errorf("I1 after quota: %v", got)
	}
	if got := m.credIDs(ids.i2); contains(got, "crd_hank_wg") {
		t.Errorf("I2 after quota: %v (every credential of the user leaves)", got)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestNewerStreamSupersedes(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c1, _, _ := connectFull(a, "inst1")
	c2 := a.open()
	c2.send(0, hello("inst1", 0, ""))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if err := c1.ended(); code(err) != connect.CodeAborted {
		t.Errorf("old stream ended with %v, want ABORTED", err)
	}
	if e.f.session(a.nodeID) == nil {
		t.Error("the new stream is not registered")
	}
	// The old stream's exit must not mark the node disconnected.
	time.Sleep(100 * time.Millisecond)
	if n, _ := e.st.Node(e.ctx, a.nodeID); !n.LastDisconnectedAt.IsZero() {
		t.Error("superseded stream recorded a disconnect")
	}
}

func TestFirstMessageMustBeHelloAndApiVersion(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	c := a.open()
	c.send(0, statsBatch(1, 2, nil, nil))
	if err := c.ended(); code(err) != connect.CodeInvalidArgument {
		t.Errorf("non-Hello first message: %v", err)
	}
	c = a.open()
	h := hello("i", 0, "")
	h.GetHello().ApiVersion = 9
	c.send(0, h)
	if err := c.ended(); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("api_version 9: %v", err)
	}
}

func TestLivenessIsJudgedByReceivedMessages(t *testing.T) {
	e := newEnv(t)
	e.f.unit = 50 * time.Millisecond // the 15 s minimum becomes 750 ms
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	e.exec(`UPDATE node SET liveness_timeout_s = 15`)
	c, _, _ := connectFull(a, "inst1")

	// Any message keeps the stream alive, however slow the node answers commands.
	for i := 0; i < 6; i++ {
		time.Sleep(300 * time.Millisecond)
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: uint64(i)}}})
	}
	if e.f.session(a.nodeID) == nil {
		t.Fatal("stream dropped although messages kept arriving")
	}
	// Silence ends it.
	if err := c.ended(); code(err) != connect.CodeDeadlineExceeded {
		t.Errorf("silent stream ended with %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n, _ := e.st.Node(e.ctx, a.nodeID); n.LastDisconnectedAt.IsZero() {
		t.Error("disconnect not recorded")
	}
}

func TestRetire(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")

	// Wrong confirmation name.
	if _, err := (nodeService{e.f}).RetireNode(e.ctx, connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: a.nodeID, ConfirmName: "nope"})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("wrong name: %v", err)
	}

	type result struct {
		resp *connect.Response[adminv1.RetireNodeResponse]
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, err := nodeService{e.f}.RetireNode(e.ctx, connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: a.nodeID, ConfirmName: "nodea"}))
		done <- result{r, err}
	}()
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetRetire() != nil })
	c.st.CloseRequest() // the agent exits
	r := <-done
	if r.err != nil || !r.resp.Msg.AgentNotified {
		t.Fatalf("RetireNode: %v %+v", r.err, r.resp)
	}

	n, _ := e.st.Node(e.ctx, a.nodeID)
	if n.State != "retired" || n.RetiredAt.IsZero() {
		t.Errorf("node %+v", n)
	}
	if live := e.count(`SELECT count(*) FROM node_cert WHERE node_id = ? AND revoked_at IS NULL`, a.nodeID); live != 0 {
		t.Errorf("%d certificates still valid", live)
	}
	if e.f.session(a.nodeID) != nil {
		t.Error("stream still registered")
	}
	// Dropped from the desired state, and it cannot come back: the revoked certificate fails the handshake.
	st, err := e.f.buildState(e.ctx, n, nil)
	if err != nil || len(st.in) != 0 {
		t.Errorf("retired node desired state %v %v", st, err)
	}
	c2 := a.open()
	c2.st.Send(hello("inst1", 0, ""))
	if err := c2.ended(); err == nil {
		t.Error("a retired node reconnected")
	}
	// Re-enrolling a retired node is refused; its history stays.
	if _, err := (nodeService{e.f}).CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{NodeId: a.nodeID})); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_retired" {
		t.Errorf("re-enroll retired: %v", err)
	}
	if e.count(`SELECT count(*) FROM node WHERE id = ?`, a.nodeID) != 1 {
		t.Error("row gone")
	}
	// Other nodes are unaffected.
	if other, _ := e.st.Node(e.ctx, ids.nodeB); other.State != "active" {
		t.Error("node B changed")
	}
}

func TestReEnrollmentRevokesOldCertificate(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	c := a.open()
	c.send(0, hello("i", 0, ""))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })

	_, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	var tok string
	// The token is only in the response; fetch a fresh one through the API again.
	resp, _ := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{NodeId: a.nodeID}))
	tok = strings.Fields(resp.Msg.InstallCommand[strings.Index(resp.Msg.InstallCommand, "--token ")+8:])[0]
	key, csr := newCSR(t)
	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)
	er, err := cli.Enroll(e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: tok, CsrDer: csr, ApiVersion: APIVersion}))
	if err != nil {
		t.Fatal(err)
	}
	if er.Msg.NodeId != a.nodeID {
		t.Fatal("node id")
	}
	// The running stream (old certificate) is closed and the old certificate is dead.
	if err := c.ended(); code(err) != connect.CodeAborted {
		t.Errorf("old stream: %v", err)
	}
	old := a.open()
	old.st.Send(hello("i", 0, ""))
	if err := old.ended(); err == nil {
		t.Error("the revoked certificate still connects")
	}
	fresh := e.identity(a.nodeID, key, er.Msg.CertificatePem)
	nc := fresh.open()
	nc.send(0, hello("i", 0, ""))
	nc.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
}

func TestRestartInboundsRoundtrip(t *testing.T) {
	e := newEnv(t)
	e.f.unit = 20 * time.Millisecond // apply timeout 120 -> 2.4 s
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	e.exec(`UPDATE node SET liveness_timeout_s = 3600`)
	ns := nodeService{e.f}
	if _, err := ns.RestartInbounds(e.ctx, connect.NewRequest(&adminv1.RestartInboundsRequest{NodeId: a.nodeID})); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_offline" {
		t.Errorf("offline node: %v", err)
	}
	c, _, _ := connectFull(a, "inst1")
	if _, err := ns.RestartInbounds(e.ctx, connect.NewRequest(&adminv1.RestartInboundsRequest{NodeId: a.nodeID, InboundId: ids.i3})); code(err) != connect.CodeNotFound {
		t.Errorf("foreign inbound: %v", err)
	}
	go func() {
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetRestartInbound() != nil })
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
			RequestId: m.GetRestartInbound().RequestId, Ok: true, Affected: 2}}})
	}()
	resp, err := ns.RestartInbounds(e.ctx, connect.NewRequest(&adminv1.RestartInboundsRequest{NodeId: a.nodeID}))
	if err != nil || resp.Msg.Restarted != 2 {
		t.Fatalf("restart: %v %v", resp, err)
	}
	// A node that never answers: "no answer", the node stays online.
	if _, err := ns.RestartInbounds(e.ctx, connect.NewRequest(&adminv1.RestartInboundsRequest{NodeId: a.nodeID})); code(err) != connect.CodeDeadlineExceeded {
		t.Errorf("no answer: %v", err)
	}
	if e.f.session(a.nodeID) == nil {
		t.Error("a slow answer dropped the stream")
	}
}

func TestNodeDownAndRecoveredEvents(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	c.st.CloseRequest()
	c.ended()
	time.Sleep(100 * time.Millisecond)

	// Gap under ten minutes: a blip, no node_down.
	e.f.sweep(e.ctx)
	if e.count(`SELECT count(*) FROM event WHERE code = 'node_down'`) != 0 {
		t.Error("node_down for a fresh disconnect")
	}
	ns, _ := nodeService{e.f}.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	status := func(id string) adminv1.NodeStatus {
		for _, n := range ns.Msg.Nodes {
			if n.Id == id {
				return n.Status
			}
		}
		return 0
	}
	if status(a.nodeID) != adminv1.NodeStatus_NODE_STATUS_BLIP {
		t.Errorf("status %v", status(a.nodeID))
	}
	// Silent for 20 minutes: node_down once.
	e.exec(`UPDATE node SET last_seen_at = last_seen_at - 1200, last_disconnected_at = last_disconnected_at - 1200, last_connected_at = last_connected_at - 1200 WHERE id = ?`, a.nodeID)
	e.f.sweep(e.ctx)
	e.f.sweep(e.ctx)
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'node_down'`); n != 1 {
		t.Errorf("node_down events: %d", n)
	}
	ns, _ = nodeService{e.f}.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if status(a.nodeID) != adminv1.NodeStatus_NODE_STATUS_DOWN {
		t.Errorf("status %v", status(a.nodeID))
	}
	// Back: node_recovered.
	c2 := a.open()
	c2.send(0, hello("inst1", 0, ""))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'node_recovered'`); n != 1 {
		t.Errorf("node_recovered events: %d", n)
	}
}

// adminClient serves the admin handlers over plain HTTP for tests that need real streaming.
func (e *env) adminClients() (adminv1connect.NodeServiceClient, adminv1connect.FleetServiceClient) {
	mux := http.NewServeMux()
	p, h := e.f.NodeHandler()
	mux.Handle(p, h)
	p, h = e.f.FleetHandler()
	mux.Handle(p, h)
	srv := httptest.NewServer(mux)
	e.t.Cleanup(srv.Close)
	return adminv1connect.NewNodeServiceClient(srv.Client(), srv.URL), adminv1connect.NewFleetServiceClient(srv.Client(), srv.URL)
}

func TestAdminNodeAndFleetViews(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	pendingID, _, _ := e.createEnrollment("nodec", "203.0.113.10")
	nodes, fl := e.adminClients()
	c, _, _ := connectFull(a, "inst1")

	now := time.Now().Unix()
	hour := now - now%3600
	c.send(1, statsBatch(now-10, now,
		[]*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 1000, BytesDown: 5000}},
		[]*agentv1.Session{{CredId: "crd_alice_hy", InboundId: ids.i1, RemoteIp: "198.51.100.7", ConnectedAtUnix: now - 60}}))
	c.ack()

	ln, err := nodes.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*adminv1.Node{}
	for _, n := range ln.Msg.Nodes {
		by[n.Id] = n
	}
	na := by[a.nodeID]
	if na.Status != adminv1.NodeStatus_NODE_STATUS_ONLINE || na.TrafficTodayBytes != 6000 || !na.HasMetrics || na.CpuPct != 12.5 || na.RamPct != 50 ||
		na.UptimeS != 99 || len(na.Online) != 1 || na.Online[0].Protocol != "fakehy" || na.Online[0].Users != 1 || !reflect.DeepEqual(na.Protocols, []string{"fakehy", "fakewg"}) {
		t.Errorf("node A %+v", na)
	}
	if by[pendingID].Status != adminv1.NodeStatus_NODE_STATUS_PENDING || by[pendingID].Reason.GetCode() != "enrollment_pending" {
		t.Errorf("pending node %+v", by[pendingID])
	}
	if by[ids.nodeB].Status != adminv1.NodeStatus_NODE_STATUS_DOWN {
		t.Errorf("node B %+v", by[ids.nodeB])
	}

	gn, err := nodes.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	g := gn.Msg
	if len(g.Inbounds) != 2 || g.Inbounds[0].Port != 443 || g.Inbounds[0].TlsServerName != "nodea.example.com" || g.Inbounds[0].ProfileName != "p1" ||
		g.Metrics == nil || g.Metrics.RamTotalBytes != 1024 || g.Facts.Hostname != "h" || g.Timeouts.LivenessTimeoutS != 90 {
		t.Errorf("GetNode %+v", g)
	}
	if len(g.OnlineUsers) != 1 || g.OnlineUsers[0].UserName != "alice" || g.OnlineUsers[0].DeviceModel != "ios iPhone" || g.OnlineUsers[0].DownBps != 4000 {
		t.Errorf("online users %+v", g.OnlineUsers)
	}
	if len(g.TopToday) != 1 || g.TopToday[0].UserName != "alice" || g.TopToday[0].Bytes != 6000 {
		t.Errorf("top today %+v", g.TopToday)
	}
	if _, err := nodes.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: "nod_missing"})); code(err) != connect.CodeNotFound {
		t.Errorf("missing node: %v", err)
	}

	ov, err := fl.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	o := ov.Msg
	if o.NodesTotal != 3 || o.NodesProblem != 1 || o.UsersOnline != 1 || len(o.Traffic) != 24 || len(o.Online) != 24 || len(o.Nodes) != 3 {
		t.Errorf("overview %d nodes, %d problem, %d online, %d/%d points", o.NodesTotal, o.NodesProblem, o.UsersOnline, len(o.Traffic), len(o.Online))
	}
	last := o.Traffic[23]
	if last.StartUnix != hour || len(last.Values) != 1 || last.Values[0].Protocol != "fakehy" || last.Values[0].Value != 6000 {
		t.Errorf("traffic last point %+v", last)
	}
	if ol := o.Online[23]; len(ol.Values) != 1 || ol.Values[0].Value != 1 {
		t.Errorf("online last point %+v", ol)
	}
	for _, card := range o.Nodes {
		if card.Id == a.nodeID && (len(card.SparkBytes) != 24 || card.SparkBytes[23] != 6000 || card.DownBps != 4000 || card.UpBps != 800 || !card.HasMetrics) {
			t.Errorf("card %+v", card)
		}
	}
	if len(o.TopConsumers) != 1 || o.TopConsumers[0].UserName != "alice" || o.TopConsumers[0].NodeName != "nodea" || o.TopConsumers[0].DownBps != 4000 {
		t.Errorf("top consumers %+v", o.TopConsumers)
	}
	if len(o.Events) == 0 || o.Events[0].Id == 0 {
		t.Errorf("events %+v", o.Events)
	}
	if ov7, _ := fl.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{Range: adminv1.OverviewRange_OVERVIEW_RANGE_7D})); len(ov7.Msg.Traffic) != 168 {
		t.Error("7d range")
	}

	// Events: newest first, node filter, names, pagination.
	for i := 0; i < 5; i++ {
		e.f.event(e.ctx, 1+i%3, "test_event", a.nodeID, nil)
	}
	le, err := fl.ListEvents(e.ctx, connect.NewRequest(&adminv1.ListEventsRequest{NodeId: a.nodeID, Limit: 3}))
	if err != nil || len(le.Msg.Events) != 3 || !le.Msg.HasMore || le.Msg.Events[0].NodeName != "nodea" || le.Msg.Events[0].Id < le.Msg.Events[1].Id {
		t.Fatalf("ListEvents %+v %v", le, err)
	}
	next, _ := fl.ListEvents(e.ctx, connect.NewRequest(&adminv1.ListEventsRequest{NodeId: a.nodeID, Limit: 200, BeforeId: le.Msg.Events[2].Id}))
	if len(next.Msg.Events) == 0 || next.Msg.Events[0].Id >= le.Msg.Events[2].Id || next.Msg.HasMore {
		t.Errorf("second page %+v", next.Msg)
	}
	warn, _ := fl.ListEvents(e.ctx, connect.NewRequest(&adminv1.ListEventsRequest{MinSeverity: adminv1.EventSeverity_EVENT_SEVERITY_WARNING}))
	for _, ev := range warn.Msg.Events {
		if ev.Severity < adminv1.EventSeverity_EVENT_SEVERITY_WARNING {
			t.Errorf("info event in a warning filter: %+v", ev)
		}
	}

	// UpdateNode: validation, conflicts, persisted fields.
	up := func(r *adminv1.UpdateNodeRequest) error {
		r.NodeId = a.nodeID
		_, err := nodes.UpdateNode(e.ctx, connect.NewRequest(r))
		return err
	}
	bad := "Bad Name!"
	if code(up(&adminv1.UpdateNodeRequest{Name: &bad})) != connect.CodeInvalidArgument {
		t.Error("bad name accepted")
	}
	taken := "nodeb"
	if code(up(&adminv1.UpdateNodeRequest{Name: &taken})) != connect.CodeAlreadyExists {
		t.Error("duplicate name accepted")
	}
	if code(up(&adminv1.UpdateNodeRequest{Timeouts: &adminv1.NodeTimeouts{LivenessTimeoutS: 5}})) != connect.CodeInvalidArgument {
		t.Error("liveness 5 s accepted")
	}
	if code(up(&adminv1.UpdateNodeRequest{DnsResolvers: &adminv1.DnsResolvers{Values: []string{"not an ip"}}})) != connect.CodeInvalidArgument {
		t.Error("bad resolver accepted")
	}
	notes, loc := "hoster blocks udp", "Tallinn"
	if err := up(&adminv1.UpdateNodeRequest{Notes: &notes, Location: &loc, DnsResolvers: &adminv1.DnsResolvers{Values: []string{"77.88.8.8", "1.1.1.1:53"}},
		Timeouts: &adminv1.NodeTimeouts{LivenessTimeoutS: 200}}); err != nil {
		t.Fatal(err)
	}
	n, _ := e.st.Node(e.ctx, a.nodeID)
	if n.Notes != notes || n.Location != loc || !reflect.DeepEqual(n.DNSResolvers, []string{"77.88.8.8", "1.1.1.1:53"}) || n.LivenessTimeoutS != 200 || n.ApplyTimeoutS != 120 || n.DialTimeoutS != 15 {
		t.Errorf("node after update %+v", n)
	}

	// CreateEnrollment validation and uniqueness.
	ce := func(r *adminv1.CreateEnrollmentRequest) error {
		_, err := nodes.CreateEnrollment(e.ctx, connect.NewRequest(r))
		return err
	}
	if code(ce(&adminv1.CreateEnrollmentRequest{Name: "NODEA", Address: "x.example.com"})) != connect.CodeAlreadyExists {
		t.Error("case-insensitive duplicate accepted")
	}
	for _, r := range []*adminv1.CreateEnrollmentRequest{
		{Name: "a", Address: "x.example.com"},
		{Name: "ok-name", Address: "not a host"},
		{Name: "ok-name", Address: "x.example.com", TtlSeconds: 90000},
		{Name: "ok-name", Address: "x.example.com", CountryCode: "DEU"},
	} {
		if code(ce(r)) != connect.CodeInvalidArgument {
			t.Errorf("accepted %+v", r)
		}
	}
	if code(ce(&adminv1.CreateEnrollmentRequest{NodeId: "nod_missing"})) != connect.CodeNotFound {
		t.Error("re-enroll of a missing node")
	}
}

func TestCAPersistsAndIsSealed(t *testing.T) {
	e := newEnv(t)
	rows, err := e.st.CAs(e.ctx)
	if err != nil || len(rows) != 1 || !rows[0].Active {
		t.Fatalf("CA rows %v %v", rows, err)
	}
	if strings.Contains(string(rows[0].KeyEnc), "PRIVATE") || len(rows[0].KeyEnc) < 40 {
		t.Error("CA key is not sealed")
	}
	if _, err := e.v.Open(rows[0].KeyEnc, "cas_other"); err == nil {
		t.Error("CA key opens under another record id")
	}
	// A second panel start over the same database keeps the CA (and its fingerprint).
	f2, err := New(e.st, e.v, e.f.reg, Config{AgentSNI: testSNI, Desired: e.f.cfg.Desired})
	if err != nil || f2.CAFingerprint() != e.f.CAFingerprint() {
		t.Errorf("reload: %v", err)
	}
	if e.f.CAFingerprint() != rows[0].Fingerprint {
		t.Error("fingerprint mismatch")
	}
	// A wrong master key cannot start the module.
	key := make([]byte, 32)
	rand.Read(key)
	v2, _ := vault.New(key)
	if _, err := New(e.st, v2, e.f.reg, Config{AgentSNI: testSNI, Desired: e.f.cfg.Desired}); err == nil {
		t.Error("CA opened with another master key")
	}
}

func TestBucketHour(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := func(t time.Time) int64 { return t.Unix() - t.Unix()%3600 }
	cases := []struct {
		name  string
		end   time.Time
		want  int64
		stale bool
	}{
		{"normal", now.Add(-30 * time.Second), h(now.Add(-30 * time.Second)), false},
		{"previous hour", now.Add(-90 * time.Minute), h(now.Add(-90 * time.Minute)), false},
		{"slightly ahead", now.Add(2 * time.Minute), h(now.Add(2 * time.Minute)), false},
		{"far future is clamped", now.Add(3 * time.Hour), h(now), false},
		{"older than 48 h goes to the receive hour", now.Add(-49 * time.Hour), h(now), true},
		{"zero time", time.Unix(0, 0), h(now), true},
	}
	for _, c := range cases {
		if got, stale := bucketHour(c.end, now); got != c.want || stale != c.stale || got%3600 != 0 {
			t.Errorf("%s: got %d stale %v, want %d %v", c.name, got, stale, c.want, c.stale)
		}
	}
}

func TestDesiredStateNodeSelection(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	nb, err := e.st.Node(e.ctx, ids.nodeB)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.f.buildState(e.ctx, nb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.in) != 1 || st.in[ids.i3] == nil || st.in[ids.i3].spec.TLS.ServerName != "b.example.com" {
		t.Fatalf("node B inbounds %v", keys(st.in))
	}
	var got []string
	for _, c := range st.in[ids.i3].creds {
		got = append(got, c.CredID)
	}
	// bob is pinned to node B, so he is here (and not on A); frank has no Happ app; carol/dave are out.
	want := []string{"crd_alice_hy", "crd_bob_hy", "crd_erin_hy", "crd_gina_hy", "crd_hank_hy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("node B credentials %v, want %v", got, want)
	}
	// The hash is a pure function of the content: rebuilding gives the same one.
	again, _ := e.f.buildState(e.ctx, nb, nil)
	if again.hash != st.hash {
		t.Error("state hash is not stable")
	}
}

func TestDesiredSourceCanBeReplaced(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	spec := plugin.InboundSpec{ID: "inb_x", Protocol: "fakehy", ProfileID: "prf_x", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 4443}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "x"}, Egress: "direct", Settings: json.RawMessage(`{}`)}
	var creds []plugin.UserCred
	e.f.cfg.Desired = func(_ context.Context, nodeID string) ([]statehash.Inbound, error) {
		return []statehash.Inbound{{Spec: spec, Creds: creds}}, nil
	}
	c, m, _ := connectFull(a, "inst1")
	if len(m.in) != 1 || m.in["inb_x"] == nil {
		t.Fatalf("external source not used: %v", keys(m.in))
	}
	creds = []plugin.UserCred{{CredID: "crd_2", Data: json.RawMessage(`{"a":1}`)}, {CredID: "crd_1", Data: json.RawMessage(`{"a":2}`)}}
	e.f.StateChanged()
	ds := c.desired()
	if !m.apply(ds) || m.hash() != ds.StateHash || !reflect.DeepEqual(m.credIDs("inb_x"), []string{"crd_1", "crd_2"}) {
		t.Errorf("delta from the external source: %v", ds)
	}
}
