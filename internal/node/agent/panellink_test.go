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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
)

// sessionLinkPanel is a panel's link endpoint that runs the whole v2 handshake (sealed) and answers the Hello with a
// HelloAck. It records the audience (the Host) and the link prefix of every session that got as far as the Hello, and
// answers 404 under a refused prefix (an address that stopped answering).
type sessionLinkPanel struct {
	h        *harness
	hellos   chan string
	sessions chan string              // the link prefix of every session that got as far as the Hello
	settings *pb.NodeSettings         // the HelloAck's; nil = empty
	push     chan *pb.ConnectResponse // sent on the session that is up
	applies  chan *pb.ApplyResult     // every ApplyResult the agent sent

	mu      sync.Mutex
	refused map[string]bool
	dials   map[string]int // requests per link prefix
}

func newSessionLinkPanel(h *harness) *sessionLinkPanel {
	return &sessionLinkPanel{h: h, hellos: make(chan string, 8), sessions: make(chan string, 32),
		push: make(chan *pb.ConnectResponse, 8), applies: make(chan *pb.ApplyResult, 8),
		refused: map[string]bool{}, dials: map[string]int{}}
}

func linkPrefix(c byte) string { return strings.Repeat(string(c), 24) }

// refuse makes the link address with prefix linkPrefix(c) stop answering.
func (p *sessionLinkPanel) refuse(c byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused[linkPrefix(c)] = true
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
	refused := p.refused[prefix]
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
	select {
	case p.hellos <- audience:
	default:
	}
	select {
	case p.sessions <- prefix:
	default:
	}
	if link.Write(ctx, &pb.ConnectResponse{Message: &pb.ConnectResponse_HelloAck{HelloAck: &pb.HelloAck{
		ServerTimeUnix: time.Now().Unix(), Settings: cmp.Or(p.settings, &pb.NodeSettings{}),
	}}}) != nil {
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
	if err := saveMeta(h.dir, m); err != nil {
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

// prevLinkHarness is an agent on link address A with a new address B pending, both served by one link panel. The agent
// is not started.
func prevLinkHarness(t *testing.T) (h *harness, lp *sessionLinkPanel, a, b string) {
	h = newHarness(t, harnessOpts{noStart: true})
	lp = newSessionLinkPanel(h)
	server := httptest.NewTLSServer(lp)
	t.Cleanup(server.Close)
	a, b = linkBaseOf(server, 'a'), linkBaseOf(server, 'b')
	h.a.linkHTTPClient = server.Client()
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.NextLink = a, a, b
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
	endSession(t, h)
	if got := lp.session(t); got != linkPrefix('a') {
		t.Fatal("the next session was not on the previous address")
	}
	committedTo(t, h, a, "")
	if !h.a.linkOnly() || lp.dialsTo('b') < 2 {
		t.Fatalf("linkOnly=%v, new address dialled %d times", h.a.linkOnly(), lp.dialsTo('b'))
	}
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
		endSession(t, h)
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

// nextEstablished ends the agent's current session and waits until the next one has its HelloAck (and has run what
// follows it, such as forgetting an expired previous address).
func nextEstablished(t *testing.T, h *harness) {
	t.Helper()
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "a panel session")
	old := h.a.cur.Load()
	old.cancel()
	eventually(t, func() bool { s := h.a.cur.Load(); return s != nil && s != old }, "the next panel session")
}

// A session over the new address 8 days after it took over: the previous address is forgotten, in agent.json too, and
// is no longer dialled when the new one stops answering.
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
	nextEstablished(t, h)
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

// A session over the new address 1 day after it took over: the previous address stays.
func TestPrevAddressKeptWithin7Days(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	skew := skewLinkClock(h)
	h.start()
	lp.session(t)
	committedTo(t, h, b, a)
	since := readMeta(t, h.dir).LinkSince

	skew.Store(int64(24 * time.Hour))
	nextEstablished(t, h)
	committedTo(t, h, b, a)
	if got := readMeta(t, h.dir).LinkSince; got != since {
		t.Fatalf("link_since %d, want %d", got, since)
	}
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
	if err := saveMeta(h.dir, h.a.meta); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	skewLinkClock(h).Store(int64(8 * 24 * time.Hour))
	h.start()
	lp.session(t)
	nextEstablished(t, h)
	after, err := os.ReadFile(filepath.Join(h.dir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("%s changed:\n%s\n%s", fileMeta, before, after)
	}
}
