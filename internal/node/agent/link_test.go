package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
	"google.golang.org/protobuf/proto"
)

func TestAgentLinkSessionReconnectsAndResumesReliableSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, vault.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := store.NewID("nod_")
	token := "link-integration-enrollment-token"
	tokenHash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	if _, err := st.CreateEnrollment(ctx, &store.NodeRow{ID: nodeID, Name: "Link node", Address: "example.com"}, "", tokenHash[:], "test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE node SET bandwidth_mbps = 100 WHERE id = ?`, nodeID); err != nil {
		t.Fatal(err)
	}
	desired := []statehash.Inbound{{Spec: plugin.InboundSpec{ID: "inb_link", Protocol: "fake", ProfileID: "prf_link", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 443}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "example.com"}, Egress: "direct", Settings: []byte(`{}`)},
		Creds: []plugin.UserCred{{CredID: "crd_link", UserID: "usr_link", DeviceID: "dev_link", Data: []byte(`{"key":"value"}`)}}}}
	f, err := fleet.New(st, v, nil, fleet.Config{AgentSNI: testSNI, PanelAddr: "127.0.0.1:443", Desired: func(context.Context, string) ([]statehash.Inbound, error) {
		return desired, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	linkPrefix := "/" + strings.Repeat("l", 24) + "/"
	links := make(chan int64, 8)
	var linkHits atomic.Int64
	blockReconnect := atomic.Bool{}
	releaseReconnect := make(chan struct{})
	linkHandler := f.LinkHandler()
	root := http.NewServeMux()
	root.Handle("/", f.AgentHandler())
	root.HandleFunc(linkPrefix, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, linkPrefix+"link/") {
			http.NotFound(w, r)
			return
		}
		hit := linkHits.Add(1)
		select {
		case links <- hit:
		default:
		}
		if hit > 1 && blockReconnect.Load() {
			select {
			case <-releaseReconnect:
			case <-r.Context().Done():
				return
			}
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(linkPrefix, "/"))
		linkHandler.ServeHTTP(w, r)
	})
	publicCert, publicPool := testLinkTLSCert(t)
	ts := httptest.NewUnstartedServer(root)
	ts.EnableHTTP2 = true
	ts.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if hello.ServerName == testSNI {
			return f.AgentTLSConfig(hello)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{publicCert}, NextProtos: []string{"h2", "http/1.1"}}, nil
	}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	host := strings.TrimPrefix(ts.URL, "https://")
	stateDir := filepath.Join(t.TempDir(), "state")
	meta, err := Enroll(ctx, EnrollConfig{StateDir: stateDir, Panel: host, SNI: testSNI, CASHA256: f.CAFingerprint(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	linkURL := "wss://" + host + linkPrefix
	eng := newFakeEngine("fake")
	a, err := New(Config{StateDir: stateDir, LinkURL: linkURL, DoctorEnv: testDoctorEnv(t)},
		map[string]engine.Factory{"fake": eng.factory()}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	if a.meta.NodeID != meta.NodeID {
		t.Fatalf("agent node id %q differs from enrolled id %q", a.meta.NodeID, meta.NodeID)
	}
	a.linkHTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: publicPool,
		ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}}}}
	a.backoffMin, a.backoffMax, a.statsEvery, a.sweepEvery = 20*time.Millisecond, 80*time.Millisecond, time.Hour, time.Hour
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop")
		}
	})
	// Link requests may arrive only after the initial mTLS HelloAck advertises link support.
	if hit := waitLinkHit(t, links); hit != 1 {
		t.Fatalf("first signed link request number = %d", hit)
	}
	eventually(t, func() bool { return eng.has("inb_link") }, "desired state applied over WebSocket link")
	if got := eng.Observed(); len(got) != 1 || got[0].Spec.ID != "inb_link" {
		t.Fatalf("engine observed state = %+v", got)
	}

	eventually(t, func() bool { return agentFirstUnacked(a) == 0 }, "initial reliable event acknowledged")
	next, _ := a.out.hello()
	a.event(agentv1.Severity_SEVERITY_INFO, "link_first_event", "", nil)
	firstSeq := next
	eventually(t, func() bool {
		return linkEventCount(t, st, nodeID, "link_first_event") == 1 && agentFirstUnacked(a) == 0
	}, "reliable event acknowledged over link")

	blockReconnect.Store(true)
	current := a.cur.Load()
	if current == nil {
		t.Fatal("agent has no active WebSocket session")
	}
	current.cancel()
	eventually(t, func() bool { return a.cur.Load() == nil }, "link session disconnected")
	if hit := waitLinkHit(t, links); hit != 2 {
		t.Fatalf("reconnect link request number = %d", hit)
	}
	eventually(t, func() bool { return a.cur.Load() == nil }, "reconnect paused before WebSocket upgrade")
	next, _ = a.out.hello()
	a.event(agentv1.Severity_SEVERITY_INFO, "link_resumed_event", "", nil)
	resumeSeq := next
	if _, pending := a.out.hello(); pending != resumeSeq {
		t.Fatalf("pending seq before reconnect = %d, want %d", pending, resumeSeq)
	}
	close(releaseReconnect)
	eventually(t, func() bool {
		return linkEventCount(t, st, nodeID, "link_resumed_event") == 1 && agentFirstUnacked(a) == 0
	}, "reliable event resumed and acknowledged")
	if got := linkEventSeq(t, st, nodeID, "link_first_event"); got != firstSeq {
		t.Errorf("first reliable event seq = %d, want %d", got, firstSeq)
	}
	if got := linkEventSeq(t, st, nodeID, "link_resumed_event"); got != resumeSeq {
		t.Errorf("resumed reliable event seq = %d, want %d", got, resumeSeq)
	}
}

