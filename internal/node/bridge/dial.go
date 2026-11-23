package bridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	DefaultPoolSize           = 2
	MinPoolSize               = 2
	MaxPoolSize               = 4
	PendingWorkersPerConn     = 4
	MaxPendingWorkersPerConn  = 24
	DialRetryMin              = time.Second
	DialRetryMax              = time.Minute
	DialRetryResetAfter       = 30 * time.Second
	BridgePingInterval        = 15 * time.Second
	BridgePingTimeout         = 10 * time.Second
	BridgeConnectionAge       = time.Hour
	BridgeConnectionAgeJitter = BridgeConnectionAge / 5
	BridgeDrainTimeout        = 10 * time.Minute
	BridgeConnectionBytes     = uint64(1 << 30)
	BridgeRotateCheck         = time.Second
	BridgeDialTimeout         = 15 * time.Second
	BridgeTargetDialTimeout   = 5 * time.Second
	BridgeKeepAlive           = 30 * time.Second
)

type DialIdentity struct {
	NodeID         string
	CertificateDER []byte
	PrivateKey     *ecdsa.PrivateKey
}

type DialSpec struct {
	Dial         plugin.BridgeDial
	Error        string // A bridge-wide configuration error; no socket is opened when set.
	TargetErrors map[string]string
}

type DialState struct {
	PeerNodeID string
	Conns      uint32
	Since      time.Time
	RTT        time.Duration
	Streams    uint64
	BytesIn    uint64
	BytesOut   uint64
	Error      string
}

type dialTiming struct {
	retryMin       time.Duration
	retryMax       time.Duration
	resetAfter     time.Duration
	pingInterval   time.Duration
	pingJitter     time.Duration
	pingTimeout    time.Duration
	connectionAge  time.Duration
	ageJitter      time.Duration
	drainTimeout   time.Duration
	rotateCheck    time.Duration
	startStagger   time.Duration
	dialTimeout    time.Duration
	targetTimeout  time.Duration
	keepAlive      time.Duration
	streamIdle     time.Duration
	pendingWorkers int
	retryJitter    func(time.Duration) time.Duration
}

func defaultDialTiming() dialTiming {
	return dialTiming{
		retryMin:       DialRetryMin,
		retryMax:       DialRetryMax,
		resetAfter:     DialRetryResetAfter,
		pingInterval:   BridgePingInterval,
		pingJitter:     BridgePingInterval / 5,
		pingTimeout:    BridgePingTimeout,
		connectionAge:  BridgeConnectionAge,
		ageJitter:      BridgeConnectionAgeJitter,
		drainTimeout:   BridgeDrainTimeout,
		rotateCheck:    BridgeRotateCheck,
		startStagger:   250 * time.Millisecond,
		dialTimeout:    BridgeDialTimeout,
		targetTimeout:  BridgeTargetDialTimeout,
		keepAlive:      BridgeKeepAlive,
		streamIdle:     BridgeStreamIdleTimeout,
		pendingWorkers: PendingWorkersPerConn,
	}
}

type dialTargets struct {
	byID   map[string]plugin.BridgeTarget
	errors map[string]string
}

type dialRun struct {
	dial     plugin.BridgeDial
	ctx      context.Context
	cancel   context.CancelFunc
	targets  atomic.Pointer[dialTargets]
	pool     atomic.Int32
	poolWake chan struct{}
	drainWG  sync.WaitGroup
	done     chan struct{}
}

type Dialer struct {
	mu          sync.Mutex
	identity    func() DialIdentity
	runs        map[string]*dialRun
	states      map[string]DialState
	timing      dialTiming
	clock       func() time.Time
	panelActive bool
	closed      bool
}

func NewDialer(identity func() DialIdentity) *Dialer {
	return &Dialer{identity: identity, runs: map[string]*dialRun{}, states: map[string]DialState{},
		timing: defaultDialTiming(), clock: time.Now}
}

func (d *Dialer) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	d.mu.Lock()
	d.clock = now
	d.mu.Unlock()
}

