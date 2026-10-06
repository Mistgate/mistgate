package agent

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
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
)

const testSNI = "q3m8x2kd7w.invalid"

// fakePanel is an in-process stand-in for the panel's agent endpoint: a TLS server with HTTP/2 that
// implements EnrollmentService and AgentService with the generated code. It is deliberately dumb: it
// dedups reliable messages by (instance, seq) like the real panel and records everything it sees.
type fakePanel struct {
	agentv1connect.UnimplementedAgentServiceHandler // FetchUpdate: the node-update agent replaces it with a real fake

	t      *testing.T
	srv    *httptest.Server
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  string

	token        string
	certValidity time.Duration // validity of issued certificates (short => the agent renews at once)

	blackhole     atomic.Bool  // accept the stream, never answer
	dropAcks      atomic.Bool  // commit reliable messages but never ack them
	linkSupported atomic.Bool  // advertise the optional link transport in HelloAck
	ackedOverride atomic.Int64 // -1 = honest; otherwise HelloAck.acked_seq is forced to this value
	settings      atomic.Pointer[pb.NodeSettings]
	serverSkew    atomic.Int64 // seconds added to the panel clock in HelloAck

	mu         sync.Mutex
	tokenUsed  bool
	renewCalls int
	conns      []*panelConn
	lastSeq    map[string]uint64 // instance -> committed seq
	up, down   map[string]uint64 // cred id -> counted bytes (deduped)
	sessions   []*pb.Session     // latest snapshot
	lastStats  *pb.StatsBatch    // the newest committed batch, whole (health, WARP)
	events     []*pb.Event       // committed
	dups       int
	committed  []uint64 // seqs committed, in order

	hellos    chan *pb.Hello
	applies   chan *pb.ApplyResult
	cmds      chan *pb.CommandResult
	logs      chan *pb.LogChunk
	connects  chan struct{}
	doctors   chan *pb.DoctorReport
	bundle    map[string][]byte // FetchUpdate serves these by name (guarded by mu)
	fetches   []uint64          // offsets FetchUpdate was asked for
	fetchBusy int               // answer RESOURCE_EXHAUSTED this many times first
	cmdsSeen  atomic.Int64      // CommandResults received, whether or not a test has read them
	badSeq    atomic.Int64      // DoctorReports that arrived with a seq (they must be unreliable, seq 0)
}

type panelConn struct {
	hello *pb.Hello
	out   chan *pb.ConnectResponse
	kill  chan struct{}
	once  sync.Once
	seqs  []uint64 // every reliable seq received on this stream, in arrival order (guarded by fakePanel.mu)
	uris  []*url.URL
}

func (c *panelConn) drop() { c.once.Do(func() { close(c.kill) }) }

func newFakePanel(t *testing.T) *fakePanel {
	t.Helper()
	p := &fakePanel{
		t: t, token: "tok-one-time", certValidity: 30 * 24 * time.Hour,
		lastSeq: map[string]uint64{}, up: map[string]uint64{}, down: map[string]uint64{},
		hellos: make(chan *pb.Hello, 64), applies: make(chan *pb.ApplyResult, 64),
		cmds: make(chan *pb.CommandResult, 64), logs: make(chan *pb.LogChunk, 256), connects: make(chan struct{}, 64),
		doctors: make(chan *pb.DoctorReport, 64),
	}
	p.ackedOverride.Store(-1)
	p.settings.Store(&pb.NodeSettings{StatsIntervalS: 10, DnsResolvers: []string{"192.0.2.53"}})

	var err error
	p.caKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Panel CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &p.caKey.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	p.caCert, _ = x509.ParseCertificate(der)
	p.caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	// Server certificate for the agent SNI, signed by the CA. The chain sent on the wire includes the CA.
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "panel"}, DNSNames: []string{testSNI},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, p.caCert, &srvKey.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(p.caCert)

	mux := http.NewServeMux()
	path, h := agentv1connect.NewEnrollmentServiceHandler(p)
	mux.Handle(path, h)
	path, h = agentv1connect.NewAgentServiceHandler(p)
	mux.Handle(path, p.requireClientCert(h))
	p.srv = httptest.NewUnstartedServer(mux)
	p.srv.EnableHTTP2 = true
	p.srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{srvDER, der}, PrivateKey: srvKey}},
		ClientAuth:   tls.VerifyClientCertIfGiven, ClientCAs: pool,
	}
	p.srv.StartTLS()
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePanel) addr() string { return p.srv.Listener.Addr().String() }

