package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// sessionLinkPanel is a panel's link endpoint that runs the whole v2 handshake (sealed) and answers the Hello with a
// HelloAck. It records the audience (the Host) and the link prefix of every session that got as far as the Hello,
// answers 404 under a refused prefix (an address that stopped answering), and closes every session under a dropped
// prefix right after its HelloAck (an address that answers and then goes silent).
type sessionLinkPanel struct {
	h                *harness
	hellos           chan string
	sessions         chan string                 // the link prefix of every session that got as far as the Hello
	settings         *pb.NodeSettings            // the HelloAck's; nil = empty
	settingsByPrefix map[string]*pb.NodeSettings // immutable per-address HelloAck settings
	beforeAck        func(prefix string)         // immutable hook for coordinating an in-flight handshake with CLI writes
	afterAck         func(prefix string)         // immutable hook for coordinating peer closure after HelloAck
	push             chan *pb.ConnectResponse    // sent on the session that is up
	applies          chan *pb.ApplyResult        // every ApplyResult the agent sent

	mu      sync.Mutex
	refused map[string]bool
	dropped map[string]bool
	via     map[string]string // the Hello.link_via_node_id of the last session per link prefix
	dials   map[string]int    // requests per link prefix
}

func newSessionLinkPanel(h *harness) *sessionLinkPanel {
	return &sessionLinkPanel{h: h, hellos: make(chan string, 8), sessions: make(chan string, 32),
		push: make(chan *pb.ConnectResponse, 8), applies: make(chan *pb.ApplyResult, 8),
		refused: map[string]bool{}, dropped: map[string]bool{}, via: map[string]string{}, dials: map[string]int{}}
}

func linkPrefix(c byte) string { return strings.Repeat(string(c), 24) }

// refuse makes the link address with prefix linkPrefix(c) stop answering.
func (p *sessionLinkPanel) refuse(c byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused[linkPrefix(c)] = true
}

// answer makes the link address with prefix linkPrefix(c) answer again.
func (p *sessionLinkPanel) answer(c byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.refused, linkPrefix(c))
}

// drop makes the link address with prefix linkPrefix(c) close every session right after its HelloAck.
func (p *sessionLinkPanel) drop(c byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropped[linkPrefix(c)] = true
}

func (p *sessionLinkPanel) dialsTo(c byte) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dials[linkPrefix(c)]
}

func (p *sessionLinkPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	p.mu.Lock()
	p.dials[prefix]++
	refused, dropped := p.refused[prefix], p.dropped[prefix]
	p.mu.Unlock()
	if refused {
		http.NotFound(w, r)
		return
	}
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
	p.mu.Lock()
	p.via[prefix] = req.GetHello().GetLinkViaNodeId()
	p.mu.Unlock()
	select {
	case p.hellos <- audience:
	default:
	}
	select {
	case p.sessions <- prefix:
	default:
	}
	if p.beforeAck != nil {
		p.beforeAck(prefix)
	}
	if link.Write(ctx, &pb.ConnectResponse{Message: &pb.ConnectResponse_HelloAck{HelloAck: &pb.HelloAck{
		ServerTimeUnix: time.Now().Unix(), Settings: cmp.Or(p.settingsByPrefix[prefix], p.settings, &pb.NodeSettings{}),
	}}}) != nil {
		return
	}
	if p.afterAck != nil {
		p.afterAck(prefix)
	}
	if dropped {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-p.push:
				if link.Write(ctx, m) != nil {
					return
				}
			}
		}
	}()
	for {
		_, frame, err := link.Read(ctx)
		if err != nil {
			return
		}
		var req pb.ConnectRequest
		if proto.Unmarshal(frame, &req) == nil && req.GetApplyResult() != nil {
			p.applies <- req.GetApplyResult()
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
		t.Fatal("a new link address: not reported fresh, or not saved")
	}
	if a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: newLink}) {
		t.Fatal("the same new address was reported fresh twice")
	}
	if a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: oldLink}) || a.nextLink() != "" || readMeta(t, h.dir).NextLink != "" {
		t.Fatal("the current address did not clear the new one")
	}
}