// Sync replaces the exit's bridge targets and starts or stops their dial pools.
func (d *Dialer) Sync(ctx context.Context, specs []DialSpec) {
	desired := make(map[string]DialSpec, len(specs))
	for _, spec := range specs {
		desired[spec.Dial.EntryNodeID] = spec
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	for id, run := range d.runs {
		spec, ok := desired[id]
		if !ok {
			delete(d.runs, id)
			run.cancel()
			continue
		}
		if spec.Error != "" {
			delete(d.runs, id)
			run.cancel()
			st := d.states[id]
			st.Conns, st.Since, st.RTT, st.Error = 0, time.Time{}, 0, spec.Error
			d.states[id] = st
			delete(desired, id)
			continue
		}
		normalized, code := normalizeDial(spec.Dial)
		if code != "" {
			delete(d.runs, id)
			run.cancel()
			st := d.states[id]
			st.Conns, st.Since, st.RTT, st.Error = 0, time.Time{}, 0, code
			d.states[id] = st
			delete(desired, id)
			continue
		}
		if sameDial(run.dial, normalized) {
			run.targets.Store(makeDialTargets(normalized.Targets, spec.TargetErrors))
			run.pool.Store(int32(normalized.Pool))
			select {
			case run.poolWake <- struct{}{}:
			default:
			}
			delete(desired, id)
			continue
		}
		delete(d.runs, id)
		run.cancel()
	}
	for id, spec := range desired {
		if spec.Error != "" {
			st := d.states[id]
			st.PeerNodeID, st.Conns, st.Since, st.RTT, st.Error = id, 0, time.Time{}, 0, spec.Error
			d.states[id] = st
			continue
		}
		dial, code := normalizeDial(spec.Dial)
		if code != "" {
			st := d.states[id]
			st.PeerNodeID, st.Conns, st.Since, st.RTT, st.Error = id, 0, time.Time{}, 0, code
			d.states[id] = st
			continue
		}
		runCtx, cancel := context.WithCancel(ctx)
		run := &dialRun{dial: dial, ctx: runCtx, cancel: cancel, poolWake: make(chan struct{}, 1), done: make(chan struct{})}
		run.targets.Store(makeDialTargets(dial.Targets, spec.TargetErrors))
		run.pool.Store(int32(dial.Pool))
		d.runs[id] = run
		st := d.states[id]
		st.PeerNodeID, st.Conns, st.Since, st.RTT, st.Error = id, 0, time.Time{}, 0, ""
		d.states[id] = st
		go d.run(run)
	}
	for id := range d.states {
		if _, wanted := desired[id]; wanted {
			continue
		}
		if _, running := d.runs[id]; running {
			continue
		}
		stillDesired := false
		for _, spec := range specs {
			if spec.Dial.EntryNodeID == id {
				stillDesired = true
				break
			}
		}
		if !stillDesired {
			delete(d.states, id)
		}
	}
	d.mu.Unlock()
}

func makeDialTargets(targets []plugin.BridgeTarget, targetErrors map[string]string) *dialTargets {
	set := &dialTargets{byID: make(map[string]plugin.BridgeTarget, len(targets)), errors: make(map[string]string, len(targetErrors))}
	for _, target := range targets {
		set.byID[target.RelayID] = target
	}
	for id, code := range targetErrors {
		set.errors[id] = code
	}
	return set
}

func sameDial(a, b plugin.BridgeDial) bool {
	return a.EntryNodeID == b.EntryNodeID && a.Host == b.Host && a.Port == b.Port &&
		a.SNI == b.SNI && a.PinSHA256 == b.PinSHA256 && a.Path == b.Path && a.PanelVia == b.PanelVia
}

func normalizeDial(d plugin.BridgeDial) (plugin.BridgeDial, string) {
	if !nodeIDPattern.MatchString(d.EntryNodeID) || d.Host == "" || d.Port == 0 || !validBridgePath(d.Path) ||
		len(d.Targets) == 0 || len(d.Targets) > maxBridgeListeners {
		return plugin.BridgeDial{}, "bad_config"
	}
	if d.SNI == "" && d.PinSHA256 == "" {
		return plugin.BridgeDial{}, "no_verification"
	}
	if d.PinSHA256 != "" {
		pin, err := hex.DecodeString(d.PinSHA256)
		if err != nil || len(pin) != sha256.Size {
			return plugin.BridgeDial{}, "no_verification"
		}
	}
	if d.Pool == 0 {
		d.Pool = DefaultPoolSize
	}
	if d.Pool < MinPoolSize || d.Pool > MaxPoolSize {
		return plugin.BridgeDial{}, "bad_config"
	}
	seen := map[string]bool{}
	for _, target := range d.Targets {
		if !relayIDPattern.MatchString(target.RelayID) || target.Network != "tcp" || target.Port == 0 || seen[target.RelayID] {
			return plugin.BridgeDial{}, "target_not_local"
		}
		seen[target.RelayID] = true
	}
	return d, ""
}

func (d *Dialer) run(run *dialRun) {
	defer close(run.done)
	var wg sync.WaitGroup
	slots := make(map[int]context.CancelFunc)
	nextSlot := 0
	applyPool := func() {
		want := int(run.pool.Load())
		for len(slots) < want {
			slotCtx, cancel := context.WithCancel(run.ctx)
			index := nextSlot
			nextSlot++
			slots[index] = cancel
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.runConnection(run, slotCtx, index)
			}()
		}
		for len(slots) > want {
			highest := -1
			for index := range slots {
				if index > highest {
					highest = index
				}
			}
			slots[highest]()
			delete(slots, highest)
		}
	}
	applyPool()
	for run.ctx.Err() == nil {
		select {
		case <-run.ctx.Done():
		case <-run.poolWake:
			applyPool()
		}
	}
	for _, cancel := range slots {
		cancel()
	}
	wg.Wait()
	run.drainWG.Wait()
	d.mu.Lock()
	if d.runs[run.dial.EntryNodeID] == run {
		delete(d.runs, run.dial.EntryNodeID)
		st := d.states[run.dial.EntryNodeID]
		st.Conns, st.RTT = 0, 0
		d.states[run.dial.EntryNodeID] = st
	}
	d.mu.Unlock()
}

