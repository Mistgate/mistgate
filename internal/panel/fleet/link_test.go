package fleet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/agentlink"
	"google.golang.org/protobuf/proto"
)

func dialPanelLink(t *testing.T, serverURL, nodeID string) (*websocket.Conn, *agentv1.LinkChallenge) {
	t.Helper()
	ws, _, err := websocket.Dial(context.Background(), serverURL+"/link/"+nodeID, nil)
	if err != nil {
		t.Fatal(err)
	}
	typ, b, err := agentlink.ReadFrame(context.Background(), ws)
	if err != nil || typ != websocket.MessageBinary {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		t.Fatalf("read challenge: type=%v err=%v", typ, err)
	}
	var challenge agentv1.LinkChallenge
	if err := proto.Unmarshal(b, &challenge); err != nil {
		t.Fatal(err)
	}
	return ws, &challenge
}

func openPanelLink(t *testing.T, serverURL string, a *agent) *websocket.Conn {
	t.Helper()
	ws, challenge := dialPanelLink(t, serverURL, a.nodeID)
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	sig, err := agentlink.Sign(a.key, agentlink.AgentDigest(a.nodeID, challenge.Audience, challenge.Nonce))
	if err != nil {
		t.Fatal(err)
	}
	if err := agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkAuth{NodeId: a.nodeID, Nonce: nonce[:], Signature: sig}); err != nil {
		t.Fatal(err)
	}
	typ, b, err := agentlink.ReadFrame(context.Background(), ws)
	if err != nil || typ != websocket.MessageBinary {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		t.Fatalf("read LinkAccept: type=%v err=%v", typ, err)
	}
	var accepted agentv1.LinkAccept
	if err := proto.Unmarshal(b, &accepted); err != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		t.Fatalf("invalid LinkAccept: %v", err)
	}
	caKey, ok := a.e.f.ca.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !agentlink.Verify(caKey, agentlink.PanelDigest(a.nodeID, challenge.Audience, challenge.Nonce, nonce[:]), accepted.Signature) {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		t.Fatal("LinkAccept signature did not verify with the panel CA")
	}
	return ws
}

func linkHello(t *testing.T, ws *websocket.Conn) *agentv1.HelloAck {
	t.Helper()
	if err := agentlink.WriteFrame(context.Background(), ws, hello("link-test", 0, "")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, b, err := agentlink.ReadFrame(ctx, ws)
		if err != nil {
			t.Fatalf("read session response: %v", err)
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("session response frame type %v", typ)
		}
		var response agentv1.ConnectResponse
		if err := proto.Unmarshal(b, &response); err != nil {
			t.Fatal(err)
		}
		if h := response.GetHelloAck(); h != nil {
			return h
		}
	}
}

func TestLinkHandshakeRunsTheAgentSession(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	a := e.enroll("link-session")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	ws := openPanelLink(t, server.URL, a)
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	ack := linkHello(t, ws)
	if !ack.LinkSupported {
		t.Fatal("HelloAck did not advertise link support")
	}
}

func TestHelloAckDoesNotAdvertiseAnUnmountedLink(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = false
	a := e.enroll("link-unmounted")
	mtls := a.open()
	mtls.send(0, hello("unmounted-link", 0, ""))
	ack := mtls.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil }).GetHelloAck()
	if ack.LinkSupported {
		t.Fatal("HelloAck advertised link support without a mounted link handler")
	}
}

func TestLinkAndConnectShareOneOwner(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	a := e.enroll("link-owner")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	first := openPanelLink(t, server.URL, a)
	linkHello(t, first)
	second := openPanelLink(t, server.URL, a)
	linkHello(t, second)
	if status, err := readUntilLinkClose(t, first); status != websocket.StatusCode(4000) {
		t.Fatalf("first link close status = %v, err=%v", status, err)
	}

	mtls := a.open()
	mtls.send(0, hello("mtls-newer", 0, ""))
	mtls.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if status, err := readUntilLinkClose(t, second); status != websocket.StatusCode(4000) {
		t.Fatalf("link close after newer mTLS stream = %v, err=%v", status, err)
	}

	third := openPanelLink(t, server.URL, a)
	linkHello(t, third)
	if err := mtls.ended(); code(err) != connect.CodeAborted {
		t.Fatalf("mTLS stream did not yield to newer link: %v", err)
	}
	stale := openPanelLink(t, server.URL, a) // authenticated first, but delays Hello
	newest := openPanelLink(t, server.URL, a)
	linkHello(t, newest)
	if err := agentlink.WriteFrame(context.Background(), stale, hello("stale-link", 0, "")); err != nil {
		t.Fatal(err)
	}
	if status, err := readUntilLinkClose(t, stale); status != websocket.StatusCode(4000) {
		t.Fatalf("earlier authenticated link reclaimed ownership: close=%v err=%v", status, err)
	}
	_ = third.Close(websocket.StatusNormalClosure, "")
	_ = newest.Close(websocket.StatusNormalClosure, "")
}