// An mTLS agent (no link in agent.json) ignores a panel link address, in settings or in agent.json: nothing is written,
// it does not become link-only and has no new address to try. A running one stays on its mTLS session.
func TestMTLSAgentIgnoresAPanelLink(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	link := "wss://panel.example.com/" + linkPrefix('n') + "/"
	if a.linkOnly() {
		t.Fatal("harness agent is link-only")
	}
	before, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func() {
		t.Helper()
		after, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s changed:\n%s\n%s", fileMeta, before, after)
		}
		a.metaMu.Lock()
		next := a.meta.NextLink
		a.metaMu.Unlock()
		if a.linkOnly() || a.nextLink() != "" || next != "" || a.linkURL() != "" {
			t.Fatalf("linkOnly=%v next=%v", a.linkOnly(), a.nextLink() != "" || next != "")
		}
	}
	if a.applyPanelLink(&pb.NodeSettings{}) || a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: link}) {
		t.Fatal("an mTLS agent took a panel link address")
	}
	unchanged()

	h.panel.settings.Store(&pb.NodeSettings{PanelLinkUrl: link})
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return a.cur.Load() != nil }, "mTLS session")
	s := a.cur.Load()
	ds := fullState(1)
	ds.Settings = &pb.NodeSettings{PanelLinkUrl: link}
	h.panel.push(ds)
	h.panel.nextApply()
	time.Sleep(200 * time.Millisecond)
	if a.cur.Load() != s {
		t.Fatal("the mTLS session ended for a link address")
	}
	unchanged()

	a.metaMu.Lock()
	a.meta.NextLink = link // an agent.json with next_link but no link
	a.metaMu.Unlock()
	if a.nextLink() != "" {
		t.Fatal("an mTLS agent would try a new link address")
	}
}

// A new link address that fails is tried every other turn, the current address in between.
func TestNextLinkAlternatesWithTheCurrentLink(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	lp.refuse('b')
	h.start()
	if got := lp.session(t); got != linkPrefix('a') || lp.dialsTo('b') != 1 {
		t.Fatalf("new address tried %d times before the current one, want 1", lp.dialsTo('b'))
	}
	endSession(t, h)
	if got := lp.session(t); got != linkPrefix('a') || lp.dialsTo('b') != 2 {
		t.Fatalf("new address tried %d times, want 2", lp.dialsTo('b'))
	}
	if h.a.nextLink() != b || h.a.linkURL() != a {
		t.Fatal("a failed new address was committed")
	}
}

// The panel sends a new link address in the HelloAck: the agent leaves that session at once, opens one over the new
// address and keeps it as its link from then on.
func TestNextLinkCommitsAfterASession(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.meta.NextLink = ""
	lp.settings = &pb.NodeSettings{PanelLinkUrl: b}
	h.start()
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the first session was not on the current address")
	}
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("the next session was not on the new address")
	}
	committedTo(t, h, b, a)
}

