package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	helloTimeout    = 15 * time.Second
	ackEvery        = time.Second
	outQueue        = 256
	maxFuture       = 5 * time.Minute // agent timestamps further ahead are clamped to the receive time
	maxStatsAge     = 48 * time.Hour  // older batches are attributed to the receive hour
	maxEventParam   = 16
	maxEngines      = 32 // engines listed in one Hello
	capTorrentGuard = "torrentguard/1"
	capClientIPv6   = "client-ipv6/1" // NodeSettings.client_ipv6_disabled
	capWSLink       = "ws-link/1"
)

type agentService struct{ f *Fleet }

// onlineSess is one open client session, resolved to user x device x protocol.
type onlineSess struct {
	userID, deviceID, protocol, inboundID, remoteIP string
	since                                           time.Time
}

type logSub struct {
	ch      chan *agentv1.LogChunk
	dropped atomic.Uint32
}

// session is the one live agent session of a node.
type session struct {
	f        *Fleet
	nodeID   string
	owner    uint64
	caps     []string // Hello.capabilities: optional features of this agent build (health.go)
	ctx      context.Context
	cancel   context.CancelCauseFunc
	done     chan struct{} // closed when the Connect handler returns
	out      chan *agentv1.ConnectResponse
	liveness atomic.Int64 // nanoseconds of silence tolerated

	coreMu          sync.Mutex
	core            *SessionCore
	coreState       SessionState
	coreSidecar     SessionSidecar
	reconcileMu     sync.Mutex
	autoBandwidthAt time.Time

	// Desired-state bookkeeping: what the agent holds as far as we know. Guarded by desMu, which also
	// serializes pushes to this node.
	desMu sync.Mutex
	sent  *nodeState

	// Live data shown in the UI. Guarded by liveMu.
	liveMu    sync.Mutex
	metrics   *agentv1.HostMetrics
	metricsAt time.Time // panel receive time of the newest host metrics sample
	health    []*agentv1.InboundHealth
	online    []onlineSess
	userDown  map[string]uint64 // bits/s per user, from the newest batch
	userUp    map[string]uint64
	lastSeen  time.Time
	drift     bool
	cmds      map[string]chan *agentv1.CommandResult
	docs      map[string]chan *agentv1.DoctorReport // RunDoctor requests in flight (health.go)
	logs      map[string]*logSub
}

func (s *session) syncCoreFields() {
	state, sidecar := s.coreState, s.coreSidecar
	s.liveness.Store(state.LivenessNanos)
	s.desMu.Lock()
	s.sent = sidecar.SentDesired
	s.desMu.Unlock()
	s.liveMu.Lock()
	s.drift = state.Drift
	s.lastSeen = timeFromUnixNano(state.LastSeenUnixNano)
	s.liveMu.Unlock()
}

func (s *session) stepCore(ctx context.Context, event SessionEvent, pc *peerCert) (Transition, error) {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	if s.core == nil {
		s.core = NewSessionCore(s.f)
	}
	tr, err := s.core.Step(ctx, s.coreState, s.coreSidecar, event)
	if err != nil {
		// Step may have mutated maps shared with the current sidecar, so any error ends this session.
		if s.cancel != nil {
			s.cancel(err)
		}
		return Transition{}, err
	}
	s.coreState, s.coreSidecar = tr.State, tr.Sidecar
	s.syncCoreFields()
	if tr.Close == nil {
		err = s.dispatchCoreTransition(ctx, &tr, pc)
		if err != nil && s.cancel != nil {
			s.cancel(err)
		} else {
			s.coreState, s.coreSidecar = tr.State, tr.Sidecar
			s.syncCoreFields()
		}
	}
	return tr, err
}

func (s *session) stepDesired(ctx context.Context, event SessionEvent, pc *peerCert) (Transition, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	prepared, err := s.f.prepareDesiredState(ctx, s.nodeID, s.caps)
	if err != nil {
		return Transition{}, err
	}
	event.Prepared = prepared
	return s.stepCore(ctx, event, pc)
}

