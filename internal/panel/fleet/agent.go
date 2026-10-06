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
	"github.com/mistgate/mistgate/internal/panel/protocols"
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
	instance string
	caps     []string // Hello.capabilities: optional features of this agent build (health.go)
	ctx      context.Context
	cancel   context.CancelCauseFunc
	done     chan struct{} // closed when the Connect handler returns
	out      chan *agentv1.ConnectResponse
	liveness atomic.Int64 // nanoseconds of silence tolerated

	// Desired-state bookkeeping: what the agent holds as far as we know. Guarded by desMu, which also
	// serializes pushes to this node.
	desMu        sync.Mutex
	sent         *nodeState
	sentRev      uint64
	sentSettings string
	driftResent  bool

	// Live data shown in the UI. Guarded by liveMu.
	liveMu    sync.Mutex
	metrics   *agentv1.HostMetrics
	metricsAt time.Time // panel receive time of the newest host metrics sample
	health    []*agentv1.InboundHealth
	online    []onlineSess
	userDown  map[string]uint64 // bits/s per user, from the newest batch
	userUp    map[string]uint64
	lastEnd   int64
	lastSeen  time.Time
	drift     bool
	cmds      map[string]chan *agentv1.CommandResult
	docs      map[string]chan *agentv1.DoctorReport // RunDoctor requests in flight (health.go)
	logs      map[string]*logSub

	// Only touched by the stream's own goroutine.
	ackPending, ackSent uint64
	lastAck             time.Time
	lastReject          time.Time         // last stats_rejected event
	l3                  l3Intake          // what of the AWG / WARP health was persisted last (l3.go)
	certSeen            map[string]string // inbound id -> "pin/notAfter" last written by certStats
}

func (s *session) livenessDur() time.Duration { return time.Duration(s.liveness.Load()) }

func (s *session) touch(now time.Time) {
	s.liveMu.Lock()
	s.lastSeen = now
	s.liveMu.Unlock()
}

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

func (s *session) markAck(seq uint64, now time.Time) {
	s.ackPending = max(s.ackPending, seq)
	if now.Sub(s.lastAck) >= ackEvery {
		s.flushAck(now)
	}
}