func (p *fakePanel) fingerprint() string {
	sum := sha256.Sum256(p.caCert.Raw)
	return hex.EncodeToString(sum[:])
}

// requireClientCert is the agent endpoint's mTLS gate.
func (p *fakePanel) requireClientCert(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *fakePanel) issue(csrDER []byte) (string, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if pub, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() {
		return "", connect.NewError(connect.CodeInvalidArgument, errBadKey)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<60))
	uri, _ := url.Parse("mistgate://node/nod_test")
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "nod_test"}, URIs: []*url.URL{uri},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(p.certValidity),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, p.caCert, csr.PublicKey, p.caKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

var errBadKey = errors.New("CSR key must be ECDSA P-256")

func (p *fakePanel) Enroll(_ context.Context, req *connect.Request[pb.EnrollRequest]) (*connect.Response[pb.EnrollResponse], error) {
	p.mu.Lock()
	if req.Msg.EnrollmentToken != p.token || p.tokenUsed {
		p.mu.Unlock()
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("bad token"))
	}
	p.tokenUsed = true
	p.mu.Unlock()
	certPEM, err := p.issue(req.Msg.CsrDer)
	if err != nil {
		return nil, err
	}
	na := time.Now().Add(p.certValidity)
	return connect.NewResponse(&pb.EnrollResponse{
		NodeId: "nod_test", CertificatePem: certPEM, CaCertificatePem: p.caPEM,
		NotAfterUnix: na.Unix(), RenewAfterUnix: na.Add(-renewBefore).Unix(),
	}), nil
}

func (p *fakePanel) Renew(ctx context.Context, req *connect.Request[pb.RenewRequest]) (*connect.Response[pb.RenewResponse], error) {
	p.mu.Lock()
	p.renewCalls++
	p.certValidity = 30 * 24 * time.Hour // a renewed certificate has the full term
	p.mu.Unlock()
	certPEM, err := p.issue(req.Msg.CsrDer)
	if err != nil {
		return nil, err
	}
	na := time.Now().Add(30 * 24 * time.Hour)
	return connect.NewResponse(&pb.RenewResponse{CertificatePem: certPEM, NotAfterUnix: na.Unix(), RenewAfterUnix: na.Add(-renewBefore).Unix()}), nil
}

