package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// startLinkTurn runs the real transport/session path without connectLoop's retries, so each address decision is observable.
func startLinkTurn(t *testing.T, h *harness, via string) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		if via != "" {
			done <- h.a.sessionViaBridge(ctx, via)
		} else {
			done <- h.a.session(ctx)
		}
	}()
	return func() error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(8 * time.Second):
			t.Fatal("link session did not end")
			return nil
		}
	}
}

func bridgeLinkHarness(t *testing.T, h *harness, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid fixture address")
	}
	directLinkClient(h, closedDirectPath)
	linkPanelBridge(t, h, u.Host)
	return "nod_msk1"
}

// A -> B -> C retains A until B proves healthy, including across restart. A healthy B becomes the fallback for C.
// Run both routes so the bridge must both confirm healthy sessions and fall back after short ones.
func TestPrevLinkConsecutiveSwitches(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		for _, restart := range []bool{false, true} {
			for _, healthy := range []bool{false, true} {
				name := "direct"
				if bridge {
					name = "bridge"
				}
				if restart {
					name += "/restart"
				} else {
					name += "/running"
				}
				if healthy {
					name += "/healthy"
				} else {
					name += "/short"
				}
				t.Run(name, func(t *testing.T) {
					h, lp, a, b := prevLinkHarness(t)
					// B must prove healthy only in the healthy case; every other session here is short by design.
					h.a.resetAfter = time.Minute
					if healthy {
						h.a.resetAfter = 100 * time.Millisecond
					}
					cURL, _ := url.Parse(b)
					cURL.Path = "/" + linkPrefix('c') + "/"
					c := cURL.String()
					if !healthy {
						lp.settingsByPrefix = map[string]*pb.NodeSettings{linkPrefix('b'): {PanelLinkUrl: c}}
					}
					lp.drop('c')
					via := ""
					if bridge {
						via = bridgeLinkHarness(t, h, a)
					}
					end := startLinkTurn(t, h, via)
					if lp.session(t) != linkPrefix('b') {
						t.Fatal("first session did not use B")
					}
					if healthy {
						committedTo(t, h, b, a)
						eventually(t, func() bool { return readMeta(t, h.dir).LinkHealthy }, "persisted healthy B")
						h.a.applyPanelLink(&pb.NodeSettings{PanelLinkUrl: c})
						endSession(t, h)
					}
					_ = end()
					h.a.resetAfter = time.Minute // C and the fallback are short sessions, whatever B needed (no turn runs now)
					if restart {
						client, dialer := h.a.linkHTTPClient, h.a.bridgeDialer
						h.a = h.newAgent()
						h.a.linkHTTPClient = client
						if bridge {
							h.a.bridgeDialer.Close()
							h.a.bridgeDialer = dialer
						}
					}
					end = startLinkTurn(t, h, via)
					if lp.session(t) != linkPrefix('c') {
						t.Fatal("second switch did not use C")
					}
					_ = end() // C answers HelloAck, then drops immediately
					want, prefix := a, linkPrefix('a')
					if healthy {
						want, prefix = b, linkPrefix('b')
					}
					committedTo(t, h, c, want)
					end = startLinkTurn(t, h, via)
					if lp.session(t) != prefix {
						t.Fatal("short C session did not fall back to the last healthy address")
					}
					committedTo(t, h, want, "")
					endSession(t, h)
					_ = end()
				})
			}
		}
	}
}

