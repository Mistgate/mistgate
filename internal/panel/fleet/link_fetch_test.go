package fleet

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
)

func linkHelloWith(t *testing.T, ws *websocket.Conn, caps ...string) *agentv1.HelloAck {
	t.Helper()
	if err := agentlink.WriteFrame(context.Background(), ws, helloWithUpdate("link-fetch", 0, caps...)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, b, err := agentlink.ReadFrame(ctx, ws)
		if err != nil || typ != websocket.MessageBinary {
			t.Fatalf("read session response: %v", err)
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

func fetchGET(h http.Handler, path, token, rangeHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// A link session of a link-fetch/1 agent gets a token; with it the agent fetches a file from the start and from an offset,
// only as itself. Without the capability, on the edge, or with another node's or no token, the route is a 404.
func TestLinkFetchServesTheSessionsNode(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.LinkServed = true
	updates := &fakeUpdates{data: []byte("0123456789")}
	e.f.SetUpdates(updates)
	a := e.enroll("link-fetch")
	other := e.enroll("link-other")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)

	plain := openPanelLink(t, server.URL, a)
	if ack := linkHelloWith(t, plain); ack.FetchToken != "" {
		t.Fatal("an agent without link-fetch/1 got a fetch token")
	}
	_ = plain.Close(websocket.StatusNormalClosure, "")

	ws := openPanelLink(t, server.URL, a)
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	ack := linkHelloWith(t, ws, plugin.CapLinkFetch)
	token := ack.FetchToken
	if len(token) < 40 {
		t.Fatalf("fetch token %q, want a 32-byte random value", token)
	}
	h := e.f.LinkHandler()
	path := "/fetch/" + a.nodeID + "/mistgate-asndb-data-any"

	got := fetchGET(h, path, token, "")
	if got.Code != http.StatusOK || got.Body.String() != "0123456789" || got.Header().Get("Content-Length") != "10" {
		t.Fatalf("whole file = %d %q %v", got.Code, got.Body.String(), got.Header())
	}
	got = fetchGET(h, path, token, "bytes=4-")
	if got.Code != http.StatusPartialContent || got.Body.String() != "456789" || got.Header().Get("Content-Range") != "bytes 4-9/10" {
		t.Fatalf("from offset 4 = %d %q %v", got.Code, got.Body.String(), got.Header())
	}
	for name, c := range map[string]struct{ path, token, rng string }{
		"no token":      {path, "", ""},
		"wrong token":   {path, strings.Repeat("x", len(token)), ""},
		"other node":    {"/fetch/" + other.nodeID + "/mistgate-asndb-data-any", token, ""},
		"no file":       {"/fetch/" + a.nodeID, token, ""},
		"extra segment": {path + "/x", token, ""},
	} {
		if w := fetchGET(h, c.path, c.token, c.rng); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, w.Code)
		}
	}
	if w := fetchGET(h, path, token, "bytes=2-5"); w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("a closed range: %d, want 416", w.Code)
	}

	updates.mu.Lock()
	updates.err = connect.NewError(connect.CodeResourceExhausted, errors.New("busy"))
	updates.mu.Unlock()
	if w := fetchGET(h, path, token, ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("busy bundle: %d, want 503", w.Code)
	}
	updates.mu.Lock()
	updates.err = nil
	updates.mu.Unlock()

	e.f.cfg.Remote = &testRemote{}
	if w := fetchGET(h, path, token, ""); w.Code != http.StatusNotFound {
		t.Errorf("edge: %d, want 404", w.Code)
	}
	e.f.cfg.Remote = nil
}

// The token outlives its session by the grace only, and is never in the panel's log.
func TestLinkFetchTokenEndsAfterItsSession(t *testing.T) {
	var tokens linkFetchTokens
	now := time.Unix(1_000_000, 0)
	token, ok := tokens.issue("nod_a", now)
	if !ok || !tokens.check(token, "nod_a", now.Add(48*time.Hour)) {
		t.Fatal("a live session's token must stay valid")
	}
	tokens.end(token, now)
	if !tokens.check(token, "nod_a", now.Add(linkFetchGrace-time.Second)) || tokens.check(token, "nod_a", now.Add(linkFetchGrace)) {
		t.Fatal("the token must end linkFetchGrace after its session")
	}
	if tokens.check(token, "nod_b", now) || tokens.check("", "nod_a", now) {
		t.Fatal("a token is its node's only")
	}
	if _, ok := tokens.issue("nod_a", now.Add(linkFetchGrace)); !ok || len(tokens.m) != 1 {
		t.Fatalf("expired grants must be swept on issue: %d left", len(tokens.m))
	}
}

func TestLinkFetchTokenNotLogged(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	e.f.log = slog.New(slog.NewTextHandler(&logs, nil))
	e.f.cfg.LinkServed = true
	e.f.SetUpdates(&fakeUpdates{data: []byte("x")})
	a := e.enroll("link-fetch-log")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)
	ws := openPanelLink(t, server.URL, a)
	token := linkHelloWith(t, ws, plugin.CapLinkFetch).FetchToken
	fetchGET(e.f.LinkHandler(), "/fetch/"+a.nodeID+"/f", token, "")
	_ = ws.Close(websocket.StatusNormalClosure, "")
	if token == "" || strings.Contains(logs.String(), token) {
		t.Fatal("the fetch token reached the log")
	}
}

// A link-only agent lists update-link/1 and link-fetch/1, never update/1 or nets/1 itself, so an older VPS panel (and the
// edge) never sends it a VPS update it cannot fetch. A VPS panel that serves the fetch route reads the pair as both.
func TestLinkFetchCapabilitiesOnlyOnAVPSPanel(t *testing.T) {
	link := &agentv1.Hello{Capabilities: []string{"ws-link/1", capUpdateLink, plugin.CapLinkFetch}}
	if caps := helloInfo(link, true).Caps; !slices.Contains(caps, capUpdate) || !slices.Contains(caps, capNetworks) {
		t.Fatalf("VPS panel: %v, want update/1 and nets/1", caps)
	}
	if caps := helloInfo(link, false).Caps; slices.Contains(caps, capUpdate) || slices.Contains(caps, capNetworks) {
		t.Fatalf("edge: %v, want neither", caps)
	}
	noUpdater := &agentv1.Hello{Capabilities: []string{"ws-link/1", plugin.CapLinkFetch}}
	if caps := helloInfo(noUpdater, true).Caps; slices.Contains(caps, capUpdate) || !slices.Contains(caps, capNetworks) {
		t.Fatalf("an unsigned link agent: %v, want nets/1 only", caps)
	}
	old := &agentv1.Hello{Capabilities: []string{"ws-link/1", capUpdateLink}}
	if caps := helloInfo(old, true).Caps; slices.Contains(caps, capUpdate) || slices.Contains(caps, capNetworks) {
		t.Fatalf("an older link agent: %v, want nothing added", caps)
	}

	e := newEnv(t)
	e.f.cfg.LinkServed = true
	a := e.enroll("link-fetch-caps")
	server := httptest.NewServer(e.f.LinkHandler())
	t.Cleanup(server.Close)
	ws := openPanelLink(t, server.URL, a)
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	linkHelloWith(t, ws, capUpdateLink, plugin.CapLinkFetch)
	n, err := e.f.st.Node(context.Background(), a.nodeID)
	if err != nil || !slices.Contains(n.AgentCaps, capUpdate) || !slices.Contains(n.AgentCaps, capNetworks) {
		t.Fatalf("stored capabilities %v, %v", n.AgentCaps, err)
	}
}
