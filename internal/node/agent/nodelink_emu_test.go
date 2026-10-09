package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"google.golang.org/protobuf/proto"
)

// nodeLinkEmu plays the edge edition's Worker and its NodeLink Durable Objects (edge/worker/src/nodelink.ts) in Go, so the
// real agent can talk to the real fleet.Fleet.Link driver without Cloudflare. It implements fleet.Remote.
//
// Every method of linkObject below is a port of the method of the same name in nodelink.ts; read the two side by side.
// Where this file decides something the TypeScript leaves to the platform (what a socket's readyState does when the peer
// closes, what an RPC does when the object resets), the comment says so. Deliberate differences from nodelink.ts:
//   - the Forget flag of LinkOut (design §6.1) is honoured here; nodelink.ts does not read it yet (step 6);
//   - the open event carries the generation (LinkIn.Generation); nodelink.ts does not send it yet (step 6);
//   - the timers are real, the clock is time.Now() plus an offset moved by advance(): advance() does not re-arm a timer.
//     runAlarm() is the platform calling alarm(); it fails the test unless an alarm is armed and due, as the platform
//     would never call alarm() otherwise (so a missing arm() shows up); advanceToAlarm() moves the clock to the armed time;
//   - webSocketError is not emulated: every failed read becomes a close event with code 1006.
//
// One goroutine per object drains a FIFO, so events are handled one at a time in arrival order (the input gate plus
// blockConcurrencyWhile). Everything a goroutine starts stops when the test ends.
type nodeLinkEmu struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Constants of nodelink.ts. budget and stepMax can be shortened with setLimits for the moment a step must run out of time.
	budget    time.Duration // BLOCK_MS: what the Go calls of one serial block share (guarded by mu)
	stepMax   time.Duration // STEP_MS: the most one Go call may take (guarded by mu)
	handshake time.Duration // HANDSHAKE_MS
	minGap    time.Duration // MIN_ALARM_GAP_MS

	fl     atomic.Pointer[fleet.Fleet]
	marker http.Handler
	offset atomic.Int64 // nanoseconds added to time.Now()

	mu     sync.Mutex
	objs   map[string]*linkObject
	socks  []*linkSocket // every socket ever accepted, for the cleanup
	closed bool
	before func(context.Context, fleet.LinkIn) // test hook, runs inside a step's (or the accept's) Go call

	violation func(string) // called when the own-node guard fires; nil fails the test
}

// emuAcceptKind is the Kind the before hook sees for the Go call of LinkAuth (LinkAccept).
const emuAcceptKind fleet.LinkKind = "accept"

func newNodeLinkEmu(t *testing.T) *nodeLinkEmu {
	t.Helper()
	e := &nodeLinkEmu{t: t, budget: 25 * time.Second, stepMax: 20 * time.Second, handshake: 10 * time.Second,
		minGap: time.Second, objs: map[string]*linkObject{}}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(e.close)
	return e
}

// bind gives the emulator the panel (the Fleet is built with the emulator as its Remote, so it comes second).
func (e *nodeLinkEmu) bind(f *fleet.Fleet) {
	e.marker = f.LinkHandler()
	e.fl.Store(f)
}

// now is the clock of the Worker and, through fleet.Config.Now, of the Fleet.
func (e *nodeLinkEmu) now() time.Time {
	return time.Now().Add(time.Duration(e.offset.Load())).Round(0)
}

func (e *nodeLinkEmu) nowMs() int64 { return e.now().UnixMilli() }

// advance moves the emulated clock forward. Timers are not re-armed: call runAlarm.
func (e *nodeLinkEmu) advance(d time.Duration) { e.offset.Add(int64(d)) }

// limits are BLOCK_MS and STEP_MS: tests shorten them for the moment a step is meant to run out of time.
func (e *nodeLinkEmu) limits() (budget, stepMax time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.budget, e.stepMax
}

func (e *nodeLinkEmu) setLimits(budget, stepMax time.Duration) {
	e.mu.Lock()
	e.budget, e.stepMax = budget, stepMax
	e.mu.Unlock()
}

// setBefore installs a hook that runs inside every step's Go call, before Fleet.Link (a test can hang a step with it).
func (e *nodeLinkEmu) setBefore(fn func(context.Context, fleet.LinkIn)) {
	e.mu.Lock()
	e.before = fn
	e.mu.Unlock()
}

func (e *nodeLinkEmu) beforeHook() func(context.Context, fleet.LinkIn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.before
}

