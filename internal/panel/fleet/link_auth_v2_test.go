package fleet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/agentlink"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/proto"
)

const linkV2Audience = "panel.example.com"

// linkAuthFrame is an agent's LinkAuth: signed with v2 (covers agentNonce) or v1 (does not).
func linkAuthFrame(t *testing.T, a *agent, v2 bool, audience string, panelNonce, agentNonce []byte) *agentv1.LinkAuth {
	t.Helper()
	digest := agentlink.AgentDigest(a.nodeID, audience, panelNonce)
	if v2 {
		digest = agentlink.AgentDigestV2(a.nodeID, audience, panelNonce, agentNonce)
	}
	sig, err := agentlink.Sign(a.key, digest)
	if err != nil {
		t.Fatal(err)
	}
	return &agentv1.LinkAuth{NodeId: a.nodeID, Nonce: agentNonce, Signature: sig}
}

func acceptLink(t *testing.T, e *env, a *agent, auth *agentv1.LinkAuth, panelNonce []byte) (frame, keys []byte, ok bool) {
	t.Helper()
	b, err := proto.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	_, frame, keys, ok = e.f.linkAccept(e.ctx, a.nodeID, linkV2Audience, panelNonce, b)
	return frame, keys, ok
}

func plainNonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 32)
	if _, err := rand.Read(n); err != nil {
		t.Fatal(err)
	}
	if agentlink.WantsSeal(n) {
		n[0] ^= 0xff
	}
	return n
}

func eventCodes(t *testing.T, e *env, nodeID string) string {
	t.Helper()
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: nodeID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range rows {
		codes = append(codes, r.Code)
	}
	return strings.Join(codes, ",")
}

// A new agent signs v2 and asks for sealing: sealed keys on both ends, the node is marked.
func TestLinkAcceptV2SealsAndMarks(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	panelNonce, _, err := LinkChallenge(linkV2Audience)
	if err != nil {
		t.Fatal(err)
	}
	sealNonce, err := agentlink.SealNonce()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.st.Node(e.ctx, a.nodeID); err != nil || n.LinkSealed {
		t.Fatalf("a fresh node is marked: %v %v", n.LinkSealed, err)
	}
	frame, keys, ok := acceptLink(t, e, a, linkAuthFrame(t, a, true, linkV2Audience, panelNonce, sealNonce), panelNonce)
	if !ok || keys == nil {
		t.Fatalf("v2 proof with the seal marker: ok=%v sealed=%v", ok, keys != nil)
	}
	var accepted agentv1.LinkAccept
	caKey := e.f.ca.cert.PublicKey.(*ecdsa.PublicKey)
	if proto.Unmarshal(frame, &accepted) != nil || !agentlink.Verify(caKey, agentlink.SealedPanelDigest(a.nodeID, linkV2Audience, panelNonce, sealNonce), accepted.Signature) {
		t.Fatal("the accept is not the sealed one")
	}
	if agentKeys, err := agentlink.SealKeys(a.key, caKey, a.nodeID, linkV2Audience, panelNonce, sealNonce); err != nil || !bytes.Equal(agentKeys, keys) {
		t.Fatalf("the agent derives other keys: %v", err)
	}
	// The mark shows on the node row (and from it on the node card).
	n, err := e.st.Node(e.ctx, a.nodeID)
	if err != nil || !n.LinkSealed {
		t.Fatalf("node row: sealed=%v err=%v", n.LinkSealed, err)
	}

	// A v2 proof that does not ask for sealing is not a thing a new agent sends.
	if _, _, ok := acceptLink(t, e, a, linkAuthFrame(t, a, true, linkV2Audience, panelNonce, plainNonce(t)), panelNonce); ok {
		t.Error("a v2 proof without the seal marker was accepted")
	}
	// The nonce is part of what v2 signs: any other nonce, with or without the marker, breaks the proof.
	other, err := agentlink.SealNonce()
	if err != nil {
		t.Fatal(err)
	}
	for name, nonce := range map[string][]byte{"another sealing nonce": other, "marker taken out": plainNonce(t)} {
		forged := linkAuthFrame(t, a, true, linkV2Audience, panelNonce, sealNonce)
		forged.Nonce = nonce
		if _, _, ok := acceptLink(t, e, a, forged, panelNonce); ok {
			t.Errorf("%s: the changed v2 proof was accepted", name)
		}
	}
}