// B answers the Hello and drops the session, and the panel names B in every HelloAck and DesiredState. After the rollback
// to A the node stays on A: the address it left does not end the session again, and B is not dialled a second time.
func TestPanelLinkRollbackSurvivesPanelResend(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.meta.NextLink = ""
	h.a.resetAfter = time.Minute // no session here is healthy
	lp.settings = &pb.NodeSettings{PanelLinkUrl: b}
	lp.drop('b')
	h.start()
	for _, want := range []byte{'a', 'b', 'a'} {
		if got := lp.session(t); got != linkPrefix(want) {
			t.Fatalf("session on %q, want %q", got[:1], string(want))
		}
	}
	eventually(t, func() bool { return h.a.linkURL() == a && h.a.prevLink() == "" }, "the rollback to A")

	// The session on A is alive: it takes a DesiredState and reports the apply.
	ds := fullState(1)
	ds.Settings = &pb.NodeSettings{PanelLinkUrl: b}
	lp.push <- &pb.ConnectResponse{Message: &pb.ConnectResponse_DesiredState{DesiredState: ds}}
	select {
	case r := <-lp.applies:
		if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Revision != 1 {
			t.Fatalf("apply result %v revision %d", r.Status, r.Revision)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no ApplyResult: the session on the current address did not stay up")
	}
	if h.a.linkURL() != a || lp.dialsTo('b') != 1 || lp.dialsTo('a') != 2 || len(lp.sessions) != 0 {
		t.Fatalf("link %v, dials a=%d b=%d, extra sessions %d", h.a.linkURL() == a, lp.dialsTo('a'), lp.dialsTo('b'), len(lp.sessions))
	}
}

// A new link address in a DesiredState is saved and tried at the next reconnect: the session it came in reports the
// apply and goes on.
func TestNextLinkFromADesiredStateWaitsForTheNextSession(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.meta.NextLink = ""
	h.start()
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the first session was not on the current address")
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "a panel session")
	s := h.a.cur.Load()
	ds := fullState(1)
	ds.Settings = &pb.NodeSettings{PanelLinkUrl: b}
	lp.push <- &pb.ConnectResponse{Message: &pb.ConnectResponse_DesiredState{DesiredState: ds}}
	select {
	case r := <-lp.applies:
		if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Revision != 1 {
			t.Fatalf("apply result %v revision %d", r.Status, r.Revision)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no ApplyResult for the desired state")
	}
	if h.a.nextLink() != b || readMeta(t, h.dir).NextLink != b {
		t.Fatal("the new address was not saved")
	}
	time.Sleep(300 * time.Millisecond)
	if h.a.cur.Load() != s || len(lp.sessions) != 0 {
		t.Fatal("the session ended for the new address")
	}

	endSession(t, h)
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("the next session was not on the new address")
	}
	committedTo(t, h, b, a)
}

func TestSetPanelLink(t *testing.T) {
	dir := t.TempDir()
	link := "wss://panel.example.com/" + strings.Repeat("s", 24) + "/"
	if err := SetPanelLink(dir, link); err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("not enrolled: %v", err)
	}
	h := newHarness(t, harnessOpts{noStart: true})
	before, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPanelLink(h.dir, link); err == nil || !strings.Contains(err.Error(), "mTLS") {
		t.Fatalf("an mTLS node: %v", err)
	}
	if after, err := os.ReadFile(filepath.Join(h.dir, fileMeta)); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("%s changed for an mTLS node: %v", fileMeta, err)
	}

	m := readMeta(t, h.dir)
	m.Link = "wss://old.example.com/" + strings.Repeat("o", 24) + "/"
	if err := saveMeta(h.dir, &m); err != nil {
		t.Fatal(err)
	}
	if err := SetPanelLink(h.dir, "wss://panel.example.com/x"); err == nil {
		t.Fatal("a bad address was saved")
	}
	if err := SetPanelLink(h.dir, link); err != nil {
		t.Fatal(err)
	}
	if got := readMeta(t, h.dir); got.NextLink != link || got.Link != m.Link || got.Panel == "" {
		t.Fatalf("saved %+v", got)
	}
}

// session waits for the next session on the link panel and returns its link prefix.
func (p *sessionLinkPanel) session(t *testing.T) string {
	t.Helper()
	select {
	case got := <-p.sessions:
		return got
	case <-time.After(8 * time.Second):
		t.Fatal("no session on the link panel")
		return ""
	}
}

// endSession ends the agent's current panel session.
func endSession(t *testing.T, h *harness) {
	t.Helper()
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "a panel session")
	h.a.cur.Load().cancel()
}

// endHealthySession ends the agent's current panel session once it is healthy (has lived healthyAfter).
func endHealthySession(t *testing.T, h *harness) {
	t.Helper()
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "a panel session")
	time.Sleep(h.a.healthyAfter())
	h.a.cur.Load().cancel()
}

