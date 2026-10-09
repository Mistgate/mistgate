package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Step 5c: link-only agents enrol on the link route, get the link install command on the edge, and renew on their session.
// Design: design/cloudflare-edition/AGENT-LINK.md §6.2.

const linkTestPrefix = "/" + "pfxpfxpfxpfxpfxpfxpfxpfx" // what the public listener strips

// linkEnrollServer serves LinkHandler the way the listener does: under a stripped secret prefix.
func linkEnrollServer(t *testing.T, e *env) agentv1connect.EnrollmentServiceClient {
	t.Helper()
	srv := httptest.NewServer(http.StripPrefix(linkTestPrefix, e.f.LinkHandler()))
	t.Cleanup(srv.Close)
	return agentv1connect.NewEnrollmentServiceClient(srv.Client(), srv.URL+linkTestPrefix)
}

func TestLinkEnrollRouteSharesEnrollOnBothEditions(t *testing.T) {
	for _, edition := range []string{"vps", "edge"} {
		t.Run(edition, func(t *testing.T) {
			e := newCoreEnv(t)
			if edition == "edge" {
				e.f.cfg.Remote = &testRemote{}
				e.f.cfg.LinkURL = "wss://de1.example.com" + linkTestPrefix + "/"
			}
			cli := linkEnrollServer(t, e)
			nodeID, token, _ := e.createEnrollment("node-a", "de1.example.com")
			key, csr := newCSR(t)

			resp, err := cli.Enroll(e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: token, CsrDer: csr, ApiVersion: APIVersion}))
			if err != nil {
				t.Fatalf("Enroll over the link route: %v", err)
			}
			if resp.Msg.NodeId != nodeID || resp.Msg.CaCertificatePem != e.f.CACertPEM() {
				t.Errorf("response = node %q, CA match %v", resp.Msg.NodeId, resp.Msg.CaCertificatePem == e.f.CACertPEM())
			}
			leaf := e.identity(nodeID, key, resp.Msg.CertificatePem).leaf
			if !linkCertNamesNode(leaf, nodeID) {
				t.Error("the issued certificate does not name the node")
			}
			if n := e.count(`SELECT count(*) FROM node_cert WHERE node_id = ?`, nodeID); n != 1 {
				t.Errorf("certificates after enrolment = %d", n)
			}

			// The token is one-time, and failed attempts on this route are counted by the enrol limiter.
			_, csr2 := newCSR(t)
			var last error
			for i := 0; i < 12; i++ {
				_, last = cli.Enroll(e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: token, CsrDer: csr2, ApiVersion: APIVersion}))
				if i == 0 && code(last) != connect.CodeUnauthenticated {
					t.Fatalf("a used token: %v", last)
				}
			}
			if code(last) != connect.CodeResourceExhausted {
				t.Errorf("after many failures: %v", last)
			}
		})
	}
}

func TestLinkHandlerRoutesOnlyEnrolAndLink(t *testing.T) {
	for _, edition := range []string{"vps", "edge"} {
		t.Run(edition, func(t *testing.T) {
			e := newCoreEnv(t)
			if edition == "edge" {
				e.f.cfg.Remote = &testRemote{}
			}
			h := e.f.LinkHandler()
			for _, tc := range []struct{ method, path string }{
				{http.MethodPost, agentv1connect.EnrollmentServiceRenewProcedure}, // needs a node identity: not routed
				{http.MethodPost, "/mistgate.agent.v1.AgentService/Connect"},
				{http.MethodPost, agentv1connect.EnrollmentServiceEnrollProcedure + "/"},
				{http.MethodGet, agentv1connect.EnrollmentServiceEnrollProcedure}, // Connect unary is POST only
				{http.MethodPost, "/link/nod_x"},
				{http.MethodGet, "/"},
			} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("")))
				if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s = %d, want 404 or 405 (the listener swaps both for the decoy)", tc.method, tc.path, rec.Code)
				}
			}
		})
	}
}

