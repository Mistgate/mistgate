package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

const testSNI = "agent-test.invalid"

// fakeProto is the smallest protocol plugin: port from settings {"port":N}, one client app.
type fakeProto struct {
	id     string
	client plugin.ClientID
}

func (p fakeProto) ID() string             { return p.id }
func (p fakeProto) DisplayName() string    { return p.id }
func (p fakeProto) SettingsSchema() []byte { return []byte(`{}`) }
func (p fakeProto) DefaultSettings() (json.RawMessage, error) {
	return json.RawMessage(`{"port":443}`), nil
}
func (p fakeProto) Validate(json.RawMessage) []protocols.FieldError { return nil }
func (p fakeProto) Summary(json.RawMessage) string                  { return p.id }
func (p fakeProto) BuildInbound(in protocols.InboundInput) (plugin.InboundSpec, error) {
	var s struct{ Port int }
	if err := json.Unmarshal(in.Profile.Settings, &s); err != nil {
		return plugin.InboundSpec{}, err
	}
	if in.PortOverride != 0 {
		s.Port = int(in.PortOverride)
	}
	return plugin.InboundSpec{Listen: plugin.Listen{Network: "udp", Port: uint16(s.Port)},
		TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: in.Node.Address}, Egress: "direct", Settings: in.Profile.Settings}, nil
}
func (p fakeProto) IssueCredential(protocols.IssueInput) (protocols.Issued, error) {
	return protocols.Issued{}, nil
}
func (p fakeProto) Clients() []plugin.ClientSupport {
	return []plugin.ClientSupport{{Client: p.client, Formats: []plugin.ClientFormat{plugin.FormatURIList}}}
}
func (p fakeProto) Render(protocols.RenderInput) (plugin.Fragment, bool) {
	return plugin.Fragment{}, false
}
func (p fakeProto) Doctor() []protocols.Check { return nil }

// env is a panel with a real store and vault behind a real TLS listener on the agent SNI.
type env struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	v    *vault.Vault
	f    *Fleet
	srv  *httptest.Server
	pool *x509.CertPool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	reg, err := protocols.NewRegistry(fakeProto{"fakehy", plugin.ClientHapp}, fakeProto{"fakewg", plugin.ClientAmnezia})
	if err != nil {
		t.Fatal(err)
	}
	return newEnvWith(t, reg)
}

func newEnvWith(t *testing.T, reg *protocols.Registry) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, vault.KeySize)
	rand.Read(key)
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := access.New(st, v, reg, nil, nil, access.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(st, v, reg, Config{AgentSNI: testSNI, PanelAddr: "panel.example.com:443", Debounce: 20 * time.Millisecond,
		Desired: desiredFrom(acc), Actor: func(context.Context) string { return "adm_test" }})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(f.AgentHandler())
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{GetConfigForClient: f.AgentTLSConfig}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(f.CACertPEM())) {
		t.Fatal("CA PEM")
	}
	return &env{t: t, ctx: ctx, st: st, v: v, f: f, srv: srv, pool: pool}
}

// desiredFrom is the production wiring of Config.Desired: the access module owns the effective-access rule.
func desiredFrom(a *access.Service) func(context.Context, string) ([]statehash.Inbound, error) {
	return func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		in, err := a.Desired(ctx, nodeID)
		out := make([]statehash.Inbound, len(in))
		for i, x := range in {
			out[i] = statehash.Inbound(x)
		}
		return out, err
	}
}

// run starts the debouncer; it stops with the test.
func (e *env) run() {
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.f.Run(ctx); close(done) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) httpClient(cert *tls.Certificate, sni string) *http.Client {
	cfg := &tls.Config{RootCAs: e.pool, ServerName: sni}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}}
}

func newCSR(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "whatever"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, der
}

// createEnrollment goes through the admin NodeService and returns node id and token.
func (e *env) createEnrollment(name, address string) (nodeID, token, cmd string) {
	e.t.Helper()
	resp, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{Name: name, Address: address}))
	if err != nil {
		e.t.Fatal(err)
	}
	cmd = resp.Msg.InstallCommand
	i := strings.Index(cmd, "--token ")
	token = strings.Fields(cmd[i+len("--token "):])[0]
	return resp.Msg.Node.Id, token, cmd
}