// prevLinkHarness is an agent on link address A with a new address B pending, both served by one link panel. A session
// is healthy after 50 ms. The agent is not started.
func prevLinkHarness(t *testing.T) (h *harness, lp *sessionLinkPanel, a, b string) {
	h = newHarness(t, harnessOpts{noStart: true})
	lp = newSessionLinkPanel(h)
	server := httptest.NewTLSServer(lp)
	t.Cleanup(server.Close)
	a, b = linkBaseOf(server, 'a'), linkBaseOf(server, 'b')
	h.a.linkHTTPClient = server.Client()
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.NextLink = a, a, b
	h.a.resetAfter = 25 * time.Millisecond
	if err := saveMeta(h.dir, &h.a.meta); err != nil {
		t.Fatal(err)
	}
	return h, lp, a, b
}

// committedTo waits until the agent uses link (and has saved it) with prev as its previous address.
func committedTo(t *testing.T, h *harness, link, prev string) {
	t.Helper()
	eventually(t, func() bool {
		h.a.metaMu.Lock()
		defer h.a.metaMu.Unlock()
		return h.a.meta.Link == link && h.a.cfg.LinkURL == link && h.a.meta.NextLink == "" && h.a.meta.PrevLink == prev
	}, "link and previous address")
	if m := readMeta(t, h.dir); m.Link != link || m.NextLink != "" || m.PrevLink != prev {
		t.Fatalf("saved link=%t next=%t prev=%t", m.Link == link, m.NextLink == "", m.PrevLink == prev)
	}
}

// A committed new link address that stops answering: the agent goes back to the link it had, and forgets the new one.
func TestPrevLinkWhenTheNewOneStopsAnswering(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.start()
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("the first session was not on the new address")
	}
	committedTo(t, h, b, a)

	lp.refuse('b')
	endHealthySession(t, h)
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the next session was not on the previous address")
	}
	committedTo(t, h, a, "")
	if !h.a.linkOnly() || lp.dialsTo('b') < 2 {
		t.Fatalf("linkOnly=%v, new address dialled %d times", h.a.linkOnly(), lp.dialsTo('b'))
	}
}

// A committed new link address that answers the Hello and drops the session at once: the first session over it
// committed it, did not hold, and the next turn goes back to the previous address.
func TestPrevLinkWhenTheNewOneDropsAfterHelloAck(t *testing.T) {
	h, lp, a, _ := prevLinkHarness(t)
	h.a.resetAfter = time.Minute // healthy after 2 minutes: no session here is
	lp.drop('b')
	h.start()
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("the first session was not on the new address")
	}
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the next session was not on the previous address")
	}
	committedTo(t, h, a, "")
	if n := lp.dialsTo('b'); n != 1 {
		t.Fatalf("new address dialled %d times, want 1", n)
	}
}

// The current link (committed earlier, with a previous address) answers the Hello and drops every session at once: the
// agent goes back to the previous address although the current one never failed to establish.
func TestPrevLinkWhenTheCurrentDropsAfterHelloAck(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.resetAfter = time.Minute
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.NextLink, h.a.meta.PrevLink = b, b, "", a
	h.a.meta.LinkSince = time.Now().Add(-time.Hour).Unix()
	if err := saveMeta(h.dir, &h.a.meta); err != nil {
		t.Fatal(err)
	}
	lp.drop('b')
	h.start()
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("the first session was not on the current address")
	}
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the next session was not on the previous address")
	}
	committedTo(t, h, a, "")
}

// The previous address survives a restart: an agent that starts on a committed link that does not answer goes back.
func TestPrevLinkAfterARestart(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.start()
	lp.session(t)
	committedTo(t, h, b, a)
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}

	lp.refuse('b')
	client := h.a.linkHTTPClient
	h.a = h.newAgent()
	if h.a.prevLink() != a || h.a.linkURL() != b || !h.a.linkOnly() {
		t.Fatal("the restarted agent lost its link or its previous address")
	}
	h.a.linkHTTPClient = client
	h.start()
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the restarted agent did not go back to the previous address")
	}
	committedTo(t, h, a, "")
}