func (e *nodeLinkEmu) object(id string) *linkObject {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.objs[id]
	if o == nil {
		o = &linkObject{e: e, id: id, wake: make(chan struct{}, 1), waiters: map[string]*emuWaiter{},
			failNext: map[fleet.LinkKind]int{}, loseState: map[fleet.LinkKind]int{}}
		e.objs[id] = o
		if !e.closed {
			e.wg.Add(1)
			go o.loop()
		}
	}
	return o
}

func (e *nodeLinkEmu) track(s *linkSocket) {
	e.mu.Lock()
	closed := e.closed
	e.socks = append(e.socks, s)
	e.mu.Unlock()
	if closed {
		s.vanished.Store(true)
		_ = s.conn.CloseNow()
	}
}

// close stops everything the emulator started and fails the test if a goroutine is left behind.
func (e *nodeLinkEmu) close() {
	e.cancel()
	e.mu.Lock()
	e.closed = true
	objs := make([]*linkObject, 0, len(e.objs))
	for _, o := range e.objs {
		objs = append(objs, o)
	}
	socks := append([]*linkSocket(nil), e.socks...)
	e.mu.Unlock()
	for _, o := range objs {
		o.deleteAlarm()
		o.rejectWaiters()
	}
	for _, s := range socks {
		s.vanished.Store(true)
		_ = s.conn.CloseNow()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		e.t.Errorf("the NodeLink emulator left goroutines running")
	}
}

// guard is the own-node rule: a step must never await a call into its own node's object (it would wait for the very block
// that made the call).
func (e *nodeLinkEmu) guard(ctx context.Context, nodeID, what string) error {
	if own, _ := ctx.Value(emuOwnNode{}).(string); own != "" && own == nodeID {
		msg := fmt.Sprintf("%s(%s) was called from inside that node's own link step: a step must never await its own NodeLink", what, nodeID)
		e.mu.Lock()
		report := e.violation
		e.mu.Unlock()
		if report != nil {
			report(msg)
		} else {
			e.t.Error(msg)
		}
		return errors.New("own-node call from a link step")
	}
	return nil
}

// onViolation replaces "fail the test" as the reaction of the own-node guard (the test of the guard itself).
func (e *nodeLinkEmu) onViolation(fn func(string)) {
	e.mu.Lock()
	e.violation = fn
	e.mu.Unlock()
}

type emuOwnNode struct{}

var errEmuLinkLost = errors.New("link lost")

// errEmuTimeout is NodeLink's "timeout"; the step 6 adapter turns it into context.DeadlineExceeded, and so does this one.
var errEmuTimeout = fmt.Errorf("timeout: %w", context.DeadlineExceeded)

// ---------------------------------------------------------------------------------------------------------------------
// The Worker