func (d *Dialer) runConnection(run *dialRun, slotCtx context.Context, index int) {
	if index > 0 {
		stagger := time.Duration(index)*d.timing.startStagger + time.Duration(rand.Int64N(int64(d.timing.startStagger)+1))
		if !waitDial(slotCtx, stagger) {
			return
		}
	}
	delay := d.timing.retryMin
	for run.ctx.Err() == nil && slotCtx.Err() == nil {
		identity := d.identity()
		started := d.nowTime()
		conn, proof, err := dialTLSWithTimeout(slotCtx, run.dial, identity, d.timing.dialTimeout, d.timing.keepAlive, nil)
		if err != nil {
			d.setError(run, dialErrorCode(err))
			if !waitDial(slotCtx, d.retryDelay(delay)) {
				return
			}
			delay = min(delay*2, d.timing.retryMax)
			continue
		}
		transport := &http2.Transport{}
		cc, err := transport.NewClientConn(conn)
		if err != nil {
			_ = conn.Close()
			d.setError(run, "http2")
			if !waitDial(slotCtx, d.retryDelay(delay)) {
				return
			}
			delay = min(delay*2, d.timing.retryMax)
			continue
		}
		d.connectionUp(run)
		outcome := d.serveConnection(run, slotCtx, cc, conn, proof)
		if outcome.drain != nil {
			d.connectionDown(run)
			run.drainWG.Add(1)
			go func(old *drainingConnection) {
				defer run.drainWG.Done()
				d.drainConnection(run, old)
			}(outcome.drain)
			if run.ctx.Err() != nil || slotCtx.Err() != nil {
				return
			}
			delay = d.timing.retryMin
			continue
		}
		_ = cc.Close()
		_ = conn.Close()
		d.connectionDown(run)
		if run.ctx.Err() != nil || slotCtx.Err() != nil {
			return
		}
		if outcome.failure != "" {
			d.setError(run, outcome.failure)
		}
		if d.nowTime().Sub(started) >= d.timing.resetAfter {
			delay = d.timing.retryMin
		} else {
			delay = min(delay*2, d.timing.retryMax)
		}
		if !waitDial(slotCtx, d.retryDelay(delay)) {
			return
		}
	}
}

type connTracker struct {
	mu       sync.Mutex
	retire   bool
	active   int
	pending  map[uint64]context.CancelFunc
	sessions map[uint64]context.CancelFunc
	next     uint64
	drained  chan struct{}
	taken    []time.Time
	bytes    atomic.Uint64
}

func newConnTracker() *connTracker {
	return &connTracker{pending: map[uint64]context.CancelFunc{}, sessions: map[uint64]context.CancelFunc{}, drained: make(chan struct{})}
}

func (t *connTracker) begin(cancel context.CancelFunc) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retire {
		return 0, false
	}
	t.next++
	id := t.next
	t.pending[id] = cancel
	return id, true
}

func (t *connTracker) promote(id uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cancel, ok := t.pending[id]
	delete(t.pending, id)
	if !ok || t.retire {
		return false
	}
	t.sessions[id] = cancel
	t.active++
	return true
}

func (t *connTracker) endRequest(id uint64) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

func (t *connTracker) finish(id uint64) {
	t.mu.Lock()
	delete(t.sessions, id)
	if t.active > 0 {
		t.active--
	}
	if t.retire && t.active == 0 {
		closeDrained(t.drained)
	}
	t.mu.Unlock()
}

func (t *connTracker) retireNow() {
	t.mu.Lock()
	if t.retire {
		t.mu.Unlock()
		return
	}
	t.retire = true
	cancels := make([]context.CancelFunc, 0, len(t.pending))
	for _, cancel := range t.pending {
		cancels = append(cancels, cancel)
	}
	if t.active == 0 {
		closeDrained(t.drained)
	}
	t.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (t *connTracker) forceStop() {
	t.mu.Lock()
	t.retire = true
	cancels := make([]context.CancelFunc, 0, len(t.pending)+len(t.sessions))
	for _, cancel := range t.pending {
		cancels = append(cancels, cancel)
	}
	for _, cancel := range t.sessions {
		cancels = append(cancels, cancel)
	}
	if t.active == 0 {
		closeDrained(t.drained)
	}
	t.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func closeDrained(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

type drainingConnection struct {
	cc          *http2.ClientConn
	conn        net.Conn
	tracker     *connTracker
	liveCancel  context.CancelFunc
	pingDone    <-chan struct{}
	workersDone *sync.WaitGroup
	sessions    *sync.WaitGroup
}

type connectionOutcome struct {
	failure string
	drain   *drainingConnection
}

func (d *Dialer) serveConnection(run *dialRun, slotCtx context.Context, cc *http2.ClientConn, conn net.Conn, proof Proof) connectionOutcome {
	liveCtx, liveCancel := context.WithCancel(run.ctx)
	workerCtx, workersCancel := context.WithCancel(slotCtx)
	tracker := newConnTracker()
	var workers sync.WaitGroup
	var sessions sync.WaitGroup
	fatal := make(chan string, 1)
	pw := &pairWorkers{d: d, run: run, cc: cc, proof: proof, tracker: tracker, fatal: fatal, sessions: &sessions,
		workers: &workers, ctx: workerCtx, maxElastic: int32(MaxPendingWorkersPerConn - d.timing.pendingWorkers)}
	for i := 0; i < d.timing.pendingWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			pw.run1(false)
		}()
	}
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		d.pingLoop(liveCtx, run, cc, fatal)
	}()
	rotateAge := jitterDuration(d.timing.connectionAge, d.timing.ageJitter)
	rotate := time.NewTimer(rotateAge)
	defer rotate.Stop()
	byteCheck := time.NewTicker(d.timing.rotateCheck)
	defer byteCheck.Stop()

	var failure string
	retire := false
	for !retire && failure == "" {
		select {
		case <-run.ctx.Done():
			tracker.forceStop()
			workersCancel()
			_ = cc.Close()
			liveCancel()
			workers.Wait()
			sessions.Wait()
			<-pingDone
			return connectionOutcome{}
		case <-slotCtx.Done():
			if run.ctx.Err() != nil {
				continue
			}
			retire = true
		case failure = <-fatal:
		case <-rotate.C:
			retire = true
		case <-byteCheck.C:
			if tracker.bytes.Load() >= BridgeConnectionBytes {
				retire = true
			}
		}
	}
	if failure == "" && run.ctx.Err() == nil && retire {
		tracker.retireNow()
		workersCancel()
		workers.Wait()
		return connectionOutcome{drain: &drainingConnection{cc: cc, conn: conn, tracker: tracker,
			liveCancel: liveCancel, pingDone: pingDone, workersDone: &workers, sessions: &sessions}}
	}
	tracker.forceStop()
	workersCancel()
	_ = cc.Close()
	liveCancel()
	workers.Wait()
	sessions.Wait()
	<-pingDone
	return connectionOutcome{failure: failure}
}

func (d *Dialer) drainConnection(run *dialRun, old *drainingConnection) {
	timer := time.NewTimer(d.timing.drainTimeout)
	defer timer.Stop()
	force := false
	select {
	case <-old.tracker.drained:
	case <-run.ctx.Done():
		force = true
	case <-timer.C:
		force = true
	}
	if force {
		old.tracker.forceStop()
		_ = old.cc.Close()
	} else {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = old.cc.Shutdown(shutdownCtx)
		cancel()
	}
	old.liveCancel()
	_ = old.cc.Close()
	_ = old.conn.Close()
	<-old.pingDone
	old.sessions.Wait()
}

func (d *Dialer) pingLoop(ctx context.Context, run *dialRun, cc *http2.ClientConn, fatal chan<- string) {
	for {
		wait := jitterDuration(d.timing.pingInterval, d.timing.pingJitter)
		if !waitDial(ctx, wait) {
			return
		}
		pingCtx, pingCancel := context.WithTimeout(ctx, d.timing.pingTimeout)
		start := d.nowTime()
		err := cc.Ping(pingCtx)
		pingCancel()
		if err != nil {
			select {
			case fatal <- "ping":
			default:
			}
			return
		}
		d.setRTT(run, d.nowTime().Sub(start))
	}
}

type pairResponses struct {
	relayID    string
	down       *http.Response
	up         *http.Response
	downWriter *io.PipeWriter
}

// pairWorkers keeps a connection's idle pairs (the requests the entry hands a person to) topped up. A few permanent workers
// each hold one idle pair; when a burst of people eats them faster than they are replaced, elastic workers join, up to
// MaxPendingWorkersPerConn, and leave when their pair expires unused.
type pairWorkers struct {
	d          *Dialer
	run        *dialRun
	cc         *http2.ClientConn
	proof      Proof
	tracker    *connTracker
	fatal      chan<- string
	sessions   *sync.WaitGroup
	workers    *sync.WaitGroup
	ctx        context.Context
	elastic    atomic.Int32
	maxElastic int32
}

// grow adds two elastic workers when people have taken as many pairs in the last second as there are permanent workers:
// the pairs in flight cannot tell a burst, since each taken pair is replaced at once but only one round trip later.
func (p *pairWorkers) grow() {
	if p.ctx.Err() != nil || p.tracker.noteTaken(time.Now()) < max(1, p.d.timing.pendingWorkers) {
		return
	}
	for i := 0; i < 2; i++ {
		if p.elastic.Add(1) > p.maxElastic {
			p.elastic.Add(-1)
			return
		}
		p.workers.Add(1) // the caller is a counted worker, so the group is never at zero here
		go func() {
			defer p.workers.Done()
			defer p.elastic.Add(-1)
			p.run1(true)
		}()
	}
}

func (p *pairWorkers) run1(elastic bool) {
	d, run, cc, tracker := p.d, p.run, p.cc, p.tracker
	workerCtx := p.ctx
	delay := d.timing.retryMin
	for workerCtx.Err() == nil {
		pairID, err := newPairID()
		if err != nil {
			d.setError(run, "stream")
			if elastic || !waitDial(workerCtx, d.retryDelay(delay)) {
				return
			}
			delay = min(delay*2, d.timing.retryMax)
			continue
		}
		requestCtx, cancel := context.WithCancel(run.ctx)
		id, ok := tracker.begin(cancel)
		if !ok {
			cancel()
			return
		}
		responses, code, err := d.openPair(requestCtx, cancel, run.dial, cc, p.proof, pairID, "")
		if err != nil || code != "" {
			tracker.endRequest(id)
			cancel()
			if responses != nil {
				closePairResponses(responses)
			}
			if code == "request_idle" {
				if elastic {
					return // the burst is over
				}
				continue
			}
			if elastic || workerCtx.Err() != nil || run.ctx.Err() != nil {
				return
			}
			if err != nil && !cc.CanTakeNewRequest() {
				select {
				case p.fatal <- "stream":
				default:
				}
				return
			}
			if code != "" {
				d.setError(run, code)
			} else {
				d.setError(run, "stream")
			}
			if !waitDial(workerCtx, d.retryDelay(delay)) {
				return
			}
			delay = min(delay*2, d.timing.retryMax)
			continue
		}
		if !tracker.promote(id) {
			cancel()
			closePairResponses(responses)
			return
		}
		delay = d.timing.retryMin
		p.sessions.Add(1)
		go func() {
			defer p.sessions.Done()
			d.serveClient(run, cc, tracker, id, cancel, responses)
		}()
		p.grow()
	}
}

func (d *Dialer) openPair(ctx context.Context, cancel context.CancelFunc, dial plugin.BridgeDial, cc *http2.ClientConn, proof Proof, pairID, kind string) (*pairResponses, string, error) {
	reader, writer := io.Pipe()
	u := url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(strings.Trim(dial.Host, "[]"), strconv.Itoa(int(dial.Port))),
		Path:   dial.Path,
	}
	downReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), reader)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		cancel()
		return nil, "", err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		cancel()
		return nil, "", err
	}
	for _, request := range []*http.Request{downReq, upReq} {
		request.Header.Set(ProtocolHeader, protocolVersion)
		request.Header.Set(PairIDHeader, pairID)
		if kind != "" {
			request.Header.Set(StreamKindHeader, kind)
		}
		proof.setHeaders(request.Header)
	}
	downReq.Header.Set(StreamHeader, string(pairDown))
	upReq.Header.Set(StreamHeader, string(pairUp))
	type roundTripResult struct {
		role pairRole
		resp *http.Response
		err  error
	}
	result := make(chan roundTripResult, 2)
	go func() {
		resp, err := cc.RoundTrip(downReq)
		result <- roundTripResult{role: pairDown, resp: resp, err: err}
	}()
	go func() {
		resp, err := cc.RoundTrip(upReq)
		result <- roundTripResult{role: pairUp, resp: resp, err: err}
	}()

	responses := &pairResponses{downWriter: writer}
	var firstErr error
	var firstCode string
	ctxDone := ctx.Done()
	for received := 0; received < 2; {
		select {
		case got := <-result:
			received++
			if got.err != nil {
				if firstErr == nil {
					firstErr = got.err
					cancel()
					_ = writer.CloseWithError(got.err)
				}
				continue
			}
			if got.role == pairDown {
				responses.down = got.resp
			} else {
				responses.up = got.resp
			}
			if got.resp.StatusCode != http.StatusOK && firstCode == "" {
				firstCode = responseCode(got.resp.StatusCode, got.resp.Header)
				cancel()
				_ = writer.CloseWithError(errors.New(firstCode))
			}
		case <-ctxDone:
			if firstErr == nil {
				firstErr = ctx.Err()
				cancel()
				_ = writer.CloseWithError(firstErr)
			}
			ctxDone = nil
		}
	}
	if firstErr != nil || firstCode != "" {
		closePairResponses(responses)
		return nil, firstCode, firstErr
	}
	if responses.down == nil || responses.up == nil || responses.down.ProtoMajor != 2 || responses.up.ProtoMajor != 2 {
		closePairResponses(responses)
		cancel()
		return nil, "", errors.New("bad bridge response")
	}
	relayID := responses.down.Header.Get(RelayIDHeader)
	if (kind == StreamKindPanel && (relayID != "" || responses.up.Header.Get(RelayIDHeader) != "")) ||
		(kind != StreamKindPanel && (!relayIDPattern.MatchString(relayID) || responses.up.Header.Get(RelayIDHeader) != relayID)) ||
		responses.down.Header.Get(ProtocolHeader) != protocolVersion || responses.up.Header.Get(ProtocolHeader) != protocolVersion ||
		responses.down.Header.Get(StreamHeader) != string(pairDown) || responses.up.Header.Get(StreamHeader) != string(pairUp) ||
		responses.down.Header.Get(StreamKindHeader) != kind || responses.up.Header.Get(StreamKindHeader) != kind {
		closePairResponses(responses)
		cancel()
		return nil, "", errors.New("bad bridge response")
	}
	responses.relayID = relayID
	return responses, "", nil
}

func closePairResponses(responses *pairResponses) {
	if responses == nil {
		return
	}
	if responses.down != nil && responses.down.Body != nil {
		_ = responses.down.Body.Close()
	}
	if responses.up != nil && responses.up.Body != nil {
		_ = responses.up.Body.Close()
	}
	if responses.downWriter != nil {
		_ = responses.downWriter.Close()
	}
}

func (d *Dialer) serveClient(run *dialRun, cc *http2.ClientConn, tracker *connTracker, id uint64, cancel context.CancelFunc, responses *pairResponses) {
	defer tracker.finish(id)
	targets := run.targets.Load()
	if code := targets.errors[responses.relayID]; code != "" {
		d.setError(run, code)
		cancel()
		closePairResponses(responses)
		return
	}
	target, ok := targets.byID[responses.relayID]
	if !ok {
		code := targets.errors[responses.relayID]
		if code == "" {
			code = "target_not_local"
		}
		d.setError(run, code)
		cancel()
		closePairResponses(responses)
		return
	}
	ctx, stopIdle := context.WithCancel(run.ctx)
	targetConn, err := dialLocalTarget(ctx, target.Port, d.timing.targetTimeout, d.timing.keepAlive)
	if err != nil {
		stopIdle()
		d.setError(run, "target_connect")
		cancel()
		closePairResponses(responses)
		return
	}
	d.setError(run, "")
	d.addStream(run)
	idle := newIdleConn(targetConn, d.timing.streamIdle)
	err = d.bridgeLocal(ctx, run, target, idle, responses, tracker, cancel)
	stopIdle() // a session that ends badly is not a failure of the bridge (see Entry.servePairHalf)
	if err == nil {
		// A clean end: wait for the entry to finish its half (the end of its response) before cancelling, or the cancel's
		// RST_STREAM can overtake the last bytes still queued for the entry, and the entry resets the person.
		_, _ = io.Copy(io.Discard, responses.down.Body)
	}
	cancel()
	closePairResponses(responses)
	_ = idle.Close()
	_ = cc
}

func (d *Dialer) bridgeLocal(ctx context.Context, run *dialRun, target plugin.BridgeTarget, conn *idleConn,
	responses *pairResponses, tracker *connTracker, cancel context.CancelFunc) error {
	result := make(chan error, 2)
	go func() { result <- d.responseToTarget(ctx, run, conn, responses.up.Body, tracker) }()
	go func() { result <- d.targetToDown(ctx, run, conn, responses.downWriter, tracker) }()
	var firstErr error
	for i := 0; i < 2; i++ {
		err := <-result
		if err != nil && firstErr == nil {
			firstErr = err
			cancel()
			rejectConn(conn.Conn)
			_ = responses.downWriter.CloseWithError(err)
			_ = responses.up.Body.Close()
		}
	}
	return firstErr
}

func (d *Dialer) responseToTarget(ctx context.Context, run *dialRun, dst *idleConn, src io.Reader, tracker *connTracker) error {
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if err := writeFull(dst, buf[:n]); err != nil {
				return err
			}
			tracker.bytes.Add(uint64(n))
			d.addBytes(run, 0, uint64(n))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return closeWrite(dst.Conn)
			}
			return readErr
		}
	}
}

func (d *Dialer) targetToDown(ctx context.Context, run *dialRun, src *idleConn, dst *io.PipeWriter, tracker *connTracker) error {
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			_ = dst.CloseWithError(err)
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if err := writeFull(dst, buf[:n]); err != nil {
				_ = dst.CloseWithError(err)
				return err
			}
			tracker.bytes.Add(uint64(n))
			d.addBytes(run, uint64(n), 0)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return dst.Close()
			}
			_ = dst.CloseWithError(readErr)
			return readErr
		}
	}
}

func dialTLS(ctx context.Context, dial plugin.BridgeDial, identity DialIdentity) (net.Conn, Proof, error) {
	return dialTLSWithTimeout(ctx, dial, identity, BridgeDialTimeout, BridgeKeepAlive, nil)
}

func dialTLSWithTimeout(ctx context.Context, dial plugin.BridgeDial, identity DialIdentity, timeout, keepAlive time.Duration, roots *x509.CertPool) (net.Conn, Proof, error) {
	if dial.SNI == "" && dial.PinSHA256 == "" {
		return nil, Proof{}, errors.New("no_verification")
	}
	if identity.PrivateKey == nil || len(identity.CertificateDER) == 0 || identity.NodeID == "" {
		return nil, Proof{}, errors.New("identity_unavailable")
	}
	addr := net.JoinHostPort(strings.Trim(dial.Host, "[]"), strconv.Itoa(int(dial.Port)))
	raw, err := (&net.Dialer{Timeout: timeout, KeepAlive: keepAlive}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, Proof{}, err
	}
	config := &utls.Config{
		ServerName: dial.SNI, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"}, InsecureSkipVerify: dial.PinSHA256 != "",
		RootCAs: roots, Renegotiation: utls.RenegotiateNever,
	}
	conn := utls.UClient(raw, config, utls.HelloChrome_Auto)
	if err := conn.BuildHandshakeState(); err != nil {
		_ = raw.Close()
		return nil, Proof{}, err
	}
	for _, ext := range conn.Extensions {
		if reneg, ok := ext.(*utls.RenegotiationInfoExtension); ok {
			reneg.Renegotiation = utls.RenegotiateNever
		}
	}
	if err := conn.BuildHandshakeState(); err != nil {
		_ = raw.Close()
		return nil, Proof{}, err
	}
	if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = raw.Close()
		return nil, Proof{}, err
	}
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, timeout)
	err = conn.HandshakeContext(handshakeCtx)
	cancelHandshake()
	if err != nil {
		_ = raw.Close()
		return nil, Proof{}, err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = raw.Close()
		return nil, Proof{}, err
	}
	state := conn.ConnectionState()
	if (state.Version != tls.VersionTLS12 && state.Version != tls.VersionTLS13) || state.NegotiatedProtocol != "h2" || len(state.PeerCertificates) == 0 {
		_ = conn.Close()
		return nil, Proof{}, errors.New("tls_protocol")
	}
	if dial.PinSHA256 != "" {
		wantPin, err := hex.DecodeString(dial.PinSHA256)
		if err != nil || len(wantPin) != sha256.Size {
			_ = conn.Close()
			return nil, Proof{}, errors.New("no_verification")
		}
		gotPin := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(wantPin, gotPin[:]) != 1 {
			_ = conn.Close()
			return nil, Proof{}, errors.New("pin_mismatch")
		}
	}
	exporter, err := state.ExportKeyingMaterial("mistgate-bridge/1", nil, 32)
	if err != nil {
		_ = conn.Close()
		return nil, Proof{}, fmt.Errorf("tls_exporter: %w", err)
	}
	proof, err := MakeProof(dial.EntryNodeID, exporter, identity.CertificateDER, identity.PrivateKey)
	if err != nil {
		_ = conn.Close()
		return nil, Proof{}, err
	}
	return conn, proof, nil
}

func waitDial(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func jitterDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return DialRetryMin
	}
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(delay-half)+1))
}

func (d *Dialer) retryDelay(delay time.Duration) time.Duration {
	if d.timing.retryJitter != nil {
		return d.timing.retryJitter(delay)
	}
	if delay <= 0 {
		return d.timing.retryMin
	}
	return jitterDelay(delay)
}

func dialErrorCode(err error) string {
	switch {
	case strings.HasPrefix(err.Error(), "tls_exporter"):
		return "tls_exporter"
	case err.Error() == "identity_unavailable":
		return "identity_unavailable"
	case err.Error() == "no_verification":
		return "no_verification"
	case err.Error() == "pin_mismatch":
		return "pin_mismatch"
	case err.Error() == "tls_protocol":
		return "tls_protocol"
	}
	return "connect"
}

func dialLocalTarget(ctx context.Context, port uint16, timeout, keepAlive time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: keepAlive}
	var last error
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err == nil {
			return conn, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, last
}

func (d *Dialer) connectionUp(run *dialRun) {
	now := d.nowTime()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runs[run.dial.EntryNodeID] != run {
		return
	}
	st := d.states[run.dial.EntryNodeID]
	if st.Conns == 0 && st.Since.IsZero() {
		st.Since = now
	}
	st.Conns++
	st.Error = ""
	d.states[run.dial.EntryNodeID] = st
}

func (d *Dialer) connectionDown(run *dialRun) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runs[run.dial.EntryNodeID] != run {
		return
	}
	st := d.states[run.dial.EntryNodeID]
	if st.Conns > 0 {
		st.Conns--
	}
	d.states[run.dial.EntryNodeID] = st
}

func (d *Dialer) setRTT(run *dialRun, rtt time.Duration) {
	d.mu.Lock()
	if d.runs[run.dial.EntryNodeID] == run {
		st := d.states[run.dial.EntryNodeID]
		st.RTT = rtt
		d.states[run.dial.EntryNodeID] = st
	}
	d.mu.Unlock()
}

func (d *Dialer) setError(run *dialRun, code string) {
	d.mu.Lock()
	if d.runs[run.dial.EntryNodeID] == run {
		st := d.states[run.dial.EntryNodeID]
		st.Error = code
		d.states[run.dial.EntryNodeID] = st
	}
	d.mu.Unlock()
}

func (d *Dialer) addBytes(run *dialRun, toPeople, toExit uint64) {
	d.mu.Lock()
	if d.runs[run.dial.EntryNodeID] == run {
		st := d.states[run.dial.EntryNodeID]
		st.BytesIn += toPeople
		st.BytesOut += toExit
		d.states[run.dial.EntryNodeID] = st
	}
	d.mu.Unlock()
}

func (d *Dialer) addStream(run *dialRun) {
	d.mu.Lock()
	if d.runs[run.dial.EntryNodeID] == run {
		st := d.states[run.dial.EntryNodeID]
		st.Streams++
		d.states[run.dial.EntryNodeID] = st
	}
	d.mu.Unlock()
}

func (d *Dialer) nowTime() time.Time {
	d.mu.Lock()
	now := d.clock
	d.mu.Unlock()
	if now == nil {
		return time.Now()
	}
	return now()
}

func (d *Dialer) States() []DialState {
	d.mu.Lock()
	out := make([]DialState, 0, len(d.states))
	for _, state := range d.states {
		out = append(out, state)
	}
	d.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PeerNodeID < out[j].PeerNodeID })
	return out
}

func (d *Dialer) Close() {
	d.mu.Lock()
	d.closed = true
	runs := make([]*dialRun, 0, len(d.runs))
	for id, run := range d.runs {
		run.cancel()
		runs = append(runs, run)
		delete(d.runs, id)
	}
	d.mu.Unlock()
	for _, run := range runs {
		<-run.done
	}
}

// noteTaken records that a person took a pair now and returns how many were taken in the last second.
func (t *connTracker) noteTaken(now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := t.taken[:0]
	for _, at := range t.taken {
		if now.Sub(at) < time.Second {
			kept = append(kept, at)
		}
	}
	t.taken = append(kept, now)
	return len(t.taken)
}