// Dialing takes longer than healthyAfter, but B drops immediately after HelloAck: it is still an unhealthy address.
func TestPrevLinkHealthStartsAtHelloAck(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.resetAfter = 500 * time.Millisecond // healthy after 1 s: the short sessions below end within a few ms
	lp.drop('b')
	var dials atomic.Int32
	directLinkClient(h, func(ctx context.Context, network, address string) (net.Conn, error) {
		if dials.Add(1) == 1 { // only the dial to B outlasts the health threshold
			timer := time.NewTimer(h.a.healthyAfter() + 100*time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	end := startLinkTurn(t, h, "")
	if lp.session(t) != linkPrefix('b') {
		t.Fatal("first session did not use B")
	}
	_ = end()
	committedTo(t, h, b, a)
	end = startLinkTurn(t, h, "")
	if lp.session(t) != linkPrefix('a') {
		t.Fatal("dial time incorrectly made the short B session healthy")
	}
	endSession(t, h)
	_ = end()
}

func TestPrevLinkBridgeCurrentShortAndHealthy(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		name := "short"
		if healthy {
			name = "healthy"
		}
		t.Run(name, func(t *testing.T) {
			h, lp, a, b := prevLinkHarness(t)
			h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.NextLink, h.a.meta.PrevLink = b, b, "", a
			h.a.meta.LinkSince = time.Now().Add(-8 * 24 * time.Hour).Unix()
			if err := saveMeta(h.dir, &h.a.meta); err != nil {
				t.Fatal(err)
			}
			via := bridgeLinkHarness(t, h, a)
			if !healthy {
				h.a.resetAfter = time.Minute // the dropped B session is short by design
				lp.drop('b')
			}
			end := startLinkTurn(t, h, via)
			if lp.session(t) != linkPrefix('b') {
				t.Fatal("first bridged session did not use B")
			}
			if healthy {
				committedTo(t, h, b, "")
				endSession(t, h)
			}
			if err := end(); errors.Is(err, errSwitchTransport) {
				t.Fatal("unexpected transport switch")
			}
			if !healthy {
				committedTo(t, h, b, a)
			}
			end = startLinkTurn(t, h, via)
			want := linkPrefix('a')
			if healthy {
				want = linkPrefix('b')
			}
			if lp.session(t) != want {
				t.Fatal("bridge address selection did not reflect session health")
			}
			endSession(t, h)
			_ = end()
		})
	}
}

// A CLI request made while B is connected survives B's later health write and is followed after restart.
func TestPanelLinkPendingCLIWriteSurvivesSessionPersistence(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.resetAfter = 100 * time.Millisecond
	end := startLinkTurn(t, h, "")
	if lp.session(t) != linkPrefix('b') {
		t.Fatal("first session did not use B")
	}
	committedTo(t, h, b, a)
	cURL, _ := url.Parse(b)
	cURL.Path = "/" + linkPrefix('c') + "/"
	c := cURL.String()
	if err := SetPanelLink(h.dir, c); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return readMeta(t, h.dir).LinkHealthy }, "health write after CLI update")
	if readMeta(t, h.dir).NextLink != c {
		t.Fatal("the session health write overwrote the pending CLI address")
	}
	endSession(t, h)
	_ = end()
	client := h.a.linkHTTPClient
	h.a = h.newAgent()
	h.a.linkHTTPClient = client
	end = startLinkTurn(t, h, "")
	if lp.session(t) != linkPrefix('c') {
		t.Fatal("restarted agent did not follow the pending CLI address")
	}
	committedTo(t, h, c, b)
	endSession(t, h)
	_ = end()
}

// A previous address that fails before HelloAck must not make connectLoop abandon a working bridge. The current
// address establishes in the same turn, while a pending next address keeps its schedule.
func TestPrevLinkBridgeFailureKeepsCurrentTurn(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	cURL, _ := url.Parse(b)
	cURL.Path = "/" + linkPrefix('c') + "/"
	c := cURL.String()
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.PrevLink, h.a.meta.NextLink = b, b, a, c
	h.a.currentFailed, h.a.triedNext = true, true
	h.a.nextAnswered.Store(true)
	if err := saveMeta(h.dir, &h.a.meta); err != nil {
		t.Fatal(err)
	}
	lp.refuse('a')
	via := bridgeLinkHarness(t, h, a)
	epoch := h.a.sessionEpoch.Load()
	end := startLinkTurn(t, h, via)
	if lp.session(t) != linkPrefix('b') {
		t.Fatal("failed previous address did not leave the bridge turn to the current address")
	}
	endHealthySession(t, h)
	_ = end()
	if h.a.sessionEpoch.Load() == epoch {
		t.Fatal("the bridge turn did not establish; connectLoop would release the bridge")
	}
	if !h.a.triedNext || !h.a.nextAnswered.Load() || h.a.nextLink() != c || lp.dialsTo('c') != 0 {
		t.Fatal("the previous-address fallback consumed the pending next-address schedule")
	}
	if lp.dialsTo('a') != 1 || lp.dialsTo('b') != 1 {
		t.Fatal("expected one previous attempt and one current session on the bridge")
	}
}

func TestPrevLinkBridgeFailureRespectsParentCancellation(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.PrevLink, h.a.meta.NextLink = b, b, a, ""
	h.a.currentFailed = true
	via := bridgeLinkHarness(t, h, a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.a.sessionViaBridge(ctx, via); err == nil {
		t.Fatal("cancelled parent did not end the previous-address attempt")
	}
	if lp.dialsTo('b') != 0 {
		t.Fatal("cancelled parent still attempted the current address")
	}
}

// An unreachable previous address and a current address that HelloAcks then drops cannot starve a healthy pending
// next address. Fallback gets the first turn (with current on the same bridge), and next gets the second.
func TestPrevLinkBridgeShortCurrentDoesNotStarveNext(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	cURL, _ := url.Parse(b)
	cURL.Path = "/" + linkPrefix('c') + "/"
	c := cURL.String()
	h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.PrevLink, h.a.meta.NextLink = b, b, a, c
	h.a.currentFailed, h.a.triedNext = true, true // even an earlier next attempt cannot postpone it indefinitely
	if err := saveMeta(h.dir, &h.a.meta); err != nil {
		t.Fatal(err)
	}
	lp.refuse('a')
	lp.drop('b')
	h.a.resetAfter = time.Minute // the dropped B session is short by design
	via := bridgeLinkHarness(t, h, a)
	end := startLinkTurn(t, h, via)
	if lp.session(t) != linkPrefix('b') {
		t.Fatal("fallback did not retry current on the same bridge")
	}
	_ = end()
	h.a.resetAfter = 25 * time.Millisecond // C must prove healthy (no turn runs now)
	if !h.a.currentFailed || lp.dialsTo('a') != 1 || lp.dialsTo('b') != 1 || lp.dialsTo('c') != 0 {
		t.Fatal("first turn did not favor previous and count the short current session as a failure")
	}
	end = startLinkTurn(t, h, via)
	if lp.session(t) != linkPrefix('c') {
		t.Fatal("repeated previous/current failures starved the pending next address")
	}
	committedTo(t, h, c, a)
	eventually(t, func() bool { return readMeta(t, h.dir).LinkHealthy }, "healthy pending address through bridge")
	if lp.dialsTo('a') != 1 || lp.dialsTo('b') != 1 || lp.dialsTo('c') != 1 {
		t.Fatal("pending next address did not get the second bridge turn")
	}
	endSession(t, h)
	_ = end()
	if h.a.currentFailed {
		t.Fatal("healthy next session was counted as a current-address failure")
	}
}

func TestPanelLinkInFlightPromotionRespectsCLIIntent(t *testing.T) {
	for _, reassert := range []bool{false, true} {
		name := "cancel"
		if reassert {
			name = "reassert"
		}
		t.Run(name, func(t *testing.T) {
			h, lp, a, b := prevLinkHarness(t)
			gate := make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(gate) }) })
			lp.beforeAck = func(prefix string) {
				if prefix == linkPrefix('b') {
					<-gate
				}
			}
			end := startLinkTurn(t, h, "")
			if lp.session(t) != linkPrefix('b') {
				t.Fatal("pending candidate did not reach Hello")
			}
			requested, pending := a, ""
			if reassert {
				requested, pending = b, b
			}
			if err := SetPanelLink(h.dir, requested); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(gate) })
			if !errors.Is(end(), errSwitchTransport) {
				t.Fatal("superseded candidate session was not ended")
			}
			saved := readMeta(t, h.dir)
			if saved.Link != a || saved.NextLink != pending || saved.PrevLink != "" ||
				h.a.linkURL() != a || h.a.nextLink() != pending {
				t.Fatal("stale candidate was promoted or newer pending intent was not refreshed")
			}
			end = startLinkTurn(t, h, "")
			want := linkPrefix('a')
			if reassert {
				want = linkPrefix('b')
			}
			if lp.session(t) != want {
				t.Fatal("next turn did not follow the latest CLI intent")
			}
			if reassert {
				committedTo(t, h, b, a)
			}
			endSession(t, h)
			_ = end()
		})
	}
}