func TestNewAuthenticatedLinkClosesPendingHello(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	a := e.enroll("link-pending-hello")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	first := openPanelLink(t, server.URL, a)
	defer first.Close(websocket.StatusNormalClosure, "")
	second := openPanelLink(t, server.URL, a)
	defer second.Close(websocket.StatusNormalClosure, "")
	if status, err := readUntilLinkClose(t, first); status != websocket.StatusCode(4000) {
		t.Fatalf("pending first link close status = %v, err=%v", status, err)
	}
	linkHello(t, second)
}

func readUntilLinkClose(t *testing.T, ws *websocket.Conn) (websocket.StatusCode, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := ws.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err), err
		}
	}
}

func TestLinkHandshakeRefusals(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	a := e.enroll("link-refusal")
	other := e.enroll("link-wrong-key")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	t.Run("wrong node key", func(t *testing.T) {
		assertLinkRefused(t, e, server.URL, a.nodeID, other, func(ch *agentv1.LinkChallenge) (string, []byte) {
			return ch.Audience, ch.Nonce
		})
	})
	t.Run("signature over another nonce", func(t *testing.T) {
		assertLinkRefused(t, e, server.URL, a.nodeID, a, func(ch *agentv1.LinkChallenge) (string, []byte) {
			nonce := append([]byte(nil), ch.Nonce...)
			nonce[0] ^= 1
			return ch.Audience, nonce
		})
	})
	t.Run("signature over another audience", func(t *testing.T) {
		assertLinkRefused(t, e, server.URL, a.nodeID, a, func(ch *agentv1.LinkChallenge) (string, []byte) {
			return ch.Audience + ".invalid", ch.Nonce
		})
	})
	t.Run("retired node", func(t *testing.T) {
		retired := e.enroll("link-retired")
		if err := e.st.RetireNode(e.ctx, retired.nodeID, e.f.now().UTC()); err != nil {
			t.Fatal(err)
		}
		assertLinkRefused(t, e, server.URL, retired.nodeID, retired, func(ch *agentv1.LinkChallenge) (string, []byte) {
			return ch.Audience, ch.Nonce
		})
	})
	t.Run("unknown node", func(t *testing.T) {
		assertLinkRefused(t, e, server.URL, "node_unknown", a, func(ch *agentv1.LinkChallenge) (string, []byte) {
			return ch.Audience, ch.Nonce
		})
	})
	t.Run("text frame", func(t *testing.T) {
		ws, _ := dialPanelLink(t, server.URL, a.nodeID)
		defer ws.Close(websocket.StatusNormalClosure, "")
		if err := ws.Write(context.Background(), websocket.MessageText, []byte("no")); err != nil {
			t.Fatal(err)
		}
		assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
	})
	t.Run("oversized frame", func(t *testing.T) {
		ws, _ := dialPanelLink(t, server.URL, a.nodeID)
		defer ws.Close(websocket.StatusNormalClosure, "")
		if err := ws.Write(context.Background(), websocket.MessageBinary, make([]byte, agentlink.MaxFrameSize+1)); err != nil {
			t.Fatal(err)
		}
		assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
	})
	t.Run("handshake silence deadline", func(t *testing.T) {
		e.f.linkHandshakeTimeout = 20 * time.Millisecond
		ws, _ := dialPanelLink(t, server.URL, a.nodeID)
		defer ws.Close(websocket.StatusNormalClosure, "")
		assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
	})
}

func TestLinkAcceptsOldAndNewCertificatesDuringRenewalGrace(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	a := e.enroll("link-renewal")
	key, csr := newCSR(t)
	client := agentv1connect.NewEnrollmentServiceClient(e.httpClient(&a.cert, testSNI), e.srv.URL)
	renewed, err := client.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr}))
	if err != nil {
		t.Fatal(err)
	}
	newAgent := e.identity(a.nodeID, key, renewed.Msg.CertificatePem)
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	oldLink := openPanelLink(t, server.URL, a)
	linkHello(t, oldLink)
	newLink := openPanelLink(t, server.URL, newAgent)
	linkHello(t, newLink)
	if status, err := readUntilLinkClose(t, oldLink); status != websocket.StatusCode(4000) {
		t.Fatalf("old certificate link was not accepted during renewal grace: close=%v err=%v", status, err)
	}

	base := e.f.now().UTC()
	e.f.now = func() time.Time { return base.Add(oldCertGrace + time.Second) }
	ws, challenge := dialPanelLink(t, server.URL, a.nodeID)
	defer ws.Close(websocket.StatusNormalClosure, "")
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	sig, err := agentlink.Sign(a.key, agentlink.AgentDigest(a.nodeID, challenge.Audience, challenge.Nonce))
	if err != nil {
		t.Fatal(err)
	}
	if err := agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkAuth{NodeId: a.nodeID, Nonce: nonce[:], Signature: sig}); err != nil {
		t.Fatal(err)
	}
	assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
	if e.f.session(a.nodeID) == nil {
		t.Fatal("refusing the expired renewal certificate closed the current session")
	}
	_ = newLink.Close(websocket.StatusNormalClosure, "")
}

