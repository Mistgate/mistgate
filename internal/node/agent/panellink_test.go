package agent

import (
	"bytes"
	"cmp"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
)

// sessionLinkPanel is a panel's link endpoint that runs the whole v2 handshake (sealed) and answers the Hello with a
// HelloAck. It records the audience (the Host) of every session that got as far as the Hello.
type sessionLinkPanel struct {
	h        *harness
	hellos   chan string
	settings *pb.NodeSettings // the HelloAck's; nil = empty
}

func newSessionLinkPanel(h *harness) *sessionLinkPanel {
	return &sessionLinkPanel{h: h, hellos: make(chan string, 8)}
}

func (p *sessionLinkPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()
	audience := r.Host
	panelNonce := make([]byte, 32)
	_, _ = rand.Read(panelNonce)
	if agentlink.WriteFrame(ctx, ws, &pb.LinkChallenge{Nonce: panelNonce, Audience: audience}) != nil {
		return
	}
	_, frame, err := agentlink.ReadFrame(ctx, ws)
	var auth pb.LinkAuth
	if err != nil || proto.Unmarshal(frame, &auth) != nil {
		return
	}
	pub := &p.h.a.id.Load().cert.PrivateKey.(*ecdsa.PrivateKey).PublicKey
	if !agentlink.Verify(pub, agentlink.AgentDigestV2(auth.NodeId, audience, panelNonce, auth.Nonce), auth.Signature) {
		return
	}
	keys, err := agentlink.SealKeys(p.h.panel.caKey, pub, auth.NodeId, audience, panelNonce, auth.Nonce)
	if err != nil {
		return
	}
	sig, err := agentlink.Sign(p.h.panel.caKey, agentlink.SealedPanelDigest(auth.NodeId, audience, panelNonce, auth.Nonce))
	if err != nil || agentlink.WriteFrame(ctx, ws, &pb.LinkAccept{Signature: sig}) != nil {
		return
	}
	link := agentlink.NewLink(ws, keys, false)
	_, frame, err = link.Read(ctx)
	var req pb.ConnectRequest
	if err != nil || proto.Unmarshal(frame, &req) != nil || req.GetHello() == nil {
		return
	}
	p.hellos <- audience
	if link.Write(ctx, &pb.ConnectResponse{Message: &pb.ConnectResponse_HelloAck{HelloAck: &pb.HelloAck{
		ServerTimeUnix: time.Now().Unix(), Settings: cmp.Or(p.settings, &pb.NodeSettings{}),
	}}}) != nil {
		return
	}
	for {
		if _, _, err := link.Read(ctx); err != nil {
			return
		}
	}
}

func (p *sessionLinkPanel) hello(t *testing.T) string {
	t.Helper()
	select {
	case got := <-p.hellos:
		return got
	case <-time.After(8 * time.Second):
		t.Fatal("no Hello reached the link panel")
		return ""
	}
}

func linkBaseOf(server *httptest.Server, c byte) string {
	return "wss://" + strings.TrimPrefix(server.URL, "https://") + "/" + strings.Repeat(string(c), 24) + "/"
}

func readMeta(t *testing.T, dir string) Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestApplyPanelLink(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	oldLink := "wss://old.example.com/" + strings.Repeat("o", 24) + "/"
	newLink := "wss://panel.example.com/" + strings.Repeat("n", 24) + "/"
	a.metaMu.Lock()
	a.meta.Link = oldLink
	a.metaMu.Unlock()

	if a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: "https://panel.example.com/short"}) || a.nextLink() != "" {
		t.Fatal("a bad link address was taken")
	}
	if a.applyPanelLink(&pb.NodeSettings{}) || a.nextLink() != "" {
		t.Fatal("an empty link address was taken")
	}
	if !a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: newLink}) || a.nextLink() != newLink || readMeta(t, h.dir).NextLink != newLink {
		t.Fatal("a new link address: no reconnect, or not saved")
	}
	if a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: newLink}) {
		t.Fatal("the same new address asked for a second reconnect")
	}
	if a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: oldLink}) || a.nextLink() != "" || readMeta(t, h.dir).NextLink != "" {
		t.Fatal("the current address did not clear the new one")
	}
}

// An mTLS agent (no link in agent.json) that gets settings without a link address stays as it is: nothing is written,
// it does not become link-only and has no new address to try.
func TestMTLSAgentStaysMTLSWithoutANewLink(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	if a.linkOnly() {
		t.Fatal("harness agent is link-only")
	}
	before, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if a.applyPanelLink(&pb.NodeSettings{}) {
		t.Fatal("settings without a link address asked for a reconnect")
	}
	after, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("%s changed:\n%s\n%s", fileMeta, before, after)
	}
	if a.linkOnly() || a.nextLink() != "" {
		t.Fatalf("linkOnly=%v nextLink=%q", a.linkOnly(), a.nextLink())
	}
}

// A new link address that fails is tried every other turn, the old way to the panel in between.
func TestNextLinkAlternatesWithTheOldTransport(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	var hits atomic.Int32
	bad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(bad.Close)
	h.a.linkHTTPClient = bad.Client()
	h.a.meta.NextLink = linkBaseOf(bad, 'b')
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.start()

	waitConnect := func() {
		select {
		case <-h.panel.connects:
		case <-time.After(5 * time.Second):
			t.Fatal("no mTLS session after the failed new address")
		}
	}
	waitConnect()
	if hits.Load() != 1 {
		t.Fatalf("new address tried %d times before the old one, want 1", hits.Load())
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "mTLS session")
	h.a.cur.Load().cancel()
	waitConnect()
	if hits.Load() != 2 {
		t.Fatalf("new address tried %d times, want 2", hits.Load())
	}
	if h.a.nextLink() == "" || h.a.linkOnly() {
		t.Fatal("a failed new address was committed")
	}
}

// The panel sends a new link address in the HelloAck: the agent leaves the mTLS session, opens one over the address and
// keeps it as its link from then on.
func TestNextLinkCommitsAfterASession(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	lp := newSessionLinkPanel(h)
	server := httptest.NewTLSServer(lp)
	t.Cleanup(server.Close)
	next := linkBaseOf(server, 'n')
	h.a.linkHTTPClient = server.Client()
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.panel.settings.Store(&pb.NodeSettings{PanelLinkUrl: next})
	h.start()

	if got := lp.hello(t); got != strings.TrimPrefix(server.URL, "https://") {
		t.Fatalf("audience %q", got)
	}
	eventually(t, func() bool { return h.a.linkOnly() && h.a.nextLink() == "" && h.a.linkURL() == next }, "the new address committed")
	if m := readMeta(t, h.dir); m.Link != next || m.NextLink != "" {
		t.Fatalf("saved link=%q next=%q", m.Link, m.NextLink)
	}
}

func TestSetPanelLink(t *testing.T) {
	dir := t.TempDir()
	link := "wss://panel.example.com/" + strings.Repeat("s", 24) + "/"
	if err := SetPanelLink(dir, link); err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("not enrolled: %v", err)
	}
	h := newHarness(t, harnessOpts{noStart: true})
	if err := SetPanelLink(h.dir, "wss://panel.example.com/x"); err == nil {
		t.Fatal("a bad address was saved")
	}
	if err := SetPanelLink(h.dir, link); err != nil {
		t.Fatal(err)
	}
	if m := readMeta(t, h.dir); m.NextLink != link || m.Panel == "" {
		t.Fatalf("saved %+v", m)
	}
}