type teardownGateTransport struct {
	ctx       context.Context
	cancelled bool
	received  bool
	closing   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (s *teardownGateTransport) Context() context.Context      { return s.ctx }
func (s *teardownGateTransport) Send(*pb.ConnectRequest) error { return nil }
func (s *teardownGateTransport) CloseSend() error              { return s.Close() }
func (s *teardownGateTransport) Receive() (*pb.ConnectResponse, error) {
	if !s.received {
		s.received = true
		return &pb.ConnectResponse{Message: &pb.ConnectResponse_HelloAck{HelloAck: &pb.HelloAck{}}}, nil
	}
	if s.cancelled {
		<-s.ctx.Done()
		return nil, s.ctx.Err()
	}
	return nil, io.EOF
}
func (s *teardownGateTransport) Close() error {
	s.once.Do(func() { close(s.closing) })
	<-s.release
	return nil
}

func TestPanelLinkHealthExcludesTeardown(t *testing.T) {
	for _, cancellation := range []bool{false, true} {
		name := "receive-failure"
		if cancellation {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{noStart: true})
			h.a.resetAfter = 500 * time.Millisecond // healthy after 1 s: the session below ends at once
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stream := &teardownGateTransport{ctx: ctx, cancelled: cancellation,
				closing: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(stream.release) }) })
			done := make(chan error, 1)
			go func() { done <- h.a.runSession(cancel, stream, "") }()
			if cancellation {
				eventually(t, func() bool { return h.a.cur.Load() != nil }, "session before cancellation")
				h.a.cur.Load().cancel()
			}
			select {
			case <-stream.closing:
			case <-time.After(5 * time.Second):
				t.Fatal("session teardown did not start")
			}
			time.Sleep(h.a.healthyAfter() + 100*time.Millisecond)
			release.Do(func() { close(stream.release) })
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("session teardown did not finish")
			}
			if h.a.sessionHealthy() {
				t.Fatal("short session became healthy while waiting for teardown")
			}
			if time.Since(h.a.sessionAckAt) < h.a.healthyAfter() {
				t.Fatal("regression did not cross the health threshold during teardown")
			}
		})
	}
}