func assertLinkRefused(t *testing.T, e *env, serverURL, routeID string, signer *agent, signed func(*agentv1.LinkChallenge) (string, []byte)) {
	t.Helper()
	ws, challenge := dialPanelLink(t, serverURL, routeID)
	defer ws.Close(websocket.StatusNormalClosure, "")
	audience, nonceToSign := signed(challenge)
	sig, err := agentlink.Sign(signer.key, agentlink.AgentDigest(routeID, audience, nonceToSign))
	if err != nil {
		t.Fatal(err)
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	if err := agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkAuth{NodeId: routeID, Nonce: nonce[:], Signature: sig}); err != nil {
		t.Fatal(err)
	}
	assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
	if e.f.session(routeID) != nil {
		t.Errorf("refused node %q acquired a session", routeID)
	}
}

func assertCloseStatus(t *testing.T, ws *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := ws.Read(ctx)
	if got := websocket.CloseStatus(err); got != want {
		t.Fatalf("close status = %v, want %v (err=%v)", got, want, err)
	}
}

func TestLinkAcceptStep(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("link-accept-step")
	other := e.enroll("link-accept-other")
	const audience = "panel.example.com"
	nonce, frame, err := LinkChallenge(audience)
	if err != nil || len(nonce) != 32 {
		t.Fatalf("LinkChallenge: nonce %d bytes, err=%v", len(nonce), err)
	}
	var challenge agentv1.LinkChallenge
	if err := proto.Unmarshal(frame, &challenge); err != nil || challenge.Audience != audience || !bytes.Equal(challenge.Nonce, nonce) {
		t.Fatalf("challenge frame = %+v, err=%v", &challenge, err)
	}
	if _, _, err := LinkChallenge(""); err == nil {
		t.Fatal("LinkChallenge accepted an empty audience")
	}
	auth := func(nodeID string, key *ecdsa.PrivateKey, agentNonce []byte) []byte {
		sig, err := agentlink.Sign(key, agentlink.AgentDigest(nodeID, audience, nonce))
		if err != nil {
			t.Fatal(err)
		}
		b, err := proto.Marshal(&agentv1.LinkAuth{NodeId: nodeID, Nonce: agentNonce, Signature: sig})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	agentNonce := bytes.Repeat([]byte{7}, 32)
	pc, accept, ok := e.f.linkAccept(e.ctx, a.nodeID, audience, nonce, auth(a.nodeID, a.key, agentNonce))
	if !ok || pc.serial != a.leaf.SerialNumber.Text(16) {
		t.Fatalf("linkAccept refused the node's own proof: ok=%v serial=%q", ok, pc.serial)
	}
	var accepted agentv1.LinkAccept
	caKey := e.f.ca.cert.PublicKey.(*ecdsa.PublicKey)
	if err := proto.Unmarshal(accept, &accepted); err != nil || !agentlink.Verify(caKey, agentlink.PanelDigest(a.nodeID, audience, nonce, agentNonce), accepted.Signature) {
		t.Fatalf("accept frame does not verify with the CA: %v", err)
	}
	for name, authFrame := range map[string][]byte{
		"wrong key":      auth(a.nodeID, other.key, agentNonce),
		"wrong node id":  auth(other.nodeID, a.key, agentNonce),
		"short nonce":    auth(a.nodeID, a.key, agentNonce[:31]),
		"long nonce":     auth(a.nodeID, a.key, append(bytes.Clone(agentNonce), 0)),
		"not a LinkAuth": {0xff, 0xff},
	} {
		if _, f, ok := e.f.linkAccept(e.ctx, a.nodeID, audience, nonce, authFrame); ok || f != nil {
			t.Errorf("%s: linkAccept accepted", name)
		}
	}
}

func TestLinkMarker(t *testing.T) {
	serve := func(method, path, upgrade string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if upgrade != "" {
			r.Header.Set("Upgrade", upgrade)
		}
		w := httptest.NewRecorder()
		LinkMarker().ServeHTTP(w, r)
		return w
	}
	if w := serve(http.MethodGet, "/link/nod_x", "websocket"); w.Code != http.StatusNoContent || w.Header().Get(LinkMarkerHeader) != "nod_x" {
		t.Fatalf("upgrade: status %d, header %q", w.Code, w.Header().Get(LinkMarkerHeader))
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"no upgrade":     serve(http.MethodGet, "/link/nod_x", ""),
		"POST":           serve(http.MethodPost, "/link/nod_x", "websocket"),
		"other path":     serve(http.MethodGet, "/other/nod_x", "websocket"),
		"nested id":      serve(http.MethodGet, "/link/a/b", "websocket"),
		"empty id":       serve(http.MethodGet, "/link/", "websocket"),
		"other protocol": serve(http.MethodGet, "/link/nod_x", "h2c"),
	} {
		if w.Code != http.StatusNotFound || w.Header().Get(LinkMarkerHeader) != "" {
			t.Errorf("%s: status %d, header %q", name, w.Code, w.Header().Get(LinkMarkerHeader))
		}
	}
}