// agent is a fake node agent: an enrolled identity that can open Connect streams over real TLS.
type agent struct {
	e      *env
	nodeID string
	key    *ecdsa.PrivateKey
	cert   tls.Certificate
	leaf   *x509.Certificate
}

func (e *env) enroll(name string) *agent {
	e.t.Helper()
	id, token, _ := e.createEnrollment(name, name+".example.com")
	key, csr := newCSR(e.t)
	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)
	resp, err := cli.Enroll(e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: token, CsrDer: csr, ApiVersion: APIVersion}))
	if err != nil {
		e.t.Fatal(err)
	}
	return e.identity(id, key, resp.Msg.CertificatePem)
}

func (e *env) identity(id string, key *ecdsa.PrivateKey, certPEM string) *agent {
	block, _ := pem.Decode([]byte(certPEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		e.t.Fatal(err)
	}
	return &agent{e: e, nodeID: id, key: key, leaf: leaf, cert: tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: key}}
}

// conn is one open Connect stream with a reader goroutine.
type conn struct {
	t    *testing.T
	st   *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse]
	in   chan *agentv1.ConnectResponse
	errc chan error
}

func (a *agent) open() *conn {
	a.e.t.Helper()
	cli := agentv1connect.NewAgentServiceClient(a.e.httpClient(&a.cert, testSNI), a.e.srv.URL)
	st := cli.Connect(a.e.ctx)
	c := &conn{t: a.e.t, st: st, in: make(chan *agentv1.ConnectResponse, 64), errc: make(chan error, 1)}
	go func() {
		for {
			m, err := st.Receive()
			if err != nil {
				c.errc <- err
				return
			}
			c.in <- m
		}
	}()
	a.e.t.Cleanup(func() { st.CloseRequest(); st.CloseResponse() })
	return c
}

func (c *conn) send(seq uint64, m *agentv1.ConnectRequest) {
	c.t.Helper()
	m.Seq = seq
	if err := c.st.Send(m); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func hello(instance string, appliedRev uint64, appliedHash string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		AgentVersion: "0.0.1", ApiVersion: APIVersion, InstanceId: instance, NextSeq: 1,
		AppliedRevision: appliedRev, AppliedStateHash: appliedHash,
		Engines: []*agentv1.EngineInfo{{Protocol: "fakehy", Version: "v1"}},
		Facts:   &agentv1.HostFacts{Hostname: "h", Os: "linux", CpuCount: 2, BootUnix: 1000},
	}}}
}

// wait returns the next message matching pred, dropping others (Acks arrive at odd moments).
func (c *conn) wait(pred func(*agentv1.ConnectResponse) bool) *agentv1.ConnectResponse {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-c.in:
			if pred(m) {
				return m
			}
		case err := <-c.errc:
			c.t.Fatalf("stream ended while waiting: %v", err)
		case <-deadline:
			c.t.Fatal("timed out waiting for a message")
		}
	}
}

func (c *conn) desired() *agentv1.DesiredState {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetDesiredState() != nil }).GetDesiredState()
}

func (c *conn) ack() *agentv1.Ack {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil }).GetAck()
}

// quiet asserts that no DesiredState arrives within d.
func (c *conn) quiet(d time.Duration) {
	c.t.Helper()
	t := time.After(d)
	for {
		select {
		case m := <-c.in:
			if m.GetDesiredState() != nil {
				c.t.Fatalf("unexpected desired state: %v", m.GetDesiredState())
			}
		case <-t:
			return
		}
	}
}

func (c *conn) ended() error {
	c.t.Helper()
	select {
	case err := <-c.errc:
		return err
	case <-time.After(5 * time.Second):
		c.t.Fatal("stream did not end")
		return nil
	}
}

// model is what an agent holds: it applies DesiredState messages (full or delta) exactly as agent.proto
// describes and hashes the result, so the tests prove that deltas reproduce the announced hash.
type model struct {
	rev  uint64
	in   map[string]*modelInbound
	warp *plugin.WarpSpec // the node-level WARP configuration the agent holds
}

type modelInbound struct {
	spec  plugin.InboundSpec
	creds map[string]plugin.UserCred
}