// The HelloAck clock keeps running while promotion waits on agent.json.lock. A session ending immediately after
// that wait must persist the same healthy proof used by fallback selection, even if the timer has not run.
func TestPanelLinkHealthDeadlineIncludesPromotion(t *testing.T) {
	h, lp, a, b := prevLinkHarness(t)
	h.a.resetAfter = 100 * time.Millisecond
	drop := make(chan struct{})
	var closePeer sync.Once
	t.Cleanup(func() { closePeer.Do(func() { close(drop) }) })
	lp.afterAck = func(prefix string) {
		if prefix == linkPrefix('b') {
			<-drop
		}
	}
	lp.drop('b')
	locked, unlock := make(chan struct{}), make(chan struct{})
	lockDone := make(chan error, 1)
	var release sync.Once
	go func() {
		lockDone <- withMetaLock(h.dir, func() error {
			close(locked)
			<-unlock
			return nil
		})
	}()
	t.Cleanup(func() { release.Do(func() { close(unlock) }); <-lockDone })
	<-locked
	promoting := make(chan struct{})
	done := make(chan error, 1)
	revision := h.a.meta.NextLinkRevision
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		done <- h.a.linkAddressSession(ctx, b, "", func() bool {
			close(promoting) // runSession has received HelloAck and started its health clock
			return h.a.commitNextLink(b, revision)
		})
	}()
	select {
	case <-promoting:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate did not reach promotion")
	}
	time.Sleep(2 * h.a.healthyAfter())
	release.Do(func() { close(unlock) })
	closePeer.Do(func() { close(drop) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not finish after promotion")
	}
	committedTo(t, h, b, a)
	if !h.a.sessionHealthy() || h.a.currentFailed || !readMeta(t, h.dir).LinkHealthy {
		t.Fatal("HelloAck-based health classification and persisted proof disagree after promotion wait")
	}
}

