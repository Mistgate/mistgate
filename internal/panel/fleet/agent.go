package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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
	userID, deviceID, protocol, inboundID string
	since                                 time.Time
}

type logSub struct {
	ch      chan *agentv1.LogChunk
	dropped atomic.Uint32
}

// sessionView is one immutable admin snapshot published after each core step.
type sessionView struct {
	State SessionState
	Live  LiveSnapshot
}

// session is the one live agent session of a node.
type session struct {
	f          *Fleet
	nodeID     string
	owner      uint64
	caps       []string // Hello.capabilities: optional features of this agent build (health.go)
	ctx        context.Context
	cancel     context.CancelCauseFunc
	done       chan struct{} // closed when the Connect handler returns
	out        chan *agentv1.ConnectResponse
	alarmTimer *time.Timer

	coreMu      sync.Mutex
	core        *SessionCore
	coreState   SessionState
	coreSidecar SessionSidecar
	view        atomic.Pointer[sessionView]

	// Only request waiters need their own lock; the admin view is an immutable atomic snapshot.
	waitMu sync.Mutex
	cmds   map[string]chan *agentv1.CommandResult
	docs   map[string]chan *agentv1.DoctorReport // RunDoctor requests in flight (health.go)
	logs   map[string]*logSub
}

func (s *session) publishView() {
	state := s.coreState
	state.Capabilities = slices.Clone(state.Capabilities)
	state.SentWithheld = slices.Clone(state.SentWithheld)
	state.Pending = maps.Clone(state.Pending)
	state.L3.AWG = maps.Clone(state.L3.AWG)
	s.view.Store(&sessionView{State: state, Live: s.coreSidecar.Live})
}

func (s *session) stepCore(ctx context.Context, event SessionEvent) (Transition, error) {
	return s.step(ctx, event, 0)
}

// step runs one core step. The effect of kind inline (0 for none) is not dispatched: the caller runs it itself.
func (s *session) step(ctx context.Context, event SessionEvent, inline EffectKind) (Transition, error) {
	s.coreMu.Lock()
	var oldPoison *PoisonBatch
	if s.coreState.Poison != nil {
		poison := *s.coreState.Poison
		oldPoison = &poison
	}
	tr, err := s.core.Step(ctx, &s.coreState, &s.coreSidecar, event)
	s.publishView()
	if !samePoison(oldPoison, s.coreState.Poison) {
		s.persistPoison(s.coreState.Poison)
	}
	resetSessionAlarm(s.alarmTimer, tr.NextAlarm, event.At)
	if err != nil {
		s.coreMu.Unlock()
		s.endOnError(err)
		return Transition{}, err
	}
	if tr.Close != nil {
		s.coreMu.Unlock()
		s.cancel(sessionCloseError(tr.Close))
		return tr, nil
	}
	err = s.enqueueFrames(tr.Frames)
	s.coreMu.Unlock()
	effects := tr.Effects
	if inline != 0 {
		effects = slices.DeleteFunc(slices.Clone(effects), func(e SessionEffect) bool { return e.Kind == inline })
	}
	s.dispatchCoreEffects(ctx, effects)
	if err != nil {
		s.endOnError(err)
	}
	return tr, err
}

// stepDesired is the only place that creates EventDesiredChanged: it runs the first preparation round inline, and the
// core's follow-up rounds start in the background.
func (s *session) stepDesired(ctx context.Context) (Transition, error) {
	tr, err := s.step(ctx, SessionEvent{Kind: EventDesiredChanged, At: s.f.now()}, EffectPrepareDesired)
	if err != nil {
		return Transition{}, err
	}
	if !transitionHasEffect(tr, EffectPrepareDesired) || tr.Close != nil {
		return tr, nil
	}
	return s.runDesiredPreparation(ctx)
}

// runDesiredPreparation performs one read and hands the result (or the error) to the core. The resulting step starts
// another round in the background if the core still has a dirty preparation.
func (s *session) runDesiredPreparation(ctx context.Context) (Transition, error) {
	if !s.canApplyPrepared() {
		return Transition{}, nil
	}
	prepared, prepareErr := s.f.prepareDesiredState(ctx, s.nodeID, s.caps)
	if !s.canApplyPrepared() {
		return Transition{}, nil
	}
	tr, err := s.stepCore(ctx, SessionEvent{Kind: EventDesiredPrepared, At: s.f.now(), Prepared: prepared, Err: prepareErr})
	if prepareErr != nil && ctx.Err() == nil {
		s.f.log.Warn("prepare desired state", "node", s.nodeID, "err", prepareErr)
	}
	return tr, err
}

func (s *session) canApplyPrepared() bool {
	return s.ctx.Err() == nil && s.f.ownsSession(s.nodeID, s.owner)
}

func (s *session) startDesiredPreparation() {
	go func() {
		_, _ = s.runDesiredPreparation(s.ctx)
	}()
}