func TestAgentRejectsLinkChallengeWithWrongAudience(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, host string) error {
		return agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkChallenge{Nonce: bytes.Repeat([]byte{7}, 32), Audience: host + ".wrong"})
	}, false)
}

func TestAgentRejectsTextFrameDuringLinkHandshake(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, _ string) error {
		return ws.Write(context.Background(), websocket.MessageText, []byte("not a protobuf frame"))
	}, false)
}

func TestAgentRejectsLinkAcceptFromDifferentPanelCA(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	otherCA, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, host string) error {
		panelNonce := bytes.Repeat([]byte{9}, 32)
		if err := agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: host}); err != nil {
			return err
		}
		typ, frame, err := agentlink.ReadFrame(context.Background(), ws)
		if err != nil || typ != websocket.MessageBinary {
			return errors.New("agent did not send binary LinkAuth")
		}
		var auth agentv1.LinkAuth
		if err := proto.Unmarshal(frame, &auth); err != nil || auth.NodeId != h.a.meta.NodeID || len(auth.Nonce) != 32 {
			return errors.New("agent sent invalid LinkAuth")
		}
		sig, err := agentlink.Sign(otherCA, agentlink.PanelDigest(auth.NodeId, host, panelNonce, auth.Nonce))
		if err != nil {
			return err
		}
		return agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkAccept{Signature: sig})
	}, true)
}

func assertAgentLinkRefusal(t *testing.T, a *Agent, sendChallenge func(*websocket.Conn, string) error, expectAuth bool) {
	t.Helper()
	type handlerResult struct {
		status websocket.StatusCode
		err    error
	}
	result := make(chan handlerResult, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			result <- handlerResult{err: err}
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		err = sendChallenge(ws, r.Host)
		if err == nil && expectAuth {
			_, _, err = agentlink.ReadFrame(r.Context(), ws)
			if err != nil {
				result <- handlerResult{status: websocket.CloseStatus(err), err: err}
				return
			}
		}
		_, _, closeErr := ws.Read(r.Context())
		result <- handlerResult{status: websocket.CloseStatus(closeErr), err: err}
	}))
	defer server.Close()
	base := "wss://" + strings.TrimPrefix(server.URL, "https://") + "/" + strings.Repeat("a", 24) + "/"
	a.cfg.LinkURL = base
	a.linkHTTPClient = server.Client()
	if stream, cleanup, err := a.dialLink(context.Background()); err == nil {
		if stream != nil {
			_ = stream.Close()
		}
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("agent accepted the untrusted link handshake")
	}
	select {
	case got := <-result:
		if got.status != websocket.StatusPolicyViolation {
			t.Fatalf("panel observed close status %v, err=%v (handler err=%v)", got.status, got.err, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fake panel did not observe the agent close")
	}
}

func agentFirstUnacked(a *Agent) uint64 {
	_, first := a.out.hello()
	return first
}

func waitLinkHit(t *testing.T, links <-chan int64) int64 {
	t.Helper()
	select {
	case hit := <-links:
		return hit
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a link request")
		return 0
	}
}

func linkEventCount(t *testing.T, st *store.Store, nodeID, code string) int {
	t.Helper()
	var count int
	if err := st.R.QueryRowContext(context.Background(), `SELECT count(*) FROM event WHERE node_id = ? AND code = ?`, nodeID, code).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func linkEventSeq(t *testing.T, st *store.Store, nodeID, code string) uint64 {
	t.Helper()
	var seq uint64
	if err := st.R.QueryRowContext(context.Background(), `SELECT src_seq FROM event WHERE node_id = ? AND code = ?`, nodeID, code).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func testLinkTLSCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "link test listener"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}