// Worker is the Worker's fetch for the agent link: the panel's marker handler picks the node, the node's object takes the
// upgrade (shell.ts forwardLink). Everything else under the prefix is the panel's answer (the decoy).
func (e *nodeLinkEmu) Worker(prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		inner := r.Clone(r.Context())
		inner.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, prefix)
		rec := httptest.NewRecorder()
		e.marker.ServeHTTP(rec, inner)
		if id := rec.Header().Get(fleet.LinkMarkerHeader); rec.Code == http.StatusNoContent && id != "" {
			e.object(id).fetch(w, r)
			return
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// ---------------------------------------------------------------------------------------------------------------------
// fleet.Remote and the helpers a test drives the platform with

// Ask is the Remote seam: NodeLink.ask with the caller's absolute deadline.
func (e *nodeLinkEmu) Ask(ctx context.Context, nodeID, requestID string, frame *agentv1.ConnectResponse, deadline time.Time) (*agentv1.ConnectRequest, error) {
	if err := e.guard(ctx, nodeID, "Ask"); err != nil {
		return nil, err
	}
	raw, err := proto.Marshal(frame)
	if err != nil {
		return nil, err
	}
	reply, err := e.object(nodeID).ask(requestID, raw, deadline.UnixMilli())
	if err != nil || reply == nil {
		return nil, err
	}
	var out agentv1.ConnectRequest
	if err := proto.Unmarshal(reply, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Close is the Remote seam: NodeLink.close(4000, reason).
func (e *nodeLinkEmu) Close(ctx context.Context, nodeID, reason string) error {
	if err := e.guard(ctx, nodeID, "Close"); err != nil {
		return err
	}
	o := e.object(nodeID)
	if !o.do(func() {
		o.serial(func() {
			s := o.liveSocket()
			if s == nil {
				return
			}
			o.shut(s, 4000, reason)
			o.dropLive(true)
			o.arm()
		})
	}) {
		return errors.New("link emulator stopped")
	}
	return nil
}

// poke is NodeLink.poke: the step 6 fan-out of StateChanged, which a test calls by hand.
func (e *nodeLinkEmu) poke(ctx context.Context, nodeID string) error {
	if err := e.guard(ctx, nodeID, "Poke"); err != nil {
		return err
	}
	o := e.object(nodeID)
	if !o.do(func() {
		o.serial(func() {
			if o.liveSocket() == nil {
				return // nothing is connected: the next open step reads the new state anyway
			}
			o.poke = true
			o.setAlarm(e.nowMs())
		})
	}) {
		return errors.New("link emulator stopped")
	}
	return nil
}

// runAlarm is the platform calling alarm(). The platform does so only for an armed alarm whose time has come, so this
// fails the test when none is armed or it is not due yet (move the clock with advance or advanceToAlarm first).
func (e *nodeLinkEmu) runAlarm(nodeID string) {
	o := e.object(nodeID)
	o.do(func() {
		o.tmu.Lock()
		target := o.alarmTarget
		o.tmu.Unlock()
		now := e.nowMs()
		if target == 0 {
			e.t.Errorf("runAlarm(%s): no alarm is armed, the platform would not call alarm()", nodeID)
			return
		}
		if target > now {
			e.t.Errorf("runAlarm(%s): the alarm is armed for %d ms from now, the platform would not call alarm() yet", nodeID, target-now)
			return
		}
		o.deleteAlarm()
		o.onAlarm()
	})
}

// advanceToAlarm moves the clock to the time the object armed its alarm for (when it is still ahead) and runs it.
func (e *nodeLinkEmu) advanceToAlarm(nodeID string) {
	o := e.object(nodeID)
	var target int64
	o.do(func() {
		o.tmu.Lock()
		target = o.alarmTarget
		o.tmu.Unlock()
	})
	if target == 0 {
		e.t.Errorf("advanceToAlarm(%s): no alarm is armed", nodeID)
		return
	}
	if d := target - e.nowMs(); d > 0 {
		e.advance(time.Duration(d+1) * time.Millisecond)
	}
	e.runAlarm(nodeID)
}

// reset is a deploy or an eviction: every socket vanishes without a close event, the waiters of ask are dropped (their
// RPCs fail), storage and the alarm stay.
func (e *nodeLinkEmu) reset(nodeID string) {
	o := e.object(nodeID)
	o.do(func() {
		for _, s := range o.sockets {
			s.vanished.Store(true)
			s.state.Store(emuClosed)
			_ = s.conn.CloseNow()
		}
		o.sockets = nil
		o.rejectWaiters()
	})
}

// failNext makes the next step of this kind fail after Fleet.Link returned: the database writes are committed, the result
// is thrown away, the object closes 1011, sends no frame and writes nothing (what an error in the step does on workerd).
func (e *nodeLinkEmu) failNext(nodeID string, kind fleet.LinkKind) {
	o := e.object(nodeID)
	o.do(func() { o.failNext[kind]++ })
}

// loseState makes the next step of this kind lose the state it returns: frames, replies and alarm are applied, the state
// string is not written. This is the harsher case and cannot happen on workerd, where the output gate holds the frames
// until the state write is durable (no state write, no frames); the real path to a stale state is failNext. It tests that
// the core recovers from a state that is behind the database.
func (e *nodeLinkEmu) loseState(nodeID string, kind fleet.LinkKind) {
	o := e.object(nodeID)
	o.do(func() { o.loseState[kind]++ })
}

// What a test can look at.

// emuStep is one Go step of an object.
type emuStep struct {
	Gen  uint64         // the generation of the session the step belongs to
	Kind fleet.LinkKind //
	Msg  string         // frame, request: the message field name
	Err  bool           // the step failed (or its result was thrown away)
}

// emuClose is a close the object asked its socket for.
type emuClose struct {
	Gen    uint64 // 0 = a socket that never authenticated
	Code   int
	Reason string
}

// emuCloseEvent is a close the object heard from a socket.
type emuCloseEvent struct {
	Gen  uint64
	Code int
}

// emuSent is a ConnectResponse the object sent to the agent.
type emuSent struct {
	Gen   uint64
	Frame *agentv1.ConnectResponse
}

func (e *nodeLinkEmu) steps(nodeID string) []emuStep {
	o := e.object(nodeID)
	o.logMu.Lock()
	defer o.logMu.Unlock()
	return append([]emuStep(nil), o.stepLog...)
}

func (e *nodeLinkEmu) sent(nodeID string) []emuSent {
	o := e.object(nodeID)
	o.logMu.Lock()
	defer o.logMu.Unlock()
	return append([]emuSent(nil), o.sentLog...)
}

// dropped counts the messages the object heard from a socket that no longer counted.
func (e *nodeLinkEmu) dropped(nodeID string) int {
	o := e.object(nodeID)
	o.logMu.Lock()
	defer o.logMu.Unlock()
	return o.dropped
}

func (o *linkObject) drop() {
	o.logMu.Lock()
	o.dropped++
	o.logMu.Unlock()
}

func (e *nodeLinkEmu) closes(nodeID string) []emuClose {
	o := e.object(nodeID)
	o.logMu.Lock()
	defer o.logMu.Unlock()
	return append([]emuClose(nil), o.closeLog...)
}

func (e *nodeLinkEmu) closeEvents(nodeID string) []emuCloseEvent {
	o := e.object(nodeID)
	o.logMu.Lock()
	defer o.logMu.Unlock()
	return append([]emuCloseEvent(nil), o.eventLog...)
}

// emuStorage is the object's persistent state.
type emuStorage struct {
	Gen, Live uint64
	HasState  bool
	AlarmAt   int64
	Poke      bool
	AlarmSet  bool // the platform alarm is armed
	Sockets   int  // sockets that are open
}

// kvEmpty: nothing is stored (the platform alarm may still be armed for a socket that has not authenticated).
func (s emuStorage) kvEmpty() bool {
	return s.Gen == 0 && s.Live == 0 && !s.HasState && s.AlarmAt == 0 && !s.Poke
}

func (s emuStorage) empty() bool { return s.kvEmpty() && !s.AlarmSet }

func (e *nodeLinkEmu) storage(nodeID string) emuStorage {
	o := e.object(nodeID)
	var out emuStorage
	if !o.do(func() {
		out = emuStorage{Gen: o.gen, Live: o.live, HasState: o.state != "", AlarmAt: o.alarmAt, Poke: o.poke, Sockets: len(o.openSockets())}
	}) {
		return out
	}
	o.tmu.Lock()
	out.AlarmSet = o.alarmTarget != 0
	o.tmu.Unlock()
	return out
}

// ---------------------------------------------------------------------------------------------------------------------
// One NodeLink object

type linkAttachment struct {
	nonce      []byte
	deadline   int64
	audience   string
	generation uint64
}

const (
	emuOpen int32 = iota
	emuClosing
	emuClosed
)

type linkSocket struct {
	conn     *websocket.Conn
	state    atomic.Int32 // readyState
	vanished atomic.Bool  // reset(): gone without a trace
	att      linkAttachment
}

type emuReply struct {
	frame []byte
	err   error
}

type emuWaiter struct {
	ch   chan emuReply
	once sync.Once
}

func (w *emuWaiter) settle(frame []byte, err error) {
	w.once.Do(func() { w.ch <- emuReply{frame: frame, err: err} })
}

type linkObject struct {
	e  *nodeLinkEmu
	id string

	// The FIFO.
	qmu   sync.Mutex
	queue []func()
	wake  chan struct{}

	// Storage (sync kv). Only the object's goroutine touches it; tests read through do().
	gen, live uint64
	state     string
	alarmAt   int64
	poke      bool
	sockets   []*linkSocket
	until     time.Time // the end of the current serial block's Go budget
	forget    bool      // a step asked to forget the node: storage and alarm go after the block
	failNext  map[fleet.LinkKind]int
	loseState map[fleet.LinkKind]int

	// The platform alarm.
	tmu         sync.Mutex
	timer       *time.Timer
	timerSeq    uint64
	alarmTarget int64

	// The waiters of ask.
	wmu     sync.Mutex
	waiters map[string]*emuWaiter

	logMu    sync.Mutex
	dropped  int // messages from a socket that was not open (or not the open session's): dropped, as the platform would
	stepLog  []emuStep
	sentLog  []emuSent
	closeLog []emuClose
	eventLog []emuCloseEvent
}

func (o *linkObject) enqueue(fn func()) {
	o.qmu.Lock()
	o.queue = append(o.queue, fn)
	o.qmu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *linkObject) loop() {
	defer o.e.wg.Done()
	for {
		for {
			o.qmu.Lock()
			if len(o.queue) == 0 {
				o.qmu.Unlock()
				break
			}
			fn := o.queue[0]
			o.queue = o.queue[1:]
			o.qmu.Unlock()
			if o.e.ctx.Err() != nil {
				return
			}
			fn()
		}
		select {
		case <-o.e.ctx.Done():
			return
		case <-o.wake:
		}
	}
}

// do runs fn as one event of the object and waits for it. False when the emulator stopped first.
func (o *linkObject) do(fn func()) bool {
	done := make(chan struct{})
	o.enqueue(func() { defer close(done); fn() })
	select {
	case <-done:
		return true
	case <-o.e.ctx.Done():
		return false
	}
}

// serial is blockConcurrencyWhile: the event runs alone and its Go calls share the block budget. The Forget the last step
// asked for is carried out after the block (design §6.1).
func (o *linkObject) serial(work func()) {
	budget, _ := o.e.limits()
	o.until = time.Now().Add(budget)
	work()
	if o.forget {
		// deleteAll(), then arm(): the handshake deadline of a socket that has not authenticated yet must survive.
		o.forget = false
		o.gen, o.live, o.state, o.alarmAt, o.poke = 0, 0, "", 0, false
		o.arm()
	}
}

// emuCall is call(): a Go call inside a serial block takes at most stepMax and at most what the block has left, and none
// at all when that is gone. The call runs in its own goroutine, raced against the time left; a result that comes late is
// thrown away. The context it gets is cancelled then, and tagged with the node for the own-node guard.
func emuCall[T any](o *linkObject, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	_, stepMax := o.e.limits()
	left := min(stepMax, time.Until(o.until))
	if left <= 0 {
		return zero, errors.New("panel link budget spent")
	}
	ctx, cancel := context.WithTimeout(context.WithValue(o.e.ctx, emuOwnNode{}, o.id), left)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	o.e.wg.Add(1)
	go func() {
		defer o.e.wg.Done()
		v, err := fn(ctx)
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return zero, errors.New("panel link call timed out")
	}
}

func (o *linkObject) nowMs() int64 { return o.e.nowMs() }

func (o *linkObject) openSockets() []*linkSocket {
	var out, keep []*linkSocket
	for _, s := range o.sockets {
		if s.state.Load() != emuClosed {
			keep = append(keep, s)
		}
		if s.state.Load() == emuOpen && !s.vanished.Load() {
			out = append(out, s)
		}
	}
	o.sockets = keep
	return out
}

func (o *linkObject) liveSocket() *linkSocket {
	if o.live == 0 {
		return nil
	}
	for _, s := range o.openSockets() {
		if s.att.generation == o.live {
			return s
		}
	}
	return nil
}

// send is ws.send: a socket that is not open drops the frame (the real one throws and the event goes on).
func (o *linkObject) send(s *linkSocket, data []byte) bool {
	if s.state.Load() != emuOpen || s.vanished.Load() {
		return false
	}
	ctx, cancel := context.WithTimeout(o.e.ctx, 5*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageBinary, data) == nil
}

func validCloseCode(code int) bool {
	return code >= 1000 && code <= 4999 && code != 1005 && code != 1006 && code != 1015
}

// shut is ws.close: it throws (and the socket stays open) for a reserved code or a reason over 123 bytes; a socket that is
// already closing is left alone. The close handshake finishes in the background.
func (o *linkObject) shut(s *linkSocket, code int, reason string) {
	if !validCloseCode(code) || len(reason) > 123 {
		return
	}
	if !s.state.CompareAndSwap(emuOpen, emuClosing) {
		return
	}
	o.logMu.Lock()
	o.closeLog = append(o.closeLog, emuClose{Gen: s.att.generation, Code: code, Reason: reason})
	o.logMu.Unlock()
	o.e.wg.Add(1)
	go func() {
		defer o.e.wg.Done()
		_ = s.conn.Close(websocket.StatusCode(code), reason)
	}()
}

// read turns what a socket delivers into events of the object, in arrival order.
func (o *linkObject) read(s *linkSocket) {
	defer o.e.wg.Done()
	for {
		typ, data, err := s.conn.Read(o.e.ctx)
		if err != nil {
			if s.vanished.Load() || o.e.ctx.Err() != nil {
				return
			}
			code := int(websocket.CloseStatus(err))
			if code < 0 {
				code = 1006
			}
			o.enqueue(func() { o.webSocketClose(s, code) })
			return
		}
		o.enqueue(func() { o.webSocketMessage(s, typ, data) })
	}
}

func (o *linkObject) logStep(step emuStep) {
	o.logMu.Lock()
	o.stepLog = append(o.stepLog, step)
	o.logMu.Unlock()
}

// fetch is the agent's upgrade, forwarded by the Worker once the panel's marker named this node.
func (o *linkObject) fetch(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "Upgrade Required", http.StatusUpgradeRequired)
		return
	}
	audience := r.Host
	// The challenge needs no state: it is made before the block.
	nonce, frame, err := fleet.LinkChallenge(audience)
	if err != nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	if !o.do(func() {
		o.serial(func() {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return // Accept has answered
			}
			conn.SetReadLimit(agentlink.MaxFrameSize)
			s := &linkSocket{conn: conn, att: linkAttachment{nonce: nonce, deadline: o.nowMs() + o.e.handshake.Milliseconds(), audience: audience}}
			o.sockets = append(o.sockets, s)
			o.e.track(s)
			o.e.wg.Add(1)
			go o.read(s)
			o.send(s, frame)
			o.arm()
		})
	}) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (o *linkObject) webSocketMessage(s *linkSocket, typ websocket.MessageType, data []byte) {
	if s.vanished.Load() {
		return
	}
	o.serial(func() {
		// A socket that is not open any more no longer speaks: after a failed step (1011) its queued frames must not reach Go.
		if s.state.Load() != emuOpen {
			o.drop()
			return
		}
		switch {
		case s.att.generation == 0:
			// The first message of a socket is its LinkAuth. Anything else, or anything late, is refused.
			if typ == websocket.MessageText || o.nowMs() > s.att.deadline {
				o.shut(s, 1008, "handshake")
			} else {
				o.authenticate(s, data)
			}
		case s.att.generation == o.live:
			if typ == websocket.MessageText {
				o.shut(s, 1008, "binary frames only")
				o.dropLive(true)
			} else {
				o.run(s, emuEvent{kind: fleet.LinkFrame, at: o.nowMs(), frame: data}, s.att.generation)
			}
		default: // a frame of a socket that has been replaced: dropped
			o.drop()
		}
		o.arm()
	})
}

// webSocketClose: the peer's close frame is answered by the websocket library; here the socket stops being open. (Whether
// frames that arrived before the close are still heard is the platform's choice; they are, here.)
func (o *linkObject) webSocketClose(s *linkSocket, code int) {
	if s.vanished.Load() {
		return
	}
	o.logMu.Lock()
	o.eventLog = append(o.eventLog, emuCloseEvent{Gen: s.att.generation, Code: code})
	o.logMu.Unlock()
	s.state.Store(emuClosed)
	_ = s.conn.CloseNow()
	o.serial(func() { o.lost(s) })
}

func (o *linkObject) onAlarm() {
	o.serial(func() {
		// The session's socket vanished without a close event (a deploy or reset, or a socket stuck closing after our 1011).
		if o.live != 0 && o.liveSocket() == nil {
			o.dropLive(true)
		}
		now := o.nowMs()
		for _, s := range o.openSockets() {
			if s.att.generation == 0 && s.att.deadline <= now {
				o.shut(s, 1008, "handshake timeout")
			}
		}
		if o.poke {
			o.poke = false
			if s := o.liveSocket(); s != nil {
				o.run(s, emuEvent{kind: fleet.LinkDesired, at: now}, s.att.generation)
			}
		}
		if o.alarmAt != 0 && o.alarmAt <= o.nowMs() {
			o.alarmAt = 0
			if s := o.liveSocket(); s != nil {
				o.run(s, emuEvent{kind: fleet.LinkAlarm, at: o.nowMs()}, s.att.generation)
			}
		}
		o.arm()
	})
}

// ask sends a request frame through a session step and waits for the reply a later step resolves; the wait is outside the
// serial block. "link lost" when there is no session or it ends, "timeout" at deadlineAt.
func (o *linkObject) ask(id string, frame []byte, deadlineAt int64) ([]byte, error) {
	var (
		w       *emuWaiter
		timer   *time.Timer
		early   error
		sent    bool
		entered = o.do(func() { // the input gate lets the call in between two blocks
			o.wmu.Lock()
			_, dup := o.waiters[id]
			o.wmu.Unlock()
			if dup {
				early = errors.New("duplicate request id")
				return
			}
			if o.liveSocket() == nil {
				early = errEmuLinkLost
				return
			}
			waitMs := deadlineAt - o.nowMs()
			if waitMs <= 0 {
				early = errEmuTimeout
				return
			}
			w = &emuWaiter{ch: make(chan emuReply, 1)}
			o.wmu.Lock()
			o.waiters[id] = w
			o.wmu.Unlock()
			timer = time.AfterFunc(time.Duration(waitMs)*time.Millisecond, func() {
				o.wmu.Lock()
				mine := o.waiters[id] == w
				if mine {
					delete(o.waiters, id)
				}
				o.wmu.Unlock()
				if mine {
					w.settle(nil, errEmuTimeout)
				}
			})
			o.serial(func() {
				// Expired, or its session ended, while it waited for the block: nothing is stepped or sent.
				o.wmu.Lock()
				mine := o.waiters[id] == w
				o.wmu.Unlock()
				if !mine {
					sent = true
					return
				}
				s := o.liveSocket()
				if s == nil {
					return
				}
				o.run(s, emuEvent{kind: fleet.LinkRequest, at: o.nowMs(), requestID: id, frame: frame, deadlineAt: deadlineAt}, s.att.generation)
				o.arm()
				sent = true
			})
		})
	)
	defer func() {
		if !entered {
			return // the emulator stopped: the event may still be running, and nothing waits for the answer
		}
		if timer != nil {
			timer.Stop()
		}
		if w != nil {
			o.wmu.Lock()
			if o.waiters[id] == w {
				delete(o.waiters, id)
			}
			o.wmu.Unlock()
		}
	}()
	if !entered {
		return nil, errors.New("link emulator stopped")
	}
	if early != nil {
		return nil, early
	}
	if !sent {
		return nil, errEmuLinkLost
	}
	select {
	case r := <-w.ch:
		return r.frame, r.err
	case <-o.e.ctx.Done():
		return nil, o.e.ctx.Err()
	}
}

// authenticate is LinkAuth: Go decides. A proven node takes the link over from any other socket, whose session ends.
func (o *linkObject) authenticate(s *linkSocket, auth []byte) {
	type accept struct {
		serial   string
		notAfter time.Time
		frame    []byte
		ok       bool
	}
	att := s.att
	before := o.e.beforeHook()
	res, err := emuCall(o, func(ctx context.Context) (accept, error) {
		if before != nil {
			before(ctx, fleet.LinkIn{NodeID: o.id, Kind: emuAcceptKind})
		}
		serial, notAfter, frame, ok := o.e.fl.Load().LinkAccept(ctx, o.id, att.audience, att.nonce, auth)
		return accept{serial, notAfter, frame, ok}, nil
	})
	if err != nil {
		o.shut(s, 1011, "link error")
		return
	}
	if !res.ok {
		o.shut(s, 1008, "unauthorized")
		return
	}
	o.gen++
	generation := o.gen
	s.att.generation = generation
	// Only the other authenticated sockets are superseded; a handshake in progress stays, as on the VPS.
	for _, other := range o.openSockets() {
		if theirs := other.att.generation; theirs != 0 && theirs != generation {
			o.shut(other, 4000, "superseded")
		}
	}
	o.dropLive(false)
	o.live = generation
	o.send(s, res.frame)
	o.run(s, emuEvent{kind: fleet.LinkOpen, at: o.nowMs(), generation: generation, certSerial: res.serial, certNotAfter: res.notAfter}, generation)
}

type emuEvent struct {
	kind         fleet.LinkKind
	at           int64
	generation   uint64
	certSerial   string
	certNotAfter time.Time
	frame        []byte
	requestID    string
	deadlineAt   int64
}

// emuMessage names the oneof field of a ConnectRequest (frame) or ConnectResponse (request).
func emuMessage(kind fleet.LinkKind, raw []byte) string {
	var m proto.Message
	switch kind {
	case fleet.LinkFrame:
		m = &agentv1.ConnectRequest{}
	case fleet.LinkRequest:
		m = &agentv1.ConnectResponse{}
	default:
		return ""
	}
	if proto.Unmarshal(raw, m) != nil {
		return "undecodable"
	}
	return emuOneof(m)
}

func emuOneof(m proto.Message) string {
	r := m.ProtoReflect()
	od := r.Descriptor().Oneofs().ByName("message")
	if od == nil {
		return ""
	}
	if f := r.WhichOneof(od); f != nil {
		return string(f.Name())
	}
	return ""
}

func takeFault(faults map[fleet.LinkKind]int, kind fleet.LinkKind) bool {
	if faults[kind] == 0 {
		return false
	}
	faults[kind]--
	return true
}

// run is one session step on the Go side, applied to the session's socket (nil once it is gone). The state is written
// first, then the frames go out. An error or a spent budget closes 1011, rejects the waiters and writes nothing.
func (o *linkObject) run(s *linkSocket, ev emuEvent, gen uint64) {
	in := fleet.LinkIn{NodeID: o.id, State: o.state, Kind: ev.kind, At: time.UnixMilli(ev.at), Generation: ev.generation,
		CertSerial: ev.certSerial, CertNotAfter: ev.certNotAfter, Frame: ev.frame, RequestID: ev.requestID}
	if ev.kind == fleet.LinkRequest {
		in.DeadlineAt = time.UnixMilli(ev.deadlineAt)
	}
	step := emuStep{Gen: gen, Kind: ev.kind, Msg: emuMessage(ev.kind, ev.frame)}
	fail, lose := takeFault(o.failNext, ev.kind), takeFault(o.loseState, ev.kind)
	before := o.e.beforeHook()
	out, err := emuCall(o, func(ctx context.Context) (fleet.LinkOut, error) {
		if before != nil {
			before(ctx, in)
		}
		out, err := o.e.fl.Load().Link(ctx, in)
		if err == nil && fail {
			err = errors.New("injected failure after the step committed")
		}
		return out, err
	})
	if err != nil {
		step.Err = true
		o.logStep(step)
		// The session stays "live" until the socket's close event (or the next accept) runs its closed step.
		if s != nil {
			o.shut(s, 1011, "link error")
		}
		o.rejectWaiters()
		return
	}
	o.logStep(step)
	if !lose {
		o.state = out.State
	}
	if s != nil {
		for _, raw := range out.Frames {
			if !o.send(s, raw) {
				continue
			}
			var m agentv1.ConnectResponse
			if proto.Unmarshal(raw, &m) == nil {
				o.logMu.Lock()
				o.sentLog = append(o.sentLog, emuSent{Gen: gen, Frame: &m})
				o.logMu.Unlock()
			}
		}
	}
	for _, r := range out.Replies {
		o.wmu.Lock()
		w := o.waiters[r.RequestID]
		o.wmu.Unlock()
		if w != nil {
			w.settle(r.Frame, nil)
		}
	}
	if out.AlarmAt.IsZero() {
		o.alarmAt = 0
	} else {
		o.alarmAt = max(out.AlarmAt.UnixMilli(), o.nowMs()+o.e.minGap.Milliseconds())
	}
	if out.Forget {
		o.forget = true
	}
	if s != nil && out.Close != nil {
		o.shut(s, int(out.Close.Code), out.Close.Reason)
		o.dropLive(true)
	}
}

// dropLive: the open session is over: its waiters fail, its alarm goes, and Go gets the closed step.
func (o *linkObject) dropLive(owned bool) {
	_ = owned // the driver has no use for it: closed{owned:false} is not read as "another agent connected"
	if o.live == 0 {
		return
	}
	gen := o.live
	o.live = 0
	o.rejectWaiters()
	o.run(nil, emuEvent{kind: fleet.LinkClosed, at: o.nowMs()}, gen)
	o.alarmAt = 0
}

// lost: a socket closed or failed; only the open session's socket matters.
func (o *linkObject) lost(s *linkSocket) {
	if g := s.att.generation; g != 0 && g == o.live {
		o.dropLive(true)
	}
	o.arm()
}

func (o *linkObject) rejectWaiters() {
	o.wmu.Lock()
	ws := make([]*emuWaiter, 0, len(o.waiters))
	for _, w := range o.waiters {
		ws = append(ws, w)
	}
	o.waiters = map[string]*emuWaiter{} // an ask still queued then finds its waiter gone and sends nothing
	o.wmu.Unlock()
	for _, w := range ws {
		w.settle(nil, errEmuLinkLost)
	}
}

// arm is one alarm for everything pending; none when nothing is.
func (o *linkObject) arm() {
	at := int64(0)
	set := false
	take := func(v int64) {
		if !set || v < at {
			at, set = v, true
		}
	}
	if o.alarmAt != 0 {
		take(o.alarmAt)
	}
	for _, s := range o.openSockets() {
		if s.att.generation == 0 {
			take(s.att.deadline)
		}
	}
	if o.poke {
		take(0)
	}
	if !set {
		o.deleteAlarm()
		return
	}
	o.setAlarm(max(at, o.nowMs()))
}

// setAlarm arms the one platform alarm; it fires once, in real time, as an alarm event of the object.
func (o *linkObject) setAlarm(ms int64) {
	o.tmu.Lock()
	defer o.tmu.Unlock()
	if o.timer != nil {
		o.timer.Stop()
	}
	o.timerSeq++
	seq := o.timerSeq
	o.alarmTarget = max(ms, 1)
	d := max(time.Duration(ms-o.nowMs())*time.Millisecond, 0)
	o.timer = time.AfterFunc(d, func() { o.alarmFired(seq) })
}

func (o *linkObject) deleteAlarm() {
	o.tmu.Lock()
	defer o.tmu.Unlock()
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
	o.timerSeq++
	o.alarmTarget = 0
}

func (o *linkObject) alarmFired(seq uint64) {
	o.tmu.Lock()
	if seq != o.timerSeq || o.alarmTarget == 0 {
		o.tmu.Unlock()
		return
	}
	o.alarmTarget = 0
	o.timer = nil
	o.tmu.Unlock()
	if o.e.ctx.Err() == nil {
		o.enqueue(o.onAlarm)
	}
}