// The edge has no mTLS endpoint: a request that arrives without the agent SNI is a 404, Enroll included.
func TestMTLSEnrollWithoutAgentSNIIs404(t *testing.T) {
	e := newCoreEnv(t)
	for _, serverName := range []string{"", "www.example.com"} {
		req := httptest.NewRequest(http.MethodPost, agentv1connect.EnrollmentServiceEnrollProcedure, strings.NewReader(""))
		req.TLS = &tls.ConnectionState{ServerName: serverName}
		rec := httptest.NewRecorder()
		e.f.AgentHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("Enroll with SNI %q = %d, want 404", serverName, rec.Code)
		}
	}
}

func TestCreateEnrollmentInstallCommands(t *testing.T) {
	e := newCoreEnv(t)
	_, token, cmd := e.createEnrollment("node-a", "de1.example.com")
	want := "chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni " + testSNI +
		" --ca-sha256 " + e.f.CAFingerprint() + " --token " + token + " && /root/mistgate-node install"
	if cmd != want {
		t.Errorf("the VPS install command changed:\n got %q\nwant %q", cmd, want)
	}

	e.f.cfg.Remote = &testRemote{}
	e.f.cfg.LinkURL = "wss://de1.example.com" + linkTestPrefix + "/"
	_, token, cmd = e.createEnrollment("node-b", "de2.example.com")
	want = "chmod +x /root/mistgate-node && /root/mistgate-node enroll --link-url wss://de1.example.com" + linkTestPrefix + "/ --ca-sha256 " +
		e.f.CAFingerprint() + " --token " + token + " && /root/mistgate-node install"
	if cmd != want {
		t.Errorf("the link install command:\n got %q\nwant %q", cmd, want)
	}
	if strings.Contains(cmd, "--panel") || strings.Contains(cmd, "--sni") {
		t.Errorf("a link install command must not carry --panel or --sni: %q", cmd)
	}

	// On the edge the link address is what is required, not the mTLS address.
	e.f.cfg.LinkURL = ""
	_, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{Name: "node-c", Address: "de3.example.com"}))
	if code(err) != connect.CodeFailedPrecondition {
		t.Errorf("edge without a link address: %v", err)
	}
	e.f.cfg.Remote, e.f.cfg.PanelAddr = nil, ""
	_, err = nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{Name: "node-c", Address: "de3.example.com"}))
	if code(err) != connect.CodeFailedPrecondition {
		t.Errorf("VPS without a panel address: %v", err)
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// Renew over the link (ConnectRequest.renew)

func renewFrame(csr []byte) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Renew{Renew: &agentv1.RenewRequest{CsrDer: csr}}}
}

// renewRig is a core fixture whose node is really enrolled: the session presents the enrolment certificate.
type renewRig struct {
	e      *env
	core   *SessionCore
	ctx    context.Context
	state  SessionState // after Hello, PeerCertSerial = the enrolment certificate
	now    time.Time
	cli    agentv1connect.EnrollmentServiceClient
	nodeID string
	serial string
}

func newRenewRig(t *testing.T, name string) *renewRig {
	t.Helper()
	e := newCoreEnv(t)
	r := &renewRig{e: e, core: NewSessionCore(e.f), cli: linkEnrollServer(t, e), now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	var token string
	r.nodeID, token, _ = e.createEnrollment(name, name+".example.com")
	cert := r.enrolWith(token)
	r.serial = cert.SerialNumber.Text(16)
	_, ownerCtx := e.f.claimOwner(r.nodeID, e.ctx)
	t.Cleanup(func() {
		e.f.mu.Lock()
		owner := e.f.owners[r.nodeID]
		e.f.mu.Unlock()
		if owner.cancel != nil {
			owner.cancel(nil)
		}
	})
	r.ctx = ownerCtx
	state := SessionState{Version: sessionStateVersion, NodeID: r.nodeID, OwnerGeneration: 1, HelloDeadline: r.now.Add(helloTimeout),
		PeerCertSerial: r.serial, PeerCertNotAfter: cert.NotAfter}
	r.state = stepHello(t, r.core, r.ctx, state, r.now, hello("inst-renew", 0, "").GetHello()).State
	return r
}

// enrolWith exchanges a token for a certificate over the link route and returns it.
func (r *renewRig) enrolWith(token string) *x509.Certificate {
	r.e.t.Helper()
	key, csr := newCSR(r.e.t)
	resp, err := r.cli.Enroll(r.e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: token, CsrDer: csr, ApiVersion: APIVersion}))
	if err != nil {
		r.e.t.Fatal(err)
	}
	return r.e.identity(r.nodeID, key, resp.Msg.CertificatePem).leaf
}

