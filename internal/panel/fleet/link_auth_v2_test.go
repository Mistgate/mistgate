package fleet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"net/http/httptest"
	"os"
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

// markSealingAgents runs the marking statement of migration 00134 on the test database, as the panel update does for the
// nodes it already has.
func markSealingAgents(t *testing.T, e *env) {
	t.Helper()
	b, err := os.ReadFile("../store/migrations/00134_node_link_sealed.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	from, to := strings.Index(sql, "UPDATE node SET link_sealed"), strings.Index(sql, "-- +goose Down")
	if from < 0 || to < from {
		t.Fatal("the marking statement is not in the migration")
	}
	e.exec(sql[from:to])
}

// After the panel update, a node that runs a sealing agent (v0.1.66 and later) is already marked, though it has not
// reconnected yet: the proof a party in the middle forwards with the marker taken out is refused, with the event. A node
// that really runs an older agent stays unmarked and connects plain, as before.
func TestLinkMarkAfterPanelUpdate(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)
	current := e.enroll("de1")
	old := e.enroll("nl1")
	e.exec(`UPDATE node SET agent_version = 'v0.1.87' WHERE id = ?`, current.nodeID)
	e.exec(`UPDATE node SET agent_version = 'v0.1.65' WHERE id = ?`, old.nodeID)
	markSealingAgents(t, e)
	if n, _ := e.st.Node(e.ctx, current.nodeID); !n.LinkSealed {
		t.Fatal("the node with a v0.1.87 agent is not marked")
	}
	if n, _ := e.st.Node(e.ctx, old.nodeID); n.LinkSealed {
		t.Fatal("the node with a v0.1.65 agent is marked")
	}

	// The agent's proof (v1, with the marker) goes through a party in the middle, which sends it with another nonce.
	ws, challenge := dialPanelLink(t, server.URL, current.nodeID)
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	sealNonce, err := agentlink.SealNonce()
	if err != nil {
		t.Fatal(err)
	}
	real := linkAuthFrame(t, current, false, challenge.Audience, challenge.Nonce, sealNonce)
	forged := &agentv1.LinkAuth{NodeId: real.NodeId, Nonce: plainNonce(t), Signature: real.Signature}
	if err := agentlink.WriteFrame(context.Background(), ws, forged); err != nil {
		t.Fatal(err)
	}
	assertCloseStatus(t, ws, websocket.StatusPolicyViolation)
	if e.f.session(current.nodeID) != nil {
		t.Fatal("the party in the middle got a session")
	}
	if got := eventCodes(t, e, current.nodeID); !strings.Contains(got, "link_downgrade_refused") {
		t.Errorf("no link_downgrade_refused event, events: %q", got)
	}

	// The node on the old agent connects plain, and stays unmarked.
	pn, _, _ := LinkChallenge(linkV2Audience)
	if _, keys, ok := acceptLink(t, e, old, linkAuthFrame(t, old, false, linkV2Audience, pn, plainNonce(t)), pn); !ok || keys != nil {
		t.Fatalf("a node on an agent older than v0.1.66 was refused a plain session: ok=%v", ok)
	}
	if n, _ := e.st.Node(e.ctx, old.nodeID); n.LinkSealed {
		t.Fatal("a plain session marked the node")
	}
}
