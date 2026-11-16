package bridge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/plugin"
)

func TestPanelControlStreamUsesEntryTargetAndRejectsForeignTarget(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, server, _ := newBridgeFixture(t, target.port, pki)
	bridgeDial := panelTestDial(server, "nod_msk1", pki)
	panelAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(target.port)))
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: bridgeDial.Path, ExitNodeIDs: []string{"nod_de1"},
		PanelVia: true, PanelAddress: panelAddress, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	dialer.Sync(context.Background(), []DialSpec{{Dial: bridgeDial}})
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2
	}, "bridge dialer did not reconnect with panel_via enabled")

	stream, err := dialer.DialPanel(context.Background(), "nod_msk1")
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(stream, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	defer client.Close()
	if err := client.Handshake(); err != nil {
		t.Fatalf("panel TLS through fixed entry target: %v", err)
	}
	const payload = "control session through entry"
	if _, err := io.WriteString(client, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil || string(got) != payload {
		t.Fatalf("echo = %q, err %v", got, err)
	}
	_ = client.Close()

	// A caller cannot use the authenticated bridge endpoint to choose another destination.
	conn, proof, err := dialTLS(context.Background(), bridgeDial, DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey})
	if err != nil {
		t.Fatal(err)
	}
	transport := &http2.Transport{}
	cc, err := transport.NewClientConn(conn)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	defer cc.Close()
	pairID, err := newPairID()
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://"+server.Listener.Addr().String()+bridgeDial.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(ProtocolHeader, protocolVersion)
	request.Header.Set(StreamHeader, string(pairDown))
	request.Header.Set(StreamKindHeader, StreamKindPanel)
	request.Header.Set(PairIDHeader, pairID)
	request.Header.Set(TargetHeader, "192.0.2.44:443")
	proof.setHeaders(request.Header)
	response, err := cc.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || response.Header.Get(ErrorHeader) != "target_not_allowed" {
		t.Fatalf("foreign target response = %d %q, want 403 target_not_allowed", response.StatusCode, response.Header.Get(ErrorHeader))
	}

	downStream, err := dialer.DialPanel(context.Background(), "nod_msk1")
	if err != nil {
		t.Fatal(err)
	}
	entry.Close() // dropping the authenticated bridge also drops an active panel-control stream
	_ = downStream.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := downStream.Read(make([]byte, 1)); err == nil {
		_ = downStream.Close()
		t.Fatal("panel control stream stayed open after the entry bridge went down")
	}
	_ = downStream.Close()
}

func TestPanelControlStreamsAreIsolatedPerExit(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, first, server, _ := newBridgeFixture(t, target.port, pki)
	firstDial := panelTestDial(server, "nod_msk1", pki)
	secondKey, secondDER, _ := issueTestLeaf(t, pki.root, pki.rootKey, "nod_nl1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
		x509.ExtKeyUsageClientAuth, nil)
	secondID := DialIdentity{NodeID: "nod_nl1", CertificateDER: secondDER, PrivateKey: secondKey}
	second := NewDialer(func() DialIdentity { return secondID })
	t.Cleanup(second.Close)
	secondDial := firstDial
	secondDial.PanelVia = true
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: firstDial.Path, ExitNodeIDs: []string{"nod_de1", "nod_nl1"},
		PanelVia: true, PanelAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(target.port))), Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	first.Sync(context.Background(), []DialSpec{{Dial: firstDial}})
	second.Sync(context.Background(), []DialSpec{{Dial: secondDial}})
	waitFor(t, 10*time.Second, func() bool {
		return len(first.States()) == 1 && first.States()[0].Conns == 2 && len(second.States()) == 1 && second.States()[0].Conns == 2
	}, "both exits did not connect to the entry")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstStream, err := first.DialPanel(ctx, "nod_msk1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.DialPanel(ctx, "nod_msk1"); !errors.Is(err, errPanelBridgeBusy) {
		t.Fatalf("second control stream for one exit = %v, want control stream busy", err)
	}
	secondStream, err := second.DialPanel(ctx, "nod_msk1")
	if err != nil {
		_ = firstStream.Close()
		t.Fatal(err)
	}
	firstClient := tls.Client(firstStream, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	secondClient := tls.Client(secondStream, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	defer firstClient.Close()
	defer secondClient.Close()
	for _, client := range []*tls.Conn{firstClient, secondClient} {
		if err := client.HandshakeContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		conn *tls.Conn
		text string
	}{{firstClient, "de1"}, {secondClient, "nl1"}} {
		if _, err := io.WriteString(tc.conn, tc.text); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(tc.text))
		if _, err := io.ReadFull(tc.conn, got); err != nil || string(got) != tc.text {
			t.Fatalf("exit %s echo = %q, err %v", tc.text, got, err)
		}
	}
}