func TestPrevLinkInFlightRollbackConsumesOnlyMatchingCLIIntent(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		name := "matching"
		if unrelated {
			name = "unrelated"
		}
		t.Run(name, func(t *testing.T) {
			h, lp, a, b := prevLinkHarness(t)
			h.a.meta.Link, h.a.cfg.LinkURL, h.a.meta.PrevLink, h.a.meta.NextLink = b, b, a, ""
			h.a.currentFailed = true
			if err := saveMeta(h.dir, &h.a.meta); err != nil {
				t.Fatal(err)
			}
			gate := make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(gate) }) })
			lp.beforeAck = func(prefix string) {
				if prefix == linkPrefix('a') {
					<-gate
				}
			}
			end := startLinkTurn(t, h, "")
			if lp.session(t) != linkPrefix('a') {
				t.Fatal("rollback did not reach the previous address")
			}
			request, pending := a, ""
			if unrelated {
				u, _ := url.Parse(a)
				u.Path = "/" + linkPrefix('c') + "/"
				request, pending = u.String(), u.String()
			}
			if err := SetPanelLink(h.dir, request); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(gate) })
			eventually(t, func() bool { return h.a.cur.Load() != nil }, "rollback session")
			endSession(t, h)
			_ = end()
			saved := readMeta(t, h.dir)
			if saved.Link != a || saved.PrevLink != "" || saved.NextLink != pending ||
				h.a.linkURL() != a || h.a.nextLink() != pending {
				t.Fatal("rollback did not consume matching intent or preserve unrelated intent")
			}
			end = startLinkTurn(t, h, "")
			want := linkPrefix('a')
			if unrelated {
				want = linkPrefix('c')
			}
			if lp.session(t) != want {
				t.Fatal("next turn did not follow the post-rollback intent")
			}
			if unrelated {
				committedTo(t, h, request, a)
			} else {
				committedTo(t, h, a, "")
			}
			endSession(t, h)
			_ = end()
		})
	}
}

// Pending A after A -> B is an explicit rollback if B is still unproven, even when restart routes it through normal
// next-link promotion. If B did prove healthy, retain B as the new fallback rather than discarding that proof.
func TestPrevLinkPendingRetainedFallbackAfterRestart(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		name := "unproven"
		if healthy {
			name = "healthy"
		}
		t.Run(name, func(t *testing.T) {
			h, lp, a, b := prevLinkHarness(t)
			if !healthy {
				h.a.resetAfter = time.Minute
			}
			end := startLinkTurn(t, h, "")
			if lp.session(t) != linkPrefix('b') {
				t.Fatal("first switch did not use B")
			}
			committedTo(t, h, b, a)
			if healthy {
				eventually(t, func() bool { return readMeta(t, h.dir).LinkHealthy }, "persisted proof of B")
			}
			if err := SetPanelLink(h.dir, a); err != nil {
				t.Fatal(err)
			}
			endSession(t, h)
			_ = end()
			client := h.a.linkHTTPClient
			h.a = h.newAgent()
			h.a.linkHTTPClient = client
			if h.a.linkURL() != b || h.a.prevLink() != a || h.a.nextLink() != a {
				t.Fatal("restart did not restore pending retained-fallback intent")
			}
			end = startLinkTurn(t, h, "")
			if lp.session(t) != linkPrefix('a') {
				t.Fatal("restarted agent did not promote pending A")
			}
			prev := ""
			if healthy {
				prev = b
			}
			committedTo(t, h, a, prev)
			saved := readMeta(t, h.dir)
			if saved.PrevLink == saved.Link || saved.LinkHealthy ||
				(!healthy && saved.LinkSince != 0) || (healthy && saved.LinkSince == 0) {
				t.Fatal("return to retained fallback kept self-fallback state or discarded proven B")
			}
			endSession(t, h)
			_ = end()
		})
	}
}