// While the current link answers, the previous address is never dialled.
func TestPrevLinkUnusedWhileTheCurrentAnswers(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.start()
	lp.session(t)
	committedTo(t, h, b, a)
	for range 3 {
		endHealthySession(t, h)
		if got := lp.session(t); got != linkPrefix('b') {
			t.Fatal("a session left the current address")
		}
	}
	if n := lp.dialsTo('a'); n != 0 {
		t.Fatalf("previous address dialled %d times", n)
	}
	committedTo(t, h, b, a)
}

// skewLinkClock makes the clock of Meta.LinkSince run ahead of the real one by the returned value (nanoseconds).
func skewLinkClock(h *harness) *atomic.Int64 {
	var skew atomic.Int64
	h.a.linkClock = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	return &skew
}

// nextHealthy ends the agent's current session once it is healthy, waits for the next one and lets it become healthy too
// (and run what follows, such as forgetting an expired previous address).
func nextHealthy(t *testing.T, h *harness) {
	t.Helper()
	endHealthySession(t, h)
	old := h.a.cur.Load()
	eventually(t, func() bool { s := h.a.cur.Load(); return s != nil && s != old }, "the next panel session")
	time.Sleep(2 * h.a.healthyAfter())
}

// A healthy session over the new address 8 days after it took over: the previous address is forgotten, in agent.json
// too, and is no longer dialled when the new one stops answering.
func TestPrevAddressForgottenAfterTheNewOneWorked7Days(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	skew := skewLinkClock(h)
	h.start()
	lp.session(t)
	committedTo(t, h, b, a)
	if since := readMeta(t, h.dir).LinkSince; since == 0 || time.Since(time.Unix(since, 0)).Abs() > time.Minute {
		t.Fatalf("link_since %d is not the commit time", since)
	}

	skew.Store(int64(8 * 24 * time.Hour))
	nextHealthy(t, h)
	committedTo(t, h, b, "")
	raw, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("prev_")) || bytes.Contains(raw, []byte("link_since")) {
		t.Fatalf("%s keeps the previous address: %s", fileMeta, raw)
	}

	lp.refuse('b')
	dials := lp.dialsTo('b')
	endSession(t, h)
	eventually(t, func() bool { return lp.dialsTo('b') >= dials+3 }, "retries of the current address")
	if n := lp.dialsTo('a'); n != 0 {
		t.Fatalf("forgotten previous address dialled %d times", n)
	}
}

// A healthy session over the new address 1 day after it took over: the previous address stays.
func TestPrevAddressKeptWithin7Days(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	skew := skewLinkClock(h)
	h.start()
	lp.session(t)
	committedTo(t, h, b, a)
	since := readMeta(t, h.dir).LinkSince

	skew.Store(int64(24 * time.Hour))
	nextHealthy(t, h)
	committedTo(t, h, b, a)
	if got := readMeta(t, h.dir).LinkSince; got != since {
		t.Fatalf("link_since %d, want %d", got, since)
	}
}

// The current address took over 8 days ago and answers, but its sessions end before healthyAfter: neither the HelloAck
// nor such a session forgets the previous address (the agent goes on trying it after each). The first session that
// lives healthyAfter forgets it.
func TestPrevAddressKeptPast7DaysByUnhealthySessions(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.resetAfter = time.Second // healthy after 2 s: the 200 ms sessions below stay unhealthy even on a loaded machine
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.NextLink, h.a.meta.PrevLink = b, b, "", a
	h.a.meta.LinkSince = time.Now().Add(-8 * 24 * time.Hour).Unix()
	if err := saveMeta(h.dir, &h.a.meta); err != nil {
		t.Fatal(err)
	}
	lp.refuse('a') // the fallback finds nothing: the agent stays on b and its previous address stays a
	h.start()
	for range 2 {
		if got := lp.session(t); got != linkPrefix('b') {
			t.Fatal("a session was not on the current address")
		}
		eventually(t, func() bool { return h.a.cur.Load() != nil }, "a panel session")
		time.Sleep(200 * time.Millisecond)
		committedTo(t, h, b, a)
		dials := lp.dialsTo('a')
		endSession(t, h)
		eventually(t, func() bool { return lp.dialsTo('a') > dials }, "the fallback to the previous address")
	}
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("a session was not on the current address")
	}
	committedTo(t, h, b, "")
}