func (s *session) dispatchCoreTransition(ctx context.Context, tr *Transition, pc *peerCert) error {
	var warpEffects []SessionEffect
	for _, effect := range tr.Effects {
		if effect.Kind == EffectWarpAttention {
			warpEffects = append(warpEffects, effect)
			continue
		}
		if err := s.dispatchCoreEffect(ctx, tr, effect, pc); err != nil {
			return err
		}
	}
	// Sent state is recorded before enqueue; queue overflow cancels the session and teardown discards that state.
	var frameErr error
	for _, frame := range tr.Frames {
		if !s.enqueue(frame) {
			frameErr = errAgentQueueFull
			break
		}
	}
	for _, effect := range warpEffects {
		if err := s.dispatchCoreEffect(ctx, tr, effect, pc); err != nil {
			if frameErr == nil {
				frameErr = err
			}
		}
	}
	return frameErr
}

func (s *session) dispatchCoreEffect(ctx context.Context, tr *Transition, effect SessionEffect, pc *peerCert) error {
	switch effect.Kind {
	case EffectSessionStarted:
		if effect.Started != nil {
			started := effect.Started
			s.f.connectEvents(ctx, s.nodeID, started.Previous, started.BootAt, started.Now)
			if started.AutoMeasure {
				s.autoBandwidthAt = s.f.now().UTC()
				deadline := s.autoBandwidthAt.Add(s.f.measureDelay)
				s.coreState.AutoBandwidthUnixNano = deadline.UnixNano()
				s.f.autoMeasureBandwidth(s, deadline)
			}
		}
	case EffectAutoBandwidth:
		s.f.runAutoMeasureBandwidth(s)
	case EffectLiveUpdate:
		if effect.Live != nil {
			// Health-store writes finish before the live snapshot becomes visible; its final contents are unchanged.
			s.liveMu.Lock()
			s.metrics, s.metricsAt = effect.Live.Metrics, effect.Live.MetricsAt
			s.health, s.online = effect.Live.Health, effect.Live.Online
			s.userDown, s.userUp = effect.Live.UserDown, effect.Live.UserUp
			s.liveMu.Unlock()
		}
	case EffectUsage:
		if s.f.cfg.OnUsage != nil {
			s.f.cfg.OnUsage(ctx, effect.Users)
		}
	case EffectCommandResult:
		s.deliverCommand(effect.CommandResult)
	case EffectDoctorReport:
		s.f.onDoctorReport(ctx, s, effect.DoctorReport)
	case EffectLogChunk:
		s.deliverLog(effect.LogChunk)
	case EffectAwgPrepare:
		if effect.Event != nil {
			s.f.onAwgPrepareEvent(ctx, s.nodeID, *effect.Event)
		}
	case EffectCheckCertificate:
		if pc != nil {
			if err := s.f.recheckCert(ctx, *pc); err != nil {
				return connect.NewError(connect.CodeUnauthenticated, err)
			}
		}
	case EffectWarpAttention:
		w := s.f.warpModule()
		if w == nil {
			return nil
		}
		if effect.WarpReason == warpReasonRefresh {
			now := s.f.now().UTC()
			if !tr.Sidecar.L3.warpAsk.IsZero() && now.Sub(tr.Sidecar.L3.warpAsk) < warpRefreshGap {
				return nil
			}
			tr.Sidecar.L3.warpAsk = now
		}
		s.f.dispatchWarpAttention(w, s.nodeID, effect.WarpReason)
	case EffectDesiredReconcile:
		go func() {
			if err := s.f.reconcile(s.ctx, s, effect.ReconcileMode); err != nil {
				s.f.log.Warn(effect.ErrorLog, "node", s.nodeID, "err", err)
			}
		}()
	}
	return nil
}

func timeFromUnixNano(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

func (s *session) livenessDur() time.Duration { return time.Duration(s.liveness.Load()) }

// enqueue queues a message for the stream goroutine. A full queue means the agent does not keep up: drop the stream
// (it reconnects and resyncs) rather than block the fleet.
func (s *session) enqueue(m *agentv1.ConnectResponse) bool {
	select {
	case s.out <- m:
		return true
	default:
		s.cancel(connect.NewError(connect.CodeResourceExhausted, errors.New("agent does not keep up")))
		return false
	}
}

// claimOwner gives an authenticated transport the right to register after Hello and closes its predecessor.
func (f *Fleet) claimOwner(nodeID string, parent context.Context) (uint64, context.Context) {
	ctx, cancel := context.WithCancelCause(parent)
	f.mu.Lock()
	old := f.owners[nodeID]
	owner := old.generation + 1
	f.owners[nodeID] = sessionOwner{generation: owner, cancel: cancel}
	f.mu.Unlock()
	if old.cancel != nil {
		old.cancel(connect.NewError(connect.CodeAborted, errors.New("superseded by a newer stream")))
	}
	return owner, ctx
}

func (f *Fleet) ownsSession(nodeID string, owner uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners[nodeID].generation == owner
}

// register makes s the node's stream if no newer authenticated transport has claimed ownership.
func (f *Fleet) register(s *session) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[s.nodeID].generation != s.owner {
		return false
	}
	f.sessions[s.nodeID] = s
	return true
}

