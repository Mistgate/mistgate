package agent

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"google.golang.org/protobuf/proto"
)

// linkPanelEmu is the link handshake of a panel, in the two behaviours that matter to a new agent: one that reads the
// v2 proof, and the previous release's, which knows only v1 (and sealing). It records what the agent proved.
type linkPanelEmu struct {
	t       *testing.T
	a       *Agent
	h       *harness
	v2Panel bool
	// plainAccept: answer with the plain (unsealed) digest, as a party that cannot seal would.
	plainAccept bool
	proofs      chan linkProof
}

type linkProof struct {
	v2, v1 bool // which signature verified
	seal   bool // the nonce carried the seal marker
}

func (p *linkPanelEmu) handler(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	panelNonce := make([]byte, 32)
	panelNonce[0] = 9
	if agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: r.Host}) != nil {
		return
	}
	_, frame, err := agentlink.ReadFrame(r.Context(), ws)
	var auth agentv1.LinkAuth
	if err != nil || proto.Unmarshal(frame, &auth) != nil {
		return
	}
	pub := &p.a.id.Load().cert.PrivateKey.(*ecdsa.PrivateKey).PublicKey
	proof := linkProof{seal: agentlink.WantsSeal(auth.Nonce)}
	switch {
	case p.v2Panel && agentlink.Verify(pub, agentlink.AgentDigestV2(auth.NodeId, r.Host, panelNonce, auth.Nonce), auth.Signature):
		proof.v2 = true
	case agentlink.Verify(pub, agentlink.AgentDigest(auth.NodeId, r.Host, panelNonce), auth.Signature):
		proof.v1 = true
	default:
		p.proofs <- proof
		_ = ws.Close(websocket.StatusPolicyViolation, "")
		return
	}
	p.proofs <- proof
	digest := agentlink.PanelDigest(auth.NodeId, r.Host, panelNonce, auth.Nonce)
	if proof.seal && !p.plainAccept {
		digest = agentlink.SealedPanelDigest(auth.NodeId, r.Host, panelNonce, auth.Nonce)
	}
	sig, err := agentlink.Sign(p.h.panel.caKey, digest)
	if err != nil {
		return
	}
	_ = agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkAccept{Signature: sig})
	_, _, _ = ws.Read(r.Context()) // hold until the agent closes
}

func (p *linkPanelEmu) dial() (sealed bool, err error) {
	p.t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(p.handler))
	p.t.Cleanup(server.Close)
	p.a.cfg.LinkURL = "wss://" + strings.TrimPrefix(server.URL, "https://") + "/" + strings.Repeat("a", 24) + "/"
	p.a.linkHTTPClient = server.Client()
	stream, cleanup, err := p.a.dialLink(context.Background())
	if err != nil {
		return false, err
	}
	defer func() {
		_ = stream.Close()
		if cleanup != nil {
			cleanup()
		}
	}()
	return stream.link.Sealed(), nil
}

func (p *linkPanelEmu) proof() linkProof {
	p.t.Helper()
	select {
	case got := <-p.proofs:
		return got
	case <-time.After(3 * time.Second):
		p.t.Fatal("the panel got no proof")
		return linkProof{}
	}
}

func newLinkPanelEmu(t *testing.T, v2Panel bool) *linkPanelEmu {
	h := newHarness(t, harnessOpts{noStart: true})
	return &linkPanelEmu{t: t, a: h.a, h: h, v2Panel: v2Panel, proofs: make(chan linkProof, 8)}
}

// A new agent against a new panel: v2 proof, sealed session, and a panel that cannot seal is refused.
func TestAgentLinkV2AgainstANewPanel(t *testing.T) {
	p := newLinkPanelEmu(t, true)
	sealed, err := p.dial()
	if err != nil || !sealed {
		t.Fatalf("v2 dial: sealed=%v err=%v", sealed, err)
	}
	if got := p.proof(); !got.v2 || got.v1 || !got.seal {
		t.Fatalf("the agent proved %+v, want v2 with the seal marker", got)
	}

	// An answer signed as a plain session (a party in the middle, or a panel that cannot seal) is never taken after v2.
	p.plainAccept = true
	if _, err := p.dial(); err == nil {
		t.Fatal("a v2 agent took a plain accept")
	}
	if got := p.proof(); !got.v2 {
		t.Fatalf("the agent proved %+v, want v2", got)
	}
}

// A new agent against the previous release's panel, which reads only v1: the v2 try is refused, the next dial signs v1
// (still asking to seal) and gets the sealed session the old panel gives, and the dial after that is v2 again.
func TestAgentLinkFallsBackToV1AgainstAnOldPanel(t *testing.T) {
	p := newLinkPanelEmu(t, false)
	if _, err := p.dial(); err == nil {
		t.Fatal("an old panel took the v2 proof")
	}
	if got := p.proof(); got.v1 || got.v2 || !got.seal {
		t.Fatalf("first dial: %+v, want an unknown proof with the seal marker", got)
	}
	sealed, err := p.dial()
	if err != nil || !sealed {
		t.Fatalf("v1 fallback dial: sealed=%v err=%v", sealed, err)
	}
	if got := p.proof(); !got.v1 || !got.seal {
		t.Fatalf("fallback dial: %+v, want v1 with the seal marker", got)
	}
	if _, err := p.dial(); err == nil {
		t.Fatal("the dial after a fallback is not a v2 one")
	}
	if got := p.proof(); got.v1 {
		t.Fatalf("third dial: %+v, want v2", got)
	}
}

// A dial that fails before the proof (no challenge) does not push the agent to v1.
func TestAgentLinkV1FallbackNeedsAFailedProof(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, host string) error {
		return ws.Write(context.Background(), websocket.MessageText, []byte("not a protobuf frame"))
	}, false)
	if h.a.linkLegacy.Load() {
		t.Fatal("a handshake that failed before the proof turned the agent to v1")
	}
}