// The new address took over 8 days ago but has not established a session since: the previous address was not
// forgotten, and the agent goes back to it once the new one stops answering.
func TestPrevAddressFallbackAfter7DaysWhenTheNewOneFails(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	skew := skewLinkClock(h)
	h.start()
	lp.session(t)
	committedTo(t, h, b, a)

	skew.Store(int64(8 * 24 * time.Hour))
	lp.refuse('b')
	endSession(t, h)
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the next session was not on the previous address")
	}
	committedTo(t, h, a, "")
	if since := readMeta(t, h.dir).LinkSince; since != 0 {
		t.Fatalf("link_since %d kept without a previous address", since)
	}
}

// A link-only agent without a previous address leaves agent.json byte for byte as it was, however old its link.
func TestPrevAddressNoneLeavesAgentJSON(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	lp := newSessionLinkPanel(h)
	server := httptest.NewTLSServer(lp)
	t.Cleanup(server.Close)
	a := linkBaseOf(server, 'a')
	h.a.linkHTTPClient = server.Client()
	h.a.meta.Link, h.a.cfg.LinkURL = a, a
	h.a.resetAfter = 25 * time.Millisecond
	if err := saveMeta(h.dir, &h.a.meta); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	skewLinkClock(h).Store(int64(8 * 24 * time.Hour))
	h.start()
	lp.session(t)
	nextHealthy(t, h)
	after, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("%s changed:\n%s\n%s", fileMeta, before, after)
	}
}

// A node on the bridge whose current link is gone and whose new address starts to answer: the direct probe reaches the
// new address, the node leaves the bridge and commits the new address after a session over it.
func TestNextLinkMovesABridgedNode(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	lp.refuse('a')
	lp.refuse('b')
	var bridged atomic.Int32
	h.a.panelEntryFn = func() string { return "nod_de1" }
	h.a.bridgedFn = func(ctx context.Context, _ string) error {
		bridged.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	h.a.bridgeFallbackAfter, h.a.bridgeFallbackAttempts = time.Hour, 2
	h.a.bridgeProbeEvery, h.a.bridgeProbeTimeout = 20*time.Millisecond, 2*time.Second
	h.start()
	eventually(t, func() bool { return bridged.Load() > 0 }, "the node on the bridge")

	lp.answer('b')
	if got := lp.session(t); got != linkPrefix('b') {
		t.Fatal("the node did not come back over the new address")
	}
	committedTo(t, h, b, a)
}

// directLinkClient is the link client of h with a direct path that dial decides; through a bridge the agent replaces
// the dial and keeps the rest (the test server's roots).
func directLinkClient(h *harness, dial func(ctx context.Context, network, address string) (net.Conn, error)) {
	transport := h.a.linkHTTPClient.Transport.(*http.Transport).Clone()
	transport.DialContext = dial
	h.a.linkHTTPClient = &http.Client{Transport: transport}
}

func closedDirectPath(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("direct panel path closed")
}

// linkPanelBridge brings up the panel bridge of entry node nod_msk1 for h, forwarding to the link panel at panelAddress.
func linkPanelBridge(t *testing.T, h *harness, panelAddress string) {
	t.Helper()
	entry := nodebridge.NewEntry()
	bridgeServerCert, pin := issueBridgeSiteCertificate(t, h.panel)
	if err := entry.SetServe(nodebridge.ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{h.a.meta.NodeID},
		PanelVia: true, PanelAddress: panelAddress, Roots: h.a.id.Load().ca}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(entry.Handler(http.NotFoundHandler()))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{bridgeServerCert}, MinVersion: tls.VersionTLS13}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	t.Cleanup(func() {
		entry.Close()
		server.Close()
	})
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	identity := issueBridgePeerIdentity(t, h.panel, h.a.meta.NodeID)
	dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: "127.0.0.1", Port: uint16(port), SNI: "msk1.example.com", PinSHA256: pin,
		Path: "/private-bridge-path", Pool: 2, PanelVia: true, Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 4443}}}
	dialer := nodebridge.NewDialer(func() nodebridge.DialIdentity { return identity })
	h.a.bridgeDialer.Close()
	h.a.bridgeDialer = dialer
	dialer.Sync(context.Background(), []nodebridge.DialSpec{{Dial: dial}})
	t.Cleanup(dialer.Close)
	waitPanelBridgeConns(t, dialer, dial.EntryNodeID)
	m := newModel()
	m.bridgeDials = []plugin.BridgeDial{dial}
	h.a.setModel(m)
}