// unregister removes s if it is still registered and reports whether it still owned the node.
func (f *Fleet) unregister(s *session) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions[s.nodeID] != s {
		return false
	}
	delete(f.sessions, s.nodeID)
	return f.owners[s.nodeID].generation == s.owner
}

func errOrCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	var ce *connect.Error
	if errors.As(cause, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeCanceled, errors.New("stream closed"))
}

// Connect is the long-lived stream of one node (agent.proto "ONE STREAM, ONE OWNER").
func (a agentService) Connect(ctx context.Context, stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]) error {
	id, ok := nodeID(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("no node certificate"))
	}
	pc, _ := peerCertFrom(ctx)
	owner, ownerCtx := a.f.claimOwner(id, ctx)
	return a.runSession(ownerCtx, id, pc, owner, connectSessionStream{ctx: ownerCtx, stream: stream})
}

func (a agentService) runSession(ctx context.Context, id string, pc peerCert, owner uint64, stream agentSessionStream) error {
	f := a.f
	ctx = stream.Context()
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	msgs := make(chan *agentv1.ConnectRequest, 16)
	rerr := make(chan error, 1)
	go func() {
		defer close(msgs)
		for {
			m, err := stream.Receive()
			if err != nil {
				rerr <- err
				return
			}
			select {
			case msgs <- m:
			case <-sctx.Done():
				return
			}
		}
	}()
	endErr := func() error {
		if sctx.Err() != nil {
			return errOrCause(sctx)
		}
		select {
		case err := <-rerr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		default:
			return errOrCause(sctx)
		}
	}

	core := NewSessionCore(f)
	opened, err := core.Step(sctx, SessionState{Version: sessionStateVersion, NodeID: id, OwnerGeneration: owner}, SessionSidecar{},
		SessionEvent{Kind: EventOpen, At: f.now().UTC()})
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	state, sidecar := opened.State, opened.Sidecar
	helloTimer := time.NewTimer(helloTimeout)
	defer helloTimer.Stop()
	var first *agentv1.ConnectRequest
	select {
	case m, ok := <-msgs:
		if !ok {
			return endErr()
		}
		first = m
	case <-helloTimer.C:
		deadline := timeFromUnixNano(state.HelloDeadlineUnixNano)
		tr, alarmErr := core.Step(sctx, state, sidecar, SessionEvent{Kind: EventAlarm, Alarm: AlarmHello, At: deadline})
		if alarmErr == nil && tr.Close != nil {
			return sessionCloseError(tr.Close)
		}
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("no Hello"))
	case <-sctx.Done():
		return errOrCause(sctx)
	}

	tr, stepErr := core.Step(sctx, state, sidecar, SessionEvent{Kind: EventHello, At: f.now().UTC(), Frame: first})
	if tr.Close != nil && tr.State.InstanceID == "" {
		return sessionCloseError(tr.Close)
	}
	if stepErr != nil && tr.State.InstanceID == "" {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if tr.State.InstanceID == "" {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	s := &session{f: f, nodeID: id, owner: owner, caps: slices.Clone(tr.State.Capabilities), ctx: sctx, cancel: cancel,
		done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		userDown: map[string]uint64{}, userUp: map[string]uint64{},
		cmds: map[string]chan *agentv1.CommandResult{}, docs: map[string]chan *agentv1.DoctorReport{},
		logs: map[string]*logSub{}, core: core, coreState: tr.State, coreSidecar: tr.Sidecar}
	s.coreMu.Lock()
	s.syncCoreFields()
	if !f.register(s) {
		s.coreMu.Unlock()
		return connect.NewError(connect.CodeAborted, errors.New("superseded by a newer stream"))
	}
	defer func() {
		cancel(nil)
		close(s.done)
		_, _ = s.stepCore(sctx, SessionEvent{Kind: EventDisconnected, At: f.now().UTC()}, &pc)
		if f.unregister(s) {
			dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer dcancel()
			s.liveMu.Lock()
			seen := s.lastSeen
			s.liveMu.Unlock()
			if err := f.st.NodeDisconnected(dctx, id, seen, f.now().UTC()); err != nil {
				f.log.Warn("record disconnect", "node", id, "err", err)
			}
		}
	}()
	helloErr := s.dispatchCoreTransition(sctx, &tr, &pc)
	s.coreMu.Unlock()
	if helloErr != nil {
		f.log.Error("hello response", "node", id, "err", helloErr)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// Core-mediated admin frames wait until HelloAck is queued; Retire remains a direct send.
	tr, err = s.stepDesired(sctx, SessionEvent{Kind: EventInitialReconcile, At: f.now().UTC(), Frame: first,
		AutoBandwidthAt: s.autoBandwidthAt}, &pc)
	if err != nil {
		f.log.Error("initial desired state", "node", id, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if tr.Close != nil {
		return sessionCloseError(tr.Close)
	}

	live := time.NewTimer(s.livenessDur())
	defer live.Stop()
	ackT := time.NewTicker(ackEvery)
	defer ackT.Stop()
	// These tickers retain the VPS loop's existing phase. Each firing is fed to the core as an alarm.
	certT := time.NewTicker(f.certCheck)
	defer certT.Stop()
	_, _ = s.stepCore(sctx, SessionEvent{Kind: EventTimersStarted, At: f.now().UTC()}, &pc)
	for {
		select {
		case <-sctx.Done():
			var ce *connect.Error
			if errors.As(context.Cause(sctx), &ce) && ce.Code() == connect.CodeAborted {
				_, _ = s.stepCore(sctx, SessionEvent{Kind: EventOwnerSuperseded, At: f.now().UTC()}, &pc)
			}
			return errOrCause(sctx)
		case <-certT.C:
			_, err := s.stepCore(sctx, SessionEvent{Kind: EventAlarm, Alarm: AlarmCertificate, At: f.now().UTC()}, &pc)
			if err != nil {
				return err
			}
		case <-live.C:
			s.coreMu.Lock()
			deadline := s.coreState.LivenessDeadlineUnixNano
			s.coreMu.Unlock()
			at := timeFromUnixNano(deadline)
			if at.IsZero() {
				at = f.now().UTC()
			}
			tr, err := s.stepCore(sctx, SessionEvent{Kind: EventAlarm, Alarm: AlarmLiveness, At: at}, &pc)
			if err != nil {
				return err
			}
			if tr.Close != nil {
				return sessionCloseError(tr.Close)
			}
		case <-ackT.C:
			_, _ = s.stepCore(sctx, SessionEvent{Kind: EventAlarm, Alarm: AlarmAck, At: f.now().UTC()}, &pc)
		case m := <-s.out:
			// Only this goroutine writes to the stream (after the handler returns nobody may).
			if err := stream.Send(m); err != nil {
				return err
			}
		case m, ok := <-msgs:
			if !ok {
				return endErr()
			}
			live.Reset(s.livenessDur())
			tr, err := s.stepCore(sctx, SessionEvent{Kind: EventAgentFrame, At: f.now().UTC(), Frame: m}, &pc)
			if errors.Is(err, errAgentQueueFull) {
				continue
			}
			if err != nil {
				return err
			}
			if tr.Close != nil {
				return sessionCloseError(tr.Close)
			}
		}
	}
}

var errAgentQueueFull = errors.New("agent queue full")

func sessionCloseError(close *SessionClose) error {
	if close == nil {
		return nil
	}
	code := connect.CodeInternal
	switch close.Class {
	case CloseInvalidArgument:
		code = connect.CodeInvalidArgument
	case CloseFailedPrecondition:
		code = connect.CodeFailedPrecondition
	case CloseUnauthenticated:
		code = connect.CodeUnauthenticated
	case CloseConflict:
		code = connect.CodeAborted
	case CloseDeadline:
		code = connect.CodeDeadlineExceeded
	case CloseCanceled:
		code = connect.CodeCanceled
	case CloseInternal:
		code = connect.CodeInternal
	}
	return connect.NewError(code, errors.New(close.Reason))
}

// helloInfo takes what the node says about itself. Every string is bounded: they are stored and shown in the UI.
func helloInfo(h *agentv1.Hello) store.HelloInfo {
	info := store.HelloInfo{AgentVersion: clip(h.AgentVersion, 64), APIVersion: h.ApiVersion, Instance: h.InstanceId,
		Built: h.Built, Caps: capabilities(h), LastUpdateJSON: lastUpdateJSON(h.LastUpdate)}
	if ft := h.Facts; ft != nil {
		if ft.BootUnix > 0 {
			info.BootAt = time.Unix(ft.BootUnix, 0).UTC()
		}
		info.Facts = store.NodeFactsRow{Hostname: clip(ft.Hostname, 128), OS: clip(ft.Os, 128), Kernel: clip(ft.Kernel, 128), Arch: clip(ft.Arch, 32),
			CPUCount: ft.CpuCount, RAMTotal: min(ft.RamTotalBytes, 1<<62), DiskTotal: min(ft.DiskTotalBytes, 1<<62), Virt: clip(ft.Virt, 64), HasIPv6: ft.HasIpv6}
	}
	for i, e := range h.Engines {
		if i == maxEngines {
			break
		}
		info.Facts.Engines = append(info.Facts.Engines, store.EngineRow{Protocol: clip(e.Protocol, 64), Version: clip(e.Version, 64)})
	}
	return info
}

// connectEvents records a blip (short gap) or a recovery (after node_down) when a node comes back.
func (f *Fleet) connectEvents(ctx context.Context, id string, prev store.NodeRow, boot, now time.Time) {
	if prev.State != "active" || f.updating(id) { // the re-exec of a self-update is neither a blip nor an outage
		return
	}
	last := latest(prev.LastSeenAt, prev.LastDisconnectedAt)
	if last.IsZero() {
		return
	}
	gap := now.Sub(last)
	params := map[string]string{"minutes": fmt.Sprint(int(gap.Minutes()))}
	// Boot time is derived by the agent from uptime and jitters by a second or two: only a real
	// difference means the host rebooted (same boot time = the hoster blipped).
	if !prev.BootAt.IsZero() && !boot.IsZero() {
		d := boot.Sub(prev.BootAt)
		params["rebooted"] = fmt.Sprint(d > time.Minute || d < -time.Minute)
	}
	if code, _ := f.st.LastNodeEventCode(ctx, id, "node_down", "node_recovered"); code == "node_down" {
		f.event(ctx, 1, "node_recovered", id, params)
	} else if gap >= time.Minute && gap < blipWindow {
		f.event(ctx, 1, "node_blip", id, params)
		if h := f.hooks(); h != nil && params["rebooted"] != "true" {
			h.NodeReturned(ctx, id, last, now) // the history record of the blip
		}
	}
}

func bucketHour(end, now time.Time) (hour int64, stale bool) {
	t := end
	switch {
	case t.After(now.Add(maxFuture)):
		t = now
	case t.Before(now.Add(-maxStatsAge)):
		t, stale = now, true
	}
	h := t.Unix()
	return h - h%3600, stale
}

func clip(s string, n int) string { return store.Clip(s, n) }

func runState(s agentv1.InboundRunState) string {
	switch s {
	case agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING:
		return "active"
	case agentv1.InboundRunState_INBOUND_RUN_STATE_FAILED:
		return "failed"
	}
	return "pending"
}

// onApply records an ApplyResult and reacts: BASE_MISMATCH -> full resend; a hash that differs from what
// was sent -> state_drift event and one full resend (a second drift right after stays as an error event).
type reconcileMode int

const (
	reconcileConnect reconcileMode = iota // after HelloAck: send a full state unless the agent already holds the desired one
	reconcileChange                       // something changed: send a delta if the hash moved
	reconcileFull                         // resend everything (base mismatch, drift)
)

// nodeSettings is what the agent of a stream with these capabilities is told. The AWG, torrent blocker and client IPv6 settings
// are sent only to agents that advertise the capability, keeping old-agent settings signatures unchanged.
// An empty resolver list stays empty: the node then uses the server's own resolver (some hosters allow only theirs);
// country defaults are a preset the owner picks, never a silent substitute.
func nodeSettings(n store.NodeRow, caps []string) *agentv1.NodeSettings {
	s := &agentv1.NodeSettings{StatsIntervalS: defaultStatsIntervalS, KeepaliveIntervalS: defaultKeepaliveInterval,
		KeepaliveTimeoutS: defaultKeepaliveTimeout, DialTimeoutS: uint32(n.DialTimeoutS), DnsResolvers: slices.Clone(n.DNSResolvers),
		CountryCode: n.CountryCode}
	if slices.Contains(caps, capAWG) {
		s.AwgBackend = n.AwgBackend
	}
	if slices.Contains(caps, capTorrentGuard) {
		s.TorrentBlockerEnabled = n.TorrentBlockerEnabled
	}
	if slices.Contains(caps, capClientIPv6) {
		s.ClientIpv6Disabled = !n.ClientIPv6
	}
	return s
}

func settingsSig(s *agentv1.NodeSettings) string {
	sig := fmt.Sprint(s.StatsIntervalS, s.KeepaliveIntervalS, s.KeepaliveTimeoutS, s.DialTimeoutS, s.DnsResolvers, s.CountryCode, s.AwgBackend)
	if s.TorrentBlockerEnabled {
		sig += "|torrentguard=enabled"
	}
	if s.ClientIpv6Disabled {
		sig += "|client-ipv6=off"
	}
	return sig
}

// reconcile computes the node's desired state and sends what the agent lacks. Revisions are per node,
// strictly increasing and persisted; every message that is sent gets a fresh one.
func (f *Fleet) reconcile(ctx context.Context, s *session, mode reconcileMode) error {
	tr, err := s.stepDesired(ctx, SessionEvent{Kind: EventDesiredChanged, At: f.now().UTC(), Mode: mode}, nil)
	if err != nil {
		return err
	}
	if tr.Close != nil {
		return sessionCloseError(tr.Close)
	}
	return nil
}

// --- commands and logs ---

var (
	errNoAnswer = connect.NewError(connect.CodeDeadlineExceeded, errors.New("the node did not answer in time"))
	errLinkLost = connect.NewError(connect.CodeUnavailable, errors.New("the node link dropped"))
)

// roundtrip sends a command built with a fresh request id and waits for its CommandResult.
func (s *session) roundtrip(ctx context.Context, wait time.Duration, build func(reqID string) *agentv1.ConnectResponse) (*agentv1.CommandResult, error) {
	id := store.NewID("req_")
	ch := make(chan *agentv1.CommandResult, 1)
	s.liveMu.Lock()
	s.cmds[id] = ch
	s.liveMu.Unlock()
	defer func() {
		s.liveMu.Lock()
		delete(s.cmds, id)
		s.liveMu.Unlock()
	}()
	requestAt := s.f.now().UTC()
	tr, err := s.stepCore(ctx, SessionEvent{Kind: EventAdminCommand, At: requestAt, Request: &AdminRequest{
		RequestID: id, Deadline: requestAt.Add(wait), Frame: build(id), Kind: PendingCommand,
	}}, nil)
	if err != nil {
		return nil, errLinkLost
	}
	if tr.Close != nil {
		return nil, sessionCloseError(tr.Close)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-ch:
		return r, nil
	case <-t.C:
		return nil, errNoAnswer
	case <-s.done:
		select { // an answer that arrived just before the stream ended (UpdateAgent: the agent re-executes) still counts
		case r := <-ch:
			return r, nil
		default:
		}
		return nil, errLinkLost
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *session) deliverCommand(r *agentv1.CommandResult) {
	s.liveMu.Lock()
	ch := s.cmds[r.RequestId]
	s.liveMu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}

func (s *session) deliverLog(c *agentv1.LogChunk) {
	s.liveMu.Lock()
	sub := s.logs[c.RequestId]
	s.liveMu.Unlock()
	if sub == nil {
		return
	}
	select {
	case sub.ch <- c:
	default:
		sub.dropped.Add(uint32(len(c.Lines)))
	}
}