func TestPanelControlStreamByteBudgetRefillsAndEnds(t *testing.T) {
	e := NewEntry()
	defer e.Close()
	peer := e.peer("nod_de1")
	newPair := func(id string, budget uint64, at time.Time) *bridgePair {
		ctx, cancel := context.WithCancel(context.Background())
		pair := &bridgePair{peer: peer, key: pairKey{id: id}, ctx: ctx, cancel: cancel,
			ready: make(chan struct{}), attached: make(chan struct{}), done: make(chan struct{}), halves: map[pairRole]bool{},
			completed: map[pairRole]bool{}, kind: StreamKindPanel, budget: budget, budgetAt: at}
		e.mu.Lock()
		peer.panelPair = pair
		peer.pairs[pair.key] = pair
		e.mu.Unlock()
		return pair
	}

	// A long session spends little and the bucket refills: it never reaches the end, however long it lives.
	long := newPair(strings.Repeat("a", 32), 1<<20, time.Now().Add(-time.Hour))
	for i := 0; i < 100; i++ {
		if !e.reservePairBytes(long, 64<<10) {
			t.Fatalf("a quiet control link was cut at message %d", i)
		}
	}
	if pairDone(long) {
		t.Fatal("a quiet control link ended")
	}

	// A pump drains the bucket in one go and is ended.
	pump := newPair(strings.Repeat("b", 32), ControlBudgetBytes, time.Now())
	if !e.reservePairBytes(pump, ControlBudgetBytes) {
		t.Fatal("the whole bucket was not available")
	}
	if e.reservePairBytes(pump, 1<<20) {
		t.Fatal("the control link took bytes from an empty bucket")
	}
	e.mu.RLock()
	code := pump.err
	e.mu.RUnlock()
	if code != "control_byte_limit" || !pairDone(pump) {
		t.Fatalf("a pumping control link: code %q, done %t", code, pairDone(pump))
	}
}

// A new control link of an exit replaces the old one at once (a restarted exit must not wait for the entry to notice that the old
// one is gone), and a panel the entry cannot reach is not the bridge's error.
func TestPanelControlLinkIsReplacedAndItsErrorsStayOutOfTheBridgeState(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, first, server, _ := newBridgeFixture(t, target.port, pki)
	dial := panelTestDial(server, "nod_msk1", pki)
	serve := ServeConfig{EntryNodeID: "nod_msk1", Path: dial.Path, ExitNodeIDs: []string{"nod_de1"}, PanelVia: true,
		PanelAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(target.port))), Roots: pki.roots}
	if err := entry.SetServe(serve); err != nil {
		t.Fatal(err)
	}
	first.Sync(context.Background(), []DialSpec{{Dial: dial}})
	waitFor(t, 10*time.Second, func() bool { return len(first.States()) == 1 && first.States()[0].Conns == 2 }, "the exit did not connect")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old, err := first.DialPanel(ctx, "nod_msk1")
	if err != nil {
		t.Fatal(err)
	}
	// The same exit again, as an agent that restarted would be (its dialer has no memory of the old link).
	restarted := NewDialer(func() DialIdentity {
		return DialIdentity{NodeID: "nod_de1", CertificateDER: pki.exitDER, PrivateKey: pki.exitKey}
	})
	t.Cleanup(restarted.Close)
	restarted.Sync(context.Background(), []DialSpec{{Dial: dial}})
	waitFor(t, 10*time.Second, func() bool { return len(restarted.States()) == 1 && restarted.States()[0].Conns == 2 }, "the restarted exit did not connect")
	fresh, err := restarted.DialPanel(ctx, "nod_msk1")
	if err != nil {
		t.Fatalf("a restarted exit's control link was refused: %v", err)
	}
	defer fresh.Close()
	_ = old.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := old.Read(make([]byte, 1)); err == nil {
		t.Fatal("the replaced control link stayed open")
	}
	_ = old.Close()

	// The entry cannot reach the panel: the people's bridge state stays clean.
	_ = fresh.Close()
	serve.PanelAddress = "127.0.0.1:1"
	if err := entry.SetServe(serve); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.DialPanel(ctx, "nod_msk1"); err == nil {
		t.Fatal("a control link to an unreachable panel was opened")
	}
	for _, state := range entry.States() {
		if state.PeerNodeID == "nod_de1" && state.Error != "" {
			t.Fatalf("a panel the entry cannot reach marked the exit's bridge as broken: %q", state.Error)
		}
	}
}

func panelTestDial(server *httptest.Server, entryID string, pki testPKI) plugin.BridgeDial {
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		panic(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		panic(err)
	}
	return plugin.BridgeDial{EntryNodeID: entryID, Host: "127.0.0.1", Port: uint16(port), SNI: "msk1.example", PinSHA256: pki.pin,
		Path: "/private-bridge-path", Pool: 2, PanelVia: true,
		Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 443}}}
}