// An agent that signs v1: with the marker it gets a sealed session and the mark; without it a plain session only while
// the node was never marked.
func TestLinkAcceptV1PlainUntilTheNodeSealed(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	b := e.enroll("nl1")
	panelNonce, _, _ := LinkChallenge(linkV2Audience)

	// Never sealed: the plain v1 proof of an agent that cannot seal still works, and does not mark the node.
	if _, keys, ok := acceptLink(t, e, a, linkAuthFrame(t, a, false, linkV2Audience, panelNonce, plainNonce(t)), panelNonce); !ok || keys != nil {
		t.Fatalf("a plain v1 proof of an unmarked node: ok=%v sealed=%v", ok, keys != nil)
	}
	if n, _ := e.st.Node(e.ctx, a.nodeID); n.LinkSealed {
		t.Fatal("a plain session marked the node")
	}

	// The same agent (v0.1.66+) with the marker: sealed, and from now on the node is marked.
	sealNonce, _ := agentlink.SealNonce()
	if _, keys, ok := acceptLink(t, e, a, linkAuthFrame(t, a, false, linkV2Audience, panelNonce, sealNonce), panelNonce); !ok || keys == nil {
		t.Fatalf("a v1 proof with the marker: ok=%v sealed=%v", ok, keys != nil)
	}
	if _, _, ok := acceptLink(t, e, a, linkAuthFrame(t, a, false, linkV2Audience, panelNonce, plainNonce(t)), panelNonce); ok {
		t.Fatal("a plain session was opened for a node that sealed before")
	}
	if got := eventCodes(t, e, a.nodeID); !strings.Contains(got, "link_downgrade_refused") {
		t.Errorf("no link_downgrade_refused event, events: %q", got)
	}
	// Another node is not affected.
	if _, keys, ok := acceptLink(t, e, b, linkAuthFrame(t, b, false, linkV2Audience, panelNonce, plainNonce(t)), panelNonce); !ok || keys != nil {
		t.Fatalf("an unmarked node was refused a plain session: ok=%v", ok)
	}

	// Re-enrolling the node (the owner's way back for an agent that cannot seal) clears the mark.
	resp, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(&adminv1.CreateEnrollmentRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.Fields(resp.Msg.InstallCommand[strings.Index(resp.Msg.InstallCommand, "--token ")+8:])[0]
	key, csr := newCSR(t)
	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)
	er, err := cli.Enroll(e.ctx, connect.NewRequest(&agentv1.EnrollRequest{EnrollmentToken: tok, CsrDer: csr, ApiVersion: APIVersion}))
	if err != nil {
		t.Fatal(err)
	}
	again := e.identity(a.nodeID, key, er.Msg.CertificatePem)
	if _, keys, ok := acceptLink(t, e, again, linkAuthFrame(t, again, false, linkV2Audience, panelNonce, plainNonce(t)), panelNonce); !ok || keys != nil {
		t.Fatalf("after re-enrolment a plain session is still refused: ok=%v", ok)
	}
}

// A party that terminates the link's TLS holds no node key. It relays the agent's proof and replaces the agent's nonce
// with one without the seal marker, to get a plain session as the node (and from it the node's certificate).
func TestLinkMiddleCannotStripSealing(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		name, node string
		v2         bool
		// priorSeal: the node already opened a sealed session (an agent of v0.1.66-v0.1.87 signs v1 and always seals).
		priorSeal bool
	}{
		{"a new agent, first connection", "de1", true, false},
		{"an older agent whose node sealed before", "nl1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := e.enroll(tc.node)
			if tc.priorSeal {
				pn, _, _ := LinkChallenge(linkV2Audience)
				sealNonce, _ := agentlink.SealNonce()
				if _, _, ok := acceptLink(t, e, a, linkAuthFrame(t, a, false, linkV2Audience, pn, sealNonce), pn); !ok {
					t.Fatal("the first sealed session was refused")
				}
			}
			ws, challenge := dialPanelLink(t, server.URL, a.nodeID)
			t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
			sealNonce, err := agentlink.SealNonce()
			if err != nil {
				t.Fatal(err)
			}
			real := linkAuthFrame(t, a, tc.v2, challenge.Audience, challenge.Nonce, sealNonce)
			forged := &agentv1.LinkAuth{NodeId: real.NodeId, Nonce: bytes.Repeat([]byte{0x41}, 32), Signature: real.Signature}
			if err := agentlink.WriteFrame(context.Background(), ws, forged); err != nil {
				t.Fatal(err)
			}
			assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
			if e.f.session(a.nodeID) != nil {
				t.Fatal("the party in the middle got a session")
			}
		})
	}
}