func newModel() *model { return &model{in: map[string]*modelInbound{}} }

func specFromProto(s *agentv1.InboundSpec) plugin.InboundSpec {
	sp := plugin.InboundSpec{ID: s.InboundId, Protocol: s.Protocol, ProfileID: s.ProfileId, Version: s.SpecVersion, Enabled: s.Enabled,
		Listen: plugin.Listen{Network: s.Listen.Network, Port: uint16(s.Listen.Port), HopFrom: uint16(s.Listen.HopFrom), HopTo: uint16(s.Listen.HopTo)},
		TLS:    plugin.TLS{Mode: plugin.TLSMode(s.Tls.Mode), ServerName: s.Tls.ServerName}, Egress: s.Egress, Settings: json.RawMessage(s.SettingsJson)}
	if t := s.Tunnel; t != nil {
		if t.AddrV4 != "" {
			sp.Tunnel.AddrV4 = netip.MustParsePrefix(t.AddrV4)
		}
		if t.AddrV6 != "" {
			sp.Tunnel.AddrV6 = netip.MustParsePrefix(t.AddrV6)
		}
		sp.Tunnel.MTU = uint16(t.Mtu)
	}
	return sp
}

func warpFromProto(w *agentv1.WarpSpec) *plugin.WarpSpec {
	out := &plugin.WarpSpec{Enabled: w.Enabled, PrivateKey: w.PrivateKey, PeerPublicKey: w.PeerPublicKey, EndpointV4: w.EndpointV4,
		EndpointV6: w.EndpointV6, AddressV4: w.AddressV4, AddressV6: w.AddressV6, MTU: uint16(w.Mtu), Reserved: w.Reserved, Backend: w.Backend}
	for _, p := range w.Ports {
		out.Ports = append(out.Ports, uint16(p))
	}
	return out
}

func credFromProto(c *agentv1.Credential) plugin.UserCred {
	uc := plugin.UserCred{CredID: c.CredId, UserID: c.UserId, DeviceID: c.DeviceId, Data: json.RawMessage(c.DataJson), RateLimitBps: c.RateLimitBps}
	if c.ValidUntilUnix != 0 {
		uc.ValidUntil = time.Unix(c.ValidUntilUnix, 0)
	}
	return uc
}

// apply returns false for a delta whose base revision is not the model's (BASE_MISMATCH).
func (m *model) apply(ds *agentv1.DesiredState) bool {
	if ds.BaseRevision == 0 {
		m.in, m.warp = map[string]*modelInbound{}, nil // a full state: anything not listed is gone, WARP included
	} else if ds.BaseRevision != m.rev {
		return false
	}
	for _, id := range ds.RemovedInboundIds {
		delete(m.in, id)
	}
	for _, is := range ds.Inbounds {
		cur := m.in[is.InboundId]
		if is.Spec != nil {
			if cur == nil {
				cur = &modelInbound{creds: map[string]plugin.UserCred{}}
				m.in[is.InboundId] = cur
			}
			cur.spec = specFromProto(is.Spec)
		}
		if cur == nil {
			panic("delta for an unknown inbound without a spec")
		}
		if is.CredsReplace {
			cur.creds = map[string]plugin.UserCred{}
		}
		for _, c := range is.Creds {
			cur.creds[c.CredId] = credFromProto(c)
		}
		for _, id := range is.RemovedCredIds {
			delete(cur.creds, id)
		}
	}
	if ds.Warp != nil { // present replaces the whole WARP configuration; absent in a delta keeps it
		m.warp = warpFromProto(ds.Warp)
	}
	m.rev = ds.Revision
	return true
}

func (m *model) hash() string {
	var in []statehash.Inbound
	for _, x := range m.in {
		si := statehash.Inbound{Spec: x.spec}
		for _, c := range x.creds {
			si.Creds = append(si.Creds, c)
		}
		in = append(in, si)
	}
	return statehash.StateWarp(in, m.warp)
}

func (m *model) credIDs(inbound string) []string {
	var out []string
	if x := m.in[inbound]; x != nil {
		for id := range x.creds {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func applied(ds *agentv1.DesiredState, hash string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: ds.Revision, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: hash}}}
}

func sha(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