// renew runs one renew frame on the session state the rig holds (the session keeps its serial, so it can go on).
func (r *renewRig) renew(csr []byte) testTransition {
	r.e.t.Helper()
	tr, err := coreStep(r.ctx, r.core, r.state, SessionEvent{Kind: EventAgentFrame, At: r.now.Add(time.Second), Frame: renewFrame(csr)})
	if err != nil {
		r.e.t.Fatal(err)
	}
	return tr
}

func (r *renewRig) certSerial() string {
	var s string
	if err := r.e.st.R.QueryRowContext(r.ctx, `SELECT coalesce(cert_serial, '') FROM node WHERE id = ?`, r.nodeID).Scan(&s); err != nil {
		r.e.t.Fatal(err)
	}
	return s
}

func TestSessionCoreRenewIssuesCertificateAndKeepsSessionSerial(t *testing.T) {
	r := newRenewRig(t, "renew-good")
	e := r.e
	key, csr := newCSR(t)

	got := r.renew(csr)
	if got.Close != nil {
		t.Fatalf("renew closed: %+v", got.Close)
	}
	var renewed *agentv1.RenewResponse
	for _, f := range got.Frames {
		if f := f.GetRenew(); f != nil {
			renewed = f
		}
	}
	if renewed == nil || len(got.Frames) != 1 {
		t.Fatalf("frames = %+v, want exactly one renew answer", got.Frames)
	}
	block, _ := pem.Decode([]byte(renewed.CertificatePem))
	if block == nil {
		t.Fatal("the answer holds no certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: e.f.ca.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("the renewed certificate does not chain to the CA: %v", err)
	}
	if !linkCertNamesNode(leaf, r.nodeID) || !leaf.PublicKey.(*ecdsa.PublicKey).Equal(key.Public()) {
		t.Error("the renewed certificate is not the node's, for the CSR's key")
	}
	if renewed.NotAfterUnix != leaf.NotAfter.Unix() || renewed.RenewAfterUnix >= renewed.NotAfterUnix {
		t.Errorf("answer times = %d / %d", renewed.NotAfterUnix, renewed.RenewAfterUnix)
	}
	if r.certSerial() != leaf.SerialNumber.Text(16) {
		t.Errorf("node.cert_serial = %q, want the renewed serial", r.certSerial())
	}
	if got.State.PeerCertSerial != r.serial {
		t.Errorf("the session serial moved to %q: it keeps the old one until its recheck ends it", got.State.PeerCertSerial)
	}
	// The old certificate stays valid for the grace (a lost answer is retried on it): it is scheduled, not revoked now.
	var revokedAt int64
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT coalesce(revoked_at, 0) FROM node_cert WHERE serial = ?`, r.serial).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if want := e.f.now().Add(oldCertGrace).Unix(); revokedAt < want-5 || revokedAt > want+5 {
		t.Errorf("old certificate revoked_at = %d, want about now + %v (%d)", revokedAt, oldCertGrace, want)
	}
}

func TestSessionCoreRenewBadCSRClosesInvalidArgument(t *testing.T) {
	r := newRenewRig(t, "renew-bad")
	for name, csr := range map[string][]byte{"garbage": []byte("not a csr"), "empty": nil} {
		got := r.renew(csr)
		if got.Close == nil || got.Close.Class != CloseInvalidArgument || len(got.Frames) != 0 {
			t.Errorf("%s: close %+v, frames %d, want InvalidArgument and no frame", name, got.Close, len(got.Frames))
			continue
		}
		if strings.Contains(got.Close.Reason, "not a csr") {
			t.Errorf("%s: the close reason echoes the request: %q", name, got.Close.Reason)
		}
	}
	if n := r.e.count(`SELECT count(*) FROM node_cert WHERE node_id = ?`, r.nodeID); n != 1 {
		t.Errorf("a bad CSR left %d certificates, want only the enrolment one", n)
	}
}

func TestSessionCoreRenewStoreErrorClosesInternal(t *testing.T) {
	r := newRenewRig(t, "renew-store")
	if _, err := r.e.st.W.ExecContext(r.ctx, `DROP TABLE node_cert`); err != nil {
		t.Fatal(err)
	}
	_, csr := newCSR(t)
	got := r.renew(csr)
	if got.Close == nil || got.Close.Class != CloseInternal || len(got.Frames) != 0 || got.Close.Reason != "internal error" {
		t.Errorf("close %+v, frames %d, want Internal (internal error) and no frame", got.Close, len(got.Frames))
	}
}

// A renew that was already in flight when the owner re-enrolled the node must not undo the re-enrolment.
func TestSessionCoreRenewAfterReEnrolmentIsRefused(t *testing.T) {
	r := newRenewRig(t, "renew-reenrol")
	e := r.e
	resp, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{NodeId: r.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	cmd := resp.Msg.InstallCommand
	token := strings.Fields(cmd[strings.Index(cmd, "--token ")+len("--token "):])[0]
	reEnrolled := r.enrolWith(token).SerialNumber.Text(16) // E: revokes every earlier certificate at once
	before := e.count(`SELECT count(*) FROM node_cert WHERE node_id = ?`, r.nodeID)

	_, csr := newCSR(t) // the old session still presents the old certificate
	got := r.renew(csr)
	if got.Close == nil || got.Close.Class != CloseFailedPrecondition || len(got.Frames) != 0 {
		t.Fatalf("close %+v, frames %d, want FailedPrecondition and no frame", got.Close, len(got.Frames))
	}
	if after := e.count(`SELECT count(*) FROM node_cert WHERE node_id = ?`, r.nodeID); after != before {
		t.Errorf("node_cert rows %d -> %d: a refused renewal must write nothing", before, after)
	}
	if r.certSerial() != reEnrolled {
		t.Errorf("node.cert_serial = %q, want the re-enrolment certificate %q", r.certSerial(), reEnrolled)
	}
	if n := e.count(`SELECT count(*) FROM node_cert WHERE serial = ? AND revoked_at IS NULL`, reEnrolled); n != 1 {
		t.Error("the re-enrolment certificate got a scheduled revocation")
	}
}

// A node gets renewMax certificates per hour, enrolment included; a retry on the old certificate inside the grace counts.
func TestRenewQuotaCoreAndMTLS(t *testing.T) {
	t.Run("core", func(t *testing.T) {
		r := newRenewRig(t, "renew-quota")
		for i := 1; i < store.RenewMax; i++ { // the enrolment certificate is the first of RenewMax
			_, csr := newCSR(t)
			if got := r.renew(csr); got.Close != nil {
				t.Fatalf("renewal %d refused: %+v", i, got.Close)
			}
		}
		_, csr := newCSR(t)
		got := r.renew(csr)
		if got.Close == nil || got.Close.Class != CloseFailedPrecondition || len(got.Frames) != 0 {
			t.Fatalf("renewal over the quota: close %+v, frames %d", got.Close, len(got.Frames))
		}
		// An hour later the quota is free again.
		base := r.e.f.now
		r.e.f.now = func() time.Time { return base().Add(store.RenewWindow + time.Minute) }
		if got := r.renew(csr); got.Close != nil {
			t.Errorf("renewal after the window: %+v", got.Close)
		}
	})
	t.Run("mtls", func(t *testing.T) {
		e := newEnv(t)
		a := e.enroll("nodea")
		cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(&a.cert, testSNI), e.srv.URL)
		for i := 1; i < store.RenewMax; i++ {
			_, csr := newCSR(t)
			if _, err := cli.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr})); err != nil {
				t.Fatalf("renewal %d: %v", i, err)
			}
		}
		_, csr := newCSR(t)
		if _, err := cli.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr})); code(err) != connect.CodeFailedPrecondition {
			t.Errorf("renewal over the quota: %v, want FailedPrecondition", err)
		}
	})
}