// A node whose direct path is closed goes to the bridge, and moves to its new link address there: through the bridge,
// with the new address as the link base, committed after its HelloAck.
func TestNextLinkThroughTheBridge(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	u, err := url.Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	directLinkClient(h, closedDirectPath)
	linkPanelBridge(t, h, u.Host)
	h.a.bridgeFallbackAfter, h.a.bridgeFallbackAttempts = time.Hour, 2
	h.a.bridgeWait = func(ctx context.Context, _ time.Duration) bool { <-ctx.Done(); return false } // no direct probe
	h.start()
	for turn := 0; lp.session(t) != linkPrefix('b'); turn++ {
		if turn == 3 {
			t.Fatal("the bridged sessions never tried the new address")
		}
		endSession(t, h)
	}
	committedTo(t, h, b, a)
	lp.mu.Lock()
	via := lp.via[linkPrefix('b')]
	lp.mu.Unlock()
	if via != "nod_msk1" {
		t.Fatal("the session over the new address was not through the bridge")
	}
}

// A node on the bridge whose new link address does not answer at all (the dial hangs): the probe of the new address
// runs out its own time, the current address is still probed within its own, and the node comes back off the bridge
// over the current address.
func TestNextLinkUnreachableDoesNotHoldANodeOnTheBridge(t *testing.T) {
	h, lp, a, _ := prevLinkHarness(t)
	next := "wss://next.example.com/" + linkPrefix('b') + "/"
	h.a.meta.NextLink = next
	var directOpen atomic.Bool
	gone := make(chan struct{})
	directLinkClient(h, func(ctx context.Context, network, address string) (net.Conn, error) {
		switch {
		case !directOpen.Load():
			return closedDirectPath(ctx, network, address)
		case address == "next.example.com:443":
			select { // a host that never answers
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-gone:
				return nil, errors.New("connection refused")
			}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, address)
	})
	var bridged atomic.Int32
	left := make(chan struct{})
	h.a.panelEntryFn = func() string { return "nod_de1" }
	h.a.bridgedFn = func(ctx context.Context, _ string) error {
		if bridged.Add(1) == 1 {
			defer close(left)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	h.a.bridgeFallbackAfter, h.a.bridgeFallbackAttempts = time.Hour, 2
	h.a.bridgeProbeEvery, h.a.bridgeProbeTimeout = 20*time.Millisecond, time.Second
	h.start()
	eventually(t, func() bool { return bridged.Load() > 0 }, "the node on the bridge")

	directOpen.Store(true)
	select {
	case <-left:
	case <-time.After(10 * time.Second):
		t.Fatal("the node stayed on the bridge")
	}
	close(gone)
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the node did not come back over the current address")
	}
	if h.a.linkURL() != a || h.a.nextLink() != next {
		t.Fatal("the unreachable new address was committed, or dropped")
	}
}
