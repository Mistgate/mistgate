package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The connect loop's choice between the direct path to the panel and the bridge of an entry node, driven by scripted sessions
// (the seams sessionFn, bridgedFn, panelEntryFn and probeFn) and a short clock: a session counts as healthy after 50 ms.

type pathRig struct {
	t *testing.T
	a *Agent

	mu       sync.Mutex
	events   []string
	times    []time.Time
	waits    []time.Duration
	entry    string
	active   atomic.Int32
	maxAlive atomic.Int32

	direct  func(n int, ctx context.Context) error // n counts the direct sessions started so far, from 1
	bridged func(n int, ctx context.Context, via string) error
	probe   func(n int) error
}

func newPathRig(t *testing.T) *pathRig {
	t.Helper()
	r := &pathRig{t: t, entry: "nod_msk1"}
	r.a = &Agent{
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		backoffMin: 2 * time.Millisecond, backoffMax: 16 * time.Millisecond, resetAfter: 25 * time.Millisecond,
		bridgeFallbackAfter: time.Hour, bridgeFallbackAttempts: 3,
		bridgeProbeEvery: 10 * time.Millisecond, bridgeProbeTimeout: time.Second,
	}
	r.a.panelEntryFn = func() string { r.mu.Lock(); defer r.mu.Unlock(); return r.entry }
	r.a.sessionFn = func(ctx context.Context) error {
		n := r.start("direct")
		defer r.active.Add(-1)
		return r.direct(n, ctx)
	}
	r.a.bridgedFn = func(ctx context.Context, via string) error {
		n := r.start("bridge:" + via)
		defer r.active.Add(-1)
		return r.bridged(n, ctx, via)
	}
	r.a.bridgeWait = func(ctx context.Context, d time.Duration) bool {
		r.mu.Lock()
		r.waits = append(r.waits, d)
		r.mu.Unlock()
		sleepCtx(ctx, min(d, 15*time.Millisecond))
		return ctx.Err() == nil
	}
	n := 0
	r.a.probeFn = func(context.Context) error {
		n++
		if r.probe == nil {
			return errors.New("direct path unavailable")
		}
		return r.probe(n)
	}
	return r
}

func (r *pathRig) start(kind string) int {
	if now := r.active.Add(1); now > r.maxAlive.Load() {
		r.maxAlive.Store(now)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, kind)
	r.times = append(r.times, time.Now())
	count := 0
	for _, e := range r.events {
		if (kind == "direct") == (e == "direct") {
			count++
		}
	}
	return count
}

func (r *pathRig) setEntry(id string) { r.mu.Lock(); r.entry = id; r.mu.Unlock() }

func (r *pathRig) log() string { r.mu.Lock(); defer r.mu.Unlock(); return strings.Join(r.events, " ") }

// run drives the loop until done reports true (or five seconds pass) and stops it.
func (r *pathRig) run(done func() bool) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); r.a.connectLoop(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !done() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ok := done()
	cancel()
	<-finished
	if !ok {
		r.t.Fatalf("the loop did not get there; events: %s", r.log())
	}
	if got := r.maxAlive.Load(); got > 1 {
		r.t.Fatalf("%d control sessions of one agent ran at the same time; events: %s", got, r.log())
	}
}

func (r *pathRig) reached(prefix string) func() bool {
	return func() bool {
		return strings.HasPrefix(r.log(), prefix) && len(strings.Fields(r.log())) >= len(strings.Fields(prefix))
	}
}