// Who may open a control link: only an exit the entry lists, only while the entry's panel_via is on, and only for a dial the exit
// was given with panel_via. An unlisted caller gets the site, like for every other bridge request.
func TestPanelControlLinkNeedsAListedExitAndPanelViaOnBothSides(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, server, _ := newBridgeFixture(t, target.port, pki)
	dial := panelTestDial(server, "nod_msk1", pki)
	serve := ServeConfig{EntryNodeID: "nod_msk1", Path: dial.Path, ExitNodeIDs: []string{"nod_de1"},
		PanelAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(target.port))), Roots: pki.roots}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connect := func(d *Dialer, spec plugin.BridgeDial) {
		t.Helper()
		d.Sync(context.Background(), []DialSpec{{Dial: spec}})
		waitFor(t, 10*time.Second, func() bool { return len(d.States()) == 1 && d.States()[0].Conns == 2 }, "the exit did not connect")
	}

	// The entry's panel_via is off: the bridge works, the control link is refused with a reason that is not a bridge fault.
	if err := entry.SetServe(serve); err != nil {
		t.Fatal(err)
	}
	connect(dialer, dial)
	if _, err := dialer.DialPanel(ctx, "nod_msk1"); err == nil || !strings.Contains(err.Error(), "panel_via_disabled") {
		t.Fatalf("control link while the entry's panel_via is off: %v", err)
	}

	// The exit's own dial has no panel_via: the dialer does not even try.
	serve.PanelVia = true
	if err := entry.SetServe(serve); err != nil {
		t.Fatal(err)
	}
	noVia := dial
	noVia.PanelVia = false
	dialer.Sync(context.Background(), []DialSpec{{Dial: noVia}})
	waitFor(t, 10*time.Second, func() bool { return len(dialer.States()) == 1 && dialer.States()[0].Conns == 2 }, "the exit did not reconnect")
	if _, err := dialer.DialPanel(ctx, "nod_msk1"); !errors.Is(err, errPanelBridgeUnavailable) {
		t.Fatalf("control link for a dial without panel_via: %v", err)
	}

	// An exit the entry does not list gets the site, whatever it asks for.
	key, der, _ := issueTestLeaf(t, pki.root, pki.rootKey, "nod_nl1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth, nil)
	stranger := NewDialer(func() DialIdentity { return DialIdentity{NodeID: "nod_nl1", CertificateDER: der, PrivateKey: key} })
	t.Cleanup(stranger.Close)
	stranger.Sync(context.Background(), []DialSpec{{Dial: dial}})
	waitFor(t, 5*time.Second, func() bool { return len(stranger.States()) == 1 && stranger.States()[0].Conns > 0 }, "the stranger's dial did not start")
	if _, err := stranger.DialPanel(ctx, "nod_msk1"); err == nil {
		t.Fatal("an unlisted exit opened a control link")
	}
	for _, state := range entry.States() {
		if state.PeerNodeID == "nod_nl1" {
			t.Fatalf("an unlisted exit became a peer of the entry: %+v", state)
		}
	}
}

func TestBadPanelAddressOnlyDropsTheControlLinkNotTheBridge(t *testing.T) {
	pki := newTestPKI(t)
	e := NewEntry()
	defer e.Close()
	for _, address := range []string{"", "panel.example", "panel.example:0", "panel.example:99999", "panel.example:https", " panel.example:443"} {
		err := e.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"},
			PanelVia: true, PanelAddress: address, Roots: pki.roots})
		if err != nil {
			t.Fatalf("address %q took the people's bridge down: %v", address, err)
		}
		if cfg := e.serveSnapshot(); cfg.PanelVia || cfg.PanelAddress != "" || cfg.Path == "" {
			t.Fatalf("address %q: serve = %+v, want the bridge without the control link", address, cfg)
		}
	}
}

// The control session's HTTP/2 client treats a StreamError that its connection's read returns as one of its own stream errors and
// reads again; the reset of the bridge's stream must therefore reach it as another error, or it spins on it.
func TestPanelStreamConnHandsOutNoHTTP2StreamError(t *testing.T) {
	reset := http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal}
	pipeReader, pipeWriter := io.Pipe()
	_ = pipeReader.CloseWithError(reset)
	c := &panelStreamConn{read: io.NopCloser(&failingReader{err: reset}), write: pipeWriter}

	_, readErr := c.Read(make([]byte, 8))
	_, writeErr := c.Write([]byte("x"))
	for name, err := range map[string]error{"read": readErr, "write": writeErr} {
		if _, isStream := err.(http2.StreamError); isStream || !errors.Is(err, reset) {
			t.Fatalf("%s error = %#v: want a wrapped error that still is the reset", name, err)
		}
	}

	c.read = io.NopCloser(&failingReader{err: io.EOF})
	if _, err := c.Read(make([]byte, 8)); err != io.EOF {
		t.Fatalf("a clean end of the stream = %v, want io.EOF itself (TLS compares it)", err)
	}
}

type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }
