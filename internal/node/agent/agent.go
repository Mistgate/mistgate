// Package agent is the node agent core: it enrolls with the panel, keeps ONE agent session open
// (mTLS Connect by default, or the signed WebSocket link when the agent has a link URL and the panel advertises
// it; a link that fails to establish falls back to mTLS and is retried after a hold period; reconnecting with backoff),
// applies desired state to engines, reports traffic reliably (seq + ack), executes commands and serves
// its own log. The wire protocol is specified in the
// comment block of proto/mistgate/agent/v1/agent.proto; this package implements it, nothing more.
//
// Concurrency model: one worker goroutine owns every mutation of engine and host state (desired-state
// apply, kick, restart, retire, credential-expiry sweep), so the engines see one caller for Apply/Remove/
// Kick as their contract demands. Each stream has a reader (the session goroutine) and one writer; the
// statistics collector, certificate renewal and the expiry timer run for the agent's whole life, across
// reconnects.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/speedtest"
	"github.com/mistgate/mistgate/internal/node/update"
)

// Config is what the caller (cmd/mistgate-node, or a test) provides.
type Config struct {
	// StateDir holds the identity and the persisted desired state (created by Enroll).
	StateDir string
	// LinkURL is the full wss://host/<secret-prefix> base for the optional signed WebSocket transport.
	LinkURL string
	// Version is reported in Hello; defaults to buildinfo.Version.
	Version string
	// Built is the Unix time of the source commit (Hello.built); 0 = buildinfo.BuiltUnix().
	Built int64
	// Updater is the self-update machinery (internal/node/update). Nil = this agent never lists "update/1" and
	// refuses UpdateAgent and RollbackAgent.
	Updater *update.Updater
	// Log receives the agent's records; every record also goes to the in-memory ring that LogRequest reads.
	// Nil = slog.Default().
	Log *slog.Logger

	// Host-side services handed to the engines (engine.Env). Nil Certs means engines get no certificates,
	// nil Egress means only a "no egress configured" error, nil Masquerade serves a plain 404.
	Certs      engine.CertSource
	Egress     func(name string) (engine.Egress, error)
	Masquerade func(inboundID string) http.Handler

	// DoctorEnv adjusts the doctor's view of the machine before the doctor is built (tests point it at a fake
	// root directory and fake commands). Nil = the real host.
	DoctorEnv func(doctor.Env) doctor.Env

	// Warp is the node-level WARP manager (internal/node/warp.Manager). Nil = this build has no WARP: the agent does not
	// list "warp/1", and an inbound whose egress is WARP is not started. The manager is also what Egress("warp") returns.
	Warp WarpManager
	// UnitGen is the generation of the systemd unit the agent runs from (MISTGATE_UNIT_GEN); 3 is listed as "unit/3".
	UnitGen int
	// AwgPrepare builds the AmneziaWG kernel module on request (internal/node/awgprep). Nil = this agent does not list
	// "awg-prepare/1", and the admin UI keeps showing the manual command.
	AwgPrepare *awgprep.Controller
	// SpeedTest runs one bandwidth measurement (MeasureBandwidth). Nil = the real one against the public test servers; tests
	// give a fake, since they have no internet.
	SpeedTest func(ctx context.Context) (speedtest.Result, error)

	// MaxPending and MaxPendingBytes bound the queue of unacked reliable messages (defaults 2160 = 6 h of
	// 10 s batches, and 32 MiB).
	MaxPending      int
	MaxPendingBytes int
}

// Agent is the node agent. Build it with New and run it with Run.
type Agent struct {
	cfg  Config
	log  *slog.Logger
	ring *logRing
	host hostctl.Host
	meta Meta
	id   atomic.Pointer[identity]

	engines   map[string]engine.Engine
	protocols []string // sorted keys of engines
	closeOnce sync.Once

	torrentEventsMu sync.Mutex
	torrentEvents   map[string]time.Time
	torrentUTPSyn   map[string]*torrentUTPSynWindow

	instanceID     string
	out            *outbox
	jobs           chan job
	settings       atomic.Pointer[pb.NodeSettings]
	linkAdvertised atomic.Bool               // the last HelloAck advertised the signed link
	linkRetryAt    atomic.Pointer[time.Time] // a link attempt failed before it was established: the link is not tried again before this
	linkHTTPClient *http.Client              // test seam; nil uses normal system-root TLS verification
	offset         atomic.Int64              // seconds: panel clock minus local clock, measured at Hello
	skewed         atomic.Bool
	cur            atomic.Pointer[session]
	cancel         context.CancelCauseFunc

	upd          *update.Updater
	helloOutcome atomic.Pointer[pb.LastUpdate] // the last_update the current Hello carried; acked by the HelloAck
	connAt       atomic.Int64                  // UnixNano of the current stream's HelloAck, 0 = not connected
	dsIn, dsDone atomic.Int64                  // DesiredState messages handed to the worker / finished by it
	dsOK         atomic.Bool                   // the last DesiredState on this stream was not refused
	exiting      atomic.Bool                   // a re-exec is under way: the worker takes no new jobs

	measuring    atomic.Bool // a bandwidth test is running (one at a time)
	udpMu        sync.Mutex
	udpArmed     *udpCountArm
	udpSends     int
	udpAfterFunc func(time.Duration, func()) udpCountTimer
	sendUDP      func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error)

	doc      *doctor.Doctor
	hostRing doctor.Ring                          // host samples for the doctor's cpu_softirq and idle-load rules
	certNA   atomic.Pointer[map[string]time.Time] // certificate expiry per inbound at the last apply

	mu    sync.Mutex
	model *model // applied desired state; replaced wholesale, never mutated once stored

	// Owned by the worker goroutine (and by restore, which runs before the worker starts).
	held    map[string]*held
	hops    []hostctl.Hop
	hopsSet bool
	// warm: the persisted state was restored (or there was none), so an inbound that starts now was asked for, not
	// brought back by this process starting (the reason of engine_started). upMarker is the update marker of a new build.
	warm     bool
	upMarker *update.Marker
	hopRej   map[string]string // inbound id -> why its hop range was refused (F12), rebuilt by every syncHops

	// L3 (l3.go): inbounds that may not run in this reconcile (id -> why), the failures already reported (one event per
	// distinct message), and whether the tunnel table and the WARP subnet rules were ever installed (their removal is sent once).
	blocked             map[string]string
	warpErr, tunErr     string
	tunV6               string // the IPv6 fallback of the tunnel table last reported (hostctl.V6FallbackHost)
	warpRouteErr        string
	warpNoted           bool
	tunUsed, warpRouted bool
	torrentGuardErr     string
	torrentGuardActive  bool
	hostFirewallErr     string // Last reported host-firewall sync failure; worker-owned.

	// Timing knobs; tests shorten them.
	statsEvery   time.Duration // 0 = NodeSettings.stats_interval_s
	backoffMin   time.Duration
	backoffMax   time.Duration
	renewEvery   time.Duration
	doctorFirst  time.Duration // 0 = 30 s after connect
	doctorEvery  time.Duration // 0 = 10 min
	prepWatch    time.Duration // 0 = 5 s: how often the kernel-module job's status file is read
	sweepEvery   time.Duration
	helloTimeout time.Duration // 0 = twice the dial timeout
	commitTick   time.Duration // update commit watch poll, default 2 s
	commitSettle time.Duration // connected this long before a new build may commit, default 10 s
	linkHoldFor  time.Duration // 0 = defaultLinkHold: how long the link is left alone after a failed attempt
	linkBusyWait time.Duration // 0 = linkBusyRecheck: how long the end of a held session is put off while it has work in flight
	resetAfter   time.Duration // 0 = 1 min: a session that lived this long resets the reconnect delay

	busy atomic.Int32 // operations started by a session that ending it would cut short (goBusy)

	renewMu      sync.Mutex
	renewReply   chan *pb.RenewResponse // the link renewal waiting for its answer (identity.go), nil = none
	renewTimeout time.Duration          // 0 = 2 min: how long a link renewal waits for the answer
	renewRetry   time.Duration          // 0 = 20 s: how soon a link renewal that found no session is tried again
}

const (
	// defaultLinkHold is how long the link is left alone after a link attempt failed before it was established. The agent
	// runs over mTLS meanwhile; an mTLS session that the panel offers the link on ends when the period is over, so the
	// link is dialled again, and a failing link costs at most one attempt per period.
	defaultLinkHold = 10 * time.Minute
	// linkBusyRecheck is how long the end of a held session is put off while an operation is in flight on it.
	linkBusyRecheck = 30 * time.Second
)

// linkWait is how long the link must still be left alone after a failed attempt; zero or less means it may be tried now.
func (a *Agent) linkWait() time.Duration {
	if t := a.linkRetryAt.Load(); t != nil {
		return time.Until(*t)
	}
	return 0
}

// goBusy runs f in its own goroutine and counts it as work in flight, which a held session is not ended in the middle of.
func (a *Agent) goBusy(f func()) {
	a.busy.Add(1)
	go func() {
		defer a.busy.Add(-1)
		f()
	}()
}

// holdBusy reports whether ending s now would cut something short: an operation started by a command, a log stream,
// a retire or a re-exec (the last two answer and drain on the stream).
func (s *session) holdBusy() bool {
	s.mu.Lock()
	logs := len(s.logs)
	s.mu.Unlock()
	return s.a.busy.Load() > 0 || logs > 0 || s.a.exiting.Load()
}

// endHeldSession ends the mTLS session s after wait, once the failed link may be tried again, the way a session ends
// when its context is cancelled; expired tells the receive loop that this was the reason. Work in flight postpones it.
func (a *Agent) endHeldSession(s *session, wait time.Duration, expired *atomic.Bool) {
	recheck := a.linkBusyWait
	if recheck == 0 {
		recheck = linkBusyRecheck
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if s.holdBusy() {
			t.Reset(recheck)
			continue
		}
		a.log.Info("agent link hold over; retrying the link")
		expired.Store(true)
		s.cancel()
		return
	}
}

// held remembers what was last handed to an engine so unchanged inbounds are not re-applied.
type held struct {
	spec    string // statehash.Spec
	creds   string // statehash.Creds of the ACTIVE credentials
	expired int    // credentials withheld because their term ended
	res     *pb.InboundResult
}

// New loads the identity from cfg.StateDir and builds the engines. The panel is not contacted.
// Returns ErrNotEnrolled when `enroll` has not been run.
func New(cfg Config, engines map[string]engine.Factory, host hostctl.Host) (*Agent, error) {
	if cfg.LinkURL != "" {
		if _, err := parseLinkBase(cfg.LinkURL); err != nil {
			return nil, fmt.Errorf("link URL: %w", err)
		}
	}
	if cfg.Version == "" {
		cfg.Version = buildinfo.Version
	}
	if cfg.Built == 0 {
		cfg.Built = buildinfo.BuiltUnix()
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = 2160
	}
	if cfg.MaxPendingBytes <= 0 {
		cfg.MaxPendingBytes = 32 << 20
	}
	id, err := loadIdentity(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if id.meta.Link != "" { // enrolled over the link: that address is the only way to the panel
		if _, err := parseLinkBase(id.meta.Link); err != nil {
			return nil, fmt.Errorf("%s link: %w", fileMeta, err)
		}
		cfg.LinkURL = id.meta.Link
	}
	inst := make([]byte, 16)
	_, _ = rand.Read(inst)

	inner := slog.Default().Handler()
	if cfg.Log != nil {
		inner = cfg.Log.Handler()
	}
	ring := newLogRing()
	a := &Agent{
		cfg: cfg, ring: ring, host: host, meta: id.meta,
		log:           slog.New(&ringHandler{ring: ring, inner: inner}),
		engines:       map[string]engine.Engine{},
		torrentEvents: map[string]time.Time{},
		torrentUTPSyn: map[string]*torrentUTPSynWindow{},
		instanceID:    hex.EncodeToString(inst),
		out:           newOutbox(cfg.MaxPending, cfg.MaxPendingBytes),
		jobs:          make(chan job, 256),
		model:         newModel(),
		held:          map[string]*held{},
		backoffMin:    time.Second, backoffMax: time.Minute,
		renewEvery: time.Hour, sweepEvery: 30 * time.Second,
		commitTick: commitTick, commitSettle: commitSettle, upd: cfg.Updater,
	}
	a.id.Store(id)
	a.settings.Store(&pb.NodeSettings{})

	for proto, f := range engines {
		e, err := f(engine.Env{
			Log:        a.log.With("source", proto),
			Event:      a.engineEvent,
			Certs:      cfg.Certs,
			Egress:     a.egress,
			DNS:        a.DNS,
			Masquerade: a.masquerade,
			Now:        a.now,
		})
		if err != nil {
			a.closeEngines()
			return nil, fmt.Errorf("engine %s: %w", proto, err)
		}
		if e.Protocol() != proto {
			a.closeEngines()
			return nil, fmt.Errorf("engine registered as %q reports protocol %q", proto, e.Protocol())
		}
		a.engines[proto] = e
		a.protocols = append(a.protocols, proto)
	}
	sort.Strings(a.protocols)
	a.doc = doctor.New(a.doctorEnv())
	return a, nil
}

// DNS returns the resolvers from NodeSettings (empty = engine default). Engines call it per use.
func (a *Agent) DNS() []string {
	return append([]string(nil), a.settings.Load().DnsResolvers...)
}

func (a *Agent) egress(name string) (engine.Egress, error) {
	if a.cfg.Egress == nil {
		return nil, errors.New("no egress configured")
	}
	return a.cfg.Egress(name)
}

func (a *Agent) masquerade(inboundID string) http.Handler {
	if a.cfg.Masquerade == nil {
		return http.NotFoundHandler()
	}
	return a.cfg.Masquerade(inboundID)
}

// now is the agent clock corrected by the offset measured at Hello.
func (a *Agent) now() time.Time { return time.Now().Add(time.Duration(a.offset.Load()) * time.Second) }

func (a *Agent) knows(protocol string) bool { _, ok := a.engines[protocol]; return ok }

func (a *Agent) closeEngines() {
	a.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, p := range a.protocols {
			if err := a.engines[p].Close(ctx); err != nil {
				a.log.Warn("engine close", "protocol", p, "err", err)
			}
		}
	})
}

// Run blocks until ctx is cancelled (returns nil) or the panel retires the node (returns ErrRetired).
// On a normal stop the engines are closed but host state (hop rules, sysctl) is left in place so the node
// keeps its baseline across an agent restart.
func (a *Agent) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	a.cancel = cancel
	defer cancel(nil)

	if a.startUpdate(ctx) { // a rollback decided at start replaces this process before anything runs
		return context.Cause(ctx)
	}
	a.announceStart(ctx)
	if cleaner, ok := a.host.(hostctl.UDPCountCleaner); ok {
		if err := cleaner.CleanupUDPCount(ctx); err != nil {
			a.log.Warn("stale UDP check table not removed", "err", err)
		}
	}
	if err := a.host.ApplyBaseline(ctx); err != nil {
		a.log.Warn("host baseline not fully applied", "err", err)
	}
	a.restore(ctx)
	a.warm = true

	var wg sync.WaitGroup
	loops := []func(context.Context){a.worker, a.collectLoop, a.renewLoop, a.sweepLoop}
	if a.cfg.Warp != nil {
		loops = append(loops, a.cfg.Warp.Run)
	}
	if a.cfg.AwgPrepare != nil {
		loops = append(loops, a.prepareLoop)
	}
	for _, f := range loops {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	a.connectLoop(ctx)
	cause := context.Cause(ctx)
	cancel(cause)
	wg.Wait()
	a.closeEngines()
	if errors.Is(cause, ErrRetired) {
		return ErrRetired
	}
	if errors.Is(cause, errExec) {
		return cause
	}
	return nil
}

// restore applies the persisted state so engines come back after a restart without the panel.
func (a *Agent) restore(ctx context.Context) {
	m, err := loadState(a.cfg.StateDir, a.knows)
	if err != nil {
		a.log.Warn("persisted state ignored", "err", err)
	}
	if m.revision == 0 && len(m.inbounds) == 0 {
		return
	}
	if m.settings != nil {
		a.settings.Store(m.settings)
	}
	results := a.reconcile(ctx, m, nil)
	a.setModel(m)
	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		}
	}
	a.log.Info("restored persisted state", "revision", m.revision, "inbounds", len(m.inbounds), "failed", failed)
}

func (a *Agent) snapshotModel() *model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) setModel(m *model) {
	a.mu.Lock()
	a.model = m
	a.mu.Unlock()
}

// ---------------------------------------------------------------------------------------------------
// Connection loop

func (a *Agent) connectLoop(ctx context.Context) {
	delay := a.backoffMin
	for ctx.Err() == nil {
		started := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return
		}
		lived := time.Since(started)
		if lived >= a.backoffResetAfter() {
			delay = a.backoffMin // also when the loop goes on below without a wait: a long session ended
		}
		if errors.Is(err, errSwitchTransport) {
			continue
		}
		if errors.Is(err, errLinkNotEstablished) && !a.linkOnly() {
			// session() has noted the failure, so the next session is mTLS until the hold period is over: no backoff
			// wait, and none of the growth it would add to the delay of later real failures.
			a.log.Warn("agent link failed; falling back to mTLS", "err", err)
			continue
		}
		var wait time.Duration
		wait, delay = backoffStep(delay, lived, a.backoffMin, a.backoffMax)
		a.log.Warn("panel connection lost", "err", err, "lived", lived.Round(time.Second), "retry_in", wait.Round(10*time.Millisecond))
		sleepCtx(ctx, wait)
	}
}

func (a *Agent) backoffResetAfter() time.Duration {
	if a.resetAfter == 0 {
		return time.Minute
	}
	return a.resetAfter
}

var errSwitchTransport = errors.New("switch to advertised agent link")
var errLinkNotEstablished = errors.New("agent link session did not establish")

// backoffStep implements "1 s -> 60 s, factor 2, +-20% jitter, reset once a stream has lived 60 s".
// delay is the nominal delay for this wait; it returns the jittered wait and the nominal delay for the next.
func backoffStep(delay, lived, lo, hi time.Duration) (wait, next time.Duration) {
	if lived >= time.Minute {
		delay = lo
	}
	return jitter(delay), min(delay*2, hi)
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*mrand.Float64()))
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func secs(v uint32, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return time.Duration(v) * time.Second
}

func dialTimeout(st *pb.NodeSettings) time.Duration { return secs(st.DialTimeoutS, 15*time.Second) }

type ctlMsg struct {
	m    *pb.ConnectRequest
	done chan struct{} // closed after the message was written (or the stream died); optional
}

type agentTransport interface {
	Context() context.Context
	Send(*pb.ConnectRequest) error
	Receive() (*pb.ConnectResponse, error)
	CloseSend() error
	Close() error
}

type connectAgentTransport struct {
	ctx       context.Context
	stream    *connect.BidiStreamForClient[pb.ConnectRequest, pb.ConnectResponse]
	sendOnce  sync.Once
	closeOnce sync.Once
}

func (s *connectAgentTransport) Context() context.Context              { return s.ctx }
func (s *connectAgentTransport) Send(m *pb.ConnectRequest) error       { return s.stream.Send(m) }
func (s *connectAgentTransport) Receive() (*pb.ConnectResponse, error) { return s.stream.Receive() }
func (s *connectAgentTransport) CloseSend() error {
	var err error
	s.sendOnce.Do(func() { err = s.stream.CloseRequest() })
	return err
}
func (s *connectAgentTransport) Close() error {
	var err error
	s.closeOnce.Do(func() {
		_ = s.CloseSend()
		err = s.stream.CloseResponse()
	})
	return err
}

// session is one agent stream. Its writer goroutine is the only caller of stream.Send after Hello.
type session struct {
	a      *Agent
	ctx    context.Context
	cancel context.CancelFunc
	stream agentTransport
	ctl    chan ctlMsg
	wake   chan struct{}
	sent   uint64 // highest reliable seq written on this stream; writer goroutine only

	recv chan struct{} // closed when the receive loop ends: the panel has finished the stream (or it died)

	sendMu     sync.Mutex // serialises stream.Send with drain's half-close
	halfClosed bool       // guarded by sendMu: drain ran, nothing more may be sent

	mu   sync.Mutex
	logs map[string]context.CancelFunc
}

func (s *session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// send queues a non-reliable message; it is dropped if the stream dies first.
func (s *session) send(m *pb.ConnectRequest) {
	select {
	case s.ctl <- ctlMsg{m: m}:
	case <-s.ctx.Done():
	}
}

// sendSync waits until the message was written, for at most timeout.
func (s *session) sendSync(m *pb.ConnectRequest, timeout time.Duration) {
	done := make(chan struct{})
	select {
	case s.ctl <- ctlMsg{m: m, done: done}:
	case <-s.ctx.Done():
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
	case <-s.ctx.Done():
	}
}

// drain half-closes our side (queued data still goes out, then END_STREAM) and waits, for at most timeout,
// for the panel to finish the stream. Cancelling the context right after a write could reset the stream
// before the last frame left the transport, and the panel would never see it.
//
// It waits for the receive loop to end, not for the session context: a late write failing after the half-close
// cancels that context at once, which would send the caller on (into a re-exec) before the panel read the frame
// it was waiting for. After the half-close the writer sends nothing more.
func (s *session) drain(timeout time.Duration) {
	s.sendMu.Lock()
	s.halfClosed = true
	_ = s.stream.CloseSend()
	s.sendMu.Unlock()
	select {
	case <-s.recv:
	case <-time.After(timeout):
	}
}

func (s *session) writer() {
	write := func(m *pb.ConnectRequest) bool {
		s.sendMu.Lock()
		defer s.sendMu.Unlock()
		if s.halfClosed {
			return false
		}
		if err := s.stream.Send(m); err != nil {
			s.cancel()
			return false
		}
		return true
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case c := <-s.ctl:
			ok := write(c.m)
			if c.done != nil {
				close(c.done)
			}
			if !ok {
				return
			}
		case <-s.wake:
			for _, m := range s.a.out.after(s.sent) {
				if !write(m) {
					return
				}
				s.sent = m.Seq
			}
		}
	}
}

// linkOnly reports an agent that enrolled over the link: it always dials the link (no mTLS, no hold period, whatever
// the panel advertises).
func (a *Agent) linkOnly() bool { return a.meta.Link != "" }

func (a *Agent) session(ctx context.Context) error {
	if a.linkOnly() {
		return a.linkSession(ctx)
	}
	if a.cfg.LinkURL != "" && a.linkAdvertised.Load() && a.linkWait() <= 0 {
		err := a.linkSession(ctx)
		if errors.Is(err, errLinkNotEstablished) && ctx.Err() == nil {
			hold := a.linkHoldFor
			if hold == 0 {
				hold = defaultLinkHold
			}
			retry := time.Now().Add(hold)
			a.linkRetryAt.Store(&retry)
		}
		return err
	}
	return a.mtlsSession(ctx)
}

func (a *Agent) mtlsSession(ctx context.Context) error {
	st := a.settings.Load()
	client := newHTTPClient(a.mtlsConfig(), dialTimeout(st), &http.HTTP2Config{
		SendPingTimeout: secs(st.KeepaliveIntervalS, 20*time.Second),
		PingTimeout:     secs(st.KeepaliveTimeoutS, 45*time.Second),
	})
	defer client.CloseIdleConnections()

	sctx, cancel := context.WithCancel(ctx)
	stream := agentv1connect.NewAgentServiceClient(client, "https://"+a.meta.Panel,
		connect.WithReadMaxBytes(64<<20)).Connect(sctx)
	return a.runSession(cancel, &connectAgentTransport{ctx: sctx, stream: stream})
}

func (a *Agent) linkSession(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	stream, cleanup, err := a.dialLink(sctx)
	if err != nil {
		cancel()
		return fmt.Errorf("%w: %w", errLinkNotEstablished, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	err = a.runSession(cancel, stream)
	if !stream.established {
		if err == nil {
			return errLinkNotEstablished
		}
		return fmt.Errorf("%w: %w", errLinkNotEstablished, err)
	}
	return err
}

func (a *Agent) runSession(cancel context.CancelFunc, stream agentTransport) error {
	defer stream.Close()
	defer cancel()
	ctx := stream.Context()
	// Closing the transport on cancellation wakes a blocked receive for both HTTP/2 and WebSocket sessions.
	go func() { <-ctx.Done(); _ = stream.Close() }()

	if err := stream.Send(a.hello(ctx)); err != nil {
		if errors.Is(err, io.EOF) { // the real error is on the response side
			if _, rerr := stream.Receive(); rerr != nil {
				err = rerr
			}
		}
		return fmt.Errorf("hello: %w", err)
	}
	wait := a.helloTimeout
	if wait == 0 {
		wait = 2 * dialTimeout(a.settings.Load())
	}
	timedOut := atomic.Bool{}
	timer := time.AfterFunc(wait, func() {
		timedOut.Store(true)
		cancel()
	}) // a panel that never answers must not hold us forever
	resp, err := stream.Receive()
	timer.Stop()
	if err != nil {
		if timedOut.Load() {
			err = errors.New("no HelloAck in time")
		}
		return fmt.Errorf("hello ack: %w", err)
	}
	ack := resp.GetHelloAck()
	if ack == nil {
		return errors.New("first panel message is not HelloAck")
	}
	if link, ok := stream.(*websocketAgentTransport); ok {
		link.established = true
	}
	a.linkAdvertised.Store(ack.LinkSupported)
	a.onHelloAck(ack)
	_, isConnect := stream.(*connectAgentTransport)
	var holdFor time.Duration // > 0: this mTLS session stays although the panel offers the link, for this long
	if isConnect && a.cfg.LinkURL != "" && ack.LinkSupported {
		if holdFor = a.linkWait(); holdFor <= 0 {
			// This mTLS stream only discovered link support. Close it so the reconnect loop can choose the link.
			cancel()
			_ = stream.Close()
			return errSwitchTransport
		}
	}
	var holdExpired atomic.Bool
	a.dsOK.Store(true)
	a.connAt.Store(time.Now().UnixNano())
	defer a.connAt.Store(0)

	s := &session{
		a: a, ctx: ctx, cancel: cancel, stream: stream,
		ctl: make(chan ctlMsg, 64), wake: make(chan struct{}, 1), logs: map[string]context.CancelFunc{}, recv: make(chan struct{}),
	}
	a.cur.Store(s)
	defer a.cur.CompareAndSwap(s, nil)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.writer() }()
	wg.Add(1)
	go func() { defer wg.Done(); a.doctorSchedule(s) }()
	if holdFor > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); a.endHeldSession(s, holdFor, &holdExpired) }()
	}
	defer wg.Wait()
	defer cancel()
	s.poke() // resend everything still unacked, in order
	a.log.Info("connected to panel", "acked_seq", ack.AckedSeq)

	defer close(s.recv)
	for {
		msg, err := stream.Receive()
		if err != nil {
			if holdExpired.Load() && ctx.Err() != nil { // the error is the cancel of endHeldSession, not a real one
				return errSwitchTransport
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("stream closed by the panel")
			}
			return err
		}
		a.dispatch(s, msg)
	}
}

func (a *Agent) hello(ctx context.Context) *pb.ConnectRequest {
	next, first := a.out.hello()
	m := a.snapshotModel()
	var rev uint64
	var hash string
	if m.revision != 0 {
		rev, hash = m.revision, a.observedHash(m)
	}
	h := &pb.Hello{
		NodeId: a.meta.NodeID, AgentVersion: a.cfg.Version, ApiVersion: apiVersion, InstanceId: a.instanceID,
		NextSeq: next, FirstUnackedSeq: first, AppliedRevision: rev, AppliedStateHash: hash,
		Capabilities: a.capabilities(), Built: a.cfg.Built,
	}
	if o := a.upd.Outcome(); o != nil {
		h.LastUpdate = o
	}
	a.helloOutcome.Store(h.LastUpdate)
	for _, p := range a.protocols {
		h.Engines = append(h.Engines, &pb.EngineInfo{Protocol: p, Version: a.engines[p].Version()})
	}
	f := a.host.Facts(ctx)
	h.Facts = &pb.HostFacts{
		Hostname: f.Hostname, Os: f.OS, Kernel: f.Kernel, Arch: f.Arch, CpuCount: uint32(f.CPUCount),
		RamTotalBytes: f.RAMTotal, DiskTotalBytes: f.DiskTotal, Virt: f.Virt, HasIpv6: f.HasIPv6,
	}
	if !f.Boot.IsZero() {
		h.Facts.BootUnix = f.Boot.Unix()
	}
	return &pb.ConnectRequest{Message: &pb.ConnectRequest_Hello{Hello: h}}
}

func (a *Agent) onHelloAck(ack *pb.HelloAck) {
	off := ack.ServerTimeUnix - time.Now().Unix()
	if ack.ServerTimeUnix == 0 {
		off = 0
	}
	a.offset.Store(off)
	if ack.Settings != nil {
		// A switch changed while the node was offline reaches it only here: the panel sends no desired state when the
		// inbound hashes match, and the sweep is up to 30 s away. The tunnel firewall follows the switch at once.
		changed := ack.Settings.GetClientIpv6Disabled() != a.settings.Load().GetClientIpv6Disabled()
		a.settings.Store(ack.Settings)
		if changed {
			select {
			case a.jobs <- job{sweep: true}:
			default: // the worker is busy and its queue is full: the next sweep will do
			}
		}
	}
	a.out.ack(ack.AckedSeq)
	a.upd.AckOutcome(a.helloOutcome.Swap(nil)) // the panel has this outcome now; stop repeating it
	if math.Abs(float64(off)) > 30 {
		if a.skewed.CompareAndSwap(false, true) {
			a.log.Warn("clock differs from the panel", "offset_s", off)
			a.event(pb.Severity_SEVERITY_WARNING, "clock_skew", "", map[string]string{"offset_s": strconv.FormatInt(off, 10)})
		}
	} else {
		a.skewed.Store(false)
	}
}

func (a *Agent) dispatch(s *session, msg *pb.ConnectResponse) {
	switch m := msg.Message.(type) {
	case *pb.ConnectResponse_Ack:
		a.out.ack(m.Ack.UpToSeq)
	case *pb.ConnectResponse_DesiredState:
		a.dsIn.Add(1)
		if !a.submit(s, job{ds: m.DesiredState}) {
			a.dsIn.Add(-1)
		}
	case *pb.ConnectResponse_Kick:
		a.submit(s, job{kick: m.Kick})
	case *pb.ConnectResponse_RestartInbound:
		a.submit(s, job{restart: m.RestartInbound})
	case *pb.ConnectResponse_Retire:
		a.submit(s, job{retire: m.Retire})
	case *pb.ConnectResponse_RunDoctor:
		a.goBusy(func() { a.runDoctor(s, m.RunDoctor) })
	case *pb.ConnectResponse_ApplyFix:
		a.goBusy(func() { a.applyFix(s, m.ApplyFix) })
	case *pb.ConnectResponse_PrepareAwgKernel:
		a.goBusy(func() { a.prepareAwgKernel(s, m.PrepareAwgKernel) })
	case *pb.ConnectResponse_MeasureBandwidth:
		a.goBusy(func() { a.measureBandwidth(s, m.MeasureBandwidth) })
	case *pb.ConnectResponse_UdpCount:
		a.goBusy(func() { a.udpCount(s, m.UdpCount) })
	case *pb.ConnectResponse_UdpSend:
		a.goBusy(func() { a.udpSend(s, m.UdpSend) })
	case *pb.ConnectResponse_UpdateAgent:
		a.goBusy(func() { a.applyUpdate(s, m.UpdateAgent) })
	case *pb.ConnectResponse_RollbackAgent:
		a.goBusy(func() { a.rollbackUpdate(s, m.RollbackAgent) })
	case *pb.ConnectResponse_LogRequest:
		s.startLog(m.LogRequest)
	case *pb.ConnectResponse_LogCancel:
		s.cancelLog(m.LogCancel.RequestId)
	case *pb.ConnectResponse_Ping:
		s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_Pong{Pong: &pb.Pong{Nonce: m.Ping.Nonce}}})
	case *pb.ConnectResponse_Renew:
		a.onRenewReply(m.Renew)
	case *pb.ConnectResponse_HelloAck:
		a.log.Warn("unexpected second HelloAck ignored")
	default:
		a.log.Warn("unknown panel message ignored")
	}
}

// push queues a reliable message and wakes the current stream's writer.
func (a *Agent) push(m *pb.ConnectRequest) {
	a.out.push(m)
	if s := a.cur.Load(); s != nil {
		s.poke()
	}
}

// event queues a reliable Event. Codes are listed in agent.proto.
func (a *Agent) event(sev pb.Severity, code, inboundID string, params map[string]string) {
	a.push(&pb.ConnectRequest{Message: &pb.ConnectRequest_Event{Event: &pb.Event{
		Severity: sev, Code: code, InboundId: inboundID, TimeUnix: a.now().Unix(), Params: params,
	}}})
}

// engineEvent accepts engine signals and rate-limits torrent detections before they enter the reliable event queue.
// uTP SYN reports wait for five detections to three destination ports within 10 minutes; all reported detections then
// use the existing one-per-inbound and user five-minute throttle (one per inbound without a known user). Only the fixed
// parameters leave the node: never a client address or a destination address.
func (a *Agent) engineEvent(ev engine.Event) {
	if ev.Code != "torrent_attempt" {
		return
	}
	keyPart := ev.Params["user_id"]
	if keyPart == "" {
		keyPart = "unknown"
	}
	key := ev.InboundID + "\x00" + keyPart
	now := a.now()
	a.torrentEventsMu.Lock()
	for k, last := range a.torrentEvents {
		if now.Sub(last) >= 15*time.Minute {
			delete(a.torrentEvents, k)
		}
	}
	if ev.Params["evidence"] == "utp_syn" {
		if !a.recordUTPSynLocked(key, now, parseTorrentPort(ev.Params["dst_port"])) {
			a.torrentEventsMu.Unlock()
			return
		}
	}
	if last := a.torrentEvents[key]; !last.IsZero() && now.Sub(last) < 5*time.Minute {
		a.torrentEventsMu.Unlock()
		return
	}
	if len(a.torrentEvents) >= 4096 {
		// Keep the throttle map bounded even on a node with very many users.
		for k, last := range a.torrentEvents {
			if now.Sub(last) >= 5*time.Minute {
				delete(a.torrentEvents, k)
			}
		}
		if len(a.torrentEvents) >= 4096 {
			a.torrentEventsMu.Unlock()
			return
		}
	}
	a.torrentEvents[key] = now
	a.torrentEventsMu.Unlock()

	params := make(map[string]string, 5)
	for _, k := range []string{"protocol", "torrent_protocol", "evidence", "dst_port", "user_id"} {
		if v := ev.Params[k]; v != "" && len(v) <= 256 {
			params[k] = v
		}
	}
	sev := pb.Severity_SEVERITY_INFO
	if ev.Warning {
		sev = pb.Severity_SEVERITY_WARNING
	}
	a.event(sev, ev.Code, ev.InboundID, params)
}

const (
	torrentUTPSynWindowDuration = 10 * time.Minute // ponytail: starting point, calibrate.
	torrentUTPSynMinDetections  = 5                // ponytail: starting point, calibrate.
	torrentUTPSynMinPorts       = 3                // ponytail: starting point, calibrate.
	torrentUTPSynStateMapLimit  = 4096
)

type torrentUTPSynPort struct {
	port uint16
	at   time.Time
}

// torrentUTPSynWindow keeps only the last five detection times and the three most recently seen destination ports.
// Those are enough to decide the threshold while keeping per-user state bounded and free of addresses.
type torrentUTPSynWindow struct {
	events    [torrentUTPSynMinDetections]time.Time
	eventNext int
	eventUsed int
	ports     [torrentUTPSynMinPorts]torrentUTPSynPort
	portUsed  int
	last      time.Time
}

func (w *torrentUTPSynWindow) add(now time.Time, port uint16) {
	w.events[w.eventNext] = now
	w.eventNext = (w.eventNext + 1) % len(w.events)
	if w.eventUsed < len(w.events) {
		w.eventUsed++
	}
	w.last = now
	if port == 0 {
		return
	}
	for i := 0; i < w.portUsed; i++ {
		if w.ports[i].port == port {
			w.ports[i].at = now
			return
		}
	}
	if w.portUsed < len(w.ports) {
		w.ports[w.portUsed] = torrentUTPSynPort{port: port, at: now}
		w.portUsed++
		return
	}
	oldest := 0
	for i := 1; i < len(w.ports); i++ {
		if w.ports[i].at.Before(w.ports[oldest].at) {
			oldest = i
		}
	}
	w.ports[oldest] = torrentUTPSynPort{port: port, at: now}
}

func (w *torrentUTPSynWindow) qualifies(now time.Time) bool {
	if w.eventUsed < torrentUTPSynMinDetections {
		return false
	}
	recentEvents := 0
	for _, at := range w.events {
		if !at.IsZero() && !at.After(now) && now.Sub(at) <= torrentUTPSynWindowDuration {
			recentEvents++
		}
	}
	if recentEvents < torrentUTPSynMinDetections {
		return false
	}
	recentPorts := 0
	for i := 0; i < w.portUsed; i++ {
		if !w.ports[i].at.IsZero() && !w.ports[i].at.After(now) && now.Sub(w.ports[i].at) <= torrentUTPSynWindowDuration {
			recentPorts++
		}
	}
	return recentPorts >= torrentUTPSynMinPorts
}

// recordUTPSynLocked records weak evidence and returns true only once the window has enough detections and ports.
// The caller holds torrentEventsMu, which protects both maps.
func (a *Agent) recordUTPSynLocked(key string, now time.Time, port uint16) bool {
	if a.torrentUTPSyn == nil {
		a.torrentUTPSyn = map[string]*torrentUTPSynWindow{}
	}
	for k, state := range a.torrentUTPSyn {
		if now.Sub(state.last) > torrentUTPSynWindowDuration {
			delete(a.torrentUTPSyn, k)
		}
	}
	state := a.torrentUTPSyn[key]
	if state == nil {
		if len(a.torrentUTPSyn) >= torrentUTPSynStateMapLimit {
			return false
		}
		state = &torrentUTPSynWindow{}
		a.torrentUTPSyn[key] = state
	}
	state.add(now, port)
	return state.qualifies(now)
}

func parseTorrentPort(value string) uint16 {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(port)
}

// ---------------------------------------------------------------------------------------------------
// Worker: everything that mutates engines or host state

type job struct {
	ctx           context.Context
	s             *session // where the reply goes; nil for internal jobs
	ds            *pb.DesiredState
	kick          *pb.Kick
	restart       *pb.RestartInbound
	reconnectWarp bool
	retire        *pb.Retire
	sweep         bool
	// res, when set, receives the restart's CommandResult instead of the stream (the doctor's restart_inbound).
	res chan *pb.CommandResult
}

func (a *Agent) submit(s *session, j job) bool {
	j.s = s
	select {
	case a.jobs <- j:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (a *Agent) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-a.jobs:
			a.handle(ctx, j)
		}
	}
}

func (a *Agent) handle(ctx context.Context, j job) {
	if a.exiting.Load() { // the engines are closed and the process is about to be replaced
		return
	}
	reply := func(m *pb.ConnectRequest) {
		if j.s != nil {
			j.s.send(m)
		}
	}
	switch {
	case j.ds != nil:
		r := a.applyDesired(ctx, j.ds)
		if r != nil {
			reply(&pb.ConnectRequest{Message: &pb.ConnectRequest_ApplyResult{ApplyResult: r}})
		}
		// An ignored old delta (nil) and APPLIED/PARTIAL count as applied; REJECTED and BASE_MISMATCH do not (the panel resends).
		a.dsOK.Store(r == nil || r.Status == pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Status == pb.ApplyStatus_APPLY_STATUS_PARTIAL)
		a.dsDone.Add(1)
	case j.kick != nil:
		reply(cmdResult(a.kick(ctx, j.kick)))
	case j.restart != nil:
		if r := a.restartInbound(ctx, j.restart); j.res != nil {
			j.res <- r
		} else {
			reply(cmdResult(r))
		}
	case j.reconnectWarp:
		reconnectCtx := j.ctx
		if reconnectCtx == nil {
			reconnectCtx = ctx
		}
		r := a.reconnectWarp(reconnectCtx)
		if j.res != nil {
			j.res <- r
		} else {
			reply(cmdResult(r))
		}
	case j.retire != nil:
		a.retire(ctx, j)
	case j.sweep:
		if m := a.snapshotModel(); m.revision != 0 {
			a.reconcile(ctx, m, nil)
		}
	}
}

func cmdResult(r *pb.CommandResult) *pb.ConnectRequest {
	return &pb.ConnectRequest{Message: &pb.ConnectRequest_CommandResult{CommandResult: r}}
}

// sweepLoop makes the agent end credential terms by itself: the worker re-reconciles and only inbounds
// whose active credential set changed are touched.
func (a *Agent) sweepLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.sweepEvery):
		}
		select {
		case a.jobs <- job{sweep: true}:
		default: // the worker is busy; the next tick will do
		}
	}
}

// ---------------------------------------------------------------------------------------------------
// Commands

func (a *Agent) kick(ctx context.Context, k *pb.Kick) *pb.CommandResult {
	var n int
	var errs []error
	for _, p := range a.protocols {
		c, err := a.engines[p].Kick(ctx, k.CredIds)
		n += c
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	a.log.Info("kick", "credentials", len(k.CredIds), "sessions", n, "reason", k.Reason)
	return &pb.CommandResult{RequestId: k.RequestId, Ok: len(errs) == 0, Error: errString(errs), Affected: uint32(n)}
}

func (a *Agent) restartInbound(ctx context.Context, r *pb.RestartInbound) *pb.CommandResult {
	m := a.snapshotModel()
	ids := m.ids()
	if r.InboundId != "" {
		if m.inbounds[r.InboundId] == nil {
			return &pb.CommandResult{RequestId: r.RequestId, Error: "unknown inbound " + r.InboundId}
		}
		ids = []string{r.InboundId}
	}
	force := map[string]bool{}
	var errs []error
	for _, id := range ids {
		if err := a.engines[m.inbounds[id].spec.Protocol].Remove(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		delete(a.held, id)
		force[id] = true
	}
	var affected uint32
	for _, res := range a.reconcile(ctx, m, force) {
		if !force[res.InboundId] {
			continue
		}
		if res.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", res.InboundId, res.Error))
		} else {
			affected++
		}
	}
	return &pb.CommandResult{RequestId: r.RequestId, Ok: len(errs) == 0, Error: errString(errs), Affected: affected}
}

// retire stops the engines, removes what the agent installed on the host, answers, deletes its own state
// and key material and makes Run return ErrRetired. It always runs to the end: the panel has already
// revoked the certificate, so a half-retired node that keeps trying would only loop. Failures are
// reported in the CommandResult.
func (a *Agent) retire(ctx context.Context, j job) {
	a.log.Warn("retire requested by the panel")
	a.busy.Add(1) // the answer and the drain below are on the session: it is not to be ended meanwhile
	defer a.busy.Add(-1)
	a.closeEngines()
	var errs []error
	hctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// The WARP tunnel, its routes and rules: an orphaned "unreachable default" would black-hole sockets. Only when this node
	// has (or routes into) WARP: the cleanup removes only our rules and routes, but its routing table number may also be
	// another tool's on the host (wg-quick: 51820), so a node that never had WARP does not run it on retire.
	if a.cfg.Warp != nil && (a.warpConfigured() || a.warpRouted) {
		if err := a.cfg.Warp.Cleanup(hctx); err != nil {
			errs = append(errs, fmt.Errorf("warp cleanup: %w", err))
		}
	}
	if err := a.host.Cleanup(hctx); err != nil {
		errs = append(errs, fmt.Errorf("host cleanup: %w", err))
	}
	cancel()
	res := &pb.CommandResult{RequestId: j.retire.RequestId, Ok: len(errs) == 0, Error: errString(errs)}
	if j.s != nil {
		j.s.sendSync(cmdResult(res), 10*time.Second)
		j.s.drain(3 * time.Second)
	}
	if err := wipeState(a.cfg.StateDir); err != nil {
		a.log.Error("could not delete the state directory", "err", err)
	}
	a.cancel(ErrRetired)
}

// wipeState deletes key material first, then everything else inside the state directory. It refuses to
// touch a directory that does not look like ours (no agent.json) or that is a filesystem root.
func wipeState(dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, fileMeta)); err != nil || filepath.Dir(root) == root {
		return fmt.Errorf("%s does not look like an agent state directory", root)
	}
	_ = os.Remove(filepath.Join(root, fileIdentity))
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	_ = os.Remove(root) // fails harmlessly when the directory is a mount point
	return errors.Join(errs...)
}

func errString(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	return errors.Join(errs...).Error()
}