// established marks a session as authenticated (as runSession does after a HelloAck), then lives for d (until ctx ends when d < 0).
func (r *pathRig) established(ctx context.Context, d time.Duration, err error) error {
	r.a.sessionEpoch.Add(1)
	if d < 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	if d > 0 {
		sleepCtx(ctx, d)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

var errScripted = errors.New("scripted session end")

func TestPanelPathFallsBackAfterThreeFailuresStaysOnTheBridgeAndReturnsAfterTwoProbes(t *testing.T) {
	r := newPathRig(t)
	r.direct = func(n int, ctx context.Context) error {
		if n <= 3 {
			return errScripted // not established
		}
		return r.established(ctx, -1, nil) // the return: healthy, lives until the end
	}
	r.bridged = func(n int, ctx context.Context, via string) error {
		if n == 1 {
			return r.established(ctx, 4*time.Millisecond, errScripted) // the bridge session ends once; the agent must not go direct for that
		}
		return r.established(ctx, -1, nil)
	}
	r.probe = func(n int) error {
		if n == 1 {
			return errScripted // one failed probe resets the count
		}
		return nil
	}
	r.run(r.reached("direct direct direct bridge:nod_msk1 bridge:nod_msk1 direct"))
	if got := r.log(); got != "direct direct direct bridge:nod_msk1 bridge:nod_msk1 direct" {
		t.Fatalf("events = %s", got)
	}
}

// A filter that lets the handshake through and freezes the flow: the probes pass, the return fails within a minute. The agent goes
// straight back to the bridge (no three failures first), and the next probes come at twice the gap.
func TestPanelPathFailedReturnGoesStraightBackAndWaitsTwiceAsLong(t *testing.T) {
	r := newPathRig(t)
	r.a.bridgeProbeEvery = 20 * time.Millisecond
	r.direct = func(n int, ctx context.Context) error {
		switch n {
		case 1, 2, 3:
			return errScripted
		case 4:
			return r.established(ctx, 5*time.Millisecond, errScripted) // authenticated, dies young
		default:
			return r.established(ctx, -1, nil)
		}
	}
	r.bridged = func(n int, ctx context.Context, via string) error { return r.established(ctx, -1, nil) }
	r.probe = func(int) error { return nil }
	const want = "direct direct direct bridge:nod_msk1 direct bridge:nod_msk1 direct"
	r.run(r.reached(want))
	if got := r.log(); got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	r.mu.Lock()
	waits := append([]time.Duration(nil), r.waits...)
	r.mu.Unlock()
	if len(waits) < 4 {
		t.Fatalf("probe waits = %v", waits)
	}
	first, second := waits[0], waits[2] // the first wait of each stay on the bridge (the second wait of a stay is one gap later)
	if first < 18*time.Millisecond || first > 21*time.Millisecond {
		t.Fatalf("first stay: first probe after %s, want about 20ms", first)
	}
	if second < 36*time.Millisecond || second > 41*time.Millisecond {
		t.Fatalf("second stay: first probe after %s, want about twice that (40ms) after a failed return", second)
	}
}

// Authenticated sessions that die young are failures too: a freeze that sets in after the handshake must not hold the agent on
// the direct path forever.
func TestPanelPathShortEstablishedSessionsCountAsFailures(t *testing.T) {
	r := newPathRig(t)
	r.direct = func(n int, ctx context.Context) error { return r.established(ctx, 3*time.Millisecond, errScripted) }
	r.bridged = func(n int, ctx context.Context, via string) error { return r.established(ctx, -1, nil) }
	r.run(r.reached("direct direct direct bridge:nod_msk1"))
	if got := r.log(); got != "direct direct direct bridge:nod_msk1" {
		t.Fatalf("events = %s", got)
	}
}

// A healthy session in between starts the count again, and a mTLS session that only found out about the link says nothing.
func TestPanelPathHealthySessionResetsTheCountAndTransportSwitchIsNoFailure(t *testing.T) {
	r := newPathRig(t)
	r.direct = func(n int, ctx context.Context) error {
		switch {
		case n <= 2:
			return errScripted
		case n == 3:
			return r.established(ctx, 60*time.Millisecond, errScripted) // healthy, then the panel restarts
		case n <= 5:
			return errScripted
		case n <= 8:
			return r.established(ctx, 0, errSwitchTransport) // no verdict
		default:
			return r.established(ctx, -1, nil)
		}
	}
	r.bridged = func(n int, ctx context.Context, via string) error {
		t.Error("the bridge was used")
		return r.established(ctx, -1, nil)
	}
	r.run(r.reached("direct direct direct direct direct direct direct direct direct"))
}

// The time rule: no authenticated session for the whole delay, even without three counted failures.
func TestPanelPathFallsBackAfterTheTimeWithoutASession(t *testing.T) {
	r := newPathRig(t)
	r.a.bridgeFallbackAfter = 30 * time.Millisecond
	r.direct = func(n int, ctx context.Context) error {
		sleepCtx(ctx, 8*time.Millisecond)
		return errLinkNotEstablished
	} // never counted
	r.bridged = func(n int, ctx context.Context, via string) error { return r.established(ctx, -1, nil) }
	r.run(func() bool { return strings.Contains(r.log(), "bridge:") })
}

// The panel is down for both paths: the attempts slow down to the maximum backoff instead of staying at the minimum.
func TestPanelPathBackoffKeepsGrowingWhenTheBridgeCannotReachThePanelEither(t *testing.T) {
	r := newPathRig(t)
	r.a.backoffMin, r.a.backoffMax = 5*time.Millisecond, 80*time.Millisecond
	r.direct = func(n int, ctx context.Context) error { return errScripted }
	r.bridged = func(n int, ctx context.Context, via string) error { return errScripted }
	r.run(func() bool { return len(strings.Fields(r.log())) >= 14 })
	r.mu.Lock()
	defer r.mu.Unlock()
	longest := time.Duration(0)
	for i := 1; i < len(r.times); i++ {
		longest = max(longest, r.times[i].Sub(r.times[i-1]))
	}
	if longest < 56*time.Millisecond {
		t.Fatalf("the longest wait between attempts was %s: the backoff did not grow towards its 80ms maximum; events: %v", longest, r.events)
	}
	if elapsed := r.times[len(r.times)-1].Sub(r.times[0]); elapsed < 150*time.Millisecond {
		t.Fatalf("14 attempts in %s: hammering the panel", elapsed)
	}
}

// When the bridge goes away under a session the agent tries the direct path (and only falls back again by the rule); when the
// entry changes, the next session goes through the new one.
func TestPanelPathBridgeLossAndEntryChange(t *testing.T) {
	r := newPathRig(t)
	r.direct = func(n int, ctx context.Context) error {
		if n <= 3 {
			return errScripted
		}
		return errScripted // the direct path stays down
	}
	r.bridged = func(n int, ctx context.Context, via string) error {
		switch n {
		case 1:
			r.setEntry("nod_spb1") // the other entry's bridge is up now; this one is gone
			return r.established(ctx, 0, errScripted)
		case 2:
			r.setEntry("") // every bridge is gone
			return r.established(ctx, 0, errScripted)
		default:
			return r.established(ctx, -1, nil)
		}
	}
	// 3 direct failures, bridge via msk1, bridge via spb1 (no direct in between), then nothing is up: direct until the rule fires again.
	r.run(func() bool { return strings.Contains(r.log(), "bridge:nod_spb1 direct") })
	if got := r.log(); !strings.HasPrefix(got, "direct direct direct bridge:nod_msk1 bridge:nod_spb1 direct") {
		t.Fatalf("events = %s", got)
	}
}

// Jitter: fifty exits that fell back together do not probe together; the interval doubles per failed return up to an hour.
func TestPanelProbeDelayIsJitteredAndDoublesPerFailedReturn(t *testing.T) {
	a := &Agent{}
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := a.bridgeProbeDelay(0)
		if d < 8*time.Minute || d > 12*time.Minute {
			t.Fatalf("probe delay %s is outside 10 min +-20%%", d)
		}
		seen[d] = true
	}
	if len(seen) < 50 {
		t.Fatalf("only %d different probe delays out of 200: the exits would probe in step", len(seen))
	}
	for flaps, want := range map[int]time.Duration{1: 20 * time.Minute, 2: 40 * time.Minute, 3: time.Hour, 9: time.Hour} {
		for i := 0; i < 50; i++ {
			d := a.bridgeProbeDelay(flaps)
			if d < want*8/10 || d > want*12/10 {
				t.Fatalf("after %d failed returns the delay is %s, want about %s", flaps, d, want)
			}
		}
	}
}

// The bridge dies under the control session: the agent does not stay on it, and the direct path takes over as soon as it works.
func TestPanelSessionSurvivesTheBridgeFallingAndTheDirectPathReturning(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	entry := nodebridge.NewEntry()
	bridgeServerCert, pin := issueBridgeSiteCertificate(t, h.panel)
	if err := entry.SetServe(nodebridge.ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{h.a.meta.NodeID},
		PanelVia: true, PanelAddress: h.panel.addr(), Roots: h.a.id.Load().ca}); err != nil {
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
	proofIdentity := issueBridgePeerIdentity(t, h.panel, h.a.meta.NodeID)
	dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: "127.0.0.1", Port: uint16(port), SNI: "msk1.example.com", PinSHA256: pin,
		Path: "/private-bridge-path", Pool: 2, PanelVia: true, Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 4443}}}
	dialer := nodebridge.NewDialer(func() nodebridge.DialIdentity { return proofIdentity })
	h.a.bridgeDialer.Close()
	h.a.bridgeDialer = dialer
	dialer.Sync(context.Background(), []nodebridge.DialSpec{{Dial: dial}})
	t.Cleanup(dialer.Close)
	waitPanelBridgeConns(t, dialer, dial.EntryNodeID)

	m := newModel()
	m.bridgeDials = []plugin.BridgeDial{dial}
	h.a.setModel(m)
	var directReady atomic.Bool
	h.a.directDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !directReady.Load() {
			return nil, errors.New("direct panel path unavailable")
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}
	h.a.bridgeFallbackAfter, h.a.bridgeFallbackAttempts = time.Hour, 3
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 40*time.Millisecond
	h.a.bridgeWait = func(ctx context.Context, _ time.Duration) bool { <-ctx.Done(); return false } // no probe in this test

	h.start()
	if first := receivePanelHello(t, h.panel.hellos); first.LinkViaNodeId != "nod_msk1" {
		t.Fatalf("first Hello.link_via_node_id = %q, want nod_msk1", first.LinkViaNodeId)
	}
	entry.Close() // the entry's bridge dies under the session
	directReady.Store(true)
	for {
		hello := receivePanelHello(t, h.panel.hellos)
		if hello.LinkViaNodeId == "" {
			return // the agent is back on the direct path
		}
	}
}