// endOnError ends the session on a step's error and logs the cause once, while the session was still live (a failed
// background preparation would otherwise end it as a bare "stream closed").
func (s *session) endOnError(err error) {
	if s.ctx.Err() == nil {
		s.f.log.Error("session step", "node", s.nodeID, "err", err)
	}
	var ce *connect.Error
	switch {
	case errors.Is(err, errAgentQueueFull):
		err = connect.NewError(connect.CodeResourceExhausted, errors.New("agent does not keep up"))
	case !errors.As(err, &ce):
		err = connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	s.cancel(err)
}

func transitionHasEffect(tr Transition, kind EffectKind) bool {
	for _, effect := range tr.Effects {
		if effect.Kind == kind {
			return true
		}
	}
	return false
}

func samePoison(a, b *PoisonBatch) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// enqueueFrames queues one step's frames. The caller holds coreMu, so the agent receives frames in the order the core
// recorded them as sent (a delta never overtakes its base). Sent state is recorded before enqueue; queue overflow
// cancels the session and teardown discards that state.
func (s *session) enqueueFrames(frames []*agentv1.ConnectResponse) error {
	for _, frame := range frames {
		if !s.enqueue(frame) {
			return errAgentQueueFull
		}
	}
	return nil
}

// dispatchCoreEffects runs one step's effects after its frames are queued, outside coreMu.
func (s *session) dispatchCoreEffects(ctx context.Context, effects []SessionEffect) {
	for _, effect := range effects {
		s.dispatchCoreEffect(ctx, effect)
	}
}

func (s *session) dispatchCoreEffect(ctx context.Context, effect SessionEffect) {
	switch effect.Kind {
	case EffectUsage:
		if s.f.cfg.OnUsage != nil {
			s.f.cfg.OnUsage(ctx, effect.Users)
		}
	case EffectCommandResult:
		s.deliverCommand(effect.CommandResult)
	case EffectDoctorReport:
		s.deliverDoctor(effect.DoctorReport)
	case EffectLogChunk:
		s.deliverLog(effect.LogChunk)
	case EffectWarpAttention:
		if w := s.f.warpModule(); w != nil {
			s.f.dispatchWarpAttention(w, s.nodeID, effect.WarpReason)
		}
	case EffectConnectEvents:
		if effect.PreviousNode != nil {
			s.f.connectEvents(ctx, s.nodeID, *effect.PreviousNode, effect.BootAt.UTC(), effect.At.UTC())
		}
	case EffectPrepareDesired:
		s.startDesiredPreparation()
	}
}

func (s *session) persistPoison(poison *PoisonBatch) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if s.f.owners[s.nodeID].generation != s.owner {
		return
	}
	if poison == nil {
		delete(s.f.stuck, s.nodeID)
		return
	}
	s.f.stuck[s.nodeID] = stuckSeq{instance: poison.Instance, seq: poison.Seq}
}

func resetSessionAlarm(timer *time.Timer, next *time.Time, now time.Time) {
	timer.Stop()
	if next == nil {
		return
	}
	delay := next.Sub(now)
	if delay < 0 {
		delay = 0
	}
	timer.Reset(delay)
}

func (f *Fleet) poisonFor(nodeID string) *PoisonBatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	poison, ok := f.stuck[nodeID]
	if !ok {
		return nil
	}
	return &PoisonBatch{Instance: poison.instance, Seq: poison.seq}
}