func (p *fakePanel) Connect(ctx context.Context, stream *connect.BidiStream[pb.ConnectRequest, pb.ConnectResponse]) error {
	if p.blackhole.Load() {
		<-ctx.Done()
		return nil
	}
	first, err := stream.Receive()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("first message must be Hello"))
	}
	c := &panelConn{hello: hello, out: make(chan *pb.ConnectResponse, 64), kill: make(chan struct{})}

	p.mu.Lock()
	if len(p.conns) > 0 {
		p.conns[len(p.conns)-1].drop() // a newer stream supersedes the older one
	}
	p.conns = append(p.conns, c)
	acked := p.lastSeq[hello.InstanceId]
	p.mu.Unlock()
	if o := p.ackedOverride.Load(); o >= 0 {
		acked = uint64(o)
	}
	if err := stream.Send(&pb.ConnectResponse{Message: &pb.ConnectResponse_HelloAck{HelloAck: &pb.HelloAck{
		AckedSeq: acked, ServerTimeUnix: time.Now().Unix() + p.serverSkew.Load(), Settings: p.settings.Load(), LinkSupported: p.linkSupported.Load(),
	}}}); err != nil {
		return err
	}
	p.hellos <- hello
	p.connects <- struct{}{}

	done := make(chan struct{})
	defer close(done)
	msgs := make(chan *pb.ConnectRequest)
	go func() {
		for {
			m, err := stream.Receive()
			if err != nil {
				close(msgs)
				return
			}
			select {
			case msgs <- m:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return nil
			}
			if err := p.handle(stream, c, m); err != nil {
				return err
			}
		case r := <-c.out:
			if err := stream.Send(r); err != nil {
				return err
			}
		case <-c.kill:
			return connect.NewError(connect.CodeAborted, errors.New("superseded"))
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *fakePanel) handle(stream *connect.BidiStream[pb.ConnectRequest, pb.ConnectResponse], c *panelConn, m *pb.ConnectRequest) error {
	switch msg := m.Message.(type) {
	case *pb.ConnectRequest_Stats, *pb.ConnectRequest_Event:
		p.mu.Lock()
		c.seqs = append(c.seqs, m.Seq)
		inst := c.hello.InstanceId
		if m.Seq <= p.lastSeq[inst] {
			p.dups++
		} else {
			p.lastSeq[inst] = m.Seq
			p.committed = append(p.committed, m.Seq)
			if st := m.GetStats(); st != nil {
				for _, tr := range st.Traffic {
					p.up[tr.CredId] += tr.BytesUp
					p.down[tr.CredId] += tr.BytesDown
				}
				p.sessions = st.Sessions
				p.lastStats = st
			} else {
				p.events = append(p.events, m.GetEvent())
			}
		}
		last := p.lastSeq[inst]
		p.mu.Unlock()
		if !p.dropAcks.Load() {
			return stream.Send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Ack{Ack: &pb.Ack{UpToSeq: last}}})
		}
	case *pb.ConnectRequest_ApplyResult:
		p.applies <- msg.ApplyResult
	case *pb.ConnectRequest_CommandResult:
		p.cmdsSeen.Add(1)
		p.cmds <- msg.CommandResult
	case *pb.ConnectRequest_LogChunk:
		p.logs <- msg.LogChunk
	case *pb.ConnectRequest_DoctorReport:
		if m.Seq != 0 {
			p.badSeq.Add(1)
		}
		select {
		case p.doctors <- msg.DoctorReport:
		default: // nobody is listening: drop, like the panel would a report it cannot use
		}
	}
	return nil
}

// ---- test-side controls ----

func (p *fakePanel) current() *panelConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.conns) == 0 {
		return nil
	}
	return p.conns[len(p.conns)-1]
}

// send pushes a message to the newest stream.
func (p *fakePanel) send(m *pb.ConnectResponse) {
	p.t.Helper()
	eventually(p.t, func() bool { return p.current() != nil }, "no agent stream")
	p.current().out <- m
}

func (p *fakePanel) push(ds *pb.DesiredState) {
	p.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_DesiredState{DesiredState: ds}})
}

func (p *fakePanel) dropConn() { p.current().drop() }

func (p *fakePanel) connCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *fakePanel) committedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.committed)
}

func (p *fakePanel) eventCodes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.events {
		out = append(out, e.Code)
	}
	return out
}

func (p *fakePanel) hasEvent(code string) bool {
	for _, c := range p.eventCodes() {
		if c == code {
			return true
		}
	}
	return false
}

func (p *fakePanel) nextApply() *pb.ApplyResult {
	p.t.Helper()
	select {
	case r := <-p.applies:
		return r
	case <-time.After(5 * time.Second):
		p.t.Fatal("no ApplyResult")
		return nil
	}
}

func (p *fakePanel) nextCmd() *pb.CommandResult {
	p.t.Helper()
	select {
	case r := <-p.cmds:
		return r
	case <-time.After(5 * time.Second):
		p.t.Fatal("no CommandResult")
		return nil
	}
}

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// statsSeen returns the newest committed StatsBatch (nil before the first).
func (p *fakePanel) statsSeen() *pb.StatsBatch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastStats
}