func (s *session) flushAck(now time.Time) {
	if s.ackPending > s.ackSent {
		s.enqueue(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Ack{Ack: &agentv1.Ack{UpToSeq: s.ackPending}}})
		s.ackSent, s.lastAck = s.ackPending, now
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

	var first *agentv1.ConnectRequest
	select {
	case m, ok := <-msgs:
		if !ok {
			return endErr()
		}
		first = m
	case <-time.After(helloTimeout):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("no Hello"))
	case <-sctx.Done():
		return errOrCause(sctx)
	}
	hello := first.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the first message must be Hello"))
	}
	if hello.ApiVersion != APIVersion {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("unsupported api_version %d, this panel serves %d..%d", hello.ApiVersion, APIVersion, APIVersion))
	}
	if hello.InstanceId == "" || len(hello.InstanceId) > 64 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("instance_id is required (random per agent process, at most 64 bytes)"))
	}
	if !f.ownsSession(id, owner) {
		return connect.NewError(connect.CodeAborted, errors.New("superseded by a newer stream"))
	}
	if hello.NodeId != "" && hello.NodeId != id {
		f.log.Warn("hello names another node than the certificate", "cert_node", id)
	}

	now := f.now().UTC()
	prev, acked, err := f.st.NodeHello(ctx, id, helloInfo(hello), now)
	if errors.Is(err, store.ErrNodeRetired) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("node retired"))
	}
	if err != nil {
		f.log.Error("hello", "node", id, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	node, err := f.st.Node(ctx, id)
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	s := &session{f: f, nodeID: id, owner: owner, instance: hello.InstanceId, ctx: sctx, cancel: cancel,
		done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		userDown: map[string]uint64{}, userUp: map[string]uint64{},
		cmds: map[string]chan *agentv1.CommandResult{}, docs: map[string]chan *agentv1.DoctorReport{},
		logs: map[string]*logSub{}, lastSeen: now, caps: capabilities(hello)}
	s.liveness.Store(int64(time.Duration(node.LivenessTimeoutS) * f.unit))
	if !f.register(s) {
		return connect.NewError(connect.CodeAborted, errors.New("superseded by a newer stream"))
	}
	defer func() {
		cancel(nil)
		close(s.done)
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

	f.connectEvents(ctx, id, prev, helloInfo(hello).BootAt, now)
	if prev.State == "pending" && node.BandwidthMbps == 0 { // the first start after the enrollment: measure the link once
		f.autoMeasureBandwidth(s)
	}
	s.enqueue(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HelloAck{HelloAck: &agentv1.HelloAck{
		AckedSeq: acked, ServerTimeUnix: now.Unix(), Settings: nodeSettings(node, s.caps), LinkSupported: true}}})
	if err := f.reconcile(sctx, s, reconcileConnect, hello); err != nil {
		f.log.Error("initial desired state", "node", id, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	live := time.NewTimer(s.livenessDur())
	defer live.Stop()
	ackT := time.NewTicker(ackEvery)
	defer ackT.Stop()
	// A certificate is checked at the handshake only; a stream can live for weeks. Close it when its
	// certificate expires or is revoked (Renew schedules the old one for revocation, re-enroll and retire
	// revoke at once): the agent reconnects with its current certificate, or is locked out if it has none.
	certT := time.NewTicker(f.certCheck)
	defer certT.Stop()
	for {
		select {
		case <-sctx.Done():
			return errOrCause(sctx)
		case <-certT.C:
			if err := f.recheckCert(ctx, pc); err != nil {
				return connect.NewError(connect.CodeUnauthenticated, err)
			}
		case <-live.C:
			return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("no message from the agent for %d s", int(s.livenessDur().Seconds())))
		case <-ackT.C:
			s.flushAck(f.now())
		case m := <-s.out:
			// Only this goroutine writes to the stream (after the handler returns nobody may).
			if err := stream.Send(m); err != nil {
				return err
			}
		case m, ok := <-msgs:
			if !ok {
				return endErr()
			}
			s.touch(f.now())
			live.Reset(s.livenessDur())
			if err := f.handle(sctx, s, m); err != nil {
				return err
			}
		}
	}
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

func (f *Fleet) handle(ctx context.Context, s *session, m *agentv1.ConnectRequest) error {
	switch {
	case m.GetHello() != nil:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unexpected Hello"))
	case m.GetStats() != nil:
		return f.onStats(ctx, s, m.Seq, m.GetStats())
	case m.GetEvent() != nil:
		return f.onEvent(ctx, s, m.Seq, m.GetEvent())
	case m.GetApplyResult() != nil:
		f.onApply(ctx, s, m.GetApplyResult())
	case m.GetCommandResult() != nil:
		s.deliverCommand(m.GetCommandResult())
	case m.GetLogChunk() != nil:
		s.deliverLog(m.GetLogChunk())
	case m.GetDoctorReport() != nil:
		f.onDoctorReport(ctx, s, m.GetDoctorReport())
	}
	return nil // Pong and unknown messages only count as liveness
}

// bucketHour picks the hourly bucket of a batch from its (corrected) end time: timestamps more than 5 min
// ahead are clamped to the receive time, batches older than 48 h are attributed to the receive hour.
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

func (f *Fleet) onStats(ctx context.Context, s *session, seq uint64, st *agentv1.StatsBatch) error {
	now := f.now().UTC()
	hour, stale := bucketHour(time.Unix(st.IntervalEndUnix, 0), now)
	g := f.guardStats(st)
	if g.rejected > 0 {
		f.rejectStats(s, g, now)
	}
	in := store.FleetStatsIn{NodeID: s.nodeID, Instance: s.instance, Seq: seq, Now: now, HourStart: hour, Traffic: g.traffic}
	for i, se := range st.Sessions {
		if i == maxStatsDeltas {
			break
		}
		in.Sessions = append(in.Sessions, store.FleetSessionRef{CredID: se.CredId, InboundID: se.InboundId})
	}
	out, err := f.st.IngestStats(ctx, in)
	if err != nil {
		// Do not ack and do not go on: a later ack would cover this seq and lose the batch. The agent
		// reconnects and resends from HelloAck.acked_seq. But a batch the database refuses twice will be
		// refused every time, and resending it would wedge this node's stats for good: drop it then.
		f.log.Error("ingest stats", "node", s.nodeID, "err", err)
		if !f.stuckStats(s, seq) {
			return connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		f.log.Error("stats batch refused by the database twice, dropped", "node", s.nodeID, "seq", seq)
		f.event(ctx, 3, "stats_dropped", s.nodeID, map[string]string{"reason": "database_refused", "seq": fmt.Sprint(seq)})
		if seq != 0 {
			if err := f.st.SkipSeq(ctx, s.nodeID, s.instance, seq, now); err != nil {
				return connect.NewError(connect.CodeInternal, errors.New("internal error"))
			}
			s.markAck(seq, now)
		}
		return nil
	}
	f.unstickStats(s)
	if !out.Duplicate {
		s.applySnapshot(st, g.traffic, now, out.Refs)
		f.l3Stats(ctx, s, st, now) // AwgHealth of the inbounds, WarpHealth of the node
		f.certStats(ctx, s, st, now)
		f.touchAwgDevices(ctx, st, out.Refs, now)
		if out.Skipped > 0 {
			f.log.Warn("stats for unknown credentials or foreign inbounds dropped", "node", s.nodeID, "count", out.Skipped)
		}
		if stale {
			f.event(ctx, 2, "stats_stale", s.nodeID, map[string]string{"age_hours": fmt.Sprint(int(now.Sub(time.Unix(st.IntervalEndUnix, 0)).Hours()))})
		}
		if f.cfg.OnUsage != nil && len(out.Users) > 0 {
			f.cfg.OnUsage(ctx, out.Users) // the access module recomputes quota status and calls StateChanged
		}
	}
	if seq != 0 {
		s.markAck(seq, now)
	}
	return nil
}

// applySnapshot replaces the live view with the newest batch (sessions are an absolute snapshot).
func (s *session) applySnapshot(st *agentv1.StatsBatch, traffic []store.FleetTraffic, now time.Time, refs map[string]store.FleetCredRef) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	// An end time from the future would make every later (honest) batch look "older" and freeze the live
	// view: clamp it like the bucket hour.
	end := st.IntervalEndUnix
	if end > now.Add(maxFuture).Unix() {
		end = now.Unix()
	}
	if end < s.lastEnd {
		return // an older batch resent after a reconnect
	}
	s.lastEnd = end
	if st.Host != nil {
		s.metrics = st.Host
		s.metricsAt = now
	}
	s.health = st.Health
	s.online = s.online[:0]
	for i, se := range st.Sessions {
		ref, ok := refs[se.CredId]
		if !ok || i == maxStatsDeltas {
			continue
		}
		since := time.Unix(se.ConnectedAtUnix, 0).UTC()
		if se.ConnectedAtUnix <= 0 || since.After(now) {
			since = now
		}
		s.online = append(s.online, onlineSess{userID: ref.UserID, deviceID: ref.DeviceID, protocol: ref.Protocol,
			inboundID: se.InboundId, remoteIP: clip(se.RemoteIp, 64), since: since})
	}
	// "Current speed" is the average over the newest batch (about 10 s), not a 60 s window.
	secs := uint64(min(max(1, end-st.IntervalStartUnix), int64(maxBatchSpan/time.Second)))
	s.userDown, s.userUp = map[string]uint64{}, map[string]uint64{}
	for _, d := range traffic { // only what passed the sanity check, so the speed cannot be faked either
		if ref, ok := refs[d.CredID]; ok {
			s.userDown[ref.UserID] += d.Down * 8 / secs
			s.userUp[ref.UserID] += d.Up * 8 / secs
		}
	}
}

func (f *Fleet) onEvent(ctx context.Context, s *session, seq uint64, ev *agentv1.Event) error {
	now := f.now().UTC()
	t := time.Unix(ev.TimeUnix, 0).UTC()
	if ev.TimeUnix <= 0 || t.After(now.Add(maxFuture)) {
		t = now
	}
	sev := int(ev.Severity)
	if sev < 1 || sev > 3 {
		sev = 1
	}
	code := ev.Code
	if code == "" {
		code = "agent_event"
	}
	row := store.EventRow{Time: t, Severity: sev, Code: store.Clip(code, 64), InboundID: store.Clip(ev.InboundId, 64)}
	if row.Code == "torrent_attempt" {
		row.Params = map[string]string{}
		if userID := store.Clip(ev.Params["user_id"], 256); userID != "" {
			row.Params["user_id"] = userID
			if user, err := f.st.Access().User(ctx, userID); err == nil && user.Name != "" {
				row.Params["user_name"] = store.Clip(user.Name, 256)
			}
		}
		// This event has a fixed, non-secret contract. Do not persist arbitrary params such as a credential by mistake.
		// Nor any address: events reach helpers, API tokens and MCP, while a client's address is for the owner alone
		// (live logs), and the destination would say where a person went. The user, the inbound and the protocol stay.
		for _, k := range []string{"protocol", "torrent_protocol"} {
			if v := ev.Params[k]; v != "" {
				row.Params[k] = store.Clip(v, 256)
			}
		}
	} else if len(ev.Params) > 0 {
		row.Params = map[string]string{}
		for k, v := range ev.Params {
			if len(row.Params) == maxEventParam {
				break
			}
			row.Params[store.Clip(k, 64)] = store.Clip(v, 256)
		}
	}
	dup, err := f.st.IngestEvent(ctx, s.nodeID, s.instance, seq, now, row)
	if err != nil {
		f.log.Error("ingest event", "node", s.nodeID, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !dup && row.Code == "update_committed" {
		f.recordCommit(ctx, s.nodeID, row)
	}
	if !dup {
		f.onAwgPrepareEvent(ctx, s.nodeID, row) // the build of the AmneziaWG kernel module (awgprep.go)
	}
	if seq != 0 {
		s.markAck(seq, now)
	}
	f.onWarpEvent(s, ev) // a node that asks the panel to look at its WARP account (l3.go)
	return nil
}

// maxCertLife bounds the expiry an agent may report: the self-signed certificates the panel makes live 10 years.
const maxCertLife = 11 * 365 * 24 * time.Hour

// certStats keeps the certificate each inbound serves, as the stats stream reports it: an ACME certificate is issued after
// the ApplyResult was sent and renewed later, so the apply-time value alone stays empty. Only a well-formed pin is taken
// (it ends up in subscriptions of self-signed inbounds); a database failure is logged, the next batch tries again.
// An expiry beyond maxCertLife is not believed (the health check would never warn): the stored value stays.
func (f *Fleet) certStats(ctx context.Context, s *session, st *agentv1.StatsBatch, now time.Time) {
	for _, h := range st.Health {
		if h.CertPinSha256 == "" || h.CertNotAfterUnix <= 0 || h.CertNotAfterUnix > now.Add(maxCertLife).Unix() {
			continue
		}
		pin, ok := protocols.NormalizePin(h.CertPinSha256)
		if !ok {
			continue
		}
		key := pin + "/" + fmt.Sprint(h.CertNotAfterUnix)
		if s.certSeen[h.InboundId] == key {
			continue
		}
		if err := f.st.SetInboundCert(ctx, s.nodeID, h.InboundId, pin, time.Unix(h.CertNotAfterUnix, 0).UTC(), now); err != nil {
			f.log.Warn("store inbound certificate", "node", s.nodeID, "inbound", h.InboundId, "err", err)
			continue
		}
		if s.certSeen == nil {
			s.certSeen = map[string]string{}
		}
		s.certSeen[h.InboundId] = key
	}
}

// clip bounds text that comes from a node on a rune boundary and as valid UTF-8 (see store.Clip).
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
func (f *Fleet) onApply(ctx context.Context, s *session, r *agentv1.ApplyResult) {
	switch r.Status {
	case agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH:
		f.log.Info("agent reports base mismatch, resending full state", "node", s.nodeID, "revision", r.Revision)
		if err := f.reconcile(ctx, s, reconcileFull, nil); err != nil {
			f.log.Warn("full resend", "node", s.nodeID, "err", err)
		}
		return
	case agentv1.ApplyStatus_APPLY_STATUS_REJECTED:
		f.event(ctx, 3, "apply_rejected", s.nodeID, map[string]string{"revision": fmt.Sprint(r.Revision), "error": store.Clip(r.Error, 256)})
		return
	case agentv1.ApplyStatus_APPLY_STATUS_APPLIED, agentv1.ApplyStatus_APPLY_STATUS_PARTIAL:
	default:
		return
	}
	var in []store.InboundApplied
	for _, ir := range r.Inbounds {
		ia := store.InboundApplied{ID: ir.InboundId, State: runState(ir.State), Error: clip(ir.Error, 512), SpecHash: clip(ir.SpecHash, 128)}
		if ir.CertPinSha256 != "" {
			// The pin ends up in every subscription of the inbound: only a real SHA-256 gets there.
			if pin, ok := protocols.NormalizePin(ir.CertPinSha256); ok {
				ia.CertPin = pin
			} else {
				f.log.Warn("node reported a malformed certificate pin, ignored", "node", s.nodeID, "inbound", ir.InboundId)
			}
		}
		if ir.CertNotAfterUnix > 0 {
			ia.CertNotAfter = time.Unix(ir.CertNotAfterUnix, 0).UTC()
		}
		in = append(in, ia)
	}
	s.desMu.Lock() // the inbounds this agent was not sent are failed, with the reason, so the node page says why
	if s.sent != nil {
		in = append(in, withheldApplied(s.sent.withheld)...)
	}
	s.desMu.Unlock()
	if err := f.st.NodeApplied(ctx, s.nodeID, r.Revision, r.StateHash, in, f.now().UTC()); err != nil {
		f.log.Warn("record apply result", "node", s.nodeID, "err", err)
	}
	s.certSeen = nil // the result just overwrote the stored certificate: let the next stats batch restore it
	if r.Status != agentv1.ApplyStatus_APPLY_STATUS_APPLIED {
		return // a failed inbound explains a hash difference; the node page shows the failure
	}
	s.desMu.Lock()
	if s.sent == nil || r.Revision != s.sentRev { // stale answer: a newer state is on its way
		s.desMu.Unlock()
		return
	}
	match := r.StateHash == s.sent.hash
	resend := false
	switch {
	case match:
		s.driftResent = false
		s.liveMu.Lock()
		s.drift = false
		s.liveMu.Unlock()
	case !s.driftResent:
		s.driftResent, resend = true, true
		f.event(ctx, 2, "state_drift", s.nodeID, map[string]string{"revision": fmt.Sprint(r.Revision)})
	default:
		s.liveMu.Lock()
		s.drift = true
		s.liveMu.Unlock()
		f.event(ctx, 3, "state_drift", s.nodeID, map[string]string{"revision": fmt.Sprint(r.Revision), "persists": "true"})
	}
	s.desMu.Unlock()
	if resend {
		if err := f.reconcile(ctx, s, reconcileFull, nil); err != nil {
			f.log.Warn("full resend after drift", "node", s.nodeID, "err", err)
		}
	}
}

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
func (f *Fleet) reconcile(ctx context.Context, s *session, mode reconcileMode, hello *agentv1.Hello) error {
	s.desMu.Lock()
	defer s.desMu.Unlock()
	node, err := f.st.Node(ctx, s.nodeID)
	if err != nil {
		return err
	}
	if node.State == "retired" {
		return nil // RetireNode closes the stream
	}
	s.liveness.Store(int64(time.Duration(node.LivenessTimeoutS) * f.unit))
	want, err := f.buildState(ctx, node, s.caps)
	if err != nil {
		return err
	}
	settings := nodeSettings(node, s.caps)
	sig := settingsSig(settings)
	if len(want.withheld) > 0 {
		// Nothing is sent for these, so no ApplyResult will say they failed (onApply does it for the ones that are there).
		if err := f.st.FailWithheldInbounds(ctx, node.ID, want.withheld, withheldReason, f.now().UTC()); err != nil {
			f.log.Warn("mark withheld inbounds", "node", node.ID, "err", err)
		}
	}

	if mode == reconcileConnect {
		if hello.AppliedStateHash != "" && hello.AppliedStateHash == want.hash && hello.AppliedRevision > 0 {
			// The agent already runs exactly this state (restored from disk); nothing to send.
			s.sent, s.sentRev, s.sentSettings = want, hello.AppliedRevision, sig
			rev := max(node.DesiredRevision, hello.AppliedRevision)
			return f.st.NodeDesired(ctx, node.ID, rev, want.hash)
		}
		mode = reconcileFull
	}
	if s.sent == nil && mode == reconcileChange {
		return nil // the connect-time sync has not run yet and will build the current state itself
	}
	if s.sent == nil || s.sentRev == 0 {
		mode = reconcileFull
	}
	// A delta cannot say "remove WARP" (absence in a delta means "unchanged"): an account that is gone goes as a full state.
	if mode == reconcileChange && s.sent.warp != nil && want.warp == nil {
		mode = reconcileFull
	}
	if mode == reconcileChange && want.hash == s.sent.hash && sig == s.sentSettings {
		return nil
	}
	rev := max(node.DesiredRevision, s.sentRev) + 1
	ds := &agentv1.DesiredState{Revision: rev, StateHash: want.hash, Settings: settings}
	if mode == reconcileFull {
		ds.Inbounds = fullInbounds(want)
		ds.Warp = warpProto(want.warp) // absent in a full state = "this node has no WARP"
	} else {
		ds.BaseRevision = s.sentRev
		ds.Inbounds, ds.RemovedInboundIds = diffState(s.sent, want)
		if !sameWarp(s.sent.warp, want.warp) { // present replaces the whole configuration; (nil is handled above: full state)
			ds.Warp = warpProto(want.warp)
		}
	}
	if err := f.st.NodeDesired(ctx, node.ID, rev, want.hash); err != nil {
		return err
	}
	if !s.enqueue(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_DesiredState{DesiredState: ds}}) {
		return errors.New("agent queue full")
	}
	s.sent, s.sentRev, s.sentSettings = want, rev, sig
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
	if !s.enqueue(build(id)) {
		return nil, errLinkLost
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