// enqueue queues a message for the stream goroutine. A full queue means the agent does not keep up: drop the stream
// (it reconnects and resyncs) rather than block the fleet.
func (s *session) enqueue(m *agentv1.ConnectResponse) bool {
	select {
	case s.out <- m:
		return true
	default:
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
	state := SessionState{Version: sessionStateVersion, NodeID: id, OwnerGeneration: owner, Poison: f.poisonFor(id)}
	if pc.serial != "" {
		state.PeerCertSerial = pc.serial
	}
	if !pc.notAfter.IsZero() {
		state.PeerCertNotAfter = pc.notAfter
	}
	var sidecar SessionSidecar
	opened, err := core.Step(sctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: f.now()})
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	delay := time.Duration(0)
	if opened.NextAlarm != nil {
		delay = opened.NextAlarm.Sub(f.now())
	}
	if delay < 0 {
		delay = 0
	}
	alarmTimer := time.NewTimer(delay)
	defer alarmTimer.Stop()
	var first *agentv1.ConnectRequest
	select {
	case m, ok := <-msgs:
		if !ok {
			return endErr()
		}
		first = m
	case alarmAt := <-alarmTimer.C:
		now := f.now()
		if alarmAt.After(now) {
			now = alarmAt
		}
		tr, alarmErr := core.Step(sctx, &state, &sidecar, SessionEvent{Kind: EventAlarm, At: now})
		resetSessionAlarm(alarmTimer, tr.NextAlarm, now)
		if alarmErr == nil && tr.Close != nil {
			return sessionCloseError(tr.Close)
		}
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("no Hello"))
	case <-sctx.Done():
		return errOrCause(sctx)
	}

	if sctx.Err() != nil {
		return errOrCause(sctx)
	}
	if !f.ownsSession(id, owner) {
		return connect.NewError(connect.CodeAborted, errors.New("superseded by a newer stream"))
	}
	helloAt := f.now()
	tr, stepErr := core.Step(sctx, &state, &sidecar, SessionEvent{Kind: EventHello, At: helloAt, Frame: first})
	resetSessionAlarm(alarmTimer, tr.NextAlarm, helloAt)
	if tr.Close != nil && state.InstanceID == "" {
		return sessionCloseError(tr.Close)
	}
	if stepErr != nil && state.InstanceID == "" {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if state.InstanceID == "" {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	s := &session{f: f, nodeID: id, owner: owner, caps: slices.Clone(state.Capabilities), ctx: sctx, cancel: cancel,
		done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		cmds: map[string]chan *agentv1.CommandResult{}, docs: map[string]chan *agentv1.DoctorReport{},
		logs: map[string]*logSub{}, core: core, coreState: state, coreSidecar: sidecar, alarmTimer: alarmTimer}
	s.coreMu.Lock()
	s.publishView()
	if !f.register(s) {
		s.coreMu.Unlock()
		return connect.NewError(connect.CodeAborted, errors.New("superseded by a newer stream"))
	}
	helloErr := s.enqueueFrames(tr.Frames)
	s.coreMu.Unlock()
	defer func() {
		cancel(nil)
		if f.unregister(s) {
			// the end is written to the store (NodeDisconnected) before the waiters are released: bound it
			dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := s.stepCore(dctx, SessionEvent{Kind: EventDisconnected, At: f.now()}); err != nil {
				f.log.Warn("disconnect session core", "node", id, "err", err)
			}
			dcancel()
		}
		close(s.done)
	}()
	if helloErr != nil {
		f.log.Error("hello response", "node", id, "err", helloErr)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	s.dispatchCoreEffects(sctx, tr.Effects)
	// Core-mediated admin frames wait until HelloAck is queued; Retire remains a direct send.
	tr, err = s.stepDesired(sctx)
	if err != nil {
		return errOrCause(sctx)
	}
	if tr.Close != nil {
		return sessionCloseError(tr.Close)
	}

	for {
		select {
		case <-sctx.Done():
			var ce *connect.Error
			if errors.As(context.Cause(sctx), &ce) && ce.Code() == connect.CodeAborted {
				_, _ = s.stepCore(sctx, SessionEvent{Kind: EventOwnerSuperseded, At: f.now()})
			}
			return errOrCause(sctx)
		case alarmAt := <-alarmTimer.C:
			now := f.now()
			if alarmAt.After(now) {
				now = alarmAt
			}
			tr, err := s.stepCore(sctx, SessionEvent{Kind: EventAlarm, At: now})
			if err != nil {
				return errOrCause(sctx)
			}
			if tr.Close != nil {
				return sessionCloseError(tr.Close)
			}
		case m := <-s.out:
			// Only this goroutine writes to the stream (after the handler returns nobody may).
			if err := stream.Send(m); err != nil {
				return err
			}
		case m, ok := <-msgs:
			if !ok {
				return endErr()
			}
			tr, err := s.stepCore(sctx, SessionEvent{Kind: EventAgentFrame, At: f.now(), Frame: m})
			if err != nil {
				return errOrCause(sctx)
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
// A failed step or a close ends the session itself (endOnError, the step's cancel), so there is no result to return.
func (f *Fleet) reconcile(ctx context.Context, s *session) {
	_, _ = s.stepDesired(ctx)
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
	s.waitMu.Lock()
	s.cmds[id] = ch
	s.waitMu.Unlock()
	defer func() {
		s.waitMu.Lock()
		delete(s.cmds, id)
		s.waitMu.Unlock()
	}()
	requestAt := s.f.now()
	tr, err := s.stepCore(ctx, SessionEvent{Kind: EventAdminCommand, At: requestAt, Request: &AdminRequest{
		RequestID: id, Deadline: requestAt.Add(wait), Frame: build(id), Kind: PendingCommand,
	}})
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
	s.waitMu.Lock()
	ch := s.cmds[r.RequestId]
	s.waitMu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}

func (s *session) deliverLog(c *agentv1.LogChunk) {
	s.waitMu.Lock()
	sub := s.logs[c.RequestId]
	s.waitMu.Unlock()
	if sub == nil {
		return
	}
	select {
	case sub.ch <- c:
	default:
		sub.dropped.Add(uint32(len(c.Lines)))
	}
}
